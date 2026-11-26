import logging
from collections import defaultdict
from pathlib import Path
from typing import Optional, Union

import markdownify
import requests
import yt_dlp
from bs4 import BeautifulSoup

from kodekloud_downloader.auth import AuthProvider, auth_headers
from kodekloud_downloader.helpers import (
    download_all_resources,
    download_video,
    is_normal_content,
    sanitize_filename,
)
from kodekloud_downloader.models.course import CourseDetail
from kodekloud_downloader.models.courses import Course
from kodekloud_downloader.models.helper import fetch_course_detail
from kodekloud_downloader.models.quiz import Quiz

logger = logging.getLogger(__name__)

API_BASE = "https://learn-api.kodekloud.com/api"


class EnrollmentError(Exception):
    """KodeKloud refuses access to a course (not enrolled / plan too low)."""


def download_quiz(output_dir: Union[str, Path], sep: bool) -> None:
    """
    Download quizzes from the API and save them as Markdown files.

    :param output_dir: The directory path where the Markdown files will be saved.
    :param sep: A boolean flag indicating whether to separate each quiz into
        individual files. If `True`, each quiz will be saved as a separate
        Markdown file. If `False`, all quizzes will be combined into a single
        Markdown file.
    :return: None
    :raises ValueError: If `output_dir` is not a valid directory path.
    :raises requests.RequestException: For errors related to the HTTP request.
    :raises IOError: For file I/O errors.
    """
    quiz_markdown = [] if sep else ["# KodeKloud Quiz"]
    response = requests.get(
        "https://mcq-backend-main.kodekloud.com/api/quizzes/all", timeout=30
    )
    response.raise_for_status()

    quizzes = [Quiz(**item) for item in response.json()]
    print(f"Total {len(quizzes)} quiz available!")
    for quiz_index, quiz in enumerate(quizzes, start=1):
        quiz_name = quiz.name or quiz.topic
        quiz_markdown.append(f"\n## {quiz_name}")
        print(f"Fetching Quiz {quiz_index} - {quiz_name}")
        questions = quiz.fetch_questions()

        for index, question in enumerate(questions, start=1):
            quiz_markdown.append(f"\n**{index}. {question.question.strip()}**")
            quiz_markdown.append("\n")
            for answer in question.answers:
                quiz_markdown.append(f"* [ ] {answer}")
            quiz_markdown.append("\n**Correct answer:**")
            for answer in question.correctAnswers:
                quiz_markdown.append(f"* [x] {answer}")

            if script := question.code.get("script"):
                quiz_markdown.append(f"\n**Code**: \n{script}")
            if question.explanation:
                quiz_markdown.append(f"\n**Explaination**: {question.explanation}")
            if question.documentationLink:
                quiz_markdown.append(
                    f"\n**Documentation Link**: {question.documentationLink}"
                )

        if sep and quiz_name:
            safe_name = sanitize_filename(quiz_name)
            output_file = Path(output_dir) / f"{safe_name}.md"
            markdown_text = "\n".join(quiz_markdown)

            with open(output_file, "w", encoding="utf-8") as f:
                f.write(markdown_text)
            print(f"Quiz file written in {output_file}")

            quiz_markdown = []
        else:
            quiz_markdown.append("\n---\n")

    if not sep:
        output_file = Path(output_dir) / "KodeKloud_Quiz.md"
        markdown_text = "\n".join(quiz_markdown)

        Path(output_file).write_text(markdown_text)
        print(f"Quiz file written in {output_file}")


def parse_course_from_url(url: str) -> CourseDetail:
    """
    Parse the course slug from the given URL and fetch the course details.

    :param url: The URL from which to extract the course slug.
    :return: An instance of `CourseDetail` containing the course details.
    :raises ValueError: If the URL does not contain a valid course slug.
    """
    url = url.strip("/")
    course_slug = url.split("/")[-1]
    return fetch_course_detail(course_slug)


