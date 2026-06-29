module vaultblobstore

go 1.26.4

replace gosqlite.org => ../..

replace gosqlite.org/blobstore => ../../blobstore

replace gosqlite.org/vfs/vault => ../../vfs/vault

replace gosqlite.org/vfs/crypto => ../../vfs/crypto

replace gosqlite.org/crypto/keyring => ../../crypto/keyring

require (
	gosqlite.org v0.12.0
	gosqlite.org/blobstore v0.12.0
	gosqlite.org/crypto/keyring v0.12.0
	gosqlite.org/vfs/vault v0.12.0
)

require (
	filippo.io/age v1.3.1 // indirect
	filippo.io/edwards25519 v1.1.0 // indirect
	filippo.io/hpke v0.4.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-again/az v0.4.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	gosqlite.org/vfs/crypto v0.12.0 // indirect
	lukechampine.com/adiantum v1.1.1 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.52.0 // indirect
)
