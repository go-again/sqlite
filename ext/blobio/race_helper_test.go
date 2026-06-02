//go:build !race

package blobio_test

// raceEnabled mirrors the helper used at the root of the module to
// skip BLOB-API-touching tests under -race on darwin. modernc's
// transpiled Xsqlite3_blob_open does pointer arithmetic that Go's
// checkptr (-race) analyzer rejects; the failure is upstream, not in
// our code. Same pattern as the LoadExtension skip; will be removed
// when modernc fixes the pointer arithmetic.
const raceEnabled = false
