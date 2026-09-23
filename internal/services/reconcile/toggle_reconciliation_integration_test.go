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

type toggleFixture struct {
	userID     int64
	subID      int
	email      string
	uuid       string
	panelSubID string
	expiry     int64
	updatedAt  time.Time
}

func newToggleFixture(t *testing.T, ctx context.Context) *toggleFixture {
	t.Helper()
	tgID := time.Now().UnixNano()
	f := &toggleFixture{
		email:      fmt.Sprintf("toggle_%d@example.test", tgID),
		uuid:       fmt.Sprintf("toggle-uuid-%d", tgID),
		panelSubID: fmt.Sprintf("toggle-sub-%d", tgID),
		expiry:     time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("toggle_%d", tgID)).Scan(&f.userID); err != nil {
		t.Fatalf("create toggle owner: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO subscriptions (user_id, plan_type, client_email, client_uuid, sub_id, status, is_active, ip_limit, expire_time, traffic_limit_bytes) VALUES ($1, 'paid', $2, $3, $4, 'active', TRUE, 2, $5, $6) RETURNING id, updated_at`, f.userID, f.email, f.uuid, f.panelSubID, f.expiry, int64(15)*1073741824).Scan(&f.subID, &f.updatedAt); err != nil {
		t.Fatalf("create toggle subscription: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM reconciliation_records WHERE subscription_id=$1`, f.subID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id=$1`, f.subID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM bot_users WHERE id=$1`, f.userID)
	})
	return f
}

func queueDisableToggle(t *testing.T, ctx context.Context, f *toggleFixture, op string) (bool, error) {
	t.Helper()
	return db.QueueSubscriptionToggle(ctx, f.subID, f.userID, false, f.updatedAt, op)
}

func remoteToggleClient(f *toggleFixture) *xui.XUIClientInfo {
	return &xui.XUIClientInfo{Email: f.email, UUID: f.uuid, SubID: f.panelSubID, Enable: true, ExpiryTime: f.expiry, LimitIP: 2, TotalGB: int64(15) * 1073741824}
}

func assertToggleStillReserved(t *testing.T, ctx context.Context, f *toggleFixture, op string) {
	t.Helper()
	sub, err := db.GetSubscriptionByID(ctx, f.subID)
	if err != nil || sub == nil || sub.Status != db.SubscriptionStatusReconciliation || !sub.IsActive || sub.DesiredIsActive == nil || *sub.DesiredIsActive {
		t.Fatalf("toggle should preserve coherent observed state and desired target: sub=%#v err=%v", sub, err)
	}
	var count int
	var recordStatus string
	var desiredUUID, desiredSubID string
	if err := db.Pool.QueryRow(ctx, `SELECT count(*), min(status), min(desired_state->>'client_uuid'), min(desired_state->>'panel_sub_id') FROM reconciliation_records WHERE operation_key=$1 AND subscription_id=$2 AND kind='subscription_update_db_failed'`, op, f.subID).Scan(&count, &recordStatus, &desiredUUID, &desiredSubID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || desiredUUID != f.uuid || desiredSubID != f.panelSubID {
		t.Fatalf("toggle lacks one executable strongly identified work item: count=%d status=%q uuid=%q sub_id=%q", count, recordStatus, desiredUUID, desiredSubID)
	}
}

func processToggle(t *testing.T, ctx context.Context, worker *Processor) {
	t.Helper()
	worker.BatchSize = 100
	if _, err := worker.ProcessOnce(ctx); err != nil {
		t.Fatalf("process toggle reconciliation: %v", err)
	}
}

func TestResellerToggleReconciliationSurvivesWorkerRestartAndDuplicateCallback(t *testing.T) {
	ctx := setupIsolatedTestDBForReconcile(t)
	f := newToggleFixture(t, ctx)
	op := fmt.Sprintf("toggle-restart:%d", time.Now().UnixNano())
	if duplicate, err := queueDisableToggle(t, ctx, f, op); err != nil || duplicate {
		t.Fatalf("queue initial toggle: duplicate=%t err=%v", duplicate, err)
	}
	assertToggleStillReserved(t, ctx, f, op)
	if duplicate, err := queueDisableToggle(t, ctx, f, op); err != nil || !duplicate {
		t.Fatalf("duplicate callback must reuse durable toggle: duplicate=%t err=%v", duplicate, err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_records WHERE operation_key=$1`, op).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate callback created extra work: count=%d err=%v", count, err)
	}

	// A new worker process handles the already committed queue record.
	panel := &mockXUI{client: remoteToggleClient(f), persistUpdate: true}
	processToggle(t, ctx, NewProcessor("toggle_after_restart", panel))
	if panel.updateCalls != 1 || panel.client.Enable {
		t.Fatalf("restart worker did not apply and verify toggle: updates=%d remote=%+v", panel.updateCalls, panel.client)
	}
	sub, err := db.GetSubscriptionByID(ctx, f.subID)
	if err != nil || sub == nil || sub.Status != db.SubscriptionStatusDisabled || sub.IsActive || sub.DesiredIsActive != nil {
		t.Fatalf("toggle worker produced incoherent final lifecycle state: sub=%#v err=%v", sub, err)
	}
	var status string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM reconciliation_records WHERE operation_key=$1`, op).Scan(&status); err != nil || status != db.ReconciliationStatusResolvedVerified {
		t.Fatalf("toggle record not verified/resolved: status=%q err=%v", status, err)
	}
}

func TestResellerToggleDefinitiveRemoteFailureKeepsExecutableWork(t *testing.T) {
	ctx := setupIsolatedTestDBForReconcile(t)
	f := newToggleFixture(t, ctx)
	op := fmt.Sprintf("toggle-definitive-failure:%d", time.Now().UnixNano())
	if _, err := queueDisableToggle(t, ctx, f, op); err != nil {
		t.Fatal(err)
	}
	panel := &mockXUI{client: remoteToggleClient(f), updateRes: xui.WriteResult{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("remote rejected update")}}
	processToggle(t, ctx, NewProcessor("toggle_definitive_failure", panel))
	assertToggleStillReserved(t, ctx, f, op)
	var status string
	if err := db.Pool.QueryRow(ctx, `SELECT status FROM reconciliation_records WHERE operation_key=$1`, op).Scan(&status); err != nil || status != "retryable" {
		t.Fatalf("definitive remote failure must leave retryable executable work: status=%q err=%v", status, err)
	}
}

func TestResellerToggleTimeoutAfterRemoteCommitConvergesOnRetry(t *testing.T) {
	ctx := setupIsolatedTestDBForReconcile(t)
	f := newToggleFixture(t, ctx)
	op := fmt.Sprintf("toggle-timeout-commit:%d", time.Now().UnixNano())
	if _, err := queueDisableToggle(t, ctx, f, op); err != nil {
		t.Fatal(err)
	}
	panel := &mockXUI{client: remoteToggleClient(f), persistUpdate: true, updateRes: xui.WriteResult{Outcome: xui.WriteUnknown, Err: errors.New("response timed out after remote commit")}}
	processToggle(t, ctx, NewProcessor("toggle_timeout_first", panel))
	assertToggleStillReserved(t, ctx, f, op)
	if panel.client.Enable || panel.updateCalls != 1 {
		t.Fatalf("mock did not represent committed timeout: remote=%+v updates=%d", panel.client, panel.updateCalls)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET next_attempt_at=NOW() WHERE operation_key=$1`, op); err != nil {
		t.Fatal(err)
	}
	processToggle(t, ctx, NewProcessor("toggle_timeout_restart", panel))
	sub, err := db.GetSubscriptionByID(ctx, f.subID)
	if err != nil || sub == nil || sub.Status != db.SubscriptionStatusDisabled || sub.IsActive || sub.DesiredIsActive != nil {
		t.Fatalf("worker did not converge committed timeout: sub=%#v err=%v", sub, err)
	}
	if panel.updateCalls != 1 {
		t.Fatalf("retry should verify remote state instead of repeating write: updates=%d", panel.updateCalls)
	}
}

func TestResellerToggleRemoteSuccessWithDBCommitFailureConvergesAfterRestart(t *testing.T) {
	ctx := setupIsolatedTestDBForReconcile(t)
	f := newToggleFixture(t, ctx)
	op := fmt.Sprintf("toggle-db-failure:%d", time.Now().UnixNano())
	if _, err := queueDisableToggle(t, ctx, f, op); err != nil {
		t.Fatal(err)
	}
	panel := &mockXUI{client: remoteToggleClient(f), persistUpdate: true}
	first := NewProcessor("toggle_db_failure_first", panel)
	first.UpdateSubscriptionFn = func(context.Context, *db.Subscription) error { return errors.New("injected DB commit failure") }
	processToggle(t, ctx, first)
	assertToggleStillReserved(t, ctx, f, op)
	if panel.client.Enable || panel.updateCalls != 1 {
		t.Fatalf("remote write was not committed before DB failure simulation: remote=%+v updates=%d", panel.client, panel.updateCalls)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE reconciliation_records SET next_attempt_at=NOW() WHERE operation_key=$1`, op); err != nil {
		t.Fatal(err)
	}
	processToggle(t, ctx, NewProcessor("toggle_db_failure_restart", panel))
	sub, err := db.GetSubscriptionByID(ctx, f.subID)
	if err != nil || sub == nil || sub.Status != db.SubscriptionStatusDisabled || sub.IsActive || sub.DesiredIsActive != nil {
		t.Fatalf("worker did not converge DB failure after remote success: sub=%#v err=%v", sub, err)
	}
	if panel.updateCalls != 1 {
		t.Fatalf("worker repeated remote mutation instead of reading its committed state: updates=%d", panel.updateCalls)
	}
}
