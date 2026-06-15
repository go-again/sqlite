package sqlite // import "github.com/go-again/sqlite"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/go-again/sqlite/internal/cabi"
	"modernc.org/libc"
	"modernc.org/libc/sys/types"
	sqlite3 "modernc.org/sqlite/lib"
)

// FTS5Tokenizer splits document and query text into tokens for an FTS5 full-text
// index. Implementations are registered with [Conn.RegisterFTS5Tokenizer] and
// referenced from CREATE VIRTUAL TABLE … USING fts5(…, tokenize='name').
//
// Tokenize is called for both indexing and querying. For each token, call emit
// with the token text and the [start, end) byte offsets of the source span it
// came from (offsets into text; used for snippet/highlight). The token text need
// not be a substring of text — fold case, stem, or transliterate freely. Return
// emit's error to stop early; returning a non-nil error from Tokenize aborts the
// operation.
type FTS5Tokenizer interface {
	Tokenize(text string, emit func(token string, start, end int) error) error
}

// errTokenStop is the sentinel the tokenize trampoline uses to unwind a Go
// tokenizer loop when the C xToken callback asks to stop (SQLITE_DONE) or
// errors; the real result code is carried out of band.
var errTokenStop = errors.New("sqlite: fts5 tokenize: stop")

var (
	// ftsTokFactories maps a minted id (passed to FTS5 as the tokenizer's
	// pUserData) to the Go factory that builds a tokenizer instance. Drained per
	// conn on close.
	ftsTokFactories = newCallbackTable[func([]string) (FTS5Tokenizer, error)]()
	// ftsTokInstances maps a minted id (the opaque Fts5Tokenizer* handle) to a
	// live tokenizer instance. Self-draining: FTS5 calls xDelete for each.
	ftsTokInstances = newCallbackTable[FTS5Tokenizer]()
)

// RegisterFTS5Tokenizer registers a Go-implemented FTS5 tokenizer under name on
// this connection. After registration a table can use it via
//
//	CREATE VIRTUAL TABLE docs USING fts5(body, tokenize='name')
//
// newTokenizer is called once per table that references the tokenizer (with the
// space-separated arguments that follow the name in the tokenize= option) and
// returns the [FTS5Tokenizer] that table will use.
//
// The registration is per-connection (the fts5_api is bound to one connection),
// so create the table on the same connection — pin the pool (see
// internal/testhelp.OpenPinned). No other pure-Go SQLite driver exposes custom
// FTS5 tokenizers.
//
// https://sqlite.org/fts5.html#custom_tokenizers
func (c *Conn) RegisterFTS5Tokenizer(name string, newTokenizer func(args []string) (FTS5Tokenizer, error)) error {
	if newTokenizer == nil {
		return errors.New("sqlite: RegisterFTS5Tokenizer: nil factory")
	}
	api, err := c.fts5API()
	if err != nil {
		return err
	}

	// Build the fts5_tokenizer method table in C memory. FTS5's
	// xCreateTokenizer copies the struct, so a temporary suffices.
	pTok, err := c.malloc(int(unsafe.Sizeof(sqlite3.Tfts5_tokenizer{})))
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, pTok)
	tok := (*sqlite3.Tfts5_tokenizer)(unsafe.Pointer(pTok))
	tok.FxCreate = cFuncPointer(ftsTokCreateTrampoline)
	tok.FxDelete = cFuncPointer(ftsTokDeleteTrampoline)
	tok.FxTokenize = cFuncPointer(ftsTokTokenizeTrampoline)

	zName, err := libc.CString(name)
	if err != nil {
		return err
	}
	defer libc.Xfree(c.tls, zName)

	id := ftsTokFactories.register(newTokenizer)
	xCreateTok := cabi.AsFunc[func(*libc.TLS, uintptr, uintptr, uintptr, uintptr, uintptr) int32](
		(*sqlite3.Tfts5_api)(unsafe.Pointer(api)).FxCreateTokenizer)
	// pUserData = id; xDestroy = 0 (the id is reclaimed in dropHookHandlers on
	// conn close, mirroring the other per-conn callback registries).
	if rc := xCreateTok(c.tls, api, zName, id, pTok, 0); rc != sqlite3.SQLITE_OK {
		ftsTokFactories.drop(id)
		return fmt.Errorf("sqlite: RegisterFTS5Tokenizer(%q): %w", name, c.errstr(rc))
	}
	c.ftsTokFactoryIDs = append(c.ftsTokFactoryIDs, id)
	return nil
}

