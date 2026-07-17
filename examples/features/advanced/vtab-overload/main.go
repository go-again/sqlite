// vtab-overload: a virtual table that overrides a SQL function on its own columns
// via xFindFunction (the VTabFunctionFinder interface) — a function name gets a
// table-specific meaning, the mechanism behind operators like MATCH.
//
// The `sensor` table holds raw readings and overrides calibrate(raw) to apply
// this table's calibration. SQLite has no calibrate() of its own; you declare the
// name with (*Conn).OverloadFunction so it is accepted at prepare time, and the
// module's FindFunction supplies the body when the function is applied to the
// table's columns.
//
// Run with:
//
//	just example vtab-overload
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	sqlite "gosqlite.org"
)

var readings = []int64{100, 200, 300}

type sensor struct{}

func (sensor) BestIndex(info *sqlite.IndexInfo) error { info.EstimatedCost = 3; return nil }
func (sensor) Open() (sqlite.VTabCursor, error)       { return &sensorCursor{}, nil }
func (sensor) Disconnect() error                      { return nil }
func (sensor) Destroy() error                         { return nil }

// FindFunction overrides calibrate(x) — applied to a sensor column, it runs this
// table's calibration (raw*2 + 1). op 0 means a plain function override (not an
// indexable operator like MATCH).
func (sensor) FindFunction(nArg int, name string) (func(*sqlite.FunctionContext, []sqlite.Value) (sqlite.Value, error), int, bool) {
	if name == "calibrate" && nArg == 1 {
		return func(_ *sqlite.FunctionContext, args []sqlite.Value) (sqlite.Value, error) {
			raw, _ := args[0].(int64)
			return raw*2 + 1, nil
		}, 0, true
	}
	return nil, 0, false
}

type sensorCursor struct{ i int }

func (c *sensorCursor) Filter(int, string, []sqlite.Value) error { c.i = 0; return nil }
func (c *sensorCursor) Next() error                              { c.i++; return nil }
func (c *sensorCursor) Eof() bool                                { return c.i >= len(readings) }
func (c *sensorCursor) Column(int) (sqlite.Value, error)         { return readings[c.i], nil }
func (c *sensorCursor) Rowid() (int64, error)                    { return int64(c.i), nil }
func (c *sensorCursor) Close() error                             { return nil }

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
		c := dc.(*sqlite.Conn)
		if err := c.CreateEponymousModule("sensor",
			func(cc *sqlite.Conn, _, _, _ string, _ []string) (sqlite.VTab, error) {
				if err := cc.DeclareVTab(`CREATE TABLE x(raw INTEGER)`); err != nil {
					return nil, err
				}
				return sensor{}, nil
			}); err != nil {
			return err
		}
		// Declare the name so it is accepted at prepare time; the table's
		// FindFunction supplies the body when it is applied to the table.
		return c.OverloadFunction("calibrate", 1)
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Println("raw → calibrate(raw)   (the sensor table's own calibration):")
	rows, err := conn.QueryContext(ctx, `SELECT raw, calibrate(raw) FROM sensor ORDER BY raw`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw, cal int64
		if err := rows.Scan(&raw, &cal); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %d → %d\n", raw, cal)
		if cal != raw*2+1 {
			log.Fatalf("calibrate(%d) = %d, want %d", raw, cal, raw*2+1)
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("calibrate() means something only against the sensor table — declared with OverloadFunction, bodied by xFindFunction.")
}
