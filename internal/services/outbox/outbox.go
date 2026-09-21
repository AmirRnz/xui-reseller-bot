package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/db"
)

type OutboxRecord struct {
	ID               int64          `json:"id"`
	SubscriptionID   int64          `json:"subscription_id"`
	UserID           int64          `json:"user_id"`
	NotificationType string         `json:"notification_type"`
	EffectiveDate    string         `json:"effective_date"` // YYYY-MM-DD
	Channel          string         `json:"channel"`
	Payload          map[string]any `json:"payload"`
	Status           string         `json:"status"`
	AttemptCount     int            `json:"attempt_count"`
	NextAttemptAt    time.Time      `json:"next_attempt_at"`
	LockedAt         *time.Time     `json:"locked_at"`
	LockedBy         *string        `json:"locked_by"`
	SentAt           *time.Time     `json:"sent_at"`
	ErrorMessage     string         `json:"error_message"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type TelegramSender interface {
	Send(to telebot.Recipient, what interface{}, opts ...interface{}) (*telebot.Message, error)
}

func EnqueueExpiryNotification(ctx context.Context, subID, userID int64, daysLeft int, clientEmail, endDate string) error {
	if db.Pool == nil {
		return errors.New("database pool is not initialized")
	}

	effectiveDate := time.Now().UTC().Format("2006-01-02")
	payload := map[string]any{
		"days_left":    daysLeft,
		"client_email": clientEmail,
		"end_date":     endDate,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	notificationType := fmt.Sprintf("expiry_%d_days", daysLeft)

	_, err = db.Pool.Exec(ctx, `
		INSERT INTO notifications_outbox (
			subscription_id, user_id, notification_type, effective_date, channel, payload, status, next_attempt_at
		) VALUES ($1, $2, $3, $4, 'telegram', $5::jsonb, 'pending', NOW())
		ON CONFLICT (subscription_id, notification_type, effective_date, channel) DO NOTHING
	`, subID, userID, notificationType, effectiveDate, payloadBytes)
	return err
}

func ClaimPendingNotifications(ctx context.Context, lockedBy string, limit int) ([]*OutboxRecord, error) {
	if db.Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	rows, err := db.Pool.Query(ctx, `
		WITH claimable AS (
			SELECT id
			FROM notifications_outbox
			WHERE status IN ('pending', 'retryable')
			  AND (next_attempt_at <= NOW() OR next_attempt_at IS NULL)
			  AND (locked_at IS NULL OR locked_at < NOW() - INTERVAL '5 minutes')
			ORDER BY next_attempt_at ASC NULLS FIRST, id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notifications_outbox o
		SET locked_at = NOW(), locked_by = $1, attempt_count = attempt_count + 1, updated_at = NOW()
		FROM claimable
		WHERE o.id = claimable.id
		RETURNING o.id, o.subscription_id, o.user_id, o.notification_type, o.effective_date,
		          o.channel, o.payload, o.status, o.attempt_count, o.next_attempt_at,
		          o.locked_at, o.locked_by, o.sent_at, o.error_message, o.created_at, o.updated_at
	`, lockedBy, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*OutboxRecord
	for rows.Next() {
		r := &OutboxRecord{}
		var payloadBytes []byte
		var effDate time.Time
		err := rows.Scan(
			&r.ID, &r.SubscriptionID, &r.UserID, &r.NotificationType, &effDate,
			&r.Channel, &payloadBytes, &r.Status, &r.AttemptCount, &r.NextAttemptAt,
			&r.LockedAt, &r.LockedBy, &r.SentAt, &r.ErrorMessage, &r.CreatedAt, &r.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		r.EffectiveDate = effDate.Format("2006-01-02")
		_ = json.Unmarshal(payloadBytes, &r.Payload)
		records = append(records, r)
	}
	return records, rows.Err()
}

func MarkSent(ctx context.Context, id int64) error {
	if db.Pool == nil {
		return errors.New("database pool is not initialized")
	}
	_, err := db.Pool.Exec(ctx, `
		UPDATE notifications_outbox
		SET status = 'sent', sent_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW()
		WHERE id = $1
	`, id)
	return err
}

func MarkFailed(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error {
	if db.Pool == nil {
		return errors.New("database pool is not initialized")
	}
	intervalStr := fmt.Sprintf("%d seconds", int(retryAfter.Seconds()))
	status := "retryable"
	if retryAfter == 0 {
		status = "failed"
	}
	_, err := db.Pool.Exec(ctx, `
		UPDATE notifications_outbox
		SET status = $1, error_message = $2, next_attempt_at = NOW() + $3::interval,
		    locked_at = NULL, locked_by = NULL, updated_at = NOW()
		WHERE id = $4
	`, status, errMsg, intervalStr, id)
	return err
}

type Worker struct {
	Sender   TelegramSender
	WorkerID string
}

func NewWorker(sender TelegramSender) *Worker {
	return &Worker{
		Sender:   sender,
		WorkerID: fmt.Sprintf("outbox_%d", time.Now().UnixNano()),
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	records, err := ClaimPendingNotifications(ctx, w.WorkerID, 20)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}

	for _, rec := range records {
		w.dispatchRecord(ctx, rec)
		time.Sleep(35 * time.Millisecond) // Rate limit guard
	}
	return len(records), nil
}

func (w *Worker) dispatchRecord(ctx context.Context, rec *OutboxRecord) {
	if w.Sender == nil {
		_ = MarkFailed(ctx, rec.ID, "telegram sender is nil", 1*time.Minute)
		return
	}

	user, err := db.GetUserByID(ctx, rec.UserID)
	if err != nil || user == nil {
		_ = MarkFailed(ctx, rec.ID, "user not found in db", 0)
		return
	}

	daysLeft := 0
	if val, ok := rec.Payload["days_left"]; ok {
		switch v := val.(type) {
		case float64:
			daysLeft = int(v)
		case int:
			daysLeft = v
		}
	}
	clientEmail := fmt.Sprintf("%v", rec.Payload["client_email"])
	endDate := fmt.Sprintf("%v", rec.Payload["end_date"])

	var msgText strings.Builder
	msgText.WriteString("⚠️ **هشدار انقضای اشتراک**\n\n")
	if daysLeft == 1 {
		msgText.WriteString("سرویس شما **فردا** منقضی خواهد شد.\n")
	} else {
		msgText.WriteString(fmt.Sprintf("سرویس شما تا **%d روز دیگر** منقضی خواهد شد.\n", daysLeft))
	}
	msgText.WriteString(fmt.Sprintf("🔹 نام سرویس: `%s`\n", clientEmail))
	msgText.WriteString(fmt.Sprintf("📅 تاریخ انقضا: %s\n\n", endDate))
	msgText.WriteString("جهت تمدید سرویس می‌توانید از بخش **«سرویس‌های من»** در منوی اصلی ربات اقدام فرمایید.")

	recipient := &telebot.User{ID: user.TelegramID}
	_, sendErr := w.Sender.Send(recipient, msgText.String(), telebot.ModeMarkdown)
	if sendErr != nil {
		errMsg := sendErr.Error()
		log.Printf("[OUTBOX] Failed to send notification %d to user %d: %v", rec.ID, user.TelegramID, sendErr)
		if strings.Contains(strings.ToLower(errMsg), "too many requests") || strings.Contains(strings.ToLower(errMsg), "retry after") {
			_ = MarkFailed(ctx, rec.ID, errMsg, 5*time.Minute)
		} else if rec.AttemptCount >= 5 {
			_ = MarkFailed(ctx, rec.ID, errMsg, 0)
		} else {
			_ = MarkFailed(ctx, rec.ID, errMsg, 10*time.Minute)
		}
		return
	}

	_ = MarkSent(ctx, rec.ID)
	log.Printf("[OUTBOX] Notification %d sent to user %d (sub: %d)", rec.ID, user.TelegramID, rec.SubscriptionID)
}

func (w *Worker) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = w.ProcessOnce(ctx)
			}
		}
	}()
}
