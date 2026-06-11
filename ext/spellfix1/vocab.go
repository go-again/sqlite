package spellfix1

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
	"github.com/go-again/sqlite/internal/vtabx"
)

// Vocab is a typed handle to a spellfix1 virtual table — the fuzzy-lookup
// vocabulary peer of vec.Table and fts.Index. Construct it with Create
// (which runs CREATE VIRTUAL TABLE) or Open (assumes the vtab exists). It
// is safe for concurrent use as long as the *sql.DB is.
//
// The spellfix1 vtab module must be registered on every connection db
// hands out. The simplest way is to blank-import the auto sub-package so
// it installs via a [github.com/go-again/sqlite.Driver.ConnectHook]:
//
//	import _ "github.com/go-again/sqlite/ext/spellfix1/auto"
//
// or call [Register] on a pinned *sqlite.Conn. Without the module, Create
// fails with "no such module: spellfix1".
type Vocab struct {
	db   *sql.DB
	name string
}

type createConfig struct{ ifNotExists bool }

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithIfNotExists makes Create idempotent: if the table already exists,
// Create returns a handle to it instead of failing with ErrAlreadyExists.
// The existing table is not validated.
func WithIfNotExists() CreateOption {
	return func(c *createConfig) { c.ifNotExists = true }
}

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed. Same
// shape as vec.ErrAlreadyExists / fts.ErrAlreadyExists.
var ErrAlreadyExists = errors.New("spellfix1: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING spellfix1 and returns a
// handle. By default the call wraps [ErrAlreadyExists] if name is already
// a table; pass [WithIfNotExists] to make it idempotent. Mirrors
// vec.Create and fts.New so the same migration shape works across all
// three typed extensions.
//
// The spellfix1 module must be registered on db's connections — see the
// [Vocab] doc.
func Create(ctx context.Context, db *sql.DB, name string, opts ...CreateOption) (*Vocab, error) {
	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if err := vtabx.Create(ctx, db, name, ModuleName, nil, cfg.ifNotExists, ErrAlreadyExists); err != nil {
		return nil, err
	}
	return &Vocab{db: db, name: name}, nil
}

// Open returns a handle for a spellfix1 vtab that already exists in db.
// It performs no I/O and does not validate that the table exists — use it
// when migration is owned elsewhere. Subsequent calls surface any
// "no such table" error at first use.
func Open(db *sql.DB, name string) (*Vocab, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("spellfix1.Open: %q is not a valid SQL identifier", name)
	}
	return &Vocab{db: db, name: name}, nil
}

// Name returns the underlying SQLite vtab name.
func (v *Vocab) Name() string { return v.name }

// Add inserts a single word. Idempotent: re-adding an existing word is a
// no-op, not an error — the storage table deduplicates on word.
func (v *Vocab) Add(ctx context.Context, word string) error {
	if _, err := v.db.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s(word) VALUES (?)", quote(v.name)), word); err != nil {
		return fmt.Errorf("spellfix1.Add: %w", err)
	}
	return nil
}

// AddMany inserts a batch of words inside a single transaction, with the
// same dedup semantics as Add. Mirrors vec.BatchInsert: one commit for
// the whole batch, not one per word. Caller need not deduplicate the
// batch; the vtab ignores repeats.
func (v *Vocab) AddMany(ctx context.Context, words []string) error {
	if len(words) == 0 {
		return nil
	}
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("spellfix1.AddMany: begin: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s(word) VALUES (?)", quote(v.name)))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("spellfix1.AddMany: prepare: %w", err)
	}
	defer stmt.Close()
	for _, w := range words {
		if _, err := stmt.ExecContext(ctx, w); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("spellfix1.AddMany: insert %q: %w", w, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("spellfix1.AddMany: commit: %w", err)
	}
	return nil
}

// Size returns the number of distinct words in the vocabulary. It counts
// the <name>_storage shadow table directly because the spellfix1 vtab
// rejects a bare SELECT — every query against it requires WHERE word
// MATCH ?. Assumes the vtab lives in the main schema.
func (v *Vocab) Size(ctx context.Context) (int64, error) {
	var n int64
	if err := v.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", quote(storageTable(v.name)))).Scan(&n); err != nil {
		return 0, fmt.Errorf("spellfix1.Size: %w", err)
	}
	return n, nil
}

// Drop removes the vtab and its shadow storage. The handle is unusable
// afterward.
func (v *Vocab) Drop(ctx context.Context) error {
	return vtabx.Drop(ctx, v.db, v.name)
}

// Match is a single hit returned by [Vocab.Correct].
type Match struct {
	Word     string // the vocabulary entry
	Distance int    // Damerau-Levenshtein distance from the query term
	Score    int    // distance*1024 - rank (lower is better)
	MatchLen int    // length of the query prefix consumed
}

type correctConfig struct {
	maxDistance int // 0 == unset: the vtab's stock default (4) applies
	limit       int // 0 == unset: no LIMIT
}

// CorrectOption configures [Vocab.Correct] / [Vocab.CorrectSQL].
type CorrectOption func(*correctConfig)

// WithMaxDistance caps the edit distance of returned matches. It binds the
// spellfix1 HIDDEN scope column so the planner short-circuits early.
// Unset, the vtab's stock default (4) applies. (The HIDDEN column is named
// "scope"; this option is named for what it does.)
func WithMaxDistance(n int) CorrectOption {
	return func(c *correctConfig) { c.maxDistance = n }
}

// WithLimit caps the number of returned matches (SQL LIMIT). Unset, the
// query returns every match within the distance bound, ascending by
// distance.
func WithLimit(n int) CorrectOption {
	return func(c *correctConfig) { c.limit = n }
}

// CorrectSQL returns the SQL statement and bound arguments [Vocab.Correct]
// would execute, without running it — for callers who want to run through
// their own *sql.DB or gorm.Raw().Scan() into a custom struct. Mirrors
// vec.KNNSQL and fts.SearchSQL.
func (v *Vocab) CorrectSQL(term string, opts ...CorrectOption) (string, []any, error) {
	cfg := &correctConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	var b strings.Builder
	args := []any{term}
	fmt.Fprintf(&b, "SELECT word, distance, score, matchlen FROM %s WHERE word MATCH ?", quote(v.name))
	if cfg.maxDistance > 0 {
		b.WriteString(" AND scope = ?")
		args = append(args, cfg.maxDistance)
	}
	b.WriteString(" ORDER BY distance ASC")
	if cfg.limit > 0 {
		b.WriteString(" LIMIT ?")
		args = append(args, cfg.limit)
	}
	return b.String(), args, nil
}

// Correct returns the closest vocabulary matches for term, ascending by
// distance. Exact matches (distance == 0) are returned like any other; the
// caller decides whether distance == 0 means "no correction needed". The
// signature mirrors fts.SearchSlice — a term plus options yields a typed
// slice.
func (v *Vocab) Correct(ctx context.Context, term string, opts ...CorrectOption) ([]Match, error) {
	q, args, err := v.CorrectSQL(term, opts...)
	if err != nil {
		return nil, err
	}
	rows, err := v.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("spellfix1.Correct: %w", err)
	}
	defer rows.Close()
	var out []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.Word, &m.Distance, &m.Score, &m.MatchLen); err != nil {
			return nil, fmt.Errorf("spellfix1.Correct: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("spellfix1.Correct: %w", err)
	}
	return out, nil
}
