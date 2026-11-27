// Command kodekloud downloads KodeKloud courses you have access to.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/johnay0/kodekloud-downloader/internal/api"
	"github.com/johnay0/kodekloud-downloader/internal/auth"
	"github.com/johnay0/kodekloud-downloader/internal/catalog"
	"github.com/johnay0/kodekloud-downloader/internal/download"
	"github.com/johnay0/kodekloud-downloader/internal/logx"
	"github.com/johnay0/kodekloud-downloader/internal/quiz"
	"github.com/johnay0/kodekloud-downloader/internal/resources"
)

var version = "dev"

// exitError carries an exit code without printing usage.
type exitError struct{ msg string }

func (e *exitError) Error() string { return e.msg }

func fail(format string, a ...any) error { return &exitError{fmt.Sprintf(format, a...)} }

type app struct {
	verbose int
	log     *slog.Logger
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	// test hooks
	newAPI     func(api.TokenSource) *api.Client
	newAuth    func(email, password, statePath string) *auth.Firebase
	runner     download.VideoRunner
	readPass   func() (string, error)
	lookPath   func(string) (string, error)
	httpClient *http.Client
}

func newApp() *app {
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, lookPath: exec.LookPath,
		httpClient: &http.Client{Timeout: 5 * time.Minute}}
	a.readPass = func() (string, error) {
		fmt.Fprint(a.stderr, "KodeKloud password: ")
		defer fmt.Fprintln(a.stderr)
		if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			b, err := term.ReadPassword(int(f.Fd()))
			return string(b), err
		}
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	a.newAPI = func(ts api.TokenSource) *api.Client { return api.New(ts, a.log) }
	a.newAuth = func(e, p, s string) *auth.Firebase { return auth.New(e, p, s, a.log) }
	return a
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "kodekloud",
		Short:         "Download KodeKloud courses you have access to",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			level := slog.LevelInfo
			if a.verbose >= 2 {
				level = slog.LevelDebug
			}
			a.log = logx.New(a.stderr, level)
		},
	}
	root.PersistentFlags().CountVarP(&a.verbose, "verbose", "v", "Increase log verbosity (-vv for debug)")
	root.AddCommand(a.dlCmd(), a.quizCmd())
	return root
}

type dlFlags struct {
	quality, outputDir, email, password, authState, search, category, plan, ytdlp string
	noAuthCache, noEnroll, allPaid, dryRun                                        bool
	maxDup, jobs, fragments                                                       int
}

func defaultOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

