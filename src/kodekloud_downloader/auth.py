"""
Self-renewing KodeKloud authentication.

KodeKloud's ``learn-api`` accepts a Firebase **ID token** as a Bearer token.
ID tokens only live for ~1 hour, so long downloads used to die half-way
with a 401. This module keeps a valid ID token around for the whole run:

* :class:`FirebaseAuth` signs in with email/password through the Firebase
  Identity Toolkit REST API, and/or renews the ID token with a Firebase
  **refresh token** through the Secure Token API. A refresh token can come
  from a password sign-in, from the cookie/browser flows (it is stored next
  to the ID token in the site's IndexedDB) or from the on-disk cache, so
  auto-renewal also works for users who never type a password.
* :class:`StaticTokenAuth` wraps a pasted ``--token``. It cannot renew;
  a 401 produces a clear error instead.

Tokens are only ever sent to KodeKloud hosts (see :func:`is_kodekloud_url`)
and the refresh-token cache is written with ``0600`` permissions. The
password is never written to disk.
"""

import base64
import json
import logging
import os
import threading
import time
import urllib.parse
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Callable, Dict, Optional, Protocol

import requests

logger = logging.getLogger(__name__)

# Public web API key of KodeKloud's Firebase project, as shipped in the
# learn.kodekloud.com JS bundle (verified 2026-11). Overridable in case it
# rotates; the cookie/browser flows also pick it up from IndexedDB.
DEFAULT_FIREBASE_API_KEY = "AIzaSyAVy3_TcBija6Pc9_-glfSZuqft01zgoSA"

SIGN_IN_URL = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword"
REFRESH_URL = "https://securetoken.googleapis.com/v1/token"

# Firebase API keys may be HTTP-referrer restricted; present ourselves the
# same way the web app does.
_FIREBASE_HEADERS = {
    "Origin": "https://learn.kodekloud.com",
    "Referer": "https://learn.kodekloud.com/",
}

DEFAULT_STATE_PATH = Path.home() / ".cache" / "kodekloud-downloader" / "auth.json"
REFRESH_LEEWAY_SECONDS = 300
HTTP_TIMEOUT = 30

_FIREBASE_ERROR_HINTS = {
    "INVALID_LOGIN_CREDENTIALS": "wrong email or password",
    "INVALID_PASSWORD": "wrong email or password",
    "EMAIL_NOT_FOUND": "wrong email or password",
    "USER_DISABLED": "this account is disabled",
    "TOO_MANY_ATTEMPTS_TRY_LATER": "too many attempts, try again later",
    "PASSWORD_LOGIN_DISABLED": (
        "KodeKloud disabled password sign-in for this API; "
        "use --cookie or --browser instead (auto-renewal still works)"
    ),
    "MISSING_RECAPTCHA_TOKEN": (
        "KodeKloud now requires reCAPTCHA for password sign-in; "
        "use --cookie or --browser once instead (auto-renewal still works)"
    ),
    "TOKEN_EXPIRED": "the refresh token expired, sign in again",
    "INVALID_REFRESH_TOKEN": "the refresh token is invalid, sign in again",
    "USER_NOT_FOUND": "the account no longer exists",
}


class AuthError(Exception):
    """Raised when no valid KodeKloud token can be obtained."""


def firebase_api_key() -> str:
    return os.environ.get("KODEKLOUD_FIREBASE_API_KEY") or DEFAULT_FIREBASE_API_KEY


def is_kodekloud_url(url: str) -> bool:
    """True only for https URLs on kodekloud.com or one of its subdomains."""
    try:
        parsed = urllib.parse.urlparse(url)
    except ValueError:
        return False
    host = (parsed.hostname or "").lower()
    return parsed.scheme == "https" and (
        host == "kodekloud.com" or host.endswith(".kodekloud.com")
    )


def jwt_claims(token: str) -> Dict[str, object]:
    """Decode a JWT payload without verifying it (only used for metadata)."""
    try:
        payload = token.split(".")[1]
        payload += "=" * (-len(payload) % 4)
        claims = json.loads(base64.urlsafe_b64decode(payload))
        return claims if isinstance(claims, dict) else {}
    except (IndexError, ValueError, TypeError):
        return {}


def jwt_expiry(token: str) -> Optional[float]:
    """Return the ``exp`` claim of a JWT (epoch seconds), if any."""
    exp = jwt_claims(token).get("exp")
    try:
        return float(exp) if exp else None  # type: ignore[arg-type]
    except (TypeError, ValueError):
        return None


