package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type directSubscriptionFixture struct {
	userID         int64
	planID         int64
	subscriptionID int
	email          string
	uuid           string
	panelSubID     string
	expiry         int64
}

func newDirectSubscriptionFixture(t *testing.T, ctx context.Context, status string, active bool, expiry int64) *directSubscriptionFixture {
	t.Helper()
	tgID := time.Now().UnixNano()
	f := &directSubscriptionFixture{
		email:      fmt.Sprintf("direct_mutation_%d@example.test", tgID),
		uuid:       fmt.Sprintf("direct-mutation-uuid-%d", tgID),
		panelSubID: fmt.Sprintf("direct-mutation-sub-%d", tgID),
		expiry:     expiry,
	}
	if err := db.Pool.QueryRow(ctx, "INSERT INTO bot_users (telegram_id, username, status, wallet_balance) VALUES ($1, $2, 'approved', 500000) RETURNING id", tgID, fmt.Sprintf("direct_mutation_%d", tgID)).Scan(&f.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, "INSERT INTO paid_plans (name, enabled, base_ip_limit, max_ip_limit) VALUES ($1, TRUE, 1, 5) RETURNING id", fmt.Sprintf("direct_mutation_plan_%d", tgID)).Scan(&f.planID); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, "INSERT INTO subscriptions (user_id, plan_id, plan_type, client_email, client_uuid, sub_id, status, is_active, ip_limit, expire_time, traffic_limit_bytes) VALUES ($1, $2, 'paid', $3, $4, $5, $6, $7, 1, $8, $9) RETURNING id", f.userID, f.planID, f.email, f.uuid, f.panelSubID, status, active, expiry, int64(10)*1073741824).Scan(&f.subscriptionID); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, "DELETE FROM reconciliation_records WHERE user_id = $1", f.userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM transactions WHERE user_id = $1", f.userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM purchase_requests WHERE user_id = $1", f.userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM payment_intents WHERE user_id = $1", f.userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM subscriptions WHERE user_id = $1", f.userID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM paid_plans WHERE id = $1", f.planID)
		_, _ = db.Pool.Exec(ctx, "DELETE FROM bot_users WHERE id = $1", f.userID)
	})
	return f
}

func createDirectMutationIntent(t *testing.T, ctx context.Context, f *directSubscriptionFixture, action string) *db.PaymentIntent {
	t.Helper()
	var subUpdatedAt, planUpdatedAt time.Time
	var currentLimit int
	var currentExpiry *int64
	var currentActive bool
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at, ip_limit, expire_time, is_active FROM subscriptions WHERE id = $1", f.subscriptionID).Scan(&subUpdatedAt, &currentLimit, &currentExpiry, &currentActive); err != nil {
		t.Fatalf("read subscription snapshot: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM paid_plans WHERE id = $1", f.planID).Scan(&planUpdatedAt); err != nil {
		t.Fatalf("read plan snapshot: %v", err)
	}
	planID, subscriptionID := f.planID, int64(f.subscriptionID)
	targetIP, amount := currentLimit, int64(60000)
	if action == "upgrade_ip" {
		targetIP, amount = currentLimit+1, 40000
	}
	intent, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
		UserID: f.userID, IntentToken: fmt.Sprintf("%s_%d", action, time.Now().UnixNano()),
		ActionType: action, PlanID: &planID, SubscriptionID: &subscriptionID,
		AmountToman: amount, Months: 1, IPLimit: targetIP, DataGB: 10, ClientEmail: f.email,
		ProvisioningSnapshot: map[string]any{
			"expected_ip_limit": currentLimit, "expected_expire_time_milli": *currentExpiry,
			"expected_is_active":               currentActive,
			"expected_subscription_updated_at": subUpdatedAt.UTC().Format(time.RFC3339Nano),
			"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		Status: db.IntentStatusAwaitingReceipt,
	})
	if err != nil {
		t.Fatalf("create %s intent: %v", action, err)
	}
	return intent
}

