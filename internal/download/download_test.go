package download

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/johnay0/kodekloud-downloader/internal/api"
	"github.com/johnay0/kodekloud-downloader/internal/resources"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type staticAuth struct{ renew bool }

func (staticAuth) Token(context.Context) (string, error)       { return "tok", nil }
func (a staticAuth) ForceRefresh(context.Context, string) bool { return a.renew }

// fakeKK emulates the learn-api endpoints the downloader uses.
type fakeKK struct {
	details      map[string]api.CourseDetail
	lessonStatus map[string]int // lesson id -> forced HTTP status
	enrolled     map[string]bool
	enrollBodies []map[string]any
	lessonCalls  atomic.Int32
	mu           sync.Mutex
}

func (f *fakeKK) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/api/courses/"):
			d, ok := f.details[strings.TrimPrefix(p, "/api/courses/")]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(d)
		case strings.HasPrefix(p, "/api/lessons/"):
			f.lessonCalls.Add(1)
			id := strings.TrimPrefix(p, "/api/lessons/")
			if st := f.lessonStatus[id]; st != 0 {
				w.WriteHeader(st)
				_, _ = io.WriteString(w, `{"message":"Enrollment is required"}`)
				return
			}
			if strings.HasPrefix(id, "a") {
				_, _ = io.WriteString(w, `{"content":"# Notes for `+id+`"}`)
				return
			}
			_, _ = io.WriteString(w, `{"video_url":"https://vimeo.com/`+vimeoID(id)+`/abcdef1234?share=copy"}`)
		case strings.HasPrefix(p, "/api/users_progresses/courses/"):
			id := strings.TrimPrefix(p, "/api/users_progresses/courses/")
			_ = json.NewEncoder(w).Encode(map[string]any{"enrolled": f.enrolled[id]})
		case p == "/api/users_progresses/enroll_course":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.enrollBodies = append(f.enrollBodies, b)
			if id, ok := b["course_id"].(string); ok {
				f.enrolled[id] = true
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// vimeoID is the fake numeric Vimeo id of a lesson.
func vimeoID(lessonID string) string {
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(lessonID)))+1_000_000_000, 10)
}

func playerURL(lessonID string) string {
	return "https://player.vimeo.com/video/" + vimeoID(lessonID) + "?h=abcdef1234"
}

func course(id, title string, lessons ...api.Lesson) api.CourseDetail {
	return api.CourseDetail{
		Course:  api.Course{ID: id, Slug: id, Title: title, Plan: "Standard"},
		Modules: []api.Module{{ID: "m1", Title: "Intro: Basics", Lessons: lessons}},
	}
}

func videos(prefix string, n int) []api.Lesson {
	var ls []api.Lesson
	for i := range n {
		ls = append(ls, api.Lesson{ID: prefix + string(rune('a'+i)), Title: "Video " + string(rune('A'+i)), Type: "video"})
	}
	return ls
}

type fakeYtDlp struct {
	cur, peak atomic.Int32
	calls     atomic.Int32
	delay     time.Duration
	fail      map[string]bool
}

