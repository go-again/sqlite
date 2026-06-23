package crypto

import (
	"crypto/aes"
	"encoding/binary"
	"fmt"
	"sync"

	"golang.org/x/crypto/xts"
	"lukechampine.com/adiantum"
	"lukechampine.com/adiantum/hbsh"
)

// File-kind enum, used as a tweak ingredient so the same ciphertext
// block doesn't decrypt cleanly when copied between files (e.g.
// from -wal into main DB). Stable wire values — changing them, OR
// inserting a new constant in the middle of this block, is an
// on-disk format break. New file kinds MUST be appended at the end
// and TestFileKindName_Table must be updated in the same change.
//
// Value 0 is reserved for "unencrypted" (auxiliary files we forward
// verbatim) and never reaches the cipher; the blank identifier
// claims it so iota assigns 1 to the first named constant.
//
// Exported so [Recorder] consumers can compare event.kind to a known
// value without round-tripping through [FileKindName]:
//
//	func (r *myRec) OnRead(kind byte, ...) {
//	    if kind == crypto.FileKindWAL { ... }
//	}
const (
	_                   byte = iota // 0 reserved: unencrypted
	FileKindMainDB                  // 1
	FileKindMainJournal             // 2
	FileKindWAL                     // 3
	FileKindTempDB                  // 4
	FileKindTempJournal             // 5
	FileKindSubJournal              // 6
)

// PageCipher is a length-preserving, tweakable encrypt/decrypt primitive. This
// VFS uses it per page; other gosqlite.org storage layers (e.g. vfs/vault)
// reuse it per block. Both methods operate in place — dst and src may share
// backing memory — and add no nonce, tag, or length (confidentiality at rest
// only; no MAC). Build one with [NewCipher].
//
// The (tweak, domain) pair domain-separates the cipher so the same ciphertext
// doesn't decrypt cleanly at the same tweak under a different domain — without
// it, an attacker with disk write access could rearrange units by swapping
// bytes between, say, a -wal and the main DB, and SQLite, having no MAC, would
// see well-formed content for the wrong context. Pass a stable per-unit tweak
// (the page or block number) and a domain byte (the file kind; see FileKind*).
//
// A PageCipher returned by NewCipher is safe for concurrent use.
type PageCipher interface {
	Encrypt(buf []byte, tweak uint64, domain byte)
	Decrypt(buf []byte, tweak uint64, domain byte)
}

// adiantumCipher wraps lukechampine.com/adiantum's HBSH construction.
// Tweak = 9 bytes: [fileKind, page_be:8]. Adiantum accepts arbitrary
// tweak length, so prepending a 1-byte file-kind tag is the cheapest
// possible domain separation.
//
// The mutex is load-bearing: lukechampine.com/adiantum's *hbsh.HBSH
// carries a mutable `hashBuf [32]byte` field that Encrypt and
// Decrypt both reuse (see hbsh.go:21-36 in v1.1.1). The upstream
// library documents no concurrency guarantee, and concurrent calls
// would race on that buffer with no panic — just silently corrupted
// output. SQLite's connection pool (or any consumer with
// MaxOpenConns > 1 across the same VFS) can fire concurrent xRead /
// xWrite trampolines, so we serialize cipher access here. XTS upstream
// explicitly documents concurrency safety; only Adiantum needs the
// mutex.
type adiantumCipher struct {
	mu sync.Mutex
	h  *hbsh.HBSH
}

func (c *adiantumCipher) Encrypt(buf []byte, pageNum uint64, fileKind byte) {
	var t [9]byte
	t[0] = fileKind
	binary.BigEndian.PutUint64(t[1:], pageNum)
	c.mu.Lock()
	c.h.Encrypt(buf, t[:])
	c.mu.Unlock()
}

func (c *adiantumCipher) Decrypt(buf []byte, pageNum uint64, fileKind byte) {
	var t [9]byte
	t[0] = fileKind
	binary.BigEndian.PutUint64(t[1:], pageNum)
	c.mu.Lock()
	c.h.Decrypt(buf, t[:])
	c.mu.Unlock()
}

// xtsCipher wraps golang.org/x/crypto/xts. XTS sector index is a
// fixed uint64; we use the high 8 bits for the file kind and the low
// 56 bits for the page number. Page numbers up to 2^56 cover any
// realistic file (2^56 × 512 = 2^65 bytes minimum range), so the
// truncation is theoretical, but we mask explicitly so a programming
// error on the page-number side fails loud rather than colliding
// silently with the kind bits.
type xtsCipher struct {
	c *xts.Cipher
}

const xtsPageMask uint64 = 0x00FF_FFFF_FFFF_FFFF

func xtsSector(pageNum uint64, fileKind byte) uint64 {
	return (uint64(fileKind) << 56) | (pageNum & xtsPageMask)
}

func (c *xtsCipher) Encrypt(buf []byte, pageNum uint64, fileKind byte) {
	c.c.Encrypt(buf, buf, xtsSector(pageNum, fileKind))
}

func (c *xtsCipher) Decrypt(buf []byte, pageNum uint64, fileKind byte) {
	c.c.Decrypt(buf, buf, xtsSector(pageNum, fileKind))
}

// KeyLen returns the raw key length in bytes that [NewCipher] requires for the
// given cipher: 32 for [Adiantum], 64 for [AESXTS] (two 32-byte AES keys). It is
// the single source of the per-cipher key size that NewCipher, [DeriveKey], and
// downstream layers (e.g. the vfs/vault data key) size against, so adding a
// cipher updates one place.
func KeyLen(kind Cipher) int {
	if kind == AESXTS {
		return 64
	}
	return 32
}

// NewCipher constructs a [PageCipher] from the cipher choice and the
// caller-supplied raw key. Validates key length against the chosen
// cipher's requirement; rejects with a clear error rather than
// silently truncating or expanding. Derive a key from a passphrase with
// [DeriveKey].
//
// We defensive-copy the key before handing it to the upstream cipher
// constructor: at least lukechampine.com/adiantum's chachaStream
// keeps a reference to the input slice and dereferences it on every
// Encrypt call. Without the copy, a caller who zeros or mutates
// Options.Key after New would silently corrupt every subsequent
// encrypt/decrypt. The copy is small (32 or 64 bytes); the safety
// is large.
func NewCipher(kind Cipher, key []byte) (PageCipher, error) {
	switch kind {
	case Adiantum:
		if len(key) != KeyLen(Adiantum) {
			return nil, fmt.Errorf("crypto: Adiantum requires a 32-byte key, got %d", len(key))
		}
		ourKey := append([]byte(nil), key...)
		return &adiantumCipher{h: adiantum.New(ourKey)}, nil
	case AESXTS:
		// XTS uses two AES keys of equal length. We support AES-256
		// only, so the combined key must be 64 bytes.
		if len(key) != KeyLen(AESXTS) {
			return nil, fmt.Errorf("crypto: AES-XTS-256 requires a 64-byte key (two 32-byte AES keys), got %d", len(key))
		}
		ourKey := append([]byte(nil), key...)
		c, err := xts.NewCipher(aes.NewCipher, ourKey)
		if err != nil {
			return nil, fmt.Errorf("crypto: xts.NewCipher: %w", err)
		}
		return &xtsCipher{c: c}, nil
	default:
		return nil, fmt.Errorf("crypto: unknown cipher %d", kind)
	}
}
