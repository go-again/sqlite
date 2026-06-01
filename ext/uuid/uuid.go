// Package uuid provides RFC 4122 UUID SQL functions backed by
// [github.com/google/uuid].
//
// # Functions
//
//   - uuid([version [, namespace [, data]]]) → TEXT
//     Generate a UUID. Defaults to v4 when called with no args.
//     Version selector covers v1 (time-based), v4 (random),
//     v6 (reordered time-based), v7 (Unix epoch time-based),
//     and v3 / v5 (name-based MD5 / SHA-1, requires namespace + data).
//   - gen_random_uuid() → TEXT: shorthand for uuid() = uuid(4).
//   - uuid_str(u) → TEXT: parse a UUID (TEXT or 16-byte BLOB) into
//     the canonical 8-4-4-4-12 hex string.
//   - uuid_blob(u) → BLOB: parse a UUID into its 16-byte BLOB form.
//   - uuid_extract_version(u) → INTEGER: the RFC 4122 version field.
//   - uuid_extract_timestamp(u) → INTEGER: Unix seconds for v1/v6/v7
//     UUIDs; NULL otherwise.
//
// For v3 / v5 (name-based) UUIDs, the namespace may be any of:
//
//   - a TEXT UUID literal (e.g. '6ba7b810-9dad-11d1-80b4-00c04fd430c8')
//   - a 16-byte BLOB
//   - one of the well-known namespace shortcuts: "dns", "url", "oid",
//     "x500" (case-insensitive)
//
// Ported from [ncruces/ext/uuid]. The DCE Security (v2) variant is NOT
// surfaced — it's rarely used and adds substantial domain-specific code
// (UID/GID lookups, domain enums). Open an issue if you need it.
//
// # Usage
//
//	import (
//	    sqlite "github.com/go-again/sqlite"
//	    "github.com/go-again/sqlite/ext/uuid"
//	)
//
//	if err := uuid.Register(conn); err != nil { ... }
//	row := db.QueryRow(`SELECT uuid(4)`) // random v4
//
// For pool-wide install via [github.com/go-again/sqlite.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "github.com/go-again/sqlite/ext/uuid/auto"
//
// [ncruces/ext/uuid]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/uuid
package uuid

import (
	"bytes"
	"errors"
	"fmt"

	gid "github.com/google/uuid"

	sqlite "github.com/go-again/sqlite"
)

// Register installs the uuid family of SQL functions on c.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		// uuid([ver [, ns [, data]]]) — variadic any so we can accept
		// the polymorphic namespace argument (TEXT, BLOB, or shortcut).
		c.RegisterFunc("uuid", generate, false),
		c.RegisterFunc("gen_random_uuid", randomV4, false),
		c.RegisterFunc("uuid_str", toString, true),
		c.RegisterFunc("uuid_blob", toBlob, true),
		c.RegisterFunc("uuid_extract_version", extractVersion, true),
		c.RegisterFunc("uuid_extract_timestamp", extractTimestamp, true),
	)
}

func randomV4() (string, error) {
	u, err := gid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("uuid: NewRandom: %w", err)
	}
	return u.String(), nil
}

func generate(args ...any) (string, error) {
	ver := int64(4)
	if len(args) > 0 {
		v, err := toInt64(args[0])
		if err != nil {
			return "", fmt.Errorf("uuid: bad version: %w", err)
		}
		ver = v
	}
	switch ver {
	case 1:
		u, err := gid.NewUUID()
		if err != nil {
			return "", fmt.Errorf("uuid: NewUUID: %w", err)
		}
		return u.String(), nil
	case 4:
		return randomV4()
	case 6:
		u, err := gid.NewV6()
		if err != nil {
			return "", fmt.Errorf("uuid: NewV6: %w", err)
		}
		return u.String(), nil
	case 7:
		u, err := gid.NewV7()
		if err != nil {
			return "", fmt.Errorf("uuid: NewV7: %w", err)
		}
		return u.String(), nil
	case 3, 5:
		if len(args) < 3 {
			return "", fmt.Errorf("uuid: v%d needs (version, namespace, data)", ver)
		}
		ns, err := resolveNamespace(args[1])
		if err != nil {
			return "", fmt.Errorf("uuid: bad namespace: %w", err)
		}
		data, err := toBytes(args[2])
		if err != nil {
			return "", fmt.Errorf("uuid: bad data: %w", err)
		}
		if ver == 3 {
			return gid.NewMD5(ns, data).String(), nil
		}
		return gid.NewSHA1(ns, data).String(), nil
	default:
		return "", fmt.Errorf("uuid: unsupported version %d (supported: 1, 3, 4, 5, 6, 7)", ver)
	}
}

