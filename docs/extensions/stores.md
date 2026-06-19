---
title: Store extensions
description: Persistent specialised stores — a Bloom filter and the spellfix1 fuzzy vocabulary — that survive db.Close.
sidebar:
  order: 21
---

# Stores

Persistent specialised stores. Both are virtual tables whose data survives `db.Close` (they keep a shadow table) and both expose a typed Go handle.

## Bloom filter — `ext/bloom`

A probabilistic set-membership store. Add many items cheaply, then test membership with a tunable false-positive rate; the filter persists across reopens.

```go
import "gosqlite.org/ext/bloom"
// typed handle: bloom.Filter (Create / Add / Contains / Drop)
```

## Fuzzy vocabulary — `ext/spellfix1`

A Go-native re-implementation of SQLite's `spellfix1`: Soundex prefilter + Damerau-Levenshtein edit-distance ranking, with a persistent vocabulary shadow table. Same SQL surface as upstream (`CREATE VIRTUAL TABLE x USING spellfix1`, `INSERT INTO x(word[, rank])`, `SELECT word, distance FROM x WHERE word MATCH ?`), plus a typed `spellfix1.Vocab` handle (`Create` / `Open` / `Add` / `Correct` / `Drop`).

```go
db.Exec(`CREATE VIRTUAL TABLE dict USING spellfix1`)
db.Exec(`INSERT INTO dict(word) VALUES ('kitten'), ('sitting')`)
// SELECT word, distance FROM dict WHERE word MATCH 'kittin'
```

The stateless cousin (edit distances and phonetic codes as plain scalar functions, no vocabulary) is [`ext/fuzzy`](scalars.md). Demos: [`examples/features/extensions/bloom/`](../../examples/features/extensions/bloom/main.go), [`examples/features/extensions/spellfix1/`](../../examples/features/extensions/spellfix1/main.go).
