import base64
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
    StaticTokenAuth,
    auth_headers,
    is_kodekloud_url,
    jwt_expiry,
)


def _jwt(**claims) -> str:
    def enc(d):
        return base64.urlsafe_b64encode(json.dumps(d).encode()).decode().rstrip("=")

    return f"{enc({'alg': 'RS256'})}.{enc(claims)}.sig"


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
    auth = StaticTokenAuth("secret")
    assert auth_headers(auth, "https://github.com/x/y.zip") == {}
    assert auth_headers(auth, "https://learn-api.kodekloud.com/api") == {
        "Authorization": "Bearer secret"
    }


def test_jwt_expiry():
    assert jwt_expiry(_jwt(exp=1234)) == 1234.0
    assert jwt_expiry("garbage") is None


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


def test_force_refresh_without_any_credentials_returns_false():
    auth = FirebaseAuth(state_path=None, seed={"id_token": "tok"})
    assert auth.get_token() == "tok"  # unknown expiry: used until a 401
    assert auth.force_refresh() is False


def test_fallback_source_used_when_refresh_unavailable():
    fallback = MagicMock(return_value={"id_token": "id-fb", "refresh_token": "rt-fb"})
    auth = FirebaseAuth(state_path=None, seed={"id_token": "tok"}, fallback=fallback)
    assert auth.force_refresh() is True
    assert auth.get_token() == "id-fb"
    assert auth.has_refresh_token


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

    token = _jwt(email="a@x.com", exp=time.time() + 3600)
    auth = FirebaseAuth(state_path=state, seed={"id_token": token})
    assert not auth.has_refresh_token


def test_repr_hides_secrets():
    auth = FirebaseAuth(email="a@x.com", password="hunter2", state_path=None)
    assert "hunter2" not in repr(auth)


def test_static_token_cannot_refresh():
    assert StaticTokenAuth("t").force_refresh() is False
