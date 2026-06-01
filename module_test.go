package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// numbersTable yields N rows with a single integer column "value" running 1..N.
// Demonstrates the minimum [VTab] / [VTabCursor] surface a read-only module
// needs.
type numbersTable struct {
	n int
}

func (numbersTable) BestIndex(info *IndexInfo) error {
	info.EstimatedCost = 1
	info.EstimatedRows = 100
	return nil
}

func (t numbersTable) Open() (VTabCursor, error) { return &numbersCursor{n: t.n}, nil }
func (numbersTable) Disconnect() error           { return nil }
func (numbersTable) Destroy() error              { return nil }

type numbersCursor struct {
	n   int
	row int // 1-based; 0 means before-start.
}

func (c *numbersCursor) Filter(int, string, []Value) error {
	c.row = 1
	return nil
}

func (c *numbersCursor) Next() error { c.row++; return nil }
func (c *numbersCursor) Eof() bool   { return c.row > c.n }
func (c *numbersCursor) Column(int) (Value, error) {
	return int64(c.row), nil
}
func (c *numbersCursor) Rowid() (int64, error) { return int64(c.row), nil }
func (c *numbersCursor) Close() error          { return nil }

// numbersCtor parses N=<int> from args and constructs a read-only numbersTable.
func numbersCtor(c *Conn, _, _, _ string, args []string) (VTab, error) {
	n := 10
	for _, a := range args {
		if rest, ok := strings.CutPrefix(a, "N="); ok {
			v, err := strconv.Atoi(rest)
			if err != nil {
				return nil, fmt.Errorf("numbers: bad N: %w", err)
			}
			n = v
		}
	}
	if err := c.DeclareVTab(`CREATE TABLE x(value INTEGER)`); err != nil {
		return nil, err
	}
	return numbersTable{n: n}, nil
}

func TestCreateModule_ReadOnlyScan(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.CreateModule("numbers", numbersCtor); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE n USING numbers(N=7)`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}
	rows, err := sc.QueryContext(ctx, `SELECT value FROM n ORDER BY value`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != 7 {
		t.Fatalf("got %d rows, want 7: %v", len(got), got)
	}
	for i, v := range got {
		if v != int64(i+1) {
			t.Errorf("row[%d]=%d, want %d", i, v, i+1)
		}
	}
}

func TestCreateModule_ReadOnlyRejectsWrite(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.CreateModule("numbers", numbersCtor); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE n USING numbers(N=3)`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}
	// numbersTable does not implement VTabUpdater → SQLite reports
	// SQLITE_READONLY on insert.
	if _, err := sc.ExecContext(ctx, `INSERT INTO n(value) VALUES (99)`); err == nil {
		t.Fatal("INSERT against read-only module succeeded; expected error")
	}
}

// kvTable is a tiny in-memory key-value store backed by a sync.Map, exposed as
// a vtab with INSERT / UPDATE / DELETE through [VTabUpdater].
type kvTable struct {
	mu   sync.Mutex
	rows map[int64]string // rowid → value
	next int64
}

func (kv *kvTable) BestIndex(info *IndexInfo) error {
	info.EstimatedCost = 100
	info.EstimatedRows = int64(len(kv.rows))
	return nil
}

func (kv *kvTable) Open() (VTabCursor, error) {
	kv.mu.Lock()
	rids := make([]int64, 0, len(kv.rows))
	for r := range kv.rows {
		rids = append(rids, r)
	}
	kv.mu.Unlock()
	return &kvCursor{table: kv, rids: rids}, nil
}

func (*kvTable) Disconnect() error { return nil }
func (*kvTable) Destroy() error    { return nil }

func (kv *kvTable) Insert(cols []Value, rowid *int64) error {
	if len(cols) != 1 {
		return fmt.Errorf("kv: expected 1 column, got %d", len(cols))
	}
	v, _ := cols[0].(string)
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if *rowid == 0 {
		kv.next++
		*rowid = kv.next
	} else if *rowid > kv.next {
		kv.next = *rowid
	}
	kv.rows[*rowid] = v
	return nil
}

