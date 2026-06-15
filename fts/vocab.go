package fts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// VocabKind selects the shape of an fts5vocab table.
type VocabKind string

const (
	// VocabRow yields one row per distinct term: the term, the number of
	// documents containing it, and its total occurrence count.
	VocabRow VocabKind = "row"
	// VocabCol yields one row per (term, column): VocabRow plus the column name.
	VocabCol VocabKind = "col"
	// VocabInstance yields one row per token occurrence: term, document, column,
	// and byte offset.
	VocabInstance VocabKind = "instance"
)

// Vocab is a typed, read-only handle over an fts5vocab virtual table — a view of
// the term dictionary of an FTS5 index, for term-frequency analytics and
// autocomplete. It mirrors spellfix1.Vocab. Construct it with [NewVocab].
type Vocab struct {
	db    *sql.DB
	name  string
	index string
	kind  VocabKind
}

type vocabConfig struct {
	name        string
	ifNotExists bool
}

// VocabOption configures [NewVocab].
type VocabOption func(*vocabConfig)

// WithVocabName overrides the generated vocab-table name (default
// "<index>_vocab_<kind>").
func WithVocabName(name string) VocabOption { return func(c *vocabConfig) { c.name = name } }

// WithVocabIfNotExists makes [NewVocab] idempotent: an existing vocab table is
// reused instead of failing with [ErrVocabAlreadyExists].
func WithVocabIfNotExists() VocabOption { return func(c *vocabConfig) { c.ifNotExists = true } }

// ErrVocabAlreadyExists wraps the error from [NewVocab] when the vocab table
// already exists and [WithVocabIfNotExists] was not passed.
var ErrVocabAlreadyExists = errors.New("fts: vocab table already exists")

// NewVocab creates an fts5vocab virtual table of the given kind over the FTS5
// index named index, and returns a handle. The vocab table is named
// "<index>_vocab_<kind>" unless overridden with [WithVocabName].
//
// The index must already exist. The vocab table is a live read-only view — it
// reflects the index as queried, holds no copy, and needs no maintenance.
func NewVocab(ctx context.Context, db *sql.DB, index string, kind VocabKind, opts ...VocabOption) (*Vocab, error) {
	if !validIdent(index) {
		return nil, fmt.Errorf("fts.NewVocab: index %q is not a valid SQL identifier", index)
	}
	switch kind {
	case VocabRow, VocabCol, VocabInstance:
	default:
		return nil, fmt.Errorf("fts.NewVocab: invalid kind %q (want row | col | instance)", kind)
	}
	cfg := &vocabConfig{name: index + "_vocab_" + string(kind)}
	for _, o := range opts {
		o(cfg)
	}
	if !validIdent(cfg.name) {
		return nil, fmt.Errorf("fts.NewVocab: vocab name %q is not a valid SQL identifier", cfg.name)
	}
	ifne := ""
	if cfg.ifNotExists {
		ifne = "IF NOT EXISTS "
	}
	// index is ValidIdent (no quote chars), so single-quoting it is safe.
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING fts5vocab('%s', '%s')", ifne, quote(cfg.name), index, string(kind))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if isAlreadyExistsErr(err) {
			return nil, fmt.Errorf("fts.NewVocab %q: %w", cfg.name, ErrVocabAlreadyExists)
		}
		return nil, fmt.Errorf("fts.NewVocab %q: %w", cfg.name, err)
	}
	return &Vocab{db: db, name: cfg.name, index: index, kind: kind}, nil
}

// Name returns the vocab table name.
func (v *Vocab) Name() string { return v.name }

// Kind returns the vocab table kind.
func (v *Vocab) Kind() VocabKind { return v.kind }

// VocabTerm is one dictionary row of a [VocabRow] or [VocabCol] vocab table.
type VocabTerm struct {
	Term        string
	Column      string // populated only for VocabCol
	Documents   int64  // documents containing the term (fts5vocab 'doc')
	Occurrences int64  // total occurrences (fts5vocab 'cnt')
}

// Occurrence is one row of a [VocabInstance] vocab table: a single token occurrence.
type Occurrence struct {
	Term     string
	Document int64  // the source document rowid
	Column   string // the column the token appeared in
	Offset   int64  // token offset within the column
}

// Terms returns the dictionary rows of a [VocabRow] or [VocabCol] table, ordered
// by descending occurrence count (most frequent first). It errors on a
// [VocabInstance] table — use [Vocab.Instances] there.
func (v *Vocab) Terms(ctx context.Context) ([]VocabTerm, error) {
	return v.queryTerms(ctx, 0)
}

// TopTerms is [Vocab.Terms] limited to the n most frequent terms — for
// autocomplete / "trending terms". n <= 0 returns all.
func (v *Vocab) TopTerms(ctx context.Context, n int) ([]VocabTerm, error) {
	return v.queryTerms(ctx, n)
}

func (v *Vocab) queryTerms(ctx context.Context, limit int) ([]VocabTerm, error) {
	var cols string
	withCol := false
	switch v.kind {
	case VocabRow:
		cols = "term, doc, cnt"
	case VocabCol:
		cols = "term, col, doc, cnt"
		withCol = true
	default:
		return nil, fmt.Errorf("fts.Vocab.Terms: not valid for a %q vocab; use Instances", v.kind)
	}
	q := "SELECT " + cols + " FROM " + quote(v.name) + " ORDER BY cnt DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := v.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("fts.Vocab.Terms: %w", err)
	}
	defer rows.Close()
	var out []VocabTerm
	for rows.Next() {
		var t VocabTerm
		if withCol {
			err = rows.Scan(&t.Term, &t.Column, &t.Documents, &t.Occurrences)
		} else {
			err = rows.Scan(&t.Term, &t.Documents, &t.Occurrences)
		}
		if err != nil {
			return nil, fmt.Errorf("fts.Vocab.Terms: scan: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Instances returns the per-occurrence rows of a [VocabInstance] table. It errors
// on a row/col table — use [Vocab.Terms] there.
func (v *Vocab) Instances(ctx context.Context) ([]Occurrence, error) {
	if v.kind != VocabInstance {
		return nil, fmt.Errorf("fts.Vocab.Instances: not valid for a %q vocab; use Terms", v.kind)
	}
	rows, err := v.db.QueryContext(ctx, "SELECT term, doc, col, offset FROM "+quote(v.name))
	if err != nil {
		return nil, fmt.Errorf("fts.Vocab.Instances: %w", err)
	}
	defer rows.Close()
	var out []Occurrence
	for rows.Next() {
		var in Occurrence
		if err := rows.Scan(&in.Term, &in.Document, &in.Column, &in.Offset); err != nil {
			return nil, fmt.Errorf("fts.Vocab.Instances: scan: %w", err)
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Drop removes the vocab virtual table. The underlying FTS5 index is untouched.
func (v *Vocab) Drop(ctx context.Context) error {
	_, err := v.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(v.name))
	return err
}
