// Package vtabx holds the small create/drop machinery shared by the typed
// virtual-table handles in the ext/ sub-packages (bloom.Filter, closure.Graph,
// csv.Table, lines.Table, rtree.Table, spellfix1.Vocab). Each of those handles
// otherwise re-implemented the same skeleton: validate the name, assemble
// `CREATE VIRTUAL TABLE [IF NOT EXISTS] name USING module(params…)`, run it,
// and map an "already exists" error to a package sentinel. This package owns
// that skeleton; callers keep only their option-to-params builder and their
// ErrAlreadyExists value.
package vtabx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
)

// Create runs CREATE VIRTUAL TABLE [IF NOT EXISTS] name USING module(params…)
// on db. params are the already-formatted argument strings (each value the
// caller wishes to embed must already be quoted via [sqlid.QuoteIdent] /
// [sqlid.QuoteString]). name is validated as an identifier; an "already
// exists" failure is wrapped with alreadyExists so callers can test it with
// errors.Is.
func Create(ctx context.Context, db *sql.DB, name, module string, params []string, ifNotExists bool, alreadyExists error) error {
	if !sqlid.ValidIdent(name) {
		return fmt.Errorf("%s.Create: %q is not a valid SQL identifier", module, name)
	}
	ifne := ""
	if ifNotExists {
		ifne = "IF NOT EXISTS "
	}
	// Omit the parens when there are no arguments — some modules (e.g.
	// spellfix1) take the bare `USING module` form.
	using := module
	if len(params) > 0 {
		using = fmt.Sprintf("%s(%s)", module, strings.Join(params, ", "))
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING %s", ifne, sqlid.QuoteIdent(name), using)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if sqlid.IsAlreadyExistsErr(err) {
			return fmt.Errorf("%s.Create %q: %w", module, name, alreadyExists)
		}
		return fmt.Errorf("%s.Create %q: %w", module, name, err)
	}
	return nil
}

// Drop runs DROP TABLE IF EXISTS name on db. SQLite cascades the vtab's shadow
// tables; any backing file is untouched.
func Drop(ctx context.Context, db *sql.DB, name string) error {
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+sqlid.QuoteIdent(name)); err != nil {
		return fmt.Errorf("drop %q: %w", name, err)
	}
	return nil
}