func (kv *kvTable) Update(oldRowid int64, cols []Value, newRowid *int64) error {
	v, _ := cols[0].(string)
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if _, ok := kv.rows[oldRowid]; !ok {
		return fmt.Errorf("kv: rowid %d not found", oldRowid)
	}
	target := oldRowid
	if newRowid != nil && *newRowid != 0 && *newRowid != oldRowid {
		delete(kv.rows, oldRowid)
		target = *newRowid
	}
	kv.rows[target] = v
	return nil
}

func (kv *kvTable) Delete(oldRowid int64) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	delete(kv.rows, oldRowid)
	return nil
}

type kvCursor struct {
	table *kvTable
	rids  []int64
	pos   int
}

func (c *kvCursor) Filter(int, string, []Value) error { c.pos = 0; return nil }
func (c *kvCursor) Next() error                       { c.pos++; return nil }
func (c *kvCursor) Eof() bool                         { return c.pos >= len(c.rids) }
func (c *kvCursor) Rowid() (int64, error)             { return c.rids[c.pos], nil }
func (c *kvCursor) Close() error                      { return nil }

func (c *kvCursor) Column(col int) (Value, error) {
	c.table.mu.Lock()
	defer c.table.mu.Unlock()
	return c.table.rows[c.rids[c.pos]], nil
}

func kvCtor(c *Conn, _, _, _ string, _ []string) (VTab, error) {
	if err := c.DeclareVTab(`CREATE TABLE x(value TEXT)`); err != nil {
		return nil, err
	}
	return &kvTable{rows: make(map[int64]string)}, nil
}

func TestCreateModule_UpdaterRoundTrip(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	if err := c.CreateModule("kv", kvCtor); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE kv USING kv()`); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO kv(value) VALUES ('alpha')`); err != nil {
		t.Fatalf("INSERT alpha: %v", err)
	}
	if _, err := sc.ExecContext(ctx, `INSERT INTO kv(value) VALUES ('beta')`); err != nil {
		t.Fatalf("INSERT beta: %v", err)
	}

	// UPDATE.
	if _, err := sc.ExecContext(ctx, `UPDATE kv SET value = 'ALPHA' WHERE rowid = 1`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	// DELETE.
	if _, err := sc.ExecContext(ctx, `DELETE FROM kv WHERE rowid = 2`); err != nil {
		t.Fatalf("DELETE: %v", err)
	}

	rows, err := sc.QueryContext(ctx, `SELECT rowid, value FROM kv ORDER BY rowid`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()
	type pair struct {
		Rowid int64
		Value string
	}
	var got []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.Rowid, &p.Value); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, p)
	}
	if len(got) != 1 || got[0].Rowid != 1 || got[0].Value != "ALPHA" {
		t.Errorf("got %+v, want [{1 ALPHA}]", got)
	}
}

func TestCreateModule_Validation(t *testing.T) {
	_, _, c := withMattnConn(t, ":memory:")
	if err := c.CreateModule("", numbersCtor); err == nil {
		t.Error("CreateModule with empty name: expected error, got nil")
	}
	if err := c.CreateModule("x", nil); err == nil {
		t.Error("CreateModule with nil ctor: expected error, got nil")
	}
}

func TestCreateModule_CtorError(t *testing.T) {
	_, sc, c := withMattnConn(t, ":memory:")
	failCtor := func(*Conn, string, string, string, []string) (VTab, error) {
		return nil, fmt.Errorf("constructor refused to build")
	}
	if err := c.CreateModule("fail", failCtor); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	ctx := context.Background()
	_, err := sc.ExecContext(ctx, `CREATE VIRTUAL TABLE f USING fail()`)
	if err == nil {
		t.Fatal("CREATE VIRTUAL TABLE: expected error from ctor, got nil")
	}
	if !strings.Contains(err.Error(), "constructor refused") {
		t.Errorf("error %q does not surface ctor message", err.Error())
	}
}

func TestDeclareVTab_OutsideCtorFails(t *testing.T) {
	// sqlite3_declare_vtab returns SQLITE_MISUSE when not called from inside
	// xCreate / xConnect. The exact wording varies by SQLite version; we only
	// pin that it errors.
	_, _, c := withMattnConn(t, ":memory:")
	if err := c.DeclareVTab(`CREATE TABLE x(value)`); err == nil {
		t.Error("DeclareVTab outside ctor: expected error, got nil")
	}
}
