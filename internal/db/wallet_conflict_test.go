package db

import (
	"fmt"
	"testing"
	"time"
)

func TestCreditWalletOperationKeyRejectsDifferentEconomicEffect(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 0) RETURNING id
	`, telegramID, fmt.Sprintf("wallet_conflict_%d", telegramID)).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, userID)
		_, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()
	opKey := fmt.Sprintf("wallet_credit_conflict:%d", telegramID)
	if err := CreditWalletBalanceWithKey(ctx, userID, 1000, "subscription refund", opKey); err != nil {
		t.Fatalf("first credit: %v", err)
	}
	if err := CreditWalletBalanceWithKey(ctx, userID, 2000, "subscription refund", opKey); err != ErrWalletOperationConflict {
		t.Fatalf("same key with a different amount = %v, want economic conflict", err)
	}
	var balance int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balance); err != nil {
		t.Fatalf("read wallet: %v", err)
	}
	if balance != 1000 {
		t.Fatalf("conflicting credit must not change wallet, got %d", balance)
	}
}
