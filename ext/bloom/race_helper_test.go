package bloom_test

import "gosqlite.org/internal/raceskip"

// raceEnabled — see ext/blobio/race_helper_test.go for the rationale.
// ext/bloom persists its bit array via (*Conn).OpenBlob, which trips
// modernc's checkptr-flagged pointer arithmetic under -race on darwin.
const raceEnabled = raceskip.Enabled
