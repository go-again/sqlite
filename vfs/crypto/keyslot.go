package crypto

// keyslot.go adds multi-recipient encryption to the crypto VFS. Unlike a raw key,
// which builds the cipher up front in New, a recipients database keeps its random
// data key in a detached "<path>.keyslot" sidecar (the keyring envelope format):
// several parties open the database with their own identity, no shared secret.
// The cipher is resolved lazily at the first main-database open — that is the
// first point the database path (and so the sidecar path) is known.

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gosqlite.org/crypto/keyring"
)

// keyslotSuffix names the sidecar holding the wrapped data key, next to the
// database file. maxKeyslotBytes bounds the sidecar read (untrusted on disk): an
// age keyslot for many recipients is a few KB, far under this.
const (
	keyslotSuffix   = ".keyslot"
	maxKeyslotBytes = 1 << 20
)

// Errors surfaced when a recipients database is first opened (the data key is
// resolved lazily, so these reach the caller from the first database access).
var (
	// ErrNoIdentity reports that none of the supplied Options.Identities could
	// unwrap the keyslot. Test with [errors.Is].
	ErrNoIdentity = errors.New("crypto: no supplied identity matched the keyslot")
	// ErrUnauthorized reports that the keyslot was not signed by a trusted master
	// (Options.Masters). Test with [errors.Is].
	ErrUnauthorized = errors.New("crypto: keyslot is not signed by a trusted master")
	// ErrNotMaster reports that a non-master tried to change the recipient set of a
	// master-protected database (see [Rewrap]). Test with [errors.Is].
	ErrNotMaster = errors.New("crypto: only a master can change the recipient set")
)

// keyConfig carries the encryption inputs from [Options]. A raw Key builds the
// cipher eagerly in New; recipients/identities/masters defer it to the first open.
type keyConfig struct {
	cipher     Cipher
	rawKey     []byte
	recipients []keyring.Recipient
	identities []keyring.Identity
	masters    []keyring.MasterRecipient
	signWith   keyring.MasterIdentity
}

// lazy reports whether the cipher is resolved from a keyslot sidecar at open
// (recipients mode) rather than built directly from a raw key.
func (kc keyConfig) lazy() bool {
	return len(kc.recipients) > 0 || len(kc.identities) > 0 || len(kc.masters) > 0
}

func keyConfigFromOptions(opts Options) (keyConfig, error) {
	kc := keyConfig{
		cipher:     opts.Cipher,
		rawKey:     opts.Key,
		recipients: opts.Recipients,
		identities: opts.Identities,
		masters:    opts.Masters,
		signWith:   opts.SignWith,
	}
	if len(kc.rawKey) > 0 && kc.lazy() {
		return keyConfig{}, errors.New("crypto: set either Options.Key or Options.Recipients/Identities, not both")
	}
	if len(kc.rawKey) == 0 && !kc.lazy() {
		return keyConfig{}, errors.New("crypto: a key or recipients are required (this VFS encrypts)")
	}
	return kc, nil
}

// resolveCipher builds the page cipher for a recipients database from the keyslot
// sidecar next to dbPath, exactly once per FS (guarded by cipherOnce). It is run
// at the first main-database open, before any page I/O.
func (fs *FS) resolveCipher(dbPath string) error {
	fs.cipherOnce.Do(func() {
		fs.resolvedPath = dbPath
		fs.cipherErr = fs.doResolveCipher(dbPath)
	})
	// A recipients VFS resolves one data key for one database. A second, different
	// main database opened through the same VFS (e.g. ATTACH via ?vfs=name) would
	// otherwise silently reuse the first one's key — refuse it.
	if fs.cipherErr == nil && dbPath != fs.resolvedPath {
		return fmt.Errorf("crypto: this recipients VFS is bound to %q; open %q with its own crypto.Open", fs.resolvedPath, dbPath)
	}
	return fs.cipherErr
}

func (fs *FS) doResolveCipher(dbPath string) error {
	kc := fs.keyCfg
	sidecar := dbPath + keyslotSuffix
	var dek []byte
	switch blob, rerr := readSidecar(sidecar); {
	case rerr == nil: // open: unwrap an existing keyslot
		d, _, err := keyring.OpenKeyslot(blob, kc.masters, kc.identities...)
		if err != nil {
			return mapKeyslotErr(err)
		}
		dek = d
	case errors.Is(rerr, os.ErrNotExist): // create: generate and wrap a fresh data key
		// Refuse to mint a fresh key when the database already holds data: its
		// pages are under the old, now-lost key, so a new keyslot would make them
		// permanently unreadable. A missing sidecar over real data is a lost or
		// un-shipped sidecar, not a create.
		if fi, serr := os.Stat(dbPath); serr == nil && fi.Size() > 0 {
			return fmt.Errorf("crypto: database %q exists but its keyslot %q is missing — refusing to create a new key (would make the data unreadable)", dbPath, sidecar)
		}
		if len(kc.recipients) == 0 && len(kc.masters) == 0 {
			return fmt.Errorf("crypto: keyslot %q is missing and no Recipients were given to create it", sidecar)
		}
		dek = make([]byte, KeyLen(kc.cipher))
		if _, err := rand.Read(dek); err != nil {
			return err
		}
		blob, err := keyring.SealKeyslot(dek, keyring.Membership{Masters: kc.masters, Members: kc.recipients}, kc.signWith)
		if err != nil {
			return err
		}
		if err := writeSidecar(sidecar, blob); err != nil {
			return err
		}
	default:
		return rerr
	}
	cph, err := NewCipher(kc.cipher, dek)
	if err != nil {
		return err
	}
	fs.cipher.Store(&cph)
	return nil
}

// readSidecar reads the keyslot sidecar, bounded against an oversized (corrupt or
// hostile) file so a giant ".keyslot" can't OOM the process before parsing. A
// missing file is returned as os.ErrNotExist (the create signal).
func readSidecar(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	blob, err := io.ReadAll(io.LimitReader(f, maxKeyslotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(blob) > maxKeyslotBytes {
		return nil, fmt.Errorf("crypto: keyslot %q is larger than %d bytes (corrupt?)", path, maxKeyslotBytes)
	}
	return blob, nil
}

func mapKeyslotErr(err error) error {
	switch {
	case errors.Is(err, keyring.ErrNoMatch):
		return ErrNoIdentity
	case errors.Is(err, keyring.ErrUnauthorizedKeyslot):
		return ErrUnauthorized
	case errors.Is(err, keyring.ErrNotMaster):
		return ErrNotMaster
	default:
		return err
	}
}

// writeSidecar writes the keyslot durably (temp file → fsync → rename → directory
// fsync) so a crash cannot leave an encrypted database with a half-written or
// absent key. It runs before the database file's first page write.
func writeSidecar(path string, blob []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	write := func() error {
		if _, err := f.Write(blob); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		return f.Close()
	}
	if err := write(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp) // don't leave a half-written temp behind
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil { // make the rename durable
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
