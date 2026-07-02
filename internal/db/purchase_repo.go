package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func CreatePurchaseRequest(ctx context.Context, r *PurchaseRequest) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if r.Status == "" {
		r.Status = "pending"
	}

	return Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests (
			user_id, type, plan_id, subscription_id, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at
	`, r.UserID, r.Type, r.PlanID, r.SubscriptionID, r.Price, r.Months, r.IPLimit, r.DataGB, r.CustomName, r.ClientEmail, r.TelegramFileID, r.Status).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func GetPurchaseRequestByID(ctx context.Context, id int64) (*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &PurchaseRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, type, plan_id, subscription_id, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, admin_id, created_at, updated_at
		FROM purchase_requests
		WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func ApprovePurchaseRequest(ctx context.Context, id int64, adminID int64) (*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	r := &PurchaseRequest{}
	err = tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'approved', admin_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
		RETURNING id, user_id, type, plan_id, subscription_id, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// Add to transactions table
	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id)
		VALUES ($1, $2, 'debit', 'completed', $3, 'purchase_request', $4)
	`, r.UserID, int64(r.Price), "direct purchase approved: "+r.Type+" - "+r.ClientEmail, r.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func RejectPurchaseRequest(ctx context.Context, id int64, adminID int64) (*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &PurchaseRequest{}
	err := Pool.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'rejected', admin_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
		RETURNING id, user_id, type, plan_id, subscription_id, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func GetPendingPurchaseRequests(ctx context.Context) ([]*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, `
		SELECT id, user_id, type, plan_id, subscription_id, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, admin_id, created_at, updated_at
		FROM purchase_requests
		WHERE status = 'pending'
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reqs []*PurchaseRequest
	for rows.Next() {
		r := &PurchaseRequest{}
		if err := rows.Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		reqs = append(reqs, r)
	}
	return reqs, rows.Err()
}

func RollbackPurchaseRequest(ctx context.Context, id int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `UPDATE purchase_requests SET status = 'pending', admin_id = NULL WHERE id = $1`, id)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `DELETE FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1`, id)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func HasPendingClaimRequest(ctx context.Context, subID string) (bool, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var exists bool
	err := Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM purchase_requests 
			WHERE type = 'claim' AND custom_name = $1 AND status = 'pending'
		)
	`, subID).Scan(&exists)
	return exists, err
}

func CreateRefundRequest(ctx context.Context, r *RefundRequest) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if r.Status == "" {
		r.Status = "pending"
	}

	return Pool.QueryRow(ctx, `
		INSERT INTO refund_requests (
			user_id, subscription_id, calculated_amount, status
		) VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at
	`, r.UserID, r.SubscriptionID, r.CalculatedAmount, r.Status).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func GetRefundRequestByID(ctx context.Context, id int64) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &RefundRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, created_at, updated_at
		FROM refund_requests
		WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func ApproveRefundRequest(ctx context.Context, id int64, adminID int64, approvedAmount int64) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &RefundRequest{}
	err := Pool.QueryRow(ctx, `
		UPDATE refund_requests
		SET status = 'approved', admin_id = $1, approved_amount = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'pending'
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, created_at, updated_at
	`, adminID, approvedAmount, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func RejectRefundRequest(ctx context.Context, id int64, adminID int64) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &RefundRequest{}
	err := Pool.QueryRow(ctx, `
		UPDATE refund_requests
		SET status = 'rejected', admin_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func RollbackRefundRequest(ctx context.Context, id int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE refund_requests SET status = 'pending', admin_id = NULL, approved_amount = NULL WHERE id = $1`, id)
	return err
}
