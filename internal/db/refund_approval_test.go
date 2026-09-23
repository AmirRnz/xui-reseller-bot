package db

import (
	"strconv"
	"testing"
	"time"
)

func TestLegacyZeroSuggestionRefundApprovalIsEditableAndIdempotent(t *testing.T) {
	ctx := setupTestDB(t)
	const telegramID int64 = 999999962
	opKey := "test_legacy_refund_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	var req *RefundRequest
	defer func() {
		if Pool != nil {
			if req != nil {
				_, _ = Pool.Exec(ctx, "DELETE FROM transactions WHERE operation_key = $1", "refund_approval:"+strconv.FormatInt(req.ID, 10))
			}
			_, _ = Pool.Exec(ctx, "DELETE FROM refund_requests WHERE operation_key = $1", opKey)
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id = $1", telegramID)
		}
	}()
	var userID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username) VALUES ($1, 'legacy_refund_test') RETURNING id`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	var balanceBefore int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balanceBefore); err != nil {
		t.Fatalf("read initial balance: %v", err)
	}
	req = &RefundRequest{UserID: userID, CalculatedAmount: 0, Status: "pending", OperationKey: opKey}
	if err := CreateRefundRequest(ctx, req); err != nil {
		t.Fatalf("create legacy manual-review refund: %v", err)
	}
	const approvedAmount int64 = 43210
	const adminID int64 = 96937669
	approved, err := ApproveRefundRequestAndCredit(ctx, req.ID, adminID, approvedAmount, "legacy service reviewed against submitted proof")
	if err != nil || approved == nil {
		t.Fatalf("approve manual refund: req=%#v err=%v", approved, err)
	}
	if approved.CalculatedAmount != 0 || approved.ApprovedAmount == nil || *approved.ApprovedAmount != approvedAmount || approved.AdminID == nil || *approved.AdminID != adminID || approved.ApprovedAt == nil || approved.AuditNote != "legacy service reviewed against submitted proof" {
		t.Fatalf("approval audit or manually entered amount missing: %+v", approved)
	}
	again, err := ApproveRefundRequestAndCredit(ctx, req.ID, adminID, approvedAmount, "duplicate callback")
	if err != nil || again != nil {
		t.Fatalf("duplicate approval should be a no-op: req=%#v err=%v", again, err)
	}
	var balanceAfter, ledgerCount int64
	if err := Pool.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&balanceAfter); err != nil {
		t.Fatalf("read final balance: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM transactions WHERE operation_key = $1 AND user_id = $2 AND amount = $3 AND type = 'credit' AND status = 'completed'`, "refund_approval:"+strconv.FormatInt(req.ID, 10), userID, approvedAmount).Scan(&ledgerCount); err != nil {
		t.Fatalf("count refund credits: %v", err)
	}
	if balanceAfter-balanceBefore != approvedAmount || ledgerCount != 1 {
		t.Fatalf("refund economic effect duplicated or missing: balance delta=%d ledger rows=%d", balanceAfter-balanceBefore, ledgerCount)
	}
}