func TestDirectIPUpgradeReceiptApprovalReconcilesEndToEnd(t *testing.T) {
	ctx := setupIsolatedTestDBForReconcile(t)
	f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(10*24*time.Hour).UnixMilli())
	intent := createDirectMutationIntent(t, ctx, f, "upgrade_ip")
	var intentPlanID *int64
	if err := db.Pool.QueryRow(ctx, "SELECT plan_id FROM payment_intents WHERE id = $1", intent.ID).Scan(&intentPlanID); err != nil || intentPlanID == nil || *intentPlanID != f.planID {
		t.Fatalf("IP-upgrade intent lost its paid plan: plan_id=%v err=%v", intentPlanID, err)
	}

	receipt, err := db.SubmitReceiptForActiveIntent(ctx, intent.ID, f.userID, "ip-upgrade-receipt-file", nil)
	if err != nil || receipt == nil || receipt.PurchaseRequest == nil {
		t.Fatalf("submit IP-upgrade receipt: result=%#v err=%v", receipt, err)
	}
	request := receipt.PurchaseRequest
	if request.Status != "pending" {
		t.Fatalf("valid unchanged IP-upgrade receipt should remain pending for admin approval: status=%q reason=%q", request.Status, request.ReviewReason)
	}
	var requestPlanID *int64
	if err := db.Pool.QueryRow(ctx, "SELECT plan_id FROM purchase_requests WHERE id = $1", request.ID).Scan(&requestPlanID); err != nil || requestPlanID == nil || *requestPlanID != f.planID {
		t.Fatalf("receipt did not preserve plan_id: plan_id=%v err=%v", requestPlanID, err)
	}
	if request.PaymentIntentID == nil || *request.PaymentIntentID != intent.ID || request.TelegramFileID != "ip-upgrade-receipt-file" || request.ReceiptSubmittedAt == nil {
		t.Fatalf("receipt request did not preserve evidence identity/time: %+v", request)
	}

	subscriptionID := int64(f.subscriptionID)
	desiredIP := request.IPLimit
	work := NewDirectPaymentProvisioningRecord(&DirectPaymentProvisioningPayload{
		PurchaseRequestID: request.ID, UserID: f.userID, ActionType: request.Type,
		AmountToman: *request.PriceToman, FinancialOperationKey: request.OperationKey,
		OperationKey:   fmt.Sprintf("direct_payment:%d:provisioning", request.ID),
		SubscriptionID: &subscriptionID, ClientEmail: f.email,
		ExpectedUUID: f.uuid, ExpectedSubID: f.panelSubID,
		Months: request.Months, IPLimit: request.IPLimit, DataGB: request.DataGB,
		ExpiryTimeMilli: f.expiry, TotalBytes: int64(10) * 1073741824, DesiredIPLimit: &desiredIP,
	})
	if approved, err := db.ApprovePurchaseRequest(ctx, request.ID, 96937669, work); err != nil || approved == nil {
		t.Fatalf("approve valid IP upgrade: request=%#v err=%v", approved, err)
	}
	var debitCount int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type='purchase_request' AND reference_id=$1 AND amount=$2 AND type='debit'", request.ID, -*request.PriceToman).Scan(&debitCount); err != nil || debitCount != 1 {
		t.Fatalf("admin approval should create one exact debit: count=%d err=%v", debitCount, err)
	}

	panel := &mockXUI{client: &xui.XUIClientInfo{
		Email: f.email, UUID: f.uuid, SubID: f.panelSubID, Enable: true, ExpiryTime: f.expiry, LimitIP: 1,
		TotalGB: int64(10) * 1073741824,
	}, persistUpdate: true}
	worker := NewProcessor("direct_ip_upgrade_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("process IP-upgrade reconciliation: %v", err)
	}
	finalRequest, err := db.GetPurchaseRequestByID(ctx, request.ID)
	if err != nil || finalRequest == nil || finalRequest.ProvisioningStatus != db.PurchaseProvisioningSucceeded {
		t.Fatalf("purchase did not reach fulfilled state: request=%#v err=%v", finalRequest, err)
	}
	finalSub, err := db.GetSubscriptionByID(ctx, f.subscriptionID)
	if err != nil || finalSub == nil || finalSub.Status != db.SubscriptionStatusActive || !finalSub.IsActive ||
		finalSub.IPLimit != desiredIP || finalSub.DesiredIPLimit != nil || finalSub.DesiredExpireTime != nil || finalSub.DesiredIsActive != nil {
		t.Fatalf("subscription DB desired/current state did not converge: sub=%#v err=%v", finalSub, err)
	}
	if panel.updateCalls != 1 || panel.client.LimitIP != desiredIP {
		t.Fatalf("worker did not verify the expected panel limit: calls=%d remote=%+v", panel.updateCalls, panel.client)
	}
	var workStatus string
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM reconciliation_records WHERE purchase_request_id=$1", request.ID).Scan(&workStatus); err != nil || workStatus != db.ReconciliationStatusResolvedVerified {
		t.Fatalf("expected resolved verified work item, status=%q err=%v", workStatus, err)
	}
	if _, err := NewProcessor("duplicate_ip_upgrade_worker", panel).ProcessOnce(ctx); err != nil {
		t.Fatalf("duplicate reconciliation pass: %v", err)
	}
	if panel.updateCalls != 1 || debitCount != 1 {
		t.Fatalf("duplicate processing repeated an economic or panel effect: updates=%d debits=%d", panel.updateCalls, debitCount)
	}
}

