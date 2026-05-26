package ftsgorm

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// installTriggers creates the AFTER INSERT/UPDATE/DELETE triggers on
// the source table that keep the FTS5 external-content index in sync.
// Matches the documented FTS5 pattern (sqlite.org/fts5.html §4.4.3),
// extended with deleted-column maintenance when the model uses
// gorm.DeletedAt.
//
// Triggers are owned by ftsgorm: their names follow the convention
//
//	ftsgorm_<source>_<ai|au|ad>
//
// so DropSidecar can clean them up reliably.
func installTriggers(db *gorm.DB, mm *modelMeta) error {
	names := triggerNames(mm)
	pk := quoteIdent(mm.PKField.DBName)

	cols := make([]string, 0, len(mm.Fields)+1)
	newVals := make([]string, 0, len(mm.Fields)+1)
	oldVals := make([]string, 0, len(mm.Fields)+1)
	for _, f := range mm.Fields {
		col := quoteIdent(f.Column)
		// FTS5's column names track the user's `column=` choice but
		// the source has its own gorm-resolved column names. We use
		// the gorm column name on the NEW/OLD side; the FTS5 trigger
		// column list must match what we declared in CREATE VIRTUAL
		// TABLE.
		gormColumn := strings.ToLower(f.FieldName)
		cols = append(cols, col)
		newVals = append(newVals, "NEW."+quoteIdent(gormColumn))
		oldVals = append(oldVals, "OLD."+quoteIdent(gormColumn))
	}
	// Soft-delete mirror: deleted_at lives in the FTS5 table so the
	// search filter doesn't need a JOIN. The column tracks the source's
	// gorm.DeletedAt timestamp 1:1.
	if mm.SoftDelete {
		cols = append(cols, quoteIdent("deleted_at"))
		newVals = append(newVals, "NEW."+quoteIdent("deleted_at"))
		oldVals = append(oldVals, "OLD."+quoteIdent("deleted_at"))
	}

	colList := strings.Join(cols, ", ")
	newList := strings.Join(newVals, ", ")
	oldList := strings.Join(oldVals, ", ")

	// Trigger bodies follow the FTS5 external-content pattern.
	// deleted_at is just another column in the list when soft-delete is
	// in play; no special-casing needed because we always include it
	// in colList/newList/oldList above.
	insertBody := fmt.Sprintf(
		"INSERT INTO %s(rowid, %s) VALUES (NEW.%s, %s);",
		quoteIdent(mm.Table), colList, pk, newList)

	deleteBody := fmt.Sprintf(
		"INSERT INTO %s(%s, rowid, %s) VALUES('delete', OLD.%s, %s);",
		quoteIdent(mm.Table), quoteIdent(mm.Table), colList, pk, oldList)

	updateBody := fmt.Sprintf(
		"INSERT INTO %s(%s, rowid, %s) VALUES('delete', OLD.%s, %s);",
		quoteIdent(mm.Table), quoteIdent(mm.Table), colList, pk, oldList)
	updateBody += fmt.Sprintf(
		"INSERT INTO %s(rowid, %s) VALUES (NEW.%s, %s);",
		quoteIdent(mm.Table), colList, pk, newList)

	triggers := []struct {
		name string
		when string
		body string
	}{
		{names[0], "AFTER INSERT", insertBody},
		{names[1], "AFTER UPDATE", updateBody},
		{names[2], "AFTER DELETE", deleteBody},
	}
	for _, t := range triggers {
		stmt := fmt.Sprintf(
			"CREATE TRIGGER IF NOT EXISTS %s %s ON %s BEGIN %s END",
			quoteIdent(t.name), t.when, quoteIdent(mm.SourceTable), t.body)
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("ftsgorm: create trigger %s: %w", t.name, err)
		}
	}
	return nil
}

// triggerNames returns the three trigger names for a model, in
// AI / AU / AD order so DropSidecar can iterate without remembering
// individual suffixes.
func triggerNames(mm *modelMeta) []string {
	prefix := "ftsgorm_" + mm.SourceTable + "_"
	return []string{prefix + "ai", prefix + "au", prefix + "ad"}
}
