# Coverage: crypto/keyring

`gosqlite.org/crypto/keyring` — the envelope layer behind multi-recipient encryption in `gosqlite.org/vfs/vault`. A random data key encrypts the database; that data key is `Wrap`ped once per recipient into a compact blob any one of them can `Unwrap`. It is a thin, sealed wrapper over `filippo.io/age`, reusing its audited recipient format; the public API never exposes age types. Confidentiality model and the page cipher live in `vfs/crypto`; this package only wraps and unwraps the data key.

## Status legend

- **✓ typed** — exposed by the `keyring` package and exercised by a test in `crypto/keyring/*_test.go`.
- **✗** — out of scope.

## API

| Feature | Status | Test | Notes |
|---|---|---|---|
| `Wrap(dataKey, …Recipient)` / `Unwrap(blob, …Identity)` | ✓ typed | `TestMultiRecipientSSH` | Wrap a data key to one or more recipients into a binary (non-armored) blob; the first matching identity unwraps it; ≥1 recipient required. |
| Multiple recipients, any one opens | ✓ typed | `TestMultiRecipientSSH` | A blob wrapped to two SSH keys unwraps with either key's identity; a third, unlisted identity is rejected with `ErrNoMatch`. |
| `SSHRecipient` / `SSHIdentity` (ed25519, RSA) | ✓ typed | `TestMultiRecipientSSH`, `TestEncryptedSSHKey` | Build a recipient from an `authorized_keys` line and an identity from a PEM private key; a passphrase-encrypted private key is handled via the passphrase argument. |
| `PassphraseRecipient` / `PassphraseIdentity` (scrypt) | ✓ typed | `TestPassphrase` | A shared-passphrase recipient; documented as not combinable with key recipients in one `Wrap` (an age restriction). |
| `GenerateX25519()` | ✓ typed | `TestMultiRecipientSSH` (helpers), used by `vfs/vault` recipient tests | A fresh native age keypair — key-based access without SSH or a shared passphrase. |
| `ErrNoMatch` | ✓ typed | `TestMultiRecipientSSH` | Returned by `Unwrap` when no supplied identity matches; mapped to `vault.ErrNoIdentity` at the VFS boundary. Test with `errors.Is`. |
| Masters: `SealKeyslot` / `OpenKeyslot` (signed membership) | ✓ typed | `TestMasterSealOpen`, `TestMasterMultiple` | A master signs the keyslot's membership (the master set + wrapped data key); a reader that pins the trusted masters verifies the signature before unwrapping. 1+ masters; any one may sign. |
| Masters: `GenerateMaster` / `SSHMasterRecipient` / `SSHMasterIdentity` | ✓ typed | `TestMasterSealOpen` | A master is an ed25519 key — both an age recipient (gets the data key) and a signer. X25519 / passphrase keys can be members, not masters. |
| Masters: forgery & downgrade rejected | ✓ typed | `TestForgeryRejected`, `TestDowngradeRejected`, `TestTamperRejected` | A member that re-seals under its own master is rejected by a reader pinning the real master (`ErrUnauthorizedKeyslot`); a pinned reader also rejects a stripped-to-flat keyslot; tampering fails the signature. |
| Masters: `ResealKeyslot` gate | ✓ typed | (via `vault.TestMasterModel`, `TestRemoveMaster`) | Rewriting a master-protected membership requires a current master (`ErrNotMaster`) and cannot drop all masters; flat→master upgrade requires the actor to be one of the new masters. |
| Writers: `SignState`/`VerifyState` (read-only basis) | ✓ typed | `TestWriterSignVerify` | The keyslot pins a master-authorized writer set (`Membership.Writers`); `OpenKeyslot` returns it; a committed-state signature by a writer verifies, a non-writer's does not. This is the foundation of read-only recipients (authenticated mode) in the VFSes. |

## Design invariants

- **Sealed interfaces.** `Recipient` and `Identity` are sealed interfaces over concrete `age` objects; callers build them only through the loaders here, so the public surface never leaks `filippo.io/age` types and the wire format stays an implementation detail.
- **Binary blobs.** `Wrap` uses age's non-armored encoding — compact bytes suitable for a keyslot block, not ASCII armor.
- **No integrity beyond age's.** The package adds no separate MAC; integrity/confidentiality of the wrapped data key are age's. The database payload's at-rest model (length-preserving, no page MAC) is `vfs/crypto`'s.

## Non-goals

- **The page cipher / at-rest payload encryption** — that is `gosqlite.org/vfs/crypto` (`PageCipher`); keyring only wraps the data key those layers use.
- **Key derivation from passphrases for the raw-key path** — use `crypto.DeriveKey`; keyring's passphrase support is an age scrypt *recipient*, a different mechanism.
- **Mixing a passphrase recipient with key recipients in one `Wrap`** — disallowed by age; use key recipients for multiple parties.