func assertStaleReceiptEvidence(t *testing.T, ctx context.Context, intent *db.PaymentIntent, userID int64) {
	t.Helper()
	persistedIntent, err := db.GetPaymentIntentByID(ctx, intent.ID)
	if err != nil || persistedIntent == nil {
		t.Fatalf("reload intent after restart: intent=%#v err=%v", persistedIntent, err)
	}
	result, err := db.SubmitReceiptForActiveIntent(ctx, persistedIntent.ID, userID, "durable-stale-receipt-file", nil)
	if err != nil || result == nil || result.PurchaseRequest == nil || result.PurchaseRequest.Status != db.PurchaseStatusNeedsManualReview {
		t.Fatalf("stale receipt was not durably routed to manual review: result=%#v err=%v", result, err)
	}
	request := result.PurchaseRequest
	if request.PaymentIntentID == nil || *request.PaymentIntentID != intent.ID || request.TelegramFileID != "durable-stale-receipt-file" ||
		request.ReceiptSubmittedAt == nil || request.ReviewReason == "" {
		t.Fatalf("stale receipt evidence missing durable identity/time/reason: %+v", request)
	}
	persistedRequest, err := db.GetPurchaseRequestByID(ctx, request.ID)
	if err != nil || persistedRequest == nil || persistedRequest.Status != db.PurchaseStatusNeedsManualReview || persistedRequest.TelegramFileID != "durable-stale-receipt-file" || persistedRequest.ReviewReason == "" {
		t.Fatalf("manual-review receipt is not visible after a fresh read: request=%#v err=%v", persistedRequest, err)
	}
	manualReviews, err := db.GetPurchaseRequestsNeedingManualReview(ctx)
	if err != nil {
		t.Fatalf("load durable admin review queue: %v", err)
	}
	foundReview := false
	for _, reviewed := range manualReviews {
		if reviewed.ID == request.ID && reviewed.TelegramFileID == "durable-stale-receipt-file" && reviewed.PaymentIntentID != nil && *reviewed.PaymentIntentID == intent.ID {
			foundReview = true
		}
	}
	if !foundReview {
		t.Fatalf("admin review queue omitted durable receipt request #%d: %+v", request.ID, manualReviews)
	}
	var snapshotsEqual bool
	if err := db.Pool.QueryRow(ctx, "SELECT pi.provisioning_snapshot = pr.provisioning_snapshot FROM payment_intents pi JOIN purchase_requests pr ON pr.payment_intent_id=pi.id WHERE pi.id=$1", intent.ID).Scan(&snapshotsEqual); err != nil || !snapshotsEqual {
		t.Fatalf("receipt processing overwrote the original immutable snapshot: equal=%t err=%v", snapshotsEqual, err)
	}
	replay, err := db.SubmitReceiptForActiveIntent(ctx, intent.ID, userID, "new-retry-file-id", nil)
	if err != nil || replay == nil || !replay.IsDuplicate || !replay.NeedsManualReview || replay.PurchaseRequest == nil || replay.PurchaseRequest.TelegramFileID != "durable-stale-receipt-file" {
		t.Fatalf("receipt retry/restart did not preserve the first file: result=%#v err=%v", replay, err)
	}
	var intentStatus string
	if err := db.Pool.QueryRow(ctx, "SELECT status FROM payment_intents WHERE id=$1", intent.ID).Scan(&intentStatus); err != nil || intentStatus != db.IntentStatusReceiptSubmitted {
		t.Fatalf("stale receipt intent state is not durable: status=%q err=%v", intentStatus, err)
	}
	var debitCount, workCount int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM transactions WHERE reference_type='purchase_request' AND reference_id=$1", request.ID).Scan(&debitCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM reconciliation_records WHERE purchase_request_id=$1", request.ID).Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if debitCount != 0 || workCount != 0 {
		t.Fatalf("stale receipt was approved/provisioned: debits=%d work=%d", debitCount, workCount)
	}
	if approved, err := db.ApprovePurchaseRequest(ctx, request.ID, 96937669, &db.ReconciliationRecord{}); err != nil || approved != nil {
		t.Fatalf("manual-review request was approvable: request=%#v err=%v", approved, err)
	}
}

