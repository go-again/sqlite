package crypto

import (
	"crypto/aes"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/xts"
	"lukechampine.com/adiantum"
	"lukechampine.com/adiantum/hbsh"
)

// File-kind enum, used as a tweak ingredient so the same ciphertext
// block doesn't decrypt cleanly when copied between files (e.g.
// from -wal into main DB). Stable wire values — changing them is an
// on-disk format break.
//
// Value 0 is reserved for "unencrypted" (auxiliary files we forward
// verbatim today) and never reaches the cipher.
const (
	fileKindMainDB      byte = 1
	fileKindMainJournal byte = 2
	fileKindWAL         byte = 3
	fileKindTempDB      byte = 4
	fileKindTempJournal byte = 5
	fileKindSubJournal  byte = 6
)

// pageCipher abstracts the per-page encrypt/decrypt primitive so the
// io-method trampolines don't have to branch on cipher kind. Both
// implementations operate in place: dst and src may share backing
// memory.
//
// The (pageNum, fileKind) pair domain-separates the tweak so a
// ciphertext block from one file kind doesn't decrypt cleanly when
// copied into another at the same offset. Without it, an attacker
// with disk write access could rearrange pages by swapping bytes
// between main DB / journal / WAL — and SQLite, having no MAC, would
// see well-formed page content for the wrong context.
type pageCipher interface {
	encrypt(buf []byte, pageNum uint64, fileKind byte)
	decrypt(buf []byte, pageNum uint64, fileKind byte)
}

// adiantumCipher wraps lukechampine.com/adiantum's HBSH construction.
// Tweak = 9 bytes: [fileKind, page_be:8]. Adiantum accepts arbitrary
// tweak length, so prepending a 1-byte file-kind tag is the cheapest
// possible domain separation.
type adiantumCipher struct {
	h *hbsh.HBSH
}

func (c *adiantumCipher) makeTweak(pageNum uint64, fileKind byte) [9]byte {
	var t [9]byte
	t[0] = fileKind
	binary.BigEndian.PutUint64(t[1:], pageNum)
	return t
}

func (c *adiantumCipher) encrypt(buf []byte, pageNum uint64, fileKind byte) {
	t := c.makeTweak(pageNum, fileKind)
	c.h.Encrypt(buf, t[:])
}

func (c *adiantumCipher) decrypt(buf []byte, pageNum uint64, fileKind byte) {
	t := c.makeTweak(pageNum, fileKind)
	c.h.Decrypt(buf, t[:])
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

func (c *xtsCipher) encrypt(buf []byte, pageNum uint64, fileKind byte) {
	c.c.Encrypt(buf, buf, xtsSector(pageNum, fileKind))
}

func (c *xtsCipher) decrypt(buf []byte, pageNum uint64, fileKind byte) {
	c.c.Decrypt(buf, buf, xtsSector(pageNum, fileKind))
}

// newCipher constructs a pageCipher from the cipher choice and the
// caller-supplied raw key. Validates key length against the chosen
// cipher's requirement; rejects with a clear error rather than
// silently truncating or expanding.
func newCipher(kind Cipher, key []byte) (pageCipher, error) {
	switch kind {
	case Adiantum:
		if len(key) != 32 {
			return nil, fmt.Errorf("crypto: Adiantum requires a 32-byte key, got %d", len(key))
		}
		return &adiantumCipher{h: adiantum.New(key)}, nil
	case AESXTS:
		// XTS uses two AES keys of equal length. We support AES-256
		// only, so the combined key must be 64 bytes.
		if len(key) != 64 {
			return nil, fmt.Errorf("crypto: AES-XTS-256 requires a 64-byte key (two 32-byte AES keys), got %d", len(key))
		}
		c, err := xts.NewCipher(aes.NewCipher, key)
		if err != nil {
			return nil, fmt.Errorf("crypto: xts.NewCipher: %w", err)
		}
		return &xtsCipher{c: c}, nil
	default:
		return nil, fmt.Errorf("crypto: unknown cipher %d", kind)
	}
}
