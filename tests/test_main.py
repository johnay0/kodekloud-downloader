from unittest.mock import MagicMock, patch

import pytest

from kodekloud_downloader.main import (
    _MAX_PATH_LENGTH,
    _shorten_component,
    create_file_path,
)
from tests.conftest import FakeAuth


def test_shorten_component_short_string():
    assert _shorten_component("hello", 10) == "hello"


def test_shorten_component_no_extension():
    assert _shorten_component("very_long_component_name", 10) == "very_long_"


def test_shorten_component_preserves_extension():
    res = _shorten_component("long_document_title.pdf", 15)
    assert res.endswith(".pdf")
    assert len(res) <= 15


def test_create_file_path_under_max_length():
    path = create_file_path(
        output_dir="/tmp/downloads",
        course_name="Docker Basics",
        module_index=1,
        module_name="Getting Started",
        lesson_index=2,
        lesson_name="Containers",
    )
    assert len(str(path)) <= _MAX_PATH_LENGTH
    assert "Docker Basics" in str(path)
    assert "Getting Started" in str(path)
    assert "002 - Containers" in str(path)


def test_create_file_path_zero_pads_lesson_index():
    path = create_file_path(
        output_dir="/tmp/downloads",
        course_name="Kubernetes",
        module_index=1,
        module_name="Intro",
        lesson_index=7,
        lesson_name="Pods",
    )
    assert "007 - Pods" in str(path)


def test_create_file_path_zero_pads_module_index():
    path = create_file_path(
        output_dir="/tmp/downloads",
        course_name="Kubernetes",
        module_index=3,
        module_name="Networking",
        lesson_index=5,
        lesson_name="Services",
    )
    assert "03 - Networking" in str(path)


def test_create_file_path_truncates_overly_long_path():
    long_course = "A" * 100
    long_module = "B" * 100
    long_lesson = "C" * 100
    path = create_file_path(
        output_dir="/tmp/downloads",
        course_name=long_course,
        module_index=1,
        module_name=long_module,
        lesson_index=1,
        lesson_name=long_lesson,
    )
    assert len(str(path)) <= _MAX_PATH_LENGTH


def test_create_file_path_preserves_extension_when_shortening():
    long_base = "/tmp/" + ("X" * 150)
    path = create_file_path(
        output_dir=long_base,
        course_name="Course A",
        module_index=1,
        module_name="Module B",
        lesson_index=1,
        lesson_name=("Lesson C " * 4) + ".vtt",
    )
    assert len(str(path)) <= _MAX_PATH_LENGTH
    assert str(path).endswith(".vtt")


def test_download_course_handles_401_gracefully():
    from unittest.mock import MagicMock, patch

    import pytest
    import requests

    from kodekloud_downloader.main import download_course

    mock_course = MagicMock()
    mock_course.id = "test-course-id"
    mock_module = MagicMock()
    mock_module.id = "mod-1"
    mock_module.title = "Module 1"
    mock_lesson = MagicMock()
    mock_lesson.id = "lesson-1"
    mock_lesson.title = "Lesson 1"
    mock_lesson.type = "video"
    mock_module.lessons = [mock_lesson]
    mock_course.modules = [mock_module]

    with patch("requests.Session") as mock_session_cls:
        mock_session = MagicMock()
        mock_session_cls.return_value = mock_session

        mock_resp = MagicMock()
        mock_resp.status_code = 401
        http_err = requests.exceptions.HTTPError(
            "401 Client Error: Unauthorized", response=mock_resp
        )
        mock_resp.raise_for_status.side_effect = http_err
        mock_session.get.return_value = mock_resp

        with pytest.raises(SystemExit) as exc_info:
            download_course(
                course=mock_course,
                quality="720p",
                output_dir="/tmp/test",
                max_duplicate_count=3,
                auth=FakeAuth("invalid_token"),
                auto_enroll=False,
            )
        assert "401" in str(exc_info.value) or "expired" in str(exc_info.value).lower()


