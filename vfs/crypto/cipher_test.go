package crypto

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

// TestCipher_FileKindDomainSeparation pins the cross-file replay fix:
// the same plaintext encrypted at the same page number under two
// different file kinds must produce two different ciphertexts. Before
// the file-kind tweak was added, an attacker could byte-copy a
// ciphertext page from -wal into the main DB at the same offset and
// SQLite would decrypt it cleanly to a valid (but wrong-context) page.
// The threat model documents confidentiality only, so this isn't an
// integrity bug per se — but the data-flow channel ("rearrange pages
// undetected by swapping between sidecars") was wide enough to invite
// active-tamper attacks that no encrypted-at-rest user would expect.
func TestCipher_FileKindDomainSeparation(t *testing.T) {
	plaintext := bytes.Repeat([]byte("ABCDEFGH"), 64) // 512 bytes
	for _, kind := range []string{"Adiantum", "AES-XTS"} {
		t.Run(kind, func(t *testing.T) {
			var c pageCipher
			var err error
			switch kind {
			case "Adiantum":
				key := make([]byte, 32)
				io.ReadFull(rand.Reader, key)
				c, err = newCipher(Adiantum, key)
			case "AES-XTS":
				key := make([]byte, 64)
				io.ReadFull(rand.Reader, key)
				c, err = newCipher(AESXTS, key)
			}
			if err != nil {
				t.Fatal(err)
			}

			buf1 := append([]byte(nil), plaintext...)
			buf2 := append([]byte(nil), plaintext...)
			const samePage uint64 = 5
			c.encrypt(buf1, samePage, fileKindMainDB)
			c.encrypt(buf2, samePage, fileKindWAL)

			if bytes.Equal(buf1, buf2) {
				t.Fatal("ciphertext identical across file kinds — cross-file substitution attack possible")
			}

			// Also: decrypting with the wrong kind must NOT yield the
			// original plaintext.
			misDecrypt := append([]byte(nil), buf1...)
			c.decrypt(misDecrypt, samePage, fileKindWAL)
			if bytes.Equal(misDecrypt, plaintext) {
				t.Error("decrypt with wrong file kind produced original plaintext — domain separation broken")
			}

			// And: decrypting with the correct kind recovers the original.
			roundtrip := append([]byte(nil), buf1...)
			c.decrypt(roundtrip, samePage, fileKindMainDB)
			if !bytes.Equal(roundtrip, plaintext) {
				t.Error("decrypt with correct file kind failed to recover plaintext")
			}
		})
	}
}
