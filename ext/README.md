# ext/ — loadable Go extensions

Optional SQL extensions you can register on a connection. Each sub-package is independent: pick the ones you need, leave the rest off your import graph.

Two registration shapes per extension:

```go
// Explicit — register on a specific *sqlite.Conn (the per-conn idiom).
import "github.com/go-again/sqlite/ext/regexp"
regexp.Register(conn)

// Implicit — blank-import auto-wires via Driver.ConnectHook so every
// connection the driver opens picks the extension up.
import _ "github.com/go-again/sqlite/ext/regexp/auto"
```

The explicit form is canonical; the `/auto` blank-import is a thin shim that calls `Register` from a `ConnectHook` so the extension survives connection pool churn.

## Available extensions

Coverage matrix and status (✓ landed / ⚠ partial / ✗ deferred) lives at [`docs/coverage-ext.md`](../docs/coverage-ext.md). Shipped today:

| Package | What it gives you |
|---|---|
| [`array`](array/) | Eponymous vtab that exposes a bound Go slice as a single-column SQL table. Two binding styles: transparent via `sqlite.Pointer(slice)` (preferred — SQLite's destructor releases on stmt finalize) or explicit `array.Bind(c, slice) → token, release()` for long-lived bindings. |
| [`csv`](csv/) | Virtual table over RFC 4180 CSV files. `CREATE VIRTUAL TABLE name USING csv(filename='x.csv', header=on, schema='CREATE TABLE x(a INTEGER, b TEXT)')`. `csv.Register(c)` opens via `os.Open`; `csv.RegisterFS(c, fsys)` sandboxes to any `fs.FS`. |
| [`regexp`](regexp/) | `regexp_like`, `regexp_count`, `regexp_instr`, `regexp_substr`, `regexp_replace`, and the binary `REGEXP` operator over Go's `regexp` (RE2 syntax). |
| [`uuid`](uuid/) | `uuid([ver, ns, data])`, `gen_random_uuid`, `uuid_str/blob/extract_*` over `github.com/google/uuid`. v1 / v3 / v4 / v5 / v6 / v7 (DCE Security v2 not implemented). |
| [`hash`](hash/) | `md4`, `md5`, `sha1`, `sha224`, `sha256`, `sha384`, `sha512`, `sha3`, `blake2s`, `blake2b`, `ripemd160` with size variants. Per-algorithm registration gated on `crypto.Hash.Available()`. |
| [`ipaddr`](ipaddr/) | `ipcontains`, `ipoverlaps`, `ipfamily`, `iphost`, `ipmasklen`, `ipnetwork` over `net/netip`. Fixes an upstream `ipoverlaps` self-reference bug. |
| [`zorder`](zorder/) | `zorder(d1, …, dN)` / `unzorder(z, N, i)` Morton encoding for 2–24 dimensions. |
| [`stats`](stats/) | Aggregates + windows: `var_pop`/`var_samp`/`stddev_*`, `skewness_*`, `kurtosis_*`, `covar_*`, `corr`, the full `regr_*` family, `percentile_cont/disc`, `median`, `mode`, `every`, `some`. Welford + Terriberry + Kahan; streaming Inverse path for sliding windows. Plus `cbrt`, `cot` scalars. |
| [`unicode`](unicode/) | Unicode-aware `upper` / `lower` / `initcap` / `casefold` / `normalize` (NFC/NFD/NFKC/NFKD) / `unaccent` via `golang.org/x/text`. Preset `NOCASE_UNICODE` and `NOCASE_ACCENT` collations register automatically. `RegisterLocaleCollation(c, locale, name)` for BCP-47-tagged collators. Unicode-aware `LIKE` override is opt-in via `unicode.Register(c, unicode.WithLike())`. |

## Attribution

Several extensions are Go-native ports of [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) extensions. The function lineup and semantics follow upstream closely; the runtime is different (we target modernc.org/sqlite, ncruces targets a Wazero-based WASM build). See [`LICENSE.ncruces`](../LICENSE.ncruces) and the [NOTICE](../NOTICE) attribution.

## Adding a new extension

See the bottom of [`docs/coverage-ext.md`](../docs/coverage-ext.md) for the per-extension scaffolding checklist.
