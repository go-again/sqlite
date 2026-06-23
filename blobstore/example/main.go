// blobstore example: store a large, growable byte object in SQLite without
// ever holding the whole thing in memory. blobstore manages a chunk table and
// hands you an io.WriterAt / io.ReaderAt per object — the supported answer to
// "put a file in SQLite" (incremental BLOB I/O is fixed-size on its own, and
// `col || zeroblob(delta)` silently truncates).
//
// Run from the blobstore module:
//
//	cd blobstore && go run ./example
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	sqlite "gosqlite.org"
	"gosqlite.org/blobstore"
)

func main() {
	// A file-backed (or OpenShared) database: blobstore borrows a pooled
	// connection per operation, so every connection must see the same store.
	// A private OpenInMemory() would NOT work here.
	dir, err := os.MkdirTemp("", "blobstore-example")
	if err != nil {
		log.Fatalf("TempDir: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := sqlite.OpenWAL(filepath.Join(dir, "files.db"))
	if err != nil {
		log.Fatalf("OpenWAL: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	store, err := blobstore.Open(db, "files", blobstore.WithChunkSize(4<<10))
	if err != nil {
		log.Fatalf("blobstore.Open: %v", err)
	}

	// A new, empty object.
	id, err := store.Create(ctx)
	if err != nil {
		log.Fatalf("Create: %v", err)
	}
	fmt.Printf("created object id=%d\n", id)

	// Stream content in — packets may arrive out of order; offsets are
	// addressed directly, never buffering the whole object.
	w, err := store.Writer(ctx, id)
	if err != nil {
		log.Fatalf("Writer: %v", err)
	}
	body := []byte("The quick brown fox jumps over the lazy dog.\n")
	header := []byte("== blobstore demo ==\n")
	// Write the body first (at the offset it will end up), then the header.
	if _, err := w.WriteAt(body, int64(len(header))); err != nil {
		log.Fatalf("WriteAt body: %v", err)
	}
	if _, err := w.WriteAt(header, 0); err != nil {
		log.Fatalf("WriteAt header: %v", err)
	}
	w.Close()

	size, _ := store.Size(ctx, id)
	fmt.Printf("logical size=%d bytes\n", size)

	// Read a slice without materializing the whole object.
	r, err := store.Reader(ctx, id)
	if err != nil {
		log.Fatalf("Reader: %v", err)
	}
	slice := make([]byte, len(header))
	if _, err := r.ReadAt(slice, 0); err != nil && err != io.EOF {
		log.Fatalf("ReadAt: %v", err)
	}
	fmt.Printf("header slice: %q\n", slice)

	// Or stream the whole thing out via io.Copy + a SectionReader.
	full, err := io.ReadAll(io.NewSectionReader(r, 0, size))
	if err != nil {
		log.Fatalf("ReadAll: %v", err)
	}
	r.Close()
	fmt.Printf("full content:\n%s", full)

	// Truncate (shrink) — drops the tail, keeps the header line.
	if err := store.Truncate(ctx, id, int64(len(header))); err != nil {
		log.Fatalf("Truncate: %v", err)
	}
	size, _ = store.Size(ctx, id)
	fmt.Printf("after truncate, size=%d\n", size)

	// Delete frees the blocks the object alone held.
	if err := store.Delete(ctx, id); err != nil {
		log.Fatalf("Delete: %v", err)
	}
	if _, err := store.Size(ctx, id); err != nil {
		fmt.Printf("after delete, Size reports: %v\n", err)
	}

	// --- Compression: same API, objects stored compressed. ---
	cstore, err := blobstore.Open(db, "compressed",
		blobstore.WithCompression(blobstore.CompressionDefault))
	if err != nil {
		log.Fatalf("Open compressed: %v", err)
	}
	cid, err := cstore.Create(ctx)
	if err != nil {
		log.Fatalf("Create compressed: %v", err)
	}
	doc := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 4096) // ~180 KB, very compressible
	cw, _ := cstore.Writer(ctx, cid)
	if _, err := cw.WriteAt(doc, 0); err != nil {
		log.Fatalf("compressed WriteAt: %v", err)
	}
	cw.Close()
	csize, _ := cstore.Size(ctx, cid)

	// The logical size is the full payload; the bytes on disk are far fewer.
	var stored int64
	_ = db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(length(data)), 0) FROM compressed_chunks WHERE obj = ?`, cid).Scan(&stored)
	fmt.Printf("compressed object: logical=%d bytes, stored=%d bytes (%.1fx)\n",
		csize, stored, float64(csize)/float64(stored))
}
