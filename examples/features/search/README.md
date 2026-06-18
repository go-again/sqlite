# Search — vector, full-text, hybrid

Typed APIs over sqlite-vec and FTS5, plus rank fusion to combine them.

| example | what it shows |
|---|---|
| [`vec-search`](vec-search/) | the typed `vec.Table` API — create, batch-insert, streaming KNN, int8 quantization, metadata-filtered KNN |
| [`vec-keyed`](vec-keyed/) | `KeyedTable[K]` — vector search keyed on a string/UUID instead of an int rowid |
| [`fts-search`](fts-search/) | the typed `fts.Index[K, V]` — indexing, MATCH queries, ranking, snippet/highlight |
| [`fts-vocab`](fts-vocab/) | `fts.Vocab` — term frequencies and autocomplete from the FTS5 vocabulary tables |
| [`fts-tokenizer`](fts-tokenizer/) | a custom Go FTS5 tokenizer that splits identifiers (`getUserName` → `user`/`name`) — something the built-ins can't do |
| [`fusion-hybrid`](fusion-hybrid/) | combine `vec.KNN` and `fts.Search` rankings with `fusion.RRF2` (Reciprocal Rank Fusion) |
