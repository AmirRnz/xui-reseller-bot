package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"xui-reseller-bot/internal/config"
)

func setupTestDB(t *testing.T) context.Context {
	ctx := context.Background()

	testURL := os.Getenv("TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("Skipping test: TEST_DATABASE_URL is not set (isolated test database required to protect production)")
	}

	cfg := &config.DatabaseConfig{URL: testURL}
	if config.Global == nil {
		config.Global = &config.Config{Database: *cfg}
	} else {
		config.Global.Database = *cfg
	}

	t.Logf("Connecting to test database via TEST_DATABASE_URL")
	err := Connect(ctx, cfg)
	if err != nil {
		t.Skipf("Skipping test: database connection failed: %v", err)
	}

	// Migrate (ensures tables are present)
	err = Migrate(ctx)
	if err != nil {
		t.Skipf("Skipping test: migration failed: %v", err)
	}

	return ctx
}

func TestSettingsRepo(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_settings WHERE key LIKE 'test_key_%'")
		}
	}()

	// 1. Get non-existent key
	val, err := GetSetting(ctx, "test_key_non_existent")
	if err != nil {
		t.Fatalf("expected no error for non-existent setting, got: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty string for non-existent setting, got: %q", val)
	}

	// 2. Set key
	err = SetSetting(ctx, "test_key_1", "value_1")
	if err != nil {
		t.Fatalf("failed to set setting: %v", err)
	}

	// 3. Get key
	val, err = GetSetting(ctx, "test_key_1")
	if err != nil {
		t.Fatalf("failed to get setting: %v", err)
	}
	if val != "value_1" {
		t.Fatalf("expected 'value_1', got %q", val)
	}

	// 4. Update key (ON CONFLICT DO UPDATE)
	err = SetSetting(ctx, "test_key_1", "value_1_updated")
	if err != nil {
		t.Fatalf("failed to update setting: %v", err)
	}

	val, err = GetSetting(ctx, "test_key_1")
	if err != nil {
		t.Fatalf("failed to get setting after update: %v", err)
	}
	if val != "value_1_updated" {
		t.Fatalf("expected 'value_1_updated', got %q", val)
	}
}

