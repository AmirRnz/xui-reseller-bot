package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func createWalletMutationFixture(t *testing.T) (context.Context, int64, int, time.Time, *int64) {
	t.Helper()
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	username := fmt.Sprintf("wallet_mutation_%d", telegramID)
	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 100000)
		RETURNING id
	`, telegramID, username).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	var planID int64
	var planUpdated time.Time
	if err := Pool.QueryRow(ctx, `
		INSERT INTO paid_plans (name, base_price_toman, base_ip_limit, max_ip_limit, price_per_extra_ip_toman, enabled)
		VALUES ($1, 1000, 1, 5, 200, TRUE)
		RETURNING id, updated_at
	`, fmt.Sprintf("wallet_mutation_plan_%d", telegramID)).Scan(&planID, &planUpdated); err != nil {
		t.Fatalf("create test plan: %v", err)
	}
	expiry := time.Now().Add(24 * time.Hour).UnixMilli()
	var subID int
	if err := Pool.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, plan_id, plan_type, client_email, ip_limit, expire_time, is_active, status, end_date)
		VALUES ($1, $2, 'paid', $3, 1, $4, TRUE, 'active', NOW() + INTERVAL '24 hours')
		RETURNING id
	`, userID, planID, fmt.Sprintf("wallet-mutation-%d@example.test", telegramID), expiry).Scan(&subID); err != nil {
		t.Fatalf("create test subscription: %v", err)
	}
	t.Cleanup(func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM reconciliation_records WHERE subscription_id = $1 AND operation_key LIKE 'wallet-mutation:%'`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, subID)
		_, _ = Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	})
	return ctx, userID, subID, planUpdated, &expiry
}

func walletMutationIntent(userID int64, subID int, planUpdated time.Time, expiry *int64, operationKey string) WalletSubscriptionMutationIntent {
	desiredIP := 2
	desiredActive := true
	return WalletSubscriptionMutationIntent{
		UserID: userID, SubscriptionID: subID, Amount: 500, Description: "IP limit increase",
		OperationKey: operationKey, ExpectedIPLimit: 1, ExpectedExpireTime: expiry,
		ExpectedIsActive: true, ExpectedPlanUpdatedAt: planUpdated,
		DesiredIPLimit: &desiredIP, DesiredIsActive: &desiredActive,
	}
}

func TestWalletSubscriptionMutationPersistsRecoveryBeforeRemoteWork(t *testing.T) {
	ctx, userID, subID, planUpdated, expiry := createWalletMutationFixture(t)
	opKey := fmt.Sprintf("wallet-mutation:%d:single", time.Now().UnixNano())
	intent := walletMutationIntent(userID, subID, planUpdated, expiry, opKey)
	if err := DebitWalletForSubscriptionMutation(ctx, intent); err != nil {
		t.Fatalf("commit wallet mutation: %v", err)
	}

	var balance int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balance); err != nil {
		t.Fatalf("read wallet: %v", err)
	}
	if balance != 99500 {
		t.Fatalf("wallet balance = %d, want 99500", balance)
	}
	var status string
	var actualLimit int
	var desiredLimit *int
	if err := Pool.QueryRow(ctx, `SELECT status, ip_limit, desired_ip_limit FROM subscriptions WHERE id = $1`, subID).Scan(&status, &actualLimit, &desiredLimit); err != nil {
		t.Fatalf("read subscription intent: %v", err)
	}
	if status != SubscriptionStatusReconciliation || actualLimit != 1 || desiredLimit == nil || *desiredLimit != 2 {
		t.Fatalf("subscription intent not durable: status=%q actual=%d desired=%v", status, actualLimit, desiredLimit)
	}
	var txCount, workCount int
	if err := Pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE operation_key = $1 AND user_id = $2 AND amount = -500 AND type = 'debit' AND status = 'completed' AND reference_type = 'subscription_mutation' AND reference_id = $3`, opKey, userID, subID).Scan(&txCount); err != nil {
		t.Fatalf("read debit ledger: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_records WHERE operation_key = $1 AND status = 'pending'`, opKey+":subscription-mutation").Scan(&workCount); err != nil {
		t.Fatalf("read work item: %v", err)
	}
	if txCount != 1 || workCount != 1 {
		t.Fatalf("durable intent must include one debit and one work item: debit=%d work=%d", txCount, workCount)
	}

	if err := DebitWalletForSubscriptionMutation(ctx, intent); !errors.Is(err, ErrWalletOperationAlreadyApplied) {
		t.Fatalf("same callback replay = %v, want exact idempotent duplicate", err)
	}
	if err := DebitWalletForSubscriptionMutation(ctx, walletMutationIntent(userID, subID, planUpdated, expiry, opKey+":different")); !errors.Is(err, ErrSubscriptionMutationInProgress) {
		t.Fatalf("second concurrent commercial operation = %v, want in-progress rejection", err)
	}
}

func TestConcurrentWalletSubscriptionMutationsChargeOnlyOneProcess(t *testing.T) {
	ctx, userID, subID, planUpdated, expiry := createWalletMutationFixture(t)
	base := fmt.Sprintf("wallet-mutation:%d", time.Now().UnixNano())
	intents := []WalletSubscriptionMutationIntent{
		walletMutationIntent(userID, subID, planUpdated, expiry, base+":a"),
		walletMutationIntent(userID, subID, planUpdated, expiry, base+":b"),
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(intents))
	for _, intent := range intents {
		wg.Add(1)
		go func(intent WalletSubscriptionMutationIntent) {
			defer wg.Done()
			errs <- DebitWalletForSubscriptionMutation(context.Background(), intent)
		}(intent)
	}
	wg.Wait()
	close(errs)
	applied, rejected := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, ErrSubscriptionMutationInProgress), errors.Is(err, ErrSubscriptionMutationStale):
			rejected++
		default:
			t.Fatalf("unexpected concurrent mutation result: %v", err)
		}
	}
	if applied != 1 || rejected != 1 {
		t.Fatalf("wanted one durable mutation and one rejected stale mutation, got applied=%d rejected=%d", applied, rejected)
	}
	var debitCount int
	if err := Pool.QueryRow(ctx, `SELECT count(*) FROM transactions WHERE user_id = $1 AND operation_key LIKE $2 AND type = 'debit'`, userID, base+":%").Scan(&debitCount); err != nil {
		t.Fatalf("count operation debits: %v", err)
	}
	var balance int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balance); err != nil {
		t.Fatalf("read wallet: %v", err)
	}
	if debitCount != 1 || balance != 99500 {
		t.Fatalf("concurrent service operations must debit once: count=%d balance=%d", debitCount, balance)
	}
}
