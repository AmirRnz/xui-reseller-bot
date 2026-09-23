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

type PaymentIntentStatus string

const (
	IntentStatusAwaitingReceipt  = "awaiting_receipt"
	IntentStatusReceiptSubmitted = "receipt_submitted"
	IntentStatusCancelled        = "cancelled"
	IntentStatusExpired          = "expired"
)

type PaymentIntent struct {
	ID                   int64          `json:"id"`
	UserID               int64          `json:"user_id"`
	IntentToken          string         `json:"intent_token"`
	ActionType           string         `json:"action_type"`
	PlanID               *int64         `json:"plan_id,omitempty"`
	SubscriptionID       *int64         `json:"subscription_id,omitempty"`
	QuoteID              *int64         `json:"quote_id,omitempty"`
	AmountToman          int64          `json:"amount_toman"`
	Months               int            `json:"months"`
	IPLimit              int            `json:"ip_limit"`
	DataGB               int            `json:"data_gb"`
	DisplayName          string         `json:"display_name"`
	ClientEmail          string         `json:"client_email"`
	ProvisioningSnapshot map[string]any `json:"provisioning_snapshot"`
	Status               string         `json:"status"`
	CancelledAt          *time.Time     `json:"cancelled_at,omitempty"`
	CancelledByUserID    *int64         `json:"cancelled_by_user_id,omitempty"`
	CancellationReason   string         `json:"cancellation_reason,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

var ErrPaymentIntentNotFound = errors.New("payment intent not found")
var ErrActivePaymentIntentExists = errors.New("an active payment intent already exists for this user")
var ErrPaymentIntentNotCancellable = errors.New("payment intent is no longer awaiting receipt")

func CreatePaymentIntent(ctx context.Context, intent *PaymentIntent) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if intent.ActionType == "new_subscription" {
		intent.ActionType = "buy"
	}
	switch intent.ActionType {
	case "buy", "extend", "upgrade_ip", "topup":
	default:
		return nil, fmt.Errorf("unsupported payment intent action %q", intent.ActionType)
	}

	if intent.Status == "" {
		intent.Status = IntentStatusAwaitingReceipt
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Serialize checkout creation per user. An existing card intent may already
	// have been paid externally, so preserve it and reject the newer checkout.
	var lockedUserID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM bot_users WHERE id = $1 FOR UPDATE`, intent.UserID).Scan(&lockedUserID); err != nil {
		return nil, fmt.Errorf("failed to lock payment-intent owner: %w", err)
	}
	if intent.ActionType == "extend" || intent.ActionType == "upgrade_ip" {
		if err := validateAndSnapshotDirectSubscriptionIntent(ctx, tx, intent); err != nil {
			return nil, err
		}
	}
	if intent.ActionType == "buy" {
		if err := validateDirectBuyIntentTx(ctx, tx, intent); err != nil {
			return nil, err
		}
	}
	if intent.Status == IntentStatusAwaitingReceipt {
		var existingID int64
		err := tx.QueryRow(ctx, `SELECT id FROM payment_intents WHERE user_id = $1 AND status = $2 LIMIT 1`, intent.UserID, IntentStatusAwaitingReceipt).Scan(&existingID)
		if err == nil {
			return nil, ErrActivePaymentIntentExists
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("failed to check active payment intent: %w", err)
		}
	}

	var snapshotBytes []byte
	if intent.ProvisioningSnapshot != nil {
		snapshotBytes, err = json.Marshal(intent.ProvisioningSnapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize provisioning snapshot: %w", err)
		}
	} else {
		snapshotBytes = []byte("{}")
	}

	query := `
		INSERT INTO payment_intents (
			user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
			amount_toman, months, ip_limit, data_gb, display_name, client_email,
			provisioning_snapshot, status, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW(), NOW()
		)
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRow(ctx, query,
		intent.UserID, intent.IntentToken, intent.ActionType, intent.PlanID, intent.SubscriptionID, intent.QuoteID,
		intent.AmountToman, intent.Months, intent.IPLimit, intent.DataGB, intent.DisplayName, intent.ClientEmail,
		snapshotBytes, intent.Status,
	).Scan(&intent.ID, &intent.CreatedAt, &intent.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create payment intent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit payment intent: %w", err)
	}
	return intent, nil
}

func validateDirectBuyIntentTx(ctx context.Context, tx pgx.Tx, intent *PaymentIntent) error {
	if intent.PlanID == nil || intent.QuoteID == nil || *intent.PlanID <= 0 || *intent.QuoteID <= 0 ||
		intent.AmountToman <= 0 || intent.Months < 1 || intent.Months > 120 || intent.DataGB < 0 ||
		strings.TrimSpace(intent.ClientEmail) == "" {
		return ErrSubscriptionMutationInvalid
	}
	var enabled, isLimited bool
	var baseIP, maxIP int
	var minData int64
	var inboundJSON []byte
	var planFlow string
	var planUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT enabled, base_ip_limit, max_ip_limit, is_limited, min_data_gb, inbound_ids, flow, updated_at
		FROM paid_plans WHERE id = $1 FOR SHARE
	`, *intent.PlanID).Scan(&enabled, &baseIP, &maxIP, &isLimited, &minData, &inboundJSON, &planFlow, &planUpdatedAt); err != nil {
		return err
	}
	if !enabled || (maxIP == 0 && intent.IPLimit != 0) || (maxIP > 0 && (intent.IPLimit < baseIP || intent.IPLimit > maxIP)) || (isLimited && int64(intent.DataGB) < minData) {
		return ErrSubscriptionMutationInvalid
	}
	var quoteUserID, quotePlanID *int64
	var quoteMonths, quoteDays, quoteIP, quoteData int
	var quoteAmount int64
	var quoteCreatedAt time.Time
	if err := tx.QueryRow(ctx, `
		SELECT user_id, plan_id, months, duration_days, ip_limit, data_gb, final_price_toman, created_at
		FROM purchase_quotes WHERE id = $1 FOR SHARE
	`, *intent.QuoteID).Scan(&quoteUserID, &quotePlanID, &quoteMonths, &quoteDays, &quoteIP, &quoteData, &quoteAmount, &quoteCreatedAt); err != nil {
		return fmt.Errorf("failed to lock purchase quote: %w", err)
	}
	if quoteUserID == nil || *quoteUserID != intent.UserID || quotePlanID == nil || *quotePlanID != *intent.PlanID ||
		quoteMonths != intent.Months || quoteDays <= 0 || quoteIP != intent.IPLimit || quoteData != intent.DataGB || quoteAmount != intent.AmountToman ||
		planUpdatedAt.After(quoteCreatedAt) {
		return ErrSubscriptionMutationStale
	}
	var expectedUUID, expectedSubID string
	if intent.ProvisioningSnapshot != nil {
		expectedUUID, _ = intent.ProvisioningSnapshot["client_uuid"].(string)
		expectedSubID, _ = intent.ProvisioningSnapshot["sub_id"].(string)
	}
	if strings.TrimSpace(expectedUUID) == "" || strings.TrimSpace(expectedSubID) == "" {
		return ErrSubscriptionMutationInvalid
	}
	var inboundIDs []int
	if err := json.Unmarshal(inboundJSON, &inboundIDs); err != nil {
		return fmt.Errorf("invalid paid-plan inbound configuration: %w", err)
	}
	gotInboundIDs, ok := snapshotIntSlice(intent.ProvisioningSnapshot["inbound_ids"])
	if !ok || !sameIntIDs(gotInboundIDs, inboundIDs) {
		return ErrSubscriptionMutationStale
	}
	expectedExpiry, expiryOK := snapshotInt64Value(intent.ProvisioningSnapshot["expiry_time_milli"])
	expectedTotal, totalOK := snapshotInt64Value(intent.ProvisioningSnapshot["total_bytes"])
	if !expiryOK || !totalOK || expectedExpiry != -int64(quoteDays)*24*60*60*1000 || expectedTotal != int64(intent.DataGB)*1024*1024*1024 {
		return ErrSubscriptionMutationInvalid
	}
	expectedFlow, flowOK := intent.ProvisioningSnapshot["flow"].(string)
	if !flowOK || expectedFlow != planFlow {
		return ErrSubscriptionMutationStale
	}
	return nil
}

