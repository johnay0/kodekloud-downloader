package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnay0/kodekloud-downloader/internal/api"
	"github.com/johnay0/kodekloud-downloader/internal/auth"
)

const catalogJSON = `{"courses":[
 {"id":"f","slug":"free","title":"FreeOne","plan":"Free"},
 {"id":"s","slug":"std","title":"StdOne","plan":"Standard"},
 {"id":"p","slug":"pro","title":"ProOne","plan":"Pro"}],"metadata":{"next_page":null}}`

func fakeServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/api/courses":
			_, _ = io.WriteString(w, catalogJSON)
		case strings.HasPrefix(p, "/api/courses/"):
			slug := strings.TrimPrefix(p, "/api/courses/")
			typ := "video"
			if slug == "std" {
				typ = "lab"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": slug, "slug": slug, "title": slug + " title", "plan": "Pro",
				"modules": []any{map[string]any{"id": "m", "title": "M", "lessons": []any{map[string]any{"id": "l-" + slug, "title": "L", "type": typ}}}}})
		case strings.HasPrefix(p, "/api/lessons/"):
			_, _ = io.WriteString(w, `{"video_url":"https://vimeo.com/1"}`)
		case strings.HasPrefix(p, "/api/users_progresses/courses/"):
			_, _ = io.WriteString(w, `{"enrolled":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testApp(t *testing.T, srv *httptest.Server, stdin string) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	a := newApp()
	a.stdin, a.stdout, a.stderr = strings.NewReader(stdin), &out, &errb
	a.lookPath = func(s string) (string, error) { return "/bin/" + s, nil }
	a.newAPI = func(ts api.TokenSource) *api.Client {
		c := api.New(ts, a.log)
		c.BaseURL = srv.URL + "/api"
		return c
	}
	a.runner = func(ctx context.Context, url, outBase string) ([]byte, error) {
		return nil, os.WriteFile(outBase+".mkv", []byte("v"), 0o644)
	}
	t.Setenv("KODEKLOUD_EMAIL", "")
	t.Setenv("KODEKLOUD_PASSWORD", "")
	t.Setenv("KODEKLOUD_AUTH_STATE", filepath.Join(t.TempDir(), "auth.json"))
	return a, &out, &errb
}

func run(a *app, args ...string) error {
	cmd := a.root()
	cmd.SetArgs(args)
	cmd.SetOut(a.stdout)
	cmd.SetErr(a.stderr)
	return cmd.ExecuteContext(context.Background())
}

func cachedSession(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "auth.json")
	b, _ := json.Marshal(map[string]any{"refresh_token": "rt", "id_token": "id", "expires_at": float64(time.Now().Add(time.Hour).Unix()), "email": "me@x.com"})
	_ = os.WriteFile(p, b, 0o600)
	return p
}

func TestDryRunListsOnlyPaid(t *testing.T) {
	srv := fakeServer(t)
	a, out, _ := testApp(t, srv, "")
	if err := run(a, "dl", "--all-paid", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, "StdOne") || !strings.Contains(o, "ProOne") || strings.Contains(o, "FreeOne") ||
		!strings.Contains(o, "2 paid course(s): 1 Standard, 1 Pro (1 Free course(s) skipped).") {
		t.Errorf("output:\n%s", o)
	}
}

func TestValidation(t *testing.T) {
	srv := fakeServer(t)
	for _, args := range [][]string{
		{"dl", "--all-paid", "https://x/y"},
		{"dl", "--dry-run"},
		{"dl", "-q", "4k"},
		{"dl", "-j", "0"},
	} {
		a, _, _ := testApp(t, srv, "")
		if err := run(a, args...); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
	a, _, _ := testApp(t, srv, "")
	err := run(a, "dl", "https://learn.kodekloud.com/user/courses/x")
	if err == nil || !strings.Contains(err.Error(), "Not signed in") {
		t.Errorf("err = %v", err)
	}
}

func TestAllPaidDownloadsWithCachedSessionAndReportsLabOnly(t *testing.T) {
	srv := fakeServer(t)
	a, out, _ := testApp(t, srv, "")
	out2 := t.TempDir()
	err := run(a, "dl", "--all-paid", "-o", out2, "--auth-state", cachedSession(t), "-j", "3")
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	o := out.String()
	for _, want := range []string{"Done in", "1/2 course(s) complete", "Skipped 1 lab-only course(s)", "  - StdOne"} {
		if !strings.Contains(o, want) {
			t.Errorf("missing %q in:\n%s", want, o)
		}
	}
	if _, err := os.Stat(filepath.Join(out2, "KodeKloud", "ProOne", "01 - M", "001 - L.mkv")); err != nil {
		t.Error(err)
	}
}

func TestEmailPromptsForPassword(t *testing.T) {
	srv := fakeServer(t)
	fb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["password"] != "s3cret" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"INVALID_LOGIN_CREDENTIALS"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"idToken":"id","refreshToken":"rt","expiresIn":"3600"}`)
	}))
	defer fb.Close()
	old := auth.SignInURL
	auth.SignInURL = fb.URL
	defer func() { auth.SignInURL = old }()

	a, out, _ := testApp(t, srv, "s3cret\n")
	if err := run(a, "dl", "-e", "me@x.com", "-o", t.TempDir(), "https://learn.kodekloud.com/user/courses/pro"); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	a, _, _ = testApp(t, srv, "wrong\n")
	err := run(a, "dl", "-e", "me@x.com", "--no-auth-cache", "https://learn.kodekloud.com/user/courses/pro")
	if err == nil || !strings.Contains(err.Error(), "wrong email or password") {
		t.Errorf("err = %v", err)
	}
}
