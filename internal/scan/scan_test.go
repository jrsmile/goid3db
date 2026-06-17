package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanIndexesAudioByFilename(t *testing.T) {
	dir := t.TempDir()
	// Create files: two audio (untagged -> indexed by filename), one non-audio.
	must := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a real audio body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("Song One.mp3")
	must("Another Track.flac")
	must("readme.txt")

	got := map[string]bool{}
	for r := range Scan(context.Background(), Options{Root: dir, Workers: 2}) {
		if r.Err != nil {
			t.Fatalf("unexpected error for %s: %v", r.Path, r.Err)
		}
		if r.Track == nil {
			continue
		}
		got[r.Track.Title] = true
		if r.Track.Haystack == "" {
			t.Errorf("expected non-empty haystack for %s", r.Track.Path)
		}
	}

	if !got["Song One"] || !got["Another Track"] {
		t.Errorf("expected both audio files indexed by filename, got %v", got)
	}
	if got["readme"] {
		t.Error("non-audio file should not be indexed")
	}
}

func TestScanSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.mp3"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Skip everything -> no results.
	n := 0
	for range Scan(context.Background(), Options{
		Root: dir, Workers: 1,
		Skip: func(string, int64, int64) bool { return true },
	}) {
		n++
	}
	if n != 0 {
		t.Errorf("expected all files skipped, got %d results", n)
	}
}
