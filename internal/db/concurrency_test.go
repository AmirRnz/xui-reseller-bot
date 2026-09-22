package db

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func TestConcurrencyStress(t *testing.T) {
	ctx := setupTestDB(t)

	// Clean up settings and subscriptions after test
	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_settings WHERE key LIKE 'stress_key_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM subscriptions WHERE client_email LIKE 'stress_email_%'")
			_, _ = Pool.Exec(ctx, "DELETE FROM bot_users WHERE telegram_id >= 900000000")
		}
	}()

	// Create a test user for foreign key constraints
	var userID int64
	err := Pool.QueryRow(ctx, `
		INSERT INTO bot_users (telegram_id, username, first_name, last_name)
		VALUES (900000000, 'stress_user', 'Stress', 'User')
		ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		RETURNING id
	`).Scan(&userID)
	if err != nil {
		t.Fatalf("failed to create stress test user: %v", err)
	}

	const numGoroutines = 30
	const iterationsPerGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errChan := make(chan error, numGoroutines*iterationsPerGoroutine*10)

	for i := 0; i < numGoroutines; i++ {
		go func(routineID int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(routineID)))

			for j := 0; j < iterationsPerGoroutine; j++ {
				// 1. Stress Settings Repo (shared key and unique key)
				sharedKey := "stress_key_shared"
				uniqueKey := fmt.Sprintf("stress_key_%d_%d", routineID, j)
				valStr := fmt.Sprintf("val_%d", r.Intn(1000))

				// Set/Get Shared Key
				if err := SetSetting(ctx, sharedKey, valStr); err != nil {
					errChan <- fmt.Errorf("routine %d: SetSetting shared: %w", routineID, err)
				}
				if _, err := GetSetting(ctx, sharedKey); err != nil {
					errChan <- fmt.Errorf("routine %d: GetSetting shared: %w", routineID, err)
				}

				// Set/Get Unique Key
				if err := SetSetting(ctx, uniqueKey, valStr); err != nil {
					errChan <- fmt.Errorf("routine %d: SetSetting unique: %w", routineID, err)
				}
				if gotVal, err := GetSetting(ctx, uniqueKey); err != nil {
					errChan <- fmt.Errorf("routine %d: GetSetting unique: %w", routineID, err)
				} else if gotVal != valStr {
					errChan <- fmt.Errorf("routine %d: GetSetting unique mismatch: got %q, expected %q", routineID, gotVal, valStr)
				}

				// 2. Stress Subscriptions Repo
				planID := r.Intn(1000)
				expireTime := time.Now().Add(time.Duration(r.Intn(100)) * time.Hour).Unix()
				sub := &Subscription{
					UserID:      userID,
					PlanID:      &planID,
					PlanType:    "paid",
					ClientEmail: fmt.Sprintf("stress_email_%d_%d@example.com", routineID, j),
					SubID:       fmt.Sprintf("sub_%d_%d", routineID, j),
					DisplayName: fmt.Sprintf("Stress Sub %d %d", routineID, j),
					IPLimit:     r.Intn(5) + 1,
					ExpireTime:  &expireTime,
					IsActive:    true,
					Status:      SubscriptionStatusActive,
				}

				// Create
				if err := CreateSubscription(ctx, sub); err != nil {
					errChan <- fmt.Errorf("routine %d: CreateSubscription: %w", routineID, err)
					continue
				}

				// Get by ID
				gotSub, err := GetSubscriptionByID(ctx, sub.ID)
				if err != nil {
					errChan <- fmt.Errorf("routine %d: GetSubscriptionByID: %w", routineID, err)
				} else if gotSub == nil {
					errChan <- fmt.Errorf("routine %d: GetSubscriptionByID: not found", routineID)
				}

				// Get by Email
				gotSubEmail, err := GetSubscriptionByEmail(ctx, sub.ClientEmail)
				if err != nil {
					errChan <- fmt.Errorf("routine %d: GetSubscriptionByEmail: %w", routineID, err)
				} else if gotSubEmail == nil {
					errChan <- fmt.Errorf("routine %d: GetSubscriptionByEmail: not found", routineID)
				}

				// Get Active by User ID
				activeSubs, err := GetActiveSubscriptionsByUserID(ctx, userID)
				if err != nil {
					errChan <- fmt.Errorf("routine %d: GetActiveSubscriptionsByUserID: %w", routineID, err)
				} else if len(activeSubs) == 0 {
					errChan <- fmt.Errorf("routine %d: GetActiveSubscriptionsByUserID: empty list", routineID)
				}

				// Update
				sub.IPLimit = sub.IPLimit + 1
				sub.IsActive = false
				if err := UpdateSubscription(ctx, sub); err != nil {
					errChan <- fmt.Errorf("routine %d: UpdateSubscription: %w", routineID, err)
				}

				// Delete
				if err := DeleteSubscription(ctx, sub.ID); err != nil {
					errChan <- fmt.Errorf("routine %d: DeleteSubscription: %w", routineID, err)
				}

				// Verify the row remains auditable after the non-destructive delete.
				gotSubDeleted, err := GetSubscriptionByID(ctx, sub.ID)
				if err != nil {
					errChan <- fmt.Errorf("routine %d: Verify Deleted: %w", routineID, err)
				} else if gotSubDeleted == nil || gotSubDeleted.Status != SubscriptionStatusDeleted {
					errChan <- fmt.Errorf("routine %d: Verify Deleted: subscription was not marked deleted", routineID)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	hasErrors := false
	for err := range errChan {
		t.Errorf("Concurrency error: %v", err)
		hasErrors = true
	}

	if !hasErrors {
		t.Log("Concurrency stress test completed successfully without errors!")
	}
}

func TestReconciliationConcurrencyAndTerminalStates(t *testing.T) {
	ctx := setupTestDB(t)

	defer func() {
		if Pool != nil {
			_, _ = Pool.Exec(ctx, "DELETE FROM reconciliation_records WHERE operation_key LIKE 'test_cas_%'")
		}
	}()

	// 1. Worker claims record, admin manually closes it, worker later tries to resolve -> admin close remains final
	t.Run("worker claims record, admin manually closes it, stale worker cannot resolve", func(t *testing.T) {
		opKey := fmt.Sprintf("test_cas_close_%d", time.Now().UnixNano())
		rec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"data": "1"},
			Status:       ReconciliationStatusPending,
		}
		if err := CreateReconciliationRecord(ctx, rec); err != nil {
			t.Fatalf("failed to create record: %v", err)
		}

		claimed, err := ClaimPendingReconciliationRecords(ctx, "worker_1", 10)
		if err != nil {
			t.Fatalf("failed to claim record: %v", err)
		}
		var myRec *ReconciliationRecord
		for _, r := range claimed {
			if r.OperationKey == opKey {
				myRec = r
				break
			}
		}
		if myRec == nil {
			t.Fatalf("record was not claimed by worker_1")
		}

		// Admin manually closes record
		if err := ManuallyCloseReconciliationRecord(ctx, myRec.ID, 999, "admin closed during review"); err != nil {
			t.Fatalf("failed to manually close record: %v", err)
		}

		// Stale worker_1 tries to resolve
		resErr := ResolveReconciliationRecord(ctx, myRec.ID, "worker_1", myRec.Status, myRec.Version, "worker resolution")
		if resErr == nil {
			t.Fatalf("expected error when stale worker resolves manually closed record, got nil")
		}

		// Verify record is still manually closed
		finalRec, err := GetReconciliationRecordByID(ctx, myRec.ID)
		if err != nil || finalRec == nil {
			t.Fatalf("failed to get record: %v", err)
		}
		if finalRec.Status != ReconciliationStatusManuallyClosed {
			t.Fatalf("expected status %s, got %s", ReconciliationStatusManuallyClosed, finalRec.Status)
		}
	})

	// 2. Worker claims record, admin moves it to manual review -> stale worker cannot overwrite it
	t.Run("worker claims record, admin moves to manual review, stale worker cannot overwrite", func(t *testing.T) {
		opKey := fmt.Sprintf("test_cas_review_%d", time.Now().UnixNano())
		rec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"data": "2"},
			Status:       ReconciliationStatusPending,
		}
		if err := CreateReconciliationRecord(ctx, rec); err != nil {
			t.Fatalf("failed to create record: %v", err)
		}

		claimed, err := ClaimPendingReconciliationRecords(ctx, "worker_1", 10)
		if err != nil {
			t.Fatalf("failed to claim record: %v", err)
		}
		var myRec *ReconciliationRecord
		for _, r := range claimed {
			if r.OperationKey == opKey {
				myRec = r
				break
			}
		}
		if myRec == nil {
			t.Fatalf("record was not claimed by worker_1")
		}

		// Admin moves to manual review
		if err := MarkReconciliationManualReview(ctx, myRec.ID, "", "", 0, "admin flagged for manual review"); err != nil {
			t.Fatalf("failed to mark manual review: %v", err)
		}

		// Stale worker_1 tries to resolve
		resErr := ResolveReconciliationRecord(ctx, myRec.ID, "worker_1", myRec.Status, myRec.Version, "worker resolution")
		if resErr == nil {
			t.Fatalf("expected error when stale worker resolves record moved to manual review, got nil")
		}

		// Verify record is still in manual_review
		finalRec, err := GetReconciliationRecordByID(ctx, myRec.ID)
		if err != nil || finalRec == nil {
			t.Fatalf("failed to get record: %v", err)
		}
		if finalRec.Status != ReconciliationStatusManualReview {
			t.Fatalf("expected status %s, got %s", ReconciliationStatusManualReview, finalRec.Status)
		}
	})

	// 3. Two workers cannot both successfully complete the same claim
	t.Run("two workers cannot both successfully complete the same claim", func(t *testing.T) {
		opKey := fmt.Sprintf("test_cas_two_workers_%d", time.Now().UnixNano())
		rec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"data": "3"},
			Status:       ReconciliationStatusPending,
		}
		if err := CreateReconciliationRecord(ctx, rec); err != nil {
			t.Fatalf("failed to create record: %v", err)
		}

		claimed, err := ClaimPendingReconciliationRecords(ctx, "worker_A", 10)
		if err != nil {
			t.Fatalf("failed to claim: %v", err)
		}
		var myRec *ReconciliationRecord
		for _, r := range claimed {
			if r.OperationKey == opKey {
				myRec = r
				break
			}
		}
		if myRec == nil {
			t.Fatalf("record not claimed by worker_A")
		}

		// worker_B tries to resolve worker_A's lease
		errB := ResolveReconciliationRecord(ctx, myRec.ID, "worker_B", myRec.Status, myRec.Version, "resolved by B")
		if errB == nil {
			t.Fatalf("worker_B should not be able to resolve record claimed by worker_A")
		}

		// worker_A successfully resolves
		errA := ResolveReconciliationRecord(ctx, myRec.ID, "worker_A", myRec.Status, myRec.Version, "resolved by A")
		if errA != nil {
			t.Fatalf("worker_A should be able to resolve own lease, got: %v", errA)
		}

		// worker_A tries to resolve again (now terminal)
		errA2 := ResolveReconciliationRecord(ctx, myRec.ID, "worker_A", myRec.Status, myRec.Version, "resolved by A again")
		if errA2 == nil {
			t.Fatalf("worker_A should not be able to resolve an already terminal record")
		}
	})

	// 4. Retry reset cannot reopen a terminal record
	t.Run("retry reset cannot reopen a terminal record", func(t *testing.T) {
		opKey := fmt.Sprintf("test_cas_reset_terminal_%d", time.Now().UnixNano())
		rec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"data": "4"},
			Status:       ReconciliationStatusResolvedVerified,
		}
		if err := CreateReconciliationRecord(ctx, rec); err != nil {
			t.Fatalf("failed to create record: %v", err)
		}

		err := ResetReconciliationForRetry(ctx, rec.ID)
		if err == nil || err != ErrReconciliationNotRetryable {
			t.Fatalf("expected ErrReconciliationNotRetryable, got: %v", err)
		}

		finalRec, _ := GetReconciliationRecordByID(ctx, rec.ID)
		if finalRec.Status != ReconciliationStatusResolvedVerified {
			t.Fatalf("expected status to remain %s, got: %s", ReconciliationStatusResolvedVerified, finalRec.Status)
		}
	})

	// 5. Ordinary CreateReconciliationRecord upserts still cannot reopen terminal records
	t.Run("CreateReconciliationRecord upsert cannot reopen terminal records", func(t *testing.T) {
		opKey := fmt.Sprintf("test_cas_upsert_terminal_%d", time.Now().UnixNano())
		rec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"initial": "true"},
			Status:       ReconciliationStatusManuallyClosed,
		}
		if err := CreateReconciliationRecord(ctx, rec); err != nil {
			t.Fatalf("failed to create record: %v", err)
		}

		// Try to upsert with pending status and different desired state
		upsertRec := &ReconciliationRecord{
			OperationKey: opKey,
			Kind:         "test_kind",
			DesiredState: map[string]any{"initial": "false"},
			Status:       ReconciliationStatusPending,
		}
		if err := CreateReconciliationRecord(ctx, upsertRec); err != nil {
			t.Fatalf("upsert should succeed without error: %v", err)
		}

		finalRec, err := GetReconciliationRecordByID(ctx, rec.ID)
		if err != nil || finalRec == nil {
			t.Fatalf("failed to get record: %v", err)
		}
		if finalRec.Status != ReconciliationStatusManuallyClosed {
			t.Fatalf("expected status to remain %s, got: %s", ReconciliationStatusManuallyClosed, finalRec.Status)
		}
		if fmt.Sprintf("%v", finalRec.DesiredState["initial"]) != "true" {
			t.Fatalf("expected desired_state to be preserved on terminal record")
		}
	})
}
