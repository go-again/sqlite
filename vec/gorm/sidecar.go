package vecgorm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/vec"
	"gorm.io/gorm"
)

// openSidecar returns a vec.Table handle for the meta's sidecar. The
// underlying *sql.DB comes from gorm's connection pool; it is used only
// for KNN reads, where reading the latest committed state is the
// documented behavior. Sidecar writes go through the active *gorm.DB
// (see batchInsertEmbeddings / updateEmbedding / deleteEmbedding) so
// they participate in the caller's transaction.
func openSidecar(db *gorm.DB, m meta) (*vec.Table, error) {
	sqlDB, err := poolDB(db)
	if err != nil {
		return nil, err
	}
	return vec.Open(sqlDB, m.Table, m.Dim, vec.Options{
		Metric:   m.Metric,
		Encoding: m.Encoding,
	})
}

// execPool is the subset of gorm.ConnPool we need to issue sidecar
// statements. Both *sql.DB and *sql.Tx satisfy it. Using this interface
// in callbacks ensures writes participate in any active gorm.Transaction
// rather than auto-committing through the parent *sql.DB.
type execPool interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// activePool returns the connection pool the gorm.DB is currently using.
// Inside a gorm transaction (db.Transaction / db.Begin) this is the
// active *sql.Tx, so sidecar writes commit or roll back with the parent.
// Outside a transaction it is the underlying *sql.DB.
func activePool(db *gorm.DB) (execPool, error) {
	if db.Statement != nil && db.Statement.ConnPool != nil {
		if p, ok := db.Statement.ConnPool.(execPool); ok {
			return p, nil
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("vecgorm: unable to obtain ConnPool: %w", err)
	}
	return sqlDB, nil
}

// poolDB returns a *sql.DB for read paths (KNN) that pre-date the
// pool-aware refactor. When gorm's ConnPool is a *sql.Tx we still hand
// back the underlying *sql.DB — KNN reads are read-only and seeing the
// latest committed state is acceptable; nesting them inside the active
// tx would require plumbing the pool down through vec.Table, which is
// not worth the API churn for a read.
func poolDB(db *gorm.DB) (*sql.DB, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("vecgorm: unable to obtain *sql.DB: %w", err)
	}
	return sqlDB, nil
}

// batchInsertEmbeddings INSERTs every item into the sidecar in a single
// prepared loop. When db is inside a gorm.Transaction the active *sql.Tx
// is reused so the writes commit or roll back with the parent. Outside a
// transaction we wrap the batch in our own tx for atomicity.
//
// When m.SoftDelete is set the statement also writes deleted=0, since
// vec0 INTEGER metadata columns reject NULL.
func batchInsertEmbeddings(ctx context.Context, db *gorm.DB, m meta, items []vec.Row) (err error) {
	if len(items) == 0 {
		return nil
	}
	pool, err := activePool(db)
	if err != nil {
		return err
	}
	stmt := insertStmt(m)

	// Already inside a gorm.Transaction: reuse the active *sql.Tx
	// directly. Do NOT begin a nested tx — SQLite does not support
	// real nesting, and the parent owns Commit/Rollback.
	if _, inTx := pool.(*sql.Tx); inTx {
		prep, err := pool.PrepareContext(ctx, stmt)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := prep.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("vecgorm: close stmt: %w", cerr)
			}
		}()
		for _, it := range items {
			if _, err := prep.ExecContext(ctx, insertArgs(m, it)...); err != nil {
				return fmt.Errorf("vecgorm: insert into %s: %w", m.Table, err)
			}
		}
		return nil
	}

	// Autocommit path: open our own tx so the batch is atomic.
	sqlDB, ok := pool.(*sql.DB)
	if !ok {
		return fmt.Errorf("vecgorm: unexpected ConnPool type %T", pool)
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vecgorm: begin tx for %s: %w", m.Table, err)
	}
	prep, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return joinTxErr(fmt.Errorf("vecgorm: prepare insert into %s: %w", m.Table, err), tx.Rollback())
	}
	defer func() {
		if cerr := prep.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("vecgorm: close stmt: %w", cerr)
		}
	}()
	for _, it := range items {
		if _, err := prep.ExecContext(ctx, insertArgs(m, it)...); err != nil {
			return joinTxErr(
				fmt.Errorf("vecgorm: insert into %s: %w", m.Table, err),
				tx.Rollback(),
			)
		}
	}
	return tx.Commit()
}

// updateEmbedding overwrites a single sidecar row.
func updateEmbedding(ctx context.Context, db *gorm.DB, m meta, rowid int64, emb []float32) error {
	if len(emb) != m.Dim {
		return fmt.Errorf("vecgorm: %s: embedding length %d != dim %d", m.Table, len(emb), m.Dim)
	}
	pool, err := activePool(db)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(
		"UPDATE %s SET %s = %s WHERE rowid = ?",
		quoteIdent(m.Table), quoteIdent(m.Column), m.Encoding.Placeholder(),
	)
	if _, err := pool.ExecContext(ctx, stmt, m.Encoding.Encode(emb), rowid); err != nil {
		return fmt.Errorf("vecgorm: update %s: %w", m.Table, err)
	}
	return nil
}

