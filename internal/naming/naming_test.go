package naming

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Every lesson path of the real catalog (218 courses, 13.6k lessons) is pinned:
// if this fails, existing downloads would no longer be found and would be
// downloaded again. Fix the code, don't regenerate the fixture.
func TestLessonPathsAreStableForWholeCatalog(t *testing.T) {
	skipOnWindows(t)
	var fx struct {
		OutputDir string `json:"output_dir"`
		Courses   []struct {
			Title   string `json:"title"`
			Modules []struct {
				Title   string   `json:"title"`
				Lessons []string `json:"lessons"`
			} `json:"modules"`
			SHA string `json:"sha256"`
		} `json:"courses"`
	}
	loadGz(t, "catalog_paths.json.gz", &fx)
	if len(fx.Courses) < 200 {
		t.Fatalf("fixture too small: %d courses", len(fx.Courses))
	}
	for _, c := range fx.Courses {
		var lines []string
		gi := 0
		for mi, m := range c.Modules {
			for _, l := range m.Lessons {
				gi++
				lines = append(lines, LessonPath(fx.OutputDir, c.Title, mi+1, m.Title, gi, l))
			}
		}
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		if got := hex.EncodeToString(sum[:]); got != c.SHA {
			t.Errorf("paths changed for course %q", c.Title)
		}
	}
}

func TestTrickyPathsAreStable(t *testing.T) {
	skipOnWindows(t)
	var fx struct {
		OutputDir string `json:"output_dir"`
		Cases     []struct {
			Args []any  `json:"args"`
			Want string `json:"want"`
		} `json:"cases"`
		Sanitize []struct {
			In   string `json:"in"`
			Max  int    `json:"max"`
			Want string `json:"want"`
		} `json:"sanitize"`
	}
	loadGz(t, "tricky_paths.json.gz", &fx)
	for _, c := range fx.Cases {
		course, mod, lesson := c.Args[0].(string), c.Args[2].(string), c.Args[4].(string)
		mi, li := int(c.Args[1].(float64)), int(c.Args[3].(float64))
		if got := LessonPath(fx.OutputDir, course, mi, mod, li, lesson); got != c.Want {
			t.Errorf("\n got %q\nwant %q", got, c.Want)
		}
	}
	for _, s := range fx.Sanitize {
		if got := Sanitize(s.In, s.Max); got != s.Want {
			t.Errorf("Sanitize(%q, %d) = %q, want %q", s.In, s.Max, got, s.Want)
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
		"/a/003 - Intro v1.2": "/a/003 - Intro v1.md", // quirk, kept so notes keep their names
		"/a/.hidden":          "/a/.hidden.md",
		"/a/ends.":            "/a/ends..md",
	} {
		if got := WithSuffix(in, ".md"); got != want {
			t.Errorf("WithSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
