// vtab-planning: author a virtual table that plans efficiently. VTabDistinct,
// called from a module's BestIndex, reports how far the current query relaxes row
// ordering and duplication — so the module can stop returning duplicate rows when
// SQLite only needs the distinct values. Equivalent to sqlite3_vtab_distinct.
//
// The `pairs` table yields 0,0,1,1,…,9,9 in ascending order. Because those rows
// are already ordered by value, BestIndex tells SQLite it satisfies
// `ORDER BY value` itself (OrderByConsumed) — which unlocks a relaxed
// VTabDistinct mode for a DISTINCT query. A production module reads that mode and
// returns each value once; here we print how it changes with the query.
//
// Run with:
//
//	just example vtab-planning
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"
)

var lastMode sqlite.VTabDistinctMode // the mode BestIndex last observed

type pairsTable struct{}

func (pairsTable) BestIndex(info *sqlite.IndexInfo) error {
	// The rows come out in value order, so claim we satisfy `ORDER BY value asc`.
	// Without this claim the planner cannot relax anything and VTabDistinct stays
	// "ordered".
	if len(info.OrderBy) == 1 && info.OrderBy[0].Column == 0 && !info.OrderBy[0].Desc {
		info.OrderByConsumed = true
	}
	lastMode = sqlite.VTabDistinct(info)
	info.EstimatedCost, info.EstimatedRows = 20, 20
	return nil
}
func (pairsTable) Open() (sqlite.VTabCursor, error) { return &pairsCursor{}, nil }
func (pairsTable) Disconnect() error                { return nil }
func (pairsTable) Destroy() error                   { return nil }

// pairsCursor yields value = row/2 for rows 0..19 → 0,0,1,1,…,9,9.
type pairsCursor struct{ row int }

func (c *pairsCursor) Filter(int, string, []sqlite.Value) error { c.row = 0; return nil }
func (c *pairsCursor) Next() error                              { c.row++; return nil }
func (c *pairsCursor) Eof() bool                                { return c.row >= 20 }
func (c *pairsCursor) Column(int) (sqlite.Value, error)         { return int64(c.row / 2), nil }
func (c *pairsCursor) Rowid() (int64, error)                    { return int64(c.row), nil }
func (c *pairsCursor) Close() error                             { return nil }

func distinctName(m sqlite.VTabDistinctMode) string {
	return [...]string{"ordered", "grouped", "distinct", "unordered"}[m]
}

func main() {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the CREATE and the queries must share one connection
	ctx := context.Background()

	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(dc any) error {
		return dc.(*sqlite.Conn).CreateEponymousModule("pairs",
			func(c *sqlite.Conn, _, _, _ string, _ []string) (sqlite.VTab, error) {
				if err := c.DeclareVTab(`CREATE TABLE x(value INTEGER)`); err != nil {
					return nil, err
				}
				return pairsTable{}, nil
			})
	}); err != nil {
		log.Fatal(err)
	}

	// A plain scan: SQLite needs every row, in order — no relaxation.
	scan(ctx, conn, `SELECT value FROM pairs`)
	plain := lastMode
	fmt.Printf("SELECT value FROM pairs\n  → VTabDistinct = %s\n", distinctName(plain))

	// DISTINCT + ORDER BY, with the module claiming the ordering: SQLite tells it
	// only distinct values are needed, in any order.
	rows := scan(ctx, conn, `SELECT DISTINCT value FROM pairs ORDER BY value`)
	relaxed := lastMode
	fmt.Printf("SELECT DISTINCT value FROM pairs ORDER BY value\n  → VTabDistinct = %s  (%d distinct rows)\n",
		distinctName(relaxed), rows)
	fmt.Printf("A production module reads the relaxed hint (%q) to return each value once instead of twice.\n",
		distinctName(relaxed))

	// Self-check: a plain scan can't relax; the DISTINCT+ORDER BY query does.
	if plain != sqlite.VTabDistinctOrdered {
		log.Fatalf("plain scan VTabDistinct = %s, want ordered", distinctName(plain))
	}
	if relaxed <= sqlite.VTabDistinctOrdered {
		log.Fatalf("DISTINCT query VTabDistinct = %s, want a relaxed mode", distinctName(relaxed))
	}
	if rows != 10 {
		log.Fatalf("DISTINCT query returned %d rows, want 10", rows)
	}
}

func scan(ctx context.Context, conn *sql.Conn, q string) int {
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			log.Fatal(err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	return n
}
