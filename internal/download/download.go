// Package download downloads courses with a pool of parallel workers.
package download

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/johnay0/kodekloud-downloader/internal/api"
	"github.com/johnay0/kodekloud-downloader/internal/auth"
	"github.com/johnay0/kodekloud-downloader/internal/naming"
	"github.com/johnay0/kodekloud-downloader/internal/resources"
)

// Options configure a download run.
type Options struct {
	OutputDir         string
	Quality           string // "1080p"
	Jobs              int    // lessons downloaded in parallel
	Fragments         int    // yt-dlp -N (fragments per video in parallel)
	YtDlp             string // yt-dlp executable
	AutoEnroll        bool
	MaxDuplicateCount int
}

// AccessError means KodeKloud refuses the course (not enrolled / plan too low).
type AccessError struct{ Msg string }

func (e *AccessError) Error() string { return e.Msg }

// Result summarises one course.
type Result struct {
	Course       api.Course
	Downloadable int // video+article lessons; 0 = lab-only course
	Downloaded   atomic.Int64
	Skipped      atomic.Int64 // already on disk
	Failed       atomic.Int64
	mu           sync.Mutex
	err          error
	ctx          context.Context
	cancel       context.CancelCauseFunc
	videos       map[string]int // duplicate-video detection
}

// Err is the reason the whole course failed, if any.
func (r *Result) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Result) fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	r.cancel(err)
}

type job struct {
	res      *Result
	course   *api.CourseDetail
	lesson   api.Lesson
	path     string // lesson path without extension
	label    string
	position string
}

// VideoRunner downloads one video; replaceable in tests.
type VideoRunner func(ctx context.Context, videoURL, outBase string) ([]byte, error)

type Downloader struct {
	Opt       Options
	API       *api.Client
	Resources *resources.Downloader
	Log       *slog.Logger
	Out       io.Writer // progress lines
	RunVideo  VideoRunner

	outMu sync.Mutex
}

func (d *Downloader) printf(format string, args ...any) {
	d.outMu.Lock()
	defer d.outMu.Unlock()
	fmt.Fprintf(d.Out, format, args...)
}

// IsFatal reports errors that should stop the whole run.
func IsFatal(err error) bool {
	var ae *auth.Error
	return errors.Is(err, api.AuthFailedError) || errors.As(err, &ae)
}

// Run downloads courses and returns one Result per course that was started.
// The error is non-nil only for run-wide failures (auth, Ctrl-C).
func (d *Downloader) Run(ctx context.Context, courses []api.Course) ([]*Result, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if d.RunVideo == nil {
		d.RunVideo = d.ytdlp
	}
	jobs := make(chan job, max(1, d.Opt.Jobs)*4)

	var wg sync.WaitGroup
	for range max(1, d.Opt.Jobs) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if err := d.process(ctx, j); IsFatal(err) {
					cancel(err)
				}
			}
		}()
	}

	var results []*Result
	for i, c := range courses {
		if ctx.Err() != nil {
			break
		}
		res, js, err := d.prepare(ctx, c, fmt.Sprintf("%d/%d", i+1, len(courses)))
		if IsFatal(err) {
			cancel(err)
			break
		}
		results = append(results, res)
		for _, j := range js {
			select {
			case jobs <- j:
			case <-ctx.Done():
			}
		}
	}
	close(jobs)
	wg.Wait()
	for _, r := range results {
		r.cancel(nil) // release per-course contexts
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return results, cause
	}
	return results, ctx.Err()
}

func (d *Downloader) prepare(ctx context.Context, c api.Course, position string) (*Result, []job, error) {
	res := &Result{Course: c, videos: map[string]int{}}
	res.ctx, res.cancel = context.WithCancelCause(ctx)
	d.printf("\n[%s] %s (%s)\n", position, c.Title, c.Plan)

	detail, err := d.API.CourseDetail(ctx, c.Slug)
	if err != nil {
		res.fail(fmt.Errorf("fetching course details: %w", err))
		return res, nil, err
	}
	if c.ID == "" { // course given by URL
		res.Course = detail.Course
		c = detail.Course
	}
	if d.Opt.AutoEnroll {
		if err := d.ensureEnrolled(ctx, c); IsFatal(err) {
			return res, nil, err
		}
	}

	var js []job
	gi := 0
	for mi, m := range detail.Modules {
		for _, l := range m.Lessons {
			gi++
			if l.Type == "video" || l.Type == "article" {
				res.Downloadable++
			}
			path := naming.LessonPath(d.Opt.OutputDir, c.Title, mi+1, m.Title, gi, l.Title, naming.Sanitize)
			js = append(js, job{res: res, course: detail, lesson: l, path: path, position: position})
		}
	}
	if res.Downloadable == 0 {
		d.printf("  only labs/quizzes, nothing to download\n")
		return res, nil, nil
	}
	// Rename folders/files from older versions before any worker touches them.
	gi = 0
	for mi, m := range detail.Modules {
		for _, l := range m.Lessons {
			gi++
			old := naming.LessonPath(d.Opt.OutputDir, c.Title, mi+1, m.Title, gi, l.Title, naming.LegacySanitize)
			naming.MigrateLegacyLesson(old, js[gi-1].path, d.Log)
		}
	}
	d.printf("  %d downloadable lesson(s), %d worker(s)\n", res.Downloadable, d.Opt.Jobs)
	return res, js, nil
}

