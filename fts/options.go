package fts

import (
	"fmt"
	"strings"
)

// SQLType constrains the Go types Index[K, V] supports for keys and values.
// Anything sqlite-storable as a row column is fair game: integers, floats,
// strings, and byte slices. The generics constraint pins this at compile
// time so misuse surfaces before runtime.
type SQLType interface {
	~string | ~[]byte | ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64 | ~float32 | ~float64
}

// Detail picks FTS5's `detail=` option. Lower detail uses less disk and
// memory but disables certain features (Phrase, Near, BM25, snippets).
//
// See https://www.sqlite.org/fts5.html section 4.5.
type Detail string

const (
	// DetailFull is FTS5's default — every feature is available.
	DetailFull Detail = "full"
	// DetailColumn keeps column-affinity info but drops position info;
	// Phrase and Near are unavailable.
	DetailColumn Detail = "column"
	// DetailNone drops both column and position info; only the simplest
	// MATCH operator (single-term) works.
	DetailNone Detail = "none"
)

// External configures FTS5's external-content / content-less mode. When
// ContentTable is set, FTS5 leaves the actual data in the named source table
// and only stores the inverted index in its own table. ContentRowid names
// the column on the source table that maps to FTS5's hidden rowid.
//
// See https://www.sqlite.org/fts5.html section 4.4.
type External struct {
	ContentTable string
	ContentRowid string // optional; defaults to "rowid"

	// SyncTriggers selects which AFTER-INSERT / AFTER-UPDATE / AFTER-
	// DELETE triggers New should install on ContentTable to keep the
	// FTS5 index in sync. Zero (the default) installs no triggers — the
	// caller is responsible for sync. SyncAll installs all three.
	//
	// Trigger names are "<ftsName>_<ai|au|ad>", emitted with
	// IF NOT EXISTS so re-running New (with WithIfNotExists) is
	// idempotent. The FTS5 table name is globally unique inside the
	// SQLite schema, so the FTS5-name-only prefix is sufficient AND
	// keeps trigger names readable under the universal `<content>_fts`
	// naming convention.
	//
	// The columns the triggers reference come from Options.Columns and
	// must exist on both the FTS5 table and the content table with the
	// same names. Triggers expect ContentTable to be available at the
	// time New is called.
	SyncTriggers SyncMode
}

// SyncMode is a bitmask of which sync triggers to install on the
// content table for an external-content FTS5 index.
type SyncMode int

const (
	// SyncInsert installs an AFTER INSERT trigger that adds the new
	// row to the FTS5 index.
	SyncInsert SyncMode = 1 << iota
	// SyncUpdate installs an AFTER UPDATE trigger that re-indexes
	// changed rows via FTS5's 'delete'+INSERT idiom.
	SyncUpdate
	// SyncDelete installs an AFTER DELETE trigger that removes the
	// row from the FTS5 index via the 'delete' magic-row insert.
	SyncDelete
	// SyncAll installs SyncInsert | SyncUpdate | SyncDelete.
	SyncAll = SyncInsert | SyncUpdate | SyncDelete
)

// ColumnSpec is the rich form of an FTS5 column declaration. The bare
// []string form on Options.Columns is shorthand for []ColumnSpec with
// Name set and Unindexed false.
type ColumnSpec struct {
	// Name is the column identifier. Validated against [ValidIdent].
	Name string

	// Unindexed marks the column as FTS5 UNINDEXED — values are stored
	// in the FTS5 row but not added to the inverted index. Use this
	// for metadata columns you want to filter on via [WithFilter]
	// (tenant, status, kind) without paying tokenization cost. Search
	// MATCH queries cannot find text inside an UNINDEXED column;
	// WHERE-clause equality / range predicates can.
	//
	// See https://www.sqlite.org/fts5.html section 4.5.2.
	Unindexed bool
}

