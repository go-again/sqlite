package vault

// compact.go implements the offline container rewrites: Compact (densely repack a
// container in place, returning freed blocks to the filesystem) and Snapshot (a
// consistent encrypted+compressed copy to a new path). Under churn the live
// container reuses freed blocks (the at-rest file plateaus rather than growing)
// and never shrinks the physical file mid-session; these are the out-of-band
// reclaim and backup paths. Both close over one rewrite core.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sqlite "gosqlite.org"
)

// Compact rewrites the container at cfg.Path into a fresh, densely-packed file and
// atomically replaces the original, returning freed space to the filesystem. The
// database must NOT be open. Pass the same Options used to open it (key/recipients/
// authenticated mode); Options.Level may differ to recompress at a new level. The
// page/block geometry is taken from the source — Options.PageSize/BlockSize are
// ignored — and an encrypted source must be given Options.Key or Options.Recipients
// (Identities alone open it but cannot re-seal the copy), else Compact errors rather
// than write a plaintext file.
//
// It preserves the exact logical database and its encryption: every live page is
// re-encoded into a freshly allocated container, so the physical fragmentation that
// accumulates under churn (insert/delete/VACUUM cycles) is removed. The committed
// generation is CONTINUED, not reset, so an [Options.Anchor] stays valid across
// compaction (the anchor floor advances to the compacted generation, and any
// pre-compaction image is rejected as a rollback).
//
// Run it on a closed database; opening the same path concurrently while Compact
// runs is rejected. A crash during Compact leaves the original untouched (the new
// container is written to a temp sibling and renamed only once durable).
//
// For a multi-recipient database (Options.Recipients) there are two modes. Pass
// the full Recipients (and Masters/SignWith) and the compacted file is re-wrapped
// under a FRESH random data key — Compact doubles as a key rotation, like [Rekey].
// Pass only the Identities needed to read the source (no Recipients) and Compact
// PRESERVES the existing keyslot and data key verbatim — the copy keeps the same
// membership — so a single holder can `compact -identity <key>` a shared image
// without re-specifying every recipient. A raw-key database keeps its key. (A
// writer-signed database still needs Options.WriteAs to re-sign the copy's commits.)
func Compact(cfg sqlite.Config, opts Options) error {
	if err := requireOnDiskPath(cfg); err != nil {
		return err
	}
	kc, err := keyConfigFromOptions(opts)
	if err != nil {
		return err
	}
	// In place: read and write with the same key config, and CONTINUE the source
	// generation so an external replay anchor stays monotonic across the rewrite.
	// allowPreserve: only Identities keeps the source keyslot + data key.
	return rewrite(cfg.Path, cfg.Path, kc, kc, opts.Level, true, true)
}

