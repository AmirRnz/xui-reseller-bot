package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestCreditAllApprovedUsersWithKey(t *testing.T) {
	ctx := setupTestDB(t)

	prefix := fmt.Sprintf("bulk_test_%d", time.Now().UnixNano())

	// Create 2 approved users
	var user1ID, user2ID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 1000)
		RETURNING id
	`, time.Now().UnixNano()+1, prefix+"_user1").Scan(&user1ID); err != nil {
		t.Fatalf("create user1: %v", err)
	}
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 2000)
		RETURNING id
	`, time.Now().UnixNano()+2, prefix+"_user2").Scan(&user2ID); err != nil {
		t.Fatalf("create user2: %v", err)
	}

	defer func() {
		_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE user_id IN ($1, $2)", user1ID, user2ID)
		_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE id IN ($1, $2)", user1ID, user2ID)
		_, _ = Pool.Exec(ctx, "DELETE FROM bulk_credit_operations WHERE operation_key LIKE $1", prefix+"%")
	}()

	opKey1 := prefix + "_op1"

	// 1. Initial run credits approved users
	count, err := CreditAllApprovedUsersWithKey(ctx, 5000, "bulk credit 1", opKey1)
	if err != nil {
		t.Fatalf("CreditAllApprovedUsersWithKey failed: %v", err)
	}
	if count < 2 {
		t.Fatalf("expected at least 2 users credited, got %d", count)
	}

	var b1, b2 int64
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user1ID).Scan(&b1)
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user2ID).Scan(&b2)
	if b1 != 6000 || b2 != 7000 {
		t.Fatalf("balances unexpected: user1=%d, user2=%d", b1, b2)
	}

	// 2. Replay returns ErrWalletOperationAlreadyApplied
	_, err = CreditAllApprovedUsersWithKey(ctx, 5000, "bulk credit 1 replay", opKey1)
	if !errors.Is(err, ErrWalletOperationAlreadyApplied) {
		t.Fatalf("expected ErrWalletOperationAlreadyApplied, got %v", err)
	}

	// Balances must remain unchanged
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user1ID).Scan(&b1)
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user2ID).Scan(&b2)
	if b1 != 6000 || b2 != 7000 {
		t.Fatalf("balances changed after replay: user1=%d, user2=%d", b1, b2)
	}

	// 3. Crash/retry resumption with new user exclusion:
	// Simulate an in_progress bulk operation where only user1 is in recipient_user_ids snapshot.
	opKeyCrash := prefix + "_crash"
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bulk_credit_operations (operation_key, amount, admin_id, recipient_user_ids, status, created_at)
		VALUES ($1, 3000, NULL, $2, 'in_progress', NOW())
	`, opKeyCrash, []int64{user1ID}); err != nil {
		t.Fatalf("insert in_progress op: %v", err)
	}

	// Create user3 who is approved AFTER the snapshot was taken
	var user3ID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, status, wallet_balance)
		VALUES ($1, $2, 'approved', 500)
		RETURNING id
	`, time.Now().UnixNano()+3, prefix+"_user3").Scan(&user3ID); err != nil {
		t.Fatalf("create user3: %v", err)
	}
	defer func() {
		_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE user_id = $1", user3ID)
		_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE id = $1", user3ID)
	}()

	// Resume the crashed operation
	resumedCount, err := CreditAllApprovedUsersWithKey(ctx, 3000, "resumed credit", opKeyCrash)
	if err != nil {
		t.Fatalf("resumed credit failed: %v", err)
	}
	if resumedCount != 1 {
		t.Fatalf("expected exactly 1 user credited (user1 from snapshot), got %d", resumedCount)
	}

	// Verify user1 got 3000, user2 was not in snapshot, user3 was created after snapshot so NOT credited
	var b3 int64
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user1ID).Scan(&b1)
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user2ID).Scan(&b2)
	_ = Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", user3ID).Scan(&b3)
	if b1 != 9000 {
		t.Fatalf("expected user1 balance 9000, got %d", b1)
	}
	if b2 != 7000 {
		t.Fatalf("expected user2 balance 7000, got %d", b2)
	}
	if b3 != 500 {
		t.Fatalf("expected user3 balance 500, got %d", b3)
	}

	// Check status transitioned to 'completed'
	var opStatus string
	_ = Pool.QueryRow(ctx, "SELECT status FROM bulk_credit_operations WHERE operation_key = $1", opKeyCrash).Scan(&opStatus)
	if opStatus != "completed" {
		t.Fatalf("expected status 'completed', got %q", opStatus)
	}

	// 4. Replaying the resumed operation returns ErrWalletOperationAlreadyApplied
	_, err = CreditAllApprovedUsersWithKey(ctx, 3000, "resumed replay", opKeyCrash)
	if !errors.Is(err, ErrWalletOperationAlreadyApplied) {
		t.Fatalf("expected ErrWalletOperationAlreadyApplied on replaying resumed op, got %v", err)
	}

	// 5. 0-recipient replay
	opKeyZero := prefix + "_zero"
	if _, err := Pool.Exec(ctx, `
		INSERT INTO bulk_credit_operations (operation_key, amount, admin_id, recipient_user_ids, status, created_at)
		VALUES ($1, 1000, NULL, '{}', 'completed', NOW())
	`, opKeyZero); err != nil {
		t.Fatalf("insert zero recipient op: %v", err)
	}
	_, err = CreditAllApprovedUsersWithKey(ctx, 1000, "zero replay", opKeyZero)
	if !errors.Is(err, ErrWalletOperationAlreadyApplied) {
		t.Fatalf("expected ErrWalletOperationAlreadyApplied on 0-recipient replay, got %v", err)
	}
}