func snapshotIntSlice(value any) ([]int, bool) {
	switch values := value.(type) {
	case []int:
		return append([]int(nil), values...), true
	case []any:
		out := make([]int, 0, len(values))
		for _, value := range values {
			n, ok := snapshotInt64Value(value)
			if !ok {
				return nil, false
			}
			out = append(out, int(n))
		}
		return out, true
	default:
		return nil, false
	}
}

func sameIntIDs(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[int]int, len(left))
	for _, id := range left {
		counts[id]++
	}
	for _, id := range right {
		if counts[id] == 0 {
			return false
		}
		counts[id]--
	}
	return true
}

func validateAndSnapshotDirectSubscriptionIntent(ctx context.Context, tx pgx.Tx, intent *PaymentIntent) error {
	if intent.SubscriptionID == nil || *intent.SubscriptionID <= 0 || intent.AmountToman <= 0 || intent.PlanID == nil || *intent.PlanID <= 0 {
		return ErrSubscriptionMutationInvalid
	}
	if (intent.ActionType == "extend" && (intent.Months < 1 || intent.Months > 120)) ||
		(intent.ActionType == "upgrade_ip" && (intent.IPLimit <= 0 || intent.Months < 1 || intent.Months > 120)) {
		return ErrSubscriptionMutationInvalid
	}
	var ownerID int64
	var planID *int64
	var planType, status, email string
	var ipLimit int
	var expireTime *int64
	var active bool
	var desiredIP *int
	var desiredExpiry *int64
	var desiredActive *bool
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT user_id, plan_id, plan_type, status, client_email, ip_limit, expire_time, is_active,
		       desired_ip_limit, desired_expire_time, desired_is_active, updated_at
		FROM subscriptions WHERE id = $1 FOR UPDATE
	`, *intent.SubscriptionID).Scan(&ownerID, &planID, &planType, &status, &email, &ipLimit, &expireTime, &active,
		&desiredIP, &desiredExpiry, &desiredActive, &updatedAt)
	if err != nil {
		return fmt.Errorf("failed to lock direct-payment subscription: %w", err)
	}
	if ownerID != intent.UserID || email != intent.ClientEmail || planType != PlanTypePaid || planID == nil || *planID != *intent.PlanID ||
		desiredIP != nil || desiredExpiry != nil || desiredActive != nil {
		return ErrSubscriptionMutationStale
	}
	if intent.ActionType == "extend" {
		if !IsExtensionEligibleState(status, active, expireTime, time.Now().UTC()) {
			return ErrSubscriptionMutationStale
		}
	} else if status != SubscriptionStatusActive || !active {
		return ErrSubscriptionMutationStale
	}
	excludePurchaseRequestID := int64(0)
	if intent.ID > 0 {
		lookupErr := tx.QueryRow(ctx, `SELECT id FROM purchase_requests WHERE payment_intent_id = $1`, intent.ID).Scan(&excludePurchaseRequestID)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return fmt.Errorf("find receipt reservation for payment intent: %w", lookupErr)
		}
	}
	if err := ensureNoUnresolvedDirectMutationTx(ctx, tx, *intent.SubscriptionID, intent.ID, excludePurchaseRequestID); err != nil {
		return err
	}
	var enabled bool
	var maxIP int
	var planUpdatedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT enabled, max_ip_limit, updated_at FROM paid_plans WHERE id = $1 FOR SHARE`, *planID).
		Scan(&enabled, &maxIP, &planUpdatedAt); err != nil {
		return fmt.Errorf("failed to lock direct-payment plan: %w", err)
	}
	if !enabled || *intent.PlanID != *planID {
		return ErrSubscriptionMutationStale
	}
	expectedSubUpdatedAt, subTimeOK := snapshotTime(intent.ProvisioningSnapshot, "expected_subscription_updated_at")
	expectedPlanUpdatedAt, planTimeOK := snapshotTime(intent.ProvisioningSnapshot, "expected_plan_updated_at")
	expectedIP, ipOK := snapshotInt64Value(intent.ProvisioningSnapshot["expected_ip_limit"])
	expectedExpiry, expiryOK := snapshotInt64Value(intent.ProvisioningSnapshot["expected_expire_time_milli"])
	expectedActive, activeOK := intent.ProvisioningSnapshot["expected_is_active"].(bool)
	currentExpiry := int64(0)
	if expireTime != nil {
		currentExpiry = *expireTime
	}
	if !subTimeOK || !planTimeOK || !ipOK || !expiryOK || !activeOK ||
		!expectedSubUpdatedAt.Equal(updatedAt) || !expectedPlanUpdatedAt.Equal(planUpdatedAt) ||
		expectedIP != int64(ipLimit) || expectedExpiry != currentExpiry || expectedActive != active {
		return ErrSubscriptionMutationStale
	}
	if intent.ActionType == "upgrade_ip" && (intent.IPLimit <= ipLimit || maxIP <= 0 || intent.IPLimit > maxIP) {
		return ErrSubscriptionMutationInvalid
	}
	if intent.ProvisioningSnapshot == nil {
		intent.ProvisioningSnapshot = make(map[string]any)
	}
	intent.ProvisioningSnapshot["expected_user_id"] = ownerID
	intent.ProvisioningSnapshot["expected_plan_id"] = *planID
	intent.ProvisioningSnapshot["expected_plan_updated_at"] = planUpdatedAt.UTC().Format(time.RFC3339Nano)
	intent.ProvisioningSnapshot["expected_subscription_updated_at"] = updatedAt.UTC().Format(time.RFC3339Nano)
	intent.ProvisioningSnapshot["expected_ip_limit"] = ipLimit
	intent.ProvisioningSnapshot["expected_expire_time_milli"] = expireTime
	intent.ProvisioningSnapshot["expected_is_active"] = active
	return nil
}

