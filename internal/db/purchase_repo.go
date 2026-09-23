package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
		RETURNING id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at, provisioning_snapshot
	`, adminID, id).Scan(&r.ID, &r.UserID, &r.Type, &r.PlanID, &r.SubscriptionID, &r.QuoteID, &r.PriceToman, &r.Price, &r.Months, &r.IPLimit, &r.DataGB, &r.CustomName, &r.ClientEmail, &r.TelegramFileID, &r.Status, &r.ProvisioningStatus, &r.OperationKey, &r.AdminID, &r.CreatedAt, &r.UpdatedAt, &r.ProvisioningSnapshot)
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
	if r.Type == "extend" || r.Type == "upgrade_ip" {
		if err := reserveDirectSubscriptionMutation(ctx, tx, r, workItem); err != nil {
			return nil, err
		}
	} else if r.Type == "buy" {
		if err := validateDirectPurchaseApproval(ctx, tx, r, workItem); err != nil {
			return nil, err
		}
	}
	debitAmount := *r.PriceToman
	var transactionID int64
	err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
			VALUES ($1, $2, 'debit', 'completed', $3, 'purchase_request', $4, $5)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, r.UserID, -debitAmount, "direct purchase approved: "+r.Type+" - "+r.ClientEmail, r.ID, operationKey).Scan(&transactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = verifyWalletOperationEffect(ctx, tx, r.UserID, -debitAmount, "debit", "direct purchase approved: "+r.Type+" - "+r.ClientEmail, operationKey, "purchase_request", &r.ID)
		if errors.Is(err, ErrWalletOperationAlreadyApplied) {
			err = nil
		}
	}
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

func reserveDirectSubscriptionMutation(ctx context.Context, tx pgx.Tx, request *PurchaseRequest, workItem *ReconciliationRecord) error {
	if request.SubscriptionID == nil || request.Months < 1 || request.Months > 120 {
		return ErrSubscriptionMutationInvalid
	}
	expectedUpdatedAt, updatedOK := snapshotTime(request.ProvisioningSnapshot, "expected_subscription_updated_at")
	expectedPlanUpdatedAt, planTimeOK := snapshotTime(request.ProvisioningSnapshot, "expected_plan_updated_at")
	expectedIP, ipOK := snapshotInt64Value(request.ProvisioningSnapshot["expected_ip_limit"])
	expectedExpiry, expiryOK := snapshotInt64Value(request.ProvisioningSnapshot["expected_expire_time_milli"])
	expectedActive, activeOK := request.ProvisioningSnapshot["expected_is_active"].(bool)
	if !updatedOK || !planTimeOK || !ipOK || !expiryOK || !activeOK {
		return ErrSubscriptionMutationStale
	}
	var owner int64
	var email, status, planType string
	var planID *int64
	var ipLimit int
	var expireTime *int64
	var active bool
	var desiredIP *int
	var desiredExpiry *int64
	var desiredActive *bool
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT user_id, client_email, status, plan_type, plan_id, ip_limit, expire_time, is_active,
		       desired_ip_limit, desired_expire_time, desired_is_active, updated_at
		FROM subscriptions WHERE id = $1 FOR UPDATE
	`, *request.SubscriptionID).Scan(&owner, &email, &status, &planType, &planID, &ipLimit, &expireTime, &active,
		&desiredIP, &desiredExpiry, &desiredActive, &updatedAt)
	if err != nil {
		return err
	}
	currentExpiry := int64(0)
	if expireTime != nil {
		currentExpiry = *expireTime
	}
	if owner != request.UserID || email != request.ClientEmail || planType != PlanTypePaid || planID == nil || request.PlanID == nil || int64(*request.PlanID) != *planID ||
		status != SubscriptionStatusActive || !active || desiredIP != nil || desiredExpiry != nil || desiredActive != nil ||
		!updatedAt.Equal(expectedUpdatedAt) || int64(ipLimit) != expectedIP || currentExpiry != expectedExpiry || active != expectedActive {
		return ErrSubscriptionMutationStale
	}
	var enabled bool
	var maxIP int
	var planUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT enabled, max_ip_limit, updated_at FROM paid_plans WHERE id = $1 FOR SHARE`, *planID).
		Scan(&enabled, &maxIP, &planUpdatedAt); err != nil {
		return err
	}
	if !enabled || !planUpdatedAt.Equal(expectedPlanUpdatedAt) {
		return ErrSubscriptionMutationStale
	}
	if workItem.DesiredState["action_type"] != request.Type || workItem.DesiredState["client_email"] != request.ClientEmail ||
		int64FromMap(workItem.DesiredState, "subscription_id") != *request.SubscriptionID ||
		int64FromMap(workItem.DesiredState, "amount_toman") != *request.PriceToman ||
		int64FromMap(workItem.DesiredState, "months") != int64(request.Months) {
		return ErrSubscriptionMutationStale
	}
	desiredIPValue := ipLimit
	if request.Type == "upgrade_ip" {
		desiredIPValue = request.IPLimit
		workItemIP, ok := snapshotInt64Value(workItem.DesiredState["desired_ip_limit"])
		if !ok || workItemIP != int64(desiredIPValue) || desiredIPValue <= ipLimit || maxIP <= 0 || desiredIPValue > maxIP {
			return ErrSubscriptionMutationInvalid
		}
	} else if request.Type != "extend" {
		return ErrSubscriptionMutationInvalid
	} else if request.IPLimit != ipLimit {
		return ErrSubscriptionMutationStale
	}
	if currentExpiry == 0 {
		return ErrSubscriptionMutationInvalid
	}
	monthMillis := int64(request.Months) * 30 * 24 * 60 * 60 * 1000
	desiredExpiryValue := currentExpiry
	if request.Type == "extend" {
		if currentExpiry < 0 {
			desiredExpiryValue = currentExpiry - monthMillis
		} else {
			baseExpiry := currentExpiry
			if now := time.Now().UTC().UnixMilli(); baseExpiry < now {
				baseExpiry = now
			}
			desiredExpiryValue = baseExpiry + monthMillis
		}
	}
	desiredIPPtr := desiredIPValue
	desiredExpiryPtr := desiredExpiryValue
	desiredActiveValue := true
	workItem.DesiredState["expiry_time_milli"] = desiredExpiryValue
	_, err = tx.Exec(ctx, `
		UPDATE subscriptions
		SET status = $1, desired_ip_limit = $2, desired_expire_time = $3, desired_is_active = $4,
		    reconciliation_note = $5, updated_at = NOW()
		WHERE id = $6 AND user_id = $7
	`, SubscriptionStatusReconciliation, &desiredIPPtr, &desiredExpiryPtr, &desiredActiveValue,
		"direct payment update awaits durable XUI reconciliation", *request.SubscriptionID, request.UserID)
	return err
}

func validateDirectPurchaseApproval(ctx context.Context, tx pgx.Tx, request *PurchaseRequest, workItem *ReconciliationRecord) error {
	if request.PlanID == nil || request.QuoteID == nil || request.PriceToman == nil {
		return ErrSubscriptionMutationInvalid
	}
	intent := &PaymentIntent{
		UserID: request.UserID, ActionType: "buy", PlanID: ptrInt64(int64(*request.PlanID)), QuoteID: request.QuoteID,
		AmountToman: *request.PriceToman, Months: request.Months, IPLimit: request.IPLimit, DataGB: request.DataGB,
		ClientEmail: request.ClientEmail, ProvisioningSnapshot: request.ProvisioningSnapshot,
	}
	if err := validateDirectBuyIntentTx(ctx, tx, intent); err != nil {
		return err
	}
	wanted := workItem.DesiredState
	if wanted["action_type"] != "buy" || wanted["client_email"] != request.ClientEmail ||
		int64FromMap(wanted, "user_id") != request.UserID || int64FromMap(wanted, "amount_toman") != *request.PriceToman ||
		int64FromMap(wanted, "months") != int64(request.Months) || int64FromMap(wanted, "ip_limit") != int64(request.IPLimit) ||
		int64FromMap(wanted, "data_gb") != int64(request.DataGB) || int64FromMap(wanted, "quote_id") != *request.QuoteID ||
		int64FromMap(wanted, "plan_id") != int64(*request.PlanID) {
		return ErrSubscriptionMutationStale
	}
	if wanted["expected_uuid"] != request.ProvisioningSnapshot["client_uuid"] || wanted["expected_sub_id"] != request.ProvisioningSnapshot["sub_id"] {
		return ErrSubscriptionMutationStale
	}
	return nil
}

func int64FromMap(values map[string]any, key string) int64 {
	value, _ := snapshotInt64Value(values[key])
	return value
}

func ptrInt64(value int64) *int64 { return &value }

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
		SELECT id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id,
		       COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
		FROM refund_requests WHERE id = $1 FOR UPDATE
	`, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if r.Status != "pending" {
		return nil, nil
	}
	if r.SubscriptionID != nil {
		var subscriptionStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM subscriptions WHERE id = $1 FOR SHARE`, *r.SubscriptionID).Scan(&subscriptionStatus); err != nil {
			return nil, err
		}
		if subscriptionStatus != SubscriptionStatusCancelled {
			return nil, ErrSubscriptionCancellationNotComplete
		}
	}

	description := fmt.Sprintf("Refund approved for request #%d", r.ID)
	operationKey := fmt.Sprintf("refund_approval:%d", r.ID)
	var transactionID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
		VALUES ($1, $2, 'credit', 'completed', $3, 'refund_request', $4, $5)
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id
	`, r.UserID, approvedAmount, description, r.ID, operationKey).Scan(&transactionID)
	applyCredit := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		err = verifyWalletOperationEffect(ctx, tx, r.UserID, approvedAmount, "credit", description, operationKey, "refund_request", &r.ID)
		if errors.Is(err, ErrWalletOperationAlreadyApplied) {
			applyCredit = false
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	if applyCredit {
		walletUpdate, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, approvedAmount, r.UserID)
		if err != nil {
			return nil, err
		}
		if walletUpdate.RowsAffected() != 1 {
			return nil, errors.New("refund recipient wallet was not found")
		}
	}
	err = tx.QueryRow(ctx, `
		UPDATE refund_requests
		SET status = 'approved', admin_id = $1, approved_amount = $2, approved_at = NOW(), audit_note = $3, updated_at = NOW()
		WHERE id = $4 AND status = 'pending'
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id,
		          COALESCE(operation_key, ''), approved_at, audit_note, created_at, updated_at
	`, adminID, approvedAmount, auditNote, id).Scan(&r.ID, &r.UserID, &r.SubscriptionID, &r.CalculatedAmount, &r.ApprovedAmount, &r.Status, &r.AdminID, &r.OperationKey, &r.ApprovedAt, &r.AuditNote, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
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
