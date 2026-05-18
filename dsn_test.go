// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"database/sql"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// TestTranslateMattnDSN_PragmaAliases verifies each mattn-style `_*` alias
// rewrites into the matching _pragma=… form that applyQueryParams understands.
func TestTranslateMattnDSN_PragmaAliases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // expected _pragma values, order-agnostic
	}{
		{"foreign_keys on", "_foreign_keys=on", []string{"foreign_keys(1)"}},
		{"fk alias", "_fk=true", []string{"foreign_keys(1)"}},
		{"busy_timeout numeric", "_busy_timeout=5000", []string{"busy_timeout(5000)"}},
		{"timeout alias", "_timeout=2500", []string{"busy_timeout(2500)"}},
		{"journal WAL", "_journal_mode=WAL", []string{"journal_mode(WAL)"}},
		{"journal alias", "_journal=MEMORY", []string{"journal_mode(MEMORY)"}},
		{"sync NORMAL", "_synchronous=NORMAL", []string{"synchronous(NORMAL)"}},
		{"sync alias 0", "_sync=0", []string{"synchronous(0)"}},
		{"locking exclusive", "_locking_mode=EXCLUSIVE", []string{"locking_mode(EXCLUSIVE)"}},
		{"secure_delete FAST", "_secure_delete=FAST", []string{"secure_delete(FAST)"}},
		{"recursive_triggers", "_recursive_triggers=yes", []string{"recursive_triggers(1)"}},
		{"rt alias", "_rt=no", []string{"recursive_triggers(0)"}},
		{"cache_size", "_cache_size=-2000", []string{"cache_size(-2000)"}},
		{"auto_vacuum FULL", "_auto_vacuum=FULL", []string{"auto_vacuum(FULL)"}},
		{"defer_fk on", "_defer_fk=on", []string{"defer_foreign_keys(1)"}},
		{"ignore_check", "_ignore_check_constraints=on", []string{"ignore_check_constraints(1)"}},
		{"cslike", "_cslike=true", []string{"case_sensitive_like(1)"}},
		{"query_only", "_query_only=1", []string{"query_only(1)"}},
		{"writable_schema", "_writable_schema=1", []string{"writable_schema(1)"}},
		{"combined", "_foreign_keys=on&_busy_timeout=1000",
			[]string{"foreign_keys(1)", "busy_timeout(1000)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := translateMattnDSN(tc.in)
			if err != nil {
				t.Fatalf("translate: %v", err)
			}
			q, err := url.ParseQuery(out)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := q["_pragma"]
			for _, want := range tc.want {
				if !slices.Contains(got, want) {
					t.Errorf("missing %q in %v", want, got)
				}
			}
		})
	}
}

