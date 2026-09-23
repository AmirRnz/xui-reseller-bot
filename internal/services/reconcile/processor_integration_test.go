package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type directRestartFixture struct {
	userID     int64
	telegramID int64
	planID     int64
	quoteID    int64
	intent     *db.PaymentIntent
	request    *db.PurchaseRequest
	workItem   *db.ReconciliationRecord
	email      string
	uuid       string
	subID      string
}

func newDirectRestartFixture(t *testing.T, ctx context.Context) *directRestartFixture {
	t.Helper()
	telegramID := time.Now().UnixNano()
	f := &directRestartFixture{
		telegramID: telegramID,
		email:      fmt.Sprintf("restart_%d@example.com", telegramID),
		uuid:       fmt.Sprintf("uuid-restart-%d", telegramID),
		subID:      fmt.Sprintf("sub-restart-%d", telegramID),
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, telegramID, fmt.Sprintf("restart_user_%d", telegramID)).Scan(&f.userID); err != nil {
		t.Fatalf("create restart user: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO paid_plans (name) VALUES ($1) RETURNING id`, fmt.Sprintf("restart_plan_%d", telegramID)).Scan(&f.planID); err != nil {
		t.Fatalf("create restart plan: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_quotes (quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb, base_price_toman, final_price_toman)
		VALUES ($1, $2, $3, 'Restart plan', 3, 90, 4, 25, 123456, 123456) RETURNING id
	`, fmt.Sprintf("restart_quote_%d", telegramID), f.userID, f.planID).Scan(&f.quoteID); err != nil {
		t.Fatalf("create restart quote: %v", err)
	}
	snapshot := map[string]any{
		"client_uuid": f.uuid, "sub_id": f.subID, "inbound_ids": []int{4, 9},
		"expiry_time_milli": int64(-7776000000), "total_bytes": int64(25) * 1073741824,
		"flow": "restart-flow", "group": "restart-group", "telegram_id": telegramID, "plan_name": "Restart plan",
	}
	intent, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
		UserID: f.userID, IntentToken: fmt.Sprintf("restart_token_%d", telegramID), ActionType: "new_subscription",
		PlanID: &f.planID, QuoteID: &f.quoteID, AmountToman: 123456, Months: 3, IPLimit: 4, DataGB: 25,
		DisplayName: "Restart display", ClientEmail: f.email, ProvisioningSnapshot: snapshot,
		Status: db.IntentStatusAwaitingReceipt,
	})
	if err != nil {
		t.Fatalf("create restart intent: %v", err)
	}
	f.intent = intent
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM reconciliation_records WHERE user_id = $1`, f.userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, f.userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM subscriptions WHERE user_id = $1`, f.userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE user_id = $1`, f.userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, f.userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_quotes WHERE id = $1`, f.quoteID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, f.planID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, f.userID)
	})
	return f
}

func approveRestartFixture(t *testing.T, ctx context.Context, f *directRestartFixture) {
	t.Helper()
	// A new process has no FSM. Recover the only active receipt intent by user.
	recovered, err := db.GetLatestActivePaymentIntent(ctx, f.userID)
	if err != nil || recovered == nil || recovered.ID != f.intent.ID {
		t.Fatalf("durable intent recovery failed: intent=%#v err=%v", recovered, err)
	}
	wrongAmount := int64(1)
	result, err := db.SubmitReceiptForActiveIntent(ctx, recovered.ID, f.userID, "restart-receipt-file", &db.PurchaseRequest{
		Type: "extend", PriceToman: &wrongAmount, Months: 99, IPLimit: 99, DataGB: 99,
		CustomName: "stale FSM name", ClientEmail: "stale@example.com",
	})
	if err != nil || result == nil || result.PurchaseRequest == nil {
		t.Fatalf("durable receipt recovery failed: result=%#v err=%v", result, err)
	}
	f.request = result.PurchaseRequest
	if f.request.Type != "buy" || f.request.PlanID == nil || *f.request.PlanID != f.planID || f.request.QuoteID == nil || *f.request.QuoteID != f.quoteID || f.request.PriceToman == nil || *f.request.PriceToman != 123456 || f.request.Months != 3 || f.request.IPLimit != 4 || f.request.DataGB != 25 || f.request.CustomName != "Restart display" || f.request.ClientEmail != f.email {
		t.Fatalf("durable intent lost to stale request data: %+v", f.request)
	}
	planID := int(f.planID)
	financialKey := f.request.OperationKey
	f.workItem = NewDirectPaymentProvisioningRecord(&DirectPaymentProvisioningPayload{
		PurchaseRequestID: f.request.ID, UserID: f.userID, ActionType: "buy", QuoteID: &f.quoteID,
		AmountToman: 123456, FinancialOperationKey: financialKey,
		OperationKey: fmt.Sprintf("direct_payment:%d:provisioning", f.request.ID),
		ClientEmail:  f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.subID, PlanID: &planID,
		InboundIDs: []int{4, 9}, Months: 3, IPLimit: 4, DataGB: 25,
		ExpiryTimeMilli: -7776000000, TotalBytes: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TelegramID: f.telegramID,
		CustomName: "Restart display",
	})
	if _, err := db.ApprovePurchaseRequest(ctx, f.request.ID, 96937669, f.workItem); err != nil {
		t.Fatalf("atomic admin approval failed: %v", err)
	}
}

func setupTestDBForReconcile(t *testing.T) context.Context {
	ctx := context.Background()
	testURL := os.Getenv("TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("Skipping test: TEST_DATABASE_URL is not set")
	}

	cfg := &config.DatabaseConfig{URL: testURL}
	if config.Global == nil {
		config.Global = &config.Config{Database: *cfg}
	} else {
		config.Global.Database = *cfg
	}

	err := db.Connect(ctx, cfg)
	if err != nil {
		t.Skipf("Skipping test: database connection failed: %v", err)
	}

	err = db.Migrate(ctx)
	if err != nil {
		t.Skipf("Skipping test: migration failed: %v", err)
	}

	return ctx
}

func TestProcessOnce_Integration(t *testing.T) {
	ctx := setupTestDBForReconcile(t)

	// Create test user
	tgID := int64(9876543210 + time.Now().Unix()%10000)
	var userID int64
	err := db.Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name)
		VALUES ($1, 'recon_user', 'Recon')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`, tgID).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	defer func() {
		_, _ = db.Pool.Exec(ctx, "DELETE FROM reconciliation_records WHERE user_id = $1", userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM bot_users WHERE id = $1", userID)
	}()

	opKey := fmt.Sprintf("recon_test_op_%d", time.Now().UnixNano())
	rec := &db.ReconciliationRecord{
		OperationKey: opKey,
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"user_id":       userID,
			"refund_amount": int64(50000),
			"operation_key": opKey,
			"description":   "integration test refund",
		},
	}
	if err := db.CreateReconciliationRecord(ctx, rec); err != nil {
		t.Fatalf("failed to create reconciliation record: %v", err)
	}
	recID := rec.ID

	mockClient := &mockXUI{}
	p := NewProcessor("test_worker", mockClient)
	p.CreditFn = func(ctx context.Context, uid int64, amount int64, desc, key string) error {
		return nil // simulate successful refund credit
	}

	// 1. ProcessOnce claims and executes
	count, err := p.ProcessOnce(ctx)
	if err != nil {
		t.Fatalf("ProcessOnce failed: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected at least 1 record processed, got %d", count)
	}

	// 2. Verify record in DB is now completed
	rec, err = db.GetReconciliationRecordByID(ctx, recID)
	if err != nil {
		t.Fatalf("failed to get reconciliation record: %v", err)
	}
	if rec.Status != db.ReconciliationStatusResolvedVerified && rec.Status != db.ReconciliationStatusResolved {
		t.Fatalf("expected status 'resolved_verified' or 'resolved', got %s", rec.Status)
	}

	// 3. Second call finds 0 pending records
	count2, err := p.ProcessOnce(ctx)
	if err != nil {
		t.Fatalf("second ProcessOnce failed: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("expected 0 pending records on second run, got %d", count2)
	}
}

func TestDirectPaymentReceiptAndApprovalRecoverAfterRestart(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	approveRestartFixture(t, ctx, f)

	// The approval transaction has committed, but this process never starts the
	// synchronous provisioner. A fresh worker must discover the exact work item.
	panel := &mockXUI{persistAdded: true}
	freshWorker := NewProcessor("fresh_direct_worker", panel)
	freshWorker.BatchSize = 100
	if _, err := freshWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("fresh worker failed: %v", err)
	}
	request, err := db.GetPurchaseRequestByID(ctx, f.request.ID)
	if err != nil || request == nil || request.ProvisioningStatus != db.PurchaseProvisioningSucceeded {
		t.Fatalf("direct request was not marked succeeded: request=%#v err=%v", request, err)
	}
	subscription, err := db.GetSubscriptionByEmail(ctx, f.email)
	if err != nil || subscription == nil {
		t.Fatalf("direct payment did not create a subscription: sub=%#v err=%v", subscription, err)
	}
	if subscription.UserID != f.userID || subscription.PlanID == nil || *subscription.PlanID != int(f.planID) || subscription.QuoteID == nil || *subscription.QuoteID != f.quoteID || subscription.ClientUUID != f.uuid || subscription.SubID != f.subID || subscription.DisplayName != "Restart display" || subscription.IPLimit != 4 || subscription.TrafficLimitBytes != int64(25)*1073741824 || subscription.ExpireTime == nil || *subscription.ExpireTime != -7776000000 {
		t.Fatalf("recovered subscription does not match durable purchase: %+v", subscription)
	}
	if panel.addCalls != 1 {
		t.Fatalf("expected exactly one XUI create, got %d", panel.addCalls)
	}
	if _, err := NewProcessor("duplicate_direct_worker", panel).ProcessOnce(ctx); err != nil {
		t.Fatalf("duplicate worker pass failed: %v", err)
	}
	if panel.addCalls != 1 {
		t.Fatalf("second worker pass duplicated XUI create: calls=%d", panel.addCalls)
	}
}

func TestWalletDebitCrashBeforeXUIAndDuplicateWorker(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	if _, err := db.Pool.Exec(ctx, `UPDATE bot_users SET wallet_balance = 200000 WHERE id = $1`, f.userID); err != nil {
		t.Fatal(err)
	}
	planID := int(f.planID)
	operationKey := fmt.Sprintf("wallet_restart_%d", time.Now().UnixNano())
	payload := &PurchaseProvisioningPayload{
		UserID: f.userID, OperationKey: operationKey, DebitOperationKey: operationKey, QuoteID: &f.quoteID,
		Email: f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.subID, PlanID: &planID,
		InboundIDs: []int{4, 9}, Months: 3, IPLimit: 4, DataGB: 25, Price: 123456,
		ExpiryTimeMilli: -7776000000, TotalBytes: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TelegramID: f.telegramID,
		PlanName: "Restart plan", DisplayName: "Restart display", RefundOperationKey: operationKey + ":refund",
	}
	workItem := NewPurchaseProvisioningRecord(payload)
	workItem.ObservedState = map[string]any{"phase": "ready", "outcome": "wallet_debit_committed"}
	if err := db.DebitWalletBalanceWithReconciliation(ctx, f.userID, 123456, "test wallet purchase", operationKey, workItem); err != nil {
		t.Fatalf("atomic wallet debit/work commit failed: %v", err)
	}
	var balance int64
	if err := db.Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, f.userID).Scan(&balance); err != nil || balance != 76544 {
		t.Fatalf("debit not committed exactly once: balance=%d err=%v", balance, err)
	}
	panel := &mockXUI{persistAdded: true}
	if panel.addCalls != 0 {
		t.Fatal("XUI was called before the durable wallet transaction committed")
	}
	worker := NewProcessor("wallet_restart_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("restarted wallet worker failed: %v", err)
	}
	if panel.addCalls != 1 {
		t.Fatalf("expected one create after wallet restart, got %d", panel.addCalls)
	}
	if _, err := NewProcessor("wallet_duplicate_worker", panel).ProcessOnce(ctx); err != nil {
		t.Fatalf("duplicate wallet worker failed: %v", err)
	}
	if panel.addCalls != 1 {
		t.Fatalf("duplicate processing created another client: calls=%d", panel.addCalls)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, f.userID).Scan(&balance); err != nil || balance != 76544 {
		t.Fatalf("restart changed the wallet a second time: balance=%d err=%v", balance, err)
	}
}

func TestRemoteCreateBeforeLocalInsertIsAdoptedByPersistedIdentity(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	approveRestartFixture(t, ctx, f)
	if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET observed_state = jsonb_set(observed_state, '{phase}', '"create_attempted"'::jsonb) WHERE operation_key = $1`, f.workItem.OperationKey); err != nil {
		t.Fatal(err)
	}
	panel := &mockXUI{client: &xui.XUIClientInfo{
		Email: f.email, UUID: f.uuid, SubID: f.subID, Enable: true,
		ExpiryTime: -7776000000, LimitIP: 4, TotalGB: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TgID: f.telegramID,
		Comment: "Restart display", InboundIDs: []int{4, 9},
	}}
	worker := NewProcessor("post_create_restart_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("worker failed to adopt existing remote client: %v", err)
	}
	subscription, err := db.GetSubscriptionByEmail(ctx, f.email)
	if err != nil || subscription == nil || subscription.ClientUUID != f.uuid || subscription.SubID != f.subID || subscription.UserID != f.userID || subscription.QuoteID == nil || *subscription.QuoteID != f.quoteID {
		t.Fatalf("remote client was not adopted using the persisted identity: sub=%#v err=%v", subscription, err)
	}
	if panel.addCalls != 0 {
		t.Fatalf("adoption unexpectedly issued AddClient %d times", panel.addCalls)
	}
}

func TestUnknownAddResultReconcilesWithoutDuplicateCreate(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	approveRestartFixture(t, ctx, f)
	panel := &mockXUI{persistAdded: true, addRes: xui.WriteResult{Outcome: xui.WriteUnknown, Err: errors.New("panel timed out after accepting create")}}
	firstWorker := NewProcessor("unknown_create_worker", panel)
	firstWorker.BatchSize = 100
	if _, err := firstWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("first unknown-outcome worker pass failed: %v", err)
	}
	if panel.addCalls != 1 {
		t.Fatalf("expected one unknown XUI create attempt, got %d", panel.addCalls)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET next_attempt_at = NOW() WHERE operation_key = $1`, f.workItem.OperationKey); err != nil {
		t.Fatal(err)
	}
	secondWorker := NewProcessor("unknown_create_restart_worker", panel)
	secondWorker.BatchSize = 100
	if _, err := secondWorker.ProcessOnce(ctx); err != nil {
		t.Fatalf("restart reconciliation failed: %v", err)
	}
	subscription, err := db.GetSubscriptionByEmail(ctx, f.email)
	if err != nil || subscription == nil || subscription.ClientUUID != f.uuid || subscription.SubID != f.subID {
		t.Fatalf("remote accepted create was not adopted: sub=%#v err=%v", subscription, err)
	}
	if panel.addCalls != 1 {
		t.Fatalf("unknown AddClient outcome was retried as a duplicate create: calls=%d", panel.addCalls)
	}
}

