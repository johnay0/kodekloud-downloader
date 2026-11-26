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
    assert "--cookie" in result.output
    assert "--browser" in result.output
    assert "--token" in result.output
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
    assert "No credentials" in result.output


def test_cli_forwards_cookie_to_download_course():
    runner = CliRunner()
    fake_jwt = (
        "eyJhbGciOiJSUzI1NiJ9."
        "eyJpc3MiOiJodHRwczovL3NlY3VyZXRva2VuLmdvb2dsZS5jb20vcHJvZHVjdC12MyJ9."
        "signature"
    )
    with patch("kodekloud_downloader.cli.parse_course_from_url") as mock_parse, patch(
        "kodekloud_downloader.cli.download_course"
    ) as mock_dl, patch(
        "kodekloud_downloader.helpers.parse_token", return_value=fake_jwt
    ):
        mock_course = MagicMock()
        mock_parse.return_value = mock_course

        # Invoke CLI with --cookie
        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--cookie",
                "cookies.txt",
                "https://kodekloud.com/courses/docker-basics",
            ],
        )
        assert result.exit_code == 0
        assert mock_dl.called
        call_kwargs = mock_dl.call_args[1]
        assert call_kwargs.get("cookie") == "cookies.txt"
        assert call_kwargs["auth"].get_token() == fake_jwt


def test_cli_cookie_triggers_exchange_when_needed():
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.parse_course_from_url") as mock_parse, patch(
        "kodekloud_downloader.cli.download_course"
    ) as mock_dl, patch(
        "kodekloud_downloader.helpers.parse_token",
        return_value="_secure-user-session-val",
    ), patch(
        "kodekloud_downloader.browser.get_tokens_from_cookie_file",
        return_value={"id_token": "eyJexchanged_jwt"},
    ):
        mock_course = MagicMock()
        mock_parse.return_value = mock_course

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--cookie",
                "cookies.txt",
                "https://kodekloud.com/courses/docker-basics",
            ],
        )
        assert result.exit_code == 0
        assert mock_dl.called
        call_kwargs = mock_dl.call_args[1]
        assert call_kwargs.get("cookie") == "cookies.txt"
        assert call_kwargs["auth"].get_token() == "eyJexchanged_jwt"


def test_cli_supports_token_option():
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.parse_course_from_url") as mock_parse, patch(
        "kodekloud_downloader.cli.download_course"
    ) as mock_dl:
        mock_course = MagicMock()
        mock_parse.return_value = mock_course

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--token",
                "my_direct_jwt_token",
                "https://kodekloud.com/courses/docker-basics",
            ],
        )
        assert result.exit_code == 0
        assert mock_dl.called
        call_kwargs = mock_dl.call_args[1]
        assert call_kwargs["auth"].get_token() == "my_direct_jwt_token"


def test_cli_forwards_search_to_select_courses():
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.collect_all_courses") as mock_collect, patch(
        "kodekloud_downloader.cli.select_courses"
    ) as mock_select, patch("kodekloud_downloader.cli.download_course"):
        mock_course = MagicMock()
        mock_collect.return_value = [mock_course]
        mock_select.return_value = [mock_course]

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--token",
                "direct_token",
                "--search",
                "cnpe",
            ],
        )
        assert result.exit_code == 0
        assert mock_select.called
        assert mock_select.call_args[1].get("query") == "cnpe"


def test_cli_forwards_category_filter_to_select_courses():
    runner = CliRunner()
    with patch("kodekloud_downloader.cli.collect_all_courses") as mock_collect, patch(
        "kodekloud_downloader.cli.select_courses"
    ) as mock_select, patch("kodekloud_downloader.cli.download_course"):
        mock_course = MagicMock()
        mock_collect.return_value = [mock_course]
        mock_select.return_value = [mock_course]

        result = runner.invoke(
            kodekloud,
            [
                "dl",
                "--token",
                "direct_token",
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


def test_cli_all_paid_downloads_paid_and_continues_after_failure():
    runner = CliRunner()
    courses = [
        _course("FreeOne", "Free"),
        _course("StdOne", "Standard"),
        _course("ProOne", "Pro"),
    ]
    with patch(
        "kodekloud_downloader.cli.collect_all_courses", return_value=courses
    ), patch(
        "kodekloud_downloader.cli.download_course",
        side_effect=[RuntimeError("boom"), None],
    ) as mock_dl:
        result = runner.invoke(kodekloud, ["dl", "--token", "tok", "--all-paid"])
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


def test_cli_plan_filter_applies_to_interactive_list():
    runner = CliRunner()
    courses = [_course("FreeOne", "Free"), _course("ProOne", "Pro")]
    with patch(
        "kodekloud_downloader.cli.collect_all_courses", return_value=courses
    ), patch("kodekloud_downloader.cli.select_courses", return_value=[]) as sel:
        result = runner.invoke(kodekloud, ["dl", "-t", "tok", "--plan", "pro"])
    assert result.exit_code == 0, result.output
    assert [c.title for c in sel.call_args.args[0]] == ["ProOne"]
