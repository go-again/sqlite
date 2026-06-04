package sqlite_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	sqlite "github.com/go-again/sqlite"
)

// TestRegisterAutoHook_ChainsAndPreservesPrior pins the load-bearing
// property RegisterAutoHook exists for: multiple calls compose into
// a chain that fires every prior hook in install order, and a
// pre-existing ConnectHook is preserved.
func TestRegisterAutoHook_ChainsAndPreservesPrior(t *testing.T) {
	d := sqlite.DefaultDriver()
	saved := d.ConnectHook
	t.Cleanup(func() { d.ConnectHook = saved })
	d.ConnectHook = nil

	var order []string
	pre := func(c *sqlite.Conn) error {
		order = append(order, "pre-existing")
		return nil
	}
	d.ConnectHook = pre

	sqlite.RegisterAutoHook(func(c *sqlite.Conn) error {
		order = append(order, "auto-1")
		return nil
	})
	sqlite.RegisterAutoHook(func(c *sqlite.Conn) error {
		order = append(order, "auto-2")
		return nil
	})

	if err := d.ConnectHook(nil); err != nil {
		t.Fatalf("ConnectHook: %v", err)
	}
	want := []string{"pre-existing", "auto-1", "auto-2"}
	if len(order) != len(want) {
		t.Fatalf("order=%v, want %v", order, want)
	}
	for i, s := range want {
		if order[i] != s {
			t.Errorf("[%d] order=%q, want %q", i, order[i], s)
		}
	}
}

// TestRegisterAutoHook_StopOnPriorError: if an earlier hook returns
// an error, later hooks must not fire.
func TestRegisterAutoHook_StopOnPriorError(t *testing.T) {
	d := sqlite.DefaultDriver()
	saved := d.ConnectHook
	t.Cleanup(func() { d.ConnectHook = saved })
	d.ConnectHook = nil

	stop := errors.New("stop")
	var fired bool
	sqlite.RegisterAutoHook(func(c *sqlite.Conn) error { return stop })
	sqlite.RegisterAutoHook(func(c *sqlite.Conn) error {
		fired = true
		return nil
	})

	if err := d.ConnectHook(nil); !errors.Is(err, stop) {
		t.Errorf("err=%v, want %v", err, stop)
	}
	if fired {
		t.Error("later hook ran after earlier one returned error")
	}
}

// TestRegisterAutoHook_ConcurrentRegistration pins the invariant that
// concurrent calls to RegisterAutoHook all land in the final chain —
// none of them are lost to a torn read of d.ConnectHook. The chain
// order is not deterministic under concurrency, but every registered
// hook MUST fire exactly once when the resulting ConnectHook is
// invoked. Run with -race to also catch the read/write race between
// RegisterAutoHook (writer) and Driver.Open (reader).
func TestRegisterAutoHook_ConcurrentRegistration(t *testing.T) {
	d := sqlite.DefaultDriver()
	saved := d.ConnectHook
	t.Cleanup(func() { d.ConnectHook = saved })
	d.ConnectHook = nil

	const goroutines = 32
	var fires atomic.Int64

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			sqlite.RegisterAutoHook(func(c *sqlite.Conn) error {
				fires.Add(1)
				return nil
			})
		})
	}
	wg.Wait()

	if err := d.ConnectHook(nil); err != nil {
		t.Fatalf("ConnectHook: %v", err)
	}
	if got := fires.Load(); got != int64(goroutines) {
		t.Errorf("fires = %d, want %d (some hooks were lost to a torn write)", got, goroutines)
	}
}
