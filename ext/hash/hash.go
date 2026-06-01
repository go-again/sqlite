// Package hash provides cryptographic hash SQL functions.
//
// # Functions
//
//   - md4(data)
//   - md5(data)
//   - sha1(data)
//   - sha3(data, size) — size: 224, 256 (default), 384, 512
//   - sha224(data)
//   - sha256(data, size) — size: 224, 256 (default)
//   - sha384(data)
//   - sha512(data, size) — size: 224, 256, 384, 512 (default)
//   - blake2s(data)
//   - blake2b(data, size) — size: 256, 384, 512 (default)
//   - ripemd160(data)
//
// Each SQL function is registered only when the corresponding
// [crypto.Hash] is available — registration is gated on
// `crypto.Hash.Available()`, which is true only when the implementing
// package has been imported. To make a function available, side-effect-
// import its package:
//
//	import (
//	    _ "crypto/md5"
//	    _ "crypto/sha1"
//	    _ "crypto/sha256"
//	    _ "crypto/sha512"
//	    _ "golang.org/x/crypto/blake2b"
//	    _ "golang.org/x/crypto/ripemd160"
//	)
//
// Ported from [ncruces/ext/hash].
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/hash"
//	)
//
//	if err := hash.Register(conn); err != nil { ... }
//	row := db.QueryRow(`SELECT lower(hex(sha256('hello')))`)
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package (which also blank-imports every
// supported hash algorithm so all functions register):
//
//	import _ "github.com/go-again/sqlite/ext/hash/auto"
//
// [ncruces/ext/hash]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/hash
package hash

import (
	"crypto"
	"errors"
	"fmt"

	sqlite "github.com/go-again/sqlite"
)

// Register installs every hash function whose underlying [crypto.Hash] is
// [crypto.Hash.Available]. Functions whose backing implementation has not
// been imported are silently skipped — querying them returns a SQLite
// "no such function" error.
func Register(c *sqlite.Conn) error {
	var errs []error
	reg := func(name string, fn any) {
		errs = append(errs, c.RegisterFunc(name, fn, true))
	}
	if crypto.MD4.Available() {
		reg("md4", func(data []byte) []byte { return hashBytes(crypto.MD4, data) })
	}
	if crypto.MD5.Available() {
		reg("md5", func(data []byte) []byte { return hashBytes(crypto.MD5, data) })
	}
	if crypto.SHA1.Available() {
		reg("sha1", func(data []byte) []byte { return hashBytes(crypto.SHA1, data) })
	}
	if crypto.RIPEMD160.Available() {
		reg("ripemd160", func(data []byte) []byte { return hashBytes(crypto.RIPEMD160, data) })
	}
	if crypto.SHA256.Available() {
		reg("sha224", func(data []byte) []byte { return hashBytes(crypto.SHA224, data) })
		reg("sha256", func(data []byte, sizeOpt ...int64) ([]byte, error) {
			return variableHash("sha256", data, sizeOpt, 256, map[int64]crypto.Hash{
				224: crypto.SHA224, 256: crypto.SHA256,
			})
		})
	}
	if crypto.SHA512.Available() {
		reg("sha384", func(data []byte) []byte { return hashBytes(crypto.SHA384, data) })
		reg("sha512", func(data []byte, sizeOpt ...int64) ([]byte, error) {
			return variableHash("sha512", data, sizeOpt, 512, map[int64]crypto.Hash{
				224: crypto.SHA512_224, 256: crypto.SHA512_256,
				384: crypto.SHA384, 512: crypto.SHA512,
			})
		})
	}
	if crypto.SHA3_512.Available() {
		reg("sha3", func(data []byte, sizeOpt ...int64) ([]byte, error) {
			return variableHash("sha3", data, sizeOpt, 256, map[int64]crypto.Hash{
				224: crypto.SHA3_224, 256: crypto.SHA3_256,
				384: crypto.SHA3_384, 512: crypto.SHA3_512,
			})
		})
	}
	if crypto.BLAKE2s_256.Available() {
		reg("blake2s", func(data []byte) []byte { return hashBytes(crypto.BLAKE2s_256, data) })
	}
	if crypto.BLAKE2b_512.Available() {
		reg("blake2b", func(data []byte, sizeOpt ...int64) ([]byte, error) {
			return variableHash("blake2b", data, sizeOpt, 512, map[int64]crypto.Hash{
				256: crypto.BLAKE2b_256, 384: crypto.BLAKE2b_384, 512: crypto.BLAKE2b_512,
			})
		})
	}
	return errors.Join(errs...)
}

// hashBytes returns the raw hash digest of data under fn.
func hashBytes(fn crypto.Hash, data []byte) []byte {
	h := fn.New()
	h.Write(data)
	return h.Sum(nil)
}

// variableHash dispatches one of several output sizes for the hash families
// that accept a size argument (sha3, sha256, sha512, blake2b). dflt is the
// size used when no size argument is supplied.
func variableHash(name string, data []byte, sizeOpt []int64, dflt int64, table map[int64]crypto.Hash) ([]byte, error) {
	size := dflt
	if len(sizeOpt) > 0 {
		size = sizeOpt[0]
	}
	fn, ok := table[size]
	if !ok {
		valid := make([]int64, 0, len(table))
		for k := range table {
			valid = append(valid, k)
		}
		return nil, fmt.Errorf("%s: invalid size %d (valid: %v)", name, size, valid)
	}
	return hashBytes(fn, data), nil
}
