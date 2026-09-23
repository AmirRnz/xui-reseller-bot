package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

var ErrPaymentIntentNotFound = errors.New("payment intent not found")
var ErrActivePaymentIntentExists = errors.New("an active payment intent already exists for this user")

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

	var snapshotBytes []byte
	if intent.ProvisioningSnapshot != nil {
		var err error
		snapshotBytes, err = json.Marshal(intent.ProvisioningSnapshot)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize provisioning snapshot: %w", err)
		}
	} else {
		snapshotBytes = []byte("{}")
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

type ReceiptSubmissionResult struct {
	PurchaseRequest *PurchaseRequest
	TopupRequest    *TopupRequest
	IsDuplicate     bool
}

func SubmitReceiptForActiveIntent(ctx context.Context, intentID int64, userID int64, fileID string, reqDetails *PurchaseRequest) (*ReceiptSubmissionResult, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

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
		       provisioning_snapshot, status, created_at, updated_at
		FROM payment_intents
		WHERE id = $1
		FOR UPDATE
	`, intentID).Scan(
		&intent.ID, &intent.UserID, &intent.IntentToken, &intent.ActionType, &intent.PlanID,
		&intent.SubscriptionID, &intent.QuoteID, &intent.AmountToman, &intent.Months,
		&intent.IPLimit, &intent.DataGB, &intent.DisplayName, &intent.ClientEmail,
		&snapshotBytes, &intent.Status, &intent.CreatedAt, &intent.UpdatedAt,
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
				SELECT id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at, provisioning_snapshot
				FROM purchase_requests
				WHERE operation_key = $1
			`, opKey).Scan(&pr.ID, &pr.UserID, &pr.Type, &pr.PlanID, &pr.SubscriptionID, &pr.QuoteID, &pr.PriceToman, &pr.Price, &pr.Months, &pr.IPLimit, &pr.DataGB, &pr.CustomName, &pr.ClientEmail, &pr.TelegramFileID, &pr.Status, &pr.ProvisioningStatus, &pr.OperationKey, &pr.AdminID, &pr.CreatedAt, &pr.UpdatedAt, &pr.ProvisioningSnapshot)
			if err == nil {
				_ = tx.Commit(ctx)
				return &ReceiptSubmissionResult{PurchaseRequest: &pr, IsDuplicate: true}, nil
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
		ProvisioningSnapshot: intent.ProvisioningSnapshot,
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO purchase_requests (
			user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, operation_key, provisioning_snapshot
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''), $17::jsonb)
		ON CONFLICT (operation_key) WHERE operation_key IS NOT NULL DO UPDATE SET updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, request.UserID, request.Type, request.PlanID, request.SubscriptionID, request.QuoteID, request.PriceToman, request.Price, request.Months, request.IPLimit, request.DataGB, request.CustomName, request.ClientEmail, request.TelegramFileID, request.Status, request.ProvisioningStatus, request.OperationKey, snapshotBytes).
		Scan(&request.ID, &request.CreatedAt, &request.UpdatedAt)
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
	return &ReceiptSubmissionResult{PurchaseRequest: request, IsDuplicate: false}, nil
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
		       provisioning_snapshot, status, created_at, updated_at
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
		       provisioning_snapshot, status, created_at, updated_at
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
		       provisioning_snapshot, status, created_at, updated_at
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

func GetPaymentIntentByClientEmail(ctx context.Context, email string) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT id, user_id, intent_token, action_type, plan_id, subscription_id, quote_id,
		       amount_toman, months, ip_limit, data_gb, display_name, client_email,
		       provisioning_snapshot, status, created_at, updated_at
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
