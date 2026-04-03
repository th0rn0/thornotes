package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type UserRepo struct {
	db *sql.DB
}

func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Create(ctx context.Context, username, passwordHash, uuid string) (*model.User, error) {
	var id int64
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, uuid) VALUES (?, ?, ?) RETURNING id`,
		username, passwordHash, uuid,
	).Scan(&id)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("username %q already taken", username))
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	return r.GetByUsername(ctx, username)
}

func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	u := &model.User{}
	var uuid sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, uuid, username, password_hash, created_at FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &uuid, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	u.UUID = uuid.String
	return u, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id int64) (*model.User, error) {
	u := &model.User{}
	var uuid sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, uuid, username, password_hash, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &uuid, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	u.UUID = uuid.String
	return u, nil
}

func (r *UserRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (r *UserRepo) IDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM users`)
	if err != nil {
		return nil, fmt.Errorf("list user ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *UserRepo) SetUUID(ctx context.Context, id int64, uuid string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE users SET uuid = ? WHERE id = ?`, uuid, id)
	return err
}

func (r *UserRepo) ListWithoutUUID(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM users WHERE uuid IS NULL OR uuid = ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
