// Package spellfix1 implements a fuzzy-text-matching virtual table
// inspired by SQLite's spellfix1 extension. Build a vocabulary by
// inserting words, then query with `WHERE word MATCH ?` to retrieve
// the closest matches by edit distance, grouped phonetically.
//
//	CREATE VIRTUAL TABLE vocab USING spellfix1;
//	INSERT INTO vocab(word) VALUES ('apple'), ('banana'), ('cherry'), ('aple');
//	SELECT word, distance FROM vocab WHERE word MATCH 'aple' LIMIT 3;
//	-- aple    0
//	-- apple   1
//	-- ... etc.
//
// For a typed Go handle over this vtab — Create / Add / AddMany / Size /
// Correct / CorrectSQL / Drop, mirroring vec.Table and fts.Index — see
// [Vocab]. The raw SQL above and the typed API are interchangeable.
//
// # Schema
//
//	word      TEXT     -- the candidate vocabulary entry
//	rank      INTEGER  -- caller-supplied frequency score (0 if not given)
//	distance  INTEGER  -- Damerau-Levenshtein distance from the query
//	score     INTEGER  -- distance - rank (lower is better)
//	matchlen  INTEGER  -- length of the query prefix consumed
//	phonetic  HIDDEN   -- the Soundex key (4-char A-Z)
//	top       HIDDEN   -- caller-supplied LIMIT override
//	scope     HIDDEN   -- max distance bound (default 4)
//	srchcnt   HIDDEN   -- how many candidates the planner examined
//	soundslike HIDDEN  -- alternative search-spelling (rarely used)
//
// # What's NOT included from upstream spellfix1
//
//   - Cyrillic / Greek / other non-Latin transliteration tables.
//   - Caller-tunable cost matrices (the `editdist3` cost-matrix API).
//   - The Russian-language phonetic encoder.
//   - The `editdist1` SQL function (use `(SELECT distance FROM vocab
//     WHERE word MATCH ? LIMIT 1)` to get the distance directly).
//
// We use Soundex for phonetic grouping (well-defined, simple,
// language-agnostic) and Damerau-Levenshtein for distance (covers
// transpositions in addition to insert/delete/substitute).
//
// # Persistence
//
// The vocabulary lives in a shadow table named `<vtab>_storage` on
// the same schema. Vocabulary survives `db.Close()` / reconnect.
//
// Ported in spirit from [SQLite spellfix1] — same SQL surface, simpler
// algorithm.
//
// [SQLite spellfix1]: https://sqlite.org/spellfix1.html
package spellfix1

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/internal/sqlid"
)

// ModuleName is the name the vtab registers under: `spellfix1`.
const ModuleName = "spellfix1"

// Register installs the spellfix1 vtab on c. Vocabulary is persisted
// via `(*Conn).OpenBlob` and a shadow table; survives `db.Close()` /
// reconnect.
func Register(c *sqlite.Conn) error {
	return c.CreateModuleSplit(ModuleName, createCtor, connectCtor)
}

const declSQL = `CREATE TABLE x(
	word TEXT,
	rank INTEGER,
	distance INTEGER,
	score INTEGER,
	matchlen INTEGER,
	phonetic HIDDEN,
	top HIDDEN,
	scope HIDDEN,
	srchcnt HIDDEN,
	soundslike HIDDEN
)`

const (
	colWord = iota
	colRank
	colDistance
	colScore
	colMatchLen
	colPhonetic
	colTop
	colScope
	colSrchCnt
	colSoundsLike
)

