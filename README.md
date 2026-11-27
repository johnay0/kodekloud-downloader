# KodeKloud Downloader

## Disclaimer
Please read the following disclaimer carefully before using the Downloader CLI Tool.

- The CLI Tool is intended for personal use only. By using this tool, you agree to use it at your own risk and assume full responsibility for any consequences that may arise from its use. The developers and contributors of this tool are not responsible for any damages or losses that may occur from its use.

- The use of this tool to download premium courses is for educational purposes only. You must have the proper authorization or permission from the course provider to access the content legally.

- It is strictly prohibited to distribute or share the downloaded content through any means, including but not limited to uploading to file-sharing platforms, torrent sites, or any other form of digital or physical distribution. Doing so is a violation of copyright laws and may result in legal consequences.

---

## Features

- [x] Download video lessons in selected quality
- [x] Organizes downloaded content in a structured folder hierarchy
- [x] Download resources (PDFs, etc.) alongside video lessons
- [x] Sign in with email/password; the session renews itself, even on multi-day downloads
- [x] Enrolls you in a course automatically before downloading it
- [x] `--all-paid`: download every Standard and Pro course, skipping Free ones

---

## ⚡ Go version (beta): parallel downloads, single binary

A Go rewrite lives in `cmd/` and `internal/`. It works the same way as the Python
tool, with the same commands, flags, environment variables, folder layout and session
cache, but downloads several lessons at once:

```console
# prebuilt binaries: see the GitHub releases page, or build it yourself:
go install github.com/johnay0/kodekloud-downloader/cmd/kodekloud@latest

kodekloud dl -o /data/kodekloud -e you@example.com --all-paid -j 6
```

- **Dependencies:** it needs [`yt-dlp`](https://github.com/yt-dlp/yt-dlp#installation)
  (the standalone binary is fine, no Python required) and `ffmpeg` on your `PATH`.
  Videos are still fetched by yt-dlp; Go handles sign-in, the catalog, enrollment,
  file naming, attachments and the parallel worker pool.
- **Parallelism:** `-j/--jobs` is how many lessons download at once (default 4).
  `--fragments` is how many pieces of each video yt-dlp fetches at once (default 8).
  The old tool downloaded one lesson at a time. That, not Python itself, is why a full
  run took days. More jobs help until your bandwidth is used up, so start with 4–8.
- **Picks up where Python left off:** it uses the same folder and file names (checked
  against every lesson in the live catalog). It also renames the old underscore names
  (`AZ-204_ …`), skips videos already on disk without any network calls, and reuses the
  saved login in `~/.cache/kodekloud-downloader/auth.json`.
- **Failures:** a course your plan doesn't include fails on its own; the rest keep going.
  Authentication problems stop the run cleanly, and so does Ctrl-C. Partial videos
  resume on the next run.
- **Quizzes:** `dl-quiz` is ported, but KodeKloud appears to have retired its public quiz
  backend (`mcq-backend-main.kodekloud.com` no longer resolves), so it currently fails in
  both versions.

## Prerequisites

- A KodeKloud account with email/password sign-in and a plan that includes the
  courses you want (you are enrolled automatically, see below)
- Python 3.8 or higher
- ffmpeg

<details>
    <summary>How to Install FFmpeg on Linux, Mac, and Windows</summary>

# How to Install FFmpeg on Linux, Mac, and Windows

FFmpeg is a powerful and flexible multimedia processing tool that can handle a wide range of tasks, such as video and audio conversion, streaming, and recording. In this guide, we will show you how to install FFmpeg on Linux, Mac, and Windows operating systems.

## Linux

### Ubuntu and Debian-based distributions

1.  Update the package lists for upgrades and new package installations.

`sudo apt update`

1.  Install FFmpeg using the following command:

`sudo apt install ffmpeg`

### Fedora

1.  Install FFmpeg using the following command:

`sudo dnf install ffmpeg`

### Arch Linux and Manjaro

1.  Install FFmpeg using the following command:

`sudo pacman -S ffmpeg`

## Mac

### Using Homebrew

If you don't have Homebrew installed, you can install it by following the instructions on the [official Homebrew website](https://brew.sh/).

Update Homebrew to ensure you have the latest package information:

`brew update`

1.  Install FFmpeg using the following command:

`brew install ffmpeg`

### Using MacPorts

If you don't have MacPorts installed, you can install it by following the instructions on the [official MacPorts website](https://www.macports.org/install.php).

Update MacPorts to ensure you have the latest package information:

`sudo port selfupdate`

1.  Install FFmpeg using the following command:

`sudo port install ffmpeg`

## Windows

### Using Chocolatey

If you don't have Chocolatey installed, you can install it by following the instructions on the [official Chocolatey website](https://chocolatey.org/install).

Open an elevated Command Prompt (run as Administrator) and install FFmpeg using the following command:

`choco install ffmpeg`

### Using Scoop

If you don't have Scoop installed, you can install it by following the instructions on the [official Scoop website](https://scoop.sh/).

Open a PowerShell terminal and install FFmpeg using the following command:

`scoop install ffmpeg`

After following these steps, FFmpeg should be installed on your system. You can check the installation by running the following command in your terminal or command prompt:

`ffmpeg -version`

This will display the FFmpeg version and build information, confirming that the installation was successful.

</details>



## 🔐 Authentication

Sign in with your KodeKloud email and password:

```console
kodekloud dl -o . -e you@example.com      # prompts for the password once
# or, for scripts/servers:
export KODEKLOUD_EMAIL=you@example.com KODEKLOUD_PASSWORD='...'
kodekloud dl -o . --all-paid
```

- **Renewal:** KodeKloud's API tokens expire after about an hour. The
  downloader renews them on its own (before they expire, and after any 401),
  so long downloads keep running.
- **Password:** it is only sent to Google's Firebase sign-in endpoint (the same
  one the website uses) and is never written to disk.
