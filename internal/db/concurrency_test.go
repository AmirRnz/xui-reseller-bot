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

				// Verify Deleted
				gotSubDeleted, err := GetSubscriptionByID(ctx, sub.ID)
				if err != nil {
					errChan <- fmt.Errorf("routine %d: Verify Deleted: %w", routineID, err)
				} else if gotSubDeleted != nil {
					errChan <- fmt.Errorf("routine %d: Verify Deleted: subscription still exists", routineID)
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
