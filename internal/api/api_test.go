package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeAuth struct {
	tok       atomic.Value
	refreshed atomic.Int32
	canRenew  bool
}

func newFakeAuth(tok string, canRenew bool) *fakeAuth {
	f := &fakeAuth{canRenew: canRenew}
	f.tok.Store(tok)
	return f
}
func (f *fakeAuth) Token(context.Context) (string, error) { return f.tok.Load().(string), nil }
func (f *fakeAuth) ForceRefresh(context.Context, string) bool {
	f.refreshed.Add(1)
	if f.canRenew {
		f.tok.Store("fresh")
	}
	return f.canRenew
}

func client(srv *httptest.Server, a TokenSource) *Client {
	c := New(a, quiet)
	c.BaseURL = srv.URL + "/api"
	c.backoff = func(int) time.Duration { return time.Millisecond }
	return c
}

func TestRetriesOn5xxAnd429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) {
		case 1:
			w.WriteHeader(502)
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
		default:
			_, _ = io.WriteString(w, `{"video_url":"https://vimeo.com/1"}`)
		}
	}))
	defer srv.Close()
	l, err := client(srv, nil).Lesson(context.Background(), "l1", "c1")
	if err != nil || l.VideoURL != "https://vimeo.com/1" || n.Load() != 3 {
		t.Fatalf("lesson=%+v err=%v calls=%d", l, err, n.Load())
	}
}

func TestRenewsTokenOnceOn401(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	a := newFakeAuth("old", true)
	if _, err := client(srv, a).Lesson(context.Background(), "l", "c"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "Bearer old" || seen[1] != "Bearer fresh" {
		t.Fatalf("headers = %v", seen)
	}

	b := newFakeAuth("old", false)
	_, err := client(srv, b).Lesson(context.Background(), "l", "c")
	if !errors.Is(err, AuthFailedError) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPErrorCarriesStatusAndMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"message":"Enrollment is required"}`)
	}))
	defer srv.Close()
	_, err := client(srv, nil).Lesson(context.Background(), "l", "c")
	if StatusOf(err) != 403 || err.Error() != "HTTP 403: Enrollment is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollSendsSnakeCaseCourseID(t *testing.T) {
	var body map[string]any
	var path, authz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, authz = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
	}))
	defer srv.Close()
	if err := client(srv, newFakeAuth("tok", false)).Enroll(context.Background(), "c-1"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/users_progresses/enroll_course" || body["course_id"] != "c-1" || len(body) != 1 || authz != "Bearer tok" {
		t.Fatalf("path=%s body=%v auth=%q", path, body, authz)
	}
}

func TestNoTokenForForeignHosts(t *testing.T) {
	var authz string
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authz = r.Header.Get("Authorization")
	}))
	defer foreign.Close()
	c := New(newFakeAuth("secret", false), quiet) // BaseURL = real KodeKloud
	if err := c.Do(context.Background(), http.MethodGet, foreign.URL+"/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if authz != "" {
		t.Fatalf("token leaked to %s: %q", foreign.URL, authz)
	}
}

func TestCoursesPaginatesAndSlugFromURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, `{"courses":[{"id":"a","title":"A","plan":"Free"}],"metadata":{"next_page":2}}`)
			return
		}
		_, _ = io.WriteString(w, `{"courses":[{"id":"b","title":"B","plan":"Pro"}],"metadata":{"next_page":null}}`)
	}))
	defer srv.Close()
	cs, err := client(srv, nil).Courses(context.Background())
	if err != nil || len(cs) != 2 || cs[1].Plan != "Pro" {
		t.Fatalf("courses=%+v err=%v", cs, err)
	}
	if s := SlugFromURL("https://learn.kodekloud.com/user/courses/docker-basics/"); s != "docker-basics" {
		t.Errorf("slug = %q", s)
	}
}