- **Saved session:** what is saved is the session's refresh token, in
  `~/.cache/kodekloud-downloader/auth.json` (mode `0600`). Later runs need no
  credentials at all. `--no-auth-cache` turns this off, and deleting the file
  signs you out.
- **Token safety:** your token is only ever sent to `https://*.kodekloud.com`,
  never to third-party links found in lesson content.

## 🎓 Enrollment

KodeKloud refuses to serve a course's lessons unless you are enrolled in it.
The website enrolls you automatically when you open a lesson, and the
downloader does the same before downloading each course:

- **Already enrolled:** it does nothing.
- **Not enrolled:** it calls the same enroll endpoint the website uses. Those
  courses show up under "My Courses" afterwards.
- **No access:** if your plan does not include a course (for example a Pro
  course on a Standard plan), the download stops with a clear "denied access"
  message. With `--all-paid`, that course is listed as failed and the rest
  continue.
- **Opting out:** pass `--no-enroll` to never enroll automatically.

## 💻 Installation

```console
pip install -U kodekloud-downloader
```

You can also install the latest main changes:

```console
pip install -U git+https://github.com/johnay0/kodekloud-downloader.git
```

## 🛠️ Development Setup

### Prerequisites
- Python 3.8 or higher
- [uv](https://docs.astral.sh/uv/) (fast Python package manager)

### Setup

Clone the repository and install dependencies:

```console
git clone https://github.com/johnay0/kodekloud-downloader.git
cd kodekloud-downloader
uv sync --group dev
```

### Code Quality

This project uses [Ruff](https://docs.astral.sh/ruff/) for linting and formatting:

```console
# Lint
uv run ruff check src/ tests/

# Format
uv run ruff format src/ tests/
```

### Type Checking

```console
uv run mypy src/
```

### Running Tests

```console
uv run pytest --cov=src/
```

## Try in Browser
[![Open In Colab](https://colab.research.google.com/assets/colab-badge.svg)](https://colab.research.google.com/drive/1GsgFcqa_43GYeDKmoa0CXsRfDySrzvzT?usp=sharing)

## 📚 Usage

After installing the package, you can use the `kodekloud dl` command to download courses:

```console
kodekloud dl --help
Usage: kodekloud dl [OPTIONS] [COURSE_URL]

Options:
  -q, --quality [360p|480p|540p|720p|1080p]
                                  Quality of the video to be downloaded.
  -o, --output-dir TEXT           Output directory where downloaded files will
                                  be store.
  -e, --email TEXT                KodeKloud account email (env:
                                  KODEKLOUD_EMAIL). Only needed for the first
                                  run; the session is cached and renewed
                                  automatically.
  -p, --password TEXT             KodeKloud account password (env:
                                  KODEKLOUD_PASSWORD). Prompted for if --email
                                  is given without it. Never written to disk.
  --auth-state TEXT               Where the session (refresh token, never the
                                  password) is cached with 0600 permissions so
                                  later runs need no credentials. Env:
                                  KODEKLOUD_AUTH_STATE.  [default:
                                  ~/.cache/kodekloud-
                                  downloader/auth.json]
  --no-auth-cache                 Do not read or write the session cache.
  --no-enroll                     Do not auto-enroll in courses you are not
                                  enrolled in yet (KodeKloud refuses lesson
                                  downloads for those).
  -mdc, --max-duplicate-count INTEGER
                                  If same video is downloaded this many times,
                                  then download stops
  -s, --search TEXT               Filter course list by keyword (title,
                                  instructor, or category).
  -f, --filter, --category TEXT   Filter course list by category (e.g.
                                  'Kubernetes', 'Security', 'Golden
                                  Kubestronaut').
  --plan TEXT                     Only list courses on these plans, comma-
                                  separated (Free, Standard, Pro).
  --all-paid                      Download every Standard and Pro course (Free
                                  ones are skipped) without prompting. Combine
                                  with -s/-f to narrow it down.
  --dry-run                       With --all-paid: only list the courses that
                                  would be downloaded.
  --help                          Show this message and exit.
```

Here is an example:

### 💰 Download every Standard and Pro course (skip Free)

```console
# See what would be downloaded (no login needed)
kodekloud dl --all-paid --dry-run

# Download all of them, unattended (auto sign-in renewal + auto-enroll)
kodekloud dl -o /data/kodekloud -e you@example.com --all-paid

# Only part of the paid catalog
kodekloud dl -o . -e you@example.com --all-paid -f "Kubernetes"
```

If one course fails, the rest still download, and the failed ones are listed at the end.
Courses made only of interactive labs (about 9 "Learn By Doing" courses) have nothing to
download; they are listed as skipped instead of creating empty folders.

Re-running is cheap and safe. Videos already on disk are skipped right away (no API
or Vimeo calls). Folders and files from older versions, which turned `:`, `/` and `|`
into `_` (e.g. `AZ-204_ Developing…`), are renamed to the readable names
(`AZ-204 - Developing…`) instead of being downloaded again. `--plan Standard,Pro`
applies the same plan filter to the interactive course picker.

### 🔍 Download entire course from list of available course in current folder

```css
kodekloud dl -o . -e you@example.com
+-----+--------------------------------------------------------------------------------------+----------+------------------------------------------------------------------------------------------+
| No. | Name                                                                                 | Type     | Categories                                                                               |
+-----+--------------------------------------------------------------------------------------+----------+------------------------------------------------------------------------------------------+
| 1   | Kubernetes Administration: Package Management with Glasskube                         | Standard | Kubernetes                                                                               |
| 2   | Linux System Administration for Beginners                                            | Standard |                                                                                          |
| 3   | GKE - Google Kubernetes Engine                                                       | Standard | Cloud, Containers, DevOps, Kubernetes, Container Orchestration, GCP                      |
| 4   | DevOps Interview Preparation Course                                                  | Standard | DevOps                                                                                   |
| 5   | Kustomize                                                                            | Standard | DevOps, Kubernetes                                                                       |
| 6   | AWS Lambda                                                                           | Pro      | AWS, Cloud                                                                               |
| 7   | AWS Solutions Architect Associate Certification                                      | Pro      | AWS, Certification, Cloud                                                                |
| 8   | Amazon Elastic Compute Cloud (EC2)                                                   | Pro      | AWS, Cloud, DevOps, Infrastructure as Code (IaC)                                         |
| 9   | Learn By Doing: HashiCorp Nomad                                                      | Standard | HashiCorp                                                                                |
| 10  | GIT for Beginners                                                                    | Standard | DevOps, Programming                                                                      |
| 11  | Spacelift: Elevate Your Infrastructure Deployment                                    | Standard | DevOps, Infrastructure as Code (IaC)                                                     |
| 12  | Introduction to Sealed Secrets in Kubernetes                                         | Standard | Kubernetes                                                                               |
| 13  | Learn by Doing - Prompt Engineering 101                                              | Free     |                                                                                          |
| 14  | Prometheus Certified Associate (PCA)                                                 | Standard | Certification, DevOps, Kubernetes, Monitoring                                            |
| 15  | Learning Linux Basics Course & Labs                                                  | Standard | Linux, DevOps                                                                            |
| 16  | Helm for Beginners                                                                   | Standard | DevOps, Kubernetes, Container Orchestration, Containers                                  |
| 17  | Exploring WebAssembly (WASM)                                                         | Standard | DevOps, Cloud                                                                            |
| 18  | Docker Certified Associate Exam Course                                               | Standard | Certification, Containers, DevOps                                                        |
| 19  | HashiCorp Packer                                                                     | Standard | DevOps, HashiCorp                                                                        |
| 20  | Learn By Doing: AWS Workshop with Terraform                                          | Standard | AWS, Infrastructure as Code (IaC)                                                        |
| 21  | Kubernetes and Cloud-Native Associate (KCNA)                                         | Standard | Certification, Containers, Kubernetes, Container Orchestration                           |
| 22  | DP-900: Microsoft Azure Data Fundamentals                                            | Standard | Certification, Cloud                                                                     |
| 23  | Learn Ansible Basics - Beginners Course                                              | Standard | Automation, DevOps, Infrastructure as Code (IaC)                                         |
| 24  | Ultimate Certified Kubernetes Administrator (CKA) Mock Exam Series                   | Standard | Challenges, Containers, Kubernetes                                                       |
| 25  | 12 Factor App                                                                        | Standard |                                                                                          |
| 26  | Docker - SWARM | SERVICES | STACKS - Hands-on                                        | Standard | Containers, DevOps                                                                       |
| 27  | Fundamentals of DevOps                                                               | Standard | DevOps                                                                                   |
| 28  | Kubernetes Networking Deep Dive                                                      | Standard | Kubernetes                                                                               |
| 29  | Terraform Basics Training Course                                                     | Standard | Automation, DevOps, Infrastructure as Code (IaC)                                         |
| 30  | CKA Certification Course - Certified Kubernetes Administrator                        | Standard | Containers, Certification, Container Orchestration, DevOps, Kubernetes                   |
| 31  | Chaos Engineering                                                                    | Standard | AWS                                                                                      |
| 32  | Mastering Generative AI with OpenAI                                                  | Standard | Programming                                                                              |
| 33  | Kubernetes Challenges                                                                | Free     | Containers, DevOps, Kubernetes                                                           |
| 34  | HashiCorp Certified: Vault Associate Certification                                   | Standard | Certification, DevOps, HashiCorp                                                         |
| 35  | OpenTofu: A Beginners Guide to a Terraform Fork Including Migration From Terraform   | Standard | DevOps, Infrastructure as Code (IaC)                                                     |
| 36  | Ultimate Certified Kubernetes Application Developer (CKAD) Mock Exam Series          | Standard | Challenges, Containers, Kubernetes                                                       |
| 37  | Learn By Doing: Deploying and Managing the EFK Stack on Kubernetes                   | Standard | DevOps, Kubernetes                                                                       |
| 38  | Grafana Loki                                                                         | Standard | DevOps, Kubernetes, Monitoring                                                           |
| 39  | Linode : Kubernetes Engine                                                           | Free     | Containers, Kubernetes                                                                   |
| 40  | Microsoft Azure Security Technologies (AZ-500)                                       | Standard | Certification, Cloud, Security                                                           |
| 41  | Learn-By-Doing Kubernetes Network Policies                                           | Standard | Challenges, Containers, Kubernetes, Container Orchestration                              |
| 42  | Kubernetes for the Absolute Beginners - Hands-on Tutorial                            | Standard | Containers, DevOps, Kubernetes, Container Orchestration                                  |
| 43  | HashiCorp : Terraform Cloud                                                          | Standard | Cloud, DevOps, HashiCorp                                                                 |
| 44  | AWS RDS                                                                              | Pro      | AWS, Cloud                                                                               |
| 45  | AWS Networking Fundamentals                                                          | Pro      | AWS                                                                                      |
| 46  | Istio Service Mesh                                                                   | Standard | DevOps, Kubernetes                                                                       |
| 47  | Learn By Doing: Kubernetes Policies with Kyverno                                     | Standard | DevOps, Kubernetes                                                                       |
| 48  | Docker Training Course for the Absolute Beginner                                     | Free     | DevOps, Containers                                                                       |
| 49  | GitLab CI/CD: Architecting, Deploying, and Optimizing Pipelines                      | Standard | CI/CD                                                                                    |
| 50  | AWS Cloud Practitioner (CLF-C02)                                                     | Standard | AWS, Cloud, DevOps                                                                       |
| 51  | GitHub Actions                                                                       | Standard | Automation                                                                               |
| 52  | OpenShift 3 for the Absolute Beginners                                               | Standard | Container Orchestration, Containers, DevOps                                              |
| 53  | Linux Foundation Certified System Administrator (LFCS)                               | Standard | DevOps, Linux, Certification                                                             |
| 54  | DevOps Pre-Requisite Course                                                          | Standard | DevOps                                                                                   |
| 55  | Linux Challenges                                                                     | Free     | DevOps, Linux                                                                            |
| 56  | GitOps with FluxCD                                                                   | Standard | CI/CD, DevOps, Kubernetes                                                                |
| 57  | Amazon Simple Storage Service (Amazon S3)                                            | Pro      | AWS, Cloud                                                                               |
| 58  | Postman Essentials                                                                   | Standard | DevOps                                                                                   |
| 59  | GCP DevOps Project                                                                   | Standard | Cloud, GCP                                                                               |
| 60  | Lens - Kubernetes IDE                                                                | Free     | Kubernetes                                                                               |
| 61  | Python Basics                                                                        | Standard | Certification, DevOps, Programming, Python                                               |
| 62  | [Updated] AZ-104: Microsoft Azure Administrator                                      | Standard | Certification, DevOps, Cloud                                                             |
| 63  | Hands-On AWS Project: Deploy Your First Crypto App                                   | Pro      | AWS                                                                                      |
| 64  | Shell Scripts for Beginners                                                          | Standard | Challenges, DevOps                                                                       |
| 65  | CKS - Challenges                                                                     | Free     | Challenges, Containers, Container Orchestration, DevOps                                  |
| 66  | DevSecOps - Kubernetes DevOps & Security                                             | Standard | DevOps, DevSecOps, Kubernetes                                                            |
| 67  | OpenShift 4                                                                          | Standard | Container Orchestration, DevOps, Red Hat                                                 |
| 68  | AWS - IAM                                                                            | Pro      | AWS, Cloud                                                                               |
| 69  | HashiCorp Certified: Consul Associate Certification                                  | Standard | Certification, DevOps, HashiCorp                                                         |
| 70  | Jinja2 Basics (Mini Course)                                                          | Free     | DevOps                                                                                   |
| 71  | Open Source for Beginners                                                            | Standard |                                                                                          |
| 72  | GitOps with ArgoCD                                                                   | Standard | DevOps, Kubernetes, CI/CD                                                                |
| 73  | EFK Stack: Enterprise-Grade Logging and Monitoring                                   | Standard | Certification                                                                            |
| 74  | AWS CloudWatch                                                                       | Pro      | AWS, Cloud, Monitoring                                                                   |
| 75  | Linux Professional Institute LPIC-1 Exam 101                                         | Standard | Certification, Linux                                                                     |
| 76  | Terraform Associate Certification: HashiCorp Certified                               | Standard | Certification, Container Orchestration, Containers, DevOps, Infrastructure as Code (IaC) |
| 77  | GCP Cloud Digital Leader Certification                                               | Standard | Certification, Cloud, GCP                                                                |
| 78  | AZ900: Microsoft Azure Fundamentals                                                  | Standard | Certification, Cloud                                                                     |
| 79  | Learn By Doing - MariaDB                                                             | Standard |                                                                                          |
| 80  | Amazon Elastic Container Service (AWS ECS)                                           | Free     | AWS, Cloud, DevOps                                                                       |
| 81  | Golang                                                                               | Standard | DevOps, Programming                                                                      |
| 82  | AWS EKS                                                                              | Pro      | AWS, Cloud                                                                               |
| 83  | GitHub Actions Certification                                                         | Standard | Automation                                                                               |
| 84  | Enhancing Soft Skills for DevOps Engineers: Essential Non-Technical Skills to Thrive | Standard | DevOps                                                                                   |
| 85  | JSON Path Test - Free Course                                                         | Free     | DevOps                                                                                   |
| 86  | Terragrunt for Beginners                                                             | Standard | DevOps, Infrastructure as Code (IaC)                                                     |
| 87  | Introduction to K8sGPT and AI-Driven Kubernetes Engineering                          | Standard | DevOps, Kubernetes                                                                       |
| 88  | HashiCorp Certified: Vault Operations Professional 2022                              | Standard | Certification, DevOps, HashiCorp, Container Orchestration, Containers                    |
| 89  | AWS CodePipeline (CI/CD Pipeline)                                                    | Pro      | AWS, Cloud, DevOps                                                                       |
| 90  | LangChain                                                                            | Standard | Programming                                                                              |
| 91  | Ansible Advanced Course                                                              | Standard | Automation, DevOps, Infrastructure as Code (IaC)                                         |
| 92  | Certified Kubernetes Security Specialist (CKS)                                       | Standard | Containers, DevOps, Kubernetes, Certification, Container Orchestration                   |
| 93  | Pulumi Essentials                                                                    | Standard | DevOps, Infrastructure as Code (IaC)                                                     |
| 94  | Certified Kubernetes Application Developer (CKAD)                                    | Standard | Containers, DevOps, Kubernetes, Certification, Container Orchestration                   |
| 95  | Jenkins                                                                              | Standard | CI/CD, DevOps, Automation                                                                |
| 96  | Advanced Bash Scripting                                                              | Standard | Linux                                                                                    |
| 97  | PCAP - Python Certification Course                                                   | Standard | Certification, DevOps, Programming, Python                                               |
| 98  | Advanced Golang                                                                      | Standard | Programming                                                                              |
| 99  | Azure Kubernetes Service                                                             | Standard | Cloud, DevOps, Kubernetes                                                                |
| 100 | AZ-305: Microsoft Azure Solutions Architect Expert                                   | Standard | Certification, Cloud, DevOps                                                             |
| 101 | Terraform Challenges                                                                 | Free     | Containers, DevOps, Infrastructure as Code (IaC)                                         |
| 102 | CKA Certification Course - Certified Kubernetes Administrator                        | Standard | Containers, Certification, Container Orchestration, DevOps, Kubernetes                   |
| 103 | AWS Certified Developer - Associate                                                  | Pro      | AWS, Certification, Cloud                                                                |
+-----+--------------------------------------------------------------------------------------+----------+------------------------------------------------------------------------------------------+
Enter the courses you want to select (Multiple courses can be passes using this format 1,6-9,10-11): 1-3,7-9,10,13,55
```

![](static/demo-select-download.gif)

You can also use `kodekloud dl-quiz` to download all quiz in a markdown file.

```css
kodekloud dl-quiz --help
Usage: kodekloud dl-quiz [OPTIONS]

Options:
  -o, --output-dir TEXT  Output directory where quiz markdown file will be
                         saved.
  --sep                  Write in seperate markdown files.
  --help                 Show this message and exit.
```

Here is an example:

`kodekloud dl-quiz -o KodeKloudQuiz`

or download seperate markdown file.

`kodekloud dl-quiz -o KodeKloudQuiz --sep`

To convert the quiz markdown to PDF you can use any online PDF converter like [aconvert](https://www.aconvert.com/pdf/md-to-pdf/), or [md2pdf](https://md2pdf.netlify.app/). You can also use [`grip`](https://github.com/joeyespo/grip). Or you can also create a [GitHub Gist](https://gist.github.com/) and then use your browser to convert the page into PDF.

![](./static/KodeKloud_Quiz.png)

## Colab Notebook to download directly in Personal GDrive

You can open [this notebook](https://colab.research.google.com/github/johnay0/kodekloud-downloader/blob/main/colab_notebook.ipynb) with your google account were you want to download the course. Please note that you should open with the same account for which you want to link the drive.

## Troubleshoot

If you see `KodeKloud denied access to '<course>'`, your plan does not include
that course (or enrolling failed). Check the course on the website.

---

If you see `Authentication failed (401 Unauthorized)` or `KodeKloud sign-in
failed`, sign in again with `-e you@example.com` (wrong password, or the cached
session was revoked, e.g. after a password change).


