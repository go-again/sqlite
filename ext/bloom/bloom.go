// Package bloom provides a Bloom filter virtual table — a
// space-efficient probabilistic structure for set-membership testing.
// Classic use case: filter a stream of candidates against a known set
// before paying the cost of an exact join.
//
//	CREATE VIRTUAL TABLE recent USING bloom(size=100000, p=0.01);
//	INSERT INTO recent(word) VALUES ('hello');
//	SELECT present FROM recent WHERE word = ?;  -- returns 1 if present.
//
// # Persistence
//
// The bit array is persisted to a shadow table named `<vtab>_storage`
// on the same schema. Reads and writes go through SQLite's incremental
// BLOB API ([github.com/go-again/sqlite.Conn.OpenBlob]), so the bit
// array survives [database/sql.DB.Close] and reconnects. The shadow
// table is dropped automatically when the virtual table is dropped.
//
// # Module parameters
//
// Positional and named forms are both accepted:
//
//		CREATE VIRTUAL TABLE name USING bloom(100000, 0.01, 7);
//		CREATE VIRTUAL TABLE name USING bloom(size=100000, p=0.01, k=7);
//
//	  - size=N (default 100) — expected element count. Used to size the
//	    bit array.
//	  - p=0.01 (default 0.01) — target false-positive probability (0 < p < 1).
//	  - k=N — number of hash functions. Default: optimal for the chosen p,
//	    `round(-log2(p))`.
//
// # Schema
//
// The vtab declares one visible column (`present` BOOL) and one HIDDEN
// column (`word` TEXT) used for INSERTs and WHERE-clause membership
// tests:
//
//	INSERT INTO name(word) VALUES (?);                -- add to filter
//	SELECT present FROM name WHERE word = ?;          -- test membership
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/bloom"
//	)
//
//	if err := bloom.Register(conn); err != nil { ... }
//
// For pool-wide auto-registration via [sqlite.Driver.ConnectHook]:
//
//	import _ "github.com/go-again/sqlite/ext/bloom/auto"
//
// # Acknowledgement
//
// Ported from [ncruces/ext/bloom] with a Go-native blob-IO path.
//
// [ncruces/ext/bloom]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/bloom
package bloom

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"strconv"
	"strings"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/internal/sqlid"
)

// Stable 8-byte salts prepended to the FNV input to derive two
// stream-independent 64-bit hashes from a single hash family. Stable
// across processes because the filter state in the shadow blob must
// give the same bit positions on every reopen — a randomized
// hash/maphash seed would break that contract.
var (
	saltA = [8]byte{0xc9, 0xf1, 0x0a, 0x4c, 0xb1, 0xc3, 0xeb, 0x96}
	saltB = [8]byte{0x9e, 0x37, 0x79, 0xb9, 0x7f, 0x4a, 0x7c, 0x15}
)

// ModuleName is the name the vtab registers under: `bloom`.
const ModuleName = "bloom"

// Register installs the bloom module on c. xCreate (first CREATE
// VIRTUAL TABLE) builds the shadow storage table; xConnect (every
// subsequent open) just reads the persisted params from it.
func Register(c *sqlite.Conn) error {
	return c.CreateModuleSplit(ModuleName, createCtor, connectCtor)
}

func createCtor(c *sqlite.Conn, _, schema, vtabName string, args []string) (sqlite.VTab, error) {
	t := newTable(c, schema, vtabName)
	if err := c.DeclareVTab(
		`CREATE TABLE x(present, word TEXT HIDDEN NOT NULL PRIMARY KEY) WITHOUT ROWID`,
	); err != nil {
		return nil, err
	}
	if err := parseArgs(t, args); err != nil {
		return nil, err
	}
	if err := t.create(); err != nil {
		return nil, err
	}
	return t, nil
}

func connectCtor(c *sqlite.Conn, _, schema, vtabName string, _ []string) (sqlite.VTab, error) {
	t := newTable(c, schema, vtabName)
	if err := c.DeclareVTab(
		`CREATE TABLE x(present, word TEXT HIDDEN NOT NULL PRIMARY KEY) WITHOUT ROWID`,
	); err != nil {
		return nil, err
	}
	if err := t.loadParams(); err != nil {
		return nil, fmt.Errorf("bloom: connect: load params from %s: %w", t.qualified(), err)
	}
	return t, nil
}

func newTable(c *sqlite.Conn, schema, vtabName string) *table {
	if schema == "" {
		schema = "main"
	}
	return &table{
		conn:    c,
		schema:  schema,
		storage: vtabName + "_storage",
	}
}

