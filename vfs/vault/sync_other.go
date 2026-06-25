//go:build !darwin

package vault

import "os"

// syncBacking flushes f. F_BARRIERFSYNC is a darwin-specific optimization (see
// sync_darwin.go); on every other platform os.File.Sync is the right durable flush.
func syncBacking(f *os.File) error { return f.Sync() }
