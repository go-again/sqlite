package ftsgorm

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-again/sqlite/fts"
)

// tagName is the struct tag key we look for. Tagged fields' values are
// stored in a shared FTS5 external-content table; the field type must
// be a string (or convertible to one) on the source model.
const tagName = "fts5"

// fieldMeta holds parsed data for a single string field on the model.
type fieldMeta struct {
	FieldIndex []int
	FieldName  string
	Column     string // FTS5 column name; defaults to lowercase field name
}

// Mode selects how the FTS5 sidecar relates to the gorm source table.
type Mode int

const (
	// ModeExternal (the default) declares the FTS5 table with
	// content='source_table' so FTS5 doesn't store the text itself;
	// content is read on demand from the source. AFTER INSERT/UPDATE/
	// DELETE triggers on the source maintain the FTS5 index.
	ModeExternal Mode = iota
	// ModeInTable declares the FTS5 table without a content= option,
	// so the FTS5 table is the source of truth for the indexed text.
	// The plugin INSERTs into the FTS5 table on Create/Save callbacks.
	// Slightly cheaper to query (no source join) but doubles storage.
	ModeInTable
	// ModeContentless declares the FTS5 table with content='', which
	// stores only the inverted index — no text at all. Cheapest
	// storage; snippet() and highlight() are unavailable (the plugin
	// errors at Search time if a WithSnippet/WithHighlight is passed).
	ModeContentless
)

// tableMeta gathers the table-level config that fields agree on (or
// must agree on if specified).
type tableMeta struct {
	Table    string
	Tokenize string
	Prefix   string // verbatim N1,N2,N3 or empty
	Detail   string // "full" | "column" | "none"
	Mode     Mode
	modeSet  bool // tracks whether external/contentless were explicitly set
	Fields   []fieldMeta
}

// parseTags walks a struct type and collects the fts5: tags into one
// tableMeta. Returns nil meta + nil error if the struct has no tagged
// fields. Errors on conflicting table-level keys.
//
// This is invoked BEFORE gorm parses the schema, so we don't have
// gorm's NamingStrategy at hand here; the caller fills the default
// table name later.
func parseTags(rt reflect.Type) (*tableMeta, error) {
	if rt.Kind() != reflect.Struct {
		return nil, nil
	}
	var tm tableMeta
	if err := walkTags(rt, nil, &tm); err != nil {
		return nil, err
	}
	if len(tm.Fields) == 0 {
		return nil, nil
	}
	return &tm, nil
}

// walkTags recurses through embedded structs to find fts5:-tagged
// fields anywhere in the struct hierarchy. Indexes accumulate so
// FieldByIndex at callback time works.
func walkTags(rt reflect.Type, prefix []int, out *tableMeta) error {
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		idx := append(append([]int(nil), prefix...), i)
		if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			if err := walkTags(sf.Type, idx, out); err != nil {
				return err
			}
			continue
		}
		tag, ok := sf.Tag.Lookup(tagName)
		if !ok {
			continue
		}
		if sf.Type.Kind() != reflect.String {
			return fmt.Errorf(
				"ftsgorm: %s.%s is %s but fts5: tags only apply to string fields",
				rt.Name(), sf.Name, sf.Type)
		}
		fm := fieldMeta{
			FieldIndex: idx,
			FieldName:  sf.Name,
			Column:     strings.ToLower(sf.Name),
		}
		if err := mergeTag(out, tag, &fm, rt.Name(), sf.Name); err != nil {
			return err
		}
		out.Fields = append(out.Fields, fm)
	}
	return nil
}

