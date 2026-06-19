// Package series implements the `generate_series` table-valued function — an
// eponymous virtual table that yields a sequence of integers:
//
//	SELECT value FROM generate_series(1, 10);     -- 1,2,…,10
//	SELECT value FROM generate_series(0, 100, 25); -- 0,25,50,75,100
//	SELECT value FROM generate_series(5, 1, -1);   -- 5,4,3,2,1 (descending)
//
// start and stop are required; step defaults to 1 and may be negative.
// (SQLite's built-in generate_series treats stop as optional and unbounded;
// this port requires it to avoid an unbounded scan.)
//
// Ported in spirit from SQLite's series.c; see https://sqlite.org/series.html.
package series

import (
	sqlite "gosqlite.org"
)

// ModuleName is the name the table-valued function registers under.
const ModuleName = "generate_series"

// Register installs the generate_series table-valued function on c.
//
// Per-connection registration. For pool-wide install, blank-import the auto
// sub-package:
//
//	import _ "gosqlite.org/ext/series/auto"
func Register(c *sqlite.Conn) error {
	return c.CreateEponymousModule(ModuleName, ctor)
}

// Column indices in the declared schema.
const (
	colValue = iota
	colStart
	colStop
	colStep
)

func ctor(c *sqlite.Conn, _, _, _ string, _ []string) (sqlite.VTab, error) {
	if err := c.DeclareVTab(
		`CREATE TABLE x(value INTEGER, start HIDDEN, stop HIDDEN, step HIDDEN)`); err != nil {
		return nil, err
	}
	return seriesTable{}, nil
}

type seriesTable struct{}

func (seriesTable) Disconnect() error { return nil }
func (seriesTable) Destroy() error    { return nil }

// BestIndex packs the argv position of each provided start/stop/step EQ
// constraint into a nibble of IdxNum (0 = absent, else argIndex+1).
func (seriesTable) BestIndex(info *sqlite.IndexInfo) error {
	var plan int64
	pos := 0
	for i, cst := range info.Constraints {
		if !cst.Usable || cst.Op != sqlite.OpEQ {
			continue
		}
		var shift uint
		switch cst.Column {
		case colStart:
			shift = 0
		case colStop:
			shift = 4
		case colStep:
			shift = 8
		default:
			continue
		}
		if plan&(0xf<<shift) != 0 {
			continue
		}
		info.Constraints[i].ArgIndex = pos
		info.Constraints[i].Omit = true
		plan |= int64(pos+1) << shift
		pos++
	}
	info.IdxNum = plan
	info.EstimatedCost = 1
	// start (nibble 0) and stop (nibble 1) are required; if either is absent
	// make this plan unattractive so the planner prefers one that supplies
	// them, and Filter reports a clear error if it's used anyway.
	if plan&0xf == 0 || plan&0xf0 == 0 {
		info.EstimatedCost = 1e18
	}
	return nil
}

func (seriesTable) Open() (sqlite.VTabCursor, error) { return &seriesCursor{}, nil }
