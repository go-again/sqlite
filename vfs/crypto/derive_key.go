package crypto

import (
	"fmt"

	"golang.org/x/crypto/argon2"
)

// MinSaltLen is the minimum salt length DeriveKey accepts. Argon2's
// salt-length recommendation is 16 bytes; shorter salts weaken the
// per-database uniqueness guarantee the key-rotation recipe relies on.
const MinSaltLen = 16

// DeriveKey turns a passphrase + per-database salt into a key sized
// for the requested cipher (32 bytes for [Adiantum], 64 bytes for
// [AESXTS]) using Argon2id with the Argon2 authors' recommended
// interactive-login parameters: 3 iterations, 64 MiB memory, 4
// threads.
//
// IMPORTANT: salt must be unique per database and persisted
// alongside it. If the salt is lost the key cannot be re-derived;
// rotating the salt forces a full re-encryption. The doc.go
// "Key rotation recipe" section describes the offline migration.
// Returns an error if len(salt) < [MinSaltLen]; a too-short salt
// silently undermines the per-DB-uniqueness invariant the rest of
// the package depends on.
//
// Use:
//
//	salt := loadOrGenerateSalt() // 16+ bytes, per-DB unique
//	key, err := crypto.DeriveKey(passphrase, salt, crypto.Adiantum)
//	if err != nil { ... }
//	name, fs, _ := crypto.New(crypto.Options{Key: key})
//
// For higher-stakes archive-grade keys bump time + memory beyond
// the interactive defaults; for that case derive your key directly
// via golang.org/x/crypto/argon2 instead of this helper.
func DeriveKey(passphrase, salt []byte, cipher Cipher) ([]byte, error) {
	if len(salt) < MinSaltLen {
		return nil, fmt.Errorf("crypto.DeriveKey: salt is %d bytes; want at least %d for per-DB uniqueness",
			len(salt), MinSaltLen)
	}
	const (
		time    uint32 = 3
		memory  uint32 = 64 * 1024 // KiB
		threads uint8  = 4
	)
	return argon2.IDKey(passphrase, salt, time, memory, threads, uint32(KeyLen(cipher))), nil
}
