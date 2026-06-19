// Package filevtab holds the file-backed virtual-table scaffolding shared
// by the file-reading ext modules (ext/csv, ext/lines). Both expose a
// single file (or inline string) as a vtab, open it through a configurable
// fs.FS, strip a leading UTF-8 BOM, and answer BestIndex with a plain
// full-scan stub. The domain-specific parsing — CSV affinities vs newline
// scanning — stays in each package; only the genuinely-identical plumbing
// lives here so a fix (e.g. the BOM strip) lands in one place instead of
// drifting between two near-cloned files.
package filevtab

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	sqlite "gosqlite.org"
)

// UTF8BOM is the UTF-8 byte-order mark (U+FEFF encoded as UTF-8). Some
// editors and Windows tools prepend it to text files; left in place it
// would smuggle three bytes into the first parsed token (a CSV column
// name or the first line), so [OpenSource] strips it. Exported so tests
// that build BOM-prefixed fixtures reference the one canonical spelling
// rather than re-hardcoding the byte sequence.
const UTF8BOM = "\xEF\xBB\xBF"

// OSFS is a thin fs.FS over os.Open so a module's default Register behaves
// like a reader anchored at the OS root. Modules pass an OSFS to their
// RegisterFS to get os-backed access; callers wanting a sandbox pass their
// own fs.FS (embed.FS, fstest.MapFS, os.DirFS) instead.
type OSFS struct{}

func (OSFS) Open(name string) (fs.File, error) { return os.Open(name) }

// OpenSource resolves a file-backed vtab's input to an io.Reader plus the
// io.Closer that owns it. Exactly one of name/data is expected to be set;
// callers enforce that mutual exclusion before reaching here.
//
// When name is set the file is opened through fsys and a leading UTF-8 BOM
// is stripped so the first column name (csv) or first line (lines) doesn't
// silently carry three garbage bytes; the returned Closer closes the file.
// When data is set the inline string is wrapped in a no-op Closer. errPrefix
// (e.g. "csv" or "lines") prefixes the open-error message so each package's
// wording stays byte-identical to its previous hand-written form.
func OpenSource(fsys fs.FS, name, data, errPrefix string) (io.Reader, io.Closer, error) {
	if name != "" {
		f, err := fsys.Open(name)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: open %q: %w", errPrefix, name, err)
		}
		buf := bufio.NewReader(f)
		// Strip a UTF-8 BOM if present so the first parsed token doesn't
		// silently include three garbage bytes.
		if bom, _ := buf.Peek(len(UTF8BOM)); string(bom) == UTF8BOM {
			_, _ = buf.Discard(len(UTF8BOM))
		}
		return buf, f, nil
	}
	r := strings.NewReader(data)
	return r, io.NopCloser(r), nil
}

// FullScanBestIndex fills info with the cost/row estimates every
// file-backed module uses: there is no usable index, so SQLite always
// performs a sequential scan.
func FullScanBestIndex(info *sqlite.IndexInfo) {
	info.EstimatedCost = 1e6
	info.EstimatedRows = 1000
}