func TestReceiptEvidenceSurvivesStaleDirectTerms(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	t.Run("plan disabled after buy intent", func(t *testing.T) {
		f := newDirectRestartFixture(t, ctx)
		if _, err := db.Pool.Exec(ctx, "UPDATE paid_plans SET enabled=FALSE, updated_at=NOW() WHERE id=$1", f.planID); err != nil {
			t.Fatal(err)
		}
		assertStaleReceiptEvidence(t, ctx, f.intent, f.userID)
	})
	t.Run("plan updated after buy intent", func(t *testing.T) {
		f := newDirectRestartFixture(t, ctx)
		if _, err := db.Pool.Exec(ctx, "UPDATE paid_plans SET base_price_toman=base_price_toman+10, updated_at=NOW() WHERE id=$1", f.planID); err != nil {
			t.Fatal(err)
		}
		assertStaleReceiptEvidence(t, ctx, f.intent, f.userID)
	})
	t.Run("subscription updated before extend receipt", func(t *testing.T) {
		f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(10*24*time.Hour).UnixMilli())
		intent := createDirectMutationIntent(t, ctx, f, "extend")
		if _, err := db.Pool.Exec(ctx, "UPDATE subscriptions SET ip_limit=3, updated_at=NOW() WHERE id=$1", f.subscriptionID); err != nil {
			t.Fatal(err)
		}
		assertStaleReceiptEvidence(t, ctx, intent, f.userID)
	})
	t.Run("subscription updated before IP-upgrade receipt", func(t *testing.T) {
		f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(10*24*time.Hour).UnixMilli())
		intent := createDirectMutationIntent(t, ctx, f, "upgrade_ip")
		if _, err := db.Pool.Exec(ctx, "UPDATE subscriptions SET ip_limit=3, updated_at=NOW() WHERE id=$1", f.subscriptionID); err != nil {
			t.Fatal(err)
		}
		assertStaleReceiptEvidence(t, ctx, intent, f.userID)
	})
}

