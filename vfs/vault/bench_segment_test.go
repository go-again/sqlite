package vault

import (
	"fmt"
	"io"
	"testing"

	"gosqlite.org/vfs/crypto"
)

// countBacking is an in-memory backing with a cheap (no-op) Sync and a byte
// counter, so a benchmark can isolate the per-commit WRITE VOLUME from a backing's
// fsync model. (crashBacking copies the whole image on Sync, which would dominate.)
type countBacking struct {
	data    []byte
	written int64
}

func (c *countBacking) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (c *countBacking) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(c.data)) {
		c.data = append(c.data, make([]byte, end-int64(len(c.data)))...)
	}
	copy(c.data[off:], p)
	c.written += int64(len(p))
	return len(p), nil
}

func (c *countBacking) Sync() error          { return nil }
func (c *countBacking) Size() (int64, error) { return int64(len(c.data)), nil }
func (c *countBacking) Close() error         { return nil }
func (c *countBacking) Truncate(size int64) error {
	if size < int64(len(c.data)) {
		c.data = c.data[:size]
	}
	return nil
}

// BenchmarkDirectoryCommit measures the bytes a single page write + commit pushes
// to disk on an encrypted container of a given size, for the segmented directory
// (re-encodes only the changed segment) versus a single monolithic directory
// (re-encodes the whole thing, the pre-v2 behavior). The "writeBytes/commit"
// metric for "segmented" stays roughly flat as the page count grows; "monolithic"
// grows with it — the O(changed pages) vs O(total pages) difference.
func BenchmarkDirectoryCommit(b *testing.B) {
	const ps = 4096
	key := make([]byte, 32)
	for _, pages := range []int{1000, 4000, 16000} {
		for _, mode := range []struct {
			name string
			seg  uint64
		}{
			{"segmented", 1024},
			{"monolithic", 1 << 20}, // one segment spans the whole directory
		} {
			b.Run(fmt.Sprintf("%s/pages=%d", mode.name, pages), func(b *testing.B) {
				cb := &countBacking{}
				ct, err := newContainerOver(cb, false, defaultBlockSize, ps, mode.seg, CompressionNone,
					keyConfig{cipher: crypto.Adiantum, rawKey: key})
				if err != nil {
					b.Fatal(err)
				}
				f := &mainFile{c: ct}
				page := constPage(0x55, ps)
				for p := range pages { // grow the directory to `pages` entries
					if _, err := f.WriteAt(page, int64(p)*ps); err != nil {
						b.Fatal(err)
					}
				}
				if err := f.Sync(0); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				start := cb.written
				for i := 0; i < b.N; i++ {
					_, _ = f.WriteAt(page, int64(i%pages)*ps) // rewrite one existing page
					_ = f.Sync(0)                             // commit: re-encode the dirty directory
				}
				b.ReportMetric(float64(cb.written-start)/float64(b.N), "writeBytes/commit")
			})
		}
	}
}