func (y *fakeYtDlp) run(ctx context.Context, url, out string) ([]byte, error) {
	y.calls.Add(1)
	n := y.cur.Add(1)
	defer y.cur.Add(-1)
	for {
		p := y.peak.Load()
		if n <= p || y.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-time.After(y.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if y.fail[url] {
		return []byte("ERROR: boom"), errors.New("exit status 1")
	}
	return nil, os.WriteFile(out+".mkv", []byte(url), 0o644)
}

func newDL(t *testing.T, srv *httptest.Server, y *fakeYtDlp, jobs int, a api.TokenSource) (*Downloader, string) {
	out := t.TempDir()
	c := api.New(a, quiet)
	c.BaseURL = srv.URL + "/api"
	return &Downloader{
		Opt:       Options{OutputDir: out, Quality: "720p", Jobs: jobs, Fragments: 4, AutoEnroll: true, MaxDuplicateCount: 3},
		API:       c,
		Resources: &resources.Downloader{HTTP: srv.Client(), Log: quiet},
		Log:       quiet,
		Out:       io.Discard,
		RunVideo:  y.run,
	}, out
}

func TestParallelDownloadEnrollAndSummary(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{}, details: map[string]api.CourseDetail{
		"c1": course("c1", "AZ-204: Dev", append(videos("v", 8), api.Lesson{ID: "a1", Title: "Notes", Type: "article"})...),
		"c2": course("c2", "Labs Only", api.Lesson{ID: "l1", Title: "Lab", Type: "lab"}),
	}}
	srv := kk.serve(t)
	y := &fakeYtDlp{delay: 100 * time.Millisecond}
	dl, out := newDL(t, srv, y, 4, staticAuth{})

	res, err := dl.Run(context.Background(), []api.Course{kk.details["c1"].Course, kk.details["c2"].Course})
	if err != nil {
		t.Fatal(err)
	}
	if y.peak.Load() != 4 { // 8 videos, 4 workers: 4 yt-dlp runs at once
		t.Errorf("peak concurrency = %d, want 4", y.peak.Load())
	}
	if res[0].Downloaded.Load() != 9 || res[0].Err() != nil || res[1].Downloadable != 0 {
		t.Errorf("results: c1 downloaded=%d err=%v, c2 downloadable=%d", res[0].Downloaded.Load(), res[0].Err(), res[1].Downloadable)
	}
	dir := filepath.Join(out, "KodeKloud", "AZ-204 - Dev", "01 - Intro - Basics")
	if _, err := os.Stat(filepath.Join(dir, "001 - Video A.mkv")); err != nil {
		t.Error(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "009 - Notes.md")); string(b) != "# Notes for a1" {
		t.Errorf("notes = %q", b)
	}
	if len(kk.enrollBodies) != 2 || kk.enrollBodies[0]["course_id"] != "c1" {
		t.Errorf("enroll bodies = %v", kk.enrollBodies)
	}
	if _, err := os.Stat(filepath.Join(out, "KodeKloud", "Labs Only")); err == nil {
		t.Error("lab-only course created a folder")
	}

	// Re-run: everything is on disk, so no lesson API calls for videos and no yt-dlp.
	kk.lessonCalls.Store(0)
	y.calls.Store(0)
	res, _ = dl.Run(context.Background(), []api.Course{kk.details["c1"].Course})
	if y.calls.Load() != 0 || kk.lessonCalls.Load() != 0 || res[0].Skipped.Load() != 8 {
		t.Errorf("re-run: yt-dlp=%d lessonAPI=%d skipped=%d", y.calls.Load(), kk.lessonCalls.Load(), res[0].Skipped.Load())
	}
}

func TestAccessDeniedFailsOnlyThatCourse(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{"pro": true, "ok": true}, lessonStatus: map[string]int{"pa": 403},
		details: map[string]api.CourseDetail{
			"pro": course("pro", "Pro Course", videos("p", 5)...),
			"ok":  course("ok", "Fine", videos("o", 2)...),
		}}
	srv := kk.serve(t)
	y := &fakeYtDlp{delay: 5 * time.Millisecond}
	dl, _ := newDL(t, srv, y, 1, staticAuth{})
	res, err := dl.Run(context.Background(), []api.Course{kk.details["pro"].Course, kk.details["ok"].Course})
	if err != nil {
		t.Fatal(err)
	}
	var ae *AccessError
	if !errors.As(res[0].Err(), &ae) || !strings.Contains(ae.Msg, "Enrollment is required") {
		t.Errorf("pro err = %v", res[0].Err())
	}
	if res[0].Downloaded.Load() != 0 || res[1].Downloaded.Load() != 2 {
		t.Errorf("downloaded: pro=%d ok=%d", res[0].Downloaded.Load(), res[1].Downloaded.Load())
	}
	if len(kk.enrollBodies) != 0 {
		t.Error("enrolled although already enrolled")
	}
}

func TestFailedVideoIsCountedAndOthersContinue(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{"c": true}, details: map[string]api.CourseDetail{"c": course("c", "C", videos("v", 3)...)}}
	srv := kk.serve(t)
	y := &fakeYtDlp{fail: map[string]bool{playerURL("vb"): true}}
	dl, _ := newDL(t, srv, y, 2, staticAuth{})
	res, _ := dl.Run(context.Background(), []api.Course{kk.details["c"].Course})
	if res[0].Failed.Load() != 1 || res[0].Downloaded.Load() != 2 {
		t.Errorf("failed=%d downloaded=%d", res[0].Failed.Load(), res[0].Downloaded.Load())
	}
}

func TestAuthFailureStopsRun(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{"c": true}, lessonStatus: map[string]int{}, details: map[string]api.CourseDetail{"c": course("c", "C", videos("v", 3)...)}}
	srv := kk.serve(t)
	kk.lessonStatus["va"], kk.lessonStatus["vb"], kk.lessonStatus["vc"] = 401, 401, 401
	dl, _ := newDL(t, srv, &fakeYtDlp{}, 2, staticAuth{renew: false})
	_, err := dl.Run(context.Background(), []api.Course{kk.details["c"].Course})
	if !errors.Is(err, api.AuthFailedError) {
		t.Fatalf("err = %v", err)
	}
}

func TestYtDlpArgs(t *testing.T) {
	d := &Downloader{Opt: Options{Quality: "720p", Fragments: 8}}
	got := strings.Join(d.YtDlpArgs("https://player.vimeo.com/video/1", "/o/100% Guide"), " ")
	for _, want := range []string{"bestvideo[height<=720]+bestaudio/best[height<=720]/best", "-N 8", "/o/100%% Guide.%(ext)s", "--merge-output-format mkv", "--write-subs", "--referer https://learn.kodekloud.com/"} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q: %s", want, got)
		}
	}
}
