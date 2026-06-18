# Extensions — the `ext/` catalog

Opt-in loadable Go extensions. Each registers per-connection (or pool-wide via its `/auto` blank-import). Full status matrix: [`docs/coverage-ext.md`](../../../docs/coverage-ext.md).

**Scalars / aggregates / collations**

| example | functions |
|---|---|
| [`hash`](hash/) | `md5` / `sha1` / `sha256` / `sha512` / `sha3` / `blake2b` / `blake3` / `xxh64` |
| [`encode`](encode/) | `encode` / `decode` — base64 / base32 / base16 / ascii85 / url |
| [`text`](text/) | rune-aware `text_reverse` / `text_repeat` / `text_lpad` / `text_rpad` / `text_split` |
| [`unicode`](unicode/) | Unicode-aware `upper` / `lower` / `normalize` / `unaccent` + locale collations |
| [`regexp`](regexp/) | RE2 `REGEXP` operator + `regexp_*` scalars |
| [`fuzzy`](fuzzy/) | `levenshtein` / `jaro_winkler` / `soundex` / `caverphone` and friends |
| [`uuid`](uuid/) | `uuid()` v1–v7 (incl. DCE v2) + `uuid_str` / `uuid_blob` / extractors |
| [`ipaddr`](ipaddr/) | `ipcontains` / `ipfamily` / `ipnetwork` over `net/netip` |
| [`zorder`](zorder/) | Morton (Z-order) encoding for 2–24 dimensions |
| [`stats`](stats/) | variance / percentile / regr_* / median / mode aggregates with window inverses |
| [`extras`](extras/) | the newer scalar families together — `decimal` (exact base-10 + `decimal_sum`), `money` (fixed 2-dp + `money_format`), `time` (`time_unix`/`add`/`diff`/…), and `eval()` (dynamic SQL) |

**Table-valued functions, vtab readers, stores, I/O**

| example | what it shows |
|---|---|
| [`series`](series/) | `generate_series(start, stop[, step])` |
| [`array`](array/) | bind a Go slice as a table-valued function |
| [`csv`](csv/) | read a CSV file/string as a virtual table (delimiters, affinity, `skip=`), + the typed `csv.Table` |
| [`lines`](lines/) | read a file line-by-line as rows, + the typed `lines.Table` |
| [`statement`](statement/) | parameterised stored-statement vtab |
| [`pivot`](pivot/) | pivot a table on a column |
| [`rtree`](rtree/) | R-Tree spatial index with a typed `rtree.Table` + a `circle()` geometry |
| [`closure`](closure/) | transitive-closure graph queries with a typed `closure.Graph` |
| [`bloom`](bloom/) | a persistent Bloom-filter store (`bloom.Filter`) |
| [`spellfix1`](spellfix1/) | fuzzy vocabulary vtab with a typed `spellfix1.Vocab` |
| [`blobio`](blobio/) | incremental BLOB I/O helpers |
| [`fileio`](fileio/) | sandboxed file read/write SQL functions |