def test_download_resource_lesson_api_writes_markdown_and_downloads_resources(tmp_path):
    from unittest.mock import MagicMock, patch

    from kodekloud_downloader.main import download_resource_lesson

    mock_resp = MagicMock()
    mock_resp.status_code = 200
    mock_resp.json.return_value = {
        "id": "lesson-art-1",
        "title": "Course Slides",
        "type": "article",
        "content": "# Slides\nDownload: [Slides](https://example.com/slides.pdf)",
    }

    file_path = tmp_path / "01 - Intro" / "01 - Slides"

    with patch("requests.get", return_value=mock_resp) as mock_get, patch(
        "kodekloud_downloader.main.download_all_resources"
    ) as mock_dl_res:
        download_resource_lesson(
            lesson_url="https://learn.kodekloud.com/user/courses/test-slug/module/m1/lesson/lesson-art-1",
            file_path=file_path,
            auth=FakeAuth(),
            lesson_id="lesson-art-1",
            course_id="course-123",
        )

        assert mock_get.called
        assert (file_path.with_suffix(".md")).exists()
        assert "# Slides" in (file_path.with_suffix(".md")).read_text(encoding="utf-8")
        assert mock_dl_res.called
        # download_all_resources should receive the content and destination directory
        kwargs = mock_dl_res.call_args.kwargs
        args = mock_dl_res.call_args.args
        actual_content = kwargs.get("content") or (args[0] if args else None)
        actual_dest = kwargs.get("download_path") or (
            args[1] if len(args) > 1 else None
        )
        assert actual_content == mock_resp.json.return_value["content"]
        assert actual_dest == file_path.parent


def test_download_course_handles_article_lessons(tmp_path):
    from unittest.mock import MagicMock, patch

    from kodekloud_downloader.main import download_course

    mock_course = MagicMock()
    mock_course.id = "c-1"
    mock_course.slug = "c-slug"
    mock_course.title = "Sample Course"

    mock_module = MagicMock()
    mock_module.id = "m-1"
    mock_module.title = "Module One"

    mock_lesson = MagicMock()
    mock_lesson.id = "l-1"
    mock_lesson.title = "Course Deck"
    mock_lesson.type = "article"

    mock_module.lessons = [mock_lesson]
    mock_course.modules = [mock_module]

    with patch("requests.Session") as mock_session_cls, patch(
        "kodekloud_downloader.main.download_resource_lesson"
    ) as mock_dl_res_lesson:
        mock_session = MagicMock()
        mock_session_cls.return_value = mock_session

        download_course(
            course=mock_course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
            auto_enroll=False,
        )

        assert mock_dl_res_lesson.called
        assert mock_dl_res_lesson.call_args[1].get(
            "lesson_id"
        ) == "l-1" or mock_dl_res_lesson.call_args[0][0].endswith("l-1")


def test_download_course_global_sequential_indexing(tmp_path):
    from unittest.mock import MagicMock, patch

    from kodekloud_downloader.main import download_course

    mock_course = MagicMock()
    mock_course.id = "c-1"
    mock_course.slug = "c-slug"
    mock_course.title = "Sample Course"

    # Module 1 with 2 lessons
    m1 = MagicMock()
    m1.id = "m-1"
    m1.title = "Module 1"
    l1 = MagicMock(id="l-1", title="Lesson 1", type="article")
    l2 = MagicMock(id="l-2", title="Lesson 2", type="article")
    m1.lessons = [l1, l2]

    # Module 2 with 2 lessons
    m2 = MagicMock()
    m2.id = "m-2"
    m2.title = "Module 2"
    l3 = MagicMock(id="l-3", title="Lesson 3", type="article")
    l4 = MagicMock(id="l-4", title="Lesson 4", type="article")
    m2.lessons = [l3, l4]

    mock_course.modules = [m1, m2]

    captured_file_paths = []

    def fake_download_resource(lesson_url, file_path, *args, **kwargs):
        captured_file_paths.append(str(file_path))

    with patch("requests.Session"), patch(
        "kodekloud_downloader.main.download_resource_lesson",
        side_effect=fake_download_resource,
    ):
        download_course(
            course=mock_course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
            auto_enroll=False,
        )

    assert len(captured_file_paths) == 4
    # Check that lesson indexing increments across modules
    assert "001 - Lesson 1" in captured_file_paths[0]
    assert "002 - Lesson 2" in captured_file_paths[1]
    assert "003 - Lesson 3" in captured_file_paths[2]
    assert "004 - Lesson 4" in captured_file_paths[3]


def test_download_course_renews_token_on_401_and_retries(tmp_path):
    from unittest.mock import MagicMock, patch

    from kodekloud_downloader.main import download_course

    lesson = MagicMock(id="l-1", title="Lesson 1", type="video")
    module = MagicMock(id="m-1", title="Module 1", lessons=[lesson])
    course = MagicMock(id="c-1", slug="c", title="Course", modules=[module])

    auth = MagicMock()
    tokens = iter(["old", "new"])
    current = {"t": next(tokens)}
    auth.get_token.side_effect = lambda: current["t"]

    def refresh():
        current["t"] = next(tokens)
        return True

    auth.force_refresh.side_effect = refresh

    unauthorized = MagicMock(status_code=401)
    ok = MagicMock(status_code=200)
    ok.json.return_value = {"video_url": "https://player.vimeo.com/video/1"}

    with patch("requests.Session") as session_cls, patch(
        "kodekloud_downloader.main.download_video_lesson"
    ) as dl_video:
        session = session_cls.return_value
        session.get.side_effect = [unauthorized, ok]
        download_course(
            course=course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=auth,
            auto_enroll=False,
        )

    assert auth.force_refresh.call_count == 1
    sent = [c.kwargs["headers"]["Authorization"] for c in session.get.call_args_list]
    assert sent == ["Bearer old", "Bearer new"]
    assert dl_video.called


