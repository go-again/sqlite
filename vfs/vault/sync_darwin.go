package vault

import (
	"os"
	"syscall"
)

// fBarrierFsync is fcntl(2)'s F_BARRIERFSYNC on darwin.
const fBarrierFsync = 85

// forceFullSync, set by SQLITEFS_FULLFSYNC=1, reverts syncBacking to the full
// device-cache flush (F_FULLFSYNC) for callers that want maximum power-loss
// durability at the cost of ~30x slower commits. Read once (env is process-stable).
var forceFullSync = os.Getenv("SQLITEFS_FULLFSYNC") == "1"

// syncBacking durably orders f's writes. On darwin it issues a write BARRIER
// (F_BARRIERFSYNC) instead of the full device-cache flush that os.File.Sync uses
// (F_FULLFSYNC). The barrier guarantees ORDERING — every write issued before it
// reaches the device before any write issued after — which is exactly the
// invariant the container's copy-on-write + superblock ping-pong commit relies on
// for crash CONSISTENCY: a superblock recovered after a crash can never reference a
// directory generation that is not itself durable, so the image is never corrupt.
//
// What it trades is DURABILITY, not consistency: F_BARRIERFSYNC does not force the
// device's volatile cache to platter, so a power loss may drop the last few
// committed transactions (reopen reverts to the previous consistent generation) —
// it cannot tear or corrupt the container. This is how real filesystems (APFS)
// sync, and it is ~30x faster than F_FULLFSYNC on macOS (measured), which is the
// dominant per-checkpoint cost for an encrypted container on a slow-fsync volume.
//
// If the barrier is unsupported on the underlying volume (some network/FUSE/Time
// Machine targets return an error), fall back to the full os.File.Sync.
func syncBacking(f *os.File) error {
	if forceFullSync {
		return f.Sync()
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), fBarrierFsync, 0); errno == 0 {
		return nil
	}
	return f.Sync()
}
