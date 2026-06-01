# Coverage — ext/ loadable extensions

Tracks every loadable extension under [`ext/`](../ext/) — Go-native ports of selected ncruces/go-sqlite3 extensions and our own additions. Each row records the upstream reference, ported entry, registration shape, and the test pin.

Status legend:

- ✓ landed — code + tests + docs shipped
- ⚠ partial — code shipped but coverage incomplete, or one feature gated
- ✗ deferred — analyzed and chosen for a later round; reference only
- ✗ skipped — analyzed and intentionally dropped (overlap with existing surface, or upstream-only assumption that does not apply here)

## Vtab-based

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| array | ~230 | [ncruces/ext/array](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/array) | ✓ landed | `ext/array` + `ext/array/auto` | `ext/array/array_test.go` |
| bloom | ~370 | [ncruces/ext/bloom](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/bloom) | ✗ deferred | `ext/bloom` | — |
| closure | ~280 | [ncruces/ext/closure](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/closure) | ✗ deferred | `ext/closure` | — |
| csv | ~380 | [ncruces/ext/csv](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/csv) | ✗ deferred | `ext/csv` | — |
| fileio | ~430 | [ncruces/ext/fileio](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/fileio) | ✗ deferred | `ext/fileio` | — |
| lines | ~250 | [ncruces/ext/lines](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/lines) | ✗ deferred | `ext/lines` | — |
| pivot | ~310 | [ncruces/ext/pivot](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/pivot) | ✗ deferred | `ext/pivot` | — |
| statement | ~240 | [ncruces/ext/statement](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/statement) | ✗ deferred | `ext/statement` | — |
| blobio | ~170 | [ncruces/ext/blobio](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/blobio) | ✗ deferred | `ext/blobio` | — |

## Scalar UDFs (pure Go)

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| regexp | ~310 | [ncruces/ext/regexp](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/regexp) | ✓ landed | `ext/regexp` + `ext/regexp/auto` | `ext/regexp/regexp_test.go` |
| uuid | ~330 | [ncruces/ext/uuid](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/uuid) | ⚠ partial | `ext/uuid` + `ext/uuid/auto` | `ext/uuid/uuid_test.go` |
| hash | ~190 | [ncruces/ext/hash](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/hash) | ✓ landed | `ext/hash` + `ext/hash/auto` | `ext/hash/hash_test.go` |
| ipaddr | ~150 | [ncruces/ext/ipaddr](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/ipaddr) | ✓ landed | `ext/ipaddr` + `ext/ipaddr/auto` | `ext/ipaddr/ipaddr_test.go` |
| zorder | ~120 | [ncruces/ext/zorder](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/zorder) | ✓ landed | `ext/zorder` + `ext/zorder/auto` | `ext/zorder/zorder_test.go` |

`uuid` is ⚠ partial because the DCE Security v2 variant is not yet implemented (rarely used; consumers can open an issue if they need it). v1 / v3 / v4 / v5 / v6 / v7 plus parsing, formatting, version extraction, and timestamp extraction are covered.

## Aggregates / windows (pure Go)

| ext | LoC (est) | Upstream | Status | Entry | Test pin |
|---|---|---|---|---|---|
| stats | ~960 | [ncruces/ext/stats](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/stats) | ✓ landed | `ext/stats` + `ext/stats/auto` | `ext/stats/stats_test.go` |
| unicode | ~280 | [ncruces/ext/unicode](https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/unicode) | ⚠ partial | `ext/unicode` + `ext/unicode/auto` | `ext/unicode/unicode_test.go` |

`unicode` is ⚠ partial because the REGEXP override is intentionally not registered (would conflict with `ext/regexp`'s richer surface) and the `icu_load_collation` SQL-side helper isn't exposed (collations register through Go-side [unicode.RegisterLocaleCollation] instead). Auto-registration intentionally leaves SQLite's LIKE built-in alone to preserve the LIKE optimization; opt in via [unicode.RegisterLike] = true or [unicode.RegisterLikeOnly].

## Skipped (overlap with existing surface)

| ext | Reason |
|---|---|
| serdes | Root package already exposes `(*Conn).Serialize` / `Deserialize`. |
| vec1 | Sub-package `vec/` over `sqlite-vec` provides a richer typed surface. |
| spellfix1 | Loads modernc-untranspiled upstream C; `fts/` covers most fuzzy-text needs. |

## Registration shape

Two entry styles per extension; consumers pick whichever fits their wiring:

```go
// Explicit — register on a specific conn (the per-conn idiom for hooks).
import "github.com/go-again/sqlite/ext/regexp"
regexp.Register(c)

// Implicit — blank-import auto-wires via ConnectHook on every open.
import _ "github.com/go-again/sqlite/ext/regexp/auto"
```

The explicit form is the canonical entry; the blank-import `auto` sub-package is a thin shim that calls `Register` from a `ConnectHook` so registration survives connection pool churn.

## Adding a new extension

1. Pick the upstream ncruces module under [`.ncruces-go-sqlite3/ext/<name>`](../.ncruces-go-sqlite3/ext/) (read-only mirror).
2. Translate to our `(*Conn).RegisterFunc` / `RegisterAggregator` / `RegisterWindowFunction` / vtab helper APIs. Don't copy upstream verbatim; the runtime is different.
3. Ship `ext/<name>/<name>.go` + `<name>_test.go` + `doc.go` + `ext/<name>/auto/auto.go`.
4. Add a row to the relevant table above; flip status to ✓ landed once tests and lint pass.
5. Add a one-line entry to [`llms.txt`](../llms.txt) under "Per-package overviews" so consumer agents find it.
6. Optional: drop a runnable example under `examples/ext-<name>/`.

Last reviewed against ncruces/go-sqlite3 main on 2026-05-29.
