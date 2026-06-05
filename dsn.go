package sqlite

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Mattn-compat DSN flag names. Exposed so callers building DSN strings
// or filtering query parameters can reference the canonical spelling
// without re-hardcoding it. Aliases (e.g. `_fk` ↔ `_foreign_keys`)
// share the underlying pragma; both names are valid in DSNs.
const (
	FlagPragma                 = "_pragma"
	FlagForeignKeys            = "_foreign_keys"
	FlagFK                     = "_fk"
	FlagBusyTimeout            = "_busy_timeout"
	FlagTimeout                = "_timeout"
	FlagJournalMode            = "_journal_mode"
	FlagJournal                = "_journal"
	FlagSynchronous            = "_synchronous"
	FlagSync                   = "_sync"
	FlagLockingMode            = "_locking_mode"
	FlagLocking                = "_locking"
	FlagSecureDelete           = "_secure_delete"
	FlagRecursiveTriggers      = "_recursive_triggers"
	FlagRT                     = "_rt"
	FlagCacheSize              = "_cache_size"
	FlagAutoVacuum             = "_auto_vacuum"
	FlagVacuum                 = "_vacuum"
	FlagDeferForeignKeys       = "_defer_foreign_keys"
	FlagDeferFK                = "_defer_fk"
	FlagIgnoreCheckConstraints = "_ignore_check_constraints"
	FlagCaseSensitiveLike      = "_case_sensitive_like"
	FlagCSLike                 = "_cslike"
	FlagQueryOnly              = "_query_only"
	FlagWritableSchema         = "_writable_schema"
	FlagMutex                  = "_mutex"
	FlagLoc                    = "_loc"
	FlagTimezone               = "_timezone"
	FlagTimeFormat             = "_time_format"
	FlagTimeIntegerFormat      = "_time_integer_format"
	FlagIntToTime              = "_inttotime"
	FlagTextToTime             = "_texttotime"
	FlagTxLock                 = "_txlock"
	FlagStmtCacheSize          = "_stmt_cache_size"
	FlagStrict                 = "_strict"
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
	// Keys reference the exported Flag* constants so the canonical DSN
	// spellings have a single source of truth — rename a constant and
	// the alias table follows. Values are SQLite PRAGMA names (not DSN
	// flags), so they stay as literals.
	pragmas := map[string]pragmaSpec{
		FlagForeignKeys:            {"foreign_keys", true},
		FlagFK:                     {"foreign_keys", true},
		FlagBusyTimeout:            {"busy_timeout", false},
		FlagTimeout:                {"busy_timeout", false},
		FlagJournalMode:            {"journal_mode", false},
		FlagJournal:                {"journal_mode", false},
		FlagSynchronous:            {"synchronous", false},
		FlagSync:                   {"synchronous", false},
		FlagLockingMode:            {"locking_mode", false},
		FlagLocking:                {"locking_mode", false},
		FlagSecureDelete:           {"secure_delete", false},
		FlagRecursiveTriggers:      {"recursive_triggers", true},
		FlagRT:                     {"recursive_triggers", true},
		FlagCacheSize:              {"cache_size", false},
		FlagAutoVacuum:             {"auto_vacuum", false},
		FlagVacuum:                 {"auto_vacuum", false},
		FlagDeferForeignKeys:       {"defer_foreign_keys", true},
		FlagDeferFK:                {"defer_foreign_keys", true},
		FlagIgnoreCheckConstraints: {"ignore_check_constraints", true},
		FlagCaseSensitiveLike:      {"case_sensitive_like", true},
		FlagCSLike:                 {"case_sensitive_like", true},
		FlagQueryOnly:              {"query_only", true},
		FlagWritableSchema:         {"writable_schema", true},
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
	// treated as a timezone name. Only set _timezone when the key is absent
	// (Has check) — a present-but-empty `_timezone=` is the caller explicitly
	// asking for "no override", and silently overwriting it would surprise.
	if v := q.Get("_loc"); v != "" {
		if !q.Has("_timezone") {
			tz := v
			if strings.EqualFold(v, "auto") {
				tz = "Local"
			}
			q.Set("_timezone", tz)
		}
		q.Del("_loc")
	}

	// Reject userauth flags loudly. The sqlite_userauth extension was
	// deprecated by SQLite upstream and removed from modernc.org/sqlite;
	// pretending to accept the flag would silently produce an unauthenticated
	// connection, which is worse than a clear error.
	for _, name := range []string{"_auth", "_auth_user", "_auth_pass", "_auth_crypt", "_auth_salt"} {
		if _, ok := q[name]; ok {
			return "", fmt.Errorf("userauth flag %q is not supported (deprecated and removed from modernc.org/sqlite)", name)
		}
	}

	// _mutex requires choosing between SQLITE_OPEN_NOMUTEX and SQLITE_OPEN_FULLMUTEX
	// at sqlite3_open_v2 time. The current fork always opens with FULLMUTEX
	// (matching modernc's default) and database/sql's connection pool already
	// pins one *Conn per goroutine, so NOMUTEX would either be unnecessary or
	// unsafe. We accept _mutex=full / _mutex=true / 1 / on as no-ops and
	// reject _mutex=no / false / 0 / off so callers don't silently get the
	// opposite of what they asked for.
	if v := q.Get("_mutex"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "full", "1", "t", "true", "yes", "on":
			// Already what we do; drop the flag without changing behavior.
			q.Del("_mutex")
		case "no", "0", "f", "false", "off":
			return "", fmt.Errorf("_mutex=%q (NOMUTEX) is not supported: this driver always opens with SQLITE_OPEN_FULLMUTEX, which is safe for database/sql's connection pool", v)
		default:
			return "", fmt.Errorf("_mutex=%q: must be one of full/no (mattn-style) or true/false (boolean)", v)
		}
	}

	// _stmt_cache_size is read by applyQueryParams, where the cache lives on
	// the *conn. We only validate the integer here so a clearly-wrong DSN
	// fails fast at translate time rather than waiting for conn-open.
	if q.Has("_stmt_cache_size") {
		if _, err := strconv.Atoi(q.Get("_stmt_cache_size")); err != nil {
			return "", fmt.Errorf("_stmt_cache_size: must be an integer, got %q: %w", q.Get("_stmt_cache_size"), err)
		}
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
// _stmt_cache_size configures the per-connection prepared-statement LRU
// (see stmt_cache.go); applyQueryParams reads it.
var knownDSNFlags = map[string]bool{
	// mattn/modernc `_*` flags — keyed off the exported constants.
	FlagPragma:            true,
	FlagTimeFormat:        true,
	FlagTimeIntegerFormat: true,
	FlagIntToTime:         true,
	FlagStmtCacheSize:     true,
	FlagTextToTime:        true,
	FlagTimezone:          true,
	FlagTxLock:            true,
	// URI-level / modernc-native passthrough flags. These are not
	// mattn `_*` flags, so they have no Flag* constant; they're
	// understood directly by the URI parser / modernc VFS layer.
	"vfs":       true,
	"cache":     true,
	"mode":      true,
	"immutable": true,
	"psow":      true,
	"nolock":    true,
}
