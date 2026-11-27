// Package naming turns course/module/lesson titles into file paths.
//
// The output must never change: re-runs find finished lessons by their exact
// path, so a different name means a re-download. testdata/ pins the paths of
// the whole catalog.
package naming

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxPathLength: Windows MAX_PATH is 260; leave room
// for the drive, separators and yt-dlp's file extension.
const MaxPathLength = 220

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

var multiSpace = regexp.MustCompile(` {2,}`)

// Sanitize turns a title into a safe path component of at most max runes,
// using readable stand-ins for forbidden characters.
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

func finish(s string, max int) string {
	s = strings.Trim(s, ". ")
	s = strings.TrimRight(truncate(s, max), ". ")
	if s == "" || reservedNames[strings.TrimRight(strings.ToLower(s), ".")] {
		s = "_"
	}
	return s
}

// truncate keeps the first n runes (code points, not bytes).
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// sliceEnd resolves a slice end index where negative counts from the end.
func sliceEnd(end, length int) int {
	if end < 0 {
		end += length
	}
	return max(0, min(end, length))
}

// ShortenComponent shortens a path component to about maxLen runes, keeping a
// short extension. Its quirks are deliberate: changing them renames files.
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
		stem := r[:sliceEnd(dot-(len(r)-maxLen+1), len(r))]
		return string(stem) + string(r[dot:])
	}
	return string(r[:sliceEnd(maxLen, len(r))])
}

// LessonPath is <out>/KodeKloud/<course>/<NN - module>/<NNN - lesson>
// (no extension), shortened to stay under MaxPathLength.
func LessonPath(outputDir, course string, moduleIndex int, module string, lessonIndex int, lesson string) string {
	parts := []string{
		"KodeKloud",
		Sanitize(course, 80),
		Sanitize(fmt.Sprintf("%02d - %s", moduleIndex, module), 80),
		Sanitize(fmt.Sprintf("%03d - %s", lessonIndex, lesson), 80),
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

// WithSuffix replaces an existing suffix ("003 - v1.2" -> "003 - v1.md");
// notes have always been named this way.
func WithSuffix(path, suffix string) string {
	dir, name := filepath.Split(path)
	if i := strings.LastIndex(name, "."); i > 0 && i < len(name)-1 {
		name = name[:i]
	}
	return dir + name + suffix
}