func (d *Downloader) ensureEnrolled(ctx context.Context, c api.Course) error {
	if s := d.API.EnrollmentStatus(ctx, c.ID); s != nil && *s {
		d.Log.Debug("already enrolled", "course", c.Title)
		return nil
	}
	if err := d.API.Enroll(ctx, c.ID); err != nil {
		if IsFatal(err) {
			return err
		}
		d.Log.Warn("KodeKloud did not enroll you; trying to download anyway", "course", c.Title, "err", err)
		return nil
	}
	switch s := d.API.EnrollmentStatus(ctx, c.ID); {
	case s != nil && *s:
		d.Log.Info("enrolled", "course", c.Title)
	case s != nil:
		d.Log.Warn("enroll request accepted but KodeKloud still reports you as not enrolled", "course", c.Title)
	default:
		d.Log.Warn("enroll request accepted but KodeKloud did not confirm it", "course", c.Title)
	}
	return nil
}

func accessDenied(title string, err error) *AccessError {
	return &AccessError{fmt.Sprintf("KodeKloud denied access to '%s' (%v). You are probably not enrolled, or your plan does not include this course.", title, err)}
}

var videoExts = []string{".mkv", ".mp4", ".webm"}

// VideoDone reports whether yt-dlp's finished file exists (".part" doesn't count).
func VideoDone(base string) bool {
	for _, ext := range videoExts {
		if fi, err := os.Stat(base + ext); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func (d *Downloader) process(ctx context.Context, j job) error {
	res := j.res
	if res.ctx.Err() != nil {
		return nil // course already failed or run cancelled
	}
	name := filepath.Base(j.path)
	courseTitle := res.Course.Title

	if j.lesson.Type != "video" {
		notes := naming.WithSuffix(j.path, ".md")
		if _, err := os.Stat(notes); err == nil {
			return nil
		}
		lc, err := d.API.Lesson(res.ctx, j.lesson.ID, res.Course.ID)
		if err != nil {
			if IsFatal(err) {
				return err
			}
			d.Log.Debug("no lesson content", "lesson", name, "err", err)
			return nil
		}
		if strings.TrimSpace(lc.Content) == "" {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(notes), 0o755); err != nil {
			res.Failed.Add(1)
			return nil
		}
		if err := os.WriteFile(notes, []byte(lc.Content), 0o644); err != nil {
			d.Log.Error("writing notes failed", "file", notes, "err", err)
			res.Failed.Add(1)
			return nil
		}
		if j.lesson.Type == "article" {
			res.Downloaded.Add(1)
		}
		d.printf("  ✓ [%s] %s.md\n", j.position, name)
		d.Resources.DownloadAll(res.ctx, lc.Content, filepath.Dir(j.path))
		return nil
	}

	if VideoDone(j.path) {
		res.Skipped.Add(1)
		d.Log.Debug("already downloaded", "lesson", name)
		return nil
	}
	lc, err := d.API.Lesson(res.ctx, j.lesson.ID, res.Course.ID)
	if err != nil {
		switch st := api.StatusOf(err); {
		case IsFatal(err):
			return err
		case st == 402 || st == 403:
			res.fail(accessDenied(courseTitle, err))
		case res.ctx.Err() == nil:
			d.Log.Error("lesson lookup failed", "lesson", name, "err", err)
			res.Failed.Add(1)
		}
		return nil
	}
	if lc.VideoURL == "" {
		res.fail(&AccessError{fmt.Sprintf("KodeKloud returned no video for '%s' in '%s'. You are probably not enrolled, or your plan does not include this course.", j.lesson.Title, courseTitle)})
		return nil
	}
	parts := strings.Split(lc.VideoURL, "/")
	vimeo := "https://player.vimeo.com/video/" + parts[len(parts)-1]

	res.mu.Lock()
	dup := res.videos[vimeo] > d.Opt.MaxDuplicateCount
	res.videos[vimeo]++
	res.mu.Unlock()
	if dup {
		res.fail(&AccessError{fmt.Sprintf("the same video came back more than %d times in '%s'; you may not have access to, or be enrolled in, this course", d.Opt.MaxDuplicateCount, courseTitle)})
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		res.Failed.Add(1)
		return nil
	}
	start := time.Now()
	out, err := d.RunVideo(res.ctx, vimeo, j.path)
	if err != nil {
		if res.ctx.Err() != nil {
			return nil
		}
		res.Failed.Add(1)
		d.Log.Error("video download failed", "lesson", name, "url", vimeo, "err", err, "yt-dlp", tail(out, 15))
		d.printf("  ✗ [%s] %s\n", j.position, name)
		return nil
	}
	d.Log.Debug("yt-dlp output", "lesson", name, "out", tail(out, 40))
	res.Downloaded.Add(1)
	d.printf("  ✓ [%s] %s (%s)\n", j.position, name, time.Since(start).Round(time.Second))
	if lc.Content != "" {
		d.Resources.DownloadAll(res.ctx, lc.Content, filepath.Dir(j.path))
	}
	return nil
}

func tail(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// YtDlpArgs builds the yt-dlp command line (same choices as the Python version).
func (d *Downloader) YtDlpArgs(videoURL, outBase string) []string {
	h := strings.TrimSuffix(d.Opt.Quality, "p")
	return []string{
		"-f", fmt.Sprintf("bestvideo[height<=%s]+bestaudio/best[height<=%s]/best", h, h),
		"-N", strconv.Itoa(max(1, d.Opt.Fragments)),
		// "%" would start an output-template field.
		"-o", strings.ReplaceAll(outBase, "%", "%%") + ".%(ext)s",
		"--merge-output-format", "mkv",
		"--write-subs",
		"--referer", "https://learn.kodekloud.com/",
		"--no-progress",
		"--no-playlist",
		videoURL,
	}
}

func (d *Downloader) ytdlp(ctx context.Context, videoURL, outBase string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.Opt.YtDlp, d.YtDlpArgs(videoURL, outBase)...)
	cmd.WaitDelay = 10 * time.Second
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.Bytes(), err
}
