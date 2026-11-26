"""
Self-renewing KodeKloud authentication (email + password).

KodeKloud's ``learn-api`` accepts a Firebase **ID token** as a Bearer token.
ID tokens only live for ~1 hour, so :class:`FirebaseAuth` keeps one valid for
the whole run:

* it signs in with email/password through the Firebase Identity Toolkit
  REST API, and
* renews the ID token with the Firebase **refresh token** through the Secure
  Token API shortly before it expires (or after a 401), falling back to a
  fresh password sign-in if the refresh token stops working.

The refresh token is cached on disk with ``0600`` permissions so later runs
need no password. The password itself is never written anywhere. Tokens are
only ever sent to KodeKloud hosts (see :func:`is_kodekloud_url`).
"""

import json
import logging
import os
import threading
import time
import urllib.parse
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Dict, Optional, Protocol

import requests

logger = logging.getLogger(__name__)

# Public web API key of KodeKloud's Firebase project, as shipped in the
# learn.kodekloud.com JS bundle (verified 2026-11). Overridable in case it
# rotates.
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
    "INVALID_EMAIL": "the email address is malformed",
    "USER_DISABLED": "this account is disabled",
    "TOO_MANY_ATTEMPTS_TRY_LATER": "too many attempts, try again later",
    "PASSWORD_LOGIN_DISABLED": "KodeKloud disabled password sign-in",
    "MISSING_RECAPTCHA_TOKEN": "KodeKloud now requires reCAPTCHA for sign-in",
    "TOKEN_EXPIRED": "the cached session expired, sign in again",
    "INVALID_REFRESH_TOKEN": "the cached session is invalid, sign in again",
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


@dataclass
class AuthState:
    refresh_token: str
    id_token: str = ""
    expires_at: float = 0.0
    email: Optional[str] = None


class FirebaseAuth:
    """Keeps a fresh Firebase ID token using email/password + refresh token."""

    def __init__(
        self,
        email: Optional[str] = None,
        password: Optional[str] = None,
        state_path: Optional[Path] = DEFAULT_STATE_PATH,
        api_key: Optional[str] = None,
    ) -> None:
        self.email = email
        self._password = password
        self.state_path = Path(state_path) if state_path else None
        self._api_key = api_key or firebase_api_key()
        self._lock = threading.Lock()
        self._state: Optional[AuthState] = self._load_state()
        if self._state and not self.email:
            self.email = self._state.email

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

    @property
    def has_refresh_token(self) -> bool:
        return bool(self._state and self._state.refresh_token)

    @property
    def has_password(self) -> bool:
        return bool(self.email and self._password)

    @property
    def has_credentials(self) -> bool:
        return self.has_refresh_token or self.has_password

    def set_password(self, password: str) -> None:
        self._password = password

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
            raise AuthError(f"Session renewal failed: {_firebase_error(resp)}")
        data = resp.json()
        self._state.refresh_token = data.get("refresh_token", self._state.refresh_token)
        self._state.id_token = data["id_token"]
        self._state.expires_at = time.time() + int(data.get("expires_in", 3600))
        self._save_state()
        logger.info("Renewed KodeKloud session token")

    def _renew(self) -> None:
        """Get a new ID token: refresh token first, then password sign-in."""
        errors = []
        if self.has_refresh_token:
            try:
                self._refresh_with_token()
                return
            except (AuthError, requests.RequestException) as e:
                errors.append(str(e))
                logger.warning("%s; signing in again", e)
        if self.has_password:
            try:
                self._sign_in_with_password()
                return
            except (AuthError, requests.RequestException) as e:
                errors.append(str(e))
        if not errors:
            errors.append("no cached session, sign in with --email/--password")
        raise AuthError("; ".join(errors))

    # ------------------------------------------------------------------ public
    def get_token(self) -> str:
        with self._lock:
            if (
                self._state is None
                or not self._state.id_token
                or time.time() >= self._state.expires_at - REFRESH_LEEWAY_SECONDS
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
