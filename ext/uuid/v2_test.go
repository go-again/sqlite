package uuid_test

import (
	"context"
	"testing"

	gid "github.com/google/uuid"
)

// TestUUID_V2DCESecurity covers the DCE Security (version 2) variant:
// generation from an explicit (domain, id), the version stamp, and the
// domain/id extractors that round-trip the inputs.
func TestUUID_V2DCESecurity(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()

	var s string
	if err := sc.QueryRowContext(ctx, `SELECT uuid(2, 'person', 1000)`).Scan(&s); err != nil {
		t.Fatalf("uuid(2,'person',1000): %v", err)
	}
	u, err := gid.Parse(s)
	if err != nil {
		t.Fatalf("not parseable: %v", err)
	}
	if u.Version() != 2 {
		t.Errorf("version = %d, want 2", u.Version())
	}

	var ver int64
	if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_version(?)`, s).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Errorf("uuid_extract_version = %d, want 2", ver)
	}

	var dom string
	if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_domain(?)`, s).Scan(&dom); err != nil {
		t.Fatal(err)
	}
	if dom != "person" {
		t.Errorf("uuid_extract_domain = %q, want person", dom)
	}

	var id int64
	if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_id(?)`, s).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 1000 {
		t.Errorf("uuid_extract_id = %d, want 1000", id)
	}
}

func TestUUID_V2NumericDomainAndGroups(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()

	// Numeric domain code (1 == group) and string ('group') must agree.
	for _, dq := range []string{`uuid(2, 1, 42)`, `uuid(2, 'group', 42)`} {
		var s string
		if err := sc.QueryRowContext(ctx, `SELECT `+dq).Scan(&s); err != nil {
			t.Fatalf("%s: %v", dq, err)
		}
		var dom string
		var id int64
		if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_domain(?), uuid_extract_id(?)`, s, s).Scan(&dom, &id); err != nil {
			t.Fatal(err)
		}
		if dom != "group" || id != 42 {
			t.Errorf("%s → domain=%q id=%d, want group/42", dq, dom, id)
		}
	}
}

func TestUUID_DomainNullForOtherVersions(t *testing.T) {
	_, sc := openDB(t)
	ctx := context.Background()
	// A v4 UUID has no domain/id — the extractors return NULL.
	var v4 string
	if err := sc.QueryRowContext(ctx, `SELECT uuid(4)`).Scan(&v4); err != nil {
		t.Fatal(err)
	}
	var dom, id any
	if err := sc.QueryRowContext(ctx, `SELECT uuid_extract_domain(?), uuid_extract_id(?)`, v4, v4).Scan(&dom, &id); err != nil {
		t.Fatal(err)
	}
	if dom != nil || id != nil {
		t.Errorf("v4 domain/id = %v/%v, want NULL/NULL", dom, id)
	}
}

func TestUUID_V2BadDomain(t *testing.T) {
	_, sc := openDB(t)
	var s string
	if err := sc.QueryRowContext(context.Background(), `SELECT uuid(2, 'nope', 1)`).Scan(&s); err == nil {
		t.Error("uuid(2,'nope',1) accepted an invalid domain")
	}
}
