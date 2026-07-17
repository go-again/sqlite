module gosqlite.org/gorm

go 1.25.0

require (
	gorm.io/gorm v1.31.1
	gosqlite.org v0.13.0
	modernc.org/sqlite v1.54.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// Dev convenience: build the dialector against the in-tree core. Ignored by
// consumers (replace directives in non-main modules don't apply), so it's
// safe to commit. On publish, bump the require above to the matching core tag.
replace gosqlite.org => ..
