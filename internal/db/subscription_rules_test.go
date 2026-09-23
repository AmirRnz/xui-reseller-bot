package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCreateSubscriptionPreservesUnlimitedIPLimit(t *testing.T) {
	ctx := setupTestDB(t)
	tgID := time.Now().UnixNano()
	var userID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("unlimited_%d", tgID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM subscriptions WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()

	sub := &Subscription{UserID: userID, ClientEmail: fmt.Sprintf("unlimited_%d@example.test", tgID), PlanType: PlanTypePaid, IPLimit: 0, IsActive: true, Status: SubscriptionStatusActive}
	if err := CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("create unlimited subscription: %v", err)
	}
	loaded, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil || loaded == nil {
		t.Fatalf("load unlimited subscription: sub=%+v err=%v", loaded, err)
	}
	if loaded.IPLimit != 0 {
		t.Fatalf("unlimited limitIp was changed to %d", loaded.IPLimit)
	}
}

func TestSubscriptionLifecycleConstraintRejectsContradictoryState(t *testing.T) {
	ctx := setupTestDB(t)
	tgID := time.Now().UnixNano()
	var userID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("lifecycle_%d", tgID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM subscriptions WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()
	var subID int
	if err := Pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, client_email, plan_type, status, is_active) VALUES ($1, $2, 'paid', 'active', TRUE) RETURNING id`, userID, fmt.Sprintf("lifecycle_%d@example.test", tgID)).Scan(&subID); err != nil {
		t.Fatalf("create active subscription: %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE subscriptions SET status = 'active', is_active = FALSE WHERE id = $1`, subID); err == nil {
		t.Fatal("database accepted status='active' with is_active=false")
	}
	if err := UpdateSubscriptionStatus(ctx, subID, SubscriptionStatusReconciliation); err != nil {
		t.Fatalf("move subscription into reconciliation: %v", err)
	}
	var status string
	var active bool
	if err := Pool.QueryRow(ctx, `SELECT status, is_active FROM subscriptions WHERE id = $1`, subID).Scan(&status, &active); err != nil {
		t.Fatalf("read reconciliation state: %v", err)
	}
	if status != SubscriptionStatusReconciliation || !active {
		t.Fatalf("reconciliation transition lost remote active state: status=%q active=%t", status, active)
	}
}

