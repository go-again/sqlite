package crypto

import "golang.org/x/crypto/argon2"

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
//
// Use:
//
//	salt := loadOrGenerateSalt() // 16+ bytes, per-DB unique
//	key := crypto.DeriveKey(passphrase, salt, crypto.Adiantum)
//	name, fs, _ := crypto.New(crypto.Options{Key: key})
//
// For higher-stakes archive-grade keys bump time + memory beyond
// the interactive defaults; for that case derive your key directly
// via golang.org/x/crypto/argon2 instead of this helper.
func DeriveKey(passphrase, salt []byte, cipher Cipher) []byte {
	const (
		time    uint32 = 3
		memory  uint32 = 64 * 1024 // KiB
		threads uint8  = 4
	)
	var keyLen uint32
	switch cipher {
	case AESXTS:
		keyLen = 64
	default:
		keyLen = 32
	}
	return argon2.IDKey(passphrase, salt, time, memory, threads, keyLen)
}
