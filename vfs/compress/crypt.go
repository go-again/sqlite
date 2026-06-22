package compress

import (
	"errors"
	"fmt"

	"gosqlite.org/vfs/crypto"
)

// At-rest encryption reuses vfs/crypto's length-preserving page cipher
// ([crypto.PageCipher]), applied per block-aligned slot extent inside the
// storage engine (compress first, then encrypt — ciphertext is incompressible).
// The cipher adds no nonce, tag, or length, so the container format is
// unchanged; the trade is confidentiality at rest only, no integrity (matching
// vfs/crypto). The superblock's enc byte records the cipher so a reopen knows
// to decrypt; the data blocks and (a later increment) the directory are the
// encrypted units, while the superblock header stays plaintext to bootstrap.

// Cipher domain bytes (the file-kind tweak ingredient), distinct per on-disk
// unit so a ciphertext can't be replayed as a different one.
const (
	domainPageData  byte = 1 // a compressed data slot
	domainDirectory byte = 2 // the page directory
	domainJournal   byte = 3 // the rollback journal (-journal)
	domainWAL       byte = 4 // the write-ahead log (-wal)
	domainTempAux   byte = 5 // temp DB/journal and sub/super-journals
)

// dirTweak is the cipher tweak for the directory. The directory is a single
// unit (not indexed like data pages), so a constant tweak suffices; the domain
// byte separates it from data slots.
const dirTweak uint64 = 0

// dirCanary is a known plaintext written at the front of the encrypted
// directory. After decrypting the directory, a mismatch means the key is wrong
// — a crisp signal ([ErrWrongKey]) instead of a downstream parse/decompress
// failure. dirCanaryLen bytes, always present in an encrypted container (even an
// empty one writes a canary-only directory).
const dirCanaryLen = 16

var dirCanary = [dirCanaryLen]byte{'g', 'o', 's', 'q', 'l', 'i', 't', 'e', 'z', '-', 'e', 'n', 'c', 'v', '1', 0}

// ErrWrongKey reports that the supplied key (of the right cipher) failed to
// decrypt the container — the directory canary did not match. Test for it with
// [errors.Is].
var ErrWrongKey = errors.New("compress: wrong encryption key")

// superblock.enc markers. 0 means unencrypted; the cipher kinds are offset by
// one from [crypto.Cipher] so that crypto.Adiantum (value 0) is distinguishable
// from "unencrypted" on disk.
const (
	encNone     uint8 = 0
	encAdiantum uint8 = 1
	encAESXTS   uint8 = 2
)

// ErrEncrypted reports that the container is encrypted but no (matching) key was
// supplied in [Options]. Test for it with [errors.Is].
var ErrEncrypted = errors.New("compress: database is encrypted; a matching key is required")

func encByte(c crypto.Cipher) (uint8, error) {
	switch c {
	case crypto.Adiantum:
		return encAdiantum, nil
	case crypto.AESXTS:
		return encAESXTS, nil
	default:
		return 0, fmt.Errorf("compress: unknown cipher %v", c)
	}
}

// cipherFromOptions builds the page cipher from opts. With no key it returns
// (nil, encNone, nil) — the unencrypted default.
func cipherFromOptions(opts Options) (crypto.PageCipher, uint8, error) {
	if len(opts.Key) == 0 {
		return nil, encNone, nil
	}
	enc, err := encByte(opts.Cipher)
	if err != nil {
		return nil, 0, err
	}
	c, err := crypto.NewCipher(opts.Cipher, opts.Key)
	if err != nil {
		return nil, 0, err
	}
	return c, enc, nil
}

// checkEnc verifies the on-disk encryption marker matches what the caller
// supplied (a cipher, or its absence), called when reopening an existing
// container.
func (c *container) checkEnc(onDisk uint8) error {
	switch {
	case onDisk == c.enc:
		return nil
	case onDisk != encNone && c.cipher == nil:
		return ErrEncrypted
	case onDisk == encNone && c.cipher != nil:
		return errors.New("compress: database is not encrypted, but a key was supplied")
	default:
		return errors.New("compress: database cipher does not match the supplied key")
	}
}
