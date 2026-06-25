package crypto_test

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"

	"gosqlite.org/vfs/crypto"
)

// These isolate the raw per-page cipher cost (no VFS, no I/O), which is far higher
// than the end-to-end encrypted throughput — so the per-page cipher is NOT the
// bottleneck for large writes (that is write/RMW amplification above the cipher).
// BenchmarkPageCipher/AESXTS sits well below BenchmarkAESNIReference because
// x/crypto/xts dispatches one 16-byte AES block at a time rather than pipelining
// AES-NI; a batched XTS would close that specific gap.

func benchPageCipher(b *testing.B, kind crypto.Cipher, keyLen int) {
	const page = 8192
	c, err := crypto.NewCipher(kind, make([]byte, keyLen))
	if err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, page)
	b.SetBytes(page)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Encrypt(buf, uint64(i), 1)
	}
}

func BenchmarkPageCipher_Adiantum(b *testing.B) { benchPageCipher(b, crypto.Adiantum, 32) }
func BenchmarkPageCipher_AESXTS(b *testing.B)   { benchPageCipher(b, crypto.AESXTS, 64) }

// BenchmarkAESNIReference is the AES-NI ceiling: raw AES-256-CTR over one 8 KiB
// page. It is the yardstick the AES-XTS page cipher is measured against.
func BenchmarkAESNIReference(b *testing.B) {
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		b.Fatal(err)
	}
	iv := make([]byte, aes.BlockSize)
	buf := make([]byte, 8192)
	b.SetBytes(8192)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cipher.NewCTR(block, iv).XORKeyStream(buf, buf)
	}
}
