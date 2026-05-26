package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestContext_CancelInterruptsRecursiveCTE asserts that canceling the context
// mid-query actually interrupts SQLite's execution rather than blocking
// until the (potentially astronomical) result set completes.
//
// We use a long recursive CTE that produces 10 million rows; without
// interrupt the QueryContext call would burn CPU for seconds. With the
// driver's sqlite3_interrupt plumbing wired in correctly, canceling after
// 50ms returns an error within ~200ms.
func TestContext_CancelInterruptsRecursiveCTE(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel 50ms in — long enough for the query to start, short enough
	// that 10M-row generation hasn't completed.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// Recursive CTE that counts up to 10M.
	rows, err := db.QueryContext(ctx, `
WITH RECURSIVE c(n) AS (
    SELECT 1 UNION ALL SELECT n + 1 FROM c WHERE n < 10000000
)
SELECT count(*) FROM c
`)
	elapsed := time.Since(start)

	if err == nil {
		// Some sqlite versions return rows but with an error on first Next().
		var n int
		if rows.Next() {
			rows.Scan(&n)
		}
		rows.Close()
		// Even if the query "succeeded" we should not have taken longer than
		// a couple hundred ms — that would indicate interrupt did nothing.
		if elapsed > 500*time.Millisecond {
			t.Errorf("query completed without cancellation in %v (interrupt failed?)", elapsed)
		}
		return
	}

	// Happy path: error returned. Make sure it surfaces the cancellation,
	// not some other failure, and that we didn't burn through the whole
	// 10M-row generation.
	if elapsed > 500*time.Millisecond {
		t.Errorf("cancellation took %v to surface; expected ≤ 500ms", elapsed)
	}
	// The error should be related to interruption. The driver may surface
	// context.Canceled, context.DeadlineExceeded, or an interrupt-flavored
	// SQLite error string depending on timing. Match on any of those.
	msg := strings.ToLower(err.Error())
	if !errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!strings.Contains(msg, "interrupt") &&
		!strings.Contains(msg, "canceled") {
		t.Errorf("unexpected error after cancellation: %v", err)
	}
}

// TestContext_DeadlineExceeded is the equivalent test using a context
// deadline instead of explicit cancellation. Same expectations: error
// within ~200ms, related to the deadline.
func TestContext_DeadlineExceeded(t *testing.T) {
	db, err := sql.Open(DriverNameMattn, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = db.QueryContext(ctx, `
WITH RECURSIVE c(n) AS (
    SELECT 1 UNION ALL SELECT n + 1 FROM c WHERE n < 10000000
)
SELECT count(*) FROM c
`)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected error from deadline; query finished in %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("deadline took %v to surface; expected ≤ 500ms", elapsed)
	}
}
