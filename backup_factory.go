package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqlite3 "modernc.org/sqlite/lib"
)

// Backup creates a backup-handle that copies the database identified by
// srcSchema on src into the database identified by destSchema on the receiver.
// Step the returned *Backup to make progress and call Finish (or Close) to
// release resources.
//
// Mattn-compatibility API: equivalent to SQLiteConn.Backup, which copies
// schema "main" by default but accepts any attached schema name.
//
//	// Typical mattn pattern: backup live DB to a new on-disk file.
//	destDB, _ := sql.Open("sqlite3", "backup.db")
//	destConn, _ := destDB.Conn(ctx)
//	destConn.Raw(func(dc any) error {
//	    dst := dc.(*sqlite.Conn)
//	    bk, err := dst.Backup("main", src, "main")
//	    if err != nil { return err }
//	    for {
//	        done, err := bk.Step(100)
//	        if err != nil { return err }
//	        if !done { break }
//	    }
//	    return bk.Finish()
//	})
func (dst *Conn) Backup(destSchema string, src *Conn, srcSchema string) (*Backup, error) {
	if src == nil {
		return nil, errors.New("Backup: src conn is nil")
	}
	cDest, err := allocCStringOrMain(dst, destSchema)
	if err != nil {
		return nil, err
	}
	defer freeIfNonZero(dst, cDest)

	cSrc, err := allocCStringOrMain(src, srcSchema)
	if err != nil {
		return nil, err
	}
	defer freeIfNonZero(src, cSrc)

	pBackup := sqlite3.Xsqlite3_backup_init(dst.tls, dst.db, cDest, src.db, cSrc)
	if pBackup == 0 {
		rc := sqlite3.Xsqlite3_errcode(dst.tls, dst.db)
		return nil, errstrForDB(dst.tls, rc, dst.db)
	}
	// In the mattn-style API the source connection owns Step's TLS (see
	// Backup.Step in backup.go which uses srcConn.tls), and the destination
	// connection is the live target whose lifecycle the caller manages — not
	// ours to close. The dstConn field is unused on this path; we keep it nil
	// and only set srcConn so Finish/Close release resources without touching
	// the caller's connection.
	return &Backup{srcConn: src, dstConn: nil, pBackup: pBackup}, nil
}

// Remaining returns the number of pages still to be copied at the most recent
// completed Step. Returns 0 when the backup is complete.
//
// Mattn-compatibility API.
func (b *Backup) Remaining() int {
	if b.pBackup == 0 || b.srcConn == nil {
		return 0
	}
	return int(sqlite3.Xsqlite3_backup_remaining(b.srcConn.tls, b.pBackup))
}

// PageCount returns the total number of pages in the source database at the
// most recent completed Step.
//
// Mattn-compatibility API.
func (b *Backup) PageCount() int {
	if b.pBackup == 0 || b.srcConn == nil {
		return 0
	}
	return int(sqlite3.Xsqlite3_backup_pagecount(b.srcConn.tls, b.pBackup))
}

// Close releases all resources associated with the Backup. It is equivalent to
// Finish but the dstConn (if any) is not implicitly closed — mirroring the way
// mattn's SQLiteBackup.Close behaves.
//
// On the factory-built backups (those returned by (*Conn).Backup), dstConn is
// nil so Close behaves identically to Finish minus the dst.Close() call.
func (b *Backup) Close() error {
	if b.pBackup == 0 || b.srcConn == nil {
		return nil
	}
	rc := sqlite3.Xsqlite3_backup_finish(b.srcConn.tls, b.pBackup)
	b.pBackup = 0
	if rc == sqlite3.SQLITE_OK {
		return nil
	}
	return b.srcConn.errstr(rc)
}

// SerializeSchema returns a serialization of the schema named on this
// connection. Pass schema = "main" or empty for the main database.
//
// Convenience wrapper around the existing *conn.Serialize method that hides
// the schema argument from callers who only need the default.
func (c *Conn) SerializeSchema(schema string) ([]byte, error) {
	if schema == "" || schema == "main" {
		return c.Serialize()
	}
	// modernc's Serialize always reads schema 0 (main). For other schemas we'd
	// need to invoke sqlite3_serialize with the matching zSchema. Not commonly
	// used; return a clear error rather than silently returning the main DB.
	return nil, fmt.Errorf("SerializeSchema: only %q schema is currently supported", "main")
}

// Serialize is a top-level helper that calls (*Conn).Serialize on a fresh pool
// connection borrowed from db. It is the simplest way to dump an in-memory DB
// to bytes without manually using db.Conn().Raw(...).
func Serialize(ctx context.Context, db *sql.DB) ([]byte, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var data []byte
	err = conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*Conn)
		if !ok {
			return fmt.Errorf("Serialize: driver conn is %T, not *sqlite.Conn", driverConn)
		}
		var serErr error
		data, serErr = c.Serialize()
		return serErr
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Deserialize is a top-level helper that loads buf into the main database of a
// pool connection borrowed from db. Note: after Deserialize, subsequent pool
// connections see the new content only for the same physical connection — for
// most callers, set db.SetMaxOpenConns(1) before calling Deserialize so the
// loaded image is the only conn the pool ever returns.
func Deserialize(ctx context.Context, db *sql.DB, buf []byte) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*Conn)
		if !ok {
			return fmt.Errorf("Deserialize: driver conn is %T, not *sqlite.Conn", driverConn)
		}
		return c.Deserialize(buf)
	})
}
