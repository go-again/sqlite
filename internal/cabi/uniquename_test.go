package cabi_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/go-again/sqlite/internal/cabi"
)

func TestUniqueName_PrefixPreserved(t *testing.T) {
	n := cabi.UniqueName("test-")
	if !strings.HasPrefix(n, "test-") {
		t.Errorf("UniqueName(%q) = %q, want prefix preserved", "test-", n)
	}
	if len(n) <= len("test-") {
		t.Errorf("UniqueName(%q) = %q, want non-empty suffix", "test-", n)
	}
}

func TestUniqueName_DistinctAcrossCalls(t *testing.T) {
	a := cabi.UniqueName("uniq-")
	b := cabi.UniqueName("uniq-")
	if a == b {
		t.Errorf("UniqueName returned identical values %q twice", a)
	}
}

func TestUniqueName_ConcurrentDistinct(t *testing.T) {
	const goroutines = 16
	const perG = 64

	var wg sync.WaitGroup
	names := make(chan string, goroutines*perG)
	for range goroutines {
		wg.Go(func() {
			for range perG {
				names <- cabi.UniqueName("conc-")
			}
		})
	}
	wg.Wait()
	close(names)

	seen := map[string]bool{}
	for n := range names {
		if seen[n] {
			t.Errorf("duplicate name %q", n)
		}
		seen[n] = true
	}
	if len(seen) != goroutines*perG {
		t.Errorf("got %d unique names, want %d", len(seen), goroutines*perG)
	}
}
