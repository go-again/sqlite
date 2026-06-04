// Package auto wires the hash extension via a [sqlite.Driver.ConnectHook]
// so every new connection registers the SQL hash functions. The blank
// imports below ensure every supported algorithm is wired into Go's
// [crypto.Hash.Available] table so the registration covers the full set.
//
// Blank-import to opt in:
//
//	import _ "github.com/go-again/sqlite/ext/hash/auto"
//
// For finer control over which algorithms appear (e.g. omitting MD4/MD5 in
// a security-conscious deployment), call
// [github.com/go-again/sqlite/ext/hash.Register] directly and import only
// the implementing packages you want.
package auto

import (
	// Standard library hashes.
	_ "crypto/md5"    //nolint:gosec // surfaced as MD5() SQL function; consumers opt in.
	_ "crypto/sha1"   //nolint:gosec // surfaced as SHA1() SQL function; consumers opt in.
	_ "crypto/sha256" // sha224 / sha256
	_ "crypto/sha512" // sha384 / sha512 / sha512_224 / sha512_256

	// x/crypto algorithms. md4 and ripemd160 are deprecated upstream
	// (legacy / cryptographically broken); we surface them only for
	// compatibility with older systems that already have data hashed
	// under those algorithms. Consumers opt in by importing this auto
	// sub-package — if you want a security-conscious build, blank-import
	// only the modern algorithms and call hash.Register directly.
	_ "golang.org/x/crypto/blake2b" //nolint:gosec
	_ "golang.org/x/crypto/blake2s" //nolint:gosec
	//lint:ignore SA1019 compat surface; consumers opt in by importing this sub-package
	_ "golang.org/x/crypto/md4" //nolint:gosec,staticcheck
	//lint:ignore SA1019 compat surface; consumers opt in by importing this sub-package
	_ "golang.org/x/crypto/ripemd160" //nolint:gosec,staticcheck
	_ "golang.org/x/crypto/sha3"      //nolint:gosec

	sqlite "github.com/go-again/sqlite"
	"github.com/go-again/sqlite/ext/hash"
)

func init() {
	sqlite.RegisterAutoHook(hash.Register)
}
