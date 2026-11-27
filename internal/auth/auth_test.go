package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeFirebase struct {
	signIns, refreshes atomic.Int32
	failRefresh        bool
	signInStatus       int
	signInError        string
	lastPassword       string
	mu                 sync.Mutex
}

func (f *fakeFirebase) start(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") == "" {
			t.Error("missing API key")
		}
		switch r.URL.Path {
		case "/signin":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.lastPassword, _ = body["password"].(string)
			f.mu.Unlock()
			n := f.signIns.Add(1)
			if f.signInStatus != 0 {
				w.WriteHeader(f.signInStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": f.signInError}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"idToken": "id-pw-" + string(rune('0'+n)), "refreshToken": "rt-1", "expiresIn": "3600"})
		case "/refresh":
			_ = r.ParseForm()
			n := f.refreshes.Add(1)
			if f.failRefresh {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"message":"TOKEN_EXPIRED"}}`)
				return
			}
			if r.Form.Get("grant_type") != "refresh_token" {
				t.Error("bad grant_type")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id_token": "id-new-" + string(rune('0'+n)), "refresh_token": "rt-new", "expires_in": "3600"})
		}
	}))
	t.Cleanup(srv.Close)
	oldS, oldR := SignInURL, RefreshURL
	SignInURL, RefreshURL = srv.URL+"/signin", srv.URL+"/refresh"
	t.Cleanup(func() { SignInURL, RefreshURL = oldS, oldR })
}

func TestPasswordSignInCachesRefreshTokenSecurely(t *testing.T) {
	fb := &fakeFirebase{}
	fb.start(t)
	state := filepath.Join(t.TempDir(), "auth.json")
	a := New("me@example.com", "hunter2", state, quiet)
	tok, err := a.Token(context.Background())
	if err != nil || tok != "id-pw-1" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	raw, _ := os.ReadFile(state)
	if strings.Contains(string(raw), "hunter2") {
		t.Error("password written to cache")
	}
	var s map[string]any
	_ = json.Unmarshal(raw, &s)
	for _, k := range []string{"refresh_token", "id_token", "expires_at", "email"} {
		if _, ok := s[k]; !ok {
			t.Errorf("cache missing %q", k)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(state); fi.Mode().Perm() != 0o600 {
			t.Errorf("cache mode = %v", fi.Mode().Perm())
		}
	}
	if strings.Contains(a.String(), "hunter2") {
		t.Error("String() leaks password")
	}
}

func TestRenewsCachedSessionBeforeExpiry(t *testing.T) {
	fb := &fakeFirebase{}
	fb.start(t)
	state := filepath.Join(t.TempDir(), "auth.json")
	// Cached by an earlier run; expires inside the 5 min leeway.
	cached := map[string]any{"refresh_token": "rt-old", "id_token": "id-old", "expires_at": float64(time.Now().Unix() + 60), "email": "me@x.com"}
	b, _ := json.Marshal(cached)
	_ = os.WriteFile(state, b, 0o600)

	a := New("", "", state, quiet)
	if a.Email != "me@x.com" || !a.HasCredentials() {
		t.Fatal("cached session not loaded")
	}
	tok, err := a.Token(context.Background())
	if err != nil || tok != "id-new-1" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	if tok, _ := a.Token(context.Background()); tok != "id-new-1" || fb.refreshes.Load() != 1 {
		t.Error("renewed again although token is fresh")
	}
}

func TestCachedSessionOfAnotherAccountIsIgnored(t *testing.T) {
	state := filepath.Join(t.TempDir(), "auth.json")
	_ = os.WriteFile(state, []byte(`{"refresh_token":"rt","id_token":"id-cached","expires_at":9999999999,"email":"a@x.com"}`), 0o600)
	if tok, err := New("", "", state, quiet).Token(context.Background()); err != nil || tok != "id-cached" {
		t.Fatalf("cached session: %q %v", tok, err)
	}
	if New("b@x.com", "", state, quiet).HasRefreshToken() {
		t.Error("used another account's cached session")
	}
}

func TestRefreshFailureFallsBackToPassword(t *testing.T) {
	fb := &fakeFirebase{failRefresh: true}
	fb.start(t)
	state := filepath.Join(t.TempDir(), "auth.json")
	_ = os.WriteFile(state, []byte(`{"refresh_token":"dead","email":"a@x.com"}`), 0o600)
	tok, err := New("a@x.com", "pw", state, quiet).Token(context.Background())
	if err != nil || tok != "id-pw-1" || fb.refreshes.Load() != 1 {
		t.Fatalf("Token = %q, %v (refreshes %d)", tok, err, fb.refreshes.Load())
	}
}

func TestSignInErrorIsExplained(t *testing.T) {
	fb := &fakeFirebase{signInStatus: 400, signInError: "MISSING_RECAPTCHA_TOKEN"}
	fb.start(t)
	_, err := New("a@x.com", "pw", "", quiet).Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reCAPTCHA") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoCredentials(t *testing.T) {
	a := New("a@x.com", "", "", quiet)
	if a.HasCredentials() {
		t.Fatal("should have no credentials")
	}
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if a.ForceRefresh(context.Background(), "") {
		t.Fatal("ForceRefresh should fail")
	}
}

// Many workers hitting 401 with the same token must cause a single renewal.
func TestConcurrentForceRefreshRenewsOnce(t *testing.T) {
	fb := &fakeFirebase{}
	fb.start(t)
	a := New("a@x.com", "pw", "", quiet)
	old, _ := a.Token(context.Background())
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); a.ForceRefresh(context.Background(), old) }()
	}
	wg.Wait()
	if n := fb.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
}

func TestIsKodeKloudURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://learn-api.kodekloud.com/api/x":    true,
		"https://kodekloud.com/x.pdf":              true,
		"http://learn-api.kodekloud.com/api":       false,
		"https://raw.githubusercontent.com/a.pdf":  false,
		"https://kodekloud.com.evil.example/x":     false,
		"https://evilkodekloud.com/x":              false,
		"https://kodekloud.com@evil.example/x.pdf": false,
		"not a url": false,
	} {
		if got := IsKodeKloudURL(u); got != want {
			t.Errorf("IsKodeKloudURL(%q) = %v", u, got)
		}
	}
}