func TestTranslateMattnDSN_LocAlias(t *testing.T) {
	out, err := translateMattnDSN("_loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(out)
	if got := q.Get("_timezone"); got != "UTC" {
		t.Errorf("_loc=UTC → _timezone=%q, want UTC", got)
	}

	out, err = translateMattnDSN("_loc=auto")
	if err != nil {
		t.Fatal(err)
	}
	q, _ = url.ParseQuery(out)
	if got := q.Get("_timezone"); got != "Local" {
		t.Errorf("_loc=auto → _timezone=%q, want Local", got)
	}
}

func TestTranslateMattnDSN_UserauthRejected(t *testing.T) {
	for _, k := range []string{"_auth", "_auth_user", "_auth_pass"} {
		_, err := translateMattnDSN(k + "=foo")
		if err == nil {
			t.Errorf("%s: expected error, got nil", k)
		} else if !strings.Contains(err.Error(), "userauth") {
			t.Errorf("%s: error %q does not mention userauth", k, err)
		}
	}
}

// TestTranslateMattnDSN_MutexHonest asserts _mutex behavior:
//   - "full" / boolean-true values are a no-op (we already open FULLMUTEX).
//   - "no" / boolean-false values return a clear error rather than silently
//     producing FULLMUTEX (the opposite of what the user asked for).
//   - Garbage values surface a "must be one of …" message.
func TestTranslateMattnDSN_MutexHonest(t *testing.T) {
	// "full" and boolean-true forms accepted and stripped.
	for _, v := range []string{"full", "true", "1", "on", "yes"} {
		out, err := translateMattnDSN("_mutex=" + v)
		if err != nil {
			t.Errorf("_mutex=%s should succeed, got %v", v, err)
		}
		if strings.Contains(out, "_mutex") {
			t.Errorf("_mutex=%s should be stripped from output, got %q", v, out)
		}
	}
	// "no" and boolean-false forms return a clear error.
	for _, v := range []string{"no", "false", "0", "off"} {
		_, err := translateMattnDSN("_mutex=" + v)
		if err == nil {
			t.Errorf("_mutex=%s should error", v)
			continue
		}
		if !strings.Contains(err.Error(), "NOMUTEX") {
			t.Errorf("_mutex=%s error %q should mention NOMUTEX", v, err)
		}
	}
	// Bogus value also errors.
	_, err := translateMattnDSN("_mutex=banana")
	if err == nil {
		t.Errorf("_mutex=banana should error")
	}
}

// TestTranslateMattnDSN_StmtCacheSize asserts the DSN-translation half of
// the contract: integer validation and strict-mode acceptance. The flag is
// preserved through to applyQueryParams so the *conn can read it (the
// cache lives on the conn, not on the parsed DSN).
func TestTranslateMattnDSN_StmtCacheSize(t *testing.T) {
	out, err := translateMattnDSN("_stmt_cache_size=100")
	if err != nil {
		t.Fatalf("valid integer: %v", err)
	}
	if !strings.Contains(out, "_stmt_cache_size=100") {
		t.Errorf("flag should be preserved for applyQueryParams; got %q", out)
	}

	if _, err := translateMattnDSN("_stmt_cache_size=banana"); err == nil {
		t.Errorf("non-integer should error")
	}

	// strict mode accepts _stmt_cache_size silently.
	if _, err := translateMattnDSN("_stmt_cache_size=10&_strict=1"); err != nil {
		t.Errorf("strict mode should accept stmt cache size: %v", err)
	}
}

func TestTranslateMattnDSN_StrictMode(t *testing.T) {
	_, err := translateMattnDSN("_strict=1&_unknown_flag=foo")
	if err == nil {
		t.Fatal("expected error in strict mode")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q does not mention unknown flag", err)
	}
	// Without strict, unknown flags should pass through silently.
	_, err = translateMattnDSN("_unknown_flag=foo")
	if err != nil {
		t.Errorf("non-strict mode should accept unknown flags, got %v", err)
	}
}

func TestDSN_ForeignKeysApplied(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var v int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("foreign_keys=%d, want 1", v)
	}
}

func TestDSN_BusyTimeoutApplied(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:?_busy_timeout=3000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var v int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 3000 {
		t.Errorf("busy_timeout=%d, want 3000", v)
	}
}

func TestDSN_JournalModeWAL(t *testing.T) {
	// WAL needs a file-backed DB to take effect.
	dir := t.TempDir()
	dsn := "file:" + dir + "/x.db?_journal_mode=WAL"
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var v string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(v, "wal") {
		t.Errorf("journal_mode=%q, want wal", v)
	}
}

func TestDSN_MultiplePragmas(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:?_foreign_keys=on&_busy_timeout=1000&_synchronous=NORMAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	checks := []struct {
		pragma string
		want   string
	}{
		{"foreign_keys", "1"},
		{"busy_timeout", "1000"},
		{"synchronous", "1"}, // NORMAL = 1
	}
	for _, c := range checks {
		var v string
		if err := db.QueryRow("PRAGMA " + c.pragma).Scan(&v); err != nil {
			t.Errorf("%s: %v", c.pragma, err)
			continue
		}
		if v != c.want {
			t.Errorf("PRAGMA %s = %q, want %q", c.pragma, v, c.want)
		}
	}
}
