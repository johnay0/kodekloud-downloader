package resources

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type tok string

func (t tok) Token(context.Context) (string, error) { return string(t), nil }

func TestExtract(t *testing.T) {
	md := "# Notes\n![diagram](https://example.com/img.pdf)\n" +
		"- [Slides](https://github.com/o/r/blob/main/deck.pdf)\n" +
		"- [Code](https://example.com/lab.zip)\n" +
		"- [Docs](https://example.com/docs)\n" +
		"bare https://example.com/manifest.yaml and again https://example.com/lab.zip\n"
	got := Extract(md)
	want := []Link{
		{"https://raw.githubusercontent.com/o/r/main/deck.pdf", "Slides"},
		{"https://example.com/lab.zip", "Code"},
		{"https://example.com/img.pdf", ""}, // the bare-URL pass sees it
		{"https://example.com/manifest.yaml", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestFilename(t *testing.T) {
	cases := []struct{ url, title, want string }{
		{"https://x.com/a/b/deck.pdf?dl=1", "", "deck.pdf"},
		{"https://x.com/files/CI%3ACD%7CDeck.pdf", "", "CI-CD-Deck.pdf"},
		{"https://x.com/download?id=3", "Lab Guide.pdf", "Lab Guide.pdf"},
		{"https://x.com/", "", "resource.bin"},
	}
	for _, c := range cases {
		if got := Filename(c.url, c.title); got != c.want {
			t.Errorf("Filename(%q, %q) = %q, want %q", c.url, c.title, got, c.want)
		}
	}
}

func TestDownloadAllNoTokenLeakAndDedup(t *testing.T) {
	var mu sync.Mutex
	headers := map[string]string{}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		headers[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, "content of "+r.URL.Path)
	}))
	defer srv.Close()
	dir := t.TempDir()

	d := &Downloader{HTTP: srv.Client(), Auth: tok("SECRET"), Log: quiet,
		Trusted: func(u string) bool { return strings.Contains(u, "/kk/") }}
	md := "[a](" + srv.URL + "/kk/a.pdf) [b](" + srv.URL + "/other/b.zip) [c](" + srv.URL + "/x/CI%3ACD%7CDeck.pdf)"

	var wg sync.WaitGroup
	for range 5 { // concurrent lessons linking the same files
		wg.Add(1)
		go func() { defer wg.Done(); d.DownloadAll(context.Background(), md, dir) }()
	}
	wg.Wait()

	if headers["/kk/a.pdf"] != "Bearer SECRET" || headers["/other/b.zip"] != "" {
		t.Errorf("auth headers = %v", headers)
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("server hits = %d, want 3 (each file once)", n)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "CI-CD-Deck.pdf")); string(b) != "content of /x/CI:CD|Deck.pdf" {
		t.Errorf("CI-CD-Deck.pdf = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}