// mergeTag parses one field's tag and merges its table-level options
// into the accumulated tableMeta. Conflicts (e.g. two fields declaring
// different `tokenize=` values) produce an error.
func mergeTag(tm *tableMeta, tag string, fm *fieldMeta, structName, fieldName string) error {
	for _, kv := range strings.Split(tag, ";") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("ftsgorm: %s.%s: tag entry %q is not key=value", structName, fieldName, kv)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Spaces escaped as '+' (gorm tag parser is space-inconsistent).
		val = strings.ReplaceAll(val, "+", " ")
		switch key {
		case "column":
			if !isIdent(val) {
				return fmt.Errorf("ftsgorm: %s.%s: column=%q is not a valid identifier", structName, fieldName, val)
			}
			fm.Column = val
		case "table":
			if !isIdent(val) {
				return fmt.Errorf("ftsgorm: %s.%s: table=%q is not a valid identifier", structName, fieldName, val)
			}
			if tm.Table != "" && tm.Table != val {
				return fmt.Errorf(
					"ftsgorm: %s declares conflicting fts5 table names %q and %q; "+
						"fts5:-tagged fields on one model must share a single table",
					structName, tm.Table, val)
			}
			tm.Table = val
		case "tokenize":
			if tm.Tokenize != "" && tm.Tokenize != val {
				return fmt.Errorf(
					"ftsgorm: %s declares conflicting fts5 tokenize options %q and %q",
					structName, tm.Tokenize, val)
			}
			tm.Tokenize = val
		case "prefix":
			// Permitted forms: "2,3,4" — comma-separated positive ints.
			parts := strings.Split(val, ",")
			for _, p := range parts {
				if _, err := strconv.Atoi(strings.TrimSpace(p)); err != nil {
					return fmt.Errorf("ftsgorm: %s.%s: prefix=%q has non-integer entry %q",
						structName, fieldName, val, p)
				}
			}
			canonical := strings.Join(splitTrim(val), " ")
			if tm.Prefix != "" && tm.Prefix != canonical {
				return fmt.Errorf(
					"ftsgorm: %s declares conflicting fts5 prefix options %q and %q",
					structName, tm.Prefix, canonical)
			}
			tm.Prefix = canonical
		case "detail":
			switch val {
			case "full", "column", "none":
				if tm.Detail != "" && tm.Detail != val {
					return fmt.Errorf(
						"ftsgorm: %s declares conflicting fts5 detail options %q and %q",
						structName, tm.Detail, val)
				}
				tm.Detail = val
			default:
				return fmt.Errorf("ftsgorm: %s.%s: detail=%q (want full | column | none)",
					structName, fieldName, val)
			}
		case "external":
			b, err := parseBool(val)
			if err != nil {
				return fmt.Errorf("ftsgorm: %s.%s: external=%q must be true|false", structName, fieldName, val)
			}
			mode := ModeInTable
			if b {
				mode = ModeExternal
			}
			if err := setMode(tm, mode); err != nil {
				return fmt.Errorf("ftsgorm: %s.%s: %w", structName, fieldName, err)
			}
		case "contentless":
			b, err := parseBool(val)
			if err != nil {
				return fmt.Errorf("ftsgorm: %s.%s: contentless=%q must be true|false", structName, fieldName, val)
			}
			if !b {
				// contentless=false is a no-op — leave whatever
				// mode was previously set.
				continue
			}
			if err := setMode(tm, ModeContentless); err != nil {
				return fmt.Errorf("ftsgorm: %s.%s: %w", structName, fieldName, err)
			}
		default:
			return fmt.Errorf("ftsgorm: %s.%s: unknown tag key %q", structName, fieldName, key)
		}
	}
	return nil
}

// parseBool accepts the canonical boolean strings ("true", "false",
// "1", "0", "yes", "no") — case-insensitive — and reports an error
// otherwise. We deliberately don't use strconv.ParseBool here because
// its "t/f" abbreviations are surprising in a tag-syntax context.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, fmt.Errorf("not a boolean: %q", s)
}

// setMode commits a mode choice to the tableMeta, rejecting conflicts
// when an earlier field already chose a different mode.
func setMode(tm *tableMeta, m Mode) error {
	if tm.modeSet && tm.Mode != m {
		return fmt.Errorf("conflicting fts5 modes %v and %v on the same model", tm.Mode, m)
	}
	tm.Mode = m
	tm.modeSet = true
	return nil
}

// splitTrim splits on commas and trims each entry. "2, 3, 4" → ["2","3","4"].
func splitTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// isIdent is a thin alias for fts.ValidIdent. Keeps call sites
// readable; the rule is defined exactly once across the module.
func isIdent(s string) bool { return fts.ValidIdent(s) }
