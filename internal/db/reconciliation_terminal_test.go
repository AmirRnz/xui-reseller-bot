package db

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestPendingRefundRequiresLedgerProofOrExplicitAuditedWaiver(t *testing.T) {
	ctx := setupTestDB(t)
	const telegramID int64 = 999999961
	opKey := "test_pending_refund_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM reconciliation_records WHERE operation_key = $1", opKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = $1", telegramID)
		}
	}()
	var userID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username) VALUES ($1, 'refund_audit_test') RETURNING id`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := &ReconciliationRecord{
		OperationKey:  opKey,
		Kind:          "pending_refund",
		UserID:        &userID,
		DesiredState:  map[string]any{"user_id": userID, "amount": int64(8765), "operation_key": opKey + ":credit"},
		ObservedState: map[string]any{},
		Status:        ReconciliationStatusPendingRefund,
	}
	if err := CreateReconciliationRecord(ctx, rec); err != nil {
		t.Fatalf("create reconciliation record: %v", err)
	}
	if err := ManuallyCloseReconciliationRecord(ctx, rec.ID, 12345, "looks resolved"); !errors.Is(err, ErrFinancialWaiverRequired) {
		t.Fatalf("generic close error = %v, want ErrFinancialWaiverRequired", err)
	}
	if err := ResolvePendingRefundVerified(ctx, rec.ID, 12345); !errors.Is(err, ErrNoVerifiedFinancialEffect) {
		t.Fatalf("verified resolution error = %v, want ErrNoVerifiedFinancialEffect", err)
	}
	if err := ManuallyWaiveReconciliationRecord(ctx, rec.ID, 12345, "refund intentionally abandoned"); err != nil {
		t.Fatalf("explicit waiver: %v", err)
	}
	got, err := GetReconciliationRecordByID(ctx, rec.ID)
	if err != nil || got == nil {
		t.Fatalf("read waived record: record=%#v err=%v", got, err)
	}
	if got.Status != ReconciliationStatusManualWaiver || got.ManualAdminID == nil || *got.ManualAdminID != 12345 || got.ManualActionAt == nil || got.ManualActionReason != "refund intentionally abandoned" || got.ManualActionAmount == nil || *got.ManualActionAmount != 8765 || got.ManualActionOperationKey != opKey+":credit" {
		t.Fatalf("waiver audit is incomplete or indistinct: %+v", got)
	}
}
