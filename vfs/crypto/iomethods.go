package crypto

import (
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// scratchPool reuses encrypt/decrypt scratch buffers across calls.
// xRead and xWrite allocate pageSize-multiples per invocation; on a
// hot SELECT path that's a measurable GC source. We pool by capacity
// rather than by exact size — most reads are a single page, so the
// pool effectively caches one pageSize-sized buffer per goroutine.
//
// The pool stores *[]byte to keep the type alloc-free; clearing the
// length before put lets the next caller resize without resetting
// elements (encryption overwrites anyway).
var scratchPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 4096)
		return &b
	},
}

func getScratch(n int64) *[]byte {
	bp := scratchPool.Get().(*[]byte)
	if int64(cap(*bp)) < n {
		// Pool's buffer was too small. Return it to the pool so the
		// next caller can reuse it instead of dropping it on the
		// floor — otherwise repeated grow-paths drain the pool.
		scratchPool.Put(bp)
		buf := make([]byte, n)
		return &buf
	}
	*bp = (*bp)[:n]
	return bp
}

func putScratch(bp *[]byte) {
	*bp = (*bp)[:0]
	scratchPool.Put(bp)
}

// iomethods.go — the wrapping io-method trampolines.
//
// Encryption-flavored methods (xRead, xWrite, xTruncate) consult the
// per-file fsToken: if zero, the file is unencrypted (Phase 2 will
// extend coverage) and the call forwards 1:1 to the default methods.
// All other methods always forward unchanged — locking, sync, file
// control, sector size are orthogonal to crypto.

func defaultMethodsFor(pFile uintptr) *sqlite3.Tsqlite3_io_methods {
	return (*sqlite3.Tsqlite3_io_methods)(unsafe.Pointer(perFileStateOf(pFile).defaultMethods))
}

func xCloseTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXClose(tls, defaultMethodsFor(pFile).FxClose, pFile)
}

// xReadTrampoline reads `amt` bytes at `off` into `buf`. For
// encrypted files we round the request out to the enclosing page
// span, fetch and decrypt those pages, then copy the requested slice
// to the caller.
func xReadTrampoline(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	fs := fsFor(pFile)
	if fs == nil {
		return callXRead(tls, defaultMethodsFor(pFile).FxRead, pFile, buf, amt, off)
	}
	var start time.Time
	if fs.recorder != nil {
		start = time.Now()
	}
	rc := readEncrypted(tls, pFile, buf, amt, off, fs)
	if fs.recorder != nil {
		fs.recorder.OnRead(perFileStateOf(pFile).fileKind, int64(off), int64(amt), time.Since(start), rc)
	}
	return rc
}

// xWriteTrampoline writes `amt` bytes from `buf` at `off`. For
// encrypted files we encrypt each affected page (read-modify-write
// for partial pages) and forward the page-aligned encrypted block.
func xWriteTrampoline(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	fs := fsFor(pFile)
	if fs == nil {
		return callXWrite(tls, defaultMethodsFor(pFile).FxWrite, pFile, buf, amt, off)
	}
	var start time.Time
	if fs.recorder != nil {
		start = time.Now()
	}
	rc := writeEncrypted(tls, pFile, buf, amt, off, fs)
	if fs.recorder != nil {
		fs.recorder.OnWrite(perFileStateOf(pFile).fileKind, int64(off), int64(amt), time.Since(start), rc)
	}
	return rc
}

func xTruncateTrampoline(tls *libc.TLS, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	// Truncate forwards directly: the cipher is length-preserving so
	// truncating to a page boundary leaves valid encrypted pages
	// behind. Truncating mid-page is a SQLite-engine concern, not
	// ours.
	return callXTruncate(tls, defaultMethodsFor(pFile).FxTruncate, pFile, size)
}

func xSyncTrampoline(tls *libc.TLS, pFile uintptr, flags int32) int32 {
	fs := fsFor(pFile)
	if fs == nil || fs.recorder == nil {
		return callXSync(tls, defaultMethodsFor(pFile).FxSync, pFile, flags)
	}
	start := time.Now()
	rc := callXSync(tls, defaultMethodsFor(pFile).FxSync, pFile, flags)
	fs.recorder.OnSync(perFileStateOf(pFile).fileKind, time.Since(start), rc)
	return rc
}

func xFileSizeTrampoline(tls *libc.TLS, pFile, pSize uintptr) int32 {
	return callXFileSize(tls, defaultMethodsFor(pFile).FxFileSize, pFile, pSize)
}

func xLockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return callXLock(tls, defaultMethodsFor(pFile).FxLock, pFile, level)
}

func xUnlockTrampoline(tls *libc.TLS, pFile uintptr, level int32) int32 {
	return callXLock(tls, defaultMethodsFor(pFile).FxUnlock, pFile, level)
}

func xCheckReservedLockTrampoline(tls *libc.TLS, pFile, pResOut uintptr) int32 {
	return callXFileSize(tls, defaultMethodsFor(pFile).FxCheckReservedLock, pFile, pResOut)
}

func xFileControlTrampoline(tls *libc.TLS, pFile uintptr, op int32, pArg uintptr) int32 {
	return callXFileControl(tls, defaultMethodsFor(pFile).FxFileControl, pFile, op, pArg)
}

func xSectorSizeTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXSectorSize(tls, defaultMethodsFor(pFile).FxSectorSize, pFile)
}

func xDeviceCharacteristicsTrampoline(tls *libc.TLS, pFile uintptr) int32 {
	return callXSectorSize(tls, defaultMethodsFor(pFile).FxDeviceCharacteristics, pFile)
}

// --- Page-level encryption helpers ---

// readEncrypted reads decrypted bytes from offset for an encrypted
// file. Algorithm:
//  1. Compute the enclosing page span [pageStart, pageEnd).
//  2. Read those pages from disk via the default xRead.
//  3. Decrypt each page in place with tweak = (file kind, page number).
//  4. Copy the requested slice from the decrypted span to caller buf.
//
// Short-read handling: if the on-disk file is smaller than the
// requested page span (common during DB open before the first page
// has been written), zero-fill the missing bytes and return
// SQLITE_IOERR_SHORT_READ to mirror the default VFS contract — SQLite
// uses the SHORT_READ signal to decide a database is fresh.
func readEncrypted(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64, fs *FS) int32 {
	pst := perFileStateOf(pFile)
	ps := int64(fs.pageSize)
	pageStart := (int64(off) / ps) * ps
	pageEnd := (int64(off) + int64(amt) + ps - 1) / ps * ps
	span := pageEnd - pageStart

	bp := getScratch(span)
	defer putScratch(bp)
	scratch := *bp
	scratchPtr := uintptr(unsafe.Pointer(&scratch[0]))
	rc := callXRead(tls, defaultMethodsFor(pFile).FxRead, pFile, scratchPtr, int32(span), sqlite3.Tsqlite3_int64(pageStart))

	if rc == sqlite3.SQLITE_OK {
		decryptSpan(fs, scratch, pageStart, ps, pst.fileKind)
	} else if rc == sqlite3.SQLITE_IOERR_SHORT_READ {
		// On a short read, the default zero-filled the tail. Decrypt
		// whatever full pages we got; leave the trailing partial
		// page as zeros so SQLite sees the SHORT_READ semantics it
		// expects for a half-initialized file.
		fullPages := len(scratch) / int(ps)
		decryptSpan(fs, scratch[:fullPages*int(ps)], pageStart, ps, pst.fileKind)
	} else {
		return rc
	}

	copyOff := int64(off) - pageStart
	dst := (*libc.RawMem)(unsafe.Pointer(buf))[:amt]
	copy(dst, scratch[copyOff:copyOff+int64(amt)])
	return rc
}

