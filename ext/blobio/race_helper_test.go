package blobio_test

import "github.com/go-again/sqlite/internal/raceskip"

// raceEnabled mirrors the helper used at the root of the module to
// skip BLOB-API-touching tests under -race on darwin. modernc's
// transpiled Xsqlite3_blob_open does pointer arithmetic that Go's
// checkptr (-race) analyzer rejects; the failure is upstream, not in
// our code.
const raceEnabled = raceskip.Enabled