// fts5API returns the connection's fts5_api pointer via the documented
// bind-pointer handshake (SELECT fts5(?1) with a "fts5_api_ptr" pointer).
func (c *conn) fts5API() (uintptr, error) {
	zSQL, err := libc.CString("SELECT fts5(?1)")
	if err != nil {
		return 0, err
	}
	defer libc.Xfree(c.tls, zSQL)

	ppStmt, err := c.malloc(int(ptrSize))
	if err != nil {
		return 0, err
	}
	defer libc.Xfree(c.tls, ppStmt)
	*(*uintptr)(unsafe.Pointer(ppStmt)) = 0
	if rc := sqlite3.Xsqlite3_prepare_v2(c.tls, c.db, zSQL, -1, ppStmt, 0); rc != sqlite3.SQLITE_OK {
		return 0, fmt.Errorf("sqlite: fts5 api: prepare: %w", c.errstr(rc))
	}
	pStmt := *(*uintptr)(unsafe.Pointer(ppStmt))
	defer sqlite3.Xsqlite3_finalize(c.tls, pStmt)

	apiSlot, err := c.malloc(int(ptrSize))
	if err != nil {
		return 0, err
	}
	defer libc.Xfree(c.tls, apiSlot)
	*(*uintptr)(unsafe.Pointer(apiSlot)) = 0

	zType, err := libc.CString("fts5_api_ptr")
	if err != nil {
		return 0, err
	}
	defer libc.Xfree(c.tls, zType)

	// fts5(?1) reads the bound pointer (a fts5_api**) and writes the api into it.
	sqlite3.Xsqlite3_bind_pointer(c.tls, pStmt, 1, apiSlot, zType, 0)
	sqlite3.Xsqlite3_step(c.tls, pStmt)
	api := *(*uintptr)(unsafe.Pointer(apiSlot))
	if api == 0 {
		return 0, errors.New("sqlite: fts5 extension not available on this connection")
	}
	return api, nil
}

// ftsTokCreateTrampoline is fts5_tokenizer.xCreate: build a tokenizer instance
// from the factory keyed by pCtx (the pUserData id) and the tokenize= args.
func ftsTokCreateTrampoline(tls *libc.TLS, pCtx uintptr, azArg uintptr, nArg int32, ppOut uintptr) int32 {
	factory, ok := ftsTokFactories.lookup(pCtx)
	if !ok {
		return sqlite3.SQLITE_ERROR
	}
	args := make([]string, 0, nArg)
	for i := range nArg {
		p := *(*uintptr)(unsafe.Pointer(azArg + uintptr(i)*ptrSize))
		args = append(args, libc.GoString(p))
	}
	tk, err := factory(args)
	if err != nil || tk == nil {
		return sqlite3.SQLITE_ERROR
	}
	*(*uintptr)(unsafe.Pointer(ppOut)) = ftsTokInstances.register(tk)
	return sqlite3.SQLITE_OK
}

// ftsTokDeleteTrampoline is fts5_tokenizer.xDelete: drop the instance.
func ftsTokDeleteTrampoline(tls *libc.TLS, pTok uintptr) {
	ftsTokInstances.drop(pTok)
}

// ftsTokTokenizeTrampoline is fts5_tokenizer.xTokenize: run the Go tokenizer,
// forwarding each token to the C xToken callback.
func ftsTokTokenizeTrampoline(tls *libc.TLS, pTok uintptr, pCtx uintptr, flags int32, pText uintptr, nText int32, xToken uintptr) int32 {
	tk, ok := ftsTokInstances.lookup(pTok)
	if !ok {
		return sqlite3.SQLITE_ERROR
	}
	var text string
	if nText > 0 {
		text = string(libc.GoBytes(pText, int(nText)))
	}
	callToken := cabi.AsFunc[func(*libc.TLS, uintptr, int32, uintptr, int32, int32, int32) int32](xToken)

	tokenRC := int32(sqlite3.SQLITE_OK)
	emit := func(token string, start, end int) error {
		var cTok uintptr
		if len(token) > 0 {
			cTok = libc.Xmalloc(tls, types.Size_t(len(token)))
			if cTok == 0 {
				tokenRC = sqlite3.SQLITE_NOMEM
				return errTokenStop
			}
			copy((*libc.RawMem)(unsafe.Pointer(cTok))[:len(token):len(token)], token)
		}
		rc := callToken(tls, pCtx, 0, cTok, int32(len(token)), int32(start), int32(end))
		if cTok != 0 {
			libc.Xfree(tls, cTok)
		}
		if rc != sqlite3.SQLITE_OK {
			// SQLITE_DONE means "stop early, not an error"; any other code is a
			// real failure. Either way, unwind the Go tokenizer loop.
			tokenRC = rc
			return errTokenStop
		}
		return nil
	}

	err := tk.Tokenize(text, emit)
	if tokenRC != sqlite3.SQLITE_OK {
		if tokenRC == sqlite3.SQLITE_DONE {
			return sqlite3.SQLITE_OK
		}
		return tokenRC
	}
	if err != nil {
		return sqlite3.SQLITE_ERROR
	}
	return sqlite3.SQLITE_OK
}