func toString(v any) (string, error) {
	u, err := fromAny(v)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func toBlob(v any) ([]byte, error) {
	u, err := fromAny(v)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	copy(out, u[:])
	return out, nil
}

func extractVersion(v any) (int64, error) {
	u, err := fromAny(v)
	if err != nil {
		return 0, err
	}
	return int64(u.Version()), nil
}

// extractTimestamp returns nil (SQL NULL) for non-time-based variants so
// callers can rely on IS NULL / COALESCE in their queries.
func extractTimestamp(v any) (any, error) {
	u, err := fromAny(v)
	if err != nil {
		return nil, err
	}
	if u.Variant() != gid.RFC4122 {
		return nil, nil
	}
	switch u.Version() {
	case 1, 6, 7:
		sec, _ := u.Time().UnixTime()
		return sec, nil
	default:
		return nil, nil
	}
}

// fromAny parses a UUID from a TEXT or BLOB sql.Value.
func fromAny(v any) (gid.UUID, error) {
	switch x := v.(type) {
	case string:
		u, err := gid.Parse(x)
		if err != nil {
			return gid.Nil, fmt.Errorf("uuid: parse: %w", err)
		}
		return u, nil
	case []byte:
		if len(x) == 16 {
			var u gid.UUID
			copy(u[:], x)
			return u, nil
		}
		// Fall back to parsing as TEXT — covers the case where SQLite
		// hands us a TEXT-typed value through the []byte path.
		u, err := gid.ParseBytes(x)
		if err != nil {
			return gid.Nil, fmt.Errorf("uuid: parse: %w", err)
		}
		return u, nil
	case nil:
		return gid.Nil, errors.New("uuid: NULL input")
	default:
		return gid.Nil, fmt.Errorf("uuid: unsupported input type %T", v)
	}
}

// resolveNamespace turns a namespace arg into a UUID. Accepts a UUID
// literal (TEXT or BLOB) OR a well-known shortcut.
func resolveNamespace(v any) (gid.UUID, error) {
	if u, err := fromAny(v); err == nil {
		return u, nil
	}
	var label []byte
	switch x := v.(type) {
	case string:
		label = []byte(x)
	case []byte:
		label = x
	default:
		return gid.Nil, fmt.Errorf("uuid: namespace must be TEXT UUID, BLOB, or shortcut; got %T", v)
	}
	switch {
	case bytes.EqualFold(label, []byte("dns")), bytes.EqualFold(label, []byte("fqdn")):
		return gid.NameSpaceDNS, nil
	case bytes.EqualFold(label, []byte("url")):
		return gid.NameSpaceURL, nil
	case bytes.EqualFold(label, []byte("oid")):
		return gid.NameSpaceOID, nil
	case bytes.EqualFold(label, []byte("x500")):
		return gid.NameSpaceX500, nil
	}
	return gid.Nil, fmt.Errorf("uuid: unrecognized namespace %q", string(label))
}

func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case string:
		var n int64
		_, err := fmt.Sscanf(x, "%d", &n)
		if err != nil {
			return 0, fmt.Errorf("not an int: %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("not an int: %T", v)
}

func toBytes(v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		return []byte(x), nil
	case []byte:
		return x, nil
	}
	return nil, fmt.Errorf("not a byte sequence: %T", v)
}
