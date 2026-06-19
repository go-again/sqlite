---
name: extensions
description: Use when the task needs a SQL function or virtual table from the go-again/sqlite ext/ catalog — regexp, uuid, hash, ipaddr, stats, unicode, fuzzy, decimal, money, time, eval, csv, lines, rtree, series, array, bloom, spellfix1, fileio, blobio.
---

# Loadable Go extensions (`ext/`)

Each extension is an independent sub-package. Two registration shapes:

```go
// Pool-wide: blank-import the /auto variant (every conn picks it up).
import _ "github.com/go-again/sqlite/ext/regexp/auto"

// Per-conn: register explicitly.
import "github.com/go-again/sqlite/ext/regexp"
regexp.Register(conn) // conn is *sqlite.Conn
```

After registration, just use the SQL:

```go
db.Where("email REGEXP ?", `^.*@example\.com$`)
db.Select(`hex(sha256(body)) AS digest`)
```

## Catalog

**Scalars / aggregates / collations** (blank-import `/auto`, use in any SQL): `regexp` (RE2 + `regexp_*`), `uuid` (v1–v7 + `uuid_str`/`uuid_blob`), `hash` (md5/sha*/blake2b/blake3/xxh64), `ipaddr` (`ipcontains`/`ipfamily`), `zorder` (Morton), `stats` (variance/percentile/regr_*/median/mode), `unicode` (`upper`/`lower`/`normalize`/`unaccent` + `NOCASE_UNICODE`), `encode` (base64/32/16/ascii85/url), `text` (`text_reverse`/`lpad`/`split`/…), `fuzzy` (`levenshtein`/`jaro_winkler`/`soundex`/`caverphone`), `decimal` (exact base-10 + `decimal_sum`), `money` (fixed-2dp + `money_format`), `time` (`time_unix`/`add`/`diff`/`part`), `eval` (dynamic SQL — trusted input only).

**Virtual tables** (`CREATE VIRTUAL TABLE … USING module(…)`): `array` (Go slice via `sqlite.Pointer`), `csv`, `lines`, `statement`, `closure` (graph), `pivot`, `rtree` (spatial), `series` (`generate_series`).

**Stores / I/O:** `bloom` (persistent Bloom filter), `spellfix1` (fuzzy vocabulary), `blobio` (incremental BLOB I/O), `fileio` (sandboxed file functions).

## Notes

- Many vtab extensions also expose a **typed Go handle** (`csv.Table`, `lines.Table`, `closure.Graph`, `bloom.Filter`, `spellfix1.Vocab`, `rtree.Table`) that hides the DDL — prefer it for new code.
- `csv` / `lines` / `fileio` / `blobio` need an `fs.FS` sandbox at registration, which `/auto` can't carry — register via `RegisterFS(conn, fsys)` or a `Driver.RegisterConnectionHook`.
- To write your OWN SQL function in Go instead, use `Conn.RegisterFunc` / `RegisterAggregator` / `RegisterWindowFunction` (no ext package needed).

Full catalog + status: [`docs/extensions/index.md`](../../docs/extensions/index.md), [`dev/coverage/ext.md`](../../dev/coverage/ext.md).