type table struct {
	conn    *sqlite.Conn
	schema  string
	storage string

	// hashes is k — number of hash functions probed per word.
	hashes int
	// bytes is the size of the bit array in bytes (m/8).
	bytes int64
	prob  float64
	nElem int64
}

func (t *table) qualified() string {
	return quote(t.schema) + "." + quote(t.storage)
}

func (t *table) loadParams() error {
	stmt, err := t.conn.Prepare(fmt.Sprintf(
		`SELECT length(data), p, n, k FROM %s WHERE rowid=1`, t.qualified()))
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	rs, err := stmt.(*sqlite.Stmt).QueryContext(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = rs.Close() }()
	dest := make([]driver.Value, 4)
	if err := rs.Next(dest); err != nil {
		return err
	}
	t.bytes = sqlid.AsInt64(dest[0])
	t.prob = sqlid.AsFloat(dest[1])
	t.nElem = sqlid.AsInt64(dest[2])
	t.hashes = int(sqlid.AsInt64(dest[3]))
	return nil
}

func (t *table) create() error {
	ctx := context.Background()
	if _, err := t.conn.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (data BLOB, p REAL, n INTEGER, m INTEGER, k INTEGER)`,
		t.qualified()), nil); err != nil {
		return fmt.Errorf("bloom: create storage: %w", err)
	}
	if _, err := t.conn.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (rowid, data, p, n, m, k) VALUES (1, zeroblob(%d), %f, %d, %d, %d)`,
		t.qualified(), t.bytes, t.prob, t.nElem, 8*t.bytes, t.hashes), nil); err != nil {
		// Seed failed after CREATE TABLE already committed; drop the
		// half-initialised shadow table so the schema doesn't leak. If
		// the drop itself fails the seed error is still primary.
		if _, dropErr := t.conn.ExecContext(ctx,
			`DROP TABLE `+t.qualified(), nil); dropErr != nil {
			return fmt.Errorf("bloom: seed storage: %w (and drop after failure: %v)", err, dropErr)
		}
		return fmt.Errorf("bloom: seed storage: %w", err)
	}
	return nil
}

func (t *table) Disconnect() error { return nil }

func (t *table) Destroy() error {
	if _, err := t.conn.ExecContext(context.Background(), `DROP TABLE `+t.qualified(), nil); err != nil {
		return fmt.Errorf("bloom: drop storage: %w", err)
	}
	return nil
}

// BestIndex routes `word = ?` constraints into Filter as argv[0].
// Anything else gets a CONSTRAINT — there's no other useful index over
// a Bloom filter.
func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	for i, c := range info.Constraints {
		if c.Column == 1 && c.Op == sqlite.OpEQ && c.Usable {
			info.Constraints[i].ArgIndex = 0
			info.Constraints[i].Omit = true
			info.EstimatedRows = 1
			info.EstimatedCost = float64(t.hashes)
			return nil
		}
	}
	return errors.New(`bloom: WHERE word = ? constraint required`)
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

// Insert hooks the [sqlite.VTabUpdater] interface so
// `INSERT INTO name(word) VALUES (?)` writes new bits to the shadow
// blob. Update/Delete are unsupported by Bloom filters and return an
// error.
func (t *table) Insert(cols []driver.Value, _ *int64) error {
	if len(cols) < 2 {
		return fmt.Errorf("bloom: insert expects (present, word) columns")
	}
	word := sqlid.AsString(cols[1])
	if word == "" {
		return nil
	}
	b, err := t.conn.OpenBlob(t.schema, t.storage, "data", 1, true)
	if err != nil {
		return fmt.Errorf("bloom: open storage blob: %w", err)
	}
	defer func() { _ = b.Close() }()
	mBits := uint64(8 * t.bytes)
	for k := range t.hashes {
		bit := kthHash(k, word) % mBits
		bytePos := int64(bit >> 3)
		bitMask := byte(1 << (bit & 7))
		var buf [1]byte
		// io.ReaderAt may return io.EOF along with the final byte; that
		// is non-fatal — only treat other errors as failures.
		if n, err := b.ReadAt(buf[:], bytePos); err != nil && !(errors.Is(err, io.EOF) && n == 1) {
			return fmt.Errorf("bloom: read bit %d: %w", bit, err)
		}
		buf[0] |= bitMask
		if _, err := b.WriteAt(buf[:], bytePos); err != nil {
			return fmt.Errorf("bloom: write bit %d: %w", bit, err)
		}
	}
	return nil
}

func (*table) Update(int64, []driver.Value, *int64) error {
	return errors.New("bloom: rows cannot be updated; INSERT-only filter")
}

func (*table) Delete(int64) error {
	return errors.New("bloom: rows cannot be deleted; INSERT-only filter")
}

