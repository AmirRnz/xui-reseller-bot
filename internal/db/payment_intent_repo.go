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

	snapshotBytes, err := json.Marshal(intent.ProvisioningSnapshot)
	if err != nil {
		snapshotBytes = []byte("{}")
	}

	if intent.Status == "" {
		intent.Status = IntentStatusAwaitingReceipt
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
	err = Pool.QueryRow(ctx, query,
		intent.UserID, intent.IntentToken, intent.ActionType, intent.PlanID, intent.SubscriptionID, intent.QuoteID,
		intent.AmountToman, intent.Months, intent.IPLimit, intent.DataGB, intent.DisplayName, intent.ClientEmail,
		snapshotBytes, intent.Status,
	).Scan(&intent.ID, &intent.CreatedAt, &intent.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("failed to create payment intent: %w", err)
	}
	return intent, nil
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
