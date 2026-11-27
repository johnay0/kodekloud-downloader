// Package auth keeps a valid KodeKloud (Firebase) ID token for the whole run.
//
// It signs in with email/password via the Firebase Identity Toolkit REST API
// and renews the ~1h ID token with the refresh token shortly before it
// expires (or after a 401), falling back to a fresh password sign-in. The
// refresh token is cached (0600) in the same file and format as the Python
// version, so either can reuse the other's session. The password is never
// written anywhere, and tokens are only sent to https://*.kodekloud.com.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultAPIKey is the public web API key of KodeKloud's Firebase project,
// as shipped in the learn.kodekloud.com bundle (verified 2026-11).
const DefaultAPIKey = "AIzaSyAVy3_TcBija6Pc9_-glfSZuqft01zgoSA"

const refreshLeeway = 5 * time.Minute

var (
	SignInURL  = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword"
	RefreshURL = "https://securetoken.googleapis.com/v1/token"
)

var errorHints = map[string]string{
	"INVALID_LOGIN_CREDENTIALS":   "wrong email or password",
	"INVALID_PASSWORD":            "wrong email or password",
	"EMAIL_NOT_FOUND":             "wrong email or password",
	"INVALID_EMAIL":               "the email address is malformed",
	"USER_DISABLED":               "this account is disabled",
	"TOO_MANY_ATTEMPTS_TRY_LATER": "too many attempts, try again later",
	"PASSWORD_LOGIN_DISABLED":     "KodeKloud disabled password sign-in",
	"MISSING_RECAPTCHA_TOKEN":     "KodeKloud now requires reCAPTCHA for sign-in",
	"TOKEN_EXPIRED":               "the cached session expired, sign in again",
	"INVALID_REFRESH_TOKEN":       "the cached session is invalid, sign in again",
	"USER_NOT_FOUND":              "the account no longer exists",
}

// Error means no valid token could be obtained.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// DefaultStatePath is ~/.cache/kodekloud-downloader/auth.json (shared with
// the Python version).
func DefaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "kodekloud-downloader", "auth.json")
}

// IsKodeKloudURL is true only for https URLs on kodekloud.com or a subdomain.
func IsKodeKloudURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "kodekloud.com" || strings.HasSuffix(host, ".kodekloud.com")
}

type state struct {
	RefreshToken string  `json:"refresh_token"`
	IDToken      string  `json:"id_token"`
	ExpiresAt    float64 `json:"expires_at"`
	Email        *string `json:"email"`
}

// Firebase is a concurrency-safe, self-renewing token source.
type Firebase struct {
	Email     string
	password  string
	statePath string
	apiKey    string
	http      *http.Client
	log       *slog.Logger
	now       func() time.Time

	mu    sync.Mutex
	state *state
}

// New loads the cached session (if any) from statePath ("" disables the cache).
func New(email, password, statePath string, log *slog.Logger) *Firebase {
	key := os.Getenv("KODEKLOUD_FIREBASE_API_KEY")
	if key == "" {
		key = DefaultAPIKey
	}
	f := &Firebase{
		Email: email, password: password, statePath: statePath, apiKey: key,
		http: &http.Client{Timeout: 30 * time.Second}, log: log, now: time.Now,
	}
	f.state = f.load()
	if f.state != nil && f.Email == "" && f.state.Email != nil {
		f.Email = *f.state.Email
	}
	return f
}

func (f *Firebase) String() string { return fmt.Sprintf("Firebase(email=%q)", f.Email) } // no secrets

func (f *Firebase) load() *state {
	if f.statePath == "" {
		return nil
	}
	raw, err := os.ReadFile(f.statePath)
	if err != nil {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		f.log.Warn("ignoring unreadable auth cache", "path", f.statePath, "err", err)
		return nil
	}
	// Cache files from the older johnay0 fork stored the ID token as "session_cookie".
	if v, ok := data["session_cookie"]; ok {
		if _, has := data["id_token"]; !has {
			data["id_token"] = v
		}
	}
	norm, _ := json.Marshal(data)
	var s state
	if err := json.Unmarshal(norm, &s); err != nil || s.RefreshToken == "" {
		f.log.Warn("ignoring unreadable auth cache", "path", f.statePath)
		return nil
	}
	if f.Email != "" && s.Email != nil && !strings.EqualFold(*s.Email, f.Email) {
		f.log.Info("cached session belongs to another account; ignoring it")
		return nil
	}
	return &s
}