ENROLL_URL = "https://learn-api.kodekloud.com/api/users_progresses/enroll_course"
PROGRESS_URL = "https://learn-api.kodekloud.com/api/users_progresses/courses/c-1"


def _resp(status, payload=None):
    r = MagicMock(status_code=status, ok=200 <= status < 300)
    r.json.return_value = payload or {}
    return r


def test_ensure_enrolled_enrolls_when_not_enrolled():
    from kodekloud_downloader.main import ensure_enrolled

    session = MagicMock()
    session.get.side_effect = [
        _resp(200, {"enrolled": False}),
        _resp(200, {"enrolled": True}),  # re-check after enrolling
    ]
    session.post.return_value = _resp(200, {})

    assert ensure_enrolled(session, "c-1", "Course", FakeAuth("tok")) is True
    assert session.get.call_args.args[0] == PROGRESS_URL
    assert session.post.call_args.args[0] == ENROLL_URL
    # The web app's axios interceptor snake_cases bodies: course_id, not courseId
    assert session.post.call_args.kwargs["json"] == {"course_id": "c-1"}
    assert session.post.call_args.kwargs["headers"] == {"Authorization": "Bearer tok"}


def test_ensure_enrolled_skips_when_already_enrolled():
    from kodekloud_downloader.main import ensure_enrolled

    session = MagicMock()
    session.get.return_value = _resp(200, {"enrolled": True})

    assert ensure_enrolled(session, "c-1", "Course", FakeAuth()) is False
    assert not session.post.called


def test_ensure_enrolled_tries_when_status_unknown_and_tolerates_refusal():
    from kodekloud_downloader.main import ensure_enrolled

    session = MagicMock()
    session.get.return_value = _resp(404)
    session.post.return_value = _resp(403, {"message": "plan required"})

    assert ensure_enrolled(session, "c-1", "Course", FakeAuth()) is False
    assert session.post.called


def test_download_course_enrolls_before_downloading(tmp_path):
    from kodekloud_downloader.main import download_course

    course = MagicMock(id="c-1", slug="c", title="Course", modules=[])
    with patch("requests.Session") as session_cls, patch(
        "kodekloud_downloader.main.ensure_enrolled"
    ) as enroll:
        download_course(
            course=course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
        )
    enroll.assert_called_once_with(
        session_cls.return_value, "c-1", "Course", enroll.call_args.args[3]
    )


def test_download_course_no_enroll_flag_skips_enrollment(tmp_path):
    from kodekloud_downloader.main import download_course

    course = MagicMock(id="c-1", slug="c", title="Course", modules=[])
    with patch("requests.Session"), patch(
        "kodekloud_downloader.main.ensure_enrolled"
    ) as enroll:
        download_course(
            course=course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
            auto_enroll=False,
        )
    assert not enroll.called


def test_download_course_403_raises_enrollment_error(tmp_path):
    import requests

    from kodekloud_downloader.main import EnrollmentError, download_course

    lesson = MagicMock(id="l-1", title="Lesson 1", type="video")
    module = MagicMock(id="m-1", title="Module 1", lessons=[lesson])
    course = MagicMock(id="c-1", slug="c", title="Pro Course", modules=[module])

    forbidden = _resp(403, {"message": "Enrollment is required"})
    forbidden.raise_for_status.side_effect = requests.HTTPError(response=forbidden)

    with patch("requests.Session") as session_cls:
        session_cls.return_value.get.return_value = forbidden
        with pytest.raises(
            EnrollmentError, match=r"Pro Course.*Enrollment is required"
        ):
            download_course(
                course=course,
                quality="720p",
                output_dir=tmp_path,
                max_duplicate_count=3,
                auth=FakeAuth(),
                auto_enroll=False,
            )


