package vecgorm_test

import (
	"context"
	"sync"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
)

// TestPlugin_ConcurrentSchemaRegistration spawns several goroutines
// that all trigger a first-access of the plugin's schema cache for
// the same model type. The cache is protected by an RWMutex with
// double-checked locking; running this test under `go test -race`
// surfaces any data race on the plugin.meta map, and any missing
// write under the lock would corrupt the cached pointer.
//
// It also indirectly exercises the case where two goroutines both
// finish parsing before either commits to the map — the second
// committer must defer to the first's cached entry.
func TestPlugin_ConcurrentSchemaRegistration(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}

	const goroutines = 16
	const calls = 8
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*calls)

	for range goroutines {
		wg.Go(func() {
			for range calls {
				// KNN drives registerSchema on the first call and
				// reads it from the cache thereafter.
				if _, err := vecgorm.KNN[Document](
					context.Background(), db, []float32{1, 0, 0, 0}, 1,
				); err != nil {
					errCh <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent KNN: %v", err)
	}
}