func (a *app) dlCmd() *cobra.Command {
	var f dlFlags
	cmd := &cobra.Command{
		Use:   "dl [COURSE_URL]",
		Short: "Download a course, courses picked from the catalog, or every paid course",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			courseURL := ""
			if len(args) == 1 {
				courseURL = args[0]
			}
			return a.runDL(cmd.Context(), courseURL, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.quality, "quality", "q", "1080p", "Video quality: 360p, 480p, 540p, 720p or 1080p")
	fl.StringVarP(&f.outputDir, "output-dir", "o", defaultOutputDir(), "Output directory")
	fl.StringVarP(&f.email, "email", "e", os.Getenv("KODEKLOUD_EMAIL"), "KodeKloud account email (env: KODEKLOUD_EMAIL); only needed for the first run")
	fl.StringVarP(&f.password, "password", "p", "", "KodeKloud password (env: KODEKLOUD_PASSWORD); prompted for if missing. Never written to disk")
	fl.StringVar(&f.authState, "auth-state", envOr("KODEKLOUD_AUTH_STATE", auth.DefaultStatePath()), "Session cache file, mode 0600 (env: KODEKLOUD_AUTH_STATE)")
	fl.BoolVar(&f.noAuthCache, "no-auth-cache", false, "Do not read or write the session cache")
	fl.BoolVar(&f.noEnroll, "no-enroll", false, "Do not auto-enroll in courses you are not enrolled in yet")
	fl.IntVar(&f.maxDup, "max-duplicate-count", 3, "Fail a course if the same video comes back more than this many times")
	fl.StringVarP(&f.search, "search", "s", "", "Filter courses by keyword (title, instructor, category)")
	fl.StringVarP(&f.category, "filter", "f", "", "Filter courses by category, comma-separated (alias: --category)")
	fl.StringVar(&f.category, "category", "", "Alias for --filter")
	_ = fl.MarkHidden("category")
	fl.StringVar(&f.plan, "plan", "", "Only list courses on these plans, comma-separated (Free, Standard, Pro)")
	fl.BoolVar(&f.allPaid, "all-paid", false, "Download every Standard and Pro course (Free ones skipped) without prompting")
	fl.BoolVar(&f.dryRun, "dry-run", false, "With --all-paid: only list the courses")
	fl.IntVarP(&f.jobs, "jobs", "j", 4, "Lessons to download in parallel")
	fl.IntVar(&f.fragments, "fragments", 8, "Fragments per video yt-dlp downloads in parallel (-N)")
	fl.StringVar(&f.ytdlp, "yt-dlp", "yt-dlp", "yt-dlp executable")
	return cmd
}

func (a *app) quizCmd() *cobra.Command {
	var out string
	var sep bool
	cmd := &cobra.Command{
		Use:   "dl-quiz",
		Short: "Export KodeKloud's public quizzes to Markdown",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			if err := quiz.Download(cmd.Context(), a.httpClient, out, sep, 16, a.stdout); err != nil {
				return fail("quiz export failed: %v\n(KodeKloud appears to have retired the public quiz backend %s)", err, quiz.BaseURL)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output-dir", "o", defaultOutputDir(), "Output directory")
	cmd.Flags().BoolVar(&sep, "sep", false, "Write each quiz to a separate file")
	return cmd
}

var qualities = map[string]bool{"360p": true, "480p": true, "540p": true, "720p": true, "1080p": true}

func (a *app) runDL(ctx context.Context, courseURL string, f dlFlags) error {
	switch {
	case !qualities[f.quality]:
		return fail("invalid --quality %q (use 360p, 480p, 540p, 720p or 1080p)", f.quality)
	case f.allPaid && courseURL != "":
		return fail("--all-paid downloads the whole catalog; do not pass a COURSE_URL")
	case f.dryRun && !f.allPaid:
		return fail("--dry-run only works together with --all-paid")
	case f.jobs < 1:
		return fail("--jobs must be at least 1")
	}
	public := a.newAPI(nil)
	if f.allPaid && f.dryRun {
		_, err := a.listPaid(ctx, public, f)
		return err
	}
	ytdlp, err := a.lookPath(f.ytdlp)
	if err != nil {
		return fail("yt-dlp not found (%v). Install it from https://github.com/yt-dlp/yt-dlp#installation (the standalone binary needs no Python) and make sure ffmpeg is installed too", err)
	}
	if _, err := a.lookPath("ffmpeg"); err != nil {
		a.log.Warn("ffmpeg not found; yt-dlp needs it to merge video and audio")
	}

	fb, err := a.signIn(ctx, f)
	if err != nil {
		return err
	}
	client := a.newAPI(fb)
	dl := &download.Downloader{
		Opt: download.Options{
			OutputDir: f.outputDir, Quality: f.quality, Jobs: f.jobs, Fragments: f.fragments,
			YtDlp: ytdlp, AutoEnroll: !f.noEnroll, MaxDuplicateCount: f.maxDup,
		},
		API:       client,
		Resources: &resources.Downloader{HTTP: a.httpClient, Auth: fb, Log: a.log},
		Log:       a.log,
		Out:       a.stdout,
		RunVideo:  a.runner,
	}

	var courses []api.Course
	switch {
	case f.allPaid:
		if courses, err = a.listPaid(ctx, public, f); err != nil {
			return err
		}
	case courseURL == "":
		all, err := public.Courses(ctx)
		if err != nil {
			return err
		}
		if f.plan != "" {
			all = catalog.FilterByPlan(all, strings.Split(f.plan, ","))
		}
		courses = catalog.Select(all, f.search, f.category, a.stdin, a.stdout)
	default:
		u, err := url.Parse(courseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fail("please enter a valid URL")
		}
		courses = []api.Course{{Slug: api.SlugFromURL(courseURL), Title: api.SlugFromURL(courseURL)}}
	}
	if len(courses) == 0 {
		return nil
	}

	start := time.Now()
	results, runErr := dl.Run(ctx, courses)
	failed := a.summary(results, time.Since(start))
	if runErr != nil {
		var ae *auth.Error
		if errors.Is(runErr, api.AuthFailedError) || errors.As(runErr, &ae) {
			return fail("%v", runErr)
		}
		return fail("stopped: %v", runErr)
	}
	if failed {
		return &exitError{}
	}
	return nil
}

func (a *app) signIn(ctx context.Context, f dlFlags) (*auth.Firebase, error) {
	statePath := f.authState
	if f.noAuthCache {
		statePath = ""
	}
	password := f.password
	if password == "" {
		password = os.Getenv("KODEKLOUD_PASSWORD")
	}
	fb := a.newAuth(f.email, password, statePath)
	if f.email != "" && password == "" && !fb.HasRefreshToken() {
		p, err := a.readPass()
		if err != nil {
			return nil, fail("could not read password: %v", err)
		}
		fb.SetPassword(p)
	}
	if !fb.HasCredentials() {
		return nil, fail("Not signed in: pass --email (and --password, or you will be prompted), or set KODEKLOUD_EMAIL / KODEKLOUD_PASSWORD.")
	}
	if _, err := fb.Token(ctx); err != nil { // fail fast on bad credentials
		return nil, fail("%v", err)
	}
	return fb, nil
}

func (a *app) listPaid(ctx context.Context, public *api.Client, f dlFlags) ([]api.Course, error) {
	all, err := public.Courses(ctx)
	if err != nil {
		return nil, err
	}
	paid := catalog.FilterByPlan(all, catalog.PaidPlans)
	items := catalog.Index(paid)
	if f.category != "" {
		items = catalog.FilterByCategory(items, f.category)
	}
	if f.search != "" {
		items = catalog.Search(items, f.search)
	}
	paid = catalog.Courses(items)
	fmt.Fprintln(a.stdout, catalog.CourseTable(catalog.Index(paid)))
	counts := map[string]int{}
	for _, c := range paid {
		counts[c.Plan]++
	}
	var parts []string
	for _, p := range catalog.PaidPlans {
		parts = append(parts, fmt.Sprintf("%d %s", counts[p], p))
	}
	skipped := len(all) - len(catalog.FilterByPlan(all, catalog.PaidPlans))
	fmt.Fprintf(a.stdout, "%d paid course(s): %s (%d Free course(s) skipped).\n", len(paid), strings.Join(parts, ", "), skipped)
	return paid, nil
}

// summary prints the end-of-run report and reports whether anything failed.
func (a *app) summary(results []*download.Result, took time.Duration) bool {
	var done, newVideos, onDisk int64
	var labOnly, failed []string
	for _, r := range results {
		newVideos += r.Downloaded.Load()
		onDisk += r.Skipped.Load()
		switch {
		case r.Err() != nil:
			failed = append(failed, fmt.Sprintf("%s: %v", r.Course.Title, r.Err()))
		case r.Downloadable == 0:
			labOnly = append(labOnly, r.Course.Title)
		case r.Failed.Load() > 0:
			failed = append(failed, fmt.Sprintf("%s: %d lesson(s) failed", r.Course.Title, r.Failed.Load()))
		default:
			done++
		}
	}
	w := a.stdout
	fmt.Fprintf(w, "\nDone in %s: %d/%d course(s) complete (%d lesson(s) downloaded, %d already on disk).\n",
		took.Round(time.Second), done, len(results), newVideos, onDisk)
	if len(labOnly) > 0 {
		fmt.Fprintf(w, "Skipped %d lab-only course(s) (interactive labs only, nothing to download):\n", len(labOnly))
		for _, t := range labOnly {
			fmt.Fprintf(w, "  - %s\n", t)
		}
	}
	if len(failed) > 0 {
		fmt.Fprintln(w, "Failed (re-run to retry, finished files are skipped):")
		for _, t := range failed {
			fmt.Fprintf(w, "  - %s\n", t)
		}
	}
	return len(failed) > 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := newApp()
	if err := a.root().ExecuteContext(ctx); err != nil {
		var ee *exitError
		if !errors.As(err, &ee) || ee.msg != "" {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}
