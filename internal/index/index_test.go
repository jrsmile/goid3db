package index

import (
	"context"
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func TestIndexUpsertAndLoad(t *testing.T) {
	ctx := context.Background()
	ix, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()

	tr := &model.Track{
		Path: "/music/song.mp3", ModTime: 100, Size: 2048,
		Title: "Song", Album: "Album", Artist: "Artist", Genre: "Rock", Year: 1999,
		Folder: "music",
	}
	tr.BuildHaystack()
	if err := ix.Upsert(ctx, tr); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if tr.ID == 0 {
		t.Error("expected ID to be set after upsert")
	}

	n, err := ix.Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}

	// Upsert same path again with new metadata -> still one row.
	tr.Title = "Song v2"
	tr.ModTime = 200
	tr.BuildHaystack()
	if err := ix.Upsert(ctx, tr); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if n, _ := ix.Count(ctx); n != 1 {
		t.Fatalf("expected 1 row after re-upsert, got %d", n)
	}

	all, err := ix.LoadAll(ctx)
	if err != nil {
		t.Fatalf("loadall: %v", err)
	}
	if len(all) != 1 || all[0].Title != "Song v2" {
		t.Errorf("unexpected load result: %+v", all)
	}

	// PathStats should reflect updated mod time.
	stats, err := ix.PathStats(ctx)
	if err != nil {
		t.Fatalf("pathstats: %v", err)
	}
	if s := stats["/music/song.mp3"]; s.ModTime != 200 || s.Size != 2048 {
		t.Errorf("unexpected stats: %+v", s)
	}
}

func TestIndexFTSAndDelete(t *testing.T) {
	ctx := context.Background()
	ix, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer ix.Close()

	tr := &model.Track{Path: "/m/a.mp3", ModTime: 1, Size: 1, Title: "Hello World", Artist: "Tester"}
	tr.BuildHaystack()
	if err := ix.Upsert(ctx, tr); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	ids, err := ix.SearchFTS(ctx, "hello", 10)
	if err != nil {
		t.Fatalf("fts: %v", err)
	}
	if len(ids) != 1 || ids[0] != tr.ID {
		t.Errorf("expected FTS to find track, got %v", ids)
	}

	if err := ix.Delete(ctx, "/m/a.mp3"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n, _ := ix.Count(ctx); n != 0 {
		t.Errorf("expected 0 after delete, got %d", n)
	}
	ids, _ = ix.SearchFTS(ctx, "hello", 10)
	if len(ids) != 0 {
		t.Errorf("expected FTS row removed, got %v", ids)
	}
}
