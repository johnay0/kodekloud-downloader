package naming

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func loadGz(t *testing.T, name string, v any) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(gz).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func skipOnWindows(t *testing.T) {
	// Fixtures were generated on POSIX ("/" separators, absolute /data path).
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path fixtures")
	}
}

// Every lesson path of the real catalog (218 courses, 13.6k lessons) must be
// identical to what the Python version produced, for both naming schemes.
func TestLessonPathMatchesPythonForWholeCatalog(t *testing.T) {
	skipOnWindows(t)
	var fx struct {
		OutputDir string `json:"output_dir"`
		Courses   []struct {
			Title   string `json:"title"`
			Modules []struct {
				Title   string   `json:"title"`
				Lessons []string `json:"lessons"`
			} `json:"modules"`
			NewSHA    string `json:"new_sha256"`
			LegacySHA string `json:"legacy_sha256"`
		} `json:"courses"`
	}
	loadGz(t, "catalog_paths.json.gz", &fx)
	if len(fx.Courses) < 200 {
		t.Fatalf("fixture too small: %d courses", len(fx.Courses))
	}
	for _, c := range fx.Courses {
		for _, scheme := range []struct {
			name string
			fn   Sanitizer
			want string
		}{{"new", Sanitize, c.NewSHA}, {"legacy", LegacySanitize, c.LegacySHA}} {
			var lines []string
			gi := 0
			for mi, m := range c.Modules {
				for _, l := range m.Lessons {
					gi++
					lines = append(lines, LessonPath(fx.OutputDir, c.Title, mi+1, m.Title, gi, l, scheme.fn))
				}
			}
			sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
			if got := hex.EncodeToString(sum[:]); got != scheme.want {
				t.Errorf("%s paths differ from Python for course %q", scheme.name, c.Title)
			}
		}
	}
}

func TestTrickyPathsMatchPython(t *testing.T) {
	skipOnWindows(t)
	var fx struct {
		OutputDir string `json:"output_dir"`
		Cases     []struct {
			Args   []any  `json:"args"`
			New    string `json:"new"`
			Legacy string `json:"legacy"`
		} `json:"cases"`
		Sanitize []struct {
			In     string `json:"in"`
			Max    int    `json:"max"`
			New    string `json:"new"`
			Legacy string `json:"legacy"`
		} `json:"sanitize"`
	}
	loadGz(t, "tricky_paths.json.gz", &fx)
	for _, c := range fx.Cases {
		course, mod, lesson := c.Args[0].(string), c.Args[2].(string), c.Args[4].(string)
		mi, li := int(c.Args[1].(float64)), int(c.Args[3].(float64))
		if got := LessonPath(fx.OutputDir, course, mi, mod, li, lesson, Sanitize); got != c.New {
			t.Errorf("new:\n got %q\nwant %q", got, c.New)
		}
		if got := LessonPath(fx.OutputDir, course, mi, mod, li, lesson, LegacySanitize); got != c.Legacy {
			t.Errorf("legacy:\n got %q\nwant %q", got, c.Legacy)
		}
	}
	for _, s := range fx.Sanitize {
		if got := Sanitize(s.In, s.Max); got != s.New {
			t.Errorf("Sanitize(%q, %d) = %q, want %q", s.In, s.Max, got, s.New)
		}
		if got := LegacySanitize(s.In, s.Max); got != s.Legacy {
			t.Errorf("LegacySanitize(%q, %d) = %q, want %q", s.In, s.Max, got, s.Legacy)
		}
	}
}

func TestReadableNames(t *testing.T) {
	for in, want := range map[string]string{
		"AZ-204: Developing Solutions":                  "AZ-204 - Developing Solutions",
		"AWS CodePipeline (CI/CD Pipeline)":             "AWS CodePipeline (CI-CD Pipeline)",
		"Docker - SWARM | SERVICES | STACKS - Hands-on": "Docker - SWARM - SERVICES - STACKS - Hands-on",
		"What is Docker?":                               "What is Docker",
		"keep_my_underscores":                           "keep_my_underscores",
	} {
		if got := Sanitize(in, 80); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithSuffix(t *testing.T) {
	for in, want := range map[string]string{
		"/a/003 - Intro":      "/a/003 - Intro.md",
		"/a/003 - Intro v1.2": "/a/003 - Intro v1.md", // pathlib quirk, kept for parity
		"/a/.hidden":          "/a/.hidden.md",
		"/a/ends.":            "/a/ends..md",
	} {
		if got := WithSuffix(in, ".md"); got != want {
			t.Errorf("WithSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrateLegacyLesson(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	args := func(s Sanitizer) string {
		return LessonPath(dir, "AZ-204: Dev", 1, "Intro: Basics", 1, "What is CI/CD?", s)
	}
	old, new := args(LegacySanitize), args(Sanitize)
	write(t, old+".mkv", "video")
	write(t, old+".en.vtt", "subs")
	write(t, filepath.Join(filepath.Dir(old), "slides.pdf"), "pdf")
	write(t, filepath.Join(filepath.Dir(old), "002 - Other.mkv"), "other")

	MigrateLegacyLesson(old, new, log)

	if _, err := os.Stat(filepath.Dir(filepath.Dir(old))); err == nil {
		t.Error("old course folder still exists")
	}
	if got := filepath.Base(filepath.Dir(filepath.Dir(new))); got != "AZ-204 - Dev" {
		t.Errorf("course folder = %q", got)
	}
	if read(t, new+".mkv") != "video" || read(t, new+".en.vtt") != "subs" {
		t.Error("lesson files not moved")
	}
	read(t, filepath.Join(filepath.Dir(new), "slides.pdf"))
	read(t, filepath.Join(filepath.Dir(new), "002 - Other.mkv"))
}

func TestMovePathMergesWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	old, new := filepath.Join(dir, "A_ B"), filepath.Join(dir, "A - B")
	write(t, filepath.Join(old, "1.mkv"), "old1")
	write(t, filepath.Join(old, "2.mkv"), "old2")
	write(t, filepath.Join(new, "2.mkv"), "new2")

	MovePath(old, new, log)

	if read(t, filepath.Join(new, "1.mkv")) != "old1" {
		t.Error("1.mkv not merged")
	}
	if read(t, filepath.Join(new, "2.mkv")) != "new2" {
		t.Error("existing file overwritten")
	}
	if read(t, filepath.Join(old, "2.mkv")) != "old2" {
		t.Error("conflicting file lost")
	}
}
