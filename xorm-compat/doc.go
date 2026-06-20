// Package xormcompat is an isolated module — deliberately NOT part of the
// gosqlite.org module — that proves gosqlite.org works as a drop-in
// xorm.io/xorm SQLite driver.
//
// It lives in its own go.mod with `replace gosqlite.org => ..`, so the
// xorm dependency (and its support libraries) never enter the main
// module's graph or any downstream consumer's build. `go build ./...` /
// `go test ./...` run from the repo root skip this directory because it is
// a separate module. The `xorm-compat` CI job runs `go test ./...` here;
// locally, `just xorm-compat`.
package xormcompat
