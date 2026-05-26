package ftsgorm

import (
	"fmt"
	"strings"

	"github.com/go-again/sqlite/fts"
	"gorm.io/gorm"
)

// Migrate creates the source tables (via gorm's AutoMigrate), the
// shared FTS5 external-content table for each model, and the AFTER
// INSERT/UPDATE/DELETE triggers that keep the index in sync with the
// source.
//
// Idempotent: existing tables and triggers are left alone (CREATE IF
// NOT EXISTS / CREATE TRIGGER IF NOT EXISTS). If the FTS5 schema
// declared by the tags disagrees with what's already on disk, the
// existing table wins — drop the FTS5 table manually to re-create it
// with the new schema.
func Migrate(db *gorm.DB, models ...any) error {
	p, err := pluginFrom(db)
	if err != nil {
		return err
	}

	if err := db.AutoMigrate(models...); err != nil {
		return err
	}

	for _, m := range models {
		mm, err := p.registerSchema(db, m)
		if err != nil {
			return err
		}
		if len(mm.Fields) == 0 {
			continue
		}
		if err := createFTSTable(db, mm); err != nil {
			return err
		}
		// External mode is the only one that uses triggers — in-table
		// and contentless modes don't have a separate source to sync
		// from. For those, the plugin's row callbacks (registered in
		// plugin.Initialize) handle Create/Save/Delete.
		if mm.Mode == ModeExternal {
			if err := installTriggers(db, mm); err != nil {
				return err
			}
			if err := backfill(db, mm); err != nil {
				return err
			}
		}
	}
	return nil
}

// createFTSTable issues CREATE VIRTUAL TABLE IF NOT EXISTS with the
// model's columns and the chosen mode's options.
//
// ModeExternal — content='source_table' + content_rowid=pk; soft-delete
// adds a deleted_at UNINDEXED mirror so Search can filter cheaply.
//
// ModeInTable — no content= option; the FTS5 table stores the text.
// Soft-delete still works: we add deleted INTEGER UNINDEXED (boolean
// flag) maintained by the row callbacks.
//
// ModeContentless — content=”; the FTS5 table stores only the
// inverted index. snippet()/highlight() are unusable; the plugin
// catches WithSnippet/WithHighlight at Search time. Soft-delete uses
// the same deleted INTEGER UNINDEXED column as ModeInTable.
func createFTSTable(db *gorm.DB, mm *modelMeta) error {
	cols := make([]string, 0, len(mm.Fields)+1)
	for _, f := range mm.Fields {
		cols = append(cols, f.Column)
	}
	if mm.SoftDelete {
		if mm.Mode == ModeExternal {
			// Mirrors the source's deleted_at timestamp.
			cols = append(cols, "deleted_at UNINDEXED")
		} else {
			// In-table / contentless modes track soft-delete via an
			// owned flag maintained by row callbacks.
			cols = append(cols, "deleted UNINDEXED")
		}
	}

	var opts []string
	switch mm.Mode {
	case ModeExternal:
		opts = append(opts,
			fmt.Sprintf("content=%s", quoteIdent(mm.SourceTable)),
			fmt.Sprintf("content_rowid=%s", quoteIdent(mm.PKField.DBName)),
		)
	case ModeContentless:
		opts = append(opts, "content=''")
	}
	if mm.Tokenize != "" {
		opts = append(opts, fmt.Sprintf("tokenize=%s", sqlString(mm.Tokenize)))
	}
	if mm.Prefix != "" {
		opts = append(opts, fmt.Sprintf("prefix=%s", sqlString(mm.Prefix)))
	}
	if mm.Detail != "" {
		opts = append(opts, fmt.Sprintf("detail=%s", mm.Detail))
	}

	all := strings.Join(cols, ", ")
	if len(opts) > 0 {
		all += ", " + strings.Join(opts, ", ")
	}
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING fts5(%s)",
		quoteIdent(mm.Table), all,
	)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("ftsgorm: create %s: %w", mm.Table, err)
	}
	return nil
}

// backfill seeds the FTS5 index with the source's existing rows. Issued
// once at Migrate; subsequent writes are caught by triggers.
//
// FTS5's documented incremental rebuild is `INSERT INTO fts(fts) VALUES('rebuild')`
// for external-content tables. It re-derives all rows from the content
// table. Cheap on small tables; uses FTS5's optimized re-indexing path.
func backfill(db *gorm.DB, mm *modelMeta) error {
	stmt := fmt.Sprintf("INSERT INTO %s(%s) VALUES('rebuild')",
		quoteIdent(mm.Table), quoteIdent(mm.Table))
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("ftsgorm: rebuild %s: %w", mm.Table, err)
	}
	// External-content rebuild reads UNINDEXED columns from the source
	// directly, so the `deleted_at` mirror is automatically populated.
	return nil
}

// DropSidecar drops the FTS5 table and any triggers we installed for
// the model. Idempotent (DROP ... IF EXISTS). The Migrator's DropTable
// path calls DropTableHook below, so users normally don't need to
// invoke this directly.
func DropSidecar(db *gorm.DB, model any) error {
	p, err := pluginFrom(db)
	if err != nil {
		return err
	}
	return p.dropSidecar(db, model)
}

// DropTableHook satisfies the sqlite-go-again gorm dialector's hook
// interface so db.Migrator().DropTable cascades into the FTS5 table +
// triggers.
func (p *plugin) DropTableHook(db *gorm.DB, model any) error {
	return p.dropSidecar(db, model)
}

func (p *plugin) dropSidecar(db *gorm.DB, model any) error {
	mm, err := p.registerSchema(db, model)
	if err != nil {
		return err
	}
	if len(mm.Fields) == 0 {
		return nil
	}
	// Only external mode has triggers to drop.
	if mm.Mode == ModeExternal {
		for _, name := range triggerNames(mm) {
			if err := db.Exec("DROP TRIGGER IF EXISTS " + quoteIdent(name)).Error; err != nil {
				return err
			}
		}
	}
	if err := db.Exec("DROP TABLE IF EXISTS " + quoteIdent(mm.Table)).Error; err != nil {
		return err
	}
	return nil
}

// quoteIdent is a thin alias for fts.QuoteIdent. Backticks the
// identifier and doubles any embedded backticks.
func quoteIdent(name string) string { return fts.QuoteIdent(name) }

// sqlString wraps a value in single quotes, doubling embedded ones.
// Used for FTS5 option values like tokenize='porter unicode61'.
func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