func TestUnsupportedPurchaseTypeCannotBeApprovedOrMarkedSucceeded(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	result, err := db.SubmitReceiptForActiveIntent(ctx, f.intent.ID, f.userID, "unsupported-type-receipt", nil)
	if err != nil || result == nil || result.PurchaseRequest == nil {
		t.Fatalf("persist receipt: result=%#v err=%v", result, err)
	}
	f.request = result.PurchaseRequest
	if _, err := db.Pool.Exec(ctx, `UPDATE purchase_requests SET type = 'unknown_action' WHERE id = $1`, f.request.ID); err != nil {
		t.Fatal(err)
	}
	f.request.Type = "unknown_action"
	planID := int(f.planID)
	workItem := NewDirectPaymentProvisioningRecord(&DirectPaymentProvisioningPayload{
		PurchaseRequestID: f.request.ID, UserID: f.userID, ActionType: "unknown_action", QuoteID: &f.quoteID,
		AmountToman: 123456, FinancialOperationKey: f.request.OperationKey,
		OperationKey: fmt.Sprintf("direct_payment:%d:provisioning", f.request.ID),
		ClientEmail:  f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.subID, PlanID: &planID,
		InboundIDs: []int{4, 9}, Months: 3, IPLimit: 4, DataGB: 25,
		ExpiryTimeMilli: -7776000000, TotalBytes: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TelegramID: f.telegramID, CustomName: "Restart display",
	})
	if _, err := db.ApprovePurchaseRequest(ctx, f.request.ID, 96937669, workItem); err == nil {
		t.Fatal("unsupported request type unexpectedly committed approval")
	}
	var status string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM purchase_requests WHERE id = $1`, f.request.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("rejected approval transaction changed purchase status: status=%q err=%v", status, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE purchase_requests SET status = 'approved', provisioning_status = 'pending' WHERE id = $1`, f.request.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateReconciliationRecord(ctx, workItem); err != nil {
		t.Fatalf("persist legacy work item: %v", err)
	}
	panel := &mockXUI{}
	worker := NewProcessor("unsupported_action_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("process unsupported legacy work item: %v", err)
	}
	rec, err := db.GetReconciliationRecordByID(ctx, workItem.ID)
	request, requestErr := db.GetPurchaseRequestByID(ctx, f.request.ID)
	if err != nil || requestErr != nil || rec == nil || request == nil {
		t.Fatalf("read unsupported action outcome: rec=%#v request=%#v errs=(%v,%v)", rec, request, err, requestErr)
	}
	if panel.addCalls != 0 || rec.Status != db.ReconciliationStatusManualReview || request.ProvisioningStatus == db.PurchaseProvisioningSucceeded {
		t.Fatalf("unsupported action mutated XUI or succeeded: calls=%d rec=%+v request=%+v", panel.addCalls, rec, request)
	}
}