func TestSubscriptionRepo(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM subscriptions WHERE client_email LIKE 'test_email_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999999")
		}
	}()

	// We need a user to reference in subscriptions
	// Create user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999999, 'test_user', 'Test', 'User')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	planID := 42
	expireTime := int64(time.Now().Add(24 * time.Hour).Unix())

	sub := &Subscription{
		UserID:      userID,
		PlanID:      &planID,
		PlanType:    "paid",
		ClientEmail: "test_email_1@example.com",
		SubID:       "sub_id_12345",
		DisplayName: "Test Subscription 1",
		IPLimit:     3,
		ExpireTime:  &expireTime,
		IsActive:    true,
	}

	// 1. CreateSubscription
	err = CreateSubscription(ctx, sub)
	if err != nil {
		t.Fatalf("failed to create subscription: %v", err)
	}

	if sub.ID == 0 {
		t.Fatalf("expected assigned subscription ID, got 0")
	}
	if sub.CreatedAt.IsZero() || sub.UpdatedAt.IsZero() {
		t.Fatalf("expected assigned timestamps, got created_at=%v, updated_at=%v", sub.CreatedAt, sub.UpdatedAt)
	}

	// 2. GetSubscriptionByID
	gotSub, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to get subscription by ID: %v", err)
	}
	if gotSub == nil {
		t.Fatalf("subscription not found by ID")
	}
	if gotSub.ClientEmail != sub.ClientEmail || *gotSub.PlanID != *sub.PlanID || gotSub.UserID != sub.UserID {
		t.Fatalf("retrieved subscription does not match, got: %+v, expected: %+v", gotSub, sub)
	}

	// 3. GetSubscriptionByEmail
	gotSub2, err := GetSubscriptionByEmail(ctx, "test_email_1@example.com")
	if err != nil {
		t.Fatalf("failed to get subscription by email: %v", err)
	}
	if gotSub2 == nil {
		t.Fatalf("subscription not found by email")
	}
	if gotSub2.ID != sub.ID {
		t.Fatalf("retrieved subscription ID mismatch, got %d, expected %d", gotSub2.ID, sub.ID)
	}

	// 4. GetSubscriptionByID non-existent
	nonExistentSub, err := GetSubscriptionByID(ctx, -999)
	if err != nil {
		t.Fatalf("expected nil and no error for non-existent ID, got: %v", err)
	}
	if nonExistentSub != nil {
		t.Fatalf("expected nil for non-existent ID, got: %+v", nonExistentSub)
	}

	// 5. GetSubscriptionByEmail non-existent
	nonExistentSubEmail, err := GetSubscriptionByEmail(ctx, "test_email_non_existent@example.com")
	if err != nil {
		t.Fatalf("expected nil and no error for non-existent email, got: %v", err)
	}
	if nonExistentSubEmail != nil {
		t.Fatalf("expected nil for non-existent email, got: %+v", nonExistentSubEmail)
	}

	// 6. GetActiveSubscriptionsByUserID
	activeSubs, err := GetActiveSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get active subscriptions by user ID: %v", err)
	}
	if len(activeSubs) != 1 {
		t.Fatalf("expected 1 active subscription, got %d", len(activeSubs))
	}
	if activeSubs[0].ID != sub.ID {
		t.Fatalf("active subscription ID mismatch, got %d, expected %d", activeSubs[0].ID, sub.ID)
	}

	// 7. UpdateSubscription
	sub.IPLimit = 5
	sub.IsActive = false
	// Sleep a bit to ensure updated_at changes
	time.Sleep(10 * time.Millisecond)

	err = UpdateSubscription(ctx, sub)
	if err != nil {
		t.Fatalf("failed to update subscription: %v", err)
	}

	// Fetch again to verify changes
	gotSubUpdated, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to get updated subscription: %v", err)
	}
	if gotSubUpdated.IPLimit != 5 || gotSubUpdated.IsActive {
		t.Fatalf("subscription fields not updated correctly: %+v", gotSubUpdated)
	}

	// Since we set IsActive to false, GetActiveSubscriptionsByUserID should return 0 results now
	activeSubs2, err := GetActiveSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("failed to get active subscriptions by user ID after update: %v", err)
	}
	if len(activeSubs2) != 0 {
		t.Fatalf("expected 0 active subscriptions, got %d", len(activeSubs2))
	}

	// 8. DeleteSubscription is a non-destructive lifecycle transition.
	err = DeleteSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to delete subscription: %v", err)
	}

	// Verify the auditable row remains.
	gotSubDeleted, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to check deleted subscription: %v", err)
	}
	if gotSubDeleted == nil || gotSubDeleted.Status != SubscriptionStatusDeleted || gotSubDeleted.IsActive {
		t.Fatalf("subscription was not marked deleted: %+v", gotSubDeleted)
	}
}

