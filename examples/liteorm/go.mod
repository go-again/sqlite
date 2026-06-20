module liteormexample

go 1.25.0

require (
	gosqlite.org v0.7.1
	liteorm.org v0.8.0
	liteorm.org/dialect/sqlite v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	lukechampine.com/adiantum v1.1.1 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.52.0 // indirect
)

// liteorm (liteorm.org) is a separate module. This example builds against the
// local .liteorm reference clone so it tracks liteorm's current typed-search
// API; gosqlite resolves to this repo. Mirrors xorm-compat's local-replace setup.
replace gosqlite.org => ../..

replace liteorm.org => ../../.liteorm

replace liteorm.org/dialect/sqlite => ../../.liteorm/dialect/sqlite