func TestDirectPaymentIntentRevalidatesIPUpgradeAgainstLockedState(t *testing.T) {
	ctx := setupTestDB(t)
	tgID := time.Now().UnixNano()
	var userID, planID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("direct_rules_%d", tgID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := Pool.QueryRow(ctx, `INSERT INTO paid_plans (name, enabled, max_ip_limit) VALUES ($1, TRUE, 4) RETURNING id`, fmt.Sprintf("direct_rules_%d", tgID)).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	var subID int
	expiry := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	var subUpdatedAt, planUpdatedAt time.Time
	if err := Pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, plan_id, plan_type, client_email, status, is_active, ip_limit, expire_time) VALUES ($1, $2, 'paid', $3, 'active', TRUE, 2, $4) RETURNING id, updated_at`, userID, planID, fmt.Sprintf("direct_rules_%d@example.test", tgID), expiry).Scan(&subID, &subUpdatedAt); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT updated_at FROM paid_plans WHERE id = $1`, planID).Scan(&planUpdatedAt); err != nil {
		t.Fatalf("read plan timestamp: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()

	makeIntent := func(token string, target int, updatedAt time.Time) *PaymentIntent {
		subID64 := int64(subID)
		return &PaymentIntent{UserID: userID, IntentToken: token, ActionType: "upgrade_ip", SubscriptionID: &subID64,
			AmountToman: 1000, Months: 1, IPLimit: target, ClientEmail: fmt.Sprintf("direct_rules_%d@example.test", tgID),
			ProvisioningSnapshot: map[string]any{
				"expected_ip_limit": 2, "expected_expire_time_milli": expiry, "expected_is_active": true,
				"expected_subscription_updated_at": updatedAt.UTC().Format(time.RFC3339Nano),
				"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
			}, Status: IntentStatusAwaitingReceipt}
	}
	valid, err := CreatePaymentIntent(ctx, makeIntent(fmt.Sprintf("direct_valid_%d", tgID), 4, subUpdatedAt))
	if err != nil {
		t.Fatalf("create valid direct IP upgrade: %v", err)
	}
	if valid.ProvisioningSnapshot["expected_plan_id"] == nil {
		t.Fatalf("locked plan identity was not saved: %#v", valid.ProvisioningSnapshot)
	}
	if _, err := CreatePaymentIntent(ctx, makeIntent(fmt.Sprintf("direct_over_max_%d", tgID), 5, subUpdatedAt)); !errors.Is(err, ErrSubscriptionMutationInvalid) {
		t.Fatalf("over-limit callback should fail business validation, got %v", err)
	}
	if _, err := CreatePaymentIntent(ctx, makeIntent(fmt.Sprintf("direct_stale_%d", tgID), 3, subUpdatedAt.Add(-time.Second))); !errors.Is(err, ErrSubscriptionMutationStale) {
		t.Fatalf("stale callback should fail compare-and-set validation, got %v", err)
	}
}

func TestDirectBuyIntentRejectsChangedPriceAndDisabledPlan(t *testing.T) {
	ctx := setupTestDB(t)
	tgID := time.Now().UnixNano()
	var userID, planID, quoteID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("direct_buy_rules_%d", tgID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := Pool.QueryRow(ctx, `INSERT INTO paid_plans (name, enabled, base_ip_limit, max_ip_limit, inbound_ids, flow) VALUES ($1, TRUE, 1, 5, '[1]'::jsonb, 'test-flow') RETURNING id`, fmt.Sprintf("direct_buy_rules_%d", tgID)).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := Pool.QueryRow(ctx, `INSERT INTO purchase_quotes (quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb, base_price_toman, final_price_toman) VALUES ($1, $2, $3, 'Direct buy test', 1, 30, 2, 0, 1000, 1000) RETURNING id`, fmt.Sprintf("direct_buy_quote_%d", tgID), userID, planID).Scan(&quoteID); err != nil {
		t.Fatalf("create quote: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM purchase_quotes WHERE id = $1`, quoteID)
		_, _ = Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()
	makeIntent := func(token string, amount int64) *PaymentIntent {
		return &PaymentIntent{UserID: userID, IntentToken: token, ActionType: "buy", PlanID: &planID, QuoteID: &quoteID,
			AmountToman: amount, Months: 1, IPLimit: 2, DataGB: 0, ClientEmail: fmt.Sprintf("direct_buy_%d@example.test", tgID),
			ProvisioningSnapshot: map[string]any{"client_uuid": "direct-buy-uuid", "sub_id": "direct-buy-sub", "inbound_ids": []int{1},
				"expiry_time_milli": int64(-30 * 24 * 60 * 60 * 1000), "total_bytes": int64(0), "flow": "test-flow"}}
	}
	if _, err := CreatePaymentIntent(ctx, makeIntent(fmt.Sprintf("direct_buy_bad_price_%d", tgID), 1001)); !errors.Is(err, ErrSubscriptionMutationStale) {
		t.Fatalf("changed quote amount should be rejected as stale, got %v", err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE paid_plans SET enabled = FALSE, updated_at = NOW() WHERE id = $1`, planID); err != nil {
		t.Fatalf("disable plan: %v", err)
	}
	if _, err := CreatePaymentIntent(ctx, makeIntent(fmt.Sprintf("direct_buy_disabled_%d", tgID), 1000)); !errors.Is(err, ErrSubscriptionMutationInvalid) {
		t.Fatalf("disabled plan should be rejected at intent creation, got %v", err)
	}
}