func TestWalletRemoteCreateBeforeLocalInsertIsAdoptedWithoutSecondCreate(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectRestartFixture(t, ctx)
	if _, err := db.Pool.Exec(ctx, `UPDATE bot_users SET wallet_balance = 200000 WHERE id = $1`, f.userID); err != nil {
		t.Fatal(err)
	}
	planID := int(f.planID)
	opKey := fmt.Sprintf("wallet_after_create_%d", time.Now().UnixNano())
	payload := &PurchaseProvisioningPayload{
		UserID: f.userID, OperationKey: opKey, DebitOperationKey: opKey, QuoteID: &f.quoteID,
		Email: f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.subID, PlanID: &planID,
		InboundIDs: []int{4, 9}, Months: 3, IPLimit: 4, DataGB: 25, Price: 123456,
		ExpiryTimeMilli: -7776000000, TotalBytes: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TelegramID: f.telegramID,
		PlanName: "Restart plan", DisplayName: "Restart display", RefundOperationKey: opKey + ":refund",
	}
	workItem := NewPurchaseProvisioningRecord(payload)
	workItem.ObservedState = map[string]any{"phase": "ready", "outcome": "wallet_debit_committed"}
	if err := db.DebitWalletBalanceWithReconciliation(ctx, f.userID, 123456, "wallet crash boundary", opKey, workItem); err != nil {
		t.Fatalf("atomic wallet commit: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET observed_state = jsonb_set(observed_state, '{phase}', '"create_attempted"'::jsonb) WHERE operation_key = $1`, workItem.OperationKey); err != nil {
		t.Fatal(err)
	}
	panel := &mockXUI{client: &xui.XUIClientInfo{
		Email: f.email, UUID: f.uuid, SubID: f.subID, Enable: true,
		ExpiryTime: -7776000000, LimitIP: 4, TotalGB: int64(25) * 1073741824,
		Flow: "restart-flow", Group: "restart-group", TgID: f.telegramID,
		Comment: "Restart plan", InboundIDs: []int{4, 9},
	}}
	if !walletRemoteMatches(panel.client, payload) {
		t.Fatalf("test setup has a non-matching wallet remote client: remote=%#v payload=%#v", panel.client, payload)
	}
	persisted, err := db.GetReconciliationRecordByID(ctx, workItem.ID)
	if err != nil || persisted == nil {
		t.Fatalf("load durable wallet work item: record=%#v err=%v", persisted, err)
	}
	decoded, err := DecodePurchaseProvisioning(persisted.DesiredState, &f.userID, workItem.OperationKey)
	if err != nil || !walletRemoteMatches(panel.client, decoded) {
		t.Fatalf("persisted wallet snapshot does not match the remote client: remote=%#v expected_telegram_id=%d decoded=%#v err=%v", panel.client, f.telegramID, decoded, err)
	}
	if decoded.TelegramID != f.telegramID {
		t.Fatalf("durable Telegram ID changed during JSONB round-trip: got %d, want %d", decoded.TelegramID, f.telegramID)
	}
	worker := NewProcessor("wallet_after_create_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("adopt pre-existing wallet client: %v", err)
	}
	sub, err := db.GetSubscriptionByEmail(ctx, f.email)
	if err != nil || sub == nil || sub.UserID != f.userID || sub.ClientUUID != f.uuid || sub.SubID != f.subID || sub.QuoteID == nil || *sub.QuoteID != f.quoteID {
		t.Fatalf("wallet subscription was not recovered from exact identity: sub=%#v err=%v", sub, err)
	}
	if panel.addCalls != 0 {
		t.Fatalf("recovery issued a duplicate XUI create: %d", panel.addCalls)
	}
}

func TestDirectDefinitiveNoWriteCanRetryButUnknownAbsentRequiresManualReview(t *testing.T) {
	t.Run("definitive no-write retries safely", func(t *testing.T) {
		ctx := setupTestDBForReconcile(t)
		f := newDirectRestartFixture(t, ctx)
		approveRestartFixture(t, ctx, f)
		panel := &mockXUI{persistAdded: true, addResults: []xui.WriteResult{
			{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("panel capability gate is closed")},
			{Outcome: xui.WriteSucceeded},
		}}
		first := NewProcessor("definitive_no_write_worker", panel)
		first.BatchSize = 100
		if _, err := first.ProcessOnce(ctx); err != nil {
			t.Fatalf("first pass failed: %v", err)
		}
		rec, err := db.GetReconciliationRecordByID(ctx, f.workItem.ID)
		if err != nil || rec == nil || rec.ObservedState["phase"] != "ready" {
			t.Fatalf("confirmed no-write did not reset durable create phase: rec=%#v err=%v", rec, err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET next_attempt_at = NOW() WHERE id = $1`, rec.ID); err != nil {
			t.Fatal(err)
		}
		second := NewProcessor("definitive_no_write_restart", panel)
		second.BatchSize = 100
		if _, err := second.ProcessOnce(ctx); err != nil {
			t.Fatalf("safe retry failed: %v", err)
		}
		request, err := db.GetPurchaseRequestByID(ctx, f.request.ID)
		sub, subErr := db.GetSubscriptionByEmail(ctx, f.email)
		if err != nil || subErr != nil || request == nil || request.ProvisioningStatus != db.PurchaseProvisioningSucceeded || sub == nil || sub.ClientUUID != f.uuid || panel.addCalls != 2 {
			t.Fatalf("definitive no-write retry did not complete one service: request=%#v sub=%#v calls=%d errs=(%v,%v)", request, sub, panel.addCalls, err, subErr)
		}
	})

	t.Run("unknown outcome with absent remote becomes manual review", func(t *testing.T) {
		ctx := setupTestDBForReconcile(t)
		f := newDirectRestartFixture(t, ctx)
		approveRestartFixture(t, ctx, f)
		panel := &mockXUI{addRes: xui.WriteResult{Outcome: xui.WriteUnknown, Err: errors.New("write result uncertain")}}
		first := NewProcessor("unknown_absent_worker", panel)
		first.BatchSize = 100
		if _, err := first.ProcessOnce(ctx); err != nil {
			t.Fatalf("first unknown pass failed: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET next_attempt_at = NOW() WHERE id = $1`, f.workItem.ID); err != nil {
			t.Fatal(err)
		}
		second := NewProcessor("unknown_absent_restart", panel)
		second.BatchSize = 100
		if _, err := second.ProcessOnce(ctx); err != nil {
			t.Fatalf("unknown outcome restart failed: %v", err)
		}
		rec, err := db.GetReconciliationRecordByID(ctx, f.workItem.ID)
		request, reqErr := db.GetPurchaseRequestByID(ctx, f.request.ID)
		if err != nil || reqErr != nil || rec == nil || request == nil || rec.Status != db.ReconciliationStatusManualReview || request.ProvisioningStatus == db.PurchaseProvisioningSucceeded || panel.addCalls != 1 {
			t.Fatalf("ambiguous absent create was retried or marked successful: rec=%#v request=%#v calls=%d errs=(%v,%v)", rec, request, panel.addCalls, err, reqErr)
		}
	})
}
