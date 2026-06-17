// Package index provides the permanent, on-disk SQLite index of tracks.
//
// It is designed for the 1M-track scale: batched transactional inserts for the
// initial walk, single-row upserts for "instant" incremental updates, and an
// FTS5 trigram table for substring/field queries that complement the in-memory
// fuzzy matcher.
package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jrsmile/goid3db/internal/model"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// Index wraps the SQLite database handle.
type Index struct {
	db *sql.DB
}

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA temp_store = MEMORY;

CREATE TABLE IF NOT EXISTS tracks (
    id        INTEGER PRIMARY KEY,
    path      TEXT UNIQUE NOT NULL,
    mod_time  INTEGER NOT NULL,
    size      INTEGER NOT NULL,
    title     TEXT,
    album     TEXT,
    artist    TEXT,
    genre     TEXT,
    year      INTEGER,
    folder    TEXT,
    haystack  TEXT
);
CREATE INDEX IF NOT EXISTS idx_tracks_year   ON tracks(year);
CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist);
CREATE INDEX IF NOT EXISTS idx_tracks_album  ON tracks(album);
CREATE INDEX IF NOT EXISTS idx_tracks_genre  ON tracks(genre);

CREATE VIRTUAL TABLE IF NOT EXISTS tracks_fts USING fts5(
    title, album, artist, genre, folder,
    tokenize='trigram'
);
`

// sqlOpen is indirected so tests can force the (otherwise unreachable) driver
// registration error path.
var sqlOpen = sql.Open

// Open opens (creating if needed) the SQLite index at path. Use ":memory:" for
// tests. A single shared connection is used because SQLite writes are
// serialized anyway; this keeps WAL semantics simple.
func Open(path string) (*Index, error) {
	db, err := sqlOpen("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is single-writer; a small pool avoids "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Index{db: db}, nil
}

// Close closes the underlying database.
func (ix *Index) Close() error { return ix.db.Close() }

// Stat is a lightweight record used to decide whether a file needs reparsing.
type Stat struct {
	ModTime int64
	Size    int64
}

// PathStats returns mod_time/size for every indexed path, so the scanner can
// skip unchanged files on a rescan.
func (ix *Index) PathStats(ctx context.Context) (map[string]Stat, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT path, mod_time, size FROM tracks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]Stat, 1<<16)
	for rows.Next() {
		var p string
		var s Stat
		if err := rows.Scan(&p, &s.ModTime, &s.Size); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}

// Writer batches upserts inside a single transaction. Call Commit when done.
type Writer struct {
	tx        *sql.Tx
	upsert    *sql.Stmt
	ftsDelete *sql.Stmt
	ftsInsert *sql.Stmt
}

// BeginBatch starts a batched write transaction for the initial walk.
func (ix *Index) BeginBatch(ctx context.Context) (*Writer, error) {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	w := &Writer{tx: tx}
	if w.upsert, err = tx.PrepareContext(ctx, upsertSQL); err != nil {
		tx.Rollback()
		return nil, err
	}
	if w.ftsDelete, err = tx.PrepareContext(ctx, `DELETE FROM tracks_fts WHERE rowid = ?`); err != nil {
		tx.Rollback()
		return nil, err
	}
	if w.ftsInsert, err = tx.PrepareContext(ctx,
		`INSERT INTO tracks_fts(rowid, title, album, artist, genre, folder) VALUES (?,?,?,?,?,?)`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return w, nil
}

const upsertSQL = `
INSERT INTO tracks (path, mod_time, size, title, album, artist, genre, year, folder, haystack)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(path) DO UPDATE SET
    mod_time=excluded.mod_time, size=excluded.size, title=excluded.title,
    album=excluded.album, artist=excluded.artist, genre=excluded.genre,
    year=excluded.year, folder=excluded.folder, haystack=excluded.haystack
RETURNING id`

// Put inserts or updates a single track and keeps the FTS row in sync. The
// track's ID is populated on return.
func (w *Writer) Put(ctx context.Context, t *model.Track) error {
	var id int64
	err := w.upsert.QueryRowContext(ctx,
		t.Path, t.ModTime, t.Size, t.Title, t.Album, t.Artist, t.Genre, t.Year, t.Folder, t.Haystack,
	).Scan(&id)
	if err != nil {
		return fmt.Errorf("upsert %s: %w", t.Path, err)
	}
	t.ID = id
	// Contentless FTS5 cannot be updated in place; delete-then-insert by rowid.
	if _, err := w.ftsDelete.ExecContext(ctx, id); err != nil {
		return fmt.Errorf("fts delete: %w", err)
	}
	if _, err := w.ftsInsert.ExecContext(ctx, id, t.Title, t.Album, t.Artist, t.Genre, t.Folder); err != nil {
		return fmt.Errorf("fts insert: %w", err)
	}
	return nil
}

// Commit commits the batch.
func (w *Writer) Commit() error { return w.tx.Commit() }

// Rollback aborts the batch.
func (w *Writer) Rollback() error { return w.tx.Rollback() }

// Upsert is a convenience for a single instant write (used by the watcher).
func (ix *Index) Upsert(ctx context.Context, t *model.Track) error {
	w, err := ix.BeginBatch(ctx)
	if err != nil {
		return err
	}
	if err := w.Put(ctx, t); err != nil {
		w.Rollback()
		return err
	}
	return w.Commit()
}

// Delete removes a track (and its FTS row) by path.
func (ix *Index) Delete(ctx context.Context, path string) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM tracks WHERE path=?`, path).Scan(&id)
	if err == sql.ErrNoRows {
		tx.Rollback()
		return nil
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracks_fts WHERE rowid = ?`, id); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracks WHERE id=?`, id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Count returns the number of indexed tracks.
func (ix *Index) Count(ctx context.Context) (int64, error) {
	var n int64
	err := ix.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks`).Scan(&n)
	return n, err
}

// LoadAll streams every track into memory for the fuzzy matcher. For 1M rows
// this is a few hundred MB; callers can pre-size their slice with Count.
func (ix *Index) LoadAll(ctx context.Context) ([]model.Track, error) {
	n, _ := ix.Count(ctx)
	rows, err := ix.db.QueryContext(ctx,
		`SELECT id, path, mod_time, size, title, album, artist, genre, year, folder, haystack FROM tracks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]model.Track, 0, n)
	for rows.Next() {
		var t model.Track
		if err := rows.Scan(&t.ID, &t.Path, &t.ModTime, &t.Size, &t.Title,
			&t.Album, &t.Artist, &t.Genre, &t.Year, &t.Folder, &t.Haystack); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SearchFTS runs a trigram FTS5 query for field-scoped/substring matching and
// returns matching track IDs ordered by rank. Empty query returns nil.
func (ix *Index) SearchFTS(ctx context.Context, query string, limit int) ([]int64, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	rows, err := ix.db.QueryContext(ctx,
		`SELECT rowid FROM tracks_fts WHERE tracks_fts MATCH ? ORDER BY rank LIMIT ?`,
		query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