func (f *Firebase) save() {
	if f.statePath == "" || f.state == nil || f.state.RefreshToken == "" {
		return
	}
	data, _ := json.Marshal(f.state)
	if err := os.MkdirAll(filepath.Dir(f.statePath), 0o700); err != nil {
		f.log.Warn("could not write auth cache", "err", err)
		return
	}
	tmp := f.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		f.log.Warn("could not write auth cache", "err", err)
		return
	}
	if err := os.Rename(tmp, f.statePath); err != nil {
		f.log.Warn("could not write auth cache", "err", err)
		return
	}
	_ = os.Chmod(f.statePath, 0o600)
}

func (f *Firebase) HasRefreshToken() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state != nil && f.state.RefreshToken != ""
}

func (f *Firebase) HasPassword() bool { return f.Email != "" && f.password != "" }

func (f *Firebase) HasCredentials() bool { return f.HasRefreshToken() || f.HasPassword() }

func (f *Firebase) SetPassword(p string) { f.password = p }

func (f *Firebase) post(ctx context.Context, endpoint string, body []byte, contentType string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?key="+url.QueryEscape(f.apiKey), strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	// Firebase API keys may be referrer-restricted; look like the web app.
	req.Header.Set("Origin", "https://learn.kodekloud.com")
	req.Header.Set("Referer", "https://learn.kodekloud.com/")
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s", firebaseError(resp.StatusCode, data))
	}
	return data, nil
}

func firebaseError(status int, data map[string]any) string {
	e, _ := data["error"].(map[string]any)
	code, _ := e["message"].(string)
	if code == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	key := strings.SplitN(strings.SplitN(code, " ", 2)[0], ":", 2)[0]
	if hint, ok := errorHints[key]; ok {
		return fmt.Sprintf("%s (%s)", code, hint)
	}
	return code
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func expiresIn(m map[string]any, k string) time.Duration {
	var secs int
	if _, err := fmt.Sscan(str(m, k), &secs); err != nil || secs <= 0 {
		secs = 3600
	}
	return time.Duration(secs) * time.Second
}

func (f *Firebase) signIn(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"email": f.Email, "password": f.password, "returnSecureToken": true})
	data, err := f.post(ctx, SignInURL, body, "application/json")
	if err != nil {
		return fmt.Errorf("KodeKloud sign-in failed: %w", err)
	}
	email := f.Email
	f.state = &state{
		RefreshToken: str(data, "refreshToken"),
		IDToken:      str(data, "idToken"),
		ExpiresAt:    float64(f.now().Add(expiresIn(data, "expiresIn")).UnixNano()) / 1e9,
		Email:        &email,
	}
	f.save()
	f.log.Info("signed in to KodeKloud", "email", f.Email)
	return nil
}

func (f *Firebase) refresh(ctx context.Context) error {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {f.state.RefreshToken}}
	data, err := f.post(ctx, RefreshURL, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return fmt.Errorf("session renewal failed: %w", err)
	}
	if rt := str(data, "refresh_token"); rt != "" {
		f.state.RefreshToken = rt
	}
	f.state.IDToken = str(data, "id_token")
	f.state.ExpiresAt = float64(f.now().Add(expiresIn(data, "expires_in")).UnixNano()) / 1e9
	f.save()
	f.log.Info("renewed KodeKloud session token")
	return nil
}

// renew gets a new ID token: refresh token first, then password sign-in.
func (f *Firebase) renew(ctx context.Context) error {
	var errs []string
	if f.state != nil && f.state.RefreshToken != "" {
		err := f.refresh(ctx)
		if err == nil {
			return nil
		}
		errs = append(errs, err.Error())
		f.log.Warn("signing in again", "reason", err)
	}
	if f.HasPassword() {
		err := f.signIn(ctx)
		if err == nil {
			return nil
		}
		errs = append(errs, err.Error())
	}
	if len(errs) == 0 {
		errs = append(errs, "no cached session, sign in with --email/--password")
	}
	return &Error{Msg: strings.Join(errs, "; ")}
}

// Token returns a valid ID token, renewing it if it expires within 5 minutes.
func (f *Firebase) Token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil || f.state.IDToken == "" ||
		float64(f.now().Add(refreshLeeway).UnixNano())/1e9 >= f.state.ExpiresAt {
		if err := f.renew(ctx); err != nil {
			return "", err
		}
	}
	return f.state.IDToken, nil
}

// ForceRefresh renews regardless of the cached expiry (used after a 401).
// Concurrent callers that saw the same rejected token share one renewal.
func (f *Firebase) ForceRefresh(ctx context.Context, rejected string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != nil && f.state.IDToken != "" && f.state.IDToken != rejected {
		return true // another goroutine already renewed it
	}
	if err := f.renew(ctx); err != nil {
		f.log.Error("could not renew session", "err", err)
		return false
	}
	return true
}
