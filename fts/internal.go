// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package fts

import (
	"fmt"
	"strings"
)

// quote returns name in backticks, escaping any embedded backticks. Used for
// table/column identifier interpolation. Sub-package vec has an identical
// helper; we keep them separate so the two packages stay independent.
func quote(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// validIdent is the conservative ASCII subset accepted as a SQL identifier:
// leading letter or underscore, then letters/digits/underscores. Anything
// else is rejected at the API boundary rather than silently interpolated.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// assignSQLType converts a SQLite-scanned value to the concrete generic type
// T. The driver's underlying types are restricted to nil, int64, float64,
// string, []byte; we accept each and convert via the SQLType union.
func assignSQLType[T SQLType](raw any) (T, error) {
	var zero T
	switch v := raw.(type) {
	case nil:
		return zero, nil
	case int64:
		return convertNumber[T](float64(v))
	case float64:
		return convertNumber[T](v)
	case string:
		return convertText[T](v, []byte(v))
	case []byte:
		return convertText[T](string(v), v)
	}
	return zero, fmt.Errorf("cannot assign %T to %T", raw, zero)
}

// convertNumber converts a numeric scan value to the requested T. If T isn't
// numeric (e.g. T is string) we coerce via fmt.Sprint, mirroring the way
// database/sql.Scan handles cross-type assignment of integer columns into
// string targets.
func convertNumber[T SQLType](f float64) (T, error) {
	var zero T
	switch any(zero).(type) {
	case int:
		return any(int(f)).(T), nil
	case int32:
		return any(int32(f)).(T), nil
	case int64:
		return any(int64(f)).(T), nil
	case uint:
		return any(uint(f)).(T), nil
	case uint32:
		return any(uint32(f)).(T), nil
	case uint64:
		return any(uint64(f)).(T), nil
	case float32:
		return any(float32(f)).(T), nil
	case float64:
		return any(f).(T), nil
	case string:
		return any(fmt.Sprintf("%g", f)).(T), nil
	case []byte:
		return any([]byte(fmt.Sprintf("%g", f))).(T), nil
	}
	return zero, fmt.Errorf("convertNumber: unsupported T=%T", zero)
}

// convertText converts a text scan value (either as string or []byte) to T.
func convertText[T SQLType](s string, b []byte) (T, error) {
	var zero T
	switch any(zero).(type) {
	case string:
		return any(s).(T), nil
	case []byte:
		return any(b).(T), nil
	case int, int32, int64, uint, uint32, uint64, float32, float64:
		// Refuse silently — the index's K/V are wrong for the data shape.
		// Returning the zero value would mask the misconfiguration.
		return zero, fmt.Errorf("convertText: text column scanned into numeric T=%T", zero)
	}
	return zero, fmt.Errorf("convertText: unsupported T=%T", zero)
}

// toString coerces a scanned column value to string. Used for the snippet/
// highlight extras which always come back as text.
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
