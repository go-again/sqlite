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

// TestAuxSpanMatrix covers the properties the small-unit aux path relies on but that
// the happy-path test above does not: multi-unit spans at non-zero base offsets, that
// the per-unit tweak is the absolute unit number (no reuse across offsets), domain
// separation across file kinds, that a one-byte tamper is confined to its own unit
// (wide-block, so neighbors stay intact), and that a hole in the MIDDLE of a span is
// skipped while the surrounding data round-trips.
func TestAuxSpanMatrix(t *testing.T) {
	c, err := NewCipher(Adiantum, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	const unit = auxCryptUnit
	mkUnits := func(n int) []byte {
		b := make([]byte, n*unit)
		for i := range b {
			b[i] = byte(i*7 + 1) // non-zero, varied
		}
		return b
	}

	t.Run("multi_unit_roundtrip_at_offset", func(t *testing.T) {
		for _, base := range []int64{0, unit, 5 * unit, 1024 * unit} {
			plain := mkUnits(4)
			span := append([]byte(nil), plain...)
			encryptSpan(c, span, base, unit, FileKindWAL)
			if bytes.Equal(span, plain) {
				t.Fatalf("base %d: span not encrypted", base)
			}
			decryptSpan(c, span, base, unit, FileKindWAL)
			if !bytes.Equal(span, plain) {
				t.Fatalf("base %d: round-trip corrupted the span", base)
			}
		}
	})

	t.Run("tweak_varies_by_offset", func(t *testing.T) {
		p := bytes.Repeat([]byte{0xab}, unit)
		a := append([]byte(nil), p...)
		encryptSpan(c, a, 0, unit, FileKindWAL)
		b := append([]byte(nil), p...)
		encryptSpan(c, b, unit, unit, FileKindWAL) // same plaintext, next unit offset
		if bytes.Equal(a, b) {
			t.Fatal("identical plaintext at different offsets produced identical ciphertext (tweak reuse)")
		}
	})

	t.Run("domain_separates_kinds", func(t *testing.T) {
		p := bytes.Repeat([]byte{0xcd}, unit)
		a := append([]byte(nil), p...)
		encryptSpan(c, a, 0, unit, FileKindWAL)
		b := append([]byte(nil), p...)
		encryptSpan(c, b, 0, unit, FileKindMainJournal) // same plaintext+offset, other kind
		if bytes.Equal(a, b) {
			t.Fatal("identical plaintext/offset under different file kinds produced identical ciphertext (no domain separation)")
		}
	})

	t.Run("tamper_isolated_to_its_unit", func(t *testing.T) {
		plain := mkUnits(3)
		span := append([]byte(nil), plain...)
		encryptSpan(c, span, 0, unit, FileKindWAL)
		span[unit+10] ^= 0xff // corrupt one byte of the middle unit
		decryptSpan(c, span, 0, unit, FileKindWAL)
		if !bytes.Equal(span[:unit], plain[:unit]) {
			t.Error("tamper in unit 1 corrupted unit 0")
		}
		if !bytes.Equal(span[2*unit:], plain[2*unit:]) {
			t.Error("tamper in unit 1 corrupted unit 2")
		}
		if bytes.Equal(span[unit:2*unit], plain[unit:2*unit]) {
			t.Error("tampered unit 1 decrypted clean; a wide-block flip should garble its whole unit")
		}
	})

	t.Run("interior_hole_skipped", func(t *testing.T) {
		// On-disk image: [ciphertext(d0)] [zero hole, never written] [ciphertext(d2)].
		d0 := bytes.Repeat([]byte{1}, unit)
		d2 := bytes.Repeat([]byte{2}, unit)
		disk := make([]byte, 3*unit)
		e0 := append([]byte(nil), d0...)
		encryptSpan(c, e0, 0, unit, FileKindWAL)
		copy(disk[0:unit], e0)
		// disk[unit:2*unit] stays zero (a hole)
		e2 := append([]byte(nil), d2...)
		encryptSpan(c, e2, 2*unit, unit, FileKindWAL)
		copy(disk[2*unit:], e2)

		decryptSpan(c, disk, 0, unit, FileKindWAL)
		if !bytes.Equal(disk[0:unit], d0) {
			t.Error("unit 0 did not round-trip")
		}
		if !allZero(disk[unit : 2*unit]) {
			t.Error("interior hole did not read back as zeros")
		}
		if !bytes.Equal(disk[2*unit:], d2) {
			t.Error("unit 2 did not round-trip")
		}
	})
}
