import logging
from pathlib import Path
from typing import List, NoReturn, Optional, Union

import click
import validators

from kodekloud_downloader.auth import (
    DEFAULT_STATE_PATH,
    AuthError,
    FirebaseAuth,
    TokenSource,
    jwt_claims,
)
from kodekloud_downloader.enums import Quality
from kodekloud_downloader.helpers import (
    PAID_PLANS,
    _filter_courses,
    filter_by_category,
    filter_by_plan,
    render_course_table,
    select_courses,
)
from kodekloud_downloader.main import (
    download_course,
    download_quiz,
    parse_course_from_url,
)
from kodekloud_downloader.models.courses import Course
from kodekloud_downloader.models.helper import collect_all_courses


@click.group()
@click.option("-v", "--verbose", count=True, help="Increase log level verbosity")
def kodekloud(verbose):
    logging.basicConfig(
        format="%(asctime)s - %(levelname)s - %(message)s", level=logging.INFO
    )
    if verbose == 1:
        logging.getLogger().setLevel(logging.INFO)
    elif verbose >= 2:
        logging.getLogger().setLevel(logging.DEBUG)


@kodekloud.command()
@click.argument("course_url", required=False)
@click.option(
    "--quality",
    "-q",
    default="1080p",
    type=click.Choice([quality.value for quality in Quality]),
    help="Quality of the video to be downloaded.",
)
@click.option(
    "--output-dir",
    "-o",
    default=Path.home() / "Downloads",
    help="Output directory where downloaded files will be store.",
)
@click.option(
    "--email",
    "-e",
    envvar="KODEKLOUD_EMAIL",
    default=None,
    help=(
        "KodeKloud account email. Enables automatic token renewal "
        "(env: KODEKLOUD_EMAIL)."
    ),
)
@click.option(
    "--password",
    "-p",
    envvar="KODEKLOUD_PASSWORD",
    default=None,
    help=(
        "KodeKloud account password (env: KODEKLOUD_PASSWORD). Prompted for "
        "if --email is given without it. Never written to disk."
    ),
)
@click.option(
    "--cookie",
    "-c",
    default=None,
    help="Cookie file exported from browser. Not needed with --browser.",
)
@click.option(
    "--browser",
    is_flag=True,
    default=False,
    help="Extract session token from running Chrome (requires playwright).",
)
@click.option(
    "--token",
    "-t",
    envvar="KODEKLOUD_TOKEN",
    default=None,
    help="Direct Firebase JWT authentication token (env: KODEKLOUD_TOKEN).",
)
@click.option(
    "--auth-state",
    envvar="KODEKLOUD_AUTH_STATE",
    default=str(DEFAULT_STATE_PATH),
    show_default=True,
    help=(
        "Where the refresh token is cached (0600) so later runs need no "
        "credentials. Env: KODEKLOUD_AUTH_STATE."
    ),
)
@click.option(
    "--no-auth-cache",
    is_flag=True,
    default=False,
    help="Do not read or write the refresh-token cache.",
)
@click.option(
    "--max-duplicate-count",
    "-mdc",
    default=3,
    type=int,
    help="If same video is downloaded this many times, then download stops",
)
@click.option(
    "--search",
    "-s",
    default=None,
    help="Filter course list by keyword (title, instructor, or category).",
)
@click.option(
    "--filter",
    "-f",
    "--category",
    "category_filter",
    default=None,
    help=(
        "Filter course list by category "
        "(e.g. 'Kubernetes', 'Security', 'Golden Kubestronaut')."
    ),
)
@click.option(
    "--plan",
    "plan_filter",
    default=None,
    help="Only list courses on these plans, comma-separated (Free, Standard, Pro).",
)
@click.option(
    "--all-paid",
    is_flag=True,
    default=False,
    help=(
        "Download every Standard and Pro course (Free ones are skipped) without "
        "prompting. Combine with -s/-f to narrow it down."
    ),
)
@click.option(
    "--dry-run",
    is_flag=True,
    default=False,
    help="With --all-paid: only list the courses that would be downloaded.",
)
def dl(
    course_url,
    quality: str,
    output_dir: Union[Path, str],
    email: Optional[str],
    password: Optional[str],
    cookie: Optional[str],
    browser: bool,
    token: Optional[str],
    auth_state: str,
    no_auth_cache: bool,
    max_duplicate_count: int,
    search: Optional[str],
    category_filter: Optional[str],
    plan_filter: Optional[str],
    all_paid: bool,
    dry_run: bool,
):
    if all_paid and course_url:
        _fail("--all-paid downloads the whole catalog; do not pass a COURSE_URL.")
    if dry_run and not all_paid:
        _fail("--dry-run only works together with --all-paid.")

    if all_paid and dry_run:
        _list_paid_courses(search, category_filter)
        return

    try:
        auth = _build_auth(
            email=email,
            password=password,
            cookie=cookie,
            browser=browser,
            token=token,
            state_path=None if no_auth_cache else Path(auth_state),
        )
        _run_downloads(
            auth=auth,
            course_url=course_url,
            quality=quality,
            output_dir=output_dir,
            cookie=cookie,
            max_duplicate_count=max_duplicate_count,
            search=search,
            category_filter=category_filter,
            plan_filter=plan_filter,
            all_paid=all_paid,
        )
    except AuthError as e:
        _fail(str(e))


