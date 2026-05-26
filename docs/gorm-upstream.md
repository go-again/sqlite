# Coverage: gorm upstream test suite

The canonical compatibility proof for our `gorm` sub-package is running the
full `gorm.io/gorm/tests` integration suite against our dialector. Last
reviewed against `gorm.io/gorm v1.31.1` on 2026-05-26.

## Headline numbers

| Result | Count |
|---|---|
| PASS | 386 |
| FAIL | 0 |
| SKIP | 22 (all other-dialect-specific; not our doing) |
| Wall time | ~54 s, single goroutine pool |

The 22 skips are: GaussDB / Postgres / TiDB / MySQL test variants, two
MultiPrimaryKeys cases that need composite foreign keys SQLite doesn't
support, `TestMigrateWithIndexComment` (SQLite has no column-comment
syntax), and a handful of fixture-dependent association tests that opt
themselves out via `if DB.Dialector.Name() == "sqlite" { t.Skip() }`.

## Reproduction recipe

The upstream suite expects the dialector to live at the `gorm.io/driver/sqlite`
import path. We re-export `github.com/go-again/sqlite/gorm` under that
path via a tiny shim module, then point gorm/tests's `go.mod` at the shim
with a `replace` directive.

Set `REPO` to the absolute path of your checkout of this repository
before running the snippet below — the `replace` directives need an
absolute filesystem path, not a module path.

```sh
export REPO=$(pwd)  # run from this repo's root
# 1. Clone gorm at the pinned version.
git clone --depth 1 --branch v1.31.1 https://github.com/go-gorm/gorm.git /tmp/gorm

# 2. Write the shim module.
mkdir -p /tmp/gorm-driver-shim
cat > /tmp/gorm-driver-shim/go.mod <<'EOF'
module gorm.io/driver/sqlite
go 1.25.0
require (
    github.com/go-again/sqlite v0.0.0
    gorm.io/gorm v1.31.1
)
replace github.com/go-again/sqlite => $REPO
EOF
cat > /tmp/gorm-driver-shim/sqlite.go <<'EOF'
package sqlite

import (
    "strings"

    gagorm "github.com/go-again/sqlite/gorm"
    "gorm.io/gorm"
)

const DriverName = gagorm.DriverName

type (
    Dialector = gagorm.Dialector
    Config    = gagorm.Config
)

// extraFlags configure WAL journaling + a busy timeout so concurrent
// goroutines in the upstream gorm/tests harness don't hit SQLITE_BUSY.
// mattn/go-sqlite3 happens to serialize via libc-level locks; modernc's
// pure-Go locks return BUSY immediately on contention.
const extraFlags = "_busy_timeout=5000&_journal=WAL&_sync=NORMAL"

func withFlags(dsn string) string {
    if dsn == "" || strings.HasPrefix(dsn, ":memory:") || strings.Contains(dsn, "busy_timeout") {
        return dsn
    }
    sep := "?"
    if strings.Contains(dsn, "?") {
        sep = "&"
    }
    return dsn + sep + extraFlags
}

func Open(dsn string) gorm.Dialector { return gagorm.Open(withFlags(dsn)) }
func New(cfg Config) gorm.Dialector {
    cfg.DSN = withFlags(cfg.DSN)
    return gagorm.New(cfg)
}
EOF
(cd /tmp/gorm-driver-shim && go mod tidy)

# 3. Wire the shim into gorm/tests.
cat >> /tmp/gorm/tests/go.mod <<'EOF'

replace gorm.io/driver/sqlite => /tmp/gorm-driver-shim
replace github.com/go-again/sqlite => $REPO
EOF
(cd /tmp/gorm/tests && go mod tidy)

# 4. Run.
rm -f /tmp/gorm.db /tmp/gorm.db-shm /tmp/gorm.db-wal
(cd /tmp/gorm/tests && go test -count=1 -timeout 180s -v ./...)
```

## Why the shim has extra flags

`mattn/go-sqlite3`'s file-locking happens through libc, which serializes
concurrent transactions implicitly. `modernc.org/sqlite`'s pure-Go locks
return `SQLITE_BUSY` immediately on contention. The upstream gorm tests
were written against mattn's behavior, so without `_busy_timeout` and WAL
mode the suite hits ~5 BUSY failures within the first 50 tests.

Setting `_busy_timeout=5000` and `_journal=WAL` makes the concurrency model
behave like mattn's from gorm's perspective. The flags live in the **shim**
rather than our dialector because they're test-harness concerns, not
ergonomics every user needs.

## What our dialector already injects

`github.com/go-again/sqlite/gorm` applies one DSN flag on every Open:

- `_texttotime=1` — makes `ColumnTypeScanType` return `time.Time` for
  DATE / DATETIME / TIMESTAMP columns. Without it, gorm's `Table(...).Find(&map)`
  reads (where gorm has no schema info) get an RFC3339-formatted string
  instead of a `time.Time`, which breaks the upstream
  `TestFind/FirstMapWithTable/Birthday` assertion. The injection is
  no-op if the caller already supplied `_texttotime` in the DSN.

This is what allowed the suite to flip from 1 fail → 0 fail on the
second local run.

## Why we run this manually rather than in CI today

The setup churns ~20 indirect modules (postgres, mysql, sqlserver, mssql,
gaussdb drivers — even though we only exercise SQLite, the gorm/tests
module brings them all in). The CI matrix would slow down by 60–90s per
job. We may add a single-job gorm-upstream lane later; for now, treat
this matrix as a periodic check, run on dependency bumps and before
release tags.

When SQLite or gorm bumps, the steps to repeat:

1. Re-run the recipe above with the new version pinned in the shim's
   `go.mod`.
2. If new tests appear, expect a one-time triage: classify each new failure
   as (a) genuine driver gap or (b) other-dialect-specific (legit skip).
3. Update the counts at the top of this file.

## What we explicitly do not claim

We don't run the gorm test suite under `-race`. modernc's
`_sqlite3LoadExtension` does pointer arithmetic that Go's checkptr (enabled
by `-race`) rejects, and several gorm tests touch extension-adjacent
code. The race-free run is the contract.

We don't claim parity with mattn's behavior on every test — only that
the suite as written passes. Where modernc semantics differ (file
locking, time formatting), we documented the adapter (shim flags +
`_texttotime`) above so the difference is auditable rather than buried.
