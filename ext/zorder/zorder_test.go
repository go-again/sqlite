package zorder_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/zorder"
)

func openWithZorder(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	sc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	if err := sc.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite.Conn)
		if !ok {
			return errors.New("not *sqlite.Conn")
		}
		return zorder.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestZorder_RoundTrip2D(t *testing.T) {
	_, sc := openWithZorder(t)
	ctx := context.Background()
	cases := []struct{ x, y int64 }{
		{0, 0}, {1, 0}, {0, 1}, {1, 1}, {7, 3}, {123, 456}, {0xFFFF, 0xAAAA},
	}
	for _, tc := range cases {
		var z, gx, gy int64
		if err := sc.QueryRowContext(ctx,
			`SELECT zorder(?, ?), unzorder(zorder(?, ?), 2, 0), unzorder(zorder(?, ?), 2, 1)`,
			tc.x, tc.y, tc.x, tc.y, tc.x, tc.y).Scan(&z, &gx, &gy); err != nil {
			t.Fatalf("(%d,%d): %v", tc.x, tc.y, err)
		}
		if gx != tc.x || gy != tc.y {
			t.Errorf("(%d,%d) → z=%d → (%d,%d)", tc.x, tc.y, z, gx, gy)
		}
	}
}

func TestZorder_RoundTrip3D(t *testing.T) {
	_, sc := openWithZorder(t)
	ctx := context.Background()
	x, y, z := int64(13), int64(42), int64(7)
	var got [3]int64
	if err := sc.QueryRowContext(ctx,
		`SELECT unzorder(zorder(?,?,?), 3, 0), unzorder(zorder(?,?,?), 3, 1), unzorder(zorder(?,?,?), 3, 2)`,
		x, y, z, x, y, z, x, y, z).Scan(&got[0], &got[1], &got[2]); err != nil {
		t.Fatal(err)
	}
	if got[0] != x || got[1] != y || got[2] != z {
		t.Errorf("3D round-trip lost data: in=(%d,%d,%d) out=%v", x, y, z, got)
	}
}

func TestZorder_BadArity(t *testing.T) {
	_, sc := openWithZorder(t)
	ctx := context.Background()
	if _, err := sc.ExecContext(ctx, `SELECT zorder(42)`); err == nil {
		t.Error("expected error for arity 1, got nil")
	}
}

func TestZorder_DimensionOverflow(t *testing.T) {
	// 2-D zorder gives each dimension 31 or 32 bits. A 1<<40 value
	// must trip the overflow check.
	_, sc := openWithZorder(t)
	_, err := sc.ExecContext(context.Background(), `SELECT zorder(1099511627776, 0)`)
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Errorf("got %v, want overflow error", err)
	}
}

func TestUnzorder_IndexOutOfRange(t *testing.T) {
	_, sc := openWithZorder(t)
	_, err := sc.ExecContext(context.Background(), `SELECT unzorder(0, 3, 9)`)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("got %v, want out-of-range error", err)
	}
}

func TestZorder_LocalityPreservation(t *testing.T) {
	// Adjacent 2D coords should produce small deltas in z space. Pin a
	// soft invariant: shifting one dimension by 1 changes z by at most
	// a few orders of magnitude (typically by 1 or 2 — the locality
	// property is the whole point of Morton encoding).
	_, sc := openWithZorder(t)
	ctx := context.Background()
	var a, b int64
	if err := sc.QueryRowContext(ctx, `SELECT zorder(100, 200), zorder(101, 200)`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	if diff := b - a; diff < 1 || diff > 1024 {
		t.Errorf("z-order locality broke: zorder(101,200) - zorder(100,200) = %d", diff)
	}
}