// CompactLogical rebuilds the container at cfg.Path into a fresh, densely-packed
// file by copying only the LIVE database content — a SQLite VACUUM INTO routed
// through a destination vault container — so reclaiming space is O(live data), not
// O(allocated pages). Unlike [Compact] (which copies every physical inner-DB page and
// so only shrinks if the database was vacuumed first), CompactLogical needs no prior
// PRAGMA incremental_vacuum or VACUUM: a large deletion shrinks in one pass,
// proportional to what is KEPT rather than what was freed.
//
// It preserves the encryption and membership — pass the same Options used to open the
// database (Key, or Identities; Masters/WriteAs for writer-signed mode), and the
// rebuilt file is sealed to the SAME data key and recipients. It is a reclaim, not a
// rotation: it never re-seals to a new recipient set or rotates the key (use [Compact]
// with Recipients, or [Rekey], for that), and it does not continue an [Options.Anchor]
// generation (the rebuild starts fresh), so an anchored database must use [Compact] —
// CompactLogical refuses one.
//
// The database must be closed (not open in this process); CompactLogical opens it,
// rebuilds into a temp sibling, and atomically renames it over the original only once
// durable, so a crash leaves the original intact. The temp container and VACUUM's
// spill files are encrypted by the same VFS — no plaintext ever touches disk.
func CompactLogical(cfg sqlite.Config, opts Options) error {
	if err := requireOnDiskPath(cfg); err != nil {
		return err
	}
	if opts.Anchor != nil {
		return errors.New("vault: CompactLogical does not continue an anchor generation; use Compact for an anchored database")
	}
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return err
	}
	// We open the source ourselves and atomically replace it, so it must not already
	// be open (a live container) or reserved (an offline op) in this process.
	containers.mu.Lock()
	busy := containers.m[abs] != nil
	if _, locked := containers.locked[abs]; locked {
		busy = true
	}
	containers.mu.Unlock()
	if busy {
		return fmt.Errorf("vault: %q is open or busy; close it before CompactLogical", cfg.Path)
	}

	// The rebuild target: a unique sibling VACUUM INTO creates, then we rename over the
	// original. Remove the placeholder so VACUUM INTO can create it fresh.
	tf, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".clogical-*")
	if err != nil {
		return err
	}
	tmpPath := tf.Name()
	_ = tf.Close()
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	// Open the source through a VFS that seals the VACUUM INTO target to the source's
	// key/membership, and rebuild only its live content into the temp container. One
	// connection so the source open (which captures the key to preserve) precedes the
	// target create on the same connection.
	db, err := openThroughVault(cfg, opts, true)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`VACUUM INTO '` + strings.ReplaceAll(tmpPath, "'", "''") + `'`); err != nil {
		_ = db.Close()
		return fmt.Errorf("vault: CompactLogical VACUUM INTO: %w", err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("vault: CompactLogical close source: %w", err)
	}

	// Reserve the now-closed source path for the swap: this fails if another opener
	// raced in after we closed, so we never rename out from under a live container.
	if !reservePath(abs) {
		return fmt.Errorf("vault: %q was reopened during CompactLogical; not replacing it", cfg.Path)
	}
	defer releasePath(abs)
	if err := os.Rename(tmpPath, abs); err != nil {
		return err
	}
	cleanup = false
	if d, err := os.Open(filepath.Dir(abs)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Snapshot writes a consistent, densely-packed copy of the container at src to a
// NEW path dst, re-encoding every live page — the encrypted, compressed analogue
// of [Pack] (which is compression-only and writes plaintext). It is the way to
// hand someone a point-in-time backup of an encrypted database without plaintext
// ever touching disk, optionally re-sealed to a different recipient set.
//
// readOpts supplies what reads src (Key, or Identities plus Masters to verify);
// writeOpts supplies the destination posture (Level, Cipher, and the NEW Key or
// Recipients/Masters/SignWith/Writers to seal dst to). An encrypted src must be
// re-sealed — writeOpts must carry a Key or Recipients — else Snapshot refuses to
// write a plaintext copy. dst is written atomically (a temp sibling renamed into
// place) and must not be open.
//
// Unlike [Compact], dst starts a FRESH commit generation: a backup is a standalone
// artifact, independent of the source's [Options.Anchor], so it carries its own
// floor (writeOpts.Anchor) rather than continuing src's. src is left untouched.
func Snapshot(dst, src string, readOpts, writeOpts Options) error {
	if src == "" || dst == "" {
		return errors.New("vault: Snapshot needs both a src and a dst path")
	}
	// Snapshot writes a standalone backup with a FRESH generation; pointed at the
	// source it would silently reset that database's generation (a rollback of an
	// Options.Anchor floor). Rewriting in place is Compact's job.
	if absSrc, e1 := filepath.Abs(src); e1 == nil {
		if absDst, e2 := filepath.Abs(dst); e2 == nil && absSrc == absDst {
			return errors.New("vault: Snapshot dst must differ from src (use Compact to rewrite in place)")
		}
	}
	readKc, err := keyConfigFromOptions(readOpts)
	if err != nil {
		return err
	}
	writeKc, err := keyConfigFromOptions(writeOpts)
	if err != nil {
		return err
	}
	// To a fresh path with possibly-different keys, and a FRESH generation (the
	// backup does not continue the source's replay anchor). Snapshot does NOT preserve
	// the source keyslot: a backup re-seals (or refuses an encrypted source with no new
	// creds) rather than silently copying the membership to a new path.
	return rewrite(dst, src, readKc, writeKc, writeOpts.Level, false, false)
}

// rewrite copies every live page of the container at srcPath into a fresh,
// densely-packed container at dstPath, atomically (a temp sibling renamed into
// place). readKc opens and reads src; writeKc and writeLevel seal and write dst.
// When continueGen is set, dst CONTINUES src's commit generation so an
// [Options.Anchor] stays valid (Compact, in place); otherwise dst starts a FRESH
// generation (Snapshot, a standalone backup). It refuses to write a plaintext dst
// from an encrypted src unless writeKc re-seals it. It is the shared core of
// [Compact] and [Snapshot]; neither opens the database, so both reserve the paths
// against a concurrent live Open.
func rewrite(dstPath, srcPath string, readKc, writeKc keyConfig, writeLevel Compression, continueGen, allowPreserve bool) error {
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		return err
	}
	absDst, err := filepath.Abs(dstPath)
	if err != nil {
		return err
	}
	// Reserve src (and dst, if different) for the duration: the rewrite drives the
	// files through its own unregistered containers, so a concurrent live container
	// over either would race two writers onto it.
	if !reservePath(absSrc) {
		return fmt.Errorf("vault: %q is open or busy; close it first", srcPath)
	}
	defer releasePath(absSrc)
	if absDst != absSrc {
		if !reservePath(absDst) {
			return fmt.Errorf("vault: destination %q is open or busy", dstPath)
		}
		defer releasePath(absDst)
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	// Open src with the package defaults: an existing container overrides its
	// geometry from the superblock, so PageSize/BlockSize are neither consulted nor
	// validated here (dst keeps the source geometry).
	src, err := newContainerOver(fileBacking{srcFile}, true, defaultBlockSize, defaultPageSize, defaultSegEntries, writeLevel, readKc)
	if err != nil {
		return err // newContainerOver closes srcFile on error
	}
	srcClosed := false
	closeSrc := func() {
		if !srcClosed {
			_ = src.back.Close()
			srcClosed = true
		}
	}
	defer closeSrc()

	// Geometry and the re-seal decision come from the SOURCE container, not from
	// opts. An encrypted source with NO new write creds is handled one of two ways:
	// if it is a recipients image we read via an identity, preserve its keyslot and
	// data key verbatim (Compact -identity keeps the membership); otherwise we have
	// no way to seal the copy and must refuse rather than write plaintext.
	var preserve *preservedKey
	if src.cipher != nil && len(writeKc.rawKey) == 0 && len(writeKc.recipients) == 0 && len(writeKc.masters) == 0 {
		if !allowPreserve || src.keyslotOffset == 0 || len(readKc.identities) == 0 {
			return errors.New("vault: rewriting an encrypted database needs Options.Key or Options.Recipients to re-seal the copy")
		}
		if src.authenticated && src.macKey == nil && src.writeAs == nil {
			// Writer-signed source: every commit of the copy must be re-signed, which
			// needs a writer identity a read-only recipient does not hold.
			return errors.New("vault: compacting a writer-signed database needs Options.WriteAs (a read-only recipient cannot re-sign the copy)")
		}
		preserve = &preservedKey{
			dek: src.dek, keyslot: src.keyslotBlob, enc: src.enc,
			auth: src.authenticated, writers: src.writers, writeAs: src.writeAs,
		}
	}
	blockSize, pageSize := src.blockSize, src.pageSize

	tmp, err := os.CreateTemp(filepath.Dir(dstPath), "."+filepath.Base(dstPath)+".rewrite-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// The temp container starts empty; do NOT run the anchor's empty-file rollback
	// check against it (the floor is legitimately above an empty temp file). The
	// real floor is advanced by the data commit below, via dst.anchor.
	dstKc := writeKc
	dstKc.anchor = nil
	dstKc.preserve = preserve // keep the source's keyslot + data key when no new creds were given
	dst, err := newContainerOver(fileBacking{tmp}, false, blockSize, pageSize, src.segEntries, writeLevel, dstKc)
	if err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	cleanup := func(e error) error {
		_ = dst.back.Close()
		_ = os.Remove(tmpPath)
		return e
	}

	// Attach the anchor now (after the empty create commit) so only the data commit
	// records the generation. For Compact, continue the sequence so the rewritten
	// file is NEWER than every pre-compaction image; for Snapshot, leave dst's fresh
	// generation as-is (a backup is independent of the source's floor).
	dst.anchor = writeKc.anchor
	if continueGen && src.committedGen > dst.committedGen {
		dst.committedGen = src.committedGen
	}

	// Copy every logical page (the slot cipher tweak is the page number, so a page
	// re-encodes correctly into the fresh, densely-packed layout). The directory's
	// freed-block holes are simply not carried over.
	buf := make([]byte, pageSize)
	for p := uint64(0); p < src.pageCount; p++ {
		if _, err := src.readAt(buf, int64(p*pageSize)); err != nil {
			return cleanup(fmt.Errorf("vault: rewrite read page %d: %w", p, err))
		}
		if _, err := dst.writeAt(buf, int64(p*pageSize)); err != nil {
			return cleanup(fmt.Errorf("vault: rewrite write page %d: %w", p, err))
		}
	}
	dst.dirty = true
	if err := dst.commit(); err != nil {
		return cleanup(fmt.Errorf("vault: rewrite commit: %w", err))
	}
	if err := dst.back.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	closeSrc()

	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if d, err := os.Open(filepath.Dir(dstPath)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
