package ipaddr_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/ipaddr"
)

func openDB(t *testing.T) (*sql.DB, *sql.Conn) {
	t.Helper()
	db, err := sql.Open(sqlite.DriverName, ":memory:")
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
		return ipaddr.Register(c)
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return db, sc
}

func TestIPContains(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		prefix, ip string
		want       bool
	}{
		{"10.0.0.0/8", "10.1.2.3", true},
		{"10.0.0.0/8", "11.1.2.3", false},
		{"192.168.1.0/24", "192.168.1.42", true},
		{"192.168.1.0/24", "192.168.2.1", false},
		{"::1/128", "::1", true},
		{"2001:db8::/32", "2001:db8:1:2::1", true},
		{"2001:db8::/32", "2002::1", false},
	}
	for _, tc := range cases {
		var got bool
		if err := sc.QueryRowContext(ctx,
			`SELECT ipcontains(?, ?)`, tc.prefix, tc.ip).Scan(&got); err != nil {
			t.Fatalf("(%s,%s): %v", tc.prefix, tc.ip, err)
		}
		if got != tc.want {
			t.Errorf("ipcontains(%q,%q)=%v, want %v", tc.prefix, tc.ip, got, tc.want)
		}
	}
}

// TestIPOverlaps_NotSelfReferential pins the upstream bug fix: ncruces
// ipoverlaps parses arg[0] twice. With that bug, ipoverlaps(A, B) always
// returns ipoverlaps(A, A) = true. The fixed version returns the right
// answer for a non-overlapping pair.
func TestIPOverlaps_NotSelfReferential(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		a, b string
		want bool
	}{
		{"10.0.0.0/8", "10.1.0.0/16", true},
		{"10.0.0.0/8", "192.168.0.0/16", false}, // ← would be true under the upstream bug
		{"192.168.0.0/16", "192.168.1.0/24", true},
		{"2001:db8::/32", "2001:db9::/32", false},
	}
	for _, tc := range cases {
		var got bool
		if err := sc.QueryRowContext(ctx, `SELECT ipoverlaps(?, ?)`, tc.a, tc.b).Scan(&got); err != nil {
			t.Fatalf("(%s,%s): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("ipoverlaps(%q,%q)=%v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIPFamily(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	cases := []struct {
		in   string
		want int64
	}{
		{"10.0.0.1", 4},
		{"192.168.1.0/24", 4},
		{"::1", 6},
		{"2001:db8::/32", 6},
		{"[::1]:8080", 6},
	}
	for _, tc := range cases {
		var got int64
		if err := sc.QueryRowContext(ctx, `SELECT ipfamily(?)`, tc.in).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ipfamily(%q)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestIPHost(t *testing.T) {
	_, sc := openDB(t)
	cases := []struct {
		in, want string
	}{
		{"10.0.0.1", "10.0.0.1"},
		{"192.168.1.0/24", "192.168.1.0"},
		{"10.0.0.5:8080", "10.0.0.5"},
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(context.Background(),
			`SELECT iphost(?)`, tc.in).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("iphost(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIPMaskLen(t *testing.T) {
	_, sc := openDB(t)
	cases := []struct {
		in   string
		want int64
	}{
		{"10.0.0.0/8", 8},
		{"192.168.1.0/24", 24},
		{"::1/128", 128},
		{"2001:db8::/32", 32},
	}
	for _, tc := range cases {
		var got int64
		if err := sc.QueryRowContext(context.Background(),
			`SELECT ipmasklen(?)`, tc.in).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ipmasklen(%q)=%d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestIPNetwork(t *testing.T) {
	_, sc := openDB(t)
	cases := []struct {
		in, want string
	}{
		{"10.1.2.3/8", "10.0.0.0/8"},
		{"192.168.1.42/24", "192.168.1.0/24"},
		{"2001:db8:1::1/32", "2001:db8::/32"},
	}
	for _, tc := range cases {
		var got string
		if err := sc.QueryRowContext(context.Background(),
			`SELECT ipnetwork(?)`, tc.in).Scan(&got); err != nil {
			t.Fatalf("(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ipnetwork(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIP_BadInputSurfaces(t *testing.T) {
	_, sc := openDB(t)
	cases := []string{
		`SELECT ipcontains('not-a-prefix', '10.0.0.1')`,
		`SELECT ipfamily('garbage')`,
		`SELECT ipnetwork('not-a-prefix')`,
	}
	for _, q := range cases {
		if _, err := sc.ExecContext(context.Background(), q); err == nil ||
			!strings.Contains(err.Error(), "ip") {
			t.Errorf("%q: got %v, want error mentioning ip*", q, err)
		}
	}
}