type cursor struct {
	table   *table
	present bool
	word    driver.Value
	emitted bool
}

func (c *cursor) Filter(_ int, _ string, args []driver.Value) error {
	if len(args) == 0 {
		return errors.New("bloom: Filter received no constraint argument")
	}
	c.word = args[0]
	c.emitted = false
	word := sqlid.AsString(args[0])
	if word == "" {
		c.present = false
		return nil
	}
	b, err := c.table.conn.OpenBlob(c.table.schema, c.table.storage, "data", 1, false)
	if err != nil {
		return fmt.Errorf("bloom: open storage blob: %w", err)
	}
	defer func() { _ = b.Close() }()
	mBits := uint64(8 * c.table.bytes)
	c.present = true
	for k := range c.table.hashes {
		bit := kthHash(k, word) % mBits
		bytePos := int64(bit >> 3)
		bitMask := byte(1 << (bit & 7))
		var buf [1]byte
		if n, err := b.ReadAt(buf[:], bytePos); err != nil && !(errors.Is(err, io.EOF) && n == 1) {
			return fmt.Errorf("bloom: read bit %d: %w", bit, err)
		}
		if buf[0]&bitMask == 0 {
			c.present = false
			return nil
		}
	}
	return nil
}

func (c *cursor) Next() error           { c.emitted = true; return nil }
func (c *cursor) Eof() bool             { return c.emitted || !c.present }
func (c *cursor) Rowid() (int64, error) { return 0, nil }
func (c *cursor) Close() error          { return nil }

func (c *cursor) Column(col int) (driver.Value, error) {
	switch col {
	case 0:
		return c.present, nil
	case 1:
		return c.word, nil
	}
	return nil, nil
}

// --- arg parsing + bit-array math ---

func parseArgs(t *table, args []string) error {
	var (
		size = int64(100)
		p    = 0.01
		k    = 0
	)
	positional := 0
	for _, a := range args {
		key, val, hasEq := strings.Cut(a, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if !hasEq {
			switch positional {
			case 0:
				key, val = "size", key
			case 1:
				key, val = "p", key
			case 2:
				key, val = "k", key
			default:
				return fmt.Errorf("bloom: too many positional arguments at %q", a)
			}
			positional++
		}
		switch key {
		case "size", "n":
			n, err := strconv.ParseInt(unquote(val), 10, 63)
			if err != nil || n <= 0 {
				return fmt.Errorf("bloom: size must be a positive integer, got %q", val)
			}
			size = n
		case "p", "probability":
			f, err := strconv.ParseFloat(unquote(val), 64)
			if err != nil || f <= 0 || f >= 1 {
				return fmt.Errorf("bloom: p must be in (0,1), got %q", val)
			}
			p = f
		case "k", "hashes":
			n, err := strconv.Atoi(unquote(val))
			if err != nil || n <= 0 {
				return fmt.Errorf("bloom: k must be a positive integer, got %q", val)
			}
			k = n
		default:
			return fmt.Errorf("bloom: unknown parameter %q", key)
		}
	}
	if k == 0 {
		k = optimalK(p)
	}
	t.hashes = k
	t.prob = p
	t.nElem = size
	t.bytes = optimalBytes(size, p)
	return nil
}

// quote / unquote forward to [internal/sqlid] so this extension shares
// the canonical implementations with the rest of the ext/* fleet.
func quote(ident string) string { return sqlid.QuoteIdent(ident) }
func unquote(s string) string   { return sqlid.Unquote(s) }

func optimalK(p float64) int {
	k := math.Round(-math.Log2(p))
	if k < 1 {
		return 1
	}
	return int(k)
}

// optimalBytes returns ceil(m/8), where m = -n*ln(p) / (ln(2))² is the
// Bloom-filter optimal bit count for n elements at false-positive
// probability p.
func optimalBytes(n int64, p float64) int64 {
	m := -float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)
	if m < 64 {
		m = 64
	}
	return (int64(math.Ceil(m)) + 7) / 8
}

// kthHash uses the Kirsch-Mitzenmacher double-hashing trick: derive
// two stream-independent 64-bit hashes from a single FNV-1a stream by
// prepending two different 8-byte salts, then combine as
// `h1 + k*h2`. Approximates k independent hash functions with two real
// hashes. The salts are constants — the bit positions must match
// across process restarts since the shadow blob persists.
func kthHash(k int, word string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(saltA[:])
	_, _ = h.Write([]byte(word))
	h1 := h.Sum64()
	h.Reset()
	_, _ = h.Write(saltB[:])
	_, _ = h.Write([]byte(word))
	h2 := h.Sum64()
	return h1 + uint64(k)*h2
}
