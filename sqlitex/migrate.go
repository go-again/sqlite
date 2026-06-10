package sqlitex

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Migration is one schema-migration step parsed from a `.sql` file.
type Migration struct {
	Version int    // numeric prefix of the filename (e.g. 0003_… → 3)
	Name    string // the description after the prefix (e.g. "add_index")
	SQL     string // file contents (may contain multiple statements)
}

// LoadMigrations reads every `*.sql` file in fsys (recursively), parsing each
// filename of the form "NNNN_description.sql" into a [Migration]. Results are
// sorted ascending by version. It errors on a non-numeric prefix or a
// duplicate version.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	var migs []Migration
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return nil
		}
		version, name, perr := parseMigrationName(path.Base(p))
		if perr != nil {
			return fmt.Errorf("sqlitex: migration %q: %w", p, perr)
		}
		body, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		migs = append(migs, Migration{Version: version, Name: name, SQL: string(body)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	for i := 1; i < len(migs); i++ {
		if migs[i].Version == migs[i-1].Version {
			return nil, fmt.Errorf("sqlitex: duplicate migration version %d (%q and %q)",
				migs[i].Version, migs[i-1].Name, migs[i].Name)
		}
	}
	return migs, nil
}

// parseMigrationName splits "0003_add_index.sql" into (3, "add_index").
func parseMigrationName(base string) (version int, name string, err error) {
	stem := strings.TrimSuffix(base, ".sql")
	digits := stem
	if d, n, ok := strings.Cut(stem, "_"); ok {
		digits, name = d, n
	}
	if digits == "" {
		return 0, "", fmt.Errorf("missing numeric version prefix")
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, "", fmt.Errorf("non-numeric version prefix %q", digits)
		}
		version = version*10 + int(r-'0')
	}
	return version, name, nil
}

// Migrate applies every migration in fsys whose version exceeds the database's
// current PRAGMA user_version, in ascending order, each in its own
// transaction, bumping user_version as it goes. It is idempotent: re-running
// applies nothing once the database is up to date. Returns the number of
// migrations applied.
//
// Migration files are named "NNNN_description.sql" (see [LoadMigrations]).
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) (applied int, err error) {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return 0, err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	var current int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return 0, fmt.Errorf("sqlitex: read user_version: %w", err)
	}

	for _, m := range migs {
		if m.Version <= current {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return applied, fmt.Errorf("sqlitex: migration %d (%s): %w", m.Version, m.Name, err)
		}
		applied++
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *sql.Conn, m Migration) (err error) {
	if _, err = conn.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()
	if _, err = conn.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	// PRAGMA does not accept bind parameters; m.Version is a validated int.
	if _, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.Version)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
