import pytest


@pytest.fixture(autouse=True)
def _isolate_auth_env(tmp_path, monkeypatch):
    """Never touch the real ~/.cache auth file or pick up real credentials."""
    monkeypatch.setenv("KODEKLOUD_AUTH_STATE", str(tmp_path / "auth-state.json"))
    for var in ("KODEKLOUD_EMAIL", "KODEKLOUD_PASSWORD", "KODEKLOUD_TOKEN"):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.setattr(
        "kodekloud_downloader.auth.DEFAULT_STATE_PATH",
        tmp_path / "auth-state-default.json",
    )
