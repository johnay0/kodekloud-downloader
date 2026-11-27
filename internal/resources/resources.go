// Package resources finds downloadable attachments (PDFs, archives, slides...)
// in lesson Markdown and downloads them next to the lesson.
package resources

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/johnay0/kodekloud-downloader/internal/auth"
	"github.com/johnay0/kodekloud-downloader/internal/naming"
)

var Extensions = []string{
	".pdf", ".zip", ".tar.gz", ".tgz", ".tar", ".gz", ".7z", ".rar", ".pptx",
	".ppt", ".docx", ".doc", ".xlsx", ".xls", ".csv", ".epub", ".ipynb",
	".yaml", ".yml", ".json", ".sh", ".py", ".txt",
}

var (
	mdLink     = regexp.MustCompile(`\[([^\]]*)\]\((https?://[^\s)"']+)\)`)
	bareURL    = regexp.MustCompile(`https?://[^\s"'<>\[\])]+`)
	githubBlob = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/blob/(.+)$`)
)

// IsTarget reports whether a URL or name ends with a known resource extension.
func IsTarget(s string) bool {
	s = strings.ToLower(s)
	s = strings.SplitN(s, "?", 2)[0]
	s = strings.TrimSpace(strings.SplitN(s, "#", 2)[0])
	for _, ext := range Extensions {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

// NormalizeURL rewrites GitHub blob URLs to raw.githubusercontent.com.
func NormalizeURL(u string) string {
	if m := githubBlob.FindStringSubmatch(u); m != nil {
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", m[1], m[2], m[3])
	}
	return u
}

type Link struct{ URL, Title string }

// Extract returns resource links from Markdown: [text](url) links that are
// not images, then bare URLs; deduplicated in order (mirrors the Python).
func Extract(md string) []Link {
	var out []Link
	seen := map[string]bool{}
	add := func(u, title string) {
		n := NormalizeURL(u)
		if !seen[n] {
			seen[n] = true
			out = append(out, Link{n, title})
		}
	}
	for _, m := range mdLink.FindAllStringSubmatchIndex(md, -1) {
		if m[0] > 0 && md[m[0]-1] == '!' { // ![image](...)
			continue
		}
		text, u := md[m[2]:m[3]], md[m[4]:m[5]]
		if IsTarget(u) || IsTarget(text) {
			add(u, text)
		}
	}
	for _, u := range bareURL.FindAllString(md, -1) {
		if IsTarget(u) {
			add(u, "")
		}
	}
	return out
}

// Filename picks a sanitized file name from the URL path or the link title.
func Filename(rawURL, title string, sanitize naming.Sanitizer) string {
	name := ""
	if u, err := url.Parse(rawURL); err == nil {
		if p := strings.TrimRight(u.Path, "/"); p != "" {
			name = path.Base(p)
		}
	}
	switch {
	case name != "" && IsTarget(name):
	case title != "" && IsTarget(title):
		name = title
	case name != "":
	default:
		name = "resource.bin"
	}
	return sanitize(name, 100)
}

// Downloader fetches resources; safe for concurrent use. The same target
// file is never downloaded twice at once.
type Downloader struct {
	HTTP *http.Client
	Auth interface {
		Token(context.Context) (string, error)
	}
	Log *slog.Logger
	// Trusted decides which URLs get the token; default auth.IsKodeKloudURL.
	Trusted func(string) bool
	locks   sync.Map // target path -> *sync.Mutex
}

func (d *Downloader) lock(p string) func() {
	m, _ := d.locks.LoadOrStore(p, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// DownloadAll downloads every resource linked in md into dir and returns the
// files present afterwards. Failures are logged, not fatal.
func (d *Downloader) DownloadAll(ctx context.Context, md, dir string) []string {
	links := Extract(md)
	var got []string
	for _, l := range links {
		target := filepath.Join(dir, Filename(l.URL, l.Title, naming.Sanitize))
		if p, err := d.download(ctx, l.URL, l.Title, dir, target); err != nil {
			d.Log.Warn("failed to download resource", "url", l.URL, "err", err)
		} else {
			got = append(got, p)
		}
	}
	return got
}

func (d *Downloader) download(ctx context.Context, rawURL, title, dir, target string) (string, error) {
	defer d.lock(target)()
	naming.MovePath(filepath.Join(dir, Filename(rawURL, title, naming.LegacySanitize)), target, d.Log)
	if fi, err := os.Stat(target); err == nil && fi.Size() > 0 {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	// Never leak the KodeKloud token to GitHub, S3 or any other host. (Go's
	// client also drops Authorization on cross-domain redirects.)
	trusted := d.Trusted
	if trusted == nil {
		trusted = auth.IsKodeKloudURL
	}
	if d.Auth != nil && trusted(rawURL) {
		tok, err := d.Auth.Token(ctx)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, ".resource-*.part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	d.Log.Info("saved resource", "file", filepath.Base(target))
	return target, nil
}