func assertDirectExtensionReconcilesWithWalletExpiryRule(t *testing.T, status string, wasActive bool, oldExpiry int64) {
	t.Helper()
	ctx := setupIsolatedTestDBForReconcile(t)
	wantExpiry, err := db.CalculateExtendedExpiry(&oldExpiry, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	f := newDirectSubscriptionFixture(t, ctx, status, wasActive, oldExpiry)
	intent := createDirectMutationIntent(t, ctx, f, "extend")
	receipt, err := db.SubmitReceiptForActiveIntent(ctx, intent.ID, f.userID, "direct-extension-receipt", nil)
	if err != nil || receipt == nil || receipt.PurchaseRequest == nil {
		t.Fatalf("submit direct extension receipt: result=%#v err=%v", receipt, err)
	}
	request := receipt.PurchaseRequest
	subID := int64(f.subscriptionID)
	work := NewDirectPaymentProvisioningRecord(&DirectPaymentProvisioningPayload{
		PurchaseRequestID: request.ID, UserID: f.userID, ActionType: "extend", AmountToman: *request.PriceToman,
		FinancialOperationKey: request.OperationKey, OperationKey: fmt.Sprintf("direct_payment:%d:provisioning", request.ID),
		SubscriptionID: &subID, ClientEmail: f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.panelSubID,
		Months: request.Months, IPLimit: request.IPLimit, DataGB: request.DataGB, ExpiryTimeMilli: oldExpiry,
		TotalBytes: int64(10) * 1073741824,
	})
	if _, err := db.ApprovePurchaseRequest(ctx, request.ID, 96937669, work); err != nil {
		t.Fatalf("approve direct extension: %v", err)
	}
	var desiredExpiry int64
	var desiredActive bool
	if err := db.Pool.QueryRow(ctx, "SELECT desired_expire_time, desired_is_active FROM subscriptions WHERE id=$1", f.subscriptionID).Scan(&desiredExpiry, &desiredActive); err != nil {
		t.Fatal(err)
	}
	if absInt64Regression(desiredExpiry-wantExpiry) > 2000 || !desiredActive {
		t.Fatalf("direct renewal must use the shared 30-day rule and activate: expiry=%d want=%d active=%t", desiredExpiry, wantExpiry, desiredActive)
	}

	panel := &mockXUI{client: &xui.XUIClientInfo{
		Email: f.email, UUID: f.uuid, SubID: f.panelSubID, Enable: wasActive, ExpiryTime: oldExpiry,
		LimitIP: 1, TotalGB: int64(10) * 1073741824,
	}, persistUpdate: true}
	worker := NewProcessor("direct_extension_worker", panel)
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("process direct extension reconciliation: %v", err)
	}
	finalRequest, err := db.GetPurchaseRequestByID(ctx, request.ID)
	if err != nil || finalRequest == nil || finalRequest.ProvisioningStatus != db.PurchaseProvisioningSucceeded {
		t.Fatalf("direct extension did not reach fulfilled state: request=%#v err=%v", finalRequest, err)
	}
	finalSub, err := db.GetSubscriptionByID(ctx, f.subscriptionID)
	if err != nil || finalSub == nil || finalSub.Status != db.SubscriptionStatusActive || !finalSub.IsActive ||
		finalSub.ExpireTime == nil || absInt64Regression(*finalSub.ExpireTime-wantExpiry) > 2000 ||
		finalSub.DesiredExpireTime != nil || finalSub.DesiredIsActive != nil {
		t.Fatalf("direct extension did not converge DB current state: sub=%#v err=%v", finalSub, err)
	}
	if panel.updateCalls != 1 || panel.client == nil || !panel.client.Enable || absInt64Regression(panel.client.ExpiryTime-wantExpiry) > 2000 {
		t.Fatalf("direct extension did not converge remote state: calls=%d client=%+v want expiry=%d", panel.updateCalls, panel.client, wantExpiry)
	}
}

func TestActiveDirectExtensionUsesWalletThirtyDayRule(t *testing.T) {
	oldExpiry := time.Now().Add(10 * 24 * time.Hour).UnixMilli()
	assertDirectExtensionReconcilesWithWalletExpiryRule(t, db.SubscriptionStatusActive, true, oldExpiry)
}

func TestExpiredDirectExtensionReactivatesFromNow(t *testing.T) {
	oldExpiry := time.Now().Add(-14 * 24 * time.Hour).UnixMilli()
	assertDirectExtensionReconcilesWithWalletExpiryRule(t, db.SubscriptionStatusExpired, false, oldExpiry)
}

