package db

import (
	"context"
	"encoding/json"
)

// ReconciliationRecord is a durable marker for a cross-system operation whose
// remote and database outcomes could not be made atomic.
type ReconciliationRecord struct {
	ID                int64
	OperationKey      string
	Kind              string
	UserID            *int64
	SubscriptionID    *int64
	PurchaseRequestID *int64
	DesiredState      map[string]any
	ObservedState     map[string]any
	Status            string
	ErrorMessage      string
}

func CreateReconciliationRecord(ctx context.Context, record *ReconciliationRecord) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	desired, err := json.Marshal(record.DesiredState)
	if err != nil {
		return err
	}
	observed, err := json.Marshal(record.ObservedState)
	if err != nil {
		return err
	}
	if record.Status == "" {
		record.Status = "pending"
	}
	err = Pool.QueryRow(ctx, `
		INSERT INTO reconciliation_records
			(operation_key, kind, user_id, subscription_id, purchase_request_id, desired_state, observed_state, status, error_message)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9)
		ON CONFLICT (operation_key) DO UPDATE SET
			desired_state = EXCLUDED.desired_state,
			observed_state = EXCLUDED.observed_state,
			status = EXCLUDED.status,
			error_message = EXCLUDED.error_message,
			updated_at = NOW()
		RETURNING id
	`, record.OperationKey, record.Kind, record.UserID, record.SubscriptionID, record.PurchaseRequestID, desired, observed, record.Status, record.ErrorMessage).Scan(&record.ID)
	return err
}
