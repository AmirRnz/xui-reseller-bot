package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func CreatePurchaseRequest(ctx context.Context, r *PurchaseRequest) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if r.Status == "" {
		r.Status = "pending"
	}
	if r.ProvisioningStatus == "" {
		r.ProvisioningStatus = PurchaseProvisioningPending
	}
	snapshot, err := json.Marshal(r.ProvisioningSnapshot)
	if err != nil {
		return fmt.Errorf("failed to serialize provisioning snapshot: %w", err)
	}

	return Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests (
			user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, operation_key, provisioning_snapshot
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''), $17::jsonb)
		RETURNING id, created_at, updated_at
	`, r.UserID, r.Type, r.PlanID, r.SubscriptionID, r.QuoteID, r.PriceToman, r.Price, r.Months, r.IPLimit, r.DataGB, r.CustomName, r.ClientEmail, r.TelegramFileID, r.Status, r.ProvisioningStatus, r.OperationKey, snapshot).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func GetPurchaseRequestByID(ctx context.Context, id int64) (*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &PurchaseRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at, provisioning_snapshot
		FROM purchase_requests
		WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.QuoteID, &r.PriceToman, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.ProvisioningStatus, &r.OperationKey, &r.AdminID, &r.CreatedAt, &r.UpdatedAt, &r.ProvisioningSnapshot)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func ApprovePurchaseRequest(ctx context.Context, id int64, adminID int64, workItem *ReconciliationRecord) (*PurchaseRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if workItem == nil {
		return nil, errors.New("approved purchase requires durable provisioning work")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	r := &PurchaseRequest{}
	err = tx.QueryRow(ctx, `
		UPDATE purchase_requests
		SET status = 'approved', provisioning_status = 'pending', admin_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
		RETURNING id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.QuoteID, &r.PriceToman, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.ProvisioningStatus, &r.OperationKey, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// The approval audit row and executable direct-payment recovery record are
	// committed together. Price is accepted only from the integer-Toman column.
	operationKey := r.OperationKey
	if operationKey == "" {
		operationKey = fmt.Sprintf("purchase_approval:%d", r.ID)
	}
	if workItem.PurchaseRequestID == nil || *workItem.PurchaseRequestID != r.ID || workItem.UserID == nil || *workItem.UserID != r.UserID {
		return nil, errors.New("provisioning work item does not match approved purchase")
	}
	if r.Type != "buy" && r.Type != "extend" && r.Type != "upgrade_ip" {
		return nil, fmt.Errorf("unsupported purchase action type %q", r.Type)
	}
	if workItem.Kind != "direct_payment_provisioning_retry" {
		return nil, errors.New("paid purchase approval requires durable direct-payment work")
	}
	if r.PriceToman == nil || *r.PriceToman <= 0 {
		return nil, errors.New("approved purchase is missing an integer-Toman amount")
	}
	debitAmount := *r.PriceToman
	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
		VALUES ($1, $2, 'debit', 'completed', $3, 'purchase_request', $4, $5)
		ON CONFLICT (operation_key) DO NOTHING
	`, r.UserID, debitAmount, "direct purchase approved: "+r.Type+" - "+r.ClientEmail, r.ID, operationKey)
	if err != nil {
		return nil, err
	}
	if err := createReconciliationRecordTx(ctx, tx, workItem); err != nil {
		return nil, fmt.Errorf("failed to persist provisioning work with direct-payment approval: %w", err)
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
		RETURNING id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.QuoteID, &r.PriceToman, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.ProvisioningStatus, &r.OperationKey, &r.AdminID, &r.CreatedAt, &r.UpdatedAt)
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
		SELECT id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at
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
		if err := rows.Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.QuoteID, &r.PriceToman, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.ProvisioningStatus, &r.OperationKey, &r.AdminID, &r.CreatedAt, &r.UpdatedAt); err != nil {
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

	_, err = tx.Exec(ctx, `UPDATE purchase_requests SET status = 'approved', provisioning_status = 'retryable', updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func SetPurchaseProvisioningStatus(ctx context.Context, id int64, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	_, err := Pool.Exec(ctx, `UPDATE purchase_requests SET provisioning_status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
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
			user_id, subscription_id, calculated_amount, status, operation_key
		) VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (operation_key) DO UPDATE SET updated_at = refund_requests.updated_at
		RETURNING id, created_at, updated_at
	`, r.UserID, r.SubscriptionID, r.CalculatedAmount, r.Status, r.OperationKey).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

func GetRefundRequestByID(ctx context.Context, id int64) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &RefundRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
		FROM refund_requests
		WHERE id = $1
	`, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func GetRefundRequestByOperationKey(ctx context.Context, operationKey string) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	r := &RefundRequest{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
		FROM refund_requests
		WHERE operation_key = $1
	`, operationKey).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func ApproveRefundRequest(ctx context.Context, id int64, adminID int64, approvedAmount int64, auditNote string) (*RefundRequest, error) {
	return ApproveRefundRequestAndCredit(ctx, id, adminID, approvedAmount, auditNote)
}

// ApproveRefundRequestAndCredit makes the approval state transition and wallet
// credit one durable idempotent database operation.
func ApproveRefundRequestAndCredit(ctx context.Context, id int64, adminID int64, approvedAmount int64, auditNote string) (*RefundRequest, error) {
	if approvedAmount <= 0 {
		return nil, errors.New("approvedAmount must be greater than 0")
	}
	if strings.TrimSpace(auditNote) == "" {
		return nil, errors.New("auditNote is required")
	}

	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	r := &RefundRequest{}
	err = tx.QueryRow(ctx, `
		UPDATE refund_requests
		SET status = 'approved', admin_id = $1, approved_amount = $2, approved_at = NOW(), audit_note = $3, updated_at = NOW()
		WHERE id = $4 AND status = 'pending'
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
	`, adminID, approvedAmount, auditNote, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	walletUpdate, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, approvedAmount, r.UserID)
	if err != nil {
		return nil, err
	}
	if walletUpdate.RowsAffected() != 1 {
		return nil, errors.New("refund recipient wallet was not found")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
		VALUES ($1, $2, 'credit', 'completed', $3, 'refund_request', $4, $5)
		ON CONFLICT (operation_key) DO NOTHING
	`, r.UserID, approvedAmount, fmt.Sprintf("Refund approved for request #%d", r.ID), r.ID, fmt.Sprintf("refund_approval:%d", r.ID)); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
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
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
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