func TestDisabledDirectExtensionUsesWalletThirtyDayRenewalRule(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	oldExpiry := time.Now().Add(15 * 24 * time.Hour).UnixMilli()
	f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusDisabled, false, oldExpiry)
	if _, err := db.Pool.Exec(ctx, `UPDATE subscriptions SET is_active=FALSE WHERE id=$1`, f.subscriptionID); err != nil {
		t.Fatal(err)
	}
	intent := createDirectMutationIntent(t, ctx, f, "extend")
	receipt, err := db.SubmitReceiptForActiveIntent(ctx, intent.ID, f.userID, "disabled-extension-receipt", nil)
	if err != nil || receipt == nil || receipt.PurchaseRequest == nil {
		t.Fatalf("submit disabled-service extension receipt: result=%#v err=%v", receipt, err)
	}
	request := receipt.PurchaseRequest
	subID := int64(f.subscriptionID)
	work := NewDirectPaymentProvisioningRecord(&DirectPaymentProvisioningPayload{
		PurchaseRequestID: request.ID, UserID: f.userID, ActionType: "extend", AmountToman: *request.PriceToman,
		FinancialOperationKey: request.OperationKey, OperationKey: fmt.Sprintf("direct_payment:%d:provisioning", request.ID),
		SubscriptionID: &subID, ClientEmail: f.email, ExpectedUUID: f.uuid, ExpectedSubID: f.panelSubID,
		Months: request.Months, IPLimit: request.IPLimit, DataGB: request.DataGB, ExpiryTimeMilli: oldExpiry,
		TotalBytes: int64(10) * 1073741824,
	})
	if _, err := db.ApprovePurchaseRequest(ctx, request.ID, 96937669, work); err != nil {
		t.Fatalf("approve disabled-service extension: %v", err)
	}
	var desiredExpiry int64
	var desiredActive bool
	if err := db.Pool.QueryRow(ctx, `SELECT desired_expire_time, desired_is_active FROM subscriptions WHERE id=$1`, f.subscriptionID).Scan(&desiredExpiry, &desiredActive); err != nil {
		t.Fatal(err)
	}
	wantExpiry := oldExpiry + int64(30*24*time.Hour/time.Millisecond)
	if desiredExpiry != wantExpiry || !desiredActive {
		t.Fatalf("direct renewal should match wallet's 30-day disabled-service rule: expiry=%d want=%d active=%t", desiredExpiry, wantExpiry, desiredActive)
	}
}

func absInt64Regression(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func TestMismatchedIPUpgradePlanIDRejected(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(10*24*time.Hour).UnixMilli())
	valid := createDirectMutationIntent(t, ctx, f, "upgrade_ip")
	if err := db.CancelPaymentIntent(ctx, valid.ID, f.userID, "regression test cleanup"); err != nil {
		t.Fatalf("cancel first intent before stale-plan attempt: %v", err)
	}
	wrongPlanID := f.planID + 100000
	subscriptionID := int64(f.subscriptionID)
	var subUpdatedAt, planUpdatedAt time.Time
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM subscriptions WHERE id=$1", f.subscriptionID).Scan(&subUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM paid_plans WHERE id=$1", f.planID).Scan(&planUpdatedAt); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
		UserID: f.userID, IntentToken: fmt.Sprintf("wrong_plan_%d", time.Now().UnixNano()), ActionType: "upgrade_ip",
		PlanID: &wrongPlanID, SubscriptionID: &subscriptionID, AmountToman: 40000, Months: 1, IPLimit: 2, ClientEmail: f.email,
		ProvisioningSnapshot: map[string]any{
			"expected_ip_limit": 1, "expected_expire_time_milli": f.expiry, "expected_is_active": true,
			"expected_subscription_updated_at": subUpdatedAt.UTC().Format(time.RFC3339Nano),
			"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
		},
	})
	if err == nil || (!errors.Is(err, db.ErrSubscriptionMutationStale) && !errors.Is(err, db.ErrSubscriptionMutationInvalid)) {
		t.Fatalf("mismatched direct IP-upgrade plan ID must be rejected, got %v", err)
	}
}

func TestUnresolvedDirectSubscriptionMutationReservesOneService(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	for _, test := range []struct {
		name   string
		first  string
		second string
	}{
		{name: "extension blocks extension", first: "extend", second: "extend"},
		{name: "extension blocks IP upgrade", first: "extend", second: "upgrade_ip"},
		{name: "IP upgrade blocks extension", first: "upgrade_ip", second: "extend"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(15*24*time.Hour).UnixMilli())
			firstIntent := createDirectMutationIntent(t, ctx, f, test.first)
			receipt, err := db.SubmitReceiptForActiveIntent(ctx, firstIntent.ID, f.userID, "first-direct-receipt", nil)
			if err != nil || receipt == nil || receipt.PurchaseRequest == nil {
				t.Fatalf("submit first paid mutation: result=%#v err=%v", receipt, err)
			}
			if _, err := createDirectMutationIntentResult(ctx, f, test.second); !errors.Is(err, db.ErrSubscriptionMutationInProgress) {
				t.Fatalf("second unresolved direct mutation error=%v; want subscription reservation rejection", err)
			}
			if _, err := db.RejectPurchaseRequest(ctx, receipt.PurchaseRequest.ID, 96937669); err != nil {
				t.Fatalf("reject earlier paid request: %v", err)
			}
			if _, err := createDirectMutationIntentResult(ctx, f, test.second); err != nil {
				t.Fatalf("terminal rejected request should release the subscription: %v", err)
			}
		})
	}
}

