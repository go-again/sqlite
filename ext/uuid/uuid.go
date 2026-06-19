// Package uuid provides RFC 4122 UUID SQL functions backed by
// [github.com/google/uuid].
//
// # Functions
//
//   - uuid([version [, ...]]) → TEXT
//     Generate a UUID. Defaults to v4 when called with no args.
//     Version selector covers v1 (time-based), v4 (random),
//     v6 (reordered time-based), v7 (Unix epoch time-based),
//     v3 / v5 (name-based MD5 / SHA-1, requires namespace + data), and
//     v2 (DCE Security, requires domain + id — see below).
//   - gen_random_uuid() → TEXT: shorthand for uuid() = uuid(4).
//   - uuid_str(u) → TEXT: parse a UUID (TEXT or 16-byte BLOB) into
//     the canonical 8-4-4-4-12 hex string.
//   - uuid_blob(u) → BLOB: parse a UUID into its 16-byte BLOB form.
//   - uuid_extract_version(u) → INTEGER: the RFC 4122 version field.
//   - uuid_extract_timestamp(u) → INTEGER: Unix seconds for v1/v6/v7
//     UUIDs; NULL otherwise.
//   - uuid_extract_domain(u) → TEXT: the DCE domain (person/group/org)
//     of a v2 UUID; NULL otherwise.
//   - uuid_extract_id(u) → INTEGER: the local identifier (UID/GID) of a
//     v2 UUID; NULL otherwise.
//
// For v3 / v5 (name-based) UUIDs, the namespace may be any of:
//
//   - a TEXT UUID literal (e.g. '6ba7b810-9dad-11d1-80b4-00c04fd430c8')
//   - a 16-byte BLOB
//   - one of the well-known namespace shortcuts: "dns", "url", "oid",
//     "x500" (case-insensitive)
//
// For v2 (DCE Security), call uuid(2, domain, id): domain is a name
// (person/group/org, case-insensitive) or its numeric code (0/1/2), and
// id is the local identifier (a POSIX UID/GID for the person/group
// domains). The id is supplied explicitly rather than read from the
// process, so generation stays portable and deterministic.
//
// Ported from [ncruces/ext/uuid].
//
// # Usage
//
//	import (
//	    sqlite "gosqlite.org"
//	    "gosqlite.org/ext/uuid"
//	)
//
//	if err := uuid.Register(conn); err != nil { ... }
//	row := db.QueryRow(`SELECT uuid(4)`) // random v4
//
// For pool-wide install via [gosqlite.org.Driver.ConnectHook],
// blank-import the auto sub-package:
//
//	import _ "gosqlite.org/ext/uuid/auto"
//
// [ncruces/ext/uuid]: https://pkg.go.dev/github.com/ncruces/go-sqlite3/ext/uuid
package uuid

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"

	gid "github.com/google/uuid"

	sqlite "gosqlite.org"
)

// Exported names of the SQL functions Register installs. Exposed as
// constants so callers can build queries that reference them without
// re-hardcoding the string.
const (
	FuncUUID                 = "uuid"
	FuncGenRandomUUID        = "gen_random_uuid"
	FuncUUIDStr              = "uuid_str"
	FuncUUIDBlob             = "uuid_blob"
	FuncUUIDExtractVersion   = "uuid_extract_version"
	FuncUUIDExtractTimestamp = "uuid_extract_timestamp"
	FuncUUIDExtractDomain    = "uuid_extract_domain"
	FuncUUIDExtractID        = "uuid_extract_id"
)

// Register installs the uuid family of SQL functions on c.
func Register(c *sqlite.Conn) error {
	return errors.Join(
		// uuid([ver [, ns [, data]]]) — variadic any so we can accept
		// the polymorphic namespace argument (TEXT, BLOB, or shortcut).
		c.RegisterFunc(FuncUUID, generate, false),
		c.RegisterFunc(FuncGenRandomUUID, randomV4, false),
		c.RegisterFunc(FuncUUIDStr, toString, true),
		c.RegisterFunc(FuncUUIDBlob, toBlob, true),
		c.RegisterFunc(FuncUUIDExtractVersion, extractVersion, true),
		c.RegisterFunc(FuncUUIDExtractTimestamp, extractTimestamp, true),
		c.RegisterFunc(FuncUUIDExtractDomain, extractDomain, true),
		c.RegisterFunc(FuncUUIDExtractID, extractID, true),
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
	case 2:
		// DCE Security: uuid(2, domain, id). The id (a POSIX UID/GID for
		// the person/group domains) is supplied explicitly rather than
		// read from the process, keeping the function portable and pure.
		if len(args) < 3 {
			return "", fmt.Errorf("uuid: v2 needs (version, domain, id) — domain is person/group/org or 0/1/2")
		}
		dom, err := resolveDomain(args[1])
		if err != nil {
			return "", fmt.Errorf("uuid: bad domain: %w", err)
		}
		id, err := toInt64(args[2])
		if err != nil {
			return "", fmt.Errorf("uuid: bad id: %w", err)
		}
		if id < 0 || id > math.MaxUint32 {
			return "", fmt.Errorf("uuid: v2 id %d out of uint32 range", id)
		}
		u, err := gid.NewDCESecurity(dom, uint32(id))
		if err != nil {
			return "", fmt.Errorf("uuid: NewDCESecurity: %w", err)
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
		return "", fmt.Errorf("uuid: unsupported version %d (supported: 1, 2, 3, 4, 5, 6, 7)", ver)
	}
}

// resolveDomain accepts a DCE Security domain as a name (person/group/
// org, case-insensitive) or its numeric code (0/1/2).
func resolveDomain(v any) (gid.Domain, error) {
	switch x := v.(type) {
	case string, []byte:
		var s string
		if b, ok := x.([]byte); ok {
			s = string(b)
		} else {
			s = x.(string)
		}
		switch strings.ToLower(s) {
		case "person", "0":
			return gid.Person, nil
		case "group", "1":
			return gid.Group, nil
		case "org", "2":
			return gid.Org, nil
		}
		return 0, fmt.Errorf("unknown domain %q (want person/group/org)", s)
	default:
		n, err := toInt64(v)
		if err != nil {
			return 0, err
		}
		switch n {
		case 0:
			return gid.Person, nil
		case 1:
			return gid.Group, nil
		case 2:
			return gid.Org, nil
		}
		return 0, fmt.Errorf("domain %d out of range (0=person, 1=group, 2=org)", n)
	}
}

// extractDomain returns the DCE domain name of a v2 UUID, or NULL for any
// other version (where the field is not a domain).
func extractDomain(v any) (any, error) {
	u, err := fromAny(v)
	if err != nil {
		return nil, err
	}
	if u.Version() != 2 {
		return nil, nil
	}
	return strings.ToLower(u.Domain().String()), nil
}

// extractID returns the local identifier (UID/GID) of a v2 UUID, or NULL
// for any other version.
func extractID(v any) (any, error) {
	u, err := fromAny(v)
	if err != nil {
		return nil, err
	}
	if u.Version() != 2 {
		return nil, nil
	}
	return int64(u.ID()), nil
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