def download_course(
    course: Union[Course, CourseDetail],
    quality: str,
    output_dir: Union[str, Path],
    max_duplicate_count: int,
    auth: AuthProvider,
    auto_enroll: bool = True,
) -> None:
    """
    Download a course from KodeKloud.

    :param course: The Course or CourseDetail object
    :param quality: The video quality (e.g. "720p")
    :param output_dir: The output directory for the downloaded course
    :param max_duplicate_count: Stop if the same video URL comes back more
        than this many times (a sign of missing access)
    :param auth: Self-renewing auth provider (see kodekloud_downloader.auth)
    :param auto_enroll: Enroll in the course first if not enrolled yet
    :raises EnrollmentError: If KodeKloud denies access to the course
    """
    session = requests.Session()
    if auto_enroll:
        ensure_enrolled(session, course.id, course.title, auth)
    params = {
        "course_id": course.id,
    }

    course_detail = (
        fetch_course_detail(course.slug) if isinstance(course, Course) else course
    )

    downloaded_videos: defaultdict = defaultdict(int)
    global_lesson_index = 0
    for module_index, module in enumerate(course_detail.modules, start=1):
        for lesson in module.lessons:
            global_lesson_index += 1
            file_path = create_file_path(
                output_dir,
                course.title,
                module_index,
                module.title,
                global_lesson_index,
                lesson.title,
            )

            if lesson.type == "video":
                url = f"https://learn-api.kodekloud.com/api/lessons/{lesson.id}"

                try:
                    response = _api_get(session, url, auth, params=params)
                except requests.HTTPError as ex:
                    status = ex.response.status_code if ex.response is not None else 0
                    if status in (402, 403):
                        raise EnrollmentError(
                            _access_denied_msg(course.title, ex)
                        ) from None
                    raise

                lesson_data = response.json()
                lesson_video_url = lesson_data.get("video_url")
                if not lesson_video_url:
                    raise EnrollmentError(
                        f"KodeKloud returned no video for '{lesson.title}' in "
                        f"'{course.title}'. You are probably not enrolled, or "
                        "your plan does not include this course."
                    )
                # TODO: Maybe if in future KodeKloud change the video streaming
                # service, this area will need some working.
                # Try to generalize this for future enhancement?
                current_video_url = (
                    f"https://player.vimeo.com/video/{lesson_video_url.split('/')[-1]}"
                )
                if (
                    current_video_url in downloaded_videos
                    and downloaded_videos[current_video_url] > max_duplicate_count
                ):
                    raise SystemExit(
                        f"The following video is downloaded more than "
                        f"{max_duplicate_count}.\nYou may not have access to, "
                        "or be enrolled in, this course.\nPlease enroll in the "
                        "course and try again."
                    )
                download_video_lesson(current_video_url, file_path, quality)
                downloaded_videos[current_video_url] += 1

                video_content = lesson_data.get("content")
                if video_content:
                    download_all_resources(
                        video_content,
                        download_path=file_path.parent,
                        auth=auth,
                    )
            else:
                lesson_url = f"https://learn.kodekloud.com/user/courses/{course.slug}/module/{module.id}/lesson/{lesson.id}"
                download_resource_lesson(
                    lesson_url,
                    file_path,
                    lesson_id=lesson.id,
                    course_id=course.id,
                    auth=auth,
                )


_AUTH_FAILED_MSG = (
    "Authentication failed (401 Unauthorized) and the session could not be "
    "renewed. Sign in again with --email/--password."
)


def _api_request(
    session: requests.Session,
    method: str,
    url: str,
    auth: AuthProvider,
    **kwargs,
) -> requests.Response:
    """Call a learn-api URL; on 401 renew the token once and retry."""
    send = getattr(session, method)
    response = send(url, headers=auth_headers(auth, url), timeout=30, **kwargs)
    if response.status_code == 401:
        logger.info("Got 401 from KodeKloud API, renewing session token...")
        if not auth.force_refresh():
            raise SystemExit(_AUTH_FAILED_MSG)
        response = send(url, headers=auth_headers(auth, url), timeout=30, **kwargs)
        if response.status_code == 401:
            raise SystemExit(_AUTH_FAILED_MSG)
    return response


