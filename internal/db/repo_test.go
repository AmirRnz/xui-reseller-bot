package db

import (
	"context"
	"os"
	"testing"
	"time"

	"xui-end-bot/internal/config"
)

func setupTestDB(t *testing.T) context.Context {
	ctx := context.Background()

	// Locate config.yaml. In tests, the working directory is internal/db,
	// so the config file is two levels up: ../../config.yaml.
	configFile := "../../config.yaml"
	if _, err := os.Stat(configFile); err != nil {
		t.Skipf("Skipping test: config file not found at %s: %v", configFile, err)
	}

	err := config.Load(configFile)
	if err != nil {
		t.Skipf("Skipping test: failed to load config: %v", err)
	}

	// Connect
	t.Logf("Connecting to database: Host=%s, Port=%d, User=%s, DBName=%s", config.Global.Database.Host, config.Global.Database.Port, config.Global.Database.User, config.Global.Database.DBName)
	err = Connect(ctx, &config.Global.Database)
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

	// 8. DeleteSubscription
	err = DeleteSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to delete subscription: %v", err)
	}

	// Verify deleted
	gotSubDeleted, err := GetSubscriptionByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("failed to check deleted subscription: %v", err)
	}
	if gotSubDeleted != nil {
		t.Fatalf("subscription was not deleted: %+v", gotSubDeleted)
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
	if approvedReq.Status != "approved" {
		t.Fatalf("expected status 'approved', got %q", approvedReq.Status)
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

	// Verify request is pending again
	rolledReq, err := GetPurchaseRequestByID(ctx, req.ID)
	if err != nil {
		t.Fatalf("failed to get purchase request: %v", err)
	}
	if rolledReq.Status != "pending" || rolledReq.AdminID != nil {
		t.Fatalf("expected status 'pending' and nil AdminID, got status %q, AdminID %v", rolledReq.Status, rolledReq.AdminID)
	}

	// Verify transaction was deleted
	err = Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type = 'purchase_request' AND reference_id = $1", req.ID).Scan(&txCount)
	if err != nil {
		t.Fatalf("failed to count transactions after rollback: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("expected 0 transactions after rollback, got %d", txCount)
	}

	// Verify HasPendingClaimRequest is true again
	hasPending, err = HasPendingClaimRequest(ctx, "sub_claim_123")
	if err != nil {
		t.Fatalf("failed to check pending claim: %v", err)
	}
	if !hasPending {
		t.Fatalf("expected HasPendingClaimRequest to be true after rollback, got false")
	}
}

