# KodeKloud Downloader

Download the KodeKloud courses your plan includes: videos, subtitles, lesson notes
and attachments, in a tidy folder per course. One static binary, with lessons
downloaded in parallel.

## Disclaimer

- This tool is intended for personal use only. You use it at your own risk and are
  responsible for any consequences. The developers and contributors are not
  responsible for any damages or losses.
- Downloading premium courses is for educational purposes only. You must have the
  proper authorization from the course provider to access the content.
- Distributing or sharing downloaded content in any way (file-sharing platforms,
  torrents, or any other digital or physical distribution) is strictly prohibited
  and violates copyright law.

---

## Features

- Signs in with email/password and renews the session on its own, even during
  multi-day downloads
- Enrolls you in a course automatically before downloading it, as the website does
- `--all-paid`: every Standard and Pro course, skipping Free ones and lab-only courses
- Downloads several lessons at once (`-j`), and several pieces of each video at once (`--fragments`)
- Re-runs only fetch what is missing: finished videos are skipped without any network calls
- Videos in the quality you choose, with subtitles; lesson notes as Markdown; attached
  PDFs and other files next to the lesson
- A course your plan doesn't include fails on its own while the rest keep going

## Install

**Prebuilt binaries** for Linux, macOS and Windows (amd64 and arm64) are attached to
each [GitHub release](https://github.com/johnay0/kodekloud-downloader/releases).

**Or build it** (Go 1.26+):

```console
go install github.com/johnay0/kodekloud-downloader/cmd/kodekloud@latest
# or, from a clone:
go build -o kodekloud ./cmd/kodekloud
```

### Requirements

- **yt-dlp**, which fetches the videos. Use the **standalone build**
  (`yt-dlp_linux`, `yt-dlp_linux_aarch64`, `yt-dlp_macos` or `yt-dlp.exe` from the
  [yt-dlp releases](https://github.com/yt-dlp/yt-dlp/releases)). It includes browser
  impersonation (`curl_cffi`), which Vimeo increasingly requires. The plain `yt-dlp`
  zip, apt's package and a bare `pip install yt-dlp` don't include it.
  `yt-dlp --list-impersonate-targets` should show `curl_cffi` without
  "(unavailable)". Put it on your `PATH`, or pass its location with `--yt-dlp`.
- **ffmpeg**, which joins video and audio: `sudo apt install ffmpeg`,
  `brew install ffmpeg`, `choco install ffmpeg` or `scoop install ffmpeg`.
- A KodeKloud account with email/password sign-in, on a plan that includes the
  courses you want.

## 🔐 Sign-in

```console
kodekloud dl -o ~/kodekloud -e you@example.com      # asks for the password once
# or, for scripts and servers:
export KODEKLOUD_EMAIL=you@example.com KODEKLOUD_PASSWORD='...'
```

Prefer the prompt or the environment variables to `-p`: a password on the command
line ends up in your shell history.

- **Renewal:** KodeKloud's API tokens expire after about an hour. They are renewed
  before they expire, and after any 401, so long downloads keep running.
- **Password:** it is only sent to Google's Firebase sign-in endpoint (the one the
  website uses) and is never written to disk.
- **Saved session:** the session's refresh token is saved in
  `~/.cache/kodekloud-downloader/auth.json` (mode `0600`), so later runs need no
  credentials. `--no-auth-cache` turns this off; deleting the file signs you out.
- **Token safety:** your token is only ever sent to `https://*.kodekloud.com`, never
  to third-party links found in lesson content.

## 🎓 Enrollment

KodeKloud only serves a course's lessons once you are enrolled in it. The website
enrolls you when you open a lesson, and the downloader does the same before each course:

- **Already enrolled:** nothing happens.
- **Not enrolled:** it calls the same enroll endpoint as the website, so the course
  then appears under "My Courses".
- **No access:** if your plan does not include the course (for example a Pro course
  on a Standard plan), that course fails with a clear "denied access" message.
- **Opting out:** `--no-enroll` never enrolls you automatically.

## 📚 Usage

```console
kodekloud dl --help
Download a course, courses picked from the catalog, or every paid course

Usage:
  kodekloud dl [COURSE_URL] [flags]

Flags:
      --all-paid                  Download every Standard and Pro course (Free ones skipped) without prompting
      --auth-state string         Session cache file, mode 0600 (env: KODEKLOUD_AUTH_STATE) (default "~/.cache/kodekloud-downloader/auth.json")
      --dry-run                   With --all-paid: only list the courses
  -e, --email string              KodeKloud account email (env: KODEKLOUD_EMAIL); only needed for the first run
  -f, --filter string             Filter courses by category, comma-separated (alias: --category)
      --fragments int             Fragments per video yt-dlp downloads in parallel (-N) (default 8)
  -h, --help                      help for dl
  -j, --jobs int                  Lessons to download in parallel (default 4)
      --max-duplicate-count int   Fail a course if the same video comes back more than this many times (default 3)
      --no-auth-cache             Do not read or write the session cache
      --no-enroll                 Do not auto-enroll in courses you are not enrolled in yet
  -o, --output-dir string         Output directory (default "~/Downloads")
  -p, --password string           KodeKloud password (env: KODEKLOUD_PASSWORD); prompted for if missing. Never written to disk
      --plan string               Only list courses on these plans, comma-separated (Free, Standard, Pro)
  -q, --quality string            Video quality: 360p, 480p, 540p, 720p or 1080p (default "1080p")
  -s, --search string             Filter courses by keyword (title, instructor, category)
      --yt-dlp string             yt-dlp executable (default "yt-dlp")

Global Flags:
  -v, --verbose count   Increase log verbosity (-vv for debug)
```

### 💰 Every Standard and Pro course

```console
# See what would be downloaded (no sign-in needed)
kodekloud dl --all-paid --dry-run

# Download all of them, unattended
kodekloud dl -o ~/kodekloud --all-paid -j 8

# Only part of the paid catalog
kodekloud dl -o ~/kodekloud --all-paid -f Kubernetes
```

The run ends with a summary: courses completed, lessons downloaded or already on disk,
lab-only courses skipped (interactive labs have nothing to download), and courses with
failed lessons. Re-running the same command is cheap: finished videos are skipped
without contacting KodeKloud or Vimeo, so it only picks up new courses and earlier
failures. Half-finished videos resume where they stopped.

### 🎯 One course

```console
kodekloud dl -o ~/kodekloud "https://learn.kodekloud.com/user/courses/helm-for-beginners"
```

### 🔍 Pick from the catalog

Without a URL, the catalog is shown as a table (narrow it with `-s`, `-f` or `--plan`):

```console
kodekloud dl -o ~/kodekloud --plan Standard,Pro
...
Enter course number(s) to select (e.g. 1,6-9), search keyword, 'c:<cat>', or 'all'/'cats'/'q':
```

Enter numbers and ranges (`1,6-9`), a keyword to search, `c:<category>` to filter by
category, `cats` to list categories, `all` to show everything again, or `q` to quit.

### ⚡ Speed

`-j/--jobs` sets how many lessons download at once, and `--fragments` how many pieces of
each video yt-dlp fetches at once. Your bandwidth is the limit, so raise `-j` until
throughput stops growing; 8–16 is plenty for most connections. Very high values (e.g.
`-j 100` means up to 800 connections) mostly invite throttling from Vimeo.

### 📁 Output

```text
<output-dir>/KodeKloud/
  Certified Kubernetes Administrator (CKA)/
    01 - Introduction/
      001 - Course Introduction.mkv
      001 - Course Introduction.en.vtt
      002 - Certification Details.md
      ...
```

Lessons are numbered across the whole course (labs included). Characters that
Windows forbids are replaced with readable stand-ins (`AZ-204: Dev` becomes
`AZ-204 - Dev`, `CI/CD` becomes `CI-CD`), and paths are kept under 220 characters.

### 📝 Quizzes

`kodekloud dl-quiz -o <dir> [--sep]` exported KodeKloud's public quizzes to Markdown.
KodeKloud has retired that quiz backend (`mcq-backend-main.kodekloud.com` no longer
resolves), so it currently fails with an explanation.

## Troubleshooting

- **`KodeKloud denied access to '<course>'`**: your plan does not include the course,
  or enrolling failed. Check the course on the website.
- **`Authentication failed (401 Unauthorized)` or `KodeKloud sign-in failed`**: sign in
  again with `-e you@example.com`. Common causes are a wrong password, or a saved
  session that was revoked, for example after a password change.
- **`your yt-dlp cannot impersonate a browser`** or **`This request has been blocked due
  to its TLS fingerprint`**: install the standalone yt-dlp build (see Requirements).
- **`Got HTTP Error 403 when using impersonate target`**: Vimeo refuses that video from
  your IP address. If the lesson says "This video isn't available right now" on the
  website too, the video is broken on KodeKloud's side. Otherwise try from a home
  connection: Vimeo blocks some data-centre and VPN addresses.
- **A lesson keeps failing with HTTP 404:** run with `-v` and look at the `video_url`
  KodeKloud returns. Some lessons link to a video that doesn't exist (e.g.
  `https://vimeo.com/10`); that needs fixing on KodeKloud's side.

## Development

```console
gofmt -l .                 # formatting
go vet ./...
go test -race ./...
```

Tests run against fake KodeKloud, Firebase and yt-dlp stand-ins; no account is
needed. `internal/naming/testdata` pins the folder and file name of every lesson in the
catalog. Re-runs find finished lessons by those exact names, so if that test fails, fix
the code rather than the fixture.

Pushing a `v*` tag builds the release binaries (`.github/workflows/release.yml`).
