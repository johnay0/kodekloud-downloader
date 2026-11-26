import logging
from pathlib import Path
from typing import List, NoReturn, Optional, Union

import click
import validators

from kodekloud_downloader.auth import DEFAULT_STATE_PATH, AuthError, FirebaseAuth
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
    EnrollmentError,
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
        "KodeKloud account email (env: KODEKLOUD_EMAIL). Only needed for the "
        "first run; the session is cached and renewed automatically."
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
    "--auth-state",
    envvar="KODEKLOUD_AUTH_STATE",
    default=str(DEFAULT_STATE_PATH),
    show_default=True,
    help=(
        "Where the session (refresh token, never the password) is cached with "
        "0600 permissions so later runs need no credentials. "
        "Env: KODEKLOUD_AUTH_STATE."
    ),
)
@click.option(
    "--no-auth-cache",
    is_flag=True,
    default=False,
    help="Do not read or write the session cache.",
)
@click.option(
    "--no-enroll",
    is_flag=True,
    default=False,
    help=(
        "Do not auto-enroll in courses you are not enrolled in yet "
        "(KodeKloud refuses lesson downloads for those)."
    ),
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
    auth_state: str,
    no_auth_cache: bool,
    no_enroll: bool,
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
            state_path=None if no_auth_cache else Path(auth_state),
        )
        _run_downloads(
            auth=auth,
            course_url=course_url,
            quality=quality,
            output_dir=output_dir,
            auto_enroll=not no_enroll,
            max_duplicate_count=max_duplicate_count,
            search=search,
            category_filter=category_filter,
            plan_filter=plan_filter,
            all_paid=all_paid,
        )
    except (AuthError, EnrollmentError) as e:
        _fail(str(e))


def _fail(msg: str) -> NoReturn:
    logging.error(msg)
    click.echo(msg, err=True)
    raise SystemExit(1)


def _build_auth(
    email: Optional[str],
    password: Optional[str],
    state_path: Optional[Path],
) -> FirebaseAuth:
    """Sign in with email/password, or reuse the cached session."""
    auth = FirebaseAuth(email=email, password=password, state_path=state_path)

    if email and not password and not auth.has_refresh_token:
        auth.set_password(click.prompt("KodeKloud password", hide_input=True))

    if not auth.has_credentials:
        _fail(
            "Not signed in: pass --email (and --password, or you will be "
            "prompted), or set KODEKLOUD_EMAIL / KODEKLOUD_PASSWORD."
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
    auto_enroll: bool,
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
        "auto_enroll": auto_enroll,
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
                failed.append(f"{course.title}: {e}")
        click.echo(f"\nDone: {len(paid) - len(failed)}/{len(paid)} course(s).")
        if failed:
            click.echo("Failed (re-run to retry, finished files are skipped):")
            for line in failed:
                click.echo(f"  - {line}")
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