def _firebase_error(resp: requests.Response) -> str:
    try:
        code = resp.json()["error"]["message"]
    except (ValueError, KeyError, TypeError):
        return f"HTTP {resp.status_code}"
    key = str(code).split(" ")[0].split(":")[0]
    hint = _FIREBASE_ERROR_HINTS.get(key)
    return f"{code} ({hint})" if hint else str(code)


class AuthProvider(Protocol):
    def get_token(self) -> str: ...

    def force_refresh(self) -> bool: ...


def auth_headers(auth: AuthProvider, url: str) -> Dict[str, str]:
    """Authorization header for ``url`` - empty for any non-KodeKloud host."""
    if not is_kodekloud_url(url):
        return {}
    return {"Authorization": f"Bearer {auth.get_token()}"}


class StaticTokenAuth:
    """A fixed ID token (``--token``). Cannot be renewed."""

    def __init__(self, token: str) -> None:
        self._token = token
        exp = jwt_expiry(token)
        if exp is not None:
            remaining = exp - time.time()
            if remaining <= 0:
                logger.warning("The provided token has already expired.")
            else:
                logger.info(
                    "Provided token expires in %d min and cannot be renewed; "
                    "use --email/--password or --cookie for long downloads.",
                    remaining // 60,
                )

    def get_token(self) -> str:
        return self._token

    def force_refresh(self) -> bool:
        return False


@dataclass
class AuthState:
    refresh_token: str
    id_token: str = ""
    expires_at: float = 0.0
    email: Optional[str] = None
    api_key: Optional[str] = None


# Signature of a callable that can produce brand-new tokens without a
# password (e.g. re-running the cookie exchange). Returns a dict with
# ``id_token`` and optionally ``refresh_token`` / ``expires_at`` / ``api_key``.
TokenSource = Callable[[], Optional[Dict[str, object]]]


