package sync

import (
	"context"
	"fmt"
	"log"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type XUIClient interface {
	GetClientByEmail(email string) (*xui.XUIClientInfo, error)
	ListClients() ([]xui.XUIClientInfo, error)
}

type DriftClassification string

const (
	DriftMatched             DriftClassification = "MATCHED"
	DriftRemoteMissing       DriftClassification = "REMOTE_MISSING"
	DriftDrifted             DriftClassification = "DRIFTED"
	DriftAmbiguous           DriftClassification = "AMBIGUOUS"
	DriftUnknownRemoteClient DriftClassification = "UNKNOWN_REMOTE_CLIENT"
)

type SyncWorker struct {
	XUI      XUIClient
	Interval time.Duration
}

func NewSyncWorker(xuiClient XUIClient, interval time.Duration) *SyncWorker {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &SyncWorker{
		XUI:      xuiClient,
		Interval: interval,
	}
}

func (w *SyncWorker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(w.Interval)
		defer ticker.Stop()

		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			w.RunSync(ctx)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.RunSync(ctx)
			}
		}
	}()
}

// RunSync checks subscriptions awaiting first connection and syncs activated expiry times.
func (w *SyncWorker) RunSync(ctx context.Context) {
	if w.XUI == nil || db.Pool == nil {
		return
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT id, user_id, client_email, expire_time, end_date, ip_limit, is_active, status
		FROM subscriptions
		WHERE status = 'active'
		  AND (expire_time < 0 OR end_date IS NULL)
		ORDER BY updated_at ASC NULLS FIRST, id ASC
		LIMIT 50
	`)
	if err != nil {
		log.Printf("[SYNC] Failed to query unactivated subscriptions: %v", err)
		return
	}
	defer rows.Close()

	type subTarget struct {
		id         int
		userID     int64
		email      string
		expireTime *int64
		endDate    *time.Time
		ipLimit    int
		isActive   bool
		status     string
	}
	var targets []subTarget
	for rows.Next() {
		var s subTarget
		if err := rows.Scan(&s.id, &s.userID, &s.email, &s.expireTime, &s.endDate, &s.ipLimit, &s.isActive, &s.status); err == nil {
			targets = append(targets, s)
		}
	}
	rows.Close()

	for _, sub := range targets {
		select {
		case <-ctx.Done():
			return
		default:
		}

		remote, err := w.XUI.GetClientByEmail(sub.email)
		if xui.IsNotFound(err) {
			log.Printf("[DRIFT: %s] Subscription %d (%s) absent on 3x-ui panel", DriftRemoteMissing, sub.id, sub.email)
			_ = db.MarkSubscriptionReconciliationRequired(ctx, sub.id, nil, nil, nil, "remote client missing on panel")
			subIDVal := int64(sub.id)
			record := &db.ReconciliationRecord{
				OperationKey:   fmt.Sprintf("drift:missing:%d", sub.id),
				Kind:           "subscription_remote_missing",
				UserID:         &sub.userID,
				SubscriptionID: &subIDVal,
				DesiredState:   map[string]any{"subscription_id": sub.id, "email": sub.email},
				ObservedState:  map[string]any{"status": "not_found"},
				ErrorMessage:   "remote client absent on panel during sync",
			}
			if recErr := db.CreateReconciliationRecord(ctx, record); recErr != nil {
				log.Printf("[SYNC] Failed to create reconciliation record for missing sub %d: %v", sub.id, recErr)
			}
			continue
		}
		if err != nil {
			log.Printf("[DRIFT: %s] Ambiguous error reading client %s: %v", DriftAmbiguous, sub.email, err)
			continue
		}
		if remote == nil {
			continue
		}

		if remote.ExpiryTime > 0 {
			newEndDate := time.UnixMilli(remote.ExpiryTime)
			newExp := remote.ExpiryTime
			_, updateErr := db.Pool.Exec(ctx, `
				UPDATE subscriptions
				SET expire_time = $1, end_date = $2, is_active = $3, updated_at = NOW()
				WHERE id = $4
			`, newExp, newEndDate, remote.Enable, sub.id)
			if updateErr == nil {
				log.Printf("[SYNC] First-connection activation detected for %s (sub %d). EndDate set to %s",
					sub.email, sub.id, newEndDate.Format("2006-01-02 15:04:05"))
			} else {
				log.Printf("[SYNC] Failed to update DB on activation for sub %d: %v", sub.id, updateErr)
				subIDVal := int64(sub.id)
				record := &db.ReconciliationRecord{
					OperationKey:   fmt.Sprintf("drift:db_failed:%d", sub.id),
					Kind:           "subscription_update_db_failed",
					UserID:         &sub.userID,
					SubscriptionID: &subIDVal,
					DesiredState:   map[string]any{"subscription_id": sub.id, "email": sub.email, "expire_time": newExp, "end_date": newEndDate.Format(time.RFC3339), "is_active": remote.Enable},
					ObservedState:  map[string]any{"db_error": updateErr.Error()},
					ErrorMessage:   updateErr.Error(),
				}
				_ = db.CreateReconciliationRecord(ctx, record)
			}
		} else {
			_, _ = db.Pool.Exec(ctx, `UPDATE subscriptions SET updated_at = NOW() WHERE id = $1`, sub.id)
		}
	}
}
