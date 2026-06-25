package crypto

import (
	"bytes"
	"testing"
)

// TestCryptUnitFor: the databases keep the page-size cipher unit (page-aligned, and
// the main DB's format is stable), the transient journals and WAL get the small
// auxCryptUnit so their misaligned record/frame writes don't force a full-page RMW.
func TestCryptUnitFor(t *testing.T) {
	const ps = 8192
	for _, tc := range []struct {
		kind byte
		want int64
	}{
		{FileKindMainDB, ps},
		{FileKindTempDB, ps},
		{FileKindMainJournal, auxCryptUnit},
		{FileKindWAL, auxCryptUnit},
		{FileKindTempJournal, auxCryptUnit},
		{FileKindSubJournal, auxCryptUnit},
	} {
		if got := cryptUnitFor(tc.kind, ps); got != tc.want {
			t.Errorf("cryptUnitFor(kind %d, %d) = %d, want %d", tc.kind, ps, got, tc.want)
		}
	}
	// A 512-byte page already equals the aux unit: nothing smaller to pick.
	if got := cryptUnitFor(FileKindWAL, 512); got != 512 {
		t.Errorf("cryptUnitFor(WAL, 512) = %d, want 512", got)
	}
}

// TestDecryptSpanSkipsSparseHoles: a span of real ciphertext round-trips, but an
// all-zero unit (a sparse hole on disk) is left as zeros rather than decrypted to
// garbage — the property that keeps unwritten regions reading back as zeros.
func TestDecryptSpanSkipsSparseHoles(t *testing.T) {
	c, err := NewCipher(Adiantum, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	const unit = auxCryptUnit

	// Real data round-trips through encrypt/decryptSpan.
	plain := bytes.Repeat([]byte("payload!"), unit/8)
	span := append([]byte(nil), plain...)
	encryptSpan(c, span, 0, unit, FileKindWAL)
	if bytes.Equal(span, plain) {
		t.Fatal("encryptSpan left the data unchanged")
	}
	decryptSpan(c, span, 0, unit, FileKindWAL)
	if !bytes.Equal(span, plain) {
		t.Fatal("round-trip through encrypt/decryptSpan corrupted the data")
	}

	// An all-zero unit is a sparse hole: decryptSpan must leave it zero. (A raw
	// Decrypt of zeros would produce non-zero garbage — confirm that, so the skip is
	// shown to matter.)
	hole := make([]byte, unit)
	decryptSpan(c, hole, unit, unit, FileKindWAL) // baseOffset unit → unitNum 1
	if !allZero(hole) {
		t.Fatal("decryptSpan decrypted a sparse (all-zero) hole to garbage")
	}
	raw := make([]byte, unit)
	c.Decrypt(raw, 1, FileKindWAL)
	if allZero(raw) {
		t.Fatal("a raw Decrypt of zeros stayed zero; the sparse-hole skip would be moot")
	}
}
