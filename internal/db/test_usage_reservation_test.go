package db

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReserveTestUsageTodayEnforcesConcurrentDailyLimit(t *testing.T) {
	ctx := setupTestDB(t)
	telegramID := time.Now().UnixNano()
	var userID, planID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id) VALUES ($1) RETURNING id`, telegramID).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	defer func() { _, _ = Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID) }()

	planName := fmt.Sprintf("reserve_resell_%d", telegramID)
	if err := Pool.QueryRow(ctx, `INSERT INTO test_plans (name) VALUES ($1) RETURNING id`, planName).Scan(&planID); err != nil {
		t.Fatalf("create test plan: %v", err)
	}
	defer func() { _, _ = Pool.Exec(ctx, `DELETE FROM test_plans WHERE id = $1`, planID) }()

	const (
		attempts = 12
		limit    = 3
	)
	var succeeded atomic.Int32
	var limited atomic.Int32
	dateCh := make(chan time.Time, attempts)
	errCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resetDate, err := ReserveTestUsageToday(ctx, userID, planID, limit)
			switch {
			case err == nil:
				succeeded.Add(1)
				dateCh <- resetDate
			case errors.Is(err, ErrTestUsageLimitReached):
				limited.Add(1)
			default:
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(dateCh)
	close(errCh)
	for err := range errCh {
		t.Errorf("reserve test usage: %v", err)
	}
	if got := succeeded.Load(); got != limit {
		t.Fatalf("successful concurrent claims = %d, want %d", got, limit)
	}
	if got := limited.Load(); got != attempts-limit {
		t.Fatalf("limited concurrent claims = %d, want %d", got, attempts-limit)
	}

	var resetDate time.Time
	for date := range dateCh {
		resetDate = date
		break
	}
	if resetDate.IsZero() {
		t.Fatal("successful reservation returned an empty UTC reset date")
	}
	if err := ReleaseTestUsageToday(ctx, userID, planID, resetDate); err != nil {
		t.Fatalf("release confirmed no-write reservation: %v", err)
	}
	if _, err := ReserveTestUsageToday(ctx, userID, planID, limit); err != nil {
		t.Fatalf("reserve after confirmed release: %v", err)
	}
	used, err := GetTestUsageToday(ctx, userID, planID)
	if err != nil {
		t.Fatalf("read daily usage: %v", err)
	}
	if used != limit {
		t.Fatalf("daily usage = %d, want %d", used, limit)
	}
}
