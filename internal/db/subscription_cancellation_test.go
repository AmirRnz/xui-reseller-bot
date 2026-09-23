package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCancellationAndManualRefundIntentAreDurableBeforeDeprovisioning(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	opKey := fmt.Sprintf("test_cancel_manual:%d", telegramID)
	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 1000) RETURNING id
	`, telegramID, fmt.Sprintf("cancel_user_%d", telegramID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var planID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO paid_plans (name, base_price_toman, base_ip_limit, max_ip_limit)
		VALUES ($1, 1000, 1, 5) RETURNING id
	`, fmt.Sprintf("cancel_plan_%d", telegramID)).Scan(&planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	var subID int
	var updatedAt time.Time
	if err := Pool.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, plan_id, plan_type, client_email, status, is_active, end_date)
		VALUES ($1, $2, 'paid', $3, 'active', TRUE, NOW() + INTERVAL '10 days')
		RETURNING id, updated_at
	`, userID, planID, fmt.Sprintf("cancel-%d@example.test", telegramID)).Scan(&subID, &updatedAt); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	t.Cleanup(func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM reconciliation_records WHERE subscription_id = $1`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM refund_requests WHERE subscription_id = $1`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	})

	refundKey := opKey + ":manual-refund"
	req, err := RequestSubscriptionCancellation(ctx, subID, userID, updatedAt, 0, refundKey, "legacy purchase needs human review")
	if err != nil {
		t.Fatalf("persist cancellation request: %v", err)
	}
	if req == nil || req.ID == 0 || req.CalculatedAmount != 0 || req.Status != "pending" {
		t.Fatalf("manual refund review intent is not durable: %+v", req)
	}

	var status string
	var active bool
	if err := Pool.QueryRow(ctx, `SELECT status, is_active FROM subscriptions WHERE id = $1`, subID).Scan(&status, &active); err != nil {
		t.Fatalf("read requested cancellation: %v", err)
	}
	if status != SubscriptionStatusCancelRequested || !active {
		t.Fatalf("cancellation must remain pending remote deprovision: status=%q active=%t", status, active)
	}
	var workCount int
	if err := Pool.QueryRow(ctx, `
		SELECT count(*) FROM reconciliation_records
		WHERE operation_key = $1 AND kind = 'subscription_cancellation_requested' AND status = 'pending'
	`, fmt.Sprintf("subscription_cancel_deprovision:%d", subID)).Scan(&workCount); err != nil {
		t.Fatalf("read cancellation work: %v", err)
	}
	if workCount != 1 {
		t.Fatalf("expected one durable deprovisioning work item, got %d", workCount)
	}

	if _, err := ApproveRefundRequestAndCredit(ctx, req.ID, 999, 500, "early approval"); !errors.Is(err, ErrSubscriptionCancellationNotComplete) {
		t.Fatalf("refund before remote deletion = %v, want cancellation-not-complete", err)
	}
	var balance int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balance); err != nil {
		t.Fatalf("read wallet after blocked approval: %v", err)
	}
	if balance != 1000 {
		t.Fatalf("blocked refund approval changed wallet balance to %d", balance)
	}

	if err := MarkSubscriptionDeprovisioning(ctx, subID); err != nil {
		t.Fatalf("mark deprovisioning: %v", err)
	}
	if err := CompleteSubscriptionCancellation(ctx, subID, userID); err != nil {
		t.Fatalf("complete cancellation after worker verification: %v", err)
	}
	approved, err := ApproveRefundRequestAndCredit(ctx, req.ID, 999, 500, "legacy purchase reviewed")
	if err != nil || approved == nil || approved.Status != "approved" {
		t.Fatalf("approve refund after cancellation: request=%+v err=%v", approved, err)
	}
}