def test_ensure_enrolled_reports_false_when_not_confirmed(caplog):
    from kodekloud_downloader.main import ensure_enrolled

    session = MagicMock()
    session.get.return_value = _resp(200, {"enrolled": False})
    session.post.return_value = _resp(200, {})

    assert ensure_enrolled(session, "c-1", "Course", FakeAuth()) is False
    assert "still reports you as not enrolled" in caplog.text


def test_download_course_lab_only_returns_zero_and_downloads_nothing(tmp_path):
    from kodekloud_downloader.main import download_course

    labs = [MagicMock(id=f"l{i}", title=f"Lab {i}", type="lab") for i in range(3)]
    module = MagicMock(id="m", title="Labs", lessons=labs)
    course = MagicMock(id="c", slug="c", title="Labs Only", modules=[module])
    with patch("requests.Session"), patch(
        "kodekloud_downloader.main.download_resource_lesson"
    ) as res:
        n = download_course(
            course=course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
            auto_enroll=False,
        )
    assert n == 0
    assert not res.called
    assert not (tmp_path / "KodeKloud").exists()


def test_migrate_legacy_lesson_renames_folders_and_files(tmp_path):
    from kodekloud_downloader.helpers import legacy_sanitize_filename
    from kodekloud_downloader.main import migrate_legacy_lesson

    args = (tmp_path, "AZ-204: Dev", 1, "Intro: Basics", 1, "What is CI/CD?")
    old = create_file_path(*args, sanitize=legacy_sanitize_filename)
    new = create_file_path(*args)
    assert old.parent.name == "01 - Intro_ Basics"
    assert new.parent.name == "01 - Intro - Basics"

    old.parent.mkdir(parents=True)
    (old.parent / (old.name + ".mkv")).write_text("video")
    (old.parent / (old.name + ".en.vtt")).write_text("subs")
    (old.parent / "slides.pdf").write_text("pdf")
    other = old.parent / "002 - Other lesson.mkv"
    other.write_text("other")

    migrate_legacy_lesson(old, new)

    assert not old.parents[1].exists()
    assert new.parents[1].name == "AZ-204 - Dev"
    assert (new.parent / "001 - What is CI-CD.mkv").read_text() == "video"
    assert (new.parent / "001 - What is CI-CD.en.vtt").read_text() == "subs"
    assert (new.parent / "slides.pdf").exists()
    assert (new.parent / "002 - Other lesson.mkv").exists()


def test_migrate_legacy_lesson_merges_into_existing_new_folder(tmp_path):
    from kodekloud_downloader.helpers import legacy_sanitize_filename
    from kodekloud_downloader.main import migrate_legacy_lesson

    args = (tmp_path, "A: B", 1, "M", 1, "L")
    old = create_file_path(*args, sanitize=legacy_sanitize_filename)
    new = create_file_path(*args)
    old.parent.mkdir(parents=True)
    (old.parent / "L1.mkv").write_text("old")
    (old.parent / "L2.mkv").write_text("old-dup")
    new.parent.mkdir(parents=True)
    (new.parent / "L2.mkv").write_text("new")

    migrate_legacy_lesson(old, new)

    assert (new.parent / "L1.mkv").read_text() == "old"
    assert (new.parent / "L2.mkv").read_text() == "new"  # never overwritten
    assert (old.parent / "L2.mkv").exists()  # conflict left behind, not lost


def test_download_course_skips_videos_already_on_disk(tmp_path):
    from kodekloud_downloader.main import download_course

    lessons = [
        MagicMock(id="l1", title="Done", type="video"),
        MagicMock(id="l2", title="Partial", type="video"),
    ]
    module = MagicMock(id="m", title="M", lessons=lessons)
    course = MagicMock(id="c", slug="c", title="C", modules=[module])
    done = create_file_path(tmp_path, "C", 1, "M", 1, "Done")
    done.parent.mkdir(parents=True)
    (done.parent / (done.name + ".mkv")).write_text("x")
    partial = create_file_path(tmp_path, "C", 1, "M", 2, "Partial")
    (partial.parent / (partial.name + ".mkv.part")).write_text("x")

    ok = _resp(200, {"video_url": "https://vimeo.com/2"})
    with patch("requests.Session") as session_cls, patch(
        "kodekloud_downloader.main.download_video_lesson"
    ) as dl:
        session_cls.return_value.get.return_value = ok
        download_course(
            course=course,
            quality="720p",
            output_dir=tmp_path,
            max_duplicate_count=3,
            auth=FakeAuth(),
            auto_enroll=False,
        )
    urls = [c.kwargs.get("params") for c in session_cls.return_value.get.call_args_list]
    assert len(urls) == 1  # only the partial lesson hit the API
    assert dl.call_count == 1
    assert dl.call_args.args[1] == partial
