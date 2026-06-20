---
title: Scalar, aggregate & collation extensions
description: SQL functions you call from any query — regexp, uuid, hash, ipaddr, zorder, stats, unicode, encode, text, fuzzy, decimal, money, time, eval.
sidebar:
  order: 19
---

# Scalars, aggregates & collations

These add SQL functions, operators, and collations. Blank-import the `/auto` package and they show up in any `Where` / `Order` / `Select`:

```go
import (
	_ "gosqlite.org/ext/hash/auto"
	_ "gosqlite.org/ext/regexp/auto"
	_ "gosqlite.org/ext/uuid/auto"
)

db.Where("email REGEXP ?", `^.*@example\.com$`).Find(&users)
db.Select(`hex(sha256(body)) AS digest`).Find(&logs)
db.Where("ipcontains(?, src_ip)", "10.0.0.0/8").Find(&events)
```

## Catalog

| Package | What it adds |
|---|---|
| [`regexp`](../../ext/regexp/) | RE2 `REGEXP` operator + `regexp_*` scalars |
| [`uuid`](../../ext/uuid/) | `uuid()` v1–v7 (incl. DCE v2) + `uuid_str` / `uuid_blob` + extractors |
| [`hash`](../../ext/hash/) | `md5` / `sha1` / `sha256` / `sha512` / `sha3` / `blake2b` / `blake3` / `xxh64` |
| [`ipaddr`](../../ext/ipaddr/) | `ipcontains` / `ipfamily` / `ipnetwork` over `net/netip` |
| [`zorder`](../../ext/zorder/) | Morton (Z-order) encoding for 2–24 dimensions |
| [`stats`](../../ext/stats/) | variance / percentile / regr_* / median / mode aggregates + sliding-window inverses |
| [`unicode`](../../ext/unicode/) | Unicode-aware `upper` / `lower` / `normalize` / `unaccent` + `NOCASE_UNICODE` collation + locale collators |
| [`encode`](../../ext/encode/) | `encode` / `decode` — base64 / base32 / base16 / ascii85 / url |
| [`text`](../../ext/text/) | rune-aware `text_reverse` / `text_repeat` / `text_lpad` / `text_rpad` / `text_split` |
| [`fuzzy`](../../ext/fuzzy/) | `levenshtein` / `damerau_levenshtein` / `hamming` / `jaro` / `jaro_winkler` / `soundex` / `caverphone` |
| [`decimal`](../../ext/decimal/) | exact base-10 `decimal_add`/`sub`/`mul`/`div`/`round`/… + `decimal_sum` aggregate (`math/big`) |
| [`money`](../../ext/money/) | fixed-2dp currency + `money_format` |
| [`time`](../../ext/time/) | `time_unix` / `add` / `diff` / `part` / `trunc` / `format` over Go `time` |
| [`eval`](../../ext/eval/) | `eval(sql[, sep])` runs dynamic SQL on the calling connection (trusted input only) |

Runnable demos under [`examples/features/extensions/`](../../examples/features/extensions/) (e.g. `hash`, `fuzzy`, `extras` for decimal/money/time/eval). End-to-end against a gorm model: [`gorm/examples/ext-scalars/`](../../gorm/examples/ext-scalars/main.go).