func createDirectMutationIntentResult(ctx context.Context, f *directSubscriptionFixture, action string) (*db.PaymentIntent, error) {
	var subUpdatedAt, planUpdatedAt time.Time
	var currentLimit int
	var currentExpiry *int64
	var currentActive bool
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at, ip_limit, expire_time, is_active FROM subscriptions WHERE id = $1", f.subscriptionID).Scan(&subUpdatedAt, &currentLimit, &currentExpiry, &currentActive); err != nil {
		return nil, err
	}
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM paid_plans WHERE id = $1", f.planID).Scan(&planUpdatedAt); err != nil {
		return nil, err
	}
	planID, subscriptionID := f.planID, int64(f.subscriptionID)
	targetIP, amount := currentLimit, int64(60000)
	if action == "upgrade_ip" {
		targetIP, amount = currentLimit+1, 40000
	}
	return db.CreatePaymentIntent(ctx, &db.PaymentIntent{
		UserID: f.userID, IntentToken: fmt.Sprintf("%s_%d", action, time.Now().UnixNano()), ActionType: action,
		PlanID: &planID, SubscriptionID: &subscriptionID, AmountToman: amount, Months: 1, IPLimit: targetIP,
		ClientEmail: f.email, Status: db.IntentStatusAwaitingReceipt,
		ProvisioningSnapshot: map[string]any{
			"expected_ip_limit": currentLimit, "expected_expire_time_milli": *currentExpiry,
			"expected_is_active":               currentActive,
			"expected_subscription_updated_at": subUpdatedAt.UTC().Format(time.RFC3339Nano),
			"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
		},
	})
}

func TestDirectMutationsOnDifferentSubscriptionsRemainIndependent(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(15*24*time.Hour).UnixMilli())
	first := createDirectMutationIntent(t, ctx, f, "extend")
	receipt, err := db.SubmitReceiptForActiveIntent(ctx, first.ID, f.userID, "first-subscription-receipt", nil)
	if err != nil || receipt == nil {
		t.Fatalf("submit first subscription receipt: result=%#v err=%v", receipt, err)
	}
	var secondID int
	secondEmail := f.email + ".independent"
	if err := db.Pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, plan_id, plan_type, client_email, client_uuid, sub_id, status, is_active, ip_limit, expire_time, traffic_limit_bytes) VALUES ($1, $2, 'paid', $3, $4, $5, 'active', TRUE, 1, $6, $7) RETURNING id`, f.userID, f.planID, secondEmail, f.uuid+"-independent", f.panelSubID+"-independent", time.Now().Add(15*24*time.Hour).UnixMilli(), int64(10)*1073741824).Scan(&secondID); err != nil {
		t.Fatalf("create independent subscription: %v", err)
	}
	secondFixture := *f
	secondFixture.subscriptionID = secondID
	secondFixture.email = secondEmail
	secondFixture.uuid += "-independent"
	secondFixture.panelSubID += "-independent"
	if _, err := createDirectMutationIntentResult(ctx, &secondFixture, "upgrade_ip"); err != nil {
		t.Fatalf("unresolved request for another subscription must not block independent service: %v", err)
	}
}

func TestConcurrentDirectMutationIntentCreationHasSingleSubscriptionWinner(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(15*24*time.Hour).UnixMilli())
	var subUpdatedAt, planUpdatedAt time.Time
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM subscriptions WHERE id = $1", f.subscriptionID).Scan(&subUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM paid_plans WHERE id = $1", f.planID).Scan(&planUpdatedAt); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
				UserID: f.userID, IntentToken: fmt.Sprintf("concurrent_direct_%d_%d", time.Now().UnixNano(), i),
				ActionType: "extend", PlanID: &f.planID, SubscriptionID: ptrInt64ForRegression(int64(f.subscriptionID)),
				AmountToman: 60000, Months: 1, IPLimit: 1, ClientEmail: f.email,
				Status: db.IntentStatusAwaitingReceipt,
				ProvisioningSnapshot: map[string]any{
					"expected_ip_limit": 1, "expected_expire_time_milli": f.expiry, "expected_is_active": true,
					"expected_subscription_updated_at": subUpdatedAt.UTC().Format(time.RFC3339Nano),
					"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
				},
			})
			results <- err
		}(i)
	}
	close(start)
	wins, blocked := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			wins++
		} else if errors.Is(err, db.ErrSubscriptionMutationInProgress) || errors.Is(err, db.ErrActivePaymentIntentExists) {
			blocked++
		} else {
			t.Fatalf("unexpected concurrent intent result: %v", err)
		}
	}
	if wins != 1 || blocked != 1 {
		t.Fatalf("subscription must have one unresolved direct-mutation winner, winners=%d blocked=%d", wins, blocked)
	}
	var unresolved int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM payment_intents WHERE subscription_id=$1 AND status='awaiting_receipt' AND action_type IN ('extend','upgrade_ip')`, f.subscriptionID).Scan(&unresolved); err != nil || unresolved != 1 {
		t.Fatalf("database reservation count=%d err=%v; want one", unresolved, err)
	}
}

