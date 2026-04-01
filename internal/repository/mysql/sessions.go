package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type SessionRepo struct {
	db *sql.DB
}

func NewSessionRepo(db *sql.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

func (r *SessionRepo) Create(ctx context.Context, token string, userID int64, ttlSeconds int) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at)
		 VALUES (?, ?, DATE_ADD(UTC_TIMESTAMP(), INTERVAL ? SECOND))`,
		token, userID, ttlSeconds,
	)
	return err
}

func (r *SessionRepo) Get(ctx context.Context, token string) (*model.Session, error) {
	s := &model.Session{}
	err := r.db.QueryRowContext(ctx,
		`SELECT token, user_id, expires_at, created_at FROM sessions
		 WHERE token = ? AND expires_at > UTC_TIMESTAMP()`,
		token,
	).Scan(&s.Token, &s.UserID, &s.ExpiresAt, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

func (r *SessionRepo) Delete(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (r *SessionRepo) DeleteExpired(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= UTC_TIMESTAMP()`)
	return err
}
