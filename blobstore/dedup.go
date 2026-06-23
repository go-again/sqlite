package blobstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// putBlockDedup is the [WithDedup] variant of putBlock: it stores (data, enc) as
// chunk (id, seq), but first looks for an already-stored block with identical
// content and references that instead of writing a copy. A write of unchanged
// content is a no-op; a write to new content repoints the chunk and releases its
// previous block. Only full-block writes (compressed objects, full-chunk
// updates, mode conversion) flow through here — raw in-place partial writes are
// not deduplicated.
func (s *Store) putBlockDedup(ctx context.Context, sc *sql.Conn, id, seq int64, data []byte, enc int) error {
	h := blockHash(data, enc)
	cur, hadCur, err := s.blockOf(ctx, sc, id, seq)
	if err != nil {
		return err
	}
	existing, found, err := s.blockByHash(ctx, sc, h)
	if err != nil {
		return err
	}
	if found {
		if hadCur && cur == existing {
			return nil // already canonical — identical content, no change
		}
		if _, err := sc.ExecContext(ctx,
			`UPDATE `+s.blocks+` SET refs = refs + 1 WHERE id = ?`, existing); err != nil {
			return err
		}
		if err := s.mapChunk(ctx, sc, id, seq, existing); err != nil {
			return err
		}
		if hadCur {
			return s.releaseBlock(ctx, sc, cur)
		}
		return nil
	}
	block, err := s.newHashedBlock(ctx, sc, data, enc, h)
	if err != nil {
		return err
	}
	if err := s.mapChunk(ctx, sc, id, seq, block); err != nil {
		return err
	}
	if hadCur {
		return s.releaseBlock(ctx, sc, cur)
	}
	return nil
}

// blockHash is the content key for dedup: SHA-256 over the encoding tag and the
// stored bytes, so a verbatim block and a compressed block with the same
// plaintext (different stored bytes) never collide.
func blockHash(data []byte, enc int) []byte {
	h := sha256.New()
	h.Write([]byte{byte(enc)})
	h.Write(data)
	return h.Sum(nil)
}

// blockByHash finds the (unique) block with content hash h, if any. It consults
// the partial unique index on hash, so it only ever matches deduped blocks.
func (s *Store) blockByHash(ctx context.Context, sc *sql.Conn, h []byte) (int64, bool, error) {
	var id int64
	err := sc.QueryRowContext(ctx,
		`SELECT id FROM `+s.blocks+` WHERE hash = ?`, h).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// newHashedBlock inserts a new, privately-owned block (refs == 1) carrying its
// content hash, and returns its id.
func (s *Store) newHashedBlock(ctx context.Context, sc *sql.Conn, data []byte, enc int, h []byte) (int64, error) {
	res, err := sc.ExecContext(ctx,
		`INSERT INTO `+s.blocks+` (data, enc, hash) VALUES (?, ?, ?)`, data, enc, h)
	if err != nil {
		return 0, fmt.Errorf("blobstore: dedup insert: %w", err)
	}
	return res.LastInsertId()
}

// releaseBlock drops one reference from block and frees it if that was the last
// one. Unlike decBlockRef (used on the copy-on-write path, where the block is
// known to remain shared), this is for the dedup repoint path, where the old
// block may reach zero references.
func (s *Store) releaseBlock(ctx context.Context, sc *sql.Conn, block int64) error {
	if _, err := sc.ExecContext(ctx,
		`UPDATE `+s.blocks+` SET refs = refs - 1 WHERE id = ?`, block); err != nil {
		return err
	}
	_, err := sc.ExecContext(ctx,
		`DELETE FROM `+s.blocks+` WHERE id = ? AND refs <= 0`, block)
	return err
}
