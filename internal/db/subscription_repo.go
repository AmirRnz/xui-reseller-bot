package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func GetSubscriptionsByUserID(ctx context.Context, userID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func GetActiveSubscriptionsByUserID(ctx context.Context, userID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE user_id = $1 AND is_active = TRUE ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func GetSubscriptionByID(ctx context.Context, id int) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE id = $1`, id)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func GetSubscriptionByEmail(ctx context.Context, email string) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE client_email = $1`, email)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func CreateSubscription(ctx context.Context, s *Subscription) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if s.Status == "" {
		s.Status = "active"
	}
	if s.DisplayName == "" {
		s.DisplayName = s.ClientEmail
	}
	if s.IPLimit == 0 {
		s.IPLimit = 1
	}
	if s.StartDate.IsZero() {
		s.StartDate = time.Now().UTC()
	}
	if s.EndDate.IsZero() && s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	var endDate *time.Time
	if !s.EndDate.IsZero() {
		endDate = &s.EndDate
	}

	query := `
		INSERT INTO subscriptions (user_id, plan_id, client_email, client_uuid, sub_id, status, plan_type, display_name, ip_limit, expire_time, is_active, start_date, end_date, traffic_limit_bytes, desired_ip_limit, desired_expire_time, desired_is_active, reconciliation_note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id, created_at, updated_at
	`
	return Pool.QueryRow(ctx, query,
		s.UserID, s.PlanID, s.ClientEmail, s.ClientUUID, s.SubID, s.Status, s.PlanType, s.DisplayName, s.IPLimit, s.ExpireTime, s.IsActive, s.StartDate, endDate, s.TrafficLimitBytes, s.DesiredIPLimit, s.DesiredExpireTime, s.DesiredIsActive, s.ReconciliationNote,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

func UpdateSubscriptionStatus(ctx context.Context, id int, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	isActive := status == "active"
	_, err := Pool.Exec(ctx, `UPDATE subscriptions SET status = $1, is_active = $2, end_date = CASE WHEN $1 = 'cancelled' THEN COALESCE(end_date, NOW()) ELSE end_date END, updated_at = NOW() WHERE id = $3`, status, isActive, id)
	return err
}

func UpdateSubscription(ctx context.Context, s *Subscription) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if s.EndDate.IsZero() && s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	var endDate *time.Time
	if !s.EndDate.IsZero() {
		endDate = &s.EndDate
	}
	_, err := Pool.Exec(ctx, `
		UPDATE subscriptions
		SET plan_id = $1, client_email = $2, client_uuid = $3, sub_id = $4, status = $5, plan_type = $6,
			display_name = $7, ip_limit = $8, expire_time = $9, is_active = $10, end_date = $11, traffic_limit_bytes = $12,
			desired_ip_limit = $13, desired_expire_time = $14, desired_is_active = $15, reconciliation_note = $16, updated_at = NOW()
		WHERE id = $17
	`, s.PlanID, s.ClientEmail, s.ClientUUID, s.SubID, s.Status, s.PlanType, s.DisplayName, s.IPLimit, s.ExpireTime, s.IsActive, endDate, s.TrafficLimitBytes, s.DesiredIPLimit, s.DesiredExpireTime, s.DesiredIsActive, s.ReconciliationNote, s.ID)
	return err
}

// CancelSubscriptionWithRefund preserves the commercial row and creates its
// refund request in one transaction. The key makes repeated callbacks safe.
func CancelSubscriptionWithRefund(ctx context.Context, subscriptionID int, userID int64, amount int64, operationKey string) (*RefundRequest, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var ownerID int64
	if err := tx.QueryRow(ctx, `
		UPDATE subscriptions
		SET status = 'cancelled', is_active = FALSE, end_date = COALESCE(end_date, NOW()), updated_at = NOW()
		WHERE id = $1 AND user_id = $2
		RETURNING user_id
	`, subscriptionID, userID).Scan(&ownerID); err != nil {
		return nil, err
	}

	request := &RefundRequest{UserID: ownerID, CalculatedAmount: amount, Status: "pending", OperationKey: operationKey}
	if err := tx.QueryRow(ctx, `
		INSERT INTO refund_requests (user_id, subscription_id, calculated_amount, status, operation_key)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (operation_key) DO UPDATE SET updated_at = refund_requests.updated_at
		RETURNING id, user_id, subscription_id, calculated_amount, approved_amount, status, admin_id, COALESCE(operation_key, ''), created_at, updated_at
	`, request.UserID, int64(subscriptionID), amount, request.Status, operationKey).Scan(&request.ID, &request.UserID, &request.SubscriptionID, &request.CalculatedAmount, &request.ApprovedAmount, &request.Status, &request.AdminID, &request.OperationKey, &request.CreatedAt, &request.UpdatedAt); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return request, nil
}

func MarkSubscriptionReconciliationRequired(ctx context.Context, id int, desiredIPLimit *int, desiredExpireTime *int64, desiredIsActive *bool, note string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	_, err := Pool.Exec(ctx, `
		UPDATE subscriptions
		SET status = 'reconciliation_required', desired_ip_limit = $1, desired_expire_time = $2,
			desired_is_active = $3, reconciliation_note = $4, updated_at = NOW()
		WHERE id = $5
	`, desiredIPLimit, desiredExpireTime, desiredIsActive, note, id)
	return err
}

func DeleteSubscription(ctx context.Context, id int) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE subscriptions SET status = 'deleted', is_active = FALSE, end_date = COALESCE(end_date, NOW()), updated_at = NOW() WHERE id = $1`, id)
	return err
}

func CountTestSubscriptionsForUser(ctx context.Context, userID int64) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = $1 AND plan_type = 'test'`, userID).Scan(&count)
	return count, err
}