// ensureNoUnresolvedDirectMutationTx serializes direct commercial mutations
// through the subscription row lock acquired by the caller. Durable payment
// and purchase rows reserve a service across all PostgreSQL-backed processes.
func ensureNoUnresolvedDirectMutationTx(ctx context.Context, tx pgx.Tx, subscriptionID, excludeIntentID, excludePurchaseRequestID int64) error {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM payment_intents pi
			WHERE pi.subscription_id = $1
			  AND pi.action_type IN ('extend', 'upgrade_ip')
			  AND (pi.status = 'awaiting_receipt' OR (pi.status = 'receipt_submitted' AND NOT EXISTS (SELECT 1 FROM purchase_requests linked WHERE linked.payment_intent_id = pi.id)))
			  AND ($2 = 0 OR pi.id <> $2)
			UNION ALL
			SELECT 1 FROM purchase_requests pr
			WHERE pr.subscription_id = $1
			  AND pr.type IN ('extend', 'upgrade_ip')
			  AND pr.status IN ('pending', 'needs_manual_review', 'approved')
			  AND (pr.status <> 'approved' OR pr.provisioning_status <> 'succeeded')
			  AND ($3 = 0 OR pr.id <> $3)
		)
	`, subscriptionID, excludeIntentID, excludePurchaseRequestID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check unresolved direct subscription mutation: %w", err)
	}
	if exists {
		return ErrSubscriptionMutationInProgress
	}
	return nil
}

func snapshotTime(snapshot map[string]any, key string) (time.Time, bool) {
	value, ok := snapshot[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func snapshotInt64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if float64(int64(v)) == v {
			return int64(v), true
		}
	case json.Number:
		parsed, err := v.Int64()
		return parsed, err == nil
	}
	return 0, false
}

type ReceiptSubmissionResult struct {
	PurchaseRequest   *PurchaseRequest
	TopupRequest      *TopupRequest
	IsDuplicate       bool
	NeedsManualReview bool
}

func SubmitReceiptForActiveIntent(ctx context.Context, intentID int64, userID int64, fileID string, reqDetails *PurchaseRequest) (*ReceiptSubmissionResult, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if strings.TrimSpace(fileID) == "" {
		return nil, errors.New("receipt file ID is required")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var intent PaymentIntent
	var snapshotBytes []byte
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at,
		       cancelled_at, cancelled_by_user_id, cancellation_reason
		FROM payment_intents
		WHERE id = $1
		FOR UPDATE
	`, intentID).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
		&intent.CancelledAt, &intent.CancelledByUserID, &intent.CancellationReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentIntentNotFound
		}
		return nil, fmt.Errorf("failed to lock payment intent: %w", err)
	}

	if intent.UserID != userID {
		return nil, errors.New("unauthorized intent access")
	}

	if len(snapshotBytes) > 0 {
		if err := json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot); err != nil {
			return nil, fmt.Errorf("invalid durable provisioning snapshot: %w", err)
		}
	}
	isTopup := intent.ActionType == "topup"
	opKey := fmt.Sprintf("direct_purchase_intent:%s", intent.IntentToken)
	if isTopup {
		opKey = fmt.Sprintf("topup_intent:%s", intent.IntentToken)
	}

	// Idempotent replay if already submitted
	if intent.Status == IntentStatusReceiptSubmitted {
		if isTopup {
			var topup TopupRequest
			err := tx.QueryRow(ctx, `
				SELECT id, user_id, telegram_file_id, status, amount, admin_id, created_at, updated_at, COALESCE(operation_key, '')
				FROM topup_requests
				WHERE operation_key = $1
			`, opKey).Scan(&topup.ID, &topup.UserID, &topup.TelegramFileID, &topup.Status, &topup.Amount, &topup.AdminID, &topup.CreatedAt, &topup.UpdatedAt, &topup.OperationKey)
			if err == nil {
				_ = tx.Commit(ctx)
				return &ReceiptSubmissionResult{TopupRequest: &topup, IsDuplicate: true}, nil
			}
		} else {
			var pr PurchaseRequest
			err := tx.QueryRow(ctx, `
				SELECT id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at, provisioning_snapshot, payment_intent_id, receipt_submitted_at, review_reason
				FROM purchase_requests
				WHERE operation_key = $1
			`, opKey).Scan(&pr.ID, &pr.UserID, &pr.Type, &pr.PlanID, &pr.SubscriptionID, &pr.QuoteID, &pr.PriceToman, &pr.Price, &pr.Months, &pr.IPLimit, &pr.DataGB, &pr.CustomName, &pr.ClientEmail, &pr.TelegramFileID, &pr.Status, &pr.ProvisioningStatus, &pr.OperationKey, &pr.AdminID, &pr.CreatedAt, &pr.UpdatedAt, &pr.ProvisioningSnapshot, &pr.PaymentIntentID, &pr.ReceiptSubmittedAt, &pr.ReviewReason)
			if err == nil {
				_ = tx.Commit(ctx)
				return &ReceiptSubmissionResult{PurchaseRequest: &pr, IsDuplicate: true, NeedsManualReview: pr.Status == PurchaseStatusNeedsManualReview}, nil
			}
		}
	}

	if intent.Status != IntentStatusAwaitingReceipt && intent.Status != IntentStatusReceiptSubmitted {
		return nil, fmt.Errorf("payment intent is not in a valid state for receipt: %s", intent.Status)
	}

	if isTopup {
		topup := &TopupRequest{
			UserID:         userID,
			TelegramFileID: fileID,
			Status:         "pending",
			OperationKey:   &opKey,
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO topup_requests (user_id, telegram_file_id, status, operation_key)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (operation_key) DO UPDATE SET updated_at = NOW()
			RETURNING id, created_at, updated_at
		`, topup.UserID, topup.TelegramFileID, topup.Status, opKey).Scan(&topup.ID, &topup.CreatedAt, &topup.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to create topup request: %w", err)
		}

		_, err = tx.Exec(ctx, `UPDATE payment_intents SET status = $1, updated_at = NOW() WHERE id = $2`, IntentStatusReceiptSubmitted, intent.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to update payment intent status: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &ReceiptSubmissionResult{TopupRequest: topup, IsDuplicate: false}, nil
	}

	// The locked intent is the only source of purchase terms. reqDetails exists
	// solely for UI compatibility and must never override the durable agreement.
	priceToman := intent.AmountToman
	request := &PurchaseRequest{
		UserID:               intent.UserID,
		Type:                 canonicalPurchaseAction(intent.ActionType),
		PlanID:               intent.PlanID,
		SubscriptionID:       intent.SubscriptionID,
		QuoteID:              intent.QuoteID,
		PriceToman:           &priceToman,
		Price:                0, // legacy compatibility column; runtime commerce uses PriceToman only
		Months:               intent.Months,
		IPLimit:              intent.IPLimit,
		DataGB:               intent.DataGB,
		CustomName:           intent.DisplayName,
		ClientEmail:          intent.ClientEmail,
		TelegramFileID:       fileID,
		Status:               "pending",
		ProvisioningStatus:   PurchaseProvisioningPending,
		OperationKey:         opKey,
		PaymentIntentID:      &intent.ID,
		ProvisioningSnapshot: intent.ProvisioningSnapshot,
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO purchase_requests (
			user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, operation_key, payment_intent_id, receipt_submitted_at, review_reason, provisioning_snapshot
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''), $17, NOW(), '', $18::jsonb)
		ON CONFLICT (operation_key) WHERE operation_key IS NOT NULL DO UPDATE SET updated_at = NOW()
		RETURNING id, created_at, updated_at, payment_intent_id, receipt_submitted_at
	`, request.UserID, request.Type, request.PlanID, request.SubscriptionID, request.QuoteID, request.PriceToman, request.Price, request.Months, request.IPLimit, request.DataGB, request.CustomName, request.ClientEmail, request.TelegramFileID, request.Status, request.ProvisioningStatus, request.OperationKey, intent.ID, snapshotBytes).
		Scan(&request.ID, &request.CreatedAt, &request.UpdatedAt, &request.PaymentIntentID, &request.ReceiptSubmittedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create purchase request: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE payment_intents SET status = $1, updated_at = NOW() WHERE id = $2`, IntentStatusReceiptSubmitted, intent.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update payment intent status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	needsReview, reviewReason, validationErr := validateSubmittedPurchaseReceipt(ctx, &intent, request.ID)
	if needsReview {
		request.Status = PurchaseStatusNeedsManualReview
		request.ReviewReason = reviewReason
		if validationErr != nil {
			request.ReviewReason = "receipt_revalidation_unavailable"
		}
		return &ReceiptSubmissionResult{PurchaseRequest: request, IsDuplicate: false, NeedsManualReview: true}, nil
	}
	return &ReceiptSubmissionResult{PurchaseRequest: request, IsDuplicate: false}, nil
}

// Receipt evidence is committed before commercial revalidation. A stale or
// temporarily unavailable plan/service check can block fulfillment, but cannot
// roll back the authenticated user's receipt file ID or quoted intent.
func validateSubmittedPurchaseReceipt(ctx context.Context, intent *PaymentIntent, requestID int64) (bool, string, error) {
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return true, "receipt_revalidation_unavailable", err
	}
	defer tx.Rollback(ctx)
	var requestStatus string
	if err := tx.QueryRow(ctx, "SELECT status FROM purchase_requests WHERE id = $1 FOR UPDATE", requestID).Scan(&requestStatus); err != nil {
		return true, "receipt_revalidation_unavailable", err
	}
	if requestStatus != "pending" {
		return requestStatus == PurchaseStatusNeedsManualReview, "", nil
	}
	var validationErr error
	switch intent.ActionType {
	case "buy":
		validationErr = validateDirectBuyIntentTx(ctx, tx, intent)
	case "extend", "upgrade_ip":
		validationErr = validateAndSnapshotDirectSubscriptionIntent(ctx, tx, intent)
	default:
		return false, "", nil
	}
	if validationErr == nil {
		return false, "", nil
	}
	reason := directPurchaseReviewReason(validationErr)
	if _, err := tx.Exec(ctx, "UPDATE purchase_requests SET status = $1, review_reason = $2, updated_at = NOW() WHERE id = $3 AND status = 'pending'", PurchaseStatusNeedsManualReview, reason, requestID); err != nil {
		return true, "receipt_revalidation_unavailable", err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, "receipt_revalidation_unavailable", err
	}
	return true, reason, validationErr
}

func directPurchaseReviewReason(err error) string {
	switch {
	case errors.Is(err, ErrSubscriptionMutationStale):
		return "commercial_state_changed_after_quote"
	case errors.Is(err, ErrSubscriptionMutationInProgress):
		return "another_subscription_mutation_is_unresolved"
	case errors.Is(err, ErrSubscriptionMutationInvalid):
		return "quoted_terms_are_no_longer_eligible"
	default:
		return "receipt_revalidation_failed"
	}
}

func canonicalPurchaseAction(action string) string {
	if action == "new_subscription" {
		return "buy"
	}
	return action
}

func GetPaymentIntentByID(ctx context.Context, id int64) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at,
		       cancelled_at, cancelled_by_user_id, cancellation_reason
		FROM payment_intents
		WHERE id = $1
	`
	var intent PaymentIntent
	var snapshotBytes []byte
	err := Pool.QueryRow(ctx, query, id).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
		&intent.CancelledAt, &intent.CancelledByUserID, &intent.CancellationReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentIntentNotFound
		}
		return nil, fmt.Errorf("failed to get payment intent by id: %w", err)
	}

	if len(snapshotBytes) > 0 {
		_ = json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot)
	}
	return &intent, nil
}

