from unittest.mock import MagicMock, patch

from click.testing import CliRunner

from kodekloud_downloader.cli import kodekloud


def test_cli_help():
    runner = CliRunner()
    result = runner.invoke(kodekloud, ["--help"])
    assert result.exit_code == 0
    assert "dl" in result.output
    assert "dl-quiz" in result.output or "dl_quiz" in result.output


def test_cli_dl_help():
    runner = CliRunner()
    result = runner.invoke(kodekloud, ["dl", "--help"])
    assert result.exit_code == 0
    assert "--quality" in result.output
    assert "--email" in result.output
    assert "--password" in result.output
    assert "--no-enroll" in result.output
    for removed in ("--cookie", "--browser", "--token"):
        assert removed not in result.output
    assert "--search" in result.output
    assert "--filter" in result.output


def test_cli_missing_auth(tmp_path):
    runner = CliRunner()
    result = runner.invoke(
        kodekloud,
        [
            "dl",
            "--auth-state",
            str(tmp_path / "auth.json"),
            "https://kodekloud.com/courses/test-course",
        ],
        env={"KODEKLOUD_EMAIL": None, "KODEKLOUD_PASSWORD": None},
    )
    assert result.exit_code != 0
    assert "Not signed in" in result.output


def test_cli_uses_cached_session_without_flags(tmp_path, monkeypatch):
    import json

    state = tmp_path / "cached.json"
    state.write_text(
        json.dumps(
            {
                "refresh_token": "rt",
                "id_token": "cached-id",
                "expires_at": 9999999999,
                "email": "me@example.com",
            }
        )
    )
    monkeypatch.setenv("KODEKLOUD_AUTH_STATE", str(state))
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.parse_course_from_url"), patch(
        "kodekloud_downloader.cli.download_course"
    ) as mock_dl:
        result = runner.invoke(kodekloud, ["dl", "https://kodekloud.com/courses/x"])
    assert result.exit_code == 0, result.output
    kwargs = mock_dl.call_args.kwargs
    assert kwargs["auth"].get_token() == "cached-id"
    assert kwargs["auto_enroll"] is True


def test_cli_no_enroll_flag(fake_auth):
    runner = CliRunner()
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.parse_course_from_url"
    ), patch("kodekloud_downloader.cli.download_course") as mock_dl:
        result = runner.invoke(
            kodekloud, ["dl", "--no-enroll", "https://kodekloud.com/courses/x"]
        )
    assert result.exit_code == 0, result.output
    assert mock_dl.call_args.kwargs["auto_enroll"] is False


def test_cli_enrollment_error_is_reported_cleanly(fake_auth):
    from kodekloud_downloader.main import EnrollmentError

    runner = CliRunner()
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.parse_course_from_url"
    ), patch(
        "kodekloud_downloader.cli.download_course",
        side_effect=EnrollmentError("denied access to 'X'"),
    ):
        result = runner.invoke(kodekloud, ["dl", "https://kodekloud.com/courses/x"])
    assert result.exit_code == 1
    assert "denied access to 'X'" in result.output


def test_cli_forwards_search_to_select_courses(fake_auth):
    runner = CliRunner()
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.collect_all_courses"
    ) as mock_collect, patch(
        "kodekloud_downloader.cli.select_courses"
    ) as mock_select, patch("kodekloud_downloader.cli.download_course"):
        mock_course = MagicMock()
        mock_collect.return_value = [mock_course]
        mock_select.return_value = [mock_course]

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--search",
                "cnpe",
            ],
        )
        assert result.exit_code == 0
        assert mock_select.called
        assert mock_select.call_args[1].get("query") == "cnpe"


def test_cli_forwards_category_filter_to_select_courses(fake_auth):
    runner = CliRunner()
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.collect_all_courses"
    ) as mock_collect, patch(
        "kodekloud_downloader.cli.select_courses"
    ) as mock_select, patch("kodekloud_downloader.cli.download_course"):
        mock_course = MagicMock()
        mock_collect.return_value = [mock_course]
        mock_select.return_value = [mock_course]

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "-f",
                "Golden Kubestronaut",
            ],
        )
        assert result.exit_code == 0
        assert mock_select.called
        assert mock_select.call_args[1].get("category") == "Golden Kubestronaut"


def _course(title, plan):
    c = MagicMock()
    c.title = title
    c.plan = plan
    c.slug = title.lower()
    c.tutors = []
    c.categories = []
    return c


def test_cli_all_paid_dry_run_lists_only_standard_and_pro():
    runner = CliRunner()
    courses = [
        _course("FreeOne", "Free"),
        _course("StdOne", "Standard"),
        _course("ProOne", "Pro"),
    ]
    with patch(
        "kodekloud_downloader.cli.collect_all_courses", return_value=courses
    ), patch("kodekloud_downloader.cli.download_course") as mock_dl:
        result = runner.invoke(kodekloud, ["dl", "--all-paid", "--dry-run"])
    assert result.exit_code == 0, result.output
    assert "StdOne" in result.output and "ProOne" in result.output
    assert "FreeOne" not in result.output
    assert "1 Free course(s) skipped" in result.output
    assert not mock_dl.called


def test_cli_all_paid_downloads_paid_and_continues_after_failure(fake_auth):
    runner = CliRunner()
    courses = [
        _course("FreeOne", "Free"),
        _course("StdOne", "Standard"),
        _course("ProOne", "Pro"),
    ]
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.collect_all_courses", return_value=courses
    ), patch(
        "kodekloud_downloader.cli.download_course",
        side_effect=[RuntimeError("boom"), None],
    ) as mock_dl:
        result = runner.invoke(kodekloud, ["dl", "--all-paid"])
    downloaded = [c.kwargs["course"].title for c in mock_dl.call_args_list]
    assert downloaded == ["StdOne", "ProOne"]
    assert "1/2 course(s)" in result.output
    assert result.exit_code == 1


def test_cli_all_paid_rejects_course_url():
    runner = CliRunner()
    result = runner.invoke(
        kodekloud, ["dl", "--all-paid", "https://kodekloud.com/courses/x"]
    )
    assert result.exit_code != 0
    assert "--all-paid" in result.output


def test_cli_email_prompts_for_password_and_signs_in():
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.parse_course_from_url"), patch(
        "kodekloud_downloader.cli.download_course"
    ) as mock_dl, patch("kodekloud_downloader.auth.requests.post") as post:
        post.return_value = MagicMock(
            ok=True,
            status_code=200,
            json=MagicMock(
                return_value={
                    "idToken": "id-pw",
                    "refreshToken": "rt",
                    "expiresIn": "3600",
                }
            ),
        )
        result = runner.invoke(
            kodekloud,
            ["dl", "-e", "me@example.com", "https://kodekloud.com/courses/x"],
            input="s3cret\n",
        )
    assert result.exit_code == 0, result.output
    assert post.call_args.kwargs["json"]["password"] == "s3cret"
    assert mock_dl.call_args.kwargs["auth"].get_token() == "id-pw"


def test_cli_plan_filter_applies_to_interactive_list(fake_auth):
    runner = CliRunner()
    courses = [_course("FreeOne", "Free"), _course("ProOne", "Pro")]
    with patch("kodekloud_downloader.cli._build_auth", return_value=fake_auth), patch(
        "kodekloud_downloader.cli.collect_all_courses", return_value=courses
    ), patch("kodekloud_downloader.cli.select_courses", return_value=[]) as sel:
        result = runner.invoke(kodekloud, ["dl", "--plan", "pro"])
    assert result.exit_code == 0, result.output
    assert [c.title for c in sel.call_args.args[0]] == ["ProOne"]