// deleteEmbedding removes a single sidecar row by rowid.
func deleteEmbedding(ctx context.Context, db *gorm.DB, m meta, rowid int64) error {
	pool, err := activePool(db)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", quoteIdent(m.Table))
	if _, err := pool.ExecContext(ctx, stmt, rowid); err != nil {
		return fmt.Errorf("vecgorm: delete from %s: %w", m.Table, err)
	}
	return nil
}

// insertStmt builds the INSERT for a single sidecar row, with or without
// the soft-delete metadata column.
func insertStmt(m meta) string {
	if m.SoftDelete {
		return fmt.Sprintf(
			"INSERT INTO %s (rowid, %s, deleted) VALUES (?, %s, 0)",
			quoteIdent(m.Table), quoteIdent(m.Column), m.Encoding.Placeholder(),
		)
	}
	return fmt.Sprintf(
		"INSERT INTO %s (rowid, %s) VALUES (?, %s)",
		quoteIdent(m.Table), quoteIdent(m.Column), m.Encoding.Placeholder(),
	)
}

// insertArgs returns the bound arguments for insertStmt in declaration
// order: (rowid, embedding-blob/json).
func insertArgs(m meta, it vec.Row) []any {
	return []any{it.Rowid, m.Encoding.Encode(it.Embedding)}
}

// joinTxErr attaches a rollback error to the original failure so neither
// is silently dropped. errors.Join skips nil values, so passing rbErr=nil
// returns the underlying err unchanged.
func joinTxErr(err error, rbErr error) error {
	if rbErr == nil {
		return err
	}
	return errors.Join(err, fmt.Errorf("vecgorm: rollback after error failed: %w", rbErr))
}

// isSoftDelete reports whether the active UPDATE statement is gorm's
// soft-delete path. We detect it by inspecting Statement.Clauses: gorm
// adds an "UPDATE" clause that targets deleted_at when soft-deleting.
// Falls back to a heuristic on the WHERE clause if not found.
func isSoftDelete(db *gorm.DB) bool {
	if db.Statement == nil || db.Statement.Schema == nil {
		return false
	}
	if db.Statement.Schema.LookUpField("DeletedAt") == nil {
		return false
	}
	for _, set := range db.Statement.Clauses {
		if strings.EqualFold(set.Name, "soft_delete_enabled") {
			return true
		}
	}
	return false
}

// softDeleteSidecar resyncs the sidecar's `deleted` metadata column from
// the source table's deleted_at, *after* gorm's soft-delete UPDATE has
// run on the source. Issued through db.Exec so the write participates in
// any active gorm.Transaction.
func softDeleteSidecar(db *gorm.DB, mm modelMeta, m meta) error {
	pkColumn := quoteIdent(mm.PKField.DBName)
	stmt := fmt.Sprintf(
		"UPDATE %s SET deleted = "+
			"COALESCE((SELECT CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END "+
			"FROM %s WHERE %s.%s = %s.rowid), 0)",
		quoteIdent(m.Table),
		quoteIdent(mm.SourceTable),
		quoteIdent(mm.SourceTable), pkColumn,
		quoteIdent(m.Table),
	)
	if err := db.Exec(stmt).Error; err != nil {
		return fmt.Errorf("vecgorm: soft-delete update on %s: %w", m.Table, err)
	}
	return nil
}

// deleteByWhere garbage-collects sidecar rows whose source rows no
// longer exist. Used when the caller's Delete had no concrete struct
// value (e.g. db.Where("status = ?", 'archived').Delete(&Model{})) so
// we cannot enumerate affected PKs from db.Statement.ReflectValue.
//
// Routes through db.Exec so it joins the active transaction.
func deleteByWhere(ctx context.Context, db *gorm.DB, mm modelMeta, m meta, _ *vec.Table) error {
	stmt := fmt.Sprintf(
		"DELETE FROM %s WHERE rowid NOT IN (SELECT %s FROM %s)",
		quoteIdent(m.Table),
		quoteIdent(mm.PKField.DBName),
		quoteIdent(mm.SourceTable),
	)
	if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
		return fmt.Errorf("vecgorm: delete-by-where on %s: %w", m.Table, err)
	}
	return nil
}

// ErrNotInstalled is returned by helper APIs when vecgorm.Plugin() has
// not been registered on the *gorm.DB. Re-exported so callers can match
// via errors.Is.
var ErrNotInstalled = errors.New("vecgorm: Plugin() not installed on *gorm.DB")