func GetLatestActivePaymentIntent(ctx context.Context, userID int64) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at,
		       cancelled_at, cancelled_by_user_id, cancellation_reason
		FROM payment_intents
		WHERE user_id = $1 AND status = $2
		ORDER BY created_at DESC
		LIMIT 1
	`
	var intent PaymentIntent
	var snapshotBytes []byte
	err := Pool.QueryRow(ctx, query, userID, IntentStatusAwaitingReceipt).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
		&intent.CancelledAt, &intent.CancelledByUserID, &intent.CancellationReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentIntentNotFound
		}
		return nil, fmt.Errorf("failed to get active payment intent: %w", err)
	}

	if len(snapshotBytes) > 0 {
		_ = json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot)
	}
	return &intent, nil
}

func GetPaymentIntentByToken(ctx context.Context, token string) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at,
		       cancelled_at, cancelled_by_user_id, cancellation_reason
		FROM payment_intents
		WHERE intent_token = $1
	`
	var intent PaymentIntent
	var snapshotBytes []byte
	err := Pool.QueryRow(ctx, query, token).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
		&intent.CancelledAt, &intent.CancelledByUserID, &intent.CancellationReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentIntentNotFound
		}
		return nil, fmt.Errorf("failed to get payment intent by token: %w", err)
	}

	if len(snapshotBytes) > 0 {
		_ = json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot)
	}
	return &intent, nil
}

