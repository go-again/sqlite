package sqlite // import "gosqlite.org"

import (
	"errors"
	"strings"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// collationNeeded maps a minted id to a Go collation-needed handler. The id is
// passed to SQLite as the callback's pArg; the conn that registered it tracks
// the id and drops it on close (dropHookHandlers), so nothing leaks under
// per-connection ConnectHook registration.
var collationNeeded = newCallbackTable[func(*Conn, string)]()

// CollationNeeded registers fn to be invoked when a statement on this
// connection references a collating sequence that has not been defined. fn is
// expected to define it — typically by calling [Conn.RegisterCollation] with
// the same name — after which SQLite retries the lookup. If fn does not define
// it, the original "no such collation sequence" error stands.
//
// This is the lazy companion to [Conn.RegisterCollation]: register collations
// on demand (e.g. resolving a locale only when a query first needs it) instead
// of eagerly up front. Like the other hooks it is per-connection, so pin the
// pool (see internal/testhelp.OpenPinned) when you need the handler on the
// connection a later query uses.
//
// https://sqlite.org/c3ref/collation_needed.html
func (c *Conn) CollationNeeded(fn func(conn *Conn, name string)) error {
	if fn == nil {
		return errors.New("sqlite: CollationNeeded: nil function")
	}
	id := collationNeeded.register(fn)
	rc := sqlite3.Xsqlite3_collation_needed(c.tls, c.db, id, cFuncPointer(collationNeededTrampoline))
	if rc != sqlite3.SQLITE_OK {
		collationNeeded.drop(id)
		return c.errstr(rc)
	}
	c.collationNeededIDs = append(c.collationNeededIDs, id)
	return nil
}

// AnyCollationNeeded satisfies every unknown collation by defining it, the
// first time it is referenced, as a byte-wise (BINARY-equivalent) comparator.
// Use it to open / ATTACH / restore / inspect a foreign schema that names
// collations this process does not implement — DDL and queries then succeed
// instead of failing with "no such collation sequence".
//
// NOTE: affected columns sort byte-wise, not by the collation's real
// (e.g. locale-aware) order. When ordering fidelity matters, use
// [Conn.CollationNeeded] with a real comparator instead.
func (c *Conn) AnyCollationNeeded() error {
	return c.CollationNeeded(func(conn *Conn, name string) {
		// strings.Compare is byte-wise over the UTF-8 bytes, i.e. SQLite BINARY.
		_ = conn.RegisterCollation(name, func(a, b string) int { return strings.Compare(a, b) })
	})
}

// collationNeededTrampoline is the C xCollNeeded callback. Its signature is
// void(*)(void* pArg, sqlite3* db, int eTextRep, const char* zName); we ignore
// eTextRep (the handler registers a UTF-8 collation regardless).
func collationNeededTrampoline(tls *libc.TLS, pArg uintptr, db uintptr, eTextRep int32, zName uintptr) {
	fn, ok := collationNeeded.lookup(pArg)
	if !ok {
		return
	}
	c := connForDB(db)
	if c == nil {
		return
	}
	fn(c, libc.GoString(zName))
}
