//go:build !race

package bloom_test

// raceEnabled — see ext/blobio/race_helper_test.go for the rationale.
// ext/bloom persists its bit array via (*Conn).OpenBlob, which trips
// modernc's checkptr-flagged pointer arithmetic under -race on darwin.
const raceEnabled = false
