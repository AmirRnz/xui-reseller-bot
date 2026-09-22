package reconcile

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

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