// writeEncrypted writes encrypted bytes from buf for an encrypted
// main-DB file. Algorithm:
//  1. Compute the enclosing page span [pageStart, pageEnd).
//  2. If the write is fully page-aligned, encrypt each page from buf
//     into a scratch span and forward unchanged-shape to default
//     xWrite.
//  3. If the write is partial (start or end mid-page), fetch the
//     affected pages from disk, decrypt them, splice the new bytes
//     into the decrypted span, re-encrypt, write back.
//
// SQLite's main-DB writes are almost always whole-page-aligned, so
// the fast path is the common case; the RMW branch covers vacuum
// quirks and any other mid-page DML the engine emits.
func writeEncrypted(tls *libc.TLS, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64, fs *FS) int32 {
	pst := perFileStateOf(pFile)
	ps := int64(fs.pageSize)
	pageStart := (int64(off) / ps) * ps
	pageEnd := (int64(off) + int64(amt) + ps - 1) / ps * ps
	span := pageEnd - pageStart

	bp := getScratch(span)
	defer putScratch(bp)
	scratch := *bp
	srcSlice := (*libc.RawMem)(unsafe.Pointer(buf))[:amt:amt]

	if int64(off) == pageStart && int64(amt) == span {
		// Fast path: full page-aligned span. No need to fetch
		// existing pages; the new bytes overwrite them entirely.
		copy(scratch, srcSlice)
	} else {
		// RMW: fetch existing pages, decrypt, splice in the new
		// bytes. A short read leaves the unwritten tail zeroed, which
		// is correct for first-write-to-uninitialized-page.
		readPtr := uintptr(unsafe.Pointer(&scratch[0]))
		rc := callXRead(tls, defaultMethodsFor(pFile).FxRead, pFile, readPtr, int32(span), sqlite3.Tsqlite3_int64(pageStart))
		if rc != sqlite3.SQLITE_OK && rc != sqlite3.SQLITE_IOERR_SHORT_READ {
			return rc
		}
		if rc == sqlite3.SQLITE_OK {
			decryptSpan(fs, scratch, pageStart, ps, pst.fileKind)
		} else {
			// SHORT_READ: only decrypt the full pages we got.
			fullBytes := (len(scratch) / int(ps)) * int(ps)
			decryptSpan(fs, scratch[:fullBytes], pageStart, ps, pst.fileKind)
		}
		copy(scratch[int64(off)-pageStart:], srcSlice)
	}

	encryptSpan(fs, scratch, pageStart, ps, pst.fileKind)
	scratchPtr := uintptr(unsafe.Pointer(&scratch[0]))
	return callXWrite(tls, defaultMethodsFor(pFile).FxWrite, pFile, scratchPtr, int32(span), sqlite3.Tsqlite3_int64(pageStart))
}

func encryptSpan(fs *FS, span []byte, baseOffset int64, pageSize int64, kind byte) {
	for i := int64(0); i < int64(len(span)); i += pageSize {
		pageNum := uint64((baseOffset + i) / pageSize)
		fs.cipher.encrypt(span[i:i+pageSize], pageNum, kind)
	}
}

func decryptSpan(fs *FS, span []byte, baseOffset int64, pageSize int64, kind byte) {
	for i := int64(0); i < int64(len(span)); i += pageSize {
		pageNum := uint64((baseOffset + i) / pageSize)
		fs.cipher.decrypt(span[i:i+pageSize], pageNum, kind)
	}
}

// --- Consumer-side function-pointer casts ---
//
// asFunc converts a uintptr (which the transpiled SQLite C code uses
// to store a function pointer in struct slots like FxRead) into a
// callable Go function value with the requested signature. Inverse
// of cabi.FuncPointer.
//
// The pattern is the one modernc's transpiled code uses internally
// (see _sqlite3OsRead in modernc.org/sqlite/lib for an example):
// wrap the uintptr in a struct, take its address, cast through
// unsafe.Pointer to a *func(...) of the right shape, and deref to
// get the function value.
//
// Each call site below specializes asFunc on a distinct signature.
// One generic helper instead of 9 verbatim casts; the read-out at
// the call site names what's being invoked, so the loss of "one
// function per signature" doc value is small.
func asFunc[F any](fp uintptr) F {
	return *(*F)(unsafe.Pointer(&struct{ uintptr }{fp}))
}

func callXClose(tls *libc.TLS, fp, pFile uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}

func callXRead(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

func callXWrite(tls *libc.TLS, fp, pFile, buf uintptr, amt int32, off sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr, int32, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, buf, amt, off)
}

func callXTruncate(tls *libc.TLS, fp, pFile uintptr, size sqlite3.Tsqlite3_int64) int32 {
	return asFunc[func(*libc.TLS, uintptr, sqlite3.Tsqlite3_int64) int32](fp)(tls, pFile, size)
}

func callXSync(tls *libc.TLS, fp, pFile uintptr, flags int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, flags)
}

func callXFileSize(tls *libc.TLS, fp, pFile, pSize uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, uintptr) int32](fp)(tls, pFile, pSize)
}

func callXLock(tls *libc.TLS, fp, pFile uintptr, level int32) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32) int32](fp)(tls, pFile, level)
}

func callXFileControl(tls *libc.TLS, fp, pFile uintptr, op int32, pArg uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr, int32, uintptr) int32](fp)(tls, pFile, op, pArg)
}

func callXSectorSize(tls *libc.TLS, fp, pFile uintptr) int32 {
	return asFunc[func(*libc.TLS, uintptr) int32](fp)(tls, pFile)
}
