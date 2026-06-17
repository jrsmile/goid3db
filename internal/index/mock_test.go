package index

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

// mockState programs exactly which database operations fail, keyed by a
// substring of the SQL. It drives a minimal database/sql driver so the
// otherwise-unreachable error branches (prepare/exec failures, mid-result scan
// errors) can be exercised deterministically.
type mockState struct {
	failBegin      bool
	prepareErrOn   string
	execErrOn      string
	queryBadRowOn  string // DB-level query returns a value that fails Scan
	stmtExecErrOn  string
	stmtBadRowOn   string // prepared-stmt query returns a value that fails Scan
	queryRowsErrOn string // DB-level query whose iteration errors
}

func mockIndex(st *mockState) *Index {
	return &Index{db: sql.OpenDB(&mockConnector{st: st})}
}

type mockConnector struct{ st *mockState }

func (c *mockConnector) Connect(context.Context) (driver.Conn, error) {
	return &mockConn{st: c.st}, nil
}
func (c *mockConnector) Driver() driver.Driver { return mockDriver{} }

type mockDriver struct{}

func (mockDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use OpenDB") }

type mockConn struct{ st *mockState }

func (c *mockConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}
func (c *mockConn) Close() error { return nil }
func (c *mockConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *mockConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.st.failBegin {
		return nil, errors.New("begin failed")
	}
	return mockTx{}, nil
}
func (c *mockConn) PrepareContext(_ context.Context, q string) (driver.Stmt, error) {
	if c.st.prepareErrOn != "" && strings.Contains(q, c.st.prepareErrOn) {
		return nil, errors.New("prepare failed")
	}
	return &mockStmt{st: c.st, q: q}, nil
}
func (c *mockConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.st.queryRowsErrOn != "" && strings.Contains(q, c.st.queryRowsErrOn) {
		return &mockRows{cols: []string{"x"}, err: errors.New("iteration failed")}, nil
	}
	if c.st.queryBadRowOn != "" && strings.Contains(q, c.st.queryBadRowOn) {
		return &mockRows{cols: []string{"id"}, data: [][]driver.Value{{"NaN"}}}, nil
	}
	// Delete's SELECT id needs a real row so it proceeds past ErrNoRows.
	if strings.Contains(q, "SELECT id FROM tracks") {
		return &mockRows{cols: []string{"id"}, data: [][]driver.Value{{int64(7)}}}, nil
	}
	return &mockRows{cols: []string{"x"}}, nil
}
func (c *mockConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if c.st.execErrOn != "" && strings.Contains(q, c.st.execErrOn) {
		return nil, errors.New("exec failed")
	}
	return mockResult{}, nil
}

type mockStmt struct {
	st *mockState
	q  string
}

func (s *mockStmt) Close() error  { return nil }
func (s *mockStmt) NumInput() int { return -1 }
func (s *mockStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), nil)
}
func (s *mockStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), nil)
}
func (s *mockStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	if s.st.stmtExecErrOn != "" && strings.Contains(s.q, s.st.stmtExecErrOn) {
		return nil, errors.New("stmt exec failed")
	}
	return mockResult{}, nil
}
func (s *mockStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	if s.st.stmtBadRowOn != "" && strings.Contains(s.q, s.st.stmtBadRowOn) {
		return &mockRows{cols: []string{"id"}, data: [][]driver.Value{{"NaN"}}}, nil
	}
	return &mockRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
}

type mockResult struct{}

func (mockResult) LastInsertId() (int64, error) { return 1, nil }
func (mockResult) RowsAffected() (int64, error) { return 1, nil }

type mockTx struct{}

func (mockTx) Commit() error   { return nil }
func (mockTx) Rollback() error { return nil }

type mockRows struct {
	cols []string
	data [][]driver.Value
	err  error
	pos  int
}

func (r *mockRows) Columns() []string { return r.cols }
func (r *mockRows) Close() error      { return nil }
func (r *mockRows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

const upsertFrag = "INSERT INTO tracks ("

func sampleTrack() *model.Track {
	tr := &model.Track{Path: "/m/a.mp3", ModTime: 1, Size: 1, Title: "t"}
	tr.BuildHaystack()
	return tr
}

func TestBeginBatchPrepareErrors(t *testing.T) {
	for _, frag := range []string{upsertFrag, "DELETE FROM tracks_fts", "INSERT INTO tracks_fts"} {
		ix := mockIndex(&mockState{prepareErrOn: frag})
		if _, err := ix.BeginBatch(context.Background()); err == nil {
			t.Errorf("expected prepare error for %q", frag)
		}
	}
}

func TestPutUpsertScanError(t *testing.T) {
	ix := mockIndex(&mockState{stmtBadRowOn: upsertFrag})
	w, err := ix.BeginBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Put(context.Background(), sampleTrack()); err == nil {
		t.Error("expected upsert scan error")
	}
}

func TestPutFtsDeleteError(t *testing.T) {
	ix := mockIndex(&mockState{stmtExecErrOn: "DELETE FROM tracks_fts"})
	w, _ := ix.BeginBatch(context.Background())
	if err := w.Put(context.Background(), sampleTrack()); err == nil {
		t.Error("expected fts delete error")
	}
}

func TestPutFtsInsertError(t *testing.T) {
	ix := mockIndex(&mockState{stmtExecErrOn: "INSERT INTO tracks_fts"})
	w, _ := ix.BeginBatch(context.Background())
	if err := w.Put(context.Background(), sampleTrack()); err == nil {
		t.Error("expected fts insert error")
	}
}

func TestUpsertPutError(t *testing.T) {
	ix := mockIndex(&mockState{stmtBadRowOn: upsertFrag})
	if err := ix.Upsert(context.Background(), sampleTrack()); err == nil {
		t.Error("expected Upsert to surface Put error")
	}
}

func TestDeleteScanError(t *testing.T) {
	ix := mockIndex(&mockState{queryBadRowOn: "SELECT id FROM tracks"})
	if err := ix.Delete(context.Background(), "/x.mp3"); err == nil {
		t.Error("expected delete scan error")
	}
}

func TestDeleteFtsExecError(t *testing.T) {
	ix := mockIndex(&mockState{execErrOn: "DELETE FROM tracks_fts"})
	if err := ix.Delete(context.Background(), "/x.mp3"); err == nil {
		t.Error("expected delete fts exec error")
	}
}

func TestDeleteTracksExecError(t *testing.T) {
	ix := mockIndex(&mockState{execErrOn: "DELETE FROM tracks WHERE id"})
	if err := ix.Delete(context.Background(), "/x.mp3"); err == nil {
		t.Error("expected delete tracks exec error")
	}
}

func TestSearchFTSScanError(t *testing.T) {
	ix := mockIndex(&mockState{queryBadRowOn: "FROM tracks_fts WHERE"})
	if _, err := ix.SearchFTS(context.Background(), "hello", 10); err == nil {
		t.Error("expected FTS scan error")
	}
}
