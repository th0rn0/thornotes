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

func (r *APITokenRepo) ListPermissions(ctx context.Context, tokenID int64) ([]model.TokenFolderPermission, error) {
	const q = `
		SELECT folder_id, permission
		FROM api_token_folder_permissions
		WHERE token_id = ?
		ORDER BY folder_id IS NULL DESC, folder_id`
	rows, err := r.readDB.QueryContext(ctx, q, tokenID)
	if err != nil {
		return nil, apperror.Internal("list token permissions", err)
	}
	defer rows.Close()

	var out []model.TokenFolderPermission
	for rows.Next() {
		var p model.TokenFolderPermission
		if err := rows.Scan(&p.FolderID, &p.Permission); err != nil {
			return nil, apperror.Internal("scan token permission", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, apperror.Internal("rows token permissions", err)
	}
	return out, nil
}

func (r *APITokenRepo) SetPermissions(ctx context.Context, userID, tokenID int64, perms []model.TokenFolderPermission) error {
	for _, p := range perms {
		if p.Permission != "read" && p.Permission != "write" {
			return apperror.BadRequest("permission must be \"read\" or \"write\"")
		}
	}

	tx, err := r.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return apperror.Internal("begin set permissions", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Confirm the token belongs to userID before mutating anything.
	var ownerID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id FROM api_tokens WHERE id = ?`, tokenID,
	).Scan(&ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperror.ErrNotFound
		}
		return apperror.Internal("lookup token owner", err)
	}
	if ownerID != userID {
		return apperror.ErrNotFound
	}

	// Verify every non-nil folder_id belongs to the same user.
	for _, p := range perms {
		if p.FolderID == nil {
			continue
		}
		var folderOwner int64
		if err := tx.QueryRowContext(ctx,
			`SELECT user_id FROM folders WHERE id = ?`, *p.FolderID,
		).Scan(&folderOwner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return apperror.BadRequest("folder does not exist")
			}
			return apperror.Internal("lookup folder owner", err)
		}
		if folderOwner != userID {
			return apperror.BadRequest("folder does not belong to user")
		}
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM api_token_folder_permissions WHERE token_id = ?`, tokenID,
	); err != nil {
		return apperror.Internal("clear token permissions", err)
	}

	for _, p := range perms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO api_token_folder_permissions (token_id, folder_id, permission) VALUES (?, ?, ?)`,
			tokenID, p.FolderID, p.Permission,
		); err != nil {
			return apperror.Internal("insert token permission", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return apperror.Internal("commit set permissions", err)
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
