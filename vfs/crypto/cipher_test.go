package crypto

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"sync"
	"testing"
)

// TestCipher_ConcurrentEncrypt_NoCorruption pins the Adiantum-mutex
// fix. lukechampine.com/adiantum's *hbsh.HBSH reuses a mutable
// hashBuf field across Encrypt/Decrypt calls; without a mutex,
// concurrent goroutines hitting the same cipher produce silently
// corrupted output (no panic, no race detector signal under our
// package-wide -race skip). N goroutines each round-trip distinct
// plaintexts; if any cipher invocation interleaves with another,
// the decrypted plaintext won't match the original.
func TestCipher_ConcurrentEncrypt_NoCorruption(t *testing.T) {
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

			const (
				goroutines = 16
				iterations = 500
				blockSize  = 512
			)
			var wg sync.WaitGroup
			errs := make(chan string, goroutines*iterations)
			for g := range goroutines {
				wg.Add(1)
				go func(gID int) {
					defer wg.Done()
					// Each goroutine uses a distinct plaintext pattern
					// so any inter-goroutine contamination shows up as
					// a byte mismatch after round-trip.
					plain := make([]byte, blockSize)
					for i := range plain {
						plain[i] = byte(gID ^ i)
					}
					for it := range iterations {
						page := uint64(gID*iterations + it)
						buf := append([]byte(nil), plain...)
						c.encrypt(buf, page, FileKindMainDB)
						c.decrypt(buf, page, FileKindMainDB)
						if !bytes.Equal(buf, plain) {
							errs <- fmt.Sprintf("goroutine %d iter %d page %d: round-trip mismatch", gID, it, page)
							return
						}
					}
				}(g)
			}
			wg.Wait()
			close(errs)
			for msg := range errs {
				t.Error(msg)
			}
		})
	}
}

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
			c.encrypt(buf1, samePage, FileKindMainDB)
			c.encrypt(buf2, samePage, FileKindWAL)

			if bytes.Equal(buf1, buf2) {
				t.Fatal("ciphertext identical across file kinds — cross-file substitution attack possible")
			}

			// Also: decrypting with the wrong kind must NOT yield the
			// original plaintext.
			misDecrypt := append([]byte(nil), buf1...)
			c.decrypt(misDecrypt, samePage, FileKindWAL)
			if bytes.Equal(misDecrypt, plaintext) {
				t.Error("decrypt with wrong file kind produced original plaintext — domain separation broken")
			}

			// And: decrypting with the correct kind recovers the original.
			roundtrip := append([]byte(nil), buf1...)
			c.decrypt(roundtrip, samePage, FileKindMainDB)
			if !bytes.Equal(roundtrip, plaintext) {
				t.Error("decrypt with correct file kind failed to recover plaintext")
			}
		})
	}
}

func TestCipher_String(t *testing.T) {
	for _, tc := range []struct {
		in   Cipher
		want string
	}{
		{Adiantum, "Adiantum"},
		{AESXTS, "AES-XTS"},
		{Cipher(99), "Cipher(99)"},
		{Cipher(-1), "Cipher(-1)"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Cipher(%d).String() = %q, want %q", int(tc.in), got, tc.want)
		}
	}
}