class FirebaseAuth:
    """Keeps a fresh Firebase ID token using a refresh token and/or a password."""

    def __init__(
        self,
        email: Optional[str] = None,
        password: Optional[str] = None,
        state_path: Optional[Path] = DEFAULT_STATE_PATH,
        seed: Optional[Dict[str, object]] = None,
        fallback: Optional[TokenSource] = None,
        api_key: Optional[str] = None,
    ) -> None:
        self.email = email
        self._password = password
        self.state_path = Path(state_path) if state_path else None
        self._fallback = fallback
        self._lock = threading.Lock()
        self._state: Optional[AuthState] = None

        self._api_key = api_key or firebase_api_key()
        self._state = self._load_state()
        if seed:
            self._apply_seed(seed)
        if api_key:
            self._api_key = api_key
        elif self._state and self._state.api_key:
            self._api_key = self._state.api_key
        else:
            self._api_key = firebase_api_key()

    def __repr__(self) -> str:  # never show tokens/password in logs or tracebacks
        return f"FirebaseAuth(email={self.email!r})"

    # ------------------------------------------------------------------ state
    def _load_state(self) -> Optional[AuthState]:
        if not self.state_path or not self.state_path.exists():
            return None
        try:
            data = json.loads(self.state_path.read_text())
            # Cache files from the older johnay0 fork stored the ID token as
            # "session_cookie".
            if "session_cookie" in data and "id_token" not in data:
                data["id_token"] = data.pop("session_cookie")
            known = AuthState.__dataclass_fields__
            state = AuthState(**{k: v for k, v in data.items() if k in known})
        except (ValueError, TypeError, AttributeError) as e:
            logger.warning("Ignoring unreadable auth cache %s: %s", self.state_path, e)
            return None
        if self.email and state.email and state.email.lower() != self.email.lower():
            logger.info("Cached session belongs to another account; ignoring it.")
            return None
        return state

    def _save_state(self) -> None:
        if not self.state_path or not self._state or not self._state.refresh_token:
            return
        try:
            self.state_path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.state_path.with_suffix(".tmp")
            fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, "w") as fp:
                json.dump(asdict(self._state), fp)
            os.replace(tmp, self.state_path)
            try:
                self.state_path.chmod(0o600)
            except OSError:
                pass
        except OSError as e:
            logger.warning("Could not write auth cache %s: %s", self.state_path, e)

    def _apply_seed(self, seed: Dict[str, object]) -> None:
        id_token = str(seed.get("id_token") or "")
        refresh_token = str(seed.get("refresh_token") or "")
        expires_at = seed.get("expires_at") or jwt_expiry(id_token) or 0.0
        api_key = seed.get("api_key")
        if api_key:
            self._api_key = str(api_key)
        token_email = jwt_claims(id_token).get("email")
        if not self.email and isinstance(token_email, str):
            self.email = token_email
        prev = self._state
        if (
            prev
            and prev.email
            and self.email
            and prev.email.lower() != self.email.lower()
        ):
            prev = None  # cached refresh token belongs to a different account
        self._state = AuthState(
            refresh_token=refresh_token or (prev.refresh_token if prev else ""),
            id_token=id_token,
            expires_at=float(expires_at),  # type: ignore[arg-type]
            email=self.email or (prev.email if prev else None),
            api_key=str(api_key) if api_key else (prev.api_key if prev else None),
        )
        self._save_state()

    @property
    def has_refresh_token(self) -> bool:
        return bool(self._state and self._state.refresh_token)

    @property
    def has_password(self) -> bool:
        return bool(self.email and self._password)

    def set_password(self, password: str) -> None:
        self._password = password

    @property
    def has_credentials(self) -> bool:
        return bool(
            (self._state and (self._state.refresh_token or self._state.id_token))
            or (self.email and self._password)
            or self._fallback
        )

    # --------------------------------------------------------------- firebase
    def _sign_in_with_password(self) -> None:
        resp = requests.post(
            SIGN_IN_URL,
            params={"key": self._api_key},
            json={
                "email": self.email,
                "password": self._password,
                "returnSecureToken": True,
            },
            headers=_FIREBASE_HEADERS,
            timeout=HTTP_TIMEOUT,
        )
        if not resp.ok:
            raise AuthError(f"KodeKloud sign-in failed: {_firebase_error(resp)}")
        data = resp.json()
        self._state = AuthState(
            refresh_token=data["refreshToken"],
            id_token=data["idToken"],
            expires_at=time.time() + int(data.get("expiresIn", 3600)),
            email=self.email,
            api_key=self._api_key,
        )
        self._save_state()
        logger.info("Signed in to KodeKloud as %s", self.email)

    def _refresh_with_token(self) -> None:
        assert self._state is not None
        resp = requests.post(
            REFRESH_URL,
            params={"key": self._api_key},
            data={
                "grant_type": "refresh_token",
                "refresh_token": self._state.refresh_token,
            },
            headers=_FIREBASE_HEADERS,
            timeout=HTTP_TIMEOUT,
        )
        if not resp.ok:
            raise AuthError(f"Token refresh failed: {_firebase_error(resp)}")
        data = resp.json()
        self._state.refresh_token = data.get("refresh_token", self._state.refresh_token)
        self._state.id_token = data["id_token"]
        self._state.expires_at = time.time() + int(data.get("expires_in", 3600))
        self._save_state()
        logger.info("Renewed KodeKloud session token")

    def _renew(self) -> None:
        """Get a new ID token, trying the cheapest method first."""
        errors = []
        if self._state and self._state.refresh_token:
            try:
                self._refresh_with_token()
                return
            except (AuthError, requests.RequestException) as e:
                errors.append(str(e))
                logger.warning("%s; trying other sign-in methods", e)
        if self.email and self._password:
            try:
                self._sign_in_with_password()
                return
            except (AuthError, requests.RequestException) as e:
                errors.append(str(e))
                logger.warning("%s", e)
        if self._fallback:
            seed = self._fallback()
            if seed and seed.get("id_token"):
                self._apply_seed(seed)
                logger.info("Obtained a new session token via fallback sign-in")
                return
            errors.append("fallback sign-in returned no token")
        if not errors:
            errors.append(
                "no refresh token cached and no --email/--password, "
                "--cookie or --browser given"
            )
        raise AuthError("Could not renew KodeKloud session: " + "; ".join(errors))

    # ------------------------------------------------------------------ public
    def get_token(self) -> str:
        with self._lock:
            # expires_at == 0 means "unknown": use the token until a 401.
            if (
                self._state is None
                or not self._state.id_token
                or (
                    self._state.expires_at > 0
                    and time.time() >= self._state.expires_at - REFRESH_LEEWAY_SECONDS
                )
            ):
                self._renew()
            assert self._state is not None
            return self._state.id_token

    def force_refresh(self) -> bool:
        """Renew regardless of the cached expiry (used after a 401)."""
        with self._lock:
            try:
                self._renew()
                return True
            except AuthError as e:
                logger.error("%s", e)
                return False