func MarkPaymentIntentStatus(ctx context.Context, id int64, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `UPDATE payment_intents SET status = $1, updated_at = NOW() WHERE id = $2`
	tag, err := Pool.Exec(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("failed to update payment intent status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPaymentIntentNotFound
	}
	return nil
}

func CancelPaymentIntent(ctx context.Context, intentID, userID int64, reason string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.New("payment-intent cancellation reason is required")
	}
	tag, err := Pool.Exec(ctx, `
		UPDATE payment_intents
		SET status = $1, cancelled_at = NOW(), cancelled_by_user_id = $2,
		    cancellation_reason = $3, updated_at = NOW()
		WHERE id = $4 AND user_id = $2 AND status = $5
	`, IntentStatusCancelled, userID, reason, intentID, IntentStatusAwaitingReceipt)
	if err != nil {
		return fmt.Errorf("cancel payment intent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM payment_intents WHERE id = $1 AND user_id = $2)`, intentID, userID).Scan(&exists); err != nil {
			return fmt.Errorf("check payment intent before cancellation: %w", err)
		}
		if !exists {
			return ErrPaymentIntentNotFound
		}
		return ErrPaymentIntentNotCancellable
	}
	return nil
}

func GetPaymentIntentByClientEmail(ctx context.Context, email string) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at,
		       cancelled_at, cancelled_by_user_id, cancellation_reason
		FROM payment_intents
		WHERE client_email = $1
		ORDER BY created_at DESC
		LIMIT 1
	`
	var intent PaymentIntent
	var snapshotBytes []byte
	err := Pool.QueryRow(ctx, query, email).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
		&intent.CancelledAt, &intent.CancelledByUserID, &intent.CancellationReason,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrPaymentIntentNotFound
		}
		return nil, fmt.Errorf("failed to get payment intent by client email: %w", err)
	}

	if len(snapshotBytes) > 0 {
		_ = json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot)
	}
	return &intent, nil
}
