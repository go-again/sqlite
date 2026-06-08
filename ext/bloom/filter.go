package bloom

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
)

// Filter is a typed handle to a bloom virtual table — a probabilistic
// set-membership filter, the build-and-test peer of vec.Table /
// fts.Index / spellfix1.Vocab. Construct it with Create (which runs
// CREATE VIRTUAL TABLE) or Open (assumes it exists). It is safe for
// concurrent use as long as the *sql.DB is.
//
// The bloom vtab module must be registered on every connection db hands
// out. The simplest way is to blank-import the auto sub-package so it
// installs via a [github.com/go-again/sqlite.Driver.ConnectHook]:
//
//	import _ "github.com/go-again/sqlite/ext/bloom/auto"
//
// or call [Register] on a pinned *sqlite.Conn. Without the module, Create
// fails with "no such module: bloom".
type Filter struct {
	db   *sql.DB
	name string
}

type createConfig struct {
	size        int
	p           float64
	k           int
	ifNotExists bool
}

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithSize sets the expected element count (the `size` argument). It sizes
// the bit array so the filter holds about this many keys at the target
// false-positive rate. Default 100.
func WithSize(n int) CreateOption { return func(c *createConfig) { c.size = n } }

// WithFalsePositiveRate sets the target false-positive probability (the
// `p` argument, 0 < p < 1). Default 0.01.
func WithFalsePositiveRate(p float64) CreateOption {
	return func(c *createConfig) { c.p = p }
}

// WithHashes sets the number of hash functions probed per key (the `k`
// argument). Default: the optimal count for the chosen p.
func WithHashes(k int) CreateOption { return func(c *createConfig) { c.k = k } }

// WithIfNotExists makes Create idempotent: if the table already exists,
// Create returns a handle to it instead of failing with ErrAlreadyExists.
func WithIfNotExists() CreateOption { return func(c *createConfig) { c.ifNotExists = true } }

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed. Same
// shape as vec.ErrAlreadyExists.
var ErrAlreadyExists = errors.New("bloom: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING bloom(size=…, p=…, k=…) and
// returns a handle, hiding the argument string and its quoting. Omitted
// options take the vtab defaults (size=100, p=0.01, k=optimal). By default
// the call wraps [ErrAlreadyExists] if name already exists; pass
// [WithIfNotExists] to make it idempotent.
//
// The bloom module must be registered on db's connections — see the
// [Filter] doc.
func Create(ctx context.Context, db *sql.DB, name string, opts ...CreateOption) (*Filter, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("bloom.Create: %q is not a valid SQL identifier", name)
	}
	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	var params []string
	if cfg.size > 0 {
		params = append(params, fmt.Sprintf("size=%d", cfg.size))
	}
	if cfg.p > 0 {
		params = append(params, fmt.Sprintf("p=%g", cfg.p))
	}
	if cfg.k > 0 {
		params = append(params, fmt.Sprintf("k=%d", cfg.k))
	}
	using := ModuleName
	if len(params) > 0 {
		using = fmt.Sprintf("%s(%s)", ModuleName, strings.Join(params, ", "))
	}
	ifNotExists := ""
	if cfg.ifNotExists {
		ifNotExists = "IF NOT EXISTS "
	}
	stmt := fmt.Sprintf("CREATE VIRTUAL TABLE %s%s USING %s", ifNotExists, quote(name), using)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if sqlid.IsAlreadyExistsErr(err) {
			return nil, fmt.Errorf("bloom.Create %q: %w", name, ErrAlreadyExists)
		}
		return nil, fmt.Errorf("bloom.Create %q: %w", name, err)
	}
	return &Filter{db: db, name: name}, nil
}

// Open returns a handle for a bloom vtab that already exists in db. It
// performs no I/O and does not validate that the table exists — use it
// when migration is owned elsewhere.
func Open(db *sql.DB, name string) (*Filter, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("bloom.Open: %q is not a valid SQL identifier", name)
	}
	return &Filter{db: db, name: name}, nil
}

// Name returns the underlying SQLite vtab name.
func (f *Filter) Name() string { return f.name }

// Add inserts a key into the filter. Idempotent: re-adding a key sets the
// same bits, so it is a no-op.
func (f *Filter) Add(ctx context.Context, key string) error {
	if _, err := f.db.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s(word) VALUES (?)", quote(f.name)), key); err != nil {
		return fmt.Errorf("bloom.Add: %w", err)
	}
	return nil
}

// AddMany inserts a batch of keys inside a single transaction. Mirrors
// vec.BatchInsert: one commit for the whole batch, not one per key.
func (f *Filter) AddMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("bloom.AddMany: begin: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s(word) VALUES (?)", quote(f.name)))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("bloom.AddMany: prepare: %w", err)
	}
	defer stmt.Close()
	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, key); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("bloom.AddMany: insert %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bloom.AddMany: commit: %w", err)
	}
	return nil
}

// Contains reports whether key is probably in the filter.
//
// A true result means "probably present": Bloom filters have a tunable
// false-positive rate (see [WithFalsePositiveRate]), so true can be wrong
// for a key that was never added. A false result is definitive — the key
// was certainly never added.
func (f *Filter) Contains(ctx context.Context, key string) (bool, error) {
	var present sql.NullInt64
	err := f.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT present FROM %s WHERE word = ?", quote(f.name)), key).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		// The vtab emits no row for an absent key — definitely not present.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("bloom.Contains: %w", err)
	}
	return present.Valid && present.Int64 != 0, nil
}

// Drop removes the vtab and its shadow storage. The handle is unusable
// afterward.
func (f *Filter) Drop(ctx context.Context) error {
	if _, err := f.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(f.name)); err != nil {
		return fmt.Errorf("bloom.Drop: %w", err)
	}
	return nil
}