def _fail(msg: str) -> NoReturn:
    logging.error(msg)
    click.echo(msg, err=True)
    raise SystemExit(1)


def _is_firebase_id_token(raw_token: Optional[str]) -> bool:
    """True for a Firebase ID token, False for a session cookie / garbage."""
    if not raw_token or not raw_token.startswith("ey"):
        return False
    if len(raw_token.split(".")) != 3:
        return False
    iss = str(jwt_claims(raw_token).get("iss", ""))
    return bool(iss) and "session.firebase" not in iss


def _build_auth(
    email: Optional[str],
    password: Optional[str],
    cookie: Optional[str],
    browser: bool,
    token: Optional[str],
    state_path: Optional[Path],
) -> FirebaseAuth:
    """Turn whatever credentials were given into one self-renewing provider.

    Every path ends in a FirebaseAuth so the ID token is renewed whenever a
    refresh token (from password sign-in, cookie/browser exchange, or the
    cache) or a password is available.
    """
    seed: Optional[dict] = None
    fallback: Optional[TokenSource] = None

    if token:
        seed = {"id_token": token}
        logging.info("Using session token provided via CLI")
    elif browser:
        from kodekloud_downloader.browser import get_session_tokens_from_browser

        seed = get_session_tokens_from_browser(auto_launch=True)
        if not seed:
            _fail(
                "Could not obtain session token from browser. "
                "Make sure Chrome is running with --remote-debugging-port=9222 "
                "and you are signed in to https://learn.kodekloud.com"
            )
        logging.info("Session token extracted from browser successfully")
    elif cookie:
        from kodekloud_downloader.browser import get_tokens_from_cookie_file
        from kodekloud_downloader.helpers import parse_token

        def _cookie_fallback() -> Optional[dict]:
            # Session cookies outlive refresh failures; re-run the exchange.
            return get_tokens_from_cookie_file(cookie)

        fallback = _cookie_fallback

        raw_token = parse_token(cookie)
        if _is_firebase_id_token(raw_token):
            seed = {"id_token": raw_token}
        else:
            logging.info("Resolving Firebase ID token from cookie file via browser...")
            click.echo("Authenticating session from cookies...", err=True)
            seed = get_tokens_from_cookie_file(cookie)
            if not seed:
                _fail(
                    "Could not extract session token from cookie file. "
                    "Make sure your cookie file contains valid session cookies, "
                    "or try using --email / --browser / --token."
                )
            logging.info("Session token resolved successfully from cookies")

    auth = FirebaseAuth(
        email=email,
        password=password,
        state_path=state_path,
        seed=seed,
        fallback=fallback,
    )

    if email and not password and not auth.has_refresh_token:
        auth.set_password(click.prompt("KodeKloud password", hide_input=True))

    if not auth.has_credentials:
        _fail(
            "No credentials: use --email/--password (recommended, auto-renews), "
            "--cookie, --browser, or --token"
        )
    if not auth.has_refresh_token and not auth.has_password:
        logging.warning(
            "No refresh token available: the session cannot be renewed when it "
            "expires (~1h). Use --email/--password for long downloads."
        )

    auth.get_token()  # fail fast on bad credentials
    return auth


