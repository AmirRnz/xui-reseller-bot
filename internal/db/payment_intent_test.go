package db

import (
	"fmt"
	"testing"
	"time"
)

func TestPaymentIntentLifecycle(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	username := fmt.Sprintf("intent_user_%d", telegramID)
	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 0)
		RETURNING id
	`, telegramID, username).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, "DELETE FROM payment_intents WHERE user_id = $1", userID)
		_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE id = $1", userID)
	}()

	token := fmt.Sprintf("test_token_%d", telegramID)
	intent := &PaymentIntent{
		UserID:      userID,
		IntentToken: token,
		ActionType:  "new_subscription",
		AmountToman: 150000,
		Months:      1,
		IPLimit:     2,
		DataGB:      50,
		DisplayName: "Test Intent Sub",
		ClientEmail: "intent@test.com",
		ProvisioningSnapshot: map[string]any{
			"key1": "val1",
			"key2": float64(42),
		},
		Status: IntentStatusAwaitingReceipt,
	}

	created, err := CreatePaymentIntent(ctx, intent)
	if err != nil {
		t.Fatalf("CreatePaymentIntent failed: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("expected positive intent ID, got %d", created.ID)
	}

	// 1. Get by token
	byToken, err := GetPaymentIntentByToken(ctx, token)
	if err != nil {
		t.Fatalf("GetPaymentIntentByToken failed: %v", err)
	}
	if byToken.ID != created.ID || byToken.AmountToman != 150000 || byToken.ClientEmail != "intent@test.com" {
		t.Fatalf("intent data mismatch: %+v", byToken)
	}
	if byToken.ProvisioningSnapshot["key1"] != "val1" {
		t.Fatalf("snapshot mismatch: %+v", byToken.ProvisioningSnapshot)
	}

	// 2. Get latest active
	latest, err := GetLatestActivePaymentIntent(ctx, userID)
	if err != nil {
		t.Fatalf("GetLatestActivePaymentIntent failed: %v", err)
	}
	if latest.ID != created.ID {
		t.Fatalf("expected latest intent %d, got %d", created.ID, latest.ID)
	}

	// 3. Mark status submitted
	if err := MarkPaymentIntentStatus(ctx, created.ID, IntentStatusReceiptSubmitted); err != nil {
		t.Fatalf("MarkPaymentIntentStatus failed: %v", err)
	}

	// 4. Now active query should not return it
	_, err = GetLatestActivePaymentIntent(ctx, userID)
	if err == nil {
		t.Fatalf("expected ErrPaymentIntentNotFound after status change, got nil")
	}

	// 5. Get by client email
	byEmail, err := GetPaymentIntentByClientEmail(ctx, "intent@test.com")
	if err != nil {
		t.Fatalf("GetPaymentIntentByClientEmail failed: %v", err)
	}
	if byEmail.ID != created.ID || byEmail.ClientEmail != "intent@test.com" {
		t.Fatalf("GetPaymentIntentByClientEmail mismatch: %+v", byEmail)
	}
}
