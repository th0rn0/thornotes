package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type APITokenRepo struct {
	readDB  *sql.DB
	writeDB *sql.DB
}

func NewAPITokenRepo(readDB, writeDB *sql.DB) *APITokenRepo {
	return &APITokenRepo{readDB: readDB, writeDB: writeDB}
}

func (r *APITokenRepo) Create(ctx context.Context, userID int64, name, token, scope string) (*model.APIToken, error) {
	prefix := token
	if len(token) >= 8 {
		prefix = token[:8]
	}
	hash := hashToken(token)

	const q = `
		INSERT INTO api_tokens (user_id, name, token_hash, prefix, scope)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, user_id, name, prefix, scope, created_at, last_used_at`

	row := r.writeDB.QueryRowContext(ctx, q, userID, name, hash, prefix, scope)
	t, err := scanAPIToken(row)
	if err != nil {
		return nil, apperror.Internal("create api token", err)
	}
	t.Token = token // return raw token once to the caller; never stored
	return t, nil
}

func (r *APITokenRepo) GetByToken(ctx context.Context, token string) (*model.APIToken, error) {
	const q = `
		SELECT id, user_id, name, prefix, scope, created_at, last_used_at
		FROM api_tokens WHERE token_hash = ?`

	row := r.readDB.QueryRowContext(ctx, q, hashToken(token))
	t, err := scanAPIToken(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, apperror.Internal("get api token", err)
	}
	return t, nil
}

func (r *APITokenRepo) ListByUser(ctx context.Context, userID int64) ([]*model.APIToken, error) {
	const q = `
		SELECT id, user_id, name, prefix, scope, created_at, last_used_at
		FROM api_tokens WHERE user_id = ? ORDER BY created_at ASC`

	rows, err := r.readDB.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, apperror.Internal("list api tokens", err)
	}
	defer rows.Close()

	var out []*model.APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, apperror.Internal("scan api token", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Internal("rows api tokens", err)
	}
	return out, nil
}

func (r *APITokenRepo) Delete(ctx context.Context, userID, tokenID int64) error {
	const q = `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`
	res, err := r.writeDB.ExecContext(ctx, q, tokenID, userID)
	if err != nil {
		return apperror.Internal("delete api token", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return apperror.Internal("rows affected api token", err)
	}
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}

func (r *APITokenRepo) TouchLastUsed(ctx context.Context, tokenID int64) error {
	const q = `UPDATE api_tokens SET last_used_at = datetime('now') WHERE id = ?`
	_, err := r.writeDB.ExecContext(ctx, q, tokenID)
	if err != nil {
		return apperror.Internal("touch api token last_used_at", err)
	}
	return nil
}

// hashToken returns the hex-encoded SHA-256 hash of the raw token.
func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanAPIToken(s scanner) (*model.APIToken, error) {
	var t model.APIToken
	if err := s.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.LastUsedAt); err != nil {
		return nil, err
	}
	return &t, nil
}
