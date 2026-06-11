package closure

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-again/sqlite/internal/sqlid"
	"github.com/go-again/sqlite/internal/vtabx"
)

// Graph is a typed handle to a transitive_closure virtual table — a
// read-only parent→child graph walker, the query peer of vec.Table /
// fts.Index. Construct it with Create (which runs CREATE VIRTUAL TABLE)
// or Open (assumes it exists). It is safe for concurrent use as long as
// the *sql.DB is.
//
// A Graph has no Add: it is a live view over the caller's edge table —
// mutate that table, not the vtab.
//
// The transitive_closure module must be registered on every connection db
// hands out. The simplest way is to blank-import the auto sub-package so
// it installs via a [github.com/go-again/sqlite.Driver.ConnectHook]:
//
//	import _ "github.com/go-again/sqlite/ext/closure/auto"
//
// or call [Register] on a pinned *sqlite.Conn. Without the module, Create
// fails with "no such module: transitive_closure".
type Graph struct {
	db   *sql.DB
	name string
}

// Over names the edge table a Graph walks. Any field may be left empty at
// Create and supplied per-query via [OverGraph] — useful for pointing one
// Graph at many edge tables.
type Over struct {
	Table        string
	IDColumn     string
	ParentColumn string
}

// Reversed swaps IDColumn and ParentColumn so a Graph walks ancestors
// instead of descendants. transitive_closure is parent→child only;
// walking the reversed edge yields the ancestor chain (e.g. the manager
// chain above a node).
func Reversed(over Over) Over {
	return Over{Table: over.Table, IDColumn: over.ParentColumn, ParentColumn: over.IDColumn}
}

type createConfig struct{ ifNotExists bool }

// CreateOption configures [Create].
type CreateOption func(*createConfig)

// WithIfNotExists makes Create idempotent.
func WithIfNotExists() CreateOption { return func(c *createConfig) { c.ifNotExists = true } }

// ErrAlreadyExists wraps the error returned by Create when the named
// virtual table already exists and WithIfNotExists was not passed. Same
// shape as vec.ErrAlreadyExists.
var ErrAlreadyExists = errors.New("closure: virtual table already exists")

// Create runs CREATE VIRTUAL TABLE name USING transitive_closure(...) with
// the supplied edge-table binding and returns a handle, hiding the
// argument string. Any Over field left empty must be supplied per call via
// [OverGraph]. By default the call wraps [ErrAlreadyExists] if name
// already exists; pass [WithIfNotExists] to make it idempotent.
//
// The module must be registered on db's connections — see the [Graph] doc.
func Create(ctx context.Context, db *sql.DB, name string, over Over, opts ...CreateOption) (*Graph, error) {
	cfg := &createConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	var params []string
	for _, p := range []struct{ key, val string }{
		{"tablename", over.Table},
		{"idcolumn", over.IDColumn},
		{"parentcolumn", over.ParentColumn},
	} {
		if p.val == "" {
			continue
		}
		if !sqlid.ValidIdent(p.val) {
			return nil, fmt.Errorf("closure.Create: %s %q is not a valid SQL identifier", p.key, p.val)
		}
		params = append(params, p.key+"="+p.val)
	}
	if err := vtabx.Create(ctx, db, name, ModuleName, params, cfg.ifNotExists, ErrAlreadyExists); err != nil {
		return nil, err
	}
	return &Graph{db: db, name: name}, nil
}

// Open returns a handle for a transitive_closure vtab that already exists
// in db. It performs no I/O and does not validate that the table exists.
func Open(db *sql.DB, name string) (*Graph, error) {
	if !sqlid.ValidIdent(name) {
		return nil, fmt.Errorf("closure.Open: %q is not a valid SQL identifier", name)
	}
	return &Graph{db: db, name: name}, nil
}

// Name returns the underlying SQLite vtab name.
func (g *Graph) Name() string { return g.name }

// Node is one node reachable from the queried root.
type Node struct {
	ID    int64 // the node id
	Depth int64 // 0 for the root, 1 for its direct children, etc.
}

type walkConfig struct {
	maxDepth   int
	exactDepth int
	hasMax     bool
	hasExact   bool
	over       *Over
}

// WalkOption configures [Graph.Descendants] / [Graph.DescendantsSQL].
type WalkOption func(*walkConfig)

// WithMaxDepth limits results to depth <= n (0 is the root itself).
func WithMaxDepth(n int) WalkOption {
	return func(c *walkConfig) { c.maxDepth = n; c.hasMax = true }
}

// WithExactDepth limits results to exactly depth == n. Takes precedence
// over WithMaxDepth if both are set.
func WithExactDepth(n int) WalkOption {
	return func(c *walkConfig) { c.exactDepth = n; c.hasExact = true }
}

// OverGraph retargets this walk to a different edge table and columns,
// driving the vtab's HIDDEN tablename/idcolumn/parentcolumn. Required when
// the Graph was created with empty Over fields; otherwise it overrides the
// create-time binding for this call only.
func OverGraph(table, idColumn, parentColumn string) WalkOption {
	return func(c *walkConfig) {
		c.over = &Over{Table: table, IDColumn: idColumn, ParentColumn: parentColumn}
	}
}

// DescendantsSQL returns the SQL statement and bound arguments
// [Graph.Descendants] would execute, without running it — for callers who
// want to run through their own *sql.DB or gorm.Raw().Scan(). Mirrors
// vec.KNNSQL and fts.SearchSQL.
func (g *Graph) DescendantsSQL(root int64, opts ...WalkOption) (string, []any, error) {
	cfg := &walkConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	var b strings.Builder
	args := []any{root}
	fmt.Fprintf(&b, "SELECT id, depth FROM %s WHERE root = ?", quote(g.name))
	switch {
	case cfg.hasExact:
		b.WriteString(" AND depth = ?")
		args = append(args, cfg.exactDepth)
	case cfg.hasMax:
		b.WriteString(" AND depth <= ?")
		args = append(args, cfg.maxDepth)
	}
	if cfg.over != nil {
		b.WriteString(" AND tablename = ? AND idcolumn = ? AND parentcolumn = ?")
		args = append(args, cfg.over.Table, cfg.over.IDColumn, cfg.over.ParentColumn)
	}
	b.WriteString(" ORDER BY depth ASC, id ASC")
	return b.String(), args, nil
}

// Descendants returns every node reachable from root, ordered by depth
// then id. The result includes root itself at depth 0 (matching the vtab);
// filter depth >= 1 if you want strict descendants.
func (g *Graph) Descendants(ctx context.Context, root int64, opts ...WalkOption) ([]Node, error) {
	q, args, err := g.DescendantsSQL(root, opts...)
	if err != nil {
		return nil, err
	}
	rows, err := g.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("closure.Descendants: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Depth); err != nil {
			return nil, fmt.Errorf("closure.Descendants: scan: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("closure.Descendants: %w", err)
	}
	return out, nil
}

// Drop removes the vtab. The underlying edge table is untouched (the vtab
// is only a view). The handle is unusable afterward.
func (g *Graph) Drop(ctx context.Context) error {
	return vtabx.Drop(ctx, g.db, g.name)
}
