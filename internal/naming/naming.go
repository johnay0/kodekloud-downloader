// Package naming turns course/module/lesson titles into file paths.
//
// The output must match the Python version byte for byte (see testdata/,
// generated from it), so existing downloads are recognised and skipped.
package naming

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxPathLength mirrors _MAX_PATH_LENGTH: Windows MAX_PATH is 260; leave room
// for the drive, separators and yt-dlp's file extension.
const MaxPathLength = 220

const reservedChars = `\/:*?"<>|`

var reservedNames = func() map[string]bool {
	m := map[string]bool{"con": true, "prn": true, "aux": true, "nul": true}
	for i := 1; i <= 9; i++ {
		m[fmt.Sprintf("com%d", i)] = true
		m[fmt.Sprintf("lpt%d", i)] = true
	}
	return m
}()

// readable stand-ins for characters Windows forbids, applied in order:
// "AZ-204: Dev" -> "AZ-204 - Dev", "CI/CD" -> "CI-CD", "A | B" -> "A - B".
var replacer = []struct{ old, new string }{
	{": ", " - "}, {":", "-"}, {"/", "-"}, {`\`, "-"}, {"|", "-"},
	{`"`, "'"}, {"<", ""}, {">", ""}, {"?", ""}, {"*", ""},
}

var (
	multiSpace      = regexp.MustCompile(` {2,}`)
	multiUnderscore = regexp.MustCompile(`_{2,}`)
)

// Sanitizer turns a title into a safe path component of at most max runes.
type Sanitizer func(name string, max int) string

// Sanitize is the current sanitizer (readable stand-ins, no underscores).
func Sanitize(name string, max int) string {
	var b strings.Builder
	for _, r := range name {
		if r >= ' ' { // drop control characters
			b.WriteRune(r)
		}
	}
	s := b.String()
	for _, r := range replacer {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	s = multiSpace.ReplaceAllString(s, " ")
	return finish(s, max)
}

// LegacySanitize reproduces the pre-2026-11 sanitizer (invalid chars -> "_")
// so downloads made by older versions can be found and renamed.
func LegacySanitize(name string, max int) string {
	s := strings.Map(func(r rune) rune {
		if strings.ContainsRune(reservedChars, r) {
			return '_'
		}
		return r
	}, name)
	s = multiUnderscore.ReplaceAllString(s, "_")
	return finish(s, max)
}

func finish(s string, max int) string {
	s = strings.Trim(s, ". ")
	s = strings.TrimRight(truncate(s, max), ". ")
	if s == "" || reservedNames[strings.TrimRight(strings.ToLower(s), ".")] {
		s = "_"
	}
	return s
}

// truncate keeps the first n runes (Python counts code points, not bytes).
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// pyEnd resolves a Python slice end index (negative counts from the end).
func pyEnd(end, length int) int {
	if end < 0 {
		end += length
	}
	return max(0, min(end, length))
}

// ShortenComponent mirrors _shorten_component, quirks included.
func ShortenComponent(name string, maxLen int) string {
	r := []rune(name)
	if len(r) <= maxLen {
		return name
	}
	dot := strings.LastIndex(name, ".")
	if dot >= 0 {
		dot = utf8.RuneCountInString(name[:dot])
	}
	if dot > 0 && len(r)-dot <= 10 {
		stem := r[:pyEnd(dot-(len(r)-maxLen+1), len(r))]
		return string(stem) + string(r[dot:])
	}
	return string(r[:pyEnd(maxLen, len(r))])
}

// LessonPath mirrors create_file_path: <out>/KodeKloud/<course>/<NN - module>/<NNN - lesson>
// (no extension), shortened to stay under MaxPathLength.
func LessonPath(outputDir, course string, moduleIndex int, module string, lessonIndex int, lesson string, sanitize Sanitizer) string {
	parts := []string{
		"KodeKloud",
		sanitize(course, 80),
		sanitize(fmt.Sprintf("%02d - %s", moduleIndex, module), 80),
		sanitize(fmt.Sprintf("%03d - %s", lessonIndex, lesson), 80),
	}
	join := func() string { return filepath.Join(append([]string{outputDir}, parts...)...) }
	full := join()
	overage := utf8.RuneCountInString(full) - MaxPathLength
	for i := len(parts) - 1; i >= 0 && overage > 0; i-- {
		cur := utf8.RuneCountInString(parts[i])
		if cur > 10 {
			shortenBy := min(overage, cur-10)
			parts[i] = ShortenComponent(parts[i], cur-shortenBy)
			full = join()
			overage = utf8.RuneCountInString(full) - MaxPathLength
		}
	}
	return full
}

// WithSuffix mirrors pathlib's with_suffix: it replaces an existing suffix
// ("003 - v1.2" -> "003 - v1.md"), exactly like the Python version did for notes.
func WithSuffix(path, suffix string) string {
	dir, name := filepath.Split(path)
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		name = name[:i]
	}
	return dir + name + suffix
}

// MovePath renames old to new; if both are directories they are merged.
// Nothing is ever overwritten: conflicting entries stay under old.
func MovePath(old, new string, log *slog.Logger) {
	if old == new {
		return
	}
	oldInfo, err := os.Stat(old)
	if err != nil {
		return
	}
	newInfo, err := os.Stat(new)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(new), 0o755); err == nil {
			if err := os.Rename(old, new); err == nil {
				log.Info("renamed", "from", filepath.Base(old), "to", filepath.Base(new))
			} else {
				log.Warn("could not rename", "from", old, "err", err)
			}
		}
		return
	}
	if err != nil || !oldInfo.IsDir() || !newInfo.IsDir() {
		return
	}
	entries, _ := os.ReadDir(old)
	for _, e := range entries {
		MovePath(filepath.Join(old, e.Name()), filepath.Join(new, e.Name()), log)
	}
	if err := os.Remove(old); err != nil {
		log.Warn("could not fully merge old folder; left behind", "old", old)
	}
}

// MigrateLegacyLesson moves an older download's course folder, module folder
// and "<old lesson name>.*" files (video, subtitles, notes) to the new names.
func MigrateLegacyLesson(old, new string, log *slog.Logger) {
	if old == new {
		return
	}
	moduleDir := filepath.Dir(new)
	courseDir := filepath.Dir(moduleDir)
	MovePath(filepath.Dir(filepath.Dir(old)), courseDir, log)
	MovePath(filepath.Join(courseDir, filepath.Base(filepath.Dir(old))), moduleDir, log)
	oldName, newName := filepath.Base(old), filepath.Base(new)
	if oldName == newName {
		return
	}
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), oldName+"."); ok {
			MovePath(filepath.Join(moduleDir, e.Name()), filepath.Join(moduleDir, newName+"."+rest), log)
		}
	}
}
