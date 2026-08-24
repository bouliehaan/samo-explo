package downloader

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"explo/src/config"
)

func writeAged(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return p
}

func names(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

// The point of the whole exercise: a track that is still being recommended
// must survive rotation, so the next run finds it in place instead of pulling
// it off a stranger's machine a second time.
func TestRotationKeepsRecentDrops(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "fresh.flac", 2*24*time.Hour)
	writeAged(t, dir, "lastweek.flac", 8*24*time.Hour)
	writeAged(t, dir, "ancient.flac", 40*24*time.Hour)

	client := &DownloadClient{Cfg: &config.DownloadConfig{
		DownloadDir:   dir,
		RetentionDays: 21,
	}}
	client.DeleteSongs()

	got := names(t, dir)
	if !got["fresh.flac"] || !got["lastweek.flac"] {
		t.Fatalf("rotation deleted a recent drop, which forces a re-download: %v", got)
	}
	if got["ancient.flac"] {
		t.Fatalf("rotation kept a file past the retention window: %v", got)
	}
}

// Subdirectories are left alone — the same rule the original had, and the
// reason a nested folder once survived rotation indefinitely.
func TestRotationIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "album")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAged(t, dir, "ancient.flac", 40*24*time.Hour)

	client := &DownloadClient{Cfg: &config.DownloadConfig{DownloadDir: dir, RetentionDays: 21}}
	client.DeleteSongs()

	if got := names(t, dir); !got["album"] {
		t.Fatalf("rotation removed a directory: %v", got)
	}
}

// 0 restores the previous delete-everything behaviour for anyone who wants it.
func TestRotationZeroDaysDeletesEverything(t *testing.T) {
	dir := t.TempDir()
	writeAged(t, dir, "fresh.flac", time.Minute)
	writeAged(t, dir, "ancient.flac", 40*24*time.Hour)

	client := &DownloadClient{Cfg: &config.DownloadConfig{DownloadDir: dir, RetentionDays: 0}}
	client.DeleteSongs()

	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("retention 0 should clear the folder, left: %v", got)
	}
}
