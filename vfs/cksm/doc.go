// Package cksm provides a corruption-detection VFS for SQLite. Every
// main-database page write computes an 8-byte checksum over the first
// (pageSize-8) bytes and stamps it into the page's reserved trailer;
// every read verifies the trailer. A mismatch surfaces as
// SQLITE_IOERR_DATA on the offending read, so corrupted databases fail
// fast instead of silently returning wrong rows.
//
// # When to use cksm
//
// SQLite already detects torn writes within a single transaction via
// the journal/WAL mechanism, but it does not by itself detect
// bit-rot — silent data corruption between commits — caused by failing
// disks, RAID controllers, filesystem bugs, or cosmic rays. cksm closes
// that gap by attaching a Fletcher-style checksum to every page.
//
// Composition note: cksm wraps the default OS-level VFS by intercepting
// xRead / xWrite at the page boundary. To stack with
// [github.com/go-again/sqlite/vfs/crypto] for checksummed-then-encrypted
// storage, register cksm first and pass its name as the crypto VFS's
// [Options.WrapVFS]; cksm itself also accepts an [Options.WrapVFS] when
// callers want to layer it on top of some other registered VFS instead
// of the system default.
//
// # On-disk format
//
// Each page's last 8 bytes hold the checksum: 32-bit Fletcher-style
// rolling sum (s1, s2) over the page's first (pageSize-8) bytes, taken
// as little-endian 32-bit words. This is the same algorithm the
// upstream SQLite cksumvfs extension uses, so a cksm database written
// here is on-disk compatible with vanilla SQLite + cksm.
//
// SQLite reserves the trailer space via the SQLite header's
// reserved_bytes byte (offset 20 of the 100-byte header). cksm enables
// itself automatically whenever the database header reports
// reserved_bytes == 8. Fresh databases can opt in via
//
//	PRAGMA reserved_bytes = 8;
//
// before any tables are created. Existing databases need a `VACUUM`
// after setting the PRAGMA to rewrite every page with the trailer in
// place.
//
// # Quick start
//
//	name, fs, err := cksm.New(cksm.Options{})
//	if err != nil { return err }
//	defer fs.Close()
//
//	db, _ := sql.Open("sqlite", "/path/to/db?vfs="+name)
//	defer db.Close()
//
//	// First-run setup: enable the trailer on new DBs. EnableChecksums
//	// flips PRAGMA reserved_bytes=8 and VACUUMs in a single conn-
//	// pinned call. See examples/vfs-cksm/main.go for the full recipe.
//	sc, _ := db.Conn(ctx)
//	defer sc.Close()
//	_ = sc.Raw(func(dc any) error {
//	    return dc.(*sqlite.Conn).EnableChecksums("main")
//	})
//
// # Limitations
//
//   - Detection only. cksm is not a recovery mechanism — a failed
//     verify aborts the read with SQLITE_IOERR_DATA. Pair with backups
//     or a WAL archive for recovery.
//   - 8 bytes of overhead per page (about 0.2% at the default 4 KiB
//     page size). The Fletcher rolling sum runs in tight 8-byte loops
//     and is dwarfed by I/O cost.
//   - Main DB pages only. Journal and WAL pages do not get the trailer
//     because SQLite already tracks per-frame integrity for those
//     formats. Sub-page reads of main DB pages also bypass verification
//     (gate at iomethods.go's xRead is `amt == pageSize`); SQLite
//     normally issues full-page reads, but any sub-page probe slips
//     through unverified by design.
//   - Not authenticated. Fletcher rolling sums detect bit-flips
//     and torn writes; they are not adversarial MACs. An attacker
//     who can write the file bytes can forge a matching trailer.
//     Composing with `vfs/crypto` does NOT close this gap — crypto
//     adds confidentiality only, not authentication.
package cksm
