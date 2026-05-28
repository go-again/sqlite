package vecgorm_test

import (
	"strings"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
)

// TestKNNSQL_Bridge_IncludesSoftDelete confirms the soft-delete filter
// is automatically injected on a SoftDoc model, so the SQL emitted
// from KNNSQL has the same semantics as KNN[T].
func TestKNNSQL_Bridge_IncludesSoftDelete(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	sql, _, err := vecgorm.KNNSQL[SoftDoc](db, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "deleted = 0") {
		t.Errorf("expected default soft-delete filter in SQL, got: %s", sql)
	}
}

// TestKNNSQL_Bridge_IncludeDeletedDropsFilter confirms WithIncludeDeleted
// strips the auto-injected filter.
func TestKNNSQL_Bridge_IncludeDeletedDropsFilter(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	sql, _, err := vecgorm.KNNSQL[SoftDoc](db, []float32{1, 0, 0, 0}, 5,
		vecgorm.IncludeDeleted())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "deleted = 0") {
		t.Errorf("IncludeDeleted should strip soft-delete filter, got: %s", sql)
	}
}

// TestKNNSQL_Bridge_WithJoin_StacksWithSoftDelete confirms the soft-
// delete filter AND user-supplied WithFilter AND WithJoin all coexist
// in the emitted SQL.
func TestKNNSQL_Bridge_WithJoin_StacksWithSoftDelete(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	sql, args, err := vecgorm.KNNSQL[SoftDoc](db, []float32{1, 0, 0, 0}, 5,
		vecgorm.WithJoin("JOIN soft_docs s ON s.id = soft_docs_vec.rowid"),
		vecgorm.WithSelect("s.title"),
		vecgorm.WithFilter("s.title LIKE ?", "%alpha%"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "deleted = 0") {
		t.Errorf("soft-delete filter missing: %s", sql)
	}
	if !strings.Contains(sql, "JOIN soft_docs s") {
		t.Errorf("WithJoin not honored: %s", sql)
	}
	if !strings.Contains(sql, "s.title") {
		t.Errorf("WithSelect projection missing: %s", sql)
	}
	if !strings.Contains(sql, "s.title LIKE ?") {
		t.Errorf("WithFilter conjunct missing: %s", sql)
	}
	// Args = [embedding bytes, "%alpha%"] in declaration order.
	if len(args) != 2 {
		t.Errorf("args=%d, want 2 (embedding + filter literal)", len(args))
	}
}

// TestKNN_Bridge_RejectsWithSelect ensures the typed KNN[T] errors out
// when WithSelect is set; consumers must use KNNSQL instead.
func TestKNN_Bridge_RejectsWithSelect(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	_, err := vecgorm.KNN[Document](t.Context(), db, []float32{1, 0, 0, 0}, 1,
		vecgorm.WithSelect("extra"))
	if err == nil {
		t.Fatal("expected error from KNN+WithSelect, got nil")
	}
	if !strings.Contains(err.Error(), "WithSelect") {
		t.Errorf("error %q doesn't mention WithSelect", err.Error())
	}
}
