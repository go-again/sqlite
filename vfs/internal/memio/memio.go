// Package memio holds page-store helpers shared by the in-memory VFS
// sub-packages (vfs/mvcc, vfs/memdb). Both store file contents as a
// map[int64][]byte keyed by byte offset, and both must answer xRead by
// copying the portion of every stored page that overlaps the requested
// byte range into the caller's buffer. Lifting the overlap copy here
// keeps the two trampolines from drifting — an off-by-one fix lands in
// one place instead of two.
package memio

// ReadFromPages copies, for every page in m that overlaps the byte
// range [off, end), the overlapping bytes into dst (whose first byte
// corresponds to offset off). It returns the largest contiguous-from-off
// byte count any single page contributed — the caller compares this
// against the requested amount to decide SHORT_READ. dst must already be
// zero-filled by the caller so gaps surface as zeroed bytes, matching
// SQLite's SHORT_READ expectation.
func ReadFromPages(m map[int64][]byte, off, end int64, dst []byte) (filled int32) {
	for pageStart, v := range m {
		pageEnd := pageStart + int64(len(v))
		s := max(pageStart, off)
		e := min(pageEnd, end)
		if s >= e {
			continue
		}
		copy(dst[s-off:e-off], v[s-pageStart:e-pageStart])
		if n := int32(e - s); n > filled {
			filled = n
		}
	}
	return filled
}
