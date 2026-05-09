import json
import logging
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Dict, Optional

import requests

from kodekloud_downloader.helpers import parse_token

logger = logging.getLogger(__name__)

FIREBASE_API_KEY = "AIzaSyAVy3_TcBija6Pc9_-glfSZuqft01zgoSA"
SIGN_IN_URL = (
    f"https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword"
    f"?key={FIREBASE_API_KEY}"
)
REFRESH_URL = f"https://securetoken.googleapis.com/v1/token?key={FIREBASE_API_KEY}"

DEFAULT_STATE_PATH = Path.home() / ".cache" / "kodekloud-downloader" / "auth.json"
REFRESH_LEEWAY_SECONDS = 60
HTTP_TIMEOUT = 30


class AuthError(Exception):
    pass


@dataclass
class AuthState:
    refresh_token: str
    # Bearer token sent to learn-api.kodekloud.com. This is the Firebase
    # idToken directly — the field is named `session_cookie` for backwards
    # compatibility with on-disk state files written by earlier versions.
    session_cookie: str
    expires_at: float
    email: Optional[str] = None


class KodekloudAuth:
    """Manages KodeKloud auth: Firebase sign-in, token refresh, session-cookie minting."""

    def __init__(
        self,
        email: Optional[str] = None,
        password: Optional[str] = None,
        state_path: Optional[Path] = None,
    ) -> None:
        self.email = email
        self.password = password
        self.state_path = Path(state_path) if state_path else DEFAULT_STATE_PATH
        self._state: Optional[AuthState] = self._load_state()

    def _load_state(self) -> Optional[AuthState]:
        if not self.state_path.exists():
            return None
        try:
            data = json.loads(self.state_path.read_text())
            return AuthState(**data)
        except (json.JSONDecodeError, TypeError, KeyError) as e:
            logger.warning("Could not read cached auth state at %s: %s", self.state_path, e)
            return None

    def _save_state(self, state: AuthState) -> None:
        self.state_path.parent.mkdir(parents=True, exist_ok=True)
        self.state_path.write_text(json.dumps(asdict(state)))
        try:
            self.state_path.chmod(0o600)
        except OSError:
            pass

    def _firebase_sign_in(self) -> Dict:
        if not (self.email and self.password):
            raise AuthError(
                "No cached refresh token and no email/password provided. "
                "Re-run with --email and --password (or set KODEKLOUD_EMAIL and KODEKLOUD_PASSWORD)."
            )
        resp = requests.post(
            SIGN_IN_URL,
            json={
                "email": self.email,
                "password": self.password,
                "returnSecureToken": True,
            },
            timeout=HTTP_TIMEOUT,
        )
        if not resp.ok:
            raise AuthError(
                f"Firebase sign-in failed (HTTP {resp.status_code}). "
                "Check that KODEKLOUD_EMAIL / KODEKLOUD_PASSWORD are correct."
            )
        return resp.json()

    def _firebase_refresh(self, refresh_token: str) -> Dict:
        resp = requests.post(
            REFRESH_URL,
            data={"grant_type": "refresh_token", "refresh_token": refresh_token},
            timeout=HTTP_TIMEOUT,
        )
        if not resp.ok:
            raise AuthError(f"Refresh token exchange failed (HTTP {resp.status_code})")
        return resp.json()

    def _full_sign_in(self) -> AuthState:
        data = self._firebase_sign_in()
        state = AuthState(
            refresh_token=data["refreshToken"],
            session_cookie=data["idToken"],
            expires_at=time.time() + int(data.get("expiresIn", 3600)),
            email=self.email,
        )
        self._save_state(state)
        self._state = state
        logger.info("Signed in to KodeKloud as %s", self.email)
        return state

    def _refresh(self) -> AuthState:
        assert self._state is not None
        try:
            data = self._firebase_refresh(self._state.refresh_token)
        except AuthError as e:
            logger.warning("Refresh failed (%s); attempting fresh sign-in", e)
            return self._full_sign_in()
        self._state.refresh_token = data["refresh_token"]
        self._state.session_cookie = data["id_token"]
        self._state.expires_at = time.time() + int(data.get("expires_in", 3600))
        self._save_state(self._state)
        logger.info("Refreshed KodeKloud session")
        return self._state

    def get_token(self) -> str:
        if self._state is None:
            self._full_sign_in()
        elif time.time() >= self._state.expires_at - REFRESH_LEEWAY_SECONDS:
            self._refresh()
        assert self._state is not None
        return self._state.session_cookie

    def get_auth_headers(self) -> Dict[str, str]:
        return {"Authorization": f"Bearer {self.get_token()}"}

    def force_refresh(self) -> None:
        """Force a token refresh regardless of cached expiry. Used on 401 responses."""
        if self._state is None:
            self._full_sign_in()
        else:
            self._refresh()


class LegacyCookieAuth:
    """Backwards-compat wrapper for users still passing a Netscape cookie file.

    Re-reads the file on each request so the user can refresh cookies on disk
    mid-run without restarting. No automatic refresh — token will eventually
    expire and the run will fail unless the file is updated.
    """

    def __init__(self, cookie_file: str) -> None:
        self.cookie_file = cookie_file

    def get_token(self) -> str:
        token = parse_token(self.cookie_file)
        if not token:
            raise AuthError(
                f"No session-cookie found in {self.cookie_file}. "
                "The file may have expired or been overwritten."
            )
        return token

    def get_auth_headers(self) -> Dict[str, str]:
        return {"Authorization": f"Bearer {self.get_token()}"}

    def force_refresh(self) -> None:
        # No-op: re-reading happens on every get_token() call already.
        pass