func TestCancelSubscriptionWithRefundIsAuditable(t *testing.T) {
	ctx := setupTestDB(t)
	const telegramID int64 = 999999997
	const email = "test_cancel_refund@example.com"
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM refund_requests WHERE subscription_id IN (SELECT id FROM subscriptions WHERE client_email = $1)", email)
			_, _ = Pool.Exec(ctx, "DELETE FROM subscriptions WHERE client_email = $1", email)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = $1", telegramID)
		}
	}()

	var userID int64
	if err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name)
		VALUES ($1, 'cancel_refund_test', 'Cancel')
		RETURNING id
	`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	expire := time.Now().Add(90 * 24 * time.Hour).UnixMilli()
	sub := &Subscription{UserID: userID, PlanType: PlanTypePaid, ClientEmail: email, ClientUUID: "uuid", SubID: "sub", DisplayName: "cancel", IPLimit: 1, ExpireTime: &expire, IsActive: true}
	if err := CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	req, err := CancelSubscriptionWithRefund(ctx, sub.ID, userID, 1234, "test_cancel_refund:1")
	if err != nil {
		t.Fatalf("cancel subscription: %v", err)
	}
	if req == nil || req.ID == 0 {
		t.Fatalf("expected a persisted refund request with a non-zero ID, got %#v", req)
	}
	gotSub, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("cancelled subscription row missing: sub=%#v err=%v", gotSub, err)
	}
	if gotSub.Status != SubscriptionStatusCancelled || gotSub.IsActive {
		t.Fatalf("unexpected cancelled state: %+v", gotSub)
	}
	gotReq, err := GetRefundRequestByID(ctx, req.ID)
	if err != nil || gotReq == nil || gotReq.SubscriptionID == nil || *gotReq.SubscriptionID != int64(sub.ID) {
		t.Fatalf("refund is not linked to the preserved subscription: req=%#v err=%v", gotReq, err)
	}
	manageable, err := GetManageableSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("query manageable subscriptions: %v", err)
	}
	for _, current := range manageable {
		if current.ID == sub.ID {
			t.Fatalf("cancelled subscription appeared in manageable services: %+v", current)
		}
	}
	allSubs, err := GetSubscriptionsByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("query audit subscriptions: %v", err)
	}
	found := false
	for _, current := range allSubs {
		if current.ID == sub.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cancelled subscription disappeared from audit query")
	}
}

func TestDeletePlanWithUsage(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM test_usage WHERE plan_id IN (SELECT id FROM test_plans WHERE name = 'Test Plan Delete')")
			_, _ = Pool.Exec(ctx, "DELETE FROM test_plans WHERE name = 'Test Plan Delete'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999998")
		}
	}()

	// 1. Create a user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999998, 'test_user_del_plan', 'Test', 'User Del Plan')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// 2. Create a test plan
	var planID int64
	err = Pool.QueryRow(ctx, `
		INSERT INTO test_plans (name, description, inbound_ids, expire_seconds, max_data_bytes, max_per_day, is_global, enabled)
		VALUES ('Test Plan Delete', 'Desc', '[]'::jsonb, 3600, 0, 1, true, true)
		RETURNING id
	`).Scan(&planID)
	if err != nil {
		t.Fatalf("failed to create test plan: %v", err)
	}

	// 3. Create test usage record
	_, err = Pool.Exec(ctx, `
		INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
		VALUES ($1, $2, 1, CURRENT_DATE)
	`, userID, planID)
	if err != nil {
		t.Fatalf("failed to create test usage: %v", err)
	}

	// 4. Delete the plan
	err = DeletePlan(ctx, PlanTypeTest, planID)
	if err != nil {
		t.Fatalf("failed to delete test plan with usage: %v", err)
	}

	// 5. Verify it is deleted from test_plans
	var exists bool
	err = Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM test_plans WHERE id = $1)", planID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check if plan exists: %v", err)
	}
	if exists {
		t.Fatalf("expected test plan to be deleted, but it still exists")
	}

	// 6. Verify usage record is also deleted
	var usageExists bool
	err = Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM test_usage WHERE plan_id = $1)", planID).Scan(&usageExists)
	if err != nil {
		t.Fatalf("failed to check if usage exists: %v", err)
	}
	if usageExists {
		t.Fatalf("expected test usage record to be deleted, but it still exists")
	}
}

func TestPlanSyncSubs(t *testing.T) {
	ctx := setupTestDB(t)

	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM test_plans WHERE name LIKE 'Test Sync %'")
			_, _ = Pool.Exec(ctx, "DELETE FROM paid_plans WHERE name LIKE 'Paid Sync %'")
		}
	}()

	// 1. Test Plan with SyncSubs = false
	tp := &TestPlan{
		Name:          "Test Sync False",
		Description:   "Desc",
		InboundIDs:    []int{1},
		ExpireSeconds: 3600,
		MaxDataBytes:  0,
		Flow:          "",
		MaxPerDay:     1,
		IsGlobal:      true,
		Enabled:       true,
		SyncSubs:      false,
	}
	err := CreateTestPlan(ctx, tp)
	if err != nil {
		t.Fatalf("failed to create test plan: %v", err)
	}

	gotTp, err := GetTestPlanByID(ctx, tp.ID)
	if err != nil {
		t.Fatalf("failed to get test plan: %v", err)
	}
	if gotTp.SyncSubs {
		t.Fatalf("expected SyncSubs to be false, got true")
	}

	// Update to true
	gotTp.SyncSubs = true
	err = UpdateTestPlan(ctx, gotTp)
	if err != nil {
		t.Fatalf("failed to update test plan: %v", err)
	}

	gotTp2, err := GetTestPlanByID(ctx, tp.ID)
	if err != nil {
		t.Fatalf("failed to get test plan: %v", err)
	}
	if !gotTp2.SyncSubs {
		t.Fatalf("expected SyncSubs to be true after update, got false")
	}

	// 2. Paid Plan with SyncSubs = false
	pp := &PaidPlan{
		Name:            "Paid Sync False",
		InboundIDs:      []int{1},
		BasePrice:       100.0,
		BaseIPLimit:     1,
		MaxIPLimit:      2,
		PricePerExtraIP: 10.0,
		Flow:            "",
		DiscountTiers:   []DiscountTier{},
		IsGlobal:        true,
		Enabled:         true,
		SyncSubs:        false,
	}
	err = CreatePaidPlan(ctx, pp)
	if err != nil {
		t.Fatalf("failed to create paid plan: %v", err)
	}

	gotPp, err := GetPaidPlanByID(ctx, pp.ID)
	if err != nil {
		t.Fatalf("failed to get paid plan: %v", err)
	}
	if gotPp.SyncSubs {
		t.Fatalf("expected SyncSubs to be false for paid plan, got true")
	}

	// Update to true
	gotPp.SyncSubs = true
	err = UpdatePaidPlan(ctx, gotPp)
	if err != nil {
		t.Fatalf("failed to update paid plan: %v", err)
	}

	gotPp2, err := GetPaidPlanByID(ctx, pp.ID)
	if err != nil {
		t.Fatalf("failed to get paid plan: %v", err)
	}
	if !gotPp2.SyncSubs {
		t.Fatalf("expected SyncSubs to be true for paid plan after update, got false")
	}
}

func TestPurchaseRollbackAndClaim(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM purchase_requests WHERE client_email LIKE 'test_claim_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE reference_type = 'purchase_request'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999998")
		}
	}()

	// Create user
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (999999998, 'test_user_claim', 'Test', 'User')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	// Create purchase request (for claim)
	req := &PurchaseRequest{
		UserID:         userID,
		Type:           "claim",
		Price:          100.0,
		Months:         1,
		IPLimit:        1,
		DataGB:         10,
		CustomName:     "sub_claim_123",
		ClientEmail:    "test_claim_email@example.com",
		TelegramFileID: "claim",
		Status:         "pending",
	}

	err = CreatePurchaseRequest(ctx, req)
	if err != nil {
		t.Fatalf("failed to create purchase request: %v", err)
	}

	// 1. Verify HasPendingClaimRequest
	hasPending, err := HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if !hasPending {
		t.Fatalf("expected HasPendingClaimRequest to be true, got false")
	}

	// 2. Approve the purchase request
	approvedReq, err := ApprovePurchaseRequest(ctx, req.ID, 999999998)
	if err != nil {
		t.Fatalf("failed to approve purchase request: %v", err)
	}
	if approvedReq == nil {
		t.Fatalf("approved request is nil")
	}
	if approvedReq.Status != "approved" || approvedReq.ProvisioningStatus != PurchaseProvisioningPending {
		t.Fatalf("expected payment approved and provisioning pending, got status=%q provisioning=%q", approvedReq.Status, approvedReq.ProvisioningStatus)
	}

	// Verify transaction was created
	var txCount int
	err = Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&txCount)
	if err != nil {
		t.Fatalf("failed to count transactions: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("expected 1 transaction, got %d", txCount)
	}

	// Verify HasPendingClaimRequest is now false since status is no longer 'pending'
	hasPending, err = HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if hasPending {
		t.Fatalf("expected HasPendingClaimRequest to be false after approval, got true")
	}

	// 3. Rollback the purchase request
	err = RollbackPurchaseRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("failed to rollback purchase request: %v", err)
	}

	// Provisioning failure/retry must not erase the approved payment fact.
	rolledReq, err := GetPurchaseRequestByID(ctx, req.ID)
	if err != nil {
		t.Fatalf("failed to get purchase request: %v", err)
	}
	if rolledReq.Status != "approved" || rolledReq.ProvisioningStatus != PurchaseProvisioningRetryable || rolledReq.AdminID == nil {
		t.Fatalf("expected approved payment and retryable provisioning, got status %q provisioning %q AdminID %v", rolledReq.Status, rolledReq.ProvisioningStatus, rolledReq.AdminID)
	}

	// Verify transaction history remains intact.
	err = Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&txCount)
	if err != nil {
		t.Fatalf("failed to count transactions after rollback: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("expected 1 transaction after provisioning rollback, got %d", txCount)
	}

	// The claim is no longer pending because the payment was approved.
	hasPending, err = HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if hasPending {
		t.Fatalf("expected HasPendingClaimRequest to remain false after provisioning retry state")
	}
}

func TestWalletOperationKeyIsConcurrentIdempotent(t *testing.T) {
	ctx := setupTestDB(t)
	operationKey := "test_wallet_operation:" + time.Now().UTC().Format("20060102150405.000000000")
	laterOperationKey := operationKey + ":later"
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE operation_key = $1", operationKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE operation_key = $1", laterOperationKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = 999999997")
		}
	}()

	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name, wallet_balance)
		VALUES (999999997, 'wallet_idempotency_user', 'Wallet', 'Idempotency', 0)
		ON CONFLICT (telegram_id) DO UPDATE SET wallet_balance = 0
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- AddWalletBalanceWithKey(ctx, userID, 100, "concurrent wallet test", operationKey)
		}()
	}
	wg.Wait()
	close(results)

	applied, duplicate := 0, 0
	for err := range results {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, ErrWalletOperationAlreadyApplied):
			duplicate++
		default:
			t.Fatalf("unexpected wallet operation error: %v", err)
		}
	}
	if applied != 1 || duplicate != 1 {
		t.Fatalf("expected one applied operation and one duplicate, got applied=%d duplicate=%d", applied, duplicate)
	}

	var balance int64
	if err := Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", userID).Scan(&balance); err != nil {
		t.Fatalf("failed to read wallet balance: %v", err)
	}
	if balance != 100 {
		t.Fatalf("expected one wallet credit, got balance %d", balance)
	}
	var transactionCount int
	if err := Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE operation_key = $1", operationKey).Scan(&transactionCount); err != nil {
		t.Fatalf("failed to count wallet operations: %v", err)
	}
	if transactionCount != 1 {
		t.Fatalf("expected one durable wallet operation, got %d", transactionCount)
	}
	// A later, distinct confirmation intent is allowed to perform the same
	// economic action again; only replaying the original key is suppressed.
	if err := AddWalletBalanceWithKey(ctx, userID, 100, "concurrent wallet test", laterOperationKey); err != nil {
		t.Fatalf("new confirmation intent was incorrectly rejected: %v", err)
	}
	if err := Pool.QueryRow(ctx, "SELECT wallet_balance FROM bot_users WHERE id = $1", userID).Scan(&balance); err != nil {
		t.Fatalf("failed to read balance after later intent: %v", err)
	}
	if balance != 200 {
		t.Fatalf("expected distinct later intent to apply, got balance %d", balance)
	}
}
