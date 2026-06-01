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

Coverage matrix and status (✓ landed / ⚠ partial / ✗ deferred) lives at [`docs/coverage-ext.md`](../docs/coverage-ext.md). This README only lists shipped packages.

_(Empty for now — see the coverage doc for what's planned.)_

## Attribution

Several extensions are Go-native ports of [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) extensions. The function lineup and semantics follow upstream closely; the runtime is different (we target modernc.org/sqlite, ncruces targets a Wazero-based WASM build). See [`LICENSE.ncruces`](../LICENSE.ncruces) and the [NOTICE](../NOTICE) attribution.

## Adding a new extension

See the bottom of [`docs/coverage-ext.md`](../docs/coverage-ext.md) for the per-extension scaffolding checklist.
