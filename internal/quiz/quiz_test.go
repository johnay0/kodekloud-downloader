package quiz

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadKeepsQuestionOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/quizzes/all":
			// JSON object order (z, a, m) must be kept.
			_, _ = io.WriteString(w, `[{"name":"Docker: Basics","questions":{"z":"q3","a":"q1","m":"q2"}}]`)
		case "/api/questions/question":
			id := r.URL.Query().Get("id")
			_, _ = io.WriteString(w, `{"question":"Question `+id+`","answers":["x","y"],"correctAnswers":["x"],"code":{"script":""}}`)
		}
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL + "/api"
	defer func() { BaseURL = old }()

	dir := t.TempDir()
	if err := Download(context.Background(), srv.Client(), dir, true, 4, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "Docker - Basics.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i3, i1, i2 := strings.Index(s, "Question q3"), strings.Index(s, "Question q1"), strings.Index(s, "Question q2")
	if !(i3 >= 0 && i3 < i1 && i1 < i2) || !strings.Contains(s, "**1. Question q3**") || !strings.Contains(s, "* [x] x") {
		t.Errorf("unexpected markdown:\n%s", s)
	}
}