func createCtor(c *sqlite.Conn, _, schema, vtabName string, args []string) (sqlite.VTab, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("spellfix1: this vtab does not accept module arguments, got %d", len(args))
	}
	t := newTable(c, schema, vtabName)
	if err := c.DeclareVTab(declSQL); err != nil {
		return nil, err
	}
	ctx := context.Background()
	if _, err := c.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (word TEXT NOT NULL, rank INTEGER NOT NULL DEFAULT 0, phonetic TEXT NOT NULL)`,
		t.qualified()), nil); err != nil {
		return nil, fmt.Errorf("spellfix1: create storage: %w", err)
	}
	// CREATE INDEX requires the index name to be schema-qualified, and
	// the bare table name is inferred from that schema.
	if _, err := c.ExecContext(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS %s.%s ON %s(phonetic)`,
		quote(t.schema), quote(t.storage+"_phon"), quote(t.storage)), nil); err != nil {
		return nil, fmt.Errorf("spellfix1: create index: %w", err)
	}
	// UNIQUE on word makes the vocabulary a set: Insert uses INSERT OR
	// IGNORE so re-adding a word is a no-op rather than a silent duplicate
	// (which would inflate COUNT(*) and skew ranking). Added only here in
	// xCreate — existing tables reopened via connectCtor keep their schema,
	// since adding the index to one that already holds duplicates would
	// fail.
	if _, err := c.ExecContext(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS %s.%s ON %s(word)`,
		quote(t.schema), quote(t.storage+"_word"), quote(t.storage)), nil); err != nil {
		return nil, fmt.Errorf("spellfix1: create word index: %w", err)
	}
	return t, nil
}

func connectCtor(c *sqlite.Conn, _, schema, vtabName string, args []string) (sqlite.VTab, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("spellfix1: this vtab does not accept module arguments, got %d", len(args))
	}
	t := newTable(c, schema, vtabName)
	if err := c.DeclareVTab(declSQL); err != nil {
		return nil, err
	}
	return t, nil
}

// storageTable returns the name of the shadow storage table backing a
// spellfix1 vtab named vtabName. Defined once so the vtab implementation
// and the typed Vocab.Size (which counts it directly, since the vtab
// itself rejects a bare SELECT without WHERE word MATCH ?) can't drift on
// the suffix.
func storageTable(vtabName string) string { return vtabName + "_storage" }

func newTable(c *sqlite.Conn, schema, vtabName string) *table {
	if schema == "" {
		schema = "main"
	}
	return &table{
		conn:    c,
		schema:  schema,
		storage: storageTable(vtabName),
	}
}

type table struct {
	conn    *sqlite.Conn
	schema  string
	storage string
}

func (t *table) qualified() string {
	return quote(t.schema) + "." + quote(t.storage)
}

func (t *table) Disconnect() error { return nil }
func (t *table) Destroy() error {
	if _, err := t.conn.ExecContext(context.Background(), `DROP TABLE `+t.qualified(), nil); err != nil {
		return fmt.Errorf("spellfix1: drop storage: %w", err)
	}
	return nil
}

// BestIndex captures the MATCH constraint as argv[0] and optional
// scope/top constraints into IdxNum bits.
//
//	bit 0: scope present (argv index follows)
//	bit 1: top present (argv index follows)
//	IdxStr packs the indices as 2 raw bytes.
func (t *table) BestIndex(info *sqlite.IndexInfo) error {
	var matchIdx = -1
	var scopeIdx = -1
	var topIdx = -1
	for i, cst := range info.Constraints {
		if !cst.Usable {
			continue
		}
		switch cst.Column {
		case colWord:
			if cst.Op == sqlite.OpMATCH {
				matchIdx = i
			}
		case colScope:
			if cst.Op == sqlite.OpEQ {
				scopeIdx = i
			}
		case colTop:
			if cst.Op == sqlite.OpEQ {
				topIdx = i
			}
		}
	}
	if matchIdx < 0 {
		return errors.New("spellfix1: WHERE word MATCH ? constraint required")
	}
	idx := 0
	info.Constraints[matchIdx].ArgIndex = idx
	info.Constraints[matchIdx].Omit = true
	idx++
	var idxNum int64
	var idxBytes []byte
	if scopeIdx >= 0 {
		info.Constraints[scopeIdx].ArgIndex = idx
		info.Constraints[scopeIdx].Omit = true
		idxNum |= 1
		idxBytes = append(idxBytes, byte(idx))
		idx++
	}
	if topIdx >= 0 {
		info.Constraints[topIdx].ArgIndex = idx
		info.Constraints[topIdx].Omit = true
		idxNum |= 2
		idxBytes = append(idxBytes, byte(idx))
	}
	info.IdxNum = idxNum
	info.IdxStr = string(idxBytes)
	info.EstimatedCost = 1000
	return nil
}

func (t *table) Open() (sqlite.VTabCursor, error) {
	return &cursor{table: t}, nil
}

// Insert turns INSERT INTO vtab(word [, rank]) into a write to the
// shadow table. The phonetic key is computed at insert time.
func (t *table) Insert(cols []driver.Value, _ *int64) error {
	if len(cols) < 1 {
		return errors.New("spellfix1: insert expects (word [, rank]) columns")
	}
	// cols layout matches the vtab schema: word, rank, distance,
	// score, matchlen, phonetic, top, scope, srchcnt, soundslike.
	// Only word + rank are meaningful for INSERT; the rest are NULL.
	word := sqlid.AsString(cols[0])
	if word == "" {
		return nil
	}
	rank := int64(0)
	if len(cols) > 1 {
		rank = int64Of(cols[1])
	}
	ph := soundex(word)
	_, err := t.conn.ExecContext(context.Background(), fmt.Sprintf(
		`INSERT OR IGNORE INTO %s (word, rank, phonetic) VALUES (?, ?, ?)`, t.qualified()),
		sqlid.ToNamedValues([]driver.Value{word, rank, ph}))
	if err != nil {
		return fmt.Errorf("spellfix1: insert: %w", err)
	}
	return nil
}

func (*table) Update(int64, []driver.Value, *int64) error {
	return errors.New("spellfix1: rows cannot be updated; DELETE + INSERT to replace")
}

func (*table) Delete(rowid int64) error {
	// Defer until needed — most spellfix workloads are append-only.
	return errors.New("spellfix1: row delete via vtab not yet supported")
}

type matchRow struct {
	word     string
	rank     int64
	distance int
	matchlen int
}

type cursor struct {
	table   *table
	query   string
	scope   int
	top     int
	results []matchRow
	row     int
}

func (c *cursor) Filter(idxNumInt int, idxStr string, args []driver.Value) error {
	idxNum := int(idxNumInt)
	if len(args) < 1 {
		return errors.New("spellfix1: missing MATCH argument")
	}
	c.query = strings.ToLower(sqlid.AsString(args[0]))
	c.scope = 4
	c.top = 20
	ord := []byte(idxStr)
	pos := 0
	if idxNum&1 != 0 {
		if pos < len(ord) {
			c.scope = int(int64Of(args[ord[pos]]))
			pos++
		}
	}
	if idxNum&2 != 0 {
		if pos < len(ord) {
			c.top = int(int64Of(args[ord[pos]]))
		}
	}
	if c.scope < 0 {
		c.scope = 4
	}
	if c.top < 1 {
		c.top = 20
	}

	ph := soundex(c.query)
	// Fetch candidates with the same phonetic key, plus a generous
	// neighborhood (one Soundex digit off) to catch near-misses.
	stmt, err := c.table.conn.Prepare(fmt.Sprintf(
		`SELECT word, rank FROM %s WHERE phonetic = ? OR substr(phonetic,1,2) = substr(?,1,2)`,
		c.table.qualified()))
	if err != nil {
		return fmt.Errorf("spellfix1: scan prepare: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	rs, err := stmt.(*sqlite.Stmt).QueryContext(context.Background(), sqlid.ToNamedValues([]driver.Value{ph, ph}))
	if err != nil {
		return fmt.Errorf("spellfix1: scan query: %w", err)
	}
	row := make([]driver.Value, 2)
	c.results = c.results[:0]
	scanned := 0
	for {
		// Poll the conn's interrupt flag every 256 candidates so a
		// large vocabulary scan honors the enclosing QueryContext's
		// cancellation. The vtab Filter API doesn't pass ctx in.
		if scanned&0xff == 0 && c.table.conn.IsInterrupted() {
			_ = rs.Close()
			return fmt.Errorf("spellfix1: interrupted")
		}
		scanned++
		if err := rs.Next(row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = rs.Close()
			return err
		}
		w := sqlid.AsString(row[0])
		r := int64Of(row[1])
		d := damerauLevenshtein(c.query, strings.ToLower(w), c.scope)
		if d > c.scope {
			continue
		}
		c.results = append(c.results, matchRow{
			word: w, rank: r, distance: d, matchlen: len(c.query),
		})
	}
	_ = rs.Close()

	// Sort by (distance - rank/1024) ascending; cap to top.
	sortByScore(c.results)
	if len(c.results) > c.top {
		c.results = c.results[:c.top]
	}
	c.row = 0
	return nil
}

func (c *cursor) Next() error           { c.row++; return nil }
func (c *cursor) Eof() bool             { return c.row >= len(c.results) }
func (c *cursor) Rowid() (int64, error) { return int64(c.row), nil }
func (c *cursor) Close() error          { return nil }

func (c *cursor) Column(col int) (sqlite.Value, error) {
	r := c.results[c.row]
	switch col {
	case colWord:
		return r.word, nil
	case colRank:
		return r.rank, nil
	case colDistance:
		return int64(r.distance), nil
	case colScore:
		// score = distance*1024 - rank, lower is better.
		return int64(r.distance)*1024 - r.rank, nil
	case colMatchLen:
		return int64(r.matchlen), nil
	case colPhonetic:
		return soundex(r.word), nil
	case colTop:
		return int64(c.top), nil
	case colScope:
		return int64(c.scope), nil
	case colSrchCnt:
		return int64(len(c.results)), nil
	case colSoundsLike:
		return nil, nil
	}
	return nil, nil
}

// --- algorithms ---

// soundex computes the classic 4-character Soundex code for s. Returns
// "0000" for empty / non-letter input so empty queries don't match
// everything.
func soundex(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return "0000"
	}
	digit := func(b byte) byte {
		switch b {
		case 'B', 'F', 'P', 'V':
			return '1'
		case 'C', 'G', 'J', 'K', 'Q', 'S', 'X', 'Z':
			return '2'
		case 'D', 'T':
			return '3'
		case 'L':
			return '4'
		case 'M', 'N':
			return '5'
		case 'R':
			return '6'
		}
		return 0
	}
	out := make([]byte, 0, 4)
	first := s[0]
	if first < 'A' || first > 'Z' {
		return "0000"
	}
	out = append(out, first)
	last := digit(first)
	for i := 1; i < len(s) && len(out) < 4; i++ {
		c := s[i]
		if c < 'A' || c > 'Z' {
			continue
		}
		d := digit(c)
		if d == 0 {
			last = 0
			continue
		}
		if d == last {
			continue
		}
		out = append(out, d)
		last = d
	}
	for len(out) < 4 {
		out = append(out, '0')
	}
	return string(out)
}

// dlScratchPool pools the three working slices damerauLevenshtein
// needs so a vocabulary-wide spelling pass doesn't allocate three
// fresh []int per scored candidate. Each scored candidate borrows
// the same three buffers; concurrent goroutines get their own
// scratch via Pool.
var dlScratchPool = sync.Pool{
	New: func() any { return &dlScratch{} },
}

type dlScratch struct{ prev2, prev1, curr []int }

func (s *dlScratch) reset(n int) {
	if cap(s.prev2) < n {
		s.prev2 = make([]int, n)
		s.prev1 = make([]int, n)
		s.curr = make([]int, n)
		return
	}
	s.prev2 = s.prev2[:n]
	s.prev1 = s.prev1[:n]
	s.curr = s.curr[:n]
	for i := range s.prev2 {
		s.prev2[i] = 0
	}
}

// damerauLevenshtein computes the edit distance between a and b
// allowing single-character transpositions in addition to insert /
// delete / substitute. Bounded to `cap+1` (early-exit when the running
// minimum exceeds cap) so worst-case is O(min(len(a),len(b)) * cap).
func damerauLevenshtein(a, b string, cap int) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	if cap < 0 {
		cap = la + lb
	}
	s := dlScratchPool.Get().(*dlScratch)
	defer dlScratchPool.Put(s)
	s.reset(lb + 1)
	prev2, prev1, curr := s.prev2, s.prev1, s.curr
	for j := 0; j <= lb; j++ {
		prev1[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		minInRow := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			c := prev1[j] + 1
			if v := curr[j-1] + 1; v < c {
				c = v
			}
			if v := prev1[j-1] + cost; v < c {
				c = v
			}
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if v := prev2[j-2] + 1; v < c {
					c = v
				}
			}
			curr[j] = c
			if c < minInRow {
				minInRow = c
			}
		}
		if minInRow > cap {
			return cap + 1
		}
		prev2, prev1, curr = prev1, curr, prev2
	}
	return prev1[lb]
}

func sortByScore(rs []matchRow) {
	// Simple insertion sort — typical len is small (< top, default 20).
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0; j-- {
			if score(rs[j]) < score(rs[j-1]) {
				rs[j-1], rs[j] = rs[j], rs[j-1]
			} else {
				break
			}
		}
	}
}

func score(r matchRow) int64 {
	return int64(r.distance)*1024 - r.rank
}

// --- helpers ---

func quote(s string) string { return sqlid.QuoteIdent(s) }

// int64Of is kept local rather than routed through sqlid.AsInt64: its
// string/[]byte branch uses fmt.Sscan, which tolerates leading
// whitespace and a trailing remainder (and honors a 0x prefix), whereas
// sqlid.AsInt64 requires the whole token to be a base-10 integer.
// Preserving fmt.Sscan keeps the exact coercion behavior the
// scope/top/rank reads relied on.
func int64Of(v driver.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	case []byte:
		var n int64
		_, _ = fmt.Sscan(string(x), &n)
		return n
	case string:
		var n int64
		_, _ = fmt.Sscan(x, &n)
		return n
	}
	return 0
}
