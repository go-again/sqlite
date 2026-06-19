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

Coverage matrix and status (✓ landed / ⚠ partial / ✗ deferred) lives at [`dev/coverage/ext.md`](../dev/coverage/ext.md). Shipped today:

| Package | What it gives you |
|---|---|
| [`array`](array/) | Eponymous vtab that exposes a bound Go slice as a single-column SQL table. Two binding styles: transparent via `sqlite.Pointer(slice)` (preferred — SQLite's destructor releases on stmt finalize) or explicit `array.Bind(c, slice) → token, release()` for long-lived bindings. |
| [`csv`](csv/) | Virtual table over RFC 4180 CSV files. `CREATE VIRTUAL TABLE name USING csv(filename='x.csv', header=on, schema='CREATE TABLE x(a INTEGER, b TEXT)')`. `csv.Register(c)` opens via `os.Open`; `csv.RegisterFS(c, fsys)` sandboxes to any `fs.FS`. |
| [`regexp`](regexp/) | `regexp_like`, `regexp_count`, `regexp_instr`, `regexp_substr`, `regexp_replace`, and the binary `REGEXP` operator over Go's `regexp` (RE2 syntax). |
| [`uuid`](uuid/) | `uuid([ver, ns, data])`, `gen_random_uuid`, `uuid_str/blob/extract_*` over `github.com/google/uuid`. v1 / v3 / v4 / v5 / v6 / v7 (DCE Security v2 not implemented). |
| [`hash`](hash/) | `md4`, `md5`, `sha1`, `sha224`, `sha256`, `sha384`, `sha512`, `sha3`, `blake2s`, `blake2b`, `blake3` (XOF, byte-sized), `xxh64`, `ripemd160` with size variants. `crypto.Hash` algorithms gated on `.Available()`; `blake3` / `xxh64` always registered. |
| [`ipaddr`](ipaddr/) | `ipcontains`, `ipoverlaps`, `ipfamily`, `iphost`, `ipmasklen`, `ipnetwork` over `net/netip`. Fixes an upstream `ipoverlaps` self-reference bug. |
| [`zorder`](zorder/) | `zorder(d1, …, dN)` / `unzorder(z, N, i)` Morton encoding for 2–24 dimensions. |
| [`stats`](stats/) | Aggregates + windows: `var_pop`/`var_samp`/`stddev_*`, `skewness_*`, `kurtosis_*`, `covar_*`, `corr`, the full `regr_*` family, `percentile_cont/disc`, `median`, `mode`, `every`, `some`. Welford + Terriberry + Kahan; streaming Inverse path for sliding windows. Plus `cbrt`, `cot` scalars. |
| [`unicode`](unicode/) | Unicode-aware `upper` / `lower` / `initcap` / `casefold` / `normalize` (NFC/NFD/NFKC/NFKD) / `unaccent` via `golang.org/x/text`. Preset `NOCASE_UNICODE` and `NOCASE_ACCENT` collations register automatically. `RegisterLocaleCollation(c, locale, name)` for BCP-47-tagged collators. Unicode-aware `LIKE` override is opt-in via `unicode.Register(c, unicode.WithLike())`. |
| [`blobio`](blobio/) | `readblob(schema, table, column, rowid)` / `writeblob(...)` scalars over `sqlite3_blob_open`, plus the typed `(*Conn).OpenBlob` API. Stream large `zeroblob(N)` allocations in/out of TEXT/BLOB columns without materialising in memory. |
| [`bloom`](bloom/) | Persistent Bloom-filter vtab. `CREATE VIRTUAL TABLE name USING bloom(capacity=N, error_rate=R, ...)`; shadow-blob persistence so the filter survives reopens. Kirsch-Mitzenmacher double-hashing with stable per-table salts. |
| [`closure`](closure/) | `transitive_closure(root_col, depth)` vtab — full and depth-bounded graph walks over an adjacency-list table. Eponymous; `WHERE root=? AND depth<=?` parametrises each walk. |
| [`fileio`](fileio/) | `readfile(path)` / `writefile(path, data, mode)` scalars + `fsdir(path, depth)` recursive walker vtab. `fileio.Register(c)` uses the live OS; `fileio.RegisterFS(c, fsys)` sandboxes to any `fs.FS` (writefile then errors). |
| [`lines`](lines/) | Eponymous vtab `lines(text)` that yields one row per line. Streams via `bufio.Scanner` so large blobs of text don't materialise twice. |
| [`pivot`](pivot/) | `pivot_vtab(...)` three-SELECT cross-tab — rows × columns × cell aggregate. Constructs a dynamic schema from the column-key projection and caches the cell-aggregate stmt per cell. |
| [`spellfix1`](spellfix1/) | `CREATE VIRTUAL TABLE name USING spellfix1` — fuzzy lookup vtab. Soundex prefilter + Damerau-Levenshtein edit-distance ranking, persistent vocabulary shadow table. Go-native re-implementation of SQLite's spellfix1 (same SQL surface, simpler internals). |
| [`statement`](statement/) | `CREATE VIRTUAL TABLE name USING statement(sql='...')` — parametrised views. `?N` anonymous and `:name` named binds become HIDDEN columns on the vtab; SELECTs `... WHERE :pat=?` drive the underlying prepared stmt. |
| [`rtree`](rtree/) | R-Tree spatial index: the built-in `rtree`/`geopoly` vtabs plus a typed `rtree.Table` (`Create`/`Insert`/`InsertPoint`/`Search` bounding-box/`SearchCircle`/`Delete`/`Drop`) and a ready-made `circle(cx, cy, r)` geometry. Arbitrary geometry via root `(*Conn).RegisterRTreeGeometry` / `RegisterRTreeQuery`. |
| [`series`](series/) | `generate_series(start, stop[, step])` table-valued function — an integer sequence usable as a SQL table source. |
| [`text`](text/) | Rune-aware string scalars SQLite lacks: `text_reverse`, `text_repeat`, `text_lpad`, `text_rpad`, `text_split`. |
| [`fuzzy`](fuzzy/) | Approximate string matching: `levenshtein`, `damerau_levenshtein`, `hamming`, `jaro`, `jaro_winkler`, `soundex`. Rune-aware distances; the stateless-scalar cousin of `spellfix1`. |
| [`encode`](encode/) | `encode(data, format)` / `decode(text, format)` for base64 / base64url / base32 / base32hex / base16 / ascii85 / url. The codec half of sqlean `crypto`; digests live in `ext/hash`. |
| [`regexp/gorm`](regexp/gorm/) | gorm helpers built on `ext/regexp`. `regexpgorm.WhereRegex(db, col, pattern)` adds a `col REGEXP ?` clause without touching the dialect. |

## Attribution

Several extensions are Go-native ports of [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) extensions. The function lineup and semantics follow upstream closely; the runtime is different (we target modernc.org/sqlite, ncruces targets a Wazero-based WASM build). See [`LICENSE.ncruces`](../LICENSE.ncruces) and the [NOTICE](../NOTICE) attribution.

## Adding a new extension

See the bottom of [`dev/coverage/ext.md`](../dev/coverage/ext.md) for the per-extension scaffolding checklist.