func GetTestUsageToday(ctx context.Context, userID int64, planID int64) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, `
		SELECT used_count FROM test_usage
		WHERE user_id = $1 AND plan_id = $2 AND reset_date = CURRENT_DATE
	`, userID, planID).Scan(&count)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return count, nil
}

func IncrementTestUsageToday(ctx context.Context, userID int64, planID int64, increment int) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if increment <= 0 {
		increment = 1
	}
	_, err := Pool.Exec(ctx, `
		INSERT INTO test_usage (user_id, plan_id, used_count, reset_date)
		VALUES ($1, $2, $3, CURRENT_DATE)
		ON CONFLICT (user_id, plan_id, reset_date)
		DO UPDATE SET used_count = test_usage.used_count + EXCLUDED.used_count, updated_at = NOW()
	`, userID, planID, increment)
	return err
}

func GetActiveSubscriptionsCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM subscriptions WHERE is_active = TRUE").Scan(&count)
	return count, err
}

func GetExpiringSubscriptions(ctx context.Context, days int) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	targetDay := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	startOfDay := time.Date(targetDay.Year(), targetDay.Month(), targetDay.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)

	rows, err := Pool.Query(ctx, subscriptionSelect()+`
		WHERE is_active = TRUE
		  AND end_date IS NOT NULL
		  AND end_date >= $1 
		  AND end_date < $2
	`, startOfDay, endOfDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func subscriptionSelect() string {
	return `SELECT id, user_id, plan_id, client_email, client_uuid, sub_id, status, plan_type, display_name, ip_limit, expire_time, is_active, start_date, end_date, traffic_limit_bytes, desired_ip_limit, desired_expire_time, desired_is_active, reconciliation_note, created_at, updated_at FROM subscriptions`
}

func scanSubscriptionRows(rows pgx.Rows) (*Subscription, error) {
	s := &Subscription{}
	var endDate *time.Time
	err := rows.Scan(&s.ID, &s.UserID, &s.PlanID, &s.ClientEmail, &s.ClientUUID, &s.SubID, &s.Status, &s.PlanType, &s.DisplayName, &s.IPLimit, &s.ExpireTime, &s.IsActive, &s.StartDate, &endDate, &s.TrafficLimitBytes, &s.DesiredIPLimit, &s.DesiredExpireTime, &s.DesiredIsActive, &s.ReconciliationNote, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if endDate != nil {
		s.EndDate = *endDate
	} else if s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	return s, nil
}

// GetTestsCreatedToday returns the number of test subscriptions created today (UTC).
func GetTestsCreatedToday(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, `
		SELECT count(*) FROM subscriptions
		WHERE plan_type = 'test'
		  AND created_at >= (NOW() AT TIME ZONE 'UTC')::DATE
	`).Scan(&count)
	return count, err
}

// GetMonthlyRevenue returns the sum of all debit transactions this calendar month (UTC).
func GetMonthlyRevenue(ctx context.Context) (float64, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var total *float64
	err := Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(ABS(amount)), 0)
		FROM transactions
		WHERE type = 'debit'
		  AND status = 'completed'
		  AND created_at >= DATE_TRUNC('month', NOW() AT TIME ZONE 'UTC')
	`).Scan(&total)
	if err != nil {
		return 0, err
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

func GetActiveSubscriptionsByPlan(ctx context.Context, planType string, planID int64) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE plan_type = $1 AND plan_id = $2 AND is_active = TRUE`, planType, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}

func scanSubscriptionRow(row pgx.Row) (*Subscription, error) {
	s := &Subscription{}
	var endDate *time.Time
	err := row.Scan(&s.ID, &s.UserID, &s.PlanID, &s.ClientEmail, &s.ClientUUID, &s.SubID, &s.Status, &s.PlanType, &s.DisplayName, &s.IPLimit, &s.ExpireTime, &s.IsActive, &s.StartDate, &endDate, &s.TrafficLimitBytes, &s.DesiredIPLimit, &s.DesiredExpireTime, &s.DesiredIsActive, &s.ReconciliationNote, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if endDate != nil {
		s.EndDate = *endDate
	} else if s.ExpireTime != nil && *s.ExpireTime > 0 {
		s.EndDate = time.UnixMilli(*s.ExpireTime)
	}
	return s, nil
}

func GetSubscriptionBySubID(ctx context.Context, subID string) (*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, subscriptionSelect()+` WHERE sub_id = $1`, subID)
	s, err := scanSubscriptionRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func GetActiveSubscriptions(ctx context.Context) ([]*Subscription, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, subscriptionSelect()+` WHERE is_active = TRUE ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*Subscription
	for rows.Next() {
		s, err := scanSubscriptionRows(rows)
		if err != nil {
			return nil, err
		}
		subs = append(subs, s)
	}
	return subs, rows.Err()
}
