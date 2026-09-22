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

func CreatePaymentIntent(ctx context.Context, intent *PaymentIntent) (*PaymentIntent, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

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

	// P1-9: Cancel any prior active intent for this user to prevent ambiguity
	if intent.Status == IntentStatusAwaitingReceipt {
		_, _ = Pool.Exec(ctx, `
			UPDATE payment_intents
			SET status = $1, updated_at = NOW()
			WHERE user_id = $2 AND status = $3
		`, IntentStatusCancelled, intent.UserID, IntentStatusAwaitingReceipt)
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
	err := Pool.QueryRow(ctx, query,
		intent.UserID, intent.IntentToken, intent.ActionType, intent.PlanID, intent.SubscriptionID, intent.QuoteID,
		intent.AmountToman, intent.Months, intent.IPLimit, intent.DataGB, intent.DisplayName, intent.ClientEmail,
		snapshotBytes, intent.Status,
	).Scan(&intent.ID, &intent.CreatedAt, &intent.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create payment intent: %w", err)
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
		_ = json.Unmarshal(snapshotBytes, &intent.ProvisioningSnapshot)
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
				SELECT id, user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, COALESCE(operation_key, ''), admin_id, created_at, updated_at
				FROM purchase_requests
				WHERE operation_key = $1
			`, opKey).Scan(&pr.ID, &pr.UserID, &pr.Type, &pr.PlanID, &pr.SubscriptionID, &pr.QuoteID, &pr.PriceToman, &pr.Price, &pr.Months, &pr.IPLimit, &pr.DataGB, &pr.CustomName, &pr.ClientEmail, &pr.TelegramFileID, &pr.Status, &pr.ProvisioningStatus, &pr.OperationKey, &pr.AdminID, &pr.CreatedAt, &pr.UpdatedAt)
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

	// Purchase flow
	if reqDetails == nil {
		reqDetails = &PurchaseRequest{}
	}
	reqDetails.UserID = userID
	reqDetails.TelegramFileID = fileID
	reqDetails.OperationKey = opKey
	if reqDetails.Status == "" {
		reqDetails.Status = "pending"
	}
	if reqDetails.ProvisioningStatus == "" {
		reqDetails.ProvisioningStatus = PurchaseProvisioningPending
	}
	if intent.PlanID != nil {
		reqDetails.PlanID = intent.PlanID
	}
	if intent.SubscriptionID != nil {
		reqDetails.SubscriptionID = intent.SubscriptionID
	}
	if intent.QuoteID != nil {
		reqDetails.QuoteID = intent.QuoteID
	}
	if reqDetails.PriceToman == nil || *reqDetails.PriceToman == 0 {
		reqDetails.PriceToman = &intent.AmountToman
	}
	if reqDetails.Price == 0 {
		reqDetails.Price = float64(intent.AmountToman)
	}
	if reqDetails.Months == 0 {
		reqDetails.Months = intent.Months
	}
	if reqDetails.IPLimit == 0 {
		reqDetails.IPLimit = intent.IPLimit
	}
	if reqDetails.DataGB == 0 {
		reqDetails.DataGB = intent.DataGB
	}
	if reqDetails.CustomName == "" {
		reqDetails.CustomName = intent.DisplayName
	}
	if reqDetails.ClientEmail == "" {
		reqDetails.ClientEmail = intent.ClientEmail
	}
	if reqDetails.Type == "" {
		reqDetails.Type = intent.ActionType
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO purchase_requests (
			user_id, type, plan_id, subscription_id, quote_id, price_toman, price, months, ip_limit, data_gb, custom_name, client_email, telegram_file_id, status, provisioning_status, operation_key
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''))
		ON CONFLICT (operation_key) DO UPDATE SET updated_at = NOW()
		RETURNING id, created_at, updated_at
	`, reqDetails.UserID, reqDetails.Type, reqDetails.PlanID, reqDetails.SubscriptionID, reqDetails.QuoteID, reqDetails.PriceToman, reqDetails.Price, reqDetails.Months, reqDetails.IPLimit, reqDetails.DataGB, reqDetails.CustomName, reqDetails.ClientEmail, reqDetails.TelegramFileID, reqDetails.Status, reqDetails.ProvisioningStatus, reqDetails.OperationKey).
		Scan(&reqDetails.ID, &reqDetails.CreatedAt, &reqDetails.UpdatedAt)
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
	return &ReceiptSubmissionResult{PurchaseRequest: reqDetails, IsDuplicate: false}, nil
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
