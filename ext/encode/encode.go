// Package encode adds `encode(data, format)` and `decode(text, format)`
// scalar functions covering the common binary-to-text codecs SQLite lacks
// (only hex/quote are built in):
//
//	SELECT encode('hello', 'base64');   -- 'aGVsbG8='
//	SELECT decode('aGVsbG8=', 'base64'); -- x'68656c6c6f' (the blob "hello")
//
// Supported formats: base64, base64url, base32, base32hex, base16 / hex,
// ascii85 / base85, and url (percent-encoding). This is the codec half of the
// sqlean `crypto` surface; the digest half is covered by ext/hash.
package encode

import (
	"encoding/ascii85"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sqlite "gosqlite.org"
)

// Register installs the encode and decode scalar functions on c.
//
// Per-connection registration. For pool-wide install, blank-import the auto
// sub-package:
//
//	import _ "gosqlite.org/ext/encode/auto"
func Register(c *sqlite.Conn) error {
	return errors.Join(
		c.RegisterFunc("encode", encode, true),
		c.RegisterFunc("decode", decode, true),
	)
}

// encode renders data in the named text format.
func encode(data []byte, format string) (string, error) {
	switch strings.ToLower(format) {
	case "base64":
		return base64.StdEncoding.EncodeToString(data), nil
	case "base64url":
		return base64.URLEncoding.EncodeToString(data), nil
	case "base32":
		return base32.StdEncoding.EncodeToString(data), nil
	case "base32hex":
		return base32.HexEncoding.EncodeToString(data), nil
	case "base16", "hex":
		return hex.EncodeToString(data), nil
	case "ascii85", "base85":
		var b strings.Builder
		enc := ascii85.NewEncoder(&b)
		if _, err := enc.Write(data); err != nil {
			return "", err
		}
		if err := enc.Close(); err != nil {
			return "", err
		}
		return b.String(), nil
	case "url":
		return url.QueryEscape(string(data)), nil
	default:
		return "", fmt.Errorf("encode: unknown format %q", format)
	}
}

// decode parses text in the named format back into bytes.
func decode(text, format string) ([]byte, error) {
	switch strings.ToLower(format) {
	case "base64":
		return base64.StdEncoding.DecodeString(text)
	case "base64url":
		return base64.URLEncoding.DecodeString(text)
	case "base32":
		return base32.StdEncoding.DecodeString(text)
	case "base32hex":
		return base32.HexEncoding.DecodeString(text)
	case "base16", "hex":
		return hex.DecodeString(text)
	case "ascii85", "base85":
		dst := make([]byte, len(text))
		n, _, err := ascii85.Decode(dst, []byte(text), true)
		if err != nil {
			return nil, err
		}
		return dst[:n], nil
	case "url":
		s, err := url.QueryUnescape(text)
		if err != nil {
			return nil, err
		}
		return []byte(s), nil
	default:
		return nil, fmt.Errorf("decode: unknown format %q", format)
	}
}