def _list_paid_courses(
    search: Optional[str], category_filter: Optional[str]
) -> List[Course]:
    courses = collect_all_courses()
    paid = filter_by_plan(courses, PAID_PLANS)
    if category_filter:
        paid = [c for _, c in filter_by_category(paid, category_filter)]
    if search:
        paid = [c for _, c in _filter_courses(paid, search)]
    click.echo(render_course_table(list(enumerate(paid, start=1))))
    counts = {p: sum(1 for c in paid if c.plan == p) for p in PAID_PLANS}
    skipped = len(courses) - len(filter_by_plan(courses, PAID_PLANS))
    click.echo(
        f"{len(paid)} paid course(s): "
        + ", ".join(f"{n} {p}" for p, n in counts.items())
        + f" ({skipped} Free course(s) skipped)."
    )
    return paid


def _run_downloads(
    auth: FirebaseAuth,
    course_url: Optional[str],
    quality: str,
    output_dir: Union[Path, str],
    cookie: Optional[str],
    max_duplicate_count: int,
    search: Optional[str],
    category_filter: Optional[str],
    plan_filter: Optional[str],
    all_paid: bool,
) -> None:
    common: dict = {
        "quality": quality,
        "output_dir": output_dir,
        "max_duplicate_count": max_duplicate_count,
        "cookie": cookie,
        "auth": auth,
    }

    if all_paid:
        paid = _list_paid_courses(search, category_filter)
        failed: List[str] = []
        for i, course in enumerate(paid, start=1):
            click.echo(f"\n[{i}/{len(paid)}] {course.title} ({course.plan})")
            try:
                download_course(course=course, **common)
            except AuthError:
                raise
            except Exception as e:  # keep going: one broken course != dead dump
                logging.error("Failed to download %s: %s", course.title, e)
                failed.append(course.title)
        click.echo(f"\nDone: {len(paid) - len(failed)}/{len(paid)} course(s).")
        if failed:
            click.echo("Failed (re-run to retry, finished files are skipped):")
            for title in failed:
                click.echo(f"  - {title}")
            raise SystemExit(1)
        return

    if course_url is None:
        courses = collect_all_courses()
        if plan_filter:
            courses = filter_by_plan(courses, plan_filter)
        selected_courses = select_courses(
            courses, query=search, category=category_filter
        )
        for selected_course in selected_courses:
            download_course(course=selected_course, **common)
    elif validators.url(course_url):
        course_detail = parse_course_from_url(course_url)
        download_course(course=course_detail, **common)
    else:
        _fail("Please enter a valid URL")


@kodekloud.command()
@click.option(
    "--output-dir",
    "-o",
    default=Path.home() / "Downloads",
    help="Output directory where quiz markdown file will be saved.",
)
@click.option(
    "--sep",
    is_flag=True,
    show_default=True,
    default=False,
    help="Write in seperate markdown files.",
)
def dl_quiz(output_dir: Union[Path, str], sep: bool):
    Path(output_dir).mkdir(parents=True, exist_ok=True)
    download_quiz(output_dir, sep)


if __name__ == "__main__":
    kodekloud()
