package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReconciliationRecord is a durable marker for a cross-system operation whose
// remote and database outcomes could not be made atomic.  Keeping the desired
// state here allows a worker or operator to repair the operation without
// issuing a second non-idempotent XUI write.
type ReconciliationRecord struct {
	ID                       int64          `json:"id"`
	OperationKey             string         `json:"operation_key"`
	Kind                     string         `json:"kind"`
	UserID                   *int64         `json:"user_id"`
	SubscriptionID           *int64         `json:"subscription_id"`
	PurchaseRequestID        *int64         `json:"purchase_request_id"`
	DesiredState             map[string]any `json:"desired_state"`
	ObservedState            map[string]any `json:"observed_state"`
	Status                   string         `json:"status"`
	ErrorMessage             string         `json:"error_message"`
	AttemptCount             int            `json:"attempt_count"`
	NextAttemptAt            time.Time      `json:"next_attempt_at"`
	LockedAt                 *time.Time     `json:"locked_at"`
	LockedBy                 *string        `json:"locked_by"`
	ResolvedAt               *time.Time     `json:"resolved_at"`
	Resolution               string         `json:"resolution"`
	ManualReviewReason       string         `json:"manual_review_reason"`
	ManualAdminID            *int64         `json:"manual_admin_id,omitempty"`
	ManualActionAt           *time.Time     `json:"manual_action_at,omitempty"`
	ManualActionReason       string         `json:"manual_action_reason,omitempty"`
	ManualActionAmount       *int64         `json:"manual_action_amount,omitempty"`
	ManualActionOperationKey string         `json:"manual_action_operation_key,omitempty"`
	Version                  int            `json:"version"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

type ReconciliationStats struct {
	PendingCount             int64 `json:"pending_count"`
	PendingRefundCount       int64 `json:"pending_refund_count"`
	ProvisioningUnknownCount int64 `json:"provisioning_unknown_count"`
	ManualReviewCount        int64 `json:"manual_review_count"`
	OldestPendingAgeSeconds  int64 `json:"oldest_pending_age_seconds"`
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
	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	return Pool.QueryRow(ctx, `
		INSERT INTO reconciliation_records
			(operation_key, kind, user_id, subscription_id, purchase_request_id, desired_state, observed_state, status, error_message, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, NOW())
		ON CONFLICT (operation_key) DO UPDATE SET
			desired_state = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.desired_state
				ELSE EXCLUDED.desired_state
			END,
			observed_state = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.observed_state
				ELSE EXCLUDED.observed_state
			END,
			status = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.status
				ELSE EXCLUDED.status
			END,
			error_message = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.error_message
				ELSE EXCLUDED.error_message
			END,
			next_attempt_at = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.next_attempt_at
				ELSE NOW()
			END,
			updated_at = CASE
				WHEN reconciliation_records.status IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
				THEN reconciliation_records.updated_at
				ELSE NOW()
			END
		RETURNING id, status
	`, record.OperationKey, record.Kind, record.UserID, record.SubscriptionID, record.PurchaseRequestID, desired, observed, record.Status, record.ErrorMessage).Scan(&record.ID, &record.Status)
}

func createReconciliationRecordTx(ctx context.Context, tx pgx.Tx, record *ReconciliationRecord) error {
	desired, err := json.Marshal(record.DesiredState)
	if err != nil {
		return err
	}
	observed, err := json.Marshal(record.ObservedState)
	if err != nil {
		return err
	}
	if record.Status == "" {
		record.Status = ReconciliationStatusPending
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO reconciliation_records
			(operation_key, kind, user_id, subscription_id, purchase_request_id, desired_state, observed_state, status, error_message, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, NOW())
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id
	`, record.OperationKey, record.Kind, record.UserID, record.SubscriptionID, record.PurchaseRequestID, desired, observed, record.Status, record.ErrorMessage).Scan(&record.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var existingID int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM reconciliation_records
		WHERE operation_key = $1 AND kind = $2
		  AND user_id IS NOT DISTINCT FROM $3
		  AND purchase_request_id IS NOT DISTINCT FROM $4
		  AND desired_state = $5::jsonb
	`, record.OperationKey, record.Kind, record.UserID, record.PurchaseRequestID, desired).Scan(&existingID)
	if err != nil {
		return fmt.Errorf("reconciliation operation key conflicts with different durable work: %w", err)
	}
	record.ID = existingID
	return nil
}

// MarkReconciliationCreateAttempted persists the non-idempotent AddClient
// boundary while the worker owns the record lease. A restarted worker can
// distinguish a never-issued create from an ambiguous prior attempt.
func MarkReconciliationCreateAttempted(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if Pool == nil {
		return 0, errors.New("database pool is not initialized")
	}
	var version int
	err := Pool.QueryRow(ctx, `
		UPDATE reconciliation_records
		SET observed_state = jsonb_set(COALESCE(observed_state, '{}'::jsonb), '{phase}', '"create_attempted"'::jsonb, TRUE),
		    updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $1 AND locked_by = $2 AND status = $3 AND version = $4
		RETURNING version
	`, id, lockedBy, expectedStatus, expectedVersion).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrReconciliationLeaseLost
	}
	return version, err
}

// ResetReconciliationCreateAttempt is safe only after XUI has classified the
// request as a definitive no-write. Unknown outcomes keep create_attempted.
func ResetReconciliationCreateAttempt(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if Pool == nil {
		return 0, errors.New("database pool is not initialized")
	}
	var version int
	err := Pool.QueryRow(ctx, `
		UPDATE reconciliation_records
		SET observed_state = jsonb_set(COALESCE(observed_state, '{}'::jsonb), '{phase}', '"ready"'::jsonb, TRUE),
		    updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $1 AND locked_by = $2 AND status = $3 AND version = $4
		  AND observed_state->>'phase' = 'create_attempted'
		RETURNING version
	`, id, lockedBy, expectedStatus, expectedVersion).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrReconciliationLeaseLost
	}
	return version, err
}

// ClaimPendingReconciliationRecords claims pending records using FOR UPDATE SKIP LOCKED.
func ClaimPendingReconciliationRecords(ctx context.Context, lockedBy string, limit int) ([]*ReconciliationRecord, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	rows, err := Pool.Query(ctx, `
		WITH claimable AS (
			SELECT id
			FROM reconciliation_records
			WHERE status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')
			  AND (next_attempt_at <= NOW() OR next_attempt_at IS NULL)
			  AND (locked_at IS NULL OR locked_at < NOW() - INTERVAL '5 minutes')
			ORDER BY next_attempt_at ASC NULLS FIRST, id ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE reconciliation_records r
		SET locked_at = NOW(), locked_by = $1, attempt_count = attempt_count + 1, updated_at = NOW(), version = COALESCE(version, 1) + 1
		FROM claimable
		WHERE r.id = claimable.id
		RETURNING r.id, r.operation_key, r.kind, r.user_id, r.subscription_id, r.purchase_request_id,
		          r.desired_state, r.observed_state, r.status, r.error_message, r.attempt_count,
		          r.next_attempt_at, r.locked_at, r.locked_by, r.resolved_at, r.resolution,
		          r.manual_review_reason, r.created_at, r.updated_at, COALESCE(r.version, 1)
	`, lockedBy, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ReconciliationRecord
	for rows.Next() {
		r := &ReconciliationRecord{}
		var desiredBytes, observedBytes []byte
		err := rows.Scan(
			&r.ID, &r.OperationKey, &r.Kind, &r.UserID, &r.SubscriptionID, &r.PurchaseRequestID,
			&desiredBytes, &observedBytes, &r.Status, &r.ErrorMessage, &r.AttemptCount,
			&r.NextAttemptAt, &r.LockedAt, &r.LockedBy, &r.ResolvedAt, &r.Resolution,
			&r.ManualReviewReason, &r.CreatedAt, &r.UpdatedAt, &r.Version,
		)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(desiredBytes, &r.DesiredState)
		_ = json.Unmarshal(observedBytes, &r.ObservedState)
		records = append(records, r)
	}
	return records, rows.Err()
}

var (
	ErrReconciliationTransitionNotAllowed = errors.New("reconciliation transition not allowed from current state")
	ErrReconciliationLeaseLost            = errors.New("reconciliation worker lease lost or superseded")
	ErrReconciliationNotFound             = errors.New("reconciliation record not found")
	ErrFinancialWaiverRequired            = errors.New("financial reconciliation requires explicit manual waiver")
	ErrNoVerifiedFinancialEffect          = errors.New("matching financial ledger effect was not found")
)

const (
	ReconciliationStatusPending          = "pending"
	ReconciliationStatusPendingRefund    = "pending_refund"
	ReconciliationStatusRequired         = "reconciliation_required"
	ReconciliationStatusManualReview     = "manual_review"
	ReconciliationStatusResolvedVerified = "resolved_verified"
	ReconciliationStatusResolved         = "resolved"
	ReconciliationStatusManuallyClosed   = "manually_closed"
	ReconciliationStatusManualWaiver     = "manual_waiver"
	ReconciliationStatusSuperseded       = "superseded"
	ReconciliationStatusFailedTerminal   = "failed_terminal"
)

func IsTerminalReconciliationStatus(status string) bool {
	switch status {
	case ReconciliationStatusResolvedVerified,
		ReconciliationStatusResolved,
		ReconciliationStatusSuperseded,
		ReconciliationStatusFailedTerminal,
		ReconciliationStatusManuallyClosed,
		ReconciliationStatusManualWaiver:
		return true
	default:
		return false
	}
}

func checkReconciliationRecordTransitionFailure(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int) error {
	current, err := GetReconciliationRecordByID(ctx, id)
	if err != nil {
		return err
	}
	if current == nil {
		return ErrReconciliationNotFound
	}
	if IsTerminalReconciliationStatus(current.Status) {
		return ErrReconciliationTransitionNotAllowed
	}
	if expectedStatus != "" && current.Status != expectedStatus {
		return ErrReconciliationTransitionNotAllowed
	}
	if lockedBy != "" && (current.LockedBy == nil || *current.LockedBy != lockedBy) {
		return ErrReconciliationLeaseLost
	}
	if expectedVersion != 0 && current.Version != expectedVersion {
		return ErrReconciliationLeaseLost
	}
	return ErrReconciliationTransitionNotAllowed
}

func ResolveReconciliationRecord(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int, resolution string) error {
	return ResolveReconciliationRecordWithStatus(ctx, id, lockedBy, expectedStatus, expectedVersion, ReconciliationStatusResolvedVerified, resolution)
}

func ResolveReconciliationRecordWithStatus(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int, targetStatus string, resolution string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	if targetStatus == "" {
		targetStatus = ReconciliationStatusResolvedVerified
	}
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = $1, resolution = $2, resolved_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $3
		  AND ($4 = '' OR locked_by = $4)
		  AND ($5 = '' OR status = $5)
		  AND ($6 = 0 OR version = $6)
		  AND status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, targetStatus, resolution, id, lockedBy, expectedStatus, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkReconciliationRecordTransitionFailure(ctx, id, lockedBy, expectedStatus, expectedVersion)
	}
	return nil
}

func ManuallyCloseReconciliationRecord(ctx context.Context, id int64, adminID int64, reason string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	if adminID <= 0 || strings.TrimSpace(reason) == "" {
		return errors.New("admin id and reason are required")
	}
	if rec, err := GetReconciliationRecordByID(ctx, id); err != nil {
		return err
	} else if rec != nil && reconciliationHasFinancialObligation(rec) {
		return ErrFinancialWaiverRequired
	}
	resolution := fmt.Sprintf("manually closed by admin %d: %s", adminID, reason)
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'manually_closed', manual_review_reason = $1, resolution = $2,
		    manual_admin_id = $3, manual_action_at = NOW(), manual_action_reason = $1,
		    resolved_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $4
		  AND kind NOT IN ('pending_refund', 'purchase_provisioning_unknown', 'purchase_remote_created_db_failed', 'direct_payment_provisioning_retry')
		  AND COALESCE(NULLIF(desired_state->>'refund_amount', '')::BIGINT, 0) <= 0
		  AND COALESCE(NULLIF(desired_state->>'amount', '')::BIGINT, 0) <= 0
		  AND COALESCE(NULLIF(desired_state->>'price', '')::BIGINT, 0) <= 0
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, reason, resolution, adminID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var kind string
		if err := Pool.QueryRow(ctx, `SELECT kind FROM reconciliation_records WHERE id = $1`, id).Scan(&kind); err == nil {
			switch kind {
			case "pending_refund", "purchase_provisioning_unknown", "purchase_remote_created_db_failed", "direct_payment_provisioning_retry":
				return ErrFinancialWaiverRequired
			}
		}
		return checkReconciliationRecordTransitionFailure(ctx, id, "", "", 0)
	}
	return nil
}

func reconciliationHasFinancialObligation(rec *ReconciliationRecord) bool {
	if rec == nil {
		return false
	}
	switch rec.Kind {
	case "pending_refund", "purchase_provisioning_unknown", "purchase_remote_created_db_failed", "direct_payment_provisioning_retry":
		return true
	}
	for _, key := range []string{"refund_amount", "amount", "price"} {
		if amount, ok := anyInt64(rec.DesiredState[key]); ok && amount > 0 {
			return true
		}
	}
	return false
}

func anyInt64(value any) (int64, bool) {
	switch n := value.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func ManuallyWaiveReconciliationRecord(ctx context.Context, id int64, adminID int64, reason string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	if adminID <= 0 || strings.TrimSpace(reason) == "" {
		return errors.New("admin id and waiver reason are required")
	}
	resolution := fmt.Sprintf("manually waived by admin %d: %s", adminID, reason)
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'manual_waiver', manual_review_reason = $1, resolution = $2,
		    manual_admin_id = $3, manual_action_at = NOW(), manual_action_reason = $1,
		    manual_action_amount = COALESCE(NULLIF(desired_state->>'amount', '')::BIGINT,
		                                    NULLIF(desired_state->>'price', '')::BIGINT,
		                                    NULLIF(desired_state->>'refund_amount', '')::BIGINT),
		    manual_action_operation_key = COALESCE(NULLIF(desired_state->>'operation_key', ''),
		                                           NULLIF(desired_state->>'refund_operation_key', ''), operation_key),
		    resolved_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $4
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, reason, resolution, adminID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkReconciliationRecordTransitionFailure(ctx, id, "", "", 0)
	}
	return nil
}

// ResolvePendingRefundVerified allows an admin to mark a refund complete only
// after the exact idempotent wallet credit is present in the ledger.
func ResolvePendingRefundVerified(ctx context.Context, id int64, adminID int64) error {
	if adminID <= 0 {
		return errors.New("admin id is required")
	}
	rec, err := GetReconciliationRecordByID(ctx, id)
	if err != nil {
		return err
	}
	if rec == nil {
		return ErrReconciliationNotFound
	}
	if rec.Kind != "pending_refund" {
		return errors.New("verified financial action is not supported for this reconciliation kind")
	}
	userID, amount, opKey, err := pendingRefundIdentity(rec)
	if err != nil {
		return err
	}
	var exists bool
	err = Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM transactions
			WHERE user_id = $1 AND operation_key = $2 AND type = 'credit'
			  AND status = 'completed' AND amount = $3
		)
	`, userID, opKey, amount).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNoVerifiedFinancialEffect
	}
	resolution := fmt.Sprintf("admin %d verified matching wallet credit in ledger", adminID)
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'resolved_verified', resolution = $1, resolved_at = NOW(),
		    manual_admin_id = $2, manual_action_at = NOW(), manual_action_reason = 'verified matching wallet credit',
		    manual_action_amount = $3, manual_action_operation_key = $4,
		    locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $5 AND kind = 'pending_refund'
		  AND status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable', 'manual_review')
		  AND locked_by IS NULL
	`, resolution, adminID, amount, opKey, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrReconciliationTransitionNotAllowed
	}
	return nil
}

func pendingRefundIdentity(rec *ReconciliationRecord) (int64, int64, string, error) {
	toInt64 := func(value any) (int64, bool) {
		switch n := value.(type) {
		case int64:
			return n, true
		case int:
			return int64(n), true
		case float64:
			return int64(n), true
		case string:
			v, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
			return v, err == nil
		default:
			return 0, false
		}
	}
	userID, ok := toInt64(rec.DesiredState["user_id"])
	if !ok && rec.UserID != nil {
		userID, ok = *rec.UserID, true
	}
	amount, amountOK := toInt64(rec.DesiredState["amount"])
	opKey, _ := rec.DesiredState["operation_key"].(string)
	if userID <= 0 || !ok || amount <= 0 || !amountOK || strings.TrimSpace(opKey) == "" {
		return 0, 0, "", errors.New("pending refund payload is missing verified ledger identity")
	}
	return userID, amount, opKey, nil
}

func FailAndScheduleRetry(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int, errMessage string, retryAfter time.Duration) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	intervalStr := fmt.Sprintf("%d seconds", int(retryAfter.Seconds()))
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'retryable', error_message = $1, next_attempt_at = NOW() + $2::interval, locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $3
		  AND ($4 = '' OR locked_by = $4)
		  AND ($5 = '' OR status = $5)
		  AND ($6 = 0 OR version = $6)
		  AND status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, errMessage, intervalStr, id, lockedBy, expectedStatus, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkReconciliationRecordTransitionFailure(ctx, id, lockedBy, expectedStatus, expectedVersion)
	}
	return nil
}

func MarkReconciliationManualReview(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int, reason string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'manual_review', manual_review_reason = $1, locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $2
		  AND ($3 = '' OR locked_by = $3)
		  AND ($4 = '' OR status = $4)
		  AND ($5 = 0 OR version = $5)
		  AND status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, reason, id, lockedBy, expectedStatus, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkReconciliationRecordTransitionFailure(ctx, id, lockedBy, expectedStatus, expectedVersion)
	}
	return nil
}

func FailReconciliationTerminal(ctx context.Context, id int64, lockedBy string, expectedStatus string, expectedVersion int, reason string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'failed_terminal', error_message = $1, resolved_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $2
		  AND ($3 = '' OR locked_by = $3)
		  AND ($4 = '' OR status = $4)
		  AND ($5 = 0 OR version = $5)
		  AND status IN ('pending', 'pending_refund', 'reconciliation_required', 'manual_review', 'retryable')
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, reason, id, lockedBy, expectedStatus, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return checkReconciliationRecordTransitionFailure(ctx, id, lockedBy, expectedStatus, expectedVersion)
	}
	return nil
}

func GetReconciliationStats(ctx context.Context) (*ReconciliationStats, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	stats := &ReconciliationStats{}
	row := Pool.QueryRow(ctx, `
		SELECT
			COALESCE(COUNT(*) FILTER (WHERE status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')), 0) AS pending_count,
			COALESCE(COUNT(*) FILTER (WHERE status = 'pending_refund' OR kind = 'pending_refund'), 0) AS pending_refund_count,
			COALESCE(COUNT(*) FILTER (WHERE kind LIKE '%provisioning%'), 0) AS provisioning_unknown_count,
			COALESCE(COUNT(*) FILTER (WHERE status = 'manual_review'), 0) AS manual_review_count,
			COALESCE(EXTRACT(EPOCH FROM (NOW() - MIN(created_at))) FILTER (WHERE status IN ('pending', 'pending_refund', 'reconciliation_required', 'retryable')), 0)::BIGINT AS oldest_pending_age_seconds
		FROM reconciliation_records
	`)
	err := row.Scan(
		&stats.PendingCount,
		&stats.PendingRefundCount,
		&stats.ProvisioningUnknownCount,
		&stats.ManualReviewCount,
		&stats.OldestPendingAgeSeconds,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return stats, nil
	}
	return stats, err
}

func GetManualReviewReconciliationRecords(ctx context.Context, limit int) ([]*ReconciliationRecord, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	rows, err := Pool.Query(ctx, `
		SELECT id, operation_key, kind, user_id, subscription_id, purchase_request_id,
		       desired_state, observed_state, status, error_message, attempt_count,
		       next_attempt_at, locked_at, locked_by, resolved_at, resolution,
		       manual_review_reason, created_at, updated_at, COALESCE(version, 1),
	       manual_admin_id, manual_action_at, manual_action_reason, manual_action_amount, manual_action_operation_key
		FROM reconciliation_records
		WHERE status = 'manual_review'
		ORDER BY updated_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*ReconciliationRecord
	for rows.Next() {
		r := &ReconciliationRecord{}
		var desiredBytes, observedBytes []byte
		err := rows.Scan(
			&r.ID, &r.OperationKey, &r.Kind, &r.UserID, &r.SubscriptionID, &r.PurchaseRequestID,
			&desiredBytes, &observedBytes, &r.Status, &r.ErrorMessage, &r.AttemptCount,
			&r.NextAttemptAt, &r.LockedAt, &r.LockedBy, &r.ResolvedAt, &r.Resolution,
			&r.ManualReviewReason, &r.CreatedAt, &r.UpdatedAt, &r.Version,
			&r.ManualAdminID, &r.ManualActionAt, &r.ManualActionReason, &r.ManualActionAmount, &r.ManualActionOperationKey,
		)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(desiredBytes, &r.DesiredState)
		_ = json.Unmarshal(observedBytes, &r.ObservedState)
		records = append(records, r)
	}
	return records, rows.Err()
}

func GetReconciliationRecordByID(ctx context.Context, id int64) (*ReconciliationRecord, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}

	r := &ReconciliationRecord{}
	var desiredBytes, observedBytes []byte
	err := Pool.QueryRow(ctx, `
		SELECT id, operation_key, kind, user_id, subscription_id, purchase_request_id,
		       desired_state, observed_state, status, error_message, attempt_count,
		       next_attempt_at, locked_at, locked_by, resolved_at, resolution,
		       manual_review_reason, created_at, updated_at, COALESCE(version, 1),
	       manual_admin_id, manual_action_at, manual_action_reason, manual_action_amount, manual_action_operation_key
		FROM reconciliation_records
		WHERE id = $1
	`, id).Scan(
		&r.ID, &r.OperationKey, &r.Kind, &r.UserID, &r.SubscriptionID, &r.PurchaseRequestID,
		&desiredBytes, &observedBytes, &r.Status, &r.ErrorMessage, &r.AttemptCount,
		&r.NextAttemptAt, &r.LockedAt, &r.LockedBy, &r.ResolvedAt, &r.Resolution,
		&r.ManualReviewReason, &r.CreatedAt, &r.UpdatedAt, &r.Version,
		&r.ManualAdminID, &r.ManualActionAt, &r.ManualActionReason, &r.ManualActionAmount, &r.ManualActionOperationKey,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	_ = json.Unmarshal(desiredBytes, &r.DesiredState)
	_ = json.Unmarshal(observedBytes, &r.ObservedState)
	return r, nil
}

var ErrReconciliationNotRetryable = errors.New("reconciliation record is not in a retryable state")

func ResetReconciliationForRetry(ctx context.Context, id int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}
	tag, err := Pool.Exec(ctx, `
		UPDATE reconciliation_records
		SET status = 'pending', next_attempt_at = NOW(), locked_at = NULL, locked_by = NULL, updated_at = NOW(), version = COALESCE(version, 1) + 1
		WHERE id = $1
		  AND status IN ('retryable', 'manual_review')
		  AND status NOT IN ('resolved_verified', 'resolved', 'superseded', 'failed_terminal', 'manually_closed', 'manual_waiver')
	`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrReconciliationNotRetryable
	}
	return nil
}
