// Package api talks to learn-api.kodekloud.com.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/johnay0/kodekloud-downloader/internal/auth"
)

const DefaultBaseURL = "https://learn-api.kodekloud.com/api"

// TokenSource is satisfied by *auth.Firebase.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	ForceRefresh(ctx context.Context, rejected string) bool
}

type Named struct {
	Name string `json:"name"`
}

type Course struct {
	ID         string  `json:"id"`
	Slug       string  `json:"slug"`
	Title      string  `json:"title"`
	Plan       string  `json:"plan"`
	Tutors     []Named `json:"tutors"`
	Categories []Named `json:"categories"`
}

type Lesson struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type Module struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Lessons []Lesson `json:"lessons"`
}

type CourseDetail struct {
	Course
	Modules []Module `json:"modules"`
}

type LessonContent struct {
	VideoURL string `json:"video_url"`
	Content  string `json:"content"`
}

// HTTPError is a non-2xx response.
type HTTPError struct {
	Status  int
	Message string
	URL     string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// StatusOf returns the HTTP status of err, or 0.
func StatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// AuthFailedError means the API kept answering 401 and renewal failed.
var AuthFailedError = errors.New("authentication failed (401 Unauthorized) and the session could not be renewed; sign in again with --email/--password")

type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Auth       TokenSource // nil: anonymous (catalog endpoints are public)
	Log        *slog.Logger
	MaxRetries int
	backoff    func(attempt int) time.Duration
}

func New(auth TokenSource, log *slog.Logger) *Client {
	return &Client{
		BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 60 * time.Second},
		Auth: auth, Log: log, MaxRetries: 4,
	}
}

func (c *Client) wait(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s >= 0 && s < 120 {
		return time.Duration(s) * time.Second
	}
	if c.backoff != nil {
		return c.backoff(attempt)
	}
	base := time.Duration(1<<attempt) * 500 * time.Millisecond
	return base + time.Duration(rand.Int64N(int64(base/2)+1))
}

// Do sends a request (JSON body if body != nil) with retries on network
// errors/429/5xx and one token renewal on 401. Authorization is only ever
// attached to KodeKloud hosts.
func (c *Client) Do(ctx context.Context, method, rawURL string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		var token string
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		// BaseURL is set by code (never by lesson content), so it is trusted too.
		if c.Auth != nil && (strings.HasPrefix(rawURL, c.BaseURL+"/") || auth.IsKodeKloudURL(rawURL)) {
			if token, err = c.Auth.Token(ctx); err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil || attempt >= c.MaxRetries {
				return err
			}
			c.Log.Debug("request failed, retrying", "url", rawURL, "err", err)
			if !sleep(ctx, c.wait(attempt, "")) {
				return ctx.Err()
			}
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusUnauthorized && c.Auth != nil && token != "":
			if refreshed || !c.Auth.ForceRefresh(ctx, token) {
				return AuthFailedError
			}
			refreshed = true
			attempt--
			continue
		case (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < c.MaxRetries:
			d := c.wait(attempt, resp.Header.Get("Retry-After"))
			c.Log.Debug("server busy, retrying", "url", rawURL, "status", resp.StatusCode, "in", d)
			if !sleep(ctx, d) {
				return ctx.Err()
			}
			continue
		case resp.StatusCode/100 != 2:
			return &HTTPError{Status: resp.StatusCode, Message: errorMessage(data), URL: rawURL}
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decoding %s: %w", rawURL, err)
			}
		}
		return nil
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func errorMessage(data []byte) string {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	for _, k := range []string{"message", "error"} {
		if s, ok := m[k].(string); ok {
			return s
		}
	}
	return ""
}

// Courses returns the whole public catalog.
func (c *Client) Courses(ctx context.Context) ([]Course, error) {
	var all []Course
	page := 1
	for {
		var resp struct {
			Courses  []Course `json:"courses"`
			Metadata struct {
				NextPage *int `json:"next_page"`
			} `json:"metadata"`
		}
		u := fmt.Sprintf("%s/courses?page=%d&limit=30", c.BaseURL, page)
		if err := c.Do(ctx, http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Courses...)
		if resp.Metadata.NextPage == nil {
			return all, nil
		}
		page = *resp.Metadata.NextPage
	}
}

func (c *Client) CourseDetail(ctx context.Context, slug string) (*CourseDetail, error) {
	var d CourseDetail
	err := c.Do(ctx, http.MethodGet, c.BaseURL+"/courses/"+url.PathEscape(slug), nil, &d)
	return &d, err
}

func (c *Client) Lesson(ctx context.Context, lessonID, courseID string) (*LessonContent, error) {
	var l LessonContent
	u := fmt.Sprintf("%s/lessons/%s?course_id=%s", c.BaseURL, url.PathEscape(lessonID), url.QueryEscape(courseID))
	err := c.Do(ctx, http.MethodGet, u, nil, &l)
	return &l, err
}

// EnrollmentStatus reads "enrolled" from users_progresses/courses/{id};
// nil means unknown.
func (c *Client) EnrollmentStatus(ctx context.Context, courseID string) *bool {
	var resp map[string]any
	if err := c.Do(ctx, http.MethodGet, c.BaseURL+"/users_progresses/courses/"+url.PathEscape(courseID), nil, &resp); err != nil {
		c.Log.Debug("could not read enrollment status", "course", courseID, "err", err)
		return nil
	}
	if v, ok := resp["enrolled"].(bool); ok {
		return &v
	}
	return nil
}

// Enroll posts to users_progresses/enroll_course. The web app's axios
// interceptor snake_cases request bodies, so the key is course_id.
func (c *Client) Enroll(ctx context.Context, courseID string) error {
	return c.Do(ctx, http.MethodPost, c.BaseURL+"/users_progresses/enroll_course", map[string]string{"course_id": courseID}, nil)
}

// SlugFromURL mirrors parse_course_from_url: the last path segment.
func SlugFromURL(raw string) string {
	raw = strings.Trim(raw, "/")
	return raw[strings.LastIndex(raw, "/")+1:]
}
