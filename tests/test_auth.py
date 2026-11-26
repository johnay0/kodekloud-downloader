import json
import os
import stat
import time
from unittest.mock import MagicMock, patch

import pytest

from kodekloud_downloader.auth import (
    REFRESH_URL,
    SIGN_IN_URL,
    AuthError,
    FirebaseAuth,
    auth_headers,
    is_kodekloud_url,
)
from tests.conftest import FakeAuth


def _resp(status=200, payload=None):
    r = MagicMock()
    r.status_code = status
    r.ok = 200 <= status < 300
    r.json.return_value = payload or {}
    return r


def _sign_in_payload(id_token="id-1", refresh="rt-1", expires_in="3600"):
    return {"idToken": id_token, "refreshToken": refresh, "expiresIn": expires_in}


@pytest.mark.parametrize(
    "url,expected",
    [
        ("https://learn-api.kodekloud.com/api/lessons/1", True),
        ("https://kodekloud.com/x.pdf", True),
        ("http://learn-api.kodekloud.com/api", False),  # no token over http
        ("https://raw.githubusercontent.com/a/b/c.pdf", False),
        ("https://kodekloud.com.evil.example/x", False),
        ("https://evilkodekloud.com/x", False),
        ("https://kodekloud.com@evil.example/x", False),
        ("not a url", False),
    ],
)
def test_is_kodekloud_url(url, expected):
    assert is_kodekloud_url(url) is expected


def test_auth_headers_never_sent_to_third_parties():
    auth = FakeAuth("secret")
    assert auth_headers(auth, "https://github.com/x/y.zip") == {}
    assert auth_headers(auth, "https://learn-api.kodekloud.com/api") == {
        "Authorization": "Bearer secret"
    }


def test_password_sign_in_caches_refresh_token_securely(tmp_path):
    state = tmp_path / "auth.json"
    with patch("kodekloud_downloader.auth.requests.post") as post:
        post.return_value = _resp(200, _sign_in_payload())
        auth = FirebaseAuth(email="me@example.com", password="pw", state_path=state)
        assert auth.get_token() == "id-1"

    assert post.call_args.args[0] == SIGN_IN_URL
    assert post.call_args.kwargs["json"]["email"] == "me@example.com"
    data = json.loads(state.read_text())
    assert data["refresh_token"] == "rt-1"
    assert "pw" not in state.read_text()
    if os.name == "posix":
        assert stat.S_IMODE(state.stat().st_mode) == 0o600


def test_token_is_renewed_before_expiry_via_refresh_token(tmp_path):
    state = tmp_path / "auth.json"
    state.write_text(
        json.dumps(
            {
                "refresh_token": "rt-old",
                "id_token": "id-old",
                "expires_at": time.time() + 60,  # inside the leeway window
                "email": "me@example.com",
            }
        )
    )
    with patch("kodekloud_downloader.auth.requests.post") as post:
        post.return_value = _resp(
            200, {"id_token": "id-new", "refresh_token": "rt-new", "expires_in": "3600"}
        )
        auth = FirebaseAuth(state_path=state)
        assert auth.get_token() == "id-new"
        assert post.call_args.args[0] == REFRESH_URL
        assert post.call_args.kwargs["data"]["refresh_token"] == "rt-old"
        # Second call within validity: no new request
        assert auth.get_token() == "id-new"
        assert post.call_count == 1
    assert json.loads(state.read_text())["refresh_token"] == "rt-new"


def test_refresh_failure_falls_back_to_password(tmp_path):
    state = tmp_path / "auth.json"
    state.write_text(json.dumps({"refresh_token": "rt-dead", "email": "me@x.com"}))
    calls = []

    def fake_post(url, **kwargs):
        calls.append(url)
        if url == REFRESH_URL:
            return _resp(400, {"error": {"message": "TOKEN_EXPIRED"}})
        return _resp(200, _sign_in_payload(id_token="id-pw"))

    with patch("kodekloud_downloader.auth.requests.post", side_effect=fake_post):
        auth = FirebaseAuth(email="me@x.com", password="pw", state_path=state)
        assert auth.get_token() == "id-pw"
    assert calls == [REFRESH_URL, SIGN_IN_URL]


def test_sign_in_error_is_explained(tmp_path):
    with patch("kodekloud_downloader.auth.requests.post") as post:
        post.return_value = _resp(
            400, {"error": {"message": "MISSING_RECAPTCHA_TOKEN"}}
        )
        auth = FirebaseAuth(email="a@b.c", password="pw", state_path=None)
        with pytest.raises(AuthError, match="reCAPTCHA"):
            auth.get_token()


def test_legacy_cache_format_is_migrated(tmp_path):
    state = tmp_path / "auth.json"
    state.write_text(
        json.dumps(
            {
                "refresh_token": "rt",
                "session_cookie": "id-legacy",
                "expires_at": time.time() + 3600,
                "email": "me@x.com",
            }
        )
    )
    auth = FirebaseAuth(state_path=state)
    assert auth.get_token() == "id-legacy"


def test_cache_of_other_account_is_ignored(tmp_path):
    state = tmp_path / "auth.json"
    state.write_text(json.dumps({"refresh_token": "rt-other", "email": "b@x.com"}))
    auth = FirebaseAuth(email="a@x.com", state_path=state)
    assert not auth.has_refresh_token


def test_cached_session_needs_no_credentials(tmp_path):
    state = tmp_path / "auth.json"
    state.write_text(json.dumps({"refresh_token": "rt", "email": "a@x.com"}))
    auth = FirebaseAuth(state_path=state)
    assert auth.has_credentials
    assert auth.email == "a@x.com"


def test_no_cache_no_password_cannot_sign_in():
    auth = FirebaseAuth(email="a@x.com", state_path=None)
    assert not auth.has_credentials
    with pytest.raises(AuthError):
        auth.get_token()
    assert auth.force_refresh() is False


def test_repr_hides_secrets():
    auth = FirebaseAuth(email="a@x.com", password="hunter2", state_path=None)
    assert "hunter2" not in repr(auth)
