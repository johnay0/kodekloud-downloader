import pytest


@pytest.fixture(autouse=True)
def _isolate_auth_env(tmp_path, monkeypatch):
    """Never touch the real ~/.cache auth file or pick up real credentials."""
    monkeypatch.setenv("KODEKLOUD_AUTH_STATE", str(tmp_path / "auth-state.json"))
    for var in ("KODEKLOUD_EMAIL", "KODEKLOUD_PASSWORD"):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.setattr(
        "kodekloud_downloader.auth.DEFAULT_STATE_PATH",
        tmp_path / "auth-state-default.json",
    )


class FakeAuth:
    """Stands in for FirebaseAuth: fixed token, cannot renew."""

    def __init__(self, token: str = "valid_jwt") -> None:
        self.token = token

    def get_token(self) -> str:
        return self.token

    def force_refresh(self) -> bool:
        return False


@pytest.fixture
def fake_auth():
    return FakeAuth()
