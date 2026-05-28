package fts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// installSyncTriggers creates the AFTER INSERT / UPDATE / DELETE
// triggers on contentTable that keep ftsName in sync. The implementation
// follows FTS5's documented external-content pattern
// (sqlite.org/fts5.html §4.4.3): inserts and updates push the row's
// columns into the FTS5 table; updates and deletes emit the
// 'delete' magic-row insert first.
//
// Trigger names are deterministic: "<contentTable>_<ftsName>_<ai|au|ad>".
// IF NOT EXISTS makes the install idempotent — re-running New (with
// WithIfNotExists) over an already-migrated database is a no-op.
//
// Caller is expected to have validated identifiers via [ValidIdent]
// already — this helper does no escaping. cols and rowid must be
// non-empty.
func installSyncTriggers(ctx context.Context, db *sql.DB, ftsName, contentTable, rowid string, cols []string, mode SyncMode) error {
	if mode == 0 {
		return nil
	}
	if contentTable == "" {
		return fmt.Errorf("fts: installSyncTriggers: contentTable is required")
	}
	if rowid == "" {
		rowid = "rowid"
	}
	if len(cols) == 0 {
		return fmt.Errorf("fts: installSyncTriggers: cols is empty")
	}

	colList := strings.Join(quoteAll(cols), ", ")
	newVals := strings.Join(prefixed("NEW.", cols), ", ")
	oldVals := strings.Join(prefixed("OLD.", cols), ", ")

	insertBody := fmt.Sprintf(
		"INSERT INTO %s(rowid, %s) VALUES (NEW.%s, %s);",
		quote(ftsName), colList, quote(rowid), newVals,
	)
	deleteMagic := fmt.Sprintf(
		"INSERT INTO %s(%s, rowid, %s) VALUES('delete', OLD.%s, %s);",
		quote(ftsName), quote(ftsName), colList, quote(rowid), oldVals,
	)
	// Update is delete-then-insert per the FTS5 manual.
	updateBody := deleteMagic + insertBody

	triggers := []struct {
		bit  SyncMode
		name string
		when string
		body string
	}{
		{SyncInsert, contentTable + "_" + ftsName + "_ai", "AFTER INSERT", insertBody},
		{SyncUpdate, contentTable + "_" + ftsName + "_au", "AFTER UPDATE", updateBody},
		{SyncDelete, contentTable + "_" + ftsName + "_ad", "AFTER DELETE", deleteMagic},
	}
	for _, t := range triggers {
		if mode&t.bit == 0 {
			continue
		}
		stmt := fmt.Sprintf(
			"CREATE TRIGGER IF NOT EXISTS %s %s ON %s BEGIN %s END",
			quote(t.name), t.when, quote(contentTable), t.body,
		)
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("fts: create trigger %s: %w", t.name, err)
		}
	}
	return nil
}

// quoteAll returns a slice of backtick-quoted copies of cols.
func quoteAll(cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quote(c)
	}
	return out
}

// prefixed prefixes every col with prefix (e.g. "NEW." → "NEW.`col`").
func prefixed(prefix string, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = prefix + quote(c)
	}
	return out
}