func TestDirectExtensionRejectsCancelledDeletedAndDeletingServices(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	for _, lifecycle := range []struct {
		status string
		active bool
	}{
		{db.SubscriptionStatusCancelled, false},
		{db.SubscriptionStatusDeleted, false},
		{db.SubscriptionStatusCancelRequested, true},
		{db.SubscriptionStatusDeprovisioning, true},
	} {
		t.Run(lifecycle.status, func(t *testing.T) {
			f := newDirectSubscriptionFixture(t, ctx, lifecycle.status, lifecycle.active, time.Now().Add(15*24*time.Hour).UnixMilli())
			if _, err := createDirectMutationIntentResult(ctx, f, "extend"); !errors.Is(err, db.ErrSubscriptionMutationStale) {
				t.Fatalf("%s service should not receive paid extension instructions, got %v", lifecycle.status, err)
			}
		})
	}
}

func TestDirectExtendAndIPUpgradeRequirePaidPlanID(t *testing.T) {
	ctx := setupTestDBForReconcile(t)
	for _, action := range []string{"extend", "upgrade_ip"} {
		t.Run(action, func(t *testing.T) {
			f := newDirectSubscriptionFixture(t, ctx, db.SubscriptionStatusActive, true, time.Now().Add(15*24*time.Hour).UnixMilli())
			subscriptionID := int64(f.subscriptionID)
			var subUpdatedAt, planUpdatedAt time.Time
			if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM subscriptions WHERE id=$1", f.subscriptionID).Scan(&subUpdatedAt); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(ctx, "SELECT updated_at FROM paid_plans WHERE id=$1", f.planID).Scan(&planUpdatedAt); err != nil {
				t.Fatal(err)
			}
			_, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
				UserID: f.userID, IntentToken: fmt.Sprintf("missing_plan_%s_%d", action, time.Now().UnixNano()),
				ActionType: action, PlanID: nil, SubscriptionID: &subscriptionID,
				AmountToman: 10000, Months: 1, IPLimit: 2, ClientEmail: f.email,
				Status: db.IntentStatusAwaitingReceipt,
				ProvisioningSnapshot: map[string]any{
					"expected_ip_limit": 1, "expected_expire_time_milli": f.expiry, "expected_is_active": true,
					"expected_subscription_updated_at": subUpdatedAt.UTC().Format(time.RFC3339Nano),
					"expected_plan_updated_at":         planUpdatedAt.UTC().Format(time.RFC3339Nano),
				},
			})
			if !errors.Is(err, db.ErrSubscriptionMutationInvalid) {
				t.Fatalf("%s without durable paid plan ID error=%v; want invalid snapshot rejection", action, err)
			}
		})
	}
}

func ptrInt64ForRegression(value int64) *int64 { return &value }
