// Package bloom provides a Bloom filter virtual table — a
// space-efficient probabilistic structure for set-membership testing.
// Classic use case: filter a stream of candidates against a known set
// before paying the cost of an exact join.
//
//	CREATE VIRTUAL TABLE recent USING bloom(size=100000, p=0.01);
//	INSERT INTO recent(word) VALUES ('hello');
//	SELECT present FROM recent WHERE word = ?;  -- returns 1 if present.
//
// # In-memory only
//
// This implementation keeps the bit array in Go memory for the lifetime
// of the connection. The filter does NOT persist across `db.Close()` or
// reconnects — see [ncruces/ext/bloom] for an upstream design that
// persists to a shadow table via the SQLite incremental BLOB API
// (`sqlite3_blob_open`), which our driver does not yet expose. For the
// common "build once per session, query many times" pattern, in-memory
// is fine; for cross-session use, write your own persistence shim.
//
// # Module parameters
//
//   - size=N — expected element count. Used to size the bit array.
//     Default: 100.
//   - p=0.01 — target false-positive probability (0 < p < 1). Default 0.01.
//   - k=N — number of hash functions. Default: optimal for the chosen p,
//     `round(-log2(p))`.
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
// Ported from [ncruces/ext/bloom]. Function lineup matches; persistence
// strategy differs (we go in-memory).
//
// [ncruces/ext/bloom]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/bloom
package bloom

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"
	"strings"
	"sync"

	sqlite "github.com/go-again/sqlite"
)

// ModuleName is the name the vtab registers under: `bloom`.
const ModuleName = "bloom"

// Register installs the bloom module on c.
func Register(c *sqlite.Conn) error {
	return c.CreateModule(ModuleName, ctor)
}

func ctor(c *sqlite.Conn, _, _, _ string, args []string) (sqlite.VTab, error) {
	t, err := buildTable(args)
	if err != nil {
		return nil, err
	}
	if err := c.DeclareVTab(
		`CREATE TABLE x(present, word TEXT HIDDEN NOT NULL PRIMARY KEY) WITHOUT ROWID`,
	); err != nil {
		return nil, err
	}
	return t, nil
}

type table struct {
	mu     sync.RWMutex
	bits   []uint64 // bit array, packed
	mBits  uint64   // total bits in the array
	hashes int      // number of hash functions (k)
	prob   float64  // documented false-positive probability
	size   int64    // expected element count
}

func (*table) Disconnect() error { return nil }
func (*table) Destroy() error    { return nil }

// BestIndex routes `word = ?` constraints into Filter as argv[0].
// Anything else gets SQLITE_CONSTRAINT — there's no other useful index
// over a Bloom filter.
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
// `INSERT INTO name(word) VALUES (?)` populates the bit array.
// Update/Delete are unsupported by Bloom filters and return an error.
func (t *table) Insert(cols []driver.Value, _ *int64) error {
	if len(cols) < 2 {
		return fmt.Errorf("bloom: insert expects (present, word) columns")
	}
	word := stringify(cols[1])
	if word == "" {
		return nil // ignore NULL / empty
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.hashes {
		bit := kthHash(k, word) % t.mBits
		t.bits[bit/64] |= 1 << (bit % 64)
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
	word := stringify(args[0])
	if word == "" {
		// NULL or empty word — treat as not present.
		c.present = false
		return nil
	}
	c.table.mu.RLock()
	defer c.table.mu.RUnlock()
	c.present = true
	for k := range c.table.hashes {
		bit := kthHash(k, word) % c.table.mBits
		if c.table.bits[bit/64]&(1<<(bit%64)) == 0 {
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

func buildTable(args []string) (*table, error) {
	var (
		size = int64(100)
		p    = 0.01
		k    = 0
	)
	for _, a := range args {
		key, val, ok := strings.Cut(a, "=")
		if !ok {
			return nil, fmt.Errorf("bloom: argument %q is not a key=value pair", a)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "size", "n":
			n, err := strconv.ParseInt(unquote(val), 10, 63)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("bloom: size must be a positive integer, got %q", val)
			}
			size = n
		case "p", "probability":
			f, err := strconv.ParseFloat(unquote(val), 64)
			if err != nil || f <= 0 || f >= 1 {
				return nil, fmt.Errorf("bloom: p must be in (0,1), got %q", val)
			}
			p = f
		case "k", "hashes":
			n, err := strconv.Atoi(unquote(val))
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("bloom: k must be a positive integer, got %q", val)
			}
			k = n
		default:
			return nil, fmt.Errorf("bloom: unknown parameter %q", key)
		}
	}
	if k == 0 {
		k = optimalK(p)
	}
	m := optimalM(size, p)
	words := (m + 63) / 64
	return &table{
		bits:   make([]uint64, words),
		mBits:  uint64(m),
		hashes: k,
		prob:   p,
		size:   size,
	}, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func optimalK(p float64) int {
	k := math.Round(-math.Log2(p))
	if k < 1 {
		return 1
	}
	return int(k)
}

func optimalM(n int64, p float64) uint64 {
	// m = -n * ln(p) / (ln(2))²
	m := -float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)
	if m < 64 {
		return 64
	}
	return uint64(math.Ceil(m))
}

// kthHash uses the Kirsch-Mitzenmacher double-hashing trick: compute
// two independent 64-bit hashes (FNV-1 and FNV-1a) and combine as
// `h1 + k*h2`. Approximates k independent hash functions with two real
// hashes. Standard technique for Bloom filters.
func kthHash(k int, word string) uint64 {
	h1 := fnv.New64()
	_, _ = h1.Write([]byte(word))
	h2 := fnv.New64a()
	_, _ = h2.Write([]byte(word))
	return h1.Sum64() + uint64(k)*h2.Sum64()
}

func stringify(v driver.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(v)
}