def _api_get(
    session: requests.Session,
    url: str,
    auth: AuthProvider,
    params: Optional[dict] = None,
) -> requests.Response:
    response = _api_request(session, "get", url, auth, params=params)
    response.raise_for_status()
    return response


def _error_message(response: Optional[requests.Response]) -> str:
    if response is None:
        return ""
    try:
        data = response.json()
        msg = data.get("message") or data.get("error") or ""
    except (ValueError, AttributeError):
        msg = ""
    return f"HTTP {response.status_code}" + (f": {msg}" if msg else "")


def _access_denied_msg(course_title: str, ex: requests.HTTPError) -> str:
    return (
        f"KodeKloud denied access to '{course_title}' "
        f"({_error_message(ex.response)}). You are probably not enrolled, or "
        "your plan does not include this course."
    )


def ensure_enrolled(
    session: requests.Session,
    course_id: str,
    course_title: str,
    auth: AuthProvider,
) -> bool:
    """Enroll in a course if not enrolled yet, like the web app does.

    The learn site auto-enrolls you when you open a lesson of a course you
    are not enrolled in; lesson downloads are refused otherwise. This mirrors
    that: check ``users_progresses/courses/{id}`` and, if ``enrolled`` is
    not true, ``POST users_progresses/enroll_course``.

    :return: True if this call enrolled you, False if you already were (or
        enrolling failed, which is only logged: a real lack of access then
        surfaces as an EnrollmentError on the first lesson).
    """
    progress_url = f"{API_BASE}/users_progresses/courses/{course_id}"
    try:
        resp = _api_request(session, "get", progress_url, auth)
        if resp.status_code == 200 and resp.json().get("enrolled") is True:
            logger.debug("Already enrolled in %s", course_title)
            return False
    except (requests.RequestException, ValueError, AttributeError) as ex:
        logger.debug("Could not read enrollment status of %s: %s", course_title, ex)

    enroll_url = f"{API_BASE}/users_progresses/enroll_course"
    try:
        resp = _api_request(
            session, "post", enroll_url, auth, json={"courseId": course_id}
        )
    except requests.RequestException as ex:
        logger.warning("Could not enroll in %s: %s", course_title, ex)
        return False
    if resp.ok:
        logger.info("Enrolled in %s", course_title)
        return True
    logger.warning(
        "KodeKloud did not enroll you in %s (%s); trying to download anyway.",
        course_title,
        _error_message(resp),
    )
    return False


# Maximum safe path length (Windows MAX_PATH is 260, leave room for
# the drive letter, colon, separator, and yt-dlp file extension)
_MAX_PATH_LENGTH = 220


def _shorten_component(name: str, max_len: int) -> str:
    """Truncate a path component, keeping its extension if present."""
    if len(name) <= max_len:
        return name
    # Keep the extension
    dot = name.rfind(".")
    if dot > 0 and len(name) - dot <= 10:
        stem = name[: dot - (len(name) - max_len + 1)]
        return stem + name[dot:]
    return name[:max_len]


def create_file_path(
    output_dir: Union[str, Path],
    course_name: str,
    module_index: int,
    module_name: str,
    lesson_index: int,
    lesson_name: str,
) -> Path:
    """
    Create a file path for a lesson, ensuring it stays within safe
    filesystem length limits.

    :param output_dir: The output directory for the downloaded course
    :param course_name: The course name
    :param module_index: The module index
    :param module_name: The module name
    :param lesson_index: The lesson index
    :param lesson_name: The lesson name
    :return: The created file path
    """
    parts = [
        "KodeKloud",
        sanitize_filename(course_name, max_length=80),
        sanitize_filename(f"{module_index:02d} - {module_name}", max_length=80),
        sanitize_filename(f"{lesson_index:03d} - {lesson_name}", max_length=80),
    ]

    # Build the base path and check total length
    base = Path(output_dir)
    full = base / Path(*parts)

    if len(str(full)) > _MAX_PATH_LENGTH:
        # Shorten components from most-specific to least
        overage = len(str(full)) - _MAX_PATH_LENGTH
        # Start with the last (most specific) component
        for i in range(len(parts) - 1, -1, -1):
            if overage <= 0:
                break
            current_len = len(parts[i])
            if current_len > 10:
                shorten_by = min(overage, current_len - 10)
                parts[i] = _shorten_component(parts[i], current_len - shorten_by)
                # Rebuild the path
                full = base / Path(*parts)
                overage = len(str(full)) - _MAX_PATH_LENGTH

    return full


