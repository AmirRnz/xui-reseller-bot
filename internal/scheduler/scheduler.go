package scheduler

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/services/outbox"
)

var schedulerConfig *config.Config

func Start(ctx context.Context, cfg *config.Config) {
	schedulerConfig = cfg
	go run(ctx)
}

func run(ctx context.Context) {
	// Check if the daily scheduler check has already run today (storing last_scheduler_run in DB settings).
	// If not, run immediately instead of waiting for midnight.
	checkAndRunScheduler(ctx)

	for {
		nextRun := nextMidnightUTC()
		log.Printf("Scheduler: next run at %s UTC (in %s)", nextRun.Format("2006-01-02 15:04:05"), time.Until(nextRun).Round(time.Second))
		select {
		case <-ctx.Done():
			log.Println("Scheduler: stopping run loop due to context cancel")
			return
		case <-time.After(time.Until(nextRun)):
			log.Println("Running daily scheduler tasks...")
			if err := processExpiringSubscriptions(ctx); err != nil {
				log.Printf("Scheduler: processExpiringSubscriptions failed: %v", err)
			} else {
				_ = db.SetSetting(ctx, "last_scheduler_run", time.Now().UTC().Format("2006-01-02"))
			}
		}
	}
}

func checkAndRunScheduler(ctx context.Context) {
	todayStr := time.Now().UTC().Format("2006-01-02")
	lastRun, err := db.GetSetting(ctx, "last_scheduler_run")
	if err != nil || lastRun != todayStr {
		log.Println("Scheduler: Check has not run today yet. Running now...")
		if err := processExpiringSubscriptions(ctx); err != nil {
			log.Printf("Scheduler: initial check failed: %v", err)
		} else {
			_ = db.SetSetting(ctx, "last_scheduler_run", todayStr)
		}
	} else {
		log.Println("Scheduler: Check already ran today.")
	}
}

func nextMidnightUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}

func processExpiringSubscriptions(ctx context.Context) error {
	var overallErr error
	for _, days := range notificationDays(ctx) {
		subs, err := db.GetExpiringSubscriptions(ctx, days)
		if err != nil {
			log.Printf("Scheduler: Error getting expiring subscriptions: %v", err)
			overallErr = err
			continue
		}

		for _, sub := range subs {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			if err := outbox.EnqueueExpiryNotification(ctx, int64(sub.ID), sub.UserID, days, sub.ClientEmail, sub.EndDate.Format("2006-01-02")); err != nil {
				log.Printf("Scheduler: failed to enqueue notification for sub %d: %v", sub.ID, err)
				overallErr = err
			}
		}
	}
	return overallErr
}

func notificationDays(ctx context.Context) []int {
	_ = schedulerConfig
	value, _ := db.GetSetting(ctx, "expiry_notify_days")
	if strings.TrimSpace(value) == "" {
		return []int{3}
	}
	parts := strings.Split(value, ",")
	var days []int
	for _, part := range parts {
		day, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && day > 0 {
			days = append(days, day)
		}
	}
	if len(days) == 0 {
		return []int{3}
	}
	return days
}
