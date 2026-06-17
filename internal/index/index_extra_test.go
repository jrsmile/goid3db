package index

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func mkIndex(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestOpenSchemaError(t *testing.T) {
	// A path inside a non-existent directory fails when the schema executes.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "x.sqlite")
	if _, err := Open(bad); err == nil {
		t.Error("expected schema error for unwritable path")
	}
}

func TestOpenDriverError(t *testing.T) {
	orig := sqlOpen
	sqlOpen = func(string, string) (*sql.DB, error) { return nil, errors.New("boom") }
	defer func() { sqlOpen = orig }()
	if _, err := Open(":memory:"); err == nil {
		t.Error("expected driver open error")
	}
}

func TestRollback(t *testing.T) {
	ctx := context.Background()
	ix := mkIndex(t)
	w, err := ix.BeginBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr := &model.Track{Path: "/a.mp3", ModTime: 1, Size: 1, Title: "x"}
	tr.BuildHaystack()
	if err := w.Put(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := w.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n, _ := ix.Count(ctx); n != 0 {
		t.Errorf("rollback should discard writes, got %d rows", n)
	}
}

func TestDeleteMissingIsNoop(t *testing.T) {
	if err := mkIndex(t).Delete(context.Background(), "/does/not/exist.mp3"); err != nil {
		t.Errorf("deleting missing path should be a no-op, got %v", err)
	}
}

func TestSearchFTSEmpty(t *testing.T) {
	ids, err := mkIndex(t).SearchFTS(context.Background(), "   ", 10)
	if err != nil || ids != nil {
		t.Errorf("empty query should return nil,nil; got %v,%v", ids, err)
	}
}

func TestSearchFTSMalformed(t *testing.T) {
	// An unbalanced quote is a MATCH syntax error -> QueryContext fails.
	if _, err := mkIndex(t).SearchFTS(context.Background(), `"`, 10); err == nil {
		t.Error("expected malformed FTS query to error")
	}
}

func TestClosedDBErrors(t *testing.T) {
	ctx := context.Background()
	ix := mkIndex(t)
	ix.Close()

	if _, err := ix.PathStats(ctx); err == nil {
		t.Error("PathStats on closed db should error")
	}
	if _, err := ix.LoadAll(ctx); err == nil {
		t.Error("LoadAll on closed db should error")
	}
	if _, err := ix.Count(ctx); err == nil {
		t.Error("Count on closed db should error")
	}
	if _, err := ix.SearchFTS(ctx, "hello", 10); err == nil {
		t.Error("SearchFTS on closed db should error")
	}
	if _, err := ix.BeginBatch(ctx); err == nil {
		t.Error("BeginBatch on closed db should error")
	}
	tr := &model.Track{Path: "/a.mp3"}
	tr.BuildHaystack()
	if err := ix.Upsert(ctx, tr); err == nil {
		t.Error("Upsert on closed db should error")
	}
	if err := ix.Delete(ctx, "/a.mp3"); err == nil {
		t.Error("Delete on closed db should error")
	}
}

func TestPathStatsScanError(t *testing.T) {
	ctx := context.Background()
	ix := mkIndex(t)
	// Force a non-numeric value into an INTEGER column via SQLite type affinity,
	// so scanning mod_time into int64 fails.
	if _, err := ix.db.ExecContext(ctx,
		`INSERT INTO tracks(path, mod_time, size) VALUES('x', 'not-a-number', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.PathStats(ctx); err == nil {
		t.Error("expected scan error from non-numeric mod_time")
	}
}

func TestLoadAllScanError(t *testing.T) {
	ctx := context.Background()
	ix := mkIndex(t)
	if _, err := ix.db.ExecContext(ctx,
		`INSERT INTO tracks(path, mod_time, size, year) VALUES('x', 1, 1, 'NaN')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.LoadAll(ctx); err == nil {
		t.Error("expected scan error from non-numeric year")
	}
}
