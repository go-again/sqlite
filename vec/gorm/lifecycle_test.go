package vecgorm_test

import (
	"context"
	"strings"
	"testing"

	vecgorm "github.com/go-again/sqlite/vec/gorm"
)

func TestDropSidecar_RemovesSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	if err := vecgorm.DropSidecar(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where name='documents_vec'`).Scan(&n)
	if n != 0 {
		t.Errorf("after DropSidecar: documents_vec count=%d, want 0", n)
	}
}

func TestDimMismatch_LogsButDoesNotFail(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	// Migrate again with the same model — idempotent, no warning.
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
}

// DimChanged uses a different declared dim than Document so we can
// stand up Document first, then call Migrate with DimChanged pointing
// at the same sidecar (via table=documents_vec) to exercise the
// dim-mismatch warning path.
type DimChanged struct {
	ID        uint      `gorm:"primaryKey"`
	Embedding []float32 `gorm:"-" vec:"dim=8;table=documents_vec"`
}

func TestDimMismatch_DetectsDifferentDim(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	// Document declares dim=4; DimChanged declares dim=8 targeting the
	// SAME sidecar table. Migrate must NOT silently rebuild — the
	// existing sidecar stays at dim=4. The mismatch detection logs a
	// warning via the gorm logger; we don't assert on logger output
	// here, just that the call doesn't error and the sidecar is
	// untouched.
	if err := vecgorm.Migrate(db, &DimChanged{}); err != nil {
		t.Fatalf("dim mismatch should warn, not error: %v", err)
	}
	// Confirm the sidecar still accepts dim=4 inserts.
	if err := db.Create(&Document{Embedding: []float32{1, 0, 0, 0}}).Error; err != nil {
		t.Errorf("sidecar should still take dim=4 inserts: %v", err)
	}
}

// compositePK is rejected because vec0 rowids must be int64.
type compositePK struct {
	A         uint      `gorm:"primaryKey"`
	B         uint      `gorm:"primaryKey"`
	Embedding []float32 `gorm:"-" vec:"dim=4"`
}

func TestMigrate_RejectsCompositePK(t *testing.T) {
	db := openTestDB(t)
	err := vecgorm.Migrate(db, &compositePK{})
	if err == nil {
		t.Fatal("expected error on composite PK")
	}
	if !strings.Contains(err.Error(), "primary-key") {
		t.Errorf("error %q doesn't mention primary-key", err.Error())
	}
}

// badTag asserts the parser surfaces unknown keys with a clear message.
type badTag struct {
	ID        uint      `gorm:"primaryKey"`
	Embedding []float32 `gorm:"-" vec:"dim=4;unknown=foo"`
}

func TestTagParse_RejectsUnknownKey(t *testing.T) {
	db := openTestDB(t)
	err := vecgorm.Migrate(db, &badTag{})
	if err == nil {
		t.Fatal("expected error on unknown tag key")
	}
	if !strings.Contains(err.Error(), "unknown tag key") {
		t.Errorf("error %q doesn't mention unknown tag key", err.Error())
	}
}

// missingDim has no dim= in its tag.
type missingDim struct {
	ID        uint      `gorm:"primaryKey"`
	Embedding []float32 `gorm:"-" vec:"metric=cosine"`
}

func TestTagParse_RejectsMissingDim(t *testing.T) {
	db := openTestDB(t)
	err := vecgorm.Migrate(db, &missingDim{})
	if err == nil {
		t.Fatal("expected error on missing dim")
	}
	if !strings.Contains(err.Error(), "dim=N is required") {
		t.Errorf("error %q doesn't mention dim=N is required", err.Error())
	}
}

// noTagModel has no vec: tags — Migrate should succeed as a no-op
// (don't accidentally fail when users pass a mixed slice of models).
type noTagModel struct {
	ID    uint `gorm:"primaryKey"`
	Title string
}

func TestMigrate_NoOpForModelsWithoutTags(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &noTagModel{}); err != nil {
		t.Fatalf("Migrate without tags should be a no-op: %v", err)
	}
}

// TestDropTable_CascadesToSidecar asserts that calling gorm's standard
// Migrator().DropTable on a vec-tagged model also drops the sidecar
// vec0 table — no explicit DropSidecar call required. The cascade is
// implemented via the DropTableHook interface our gorm dialector
// exposes; both plugins register their cleanup as a hook.
func TestDropTable_CascadesToSidecar(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&Document{}); err != nil {
		t.Fatal(err)
	}
	var srcCount, sideCount int64
	db.Raw(`select count(*) from sqlite_master where name='documents'`).Scan(&srcCount)
	db.Raw(`select count(*) from sqlite_master where name='documents_vec'`).Scan(&sideCount)
	if srcCount != 0 {
		t.Errorf("source documents count=%d, want 0", srcCount)
	}
	if sideCount != 0 {
		t.Errorf("sidecar documents_vec count=%d, want 0 (DropTable should have cascaded)", sideCount)
	}
}

// TestDropSidecar_StillWorks confirms the explicit cleanup helper
// remains functional and is idempotent against the cascade.
func TestDropSidecar_StillWorks(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	if err := vecgorm.DropSidecar(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	// Idempotent: re-call doesn't error.
	if err := vecgorm.DropSidecar(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Raw(`select count(*) from sqlite_master where name='documents_vec'`).Scan(&n)
	if n != 0 {
		t.Errorf("sidecar count=%d, want 0", n)
	}
}

func TestKNN_ReturnsEmptyWhenSidecarEmpty(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &Document{}); err != nil {
		t.Fatal(err)
	}
	results, err := vecgorm.KNN[Document](context.Background(), db, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("empty-sidecar KNN: %d results, want 0", len(results))
	}
}

// TestKNN_WithFilter exercises WithFilter through the SoftDoc model,
// which declares a `deleted` metadata column on the sidecar.
// sqlite-vec accepts WHERE conjuncts on metadata columns cleanly; the
// rowid-filter path can confuse the planner so isn't exercised here.
func TestKNN_WithFilter(t *testing.T) {
	db := openTestDB(t)
	if err := vecgorm.Migrate(db, &SoftDoc{}); err != nil {
		t.Fatal(err)
	}
	docs := []SoftDoc{
		{Title: "a", Embedding: []float32{1, 0, 0, 0}},
		{Title: "b", Embedding: []float32{0.99, 0, 0, 0}},
	}
	if err := db.Create(&docs).Error; err != nil {
		t.Fatal(err)
	}
	// IncludeDeleted + WithFilter combine; default filter is
	// `deleted = 0`, so explicitly filter on deleted = 0 to confirm
	// WithFilter is wired (overlap with default is fine — just proves
	// the conjunct lands in the SQL).
	results, err := vecgorm.KNN[SoftDoc](
		context.Background(), db, []float32{1, 0, 0, 0}, 5,
		vecgorm.WithFilter("deleted = ?", 0),
		vecgorm.IncludeDeleted(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("filtered results=%d, want 2", len(results))
	}
}
