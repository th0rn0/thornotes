package mysql

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
	db *sql.DB
}

func NewAPITokenRepo(db *sql.DB) *APITokenRepo {
	return &APITokenRepo{db: db}
}

func (r *APITokenRepo) Create(ctx context.Context, userID int64, name, token, scope string) (*model.APIToken, error) {
	prefix := token
	if len(token) >= 8 {
		prefix = token[:8]
	}
	hash := hashToken(token)

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO api_tokens (user_id, name, token_hash, prefix, scope) VALUES (?, ?, ?, ?, ?)`,
		userID, name, hash, prefix, scope,
	)
	if err != nil {
		return nil, apperror.Internal("create api token", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, apperror.Internal("create api token last insert id", err)
	}

	t := &model.APIToken{}
	err = r.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, scope, created_at, last_used_at FROM api_tokens WHERE id = ?`, id,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, apperror.Internal("scan created api token", err)
	}
	t.Token = token // return raw token once; never stored
	return t, nil
}

func (r *APITokenRepo) GetByToken(ctx context.Context, token string) (*model.APIToken, error) {
	t := &model.APIToken{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, prefix, scope, created_at, last_used_at
		 FROM api_tokens WHERE token_hash = ?`, hashToken(token),
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, apperror.Internal("get api token", err)
	}
	return t, nil
}

func (r *APITokenRepo) ListByUser(ctx context.Context, userID int64) ([]*model.APIToken, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, name, prefix, scope, created_at, last_used_at
		 FROM api_tokens WHERE user_id = ? ORDER BY created_at ASC`, userID,
	)
	if err != nil {
		return nil, apperror.Internal("list api tokens", err)
	}
	defer rows.Close()

	var out []*model.APIToken
	for rows.Next() {
		t := &model.APIToken{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedAt, &t.LastUsedAt); err != nil {
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
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, tokenID, userID,
	)
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
	_, err := r.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = UTC_TIMESTAMP() WHERE id = ?`, tokenID,
	)
	if err != nil {
		return apperror.Internal("touch api token last_used_at", err)
	}
	return nil
}

// hashToken returns the hex-encoded SHA-256 of the raw token.
func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
