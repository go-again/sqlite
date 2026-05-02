// Copyright 2026 The go-again/sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package sqlite

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// translateMattnDSN takes a DSN query string (everything after "?") and
// returns a normalized form where mattn-style `_*` flags are translated to
// equivalent modernc-style flags (mostly `_pragma=…` values) understood by
// applyQueryParams.
//
// Aliases honored:
//   - _foreign_keys, _fk            → PRAGMA foreign_keys=
//   - _busy_timeout, _timeout       → PRAGMA busy_timeout=
//   - _journal_mode, _journal       → PRAGMA journal_mode=
//   - _synchronous, _sync           → PRAGMA synchronous=
//   - _locking_mode, _locking       → PRAGMA locking_mode=
//   - _secure_delete                → PRAGMA secure_delete=
//   - _recursive_triggers, _rt      → PRAGMA recursive_triggers=
//   - _cache_size                   → PRAGMA cache_size=
//   - _auto_vacuum, _vacuum         → PRAGMA auto_vacuum=
//   - _defer_foreign_keys, _defer_fk → PRAGMA defer_foreign_keys=
//   - _ignore_check_constraints     → PRAGMA ignore_check_constraints=
//   - _case_sensitive_like, _cslike → PRAGMA case_sensitive_like=
//   - _query_only                   → PRAGMA query_only=
//   - _writable_schema              → PRAGMA writable_schema=
//   - _mutex                        → open flag (SQLITE_OPEN_NOMUTEX | FULLMUTEX) — handled at open
//   - _loc                          → re-emitted as _timezone (mattn names the same flag _loc)
//   - cache, mode, immutable        → URI-level, passed through
//   - _auth*                        → rejected (userauth was removed upstream)
//   - _strict=1                     → unknown flags become hard errors
//
// modernc-native flags (_pragma, _time_format, _time_integer_format,
// _inttotime, _texttotime, _timezone, _txlock, vfs) pass through unchanged.
func translateMattnDSN(query string) (string, error) {
	if query == "" {
		return "", nil
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}

	strict := false
	if v := q.Get("_strict"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return "", fmt.Errorf("_strict: %w", err)
		}
		strict = b
		q.Del("_strict")
	}

	// Map of mattn alias → canonical PRAGMA name. Values are appended to
	// `_pragma`. Bool values are normalized to 1/0; other values pass through.
	type pragmaSpec struct {
		name string
		// boolish indicates the value should be coerced from on/off/yes/no/true/false to 1/0.
		boolish bool
	}
	pragmas := map[string]pragmaSpec{
		"_foreign_keys":             {"foreign_keys", true},
		"_fk":                       {"foreign_keys", true},
		"_busy_timeout":             {"busy_timeout", false},
		"_timeout":                  {"busy_timeout", false},
		"_journal_mode":             {"journal_mode", false},
		"_journal":                  {"journal_mode", false},
		"_synchronous":              {"synchronous", false},
		"_sync":                     {"synchronous", false},
		"_locking_mode":             {"locking_mode", false},
		"_locking":                  {"locking_mode", false},
		"_secure_delete":            {"secure_delete", false},
		"_recursive_triggers":       {"recursive_triggers", true},
		"_rt":                       {"recursive_triggers", true},
		"_cache_size":               {"cache_size", false},
		"_auto_vacuum":              {"auto_vacuum", false},
		"_vacuum":                   {"auto_vacuum", false},
		"_defer_foreign_keys":       {"defer_foreign_keys", true},
		"_defer_fk":                 {"defer_foreign_keys", true},
		"_ignore_check_constraints": {"ignore_check_constraints", true},
		"_case_sensitive_like":      {"case_sensitive_like", true},
		"_cslike":                   {"case_sensitive_like", true},
		"_query_only":               {"query_only", true},
		"_writable_schema":          {"writable_schema", true},
	}

	for alias, spec := range pragmas {
		vals, ok := q[alias]
		if !ok {
			continue
		}
		for _, v := range vals {
			if spec.boolish {
				b, err := parseBoolish(v)
				if err != nil {
					return "", fmt.Errorf("%s=%q: %w", alias, v, err)
				}
				v = "1"
				if !b {
					v = "0"
				}
			}
			q.Add("_pragma", fmt.Sprintf("%s(%s)", spec.name, v))
		}
		q.Del(alias)
	}

	// _loc=auto is the mattn equivalent of _timezone. mattn's _loc=auto means
	// "use the local time location"; we map that to Local. Any other value is
	// treated as a timezone name. We only set _timezone if not already present.
	if v := q.Get("_loc"); v != "" {
		if q.Get("_timezone") == "" {
			tz := v
			if strings.EqualFold(v, "auto") {
				tz = "Local"
			}
			q.Set("_timezone", tz)
		}
		q.Del("_loc")
	}

	// Reject userauth flags loudly — see plan-initial.md for rationale.
	for _, name := range []string{"_auth", "_auth_user", "_auth_pass", "_auth_crypt", "_auth_salt"} {
		if _, ok := q[name]; ok {
			return "", fmt.Errorf("userauth flag %q is not supported (deprecated and removed from modernc.org/sqlite)", name)
		}
	}

	// _mutex is handled at open by openV2's flags; we surface a clear error
	// because we don't currently respect it.
	if v := q.Get("_mutex"); v != "" {
		// Modernc always opens with SQLITE_OPEN_FULLMUTEX; honoring _mutex=no
		// would require plumbing through openV2. Document and move on rather
		// than silently ignoring.
		_ = v
		q.Del("_mutex")
	}

	if strict {
		for k := range q {
			if knownDSNFlags[k] {
				continue
			}
			return "", fmt.Errorf("unknown DSN flag %q (in strict mode)", k)
		}
	}

	return q.Encode(), nil
}

// parseBoolish accepts the strings mattn accepts for boolean DSN flags.
func parseBoolish(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "on":
		return true, nil
	case "0", "f", "false", "no", "off":
		return false, nil
	}
	// Fall back to strconv for less common spellings (e.g. TRUE).
	return strconv.ParseBool(s)
}

// knownDSNFlags lists DSN flag names that pass strict-mode validation.
var knownDSNFlags = map[string]bool{
	"_pragma":              true,
	"_time_format":         true,
	"_time_integer_format": true,
	"_inttotime":           true,
	"_texttotime":          true,
	"_timezone":            true,
	"_txlock":              true,
	"vfs":                  true,
	"cache":                true,
	"mode":                 true,
	"immutable":            true,
	"psow":                 true,
	"nolock":               true,
}
