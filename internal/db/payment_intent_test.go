package db

import (
	"errors"
	"fmt"
	"sync"
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

func TestSubmitReceiptUsesLockedPaymentIntentCommercialTerms(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	var userID, planID, quoteID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, telegramID, fmt.Sprintf("durable_intent_%d", telegramID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := Pool.QueryRow(ctx, `INSERT INTO paid_plans (name) VALUES ($1) RETURNING id`, fmt.Sprintf("durable_plan_%d", telegramID)).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	quoteKey := fmt.Sprintf("durable_quote_%d", telegramID)
	if err := Pool.QueryRow(ctx, `
		INSERT INTO purchase_quotes (quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb, base_price_toman, final_price_toman)
		VALUES ($1, $2, $3, 'Durable plan', 3, 90, 4, 25, 123456, 123456) RETURNING id
	`, quoteKey, userID, planID).Scan(&quoteID); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM purchase_quotes WHERE id = $1`, quoteID)
		_, _ = Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()

	intent, err := CreatePaymentIntent(ctx, &PaymentIntent{
		UserID: userID, IntentToken: fmt.Sprintf("durable_token_%d", telegramID), ActionType: "new_subscription",
		PlanID: &planID, QuoteID: &quoteID, AmountToman: 123456, Months: 3, IPLimit: 4, DataGB: 25,
		DisplayName: "Durable display", ClientEmail: fmt.Sprintf("durable_%d@example.com", telegramID),
		ProvisioningSnapshot: map[string]any{
			"client_uuid": "uuid-from-intent", "sub_id": "sub-from-intent", "inbound_ids": []int{4, 9},
			"expiry_time_milli": int64(-7776000000), "total_bytes": int64(25) * 1073741824, "flow": "durable-flow",
			"group": "durable-group", "telegram_id": telegramID, "plan_name": "Durable plan",
		},
	})
	if err != nil {
		t.Fatalf("create durable intent: %v", err)
	}
	if intent.ActionType != "buy" {
		t.Fatalf("new-subscription intent was not stored with canonical buy action: %q", intent.ActionType)
	}
	// A fresh process has no FSM. The conflicting values stand in for stale
	// transient state and must not alter the agreement persisted in the intent.
	wrongAmount := int64(999)
	conflicting := &PurchaseRequest{Type: "extend", PlanID: nil, QuoteID: nil, PriceToman: &wrongAmount, Months: 99, IPLimit: 99, DataGB: 99, CustomName: "stale", ClientEmail: "stale@example.com"}
	result, err := SubmitReceiptForActiveIntent(ctx, intent.ID, userID, "telegram-file-id", conflicting)
	if err != nil {
		t.Fatalf("submit receipt after restart: %v", err)
	}
	request := result.PurchaseRequest
	if request.Type != "buy" || request.PlanID == nil || *request.PlanID != planID || request.QuoteID == nil || *request.QuoteID != quoteID || request.PriceToman == nil || *request.PriceToman != 123456 || request.Months != 3 || request.IPLimit != 4 || request.DataGB != 25 || request.CustomName != "Durable display" || request.ClientEmail != fmt.Sprintf("durable_%d@example.com", telegramID) || request.TelegramFileID != "telegram-file-id" {
		t.Fatalf("receipt did not use durable payment-intent fields: %+v", request)
	}
	if request.ProvisioningSnapshot["client_uuid"] != "uuid-from-intent" || request.ProvisioningSnapshot["sub_id"] != "sub-from-intent" {
		t.Fatalf("durable provisioning identity was lost: %#v", request.ProvisioningSnapshot)
	}
}

func TestConcurrentActivePaymentIntentCreationHasSingleWinner(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	var userID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, telegramID, fmt.Sprintf("intent_race_%d", telegramID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := CreatePaymentIntent(ctx, &PaymentIntent{
				UserID: userID, IntentToken: fmt.Sprintf("intent_race_%d_%d", telegramID, i), ActionType: "buy", AmountToman: 1000,
			})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, rejected := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrActivePaymentIntentExists) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent creation error: %v", err)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("expected one active intent and one rejected attempt, successes=%d rejected=%d", successes, rejected)
	}
	var active int
	if err := Pool.QueryRow(ctx, `SELECT count(*) FROM payment_intents WHERE user_id = $1 AND status = 'awaiting_receipt'`, userID).Scan(&active); err != nil {
		t.Fatalf("count active intents: %v", err)
	}
	if active != 1 {
		t.Fatalf("partial unique invariant violated: active intents=%d", active)
	}
}
