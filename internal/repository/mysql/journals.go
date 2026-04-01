package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/th0rn0/thornotes/internal/apperror"
	"github.com/th0rn0/thornotes/internal/model"
)

type JournalRepo struct {
	db *sql.DB
}

func NewJournalRepo(db *sql.DB) *JournalRepo {
	return &JournalRepo{db: db}
}

func (r *JournalRepo) Create(ctx context.Context, userID int64, name string) (*model.Journal, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO journals (user_id, name) VALUES (?, ?)`,
		userID, name,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, apperror.Conflict(fmt.Sprintf("journal %q already exists", name))
		}
		return nil, fmt.Errorf("create journal: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create journal last insert id: %w", err)
	}
	return r.GetByID(ctx, userID, id)
}

func (r *JournalRepo) GetByID(ctx context.Context, userID, journalID int64) (*model.Journal, error) {
	j := &model.Journal{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, created_at FROM journals WHERE id = ? AND user_id = ?`,
		journalID, userID,
	).Scan(&j.ID, &j.UserID, &j.Name, &j.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperror.ErrNotFound
		}
		return nil, fmt.Errorf("get journal: %w", err)
	}
	return j, nil
}

func (r *JournalRepo) ListByUser(ctx context.Context, userID int64) ([]*model.Journal, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, user_id, name, created_at FROM journals WHERE user_id = ? ORDER BY name`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list journals: %w", err)
	}
	defer rows.Close()

	var journals []*model.Journal
	for rows.Next() {
		j := &model.Journal{}
		if err := rows.Scan(&j.ID, &j.UserID, &j.Name, &j.CreatedAt); err != nil {
			return nil, err
		}
		journals = append(journals, j)
	}
	if journals == nil {
		journals = []*model.Journal{}
	}
	return journals, rows.Err()
}

func (r *JournalRepo) Delete(ctx context.Context, userID, journalID int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM journals WHERE id = ? AND user_id = ?`, journalID, userID,
	)
	if err != nil {
		return fmt.Errorf("delete journal: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperror.ErrNotFound
	}
	return nil
}
