package sqlite

// Compile-time interface satisfaction checks for the gorm interfaces this
// package claims to implement. If gorm's upstream interfaces grow a new
// method, one of these assignments will stop compiling and the error will
// name the missing method, prompting an explicit decision (implement,
// stub, or drop the claim).
//
// Why a _test.go file: these assertions are pure type-system constraints;
// they should not ship to consumers, but they should run before every test
// run so an out-of-date dependency surfaces immediately. Putting them in a
// _test.go puts them in the test binary only.
//
// Why each interface:
//
//   - gorm.Dialector — the primary contract; gorm.Open(d) calls these
//     methods. Asserting satisfaction here turns a runtime "Dialector is
//     missing method X" into a compile error.
//
//   - gorm.Migrator — returned by Dialector.Migrator(db). gorm's internal
//     migration code calls these methods directly; an unimplemented one is
//     a runtime nil-pointer panic without this check.
//
//   - gorm.ErrorTranslator — optional; gorm tests with `config.TranslateError = true`
//     dispatch through it. Asserting satisfaction makes our Translate
//     method's signature drift-resistant.
//
//   - gorm.SavePointerDialectorInterface — optional; gorm uses it for
//     nested transaction support via SAVEPOINT. Our Dialector implements
//     both methods, so we assert satisfaction to keep them in sync with
//     any future signature change.

import (
	"gorm.io/gorm"
)

var (
	_ gorm.Dialector                     = (*Dialector)(nil)
	_ gorm.Migrator                      = (*Migrator)(nil)
	_ gorm.ErrorTranslator               = (*Dialector)(nil)
	_ gorm.SavePointerDialectorInterface = (*Dialector)(nil)
)
