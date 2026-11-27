package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/johnay0/kodekloud-downloader/internal/api"
)

func TestPlayerURL(t *testing.T) {
	for raw, want := range map[string]string{
		// shapes seen in a real --all-paid run
		"https://vimeo.com/1030662913":                       vimeoPlayer + "1030662913",
		"https://vimeo.com/1093000001/4003d34149?share=copy": vimeoPlayer + "1093000001?h=4003d34149",
		"https://vimeo.com/1093000001/5fb01fd530":            vimeoPlayer + "1093000001?h=5fb01fd530",
		"https://vimeo.com/1093000001/10":                    vimeoPlayer + "1093000001",
		// other Vimeo shapes
		"https://player.vimeo.com/video/1246993242":                 vimeoPlayer + "1246993242",
		"https://player.vimeo.com/video/123456789?h=abc123def0&x=1": vimeoPlayer + "123456789?h=abc123def0",
		"https://vimeo.com/showcase/11111111/video/22222222":        vimeoPlayer + "22222222",
		"https://vimeo.com/manage/videos/123456789/abcdef1234":      vimeoPlayer + "123456789?h=abcdef1234",
		"https://vimeo.com/channels/staffpicks/123456789":           vimeoPlayer + "123456789",
		"https://www.vimeo.com/123456789/":                          vimeoPlayer + "123456789",
		" 123456789 ":                                               vimeoPlayer + "123456789",
		// the same link stored twice (EC2 course, lesson 37)
		"https://vimeo.com/906338252?share=copy\n\n\n\nhttps://vimeo.com/906338252?share=copy": vimeoPlayer + "906338252",
		"see below\nhttps://vimeo.com/906338252":                                               vimeoPlayer + "906338252",
		// not Vimeo, or no id: left for yt-dlp
		"https://www.youtube.com/watch?v=abc": "https://www.youtube.com/watch?v=abc",
		"https://vimeo.com/user/abc":          "https://vimeo.com/user/abc",
		"https://notvimeo.com/123456789":      "https://notvimeo.com/123456789",
	} {
		if got := PlayerURL(raw); got != want {
			t.Errorf("PlayerURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRemoveIntermediates(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "019 - Intro [v2]")
	keep := []string{
		"019 - Intro [v2].mkv",
		"019 - Intro [v2].en-US.vtt",
		"019 - Intro [v2].fr.vtt",
		"019 - Intro [v2] extra.fhls-1.mp4", // another name
		"020 - Next.fhls-1.mp4",
	}
	drop := []string{
		"019 - Intro [v2].fhls-fastly_skyfire-406.mp4",
		"019 - Intro [v2].fhls-fastly_skyfire-audio-high-English.mp4",
		"019 - Intro [v2].fdash-video-1.mp4.part",
		"019 - Intro [v2].f137.webm.part.ytdl",
	}
	for _, n := range append(append([]string{}, keep...), drop...) {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if n := removeIntermediates(base); n != len(drop) {
		t.Errorf("removed %d, want %d", n, len(drop))
	}
	ents, _ := os.ReadDir(dir)
	var left []string
	for _, e := range ents {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	sort.Strings(keep)
	if len(left) != len(keep) {
		t.Fatalf("left %q, want %q", left, keep)
	}
	for i := range keep {
		if left[i] != keep[i] {
			t.Fatalf("left %q, want %q", left, keep)
		}
	}
}

func TestBrokenLeftoverPiecesAreRemovedAndRetried(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{"c": true}, details: map[string]api.CourseDetail{"c": course("c", "C", videos("v", 1)...)}}
	srv := kk.serve(t)
	dl, out := newDL(t, srv, &fakeYtDlp{}, 1, staticAuth{})
	var calls atomic.Int32
	dl.RunVideo = func(_ context.Context, url, base string) ([]byte, error) {
		calls.Add(1)
		piece := base + ".fhls-1.mp4"
		if _, err := os.Stat(piece); err == nil {
			return []byte("ERROR: Postprocessing: Error opening input files: Invalid data found when processing input"), errors.New("exit status 1")
		}
		return nil, os.WriteFile(base+".mkv", []byte(url), 0o644)
	}
	dir := filepath.Join(out, "KodeKloud", "C", "01 - Intro - Basics")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "001 - Video A.fhls-1.mp4"), []byte("corrupt"), 0o644)

	res, err := dl.Run(context.Background(), []api.Course{kk.details["c"].Course})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || res[0].Downloaded.Load() != 1 || res[0].Failed.Load() != 0 {
		t.Errorf("calls=%d downloaded=%d failed=%d", calls.Load(), res[0].Downloaded.Load(), res[0].Failed.Load())
	}
}

func TestMergeFailureWithoutLeftoversIsNotRetried(t *testing.T) {
	kk := &fakeKK{enrolled: map[string]bool{"c": true}, details: map[string]api.CourseDetail{"c": course("c", "C", videos("v", 1)...)}}
	srv := kk.serve(t)
	dl, _ := newDL(t, srv, &fakeYtDlp{}, 1, staticAuth{})
	var calls atomic.Int32
	dl.RunVideo = func(context.Context, string, string) ([]byte, error) {
		calls.Add(1)
		return []byte("ERROR: Postprocessing: boom"), errors.New("exit status 1")
	}
	res, _ := dl.Run(context.Background(), []api.Course{kk.details["c"].Course})
	if calls.Load() != 1 || res[0].Failed.Load() != 1 {
		t.Errorf("calls=%d failed=%d", calls.Load(), res[0].Failed.Load())
	}
}
