package download

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const vimeoPlayer = "https://player.vimeo.com/video/"

var (
	vimeoHash = regexp.MustCompile(`^[0-9a-f]{6,20}$`)
	// yt-dlp's per-format pieces of "<base>.mkv": "<base>.f<format>.<ext>[.part]".
	intermediate = regexp.MustCompile(`^\.f[^.]+\.(?:mp4|m4a|m4v|webm|mkv|ts|mp3|aac|opus)(?:\.part)?(?:\.ytdl)?$`)
)

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// PlayerURL turns the video_url KodeKloud returns into the player URL yt-dlp
// needs. KodeKloud stores several shapes:
//
//	https://vimeo.com/123456789
//	https://vimeo.com/123456789/4003d34149?share=copy   (unlisted: id + hash)
//	https://player.vimeo.com/video/123456789?h=4003d34149
//	123456789
//
// The video id is the numeric segment (the web app extracts it the same way);
// a privacy hash is kept as ?h= because unlisted videos need it. Anything
// that is not a Vimeo URL is returned unchanged for yt-dlp to handle.
func PlayerURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if allDigits(raw) {
		return vimeoPlayer + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if host != "vimeo.com" && !strings.HasSuffix(host, ".vimeo.com") {
		return raw
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	idx := -1
	// 1. ".../video(s)/<id>" (player URLs, showcases, groups)
	for i := 1; i < len(segs); i++ {
		if (segs[i-1] == "video" || segs[i-1] == "videos") && allDigits(segs[i]) {
			idx = i
			break
		}
	}
	// 2. first real-looking id; 3. any number (what the web app falls back to)
	for _, minLen := range []int{6, 1} {
		for i := 0; idx < 0 && i < len(segs); i++ {
			if allDigits(segs[i]) && len(segs[i]) >= minLen {
				idx = i
			}
		}
	}
	if idx < 0 {
		return raw
	}
	h := u.Query().Get("h")
	if h == "" && idx+1 < len(segs) && vimeoHash.MatchString(segs[idx+1]) {
		h = segs[idx+1]
	}
	out := vimeoPlayer + segs[idx]
	if h != "" {
		out += "?h=" + url.QueryEscape(h)
	}
	return out
}

// removeIntermediates deletes yt-dlp's leftover per-format files for base
// (e.g. "<base>.fhls-406.mp4"). A broken one from an earlier run makes every
// retry fail at the merge step, because yt-dlp reuses it as "already
// downloaded". Returns how many files were removed.
func removeIntermediates(base string) int {
	dir, name := filepath.Split(base)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), name)
		if !ok || e.IsDir() || !intermediate.MatchString(rest) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// mergeFailed reports a yt-dlp run that downloaded the pieces but could not
// merge them (typically a corrupt leftover piece).
func mergeFailed(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "ERROR: Postprocessing") || strings.Contains(s, "Error opening input")
}