// Options configures New. The zero value is valid and produces an FTS5 table
// using unicode61 tokenization with a single "value" column keyed by an
// integer rowid.
type Options struct {
	// Tokenizer overrides the default unicode61 tokenizer.
	Tokenizer Tokenizer

	// Prefix is FTS5's `prefix=` index option (e.g. {2, 3, 4} pre-computes
	// prefix-match indexes for 2-, 3- and 4-character prefixes).
	Prefix []int

	// Columns names the user-visible columns (all indexed). Defaults to
	// ["value"]. The "rowid" column is implicit and used as the primary
	// key. For per-column UNINDEXED control, use ColumnsRich instead;
	// when both are non-empty, ColumnsRich wins and Columns is ignored.
	Columns []string

	// ColumnsRich is the per-column form: each entry can opt into
	// FTS5's UNINDEXED storage. Use this for tables that mix indexed
	// text columns with metadata-only filter columns (tenant, status,
	// kind). When ColumnsRich is non-empty it takes precedence over
	// Columns.
	ColumnsRich []ColumnSpec

	// External enables FTS5's external-content mode; see the External type.
	// Mutually exclusive with Contentless.
	External *External

	// Contentless enables FTS5's contentless mode (`content=''`). The
	// table stores only the inverted index — the original column text is
	// discarded, so SELECT of the column from a contentless table returns
	// NULL. Useful when the source text is reproducible elsewhere and you
	// only need the rowids matching a query.
	//
	// Mutually exclusive with External. Combine with ContentlessDelete to
	// also allow DELETE operations.
	Contentless bool

	// Detail picks the index granularity; defaults to DetailFull.
	Detail Detail

	// ContentlessDelete enables FTS5's contentless_delete=1 option, allowing
	// rows to be DELETEd from a contentless table. Has no effect unless
	// Contentless=true. Requires SQLite >= 3.43.
	ContentlessDelete bool
}

// columnSpecs returns the normalized rich form: ColumnsRich if set,
// otherwise the bare Columns promoted to []ColumnSpec with Unindexed
// false. Default fallback is a single "value" column. Internal use.
func (o Options) columnSpecs() []ColumnSpec {
	if len(o.ColumnsRich) > 0 {
		return o.ColumnsRich
	}
	if len(o.Columns) > 0 {
		out := make([]ColumnSpec, len(o.Columns))
		for i, n := range o.Columns {
			out[i] = ColumnSpec{Name: n}
		}
		return out
	}
	return []ColumnSpec{{Name: "value"}}
}

// tokenizerExpr returns the FTS5 `tokenize=...` fragment for this Options'
// tokenizer, or empty if the tokenizer is unset (so FTS5 uses its default).
func (o Options) tokenizerExpr() string {
	if o.Tokenizer == nil {
		return ""
	}
	return "tokenize = " + o.Tokenizer.encode()
}

// prefixExpr renders the prefix= option (FTS5 spelling: prefix='2 3 4').
func (o Options) prefixExpr() string {
	if len(o.Prefix) == 0 {
		return ""
	}
	parts := make([]string, len(o.Prefix))
	for i, p := range o.Prefix {
		parts[i] = fmt.Sprintf("%d", p)
	}
	return "prefix = '" + strings.Join(parts, " ") + "'"
}

// externalExpr renders the content= and content_rowid= options for the
// external-content mode, or content=” for the contentless mode. Returns
// empty when neither is configured (the FTS5 default: own-content storage).
func (o Options) externalExpr() string {
	if o.External != nil {
		out := "content = '" + o.External.ContentTable + "'"
		if o.External.ContentRowid != "" {
			out += ", content_rowid = '" + o.External.ContentRowid + "'"
		}
		return out
	}
	if o.Contentless {
		return "content = ''"
	}
	return ""
}

func (o Options) detailExpr() string {
	if o.Detail == "" || o.Detail == DetailFull {
		return ""
	}
	return "detail = " + string(o.Detail)
}

func (o Options) contentlessDeleteExpr() string {
	if !o.ContentlessDelete || !o.Contentless {
		return ""
	}
	return "contentless_delete = 1"
}