def download_video_lesson(
    lesson_video_url,
    file_path: Path,
    quality: str,
) -> None:
    """
    Download a video lesson.

    :param lesson_video_url: The lesson video URL
    :param file_path: The output file path for the video
    :param quality: The video quality (e.g. "720p")
    """
    logger.info(f"Writing video file... {file_path}...")
    file_path.parent.mkdir(parents=True, exist_ok=True)
    logger.info(f"Parsing url: {lesson_video_url}")
    try:
        download_video(
            url=lesson_video_url,
            output_path=file_path,
            quality=quality,
        )
    except yt_dlp.utils.UnsupportedError:
        logger.error(
            f"Could not download video in link {lesson_video_url}. "
            "Please open link manually and verify that video exists!"
        )
    except yt_dlp.utils.DownloadError as ex:
        logger.error(
            f"Access denied while downloading video or audio file from link "
            f"{lesson_video_url}\n{ex}"
        )


def download_resource_lesson(
    lesson_url: str,
    file_path: Path,
    lesson_id: Optional[str] = None,
    course_id: Optional[str] = None,
    auth: Optional[AuthProvider] = None,
) -> None:
    """
    Download a resource lesson (article notes, presentation decks, or attachments).

    :param lesson_url: The lesson web URL
    :param file_path: The output file path for the resource
    :param lesson_id: Optional lesson ID (extracted from lesson_url if omitted)
    :param course_id: Optional course ID
    :param auth: Self-renewing auth provider
    """

    def _headers(url: str) -> dict:
        return auth_headers(auth, url) if auth is not None else {}

    # Extract lesson_id if not explicitly provided
    if not lesson_id and "/lesson/" in lesson_url:
        lesson_id = lesson_url.split("/lesson/")[-1].split("?")[0].strip("/")

    content_markdown: Optional[str] = None

    # 1. Query modern REST API if lesson_id is known
    if lesson_id:
        api_url = f"https://learn-api.kodekloud.com/api/lessons/{lesson_id}"
        params = {"course_id": course_id} if course_id else {}
        try:
            resp = requests.get(
                api_url, headers=_headers(api_url), params=params, timeout=30
            )
            if resp.status_code == 401 and auth is not None and auth.force_refresh():
                resp = requests.get(
                    api_url, headers=_headers(api_url), params=params, timeout=30
                )
            if resp.status_code == 200:
                data = resp.json()
                content_markdown = data.get("content")
        except Exception as ex:
            logger.debug(f"API lesson fetch failed for {lesson_id}: {ex}")

    # 2. Fall back to scraping lesson_url if API did not return content
    if content_markdown is None:
        try:
            page = requests.get(lesson_url, headers=_headers(lesson_url), timeout=30)
            soup = BeautifulSoup(page.content, "html.parser")
            content_elem = soup.find("div", class_="learndash_content_wrap")
            if content_elem and is_normal_content(content_elem):
                content_markdown = markdownify.markdownify(content_elem.prettify())
        except Exception as ex:
            logger.debug(f"HTML fallback failed for {lesson_url}: {ex}")

    # 3. Save markdown content and download any attached resources
    if content_markdown and content_markdown.strip():
        notes_path = file_path.with_suffix(".md")
        logger.info(f"Writing article notes... {notes_path}...")
        file_path.parent.mkdir(parents=True, exist_ok=True)
        notes_path.write_text(content_markdown, encoding="utf-8")
        downloaded = download_all_resources(
            content=content_markdown,
            download_path=file_path.parent,
            auth=auth,
        )
        if not downloaded:
            logger.info(
                "No attached downloadable resources (PDF/ZIP/slides) "
                "found in this lesson."
            )
