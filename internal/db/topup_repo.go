package db

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
)

func CreateTopupRequest(ctx context.Context, r *TopupRequest) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if r.Status == "" {
		r.Status = "pending"
	}
	return Pool.QueryRow(ctx, `
		INSERT INTO topup_requests (user_id, telegram_file_id, status)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at
	`, r.UserID, r.TelegramFileID, r.Status).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func GetTopupRequestByID(ctx context.Context, id int64) (*TopupRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &TopupRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, telegram_file_id, status, amount, admin_id, created_at, updated_at
		FROM topup_requests
		WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.TelegramFileID, &r.Status, &r.Amount, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func ApproveTopupRequest(ctx context.Context, id int64, adminID int64, amount float64) (*TopupRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amountInt := int64(math.Round(amount))
	if amountInt <= 0 {
		return nil, errors.New("amount must be positive")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	r := &TopupRequest{}
	err = tx.QueryRow(ctx, `
		UPDATE topup_requests
		SET status = 'approved', amount = $1, admin_id = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'pending'
		RETURNING id, user_id, telegram_file_id, status, amount, admin_id, created_at, updated_at
	`, amountInt, adminID, id).Scan(&r.ID, &r.UserID, &r.TelegramFileID, &r.Status, &r.Amount, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amountInt, r.UserID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id)
		VALUES ($1, $2, 'credit', 'completed', 'top-up approved', 'topup_request', $3)
	`, r.UserID, amountInt, r.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func RejectTopupRequest(ctx context.Context, id int64, adminID int64) (*TopupRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &TopupRequest{}
	err := Pool.QueryRow(ctx, `
		UPDATE topup_requests
		SET status = 'rejected', admin_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
		RETURNING id, user_id, telegram_file_id, status, amount, admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.TelegramFileID, &r.Status, &r.Amount, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func GetPendingTopupRequests(ctx context.Context) ([]*TopupRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, `
		SELECT id, user_id, telegram_file_id, status, amount, admin_id, created_at, updated_at
		FROM topup_requests
		WHERE status = 'pending'
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []*TopupRequest
	for rows.Next() {
		r := &TopupRequest{}
		if err := rows.Scan(&r.ID, &r.UserID, &r.TelegramFileID, &r.Status, &r.Amount, &r.AdminID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}
	return reqs, rows.Err()
}
