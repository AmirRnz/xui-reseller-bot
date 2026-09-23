package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type RemotePresenceState int

const (
	RemotePresenceUnknown RemotePresenceState = iota
	RemoteConfirmedPresent
	RemoteConfirmedAbsent
)

// ClassifyRemoteClient classifies remote existence according to strict roadmap invariants:
// - xui.IsNotFound(err) => confirmed absent
// - err != nil => unknown/ambiguous
// - err == nil && client == nil => unknown/anomalous
// - err == nil && client != nil => confirmed present
func ClassifyRemoteClient(client *xui.XUIClientInfo, err error) RemotePresenceState {
	if xui.IsNotFound(err) {
		return RemoteConfirmedAbsent
	}
	if err != nil || client == nil {
		return RemotePresenceUnknown
	}
	return RemoteConfirmedPresent
}

type XUIClient interface {
	GetClientByEmail(email string) (*xui.XUIClientInfo, error)
	DeleteClient(email string) error
	AddClient(req xui.AddClientRequest) error
	AddClientResult(req xui.AddClientRequest) xui.WriteResult
	UpdateClientResult(email string, client xui.ClientConfig) xui.WriteResult
}

type OutcomeKind string

const (
	OutcomeResolved     OutcomeKind = "resolved"
	OutcomeRetry        OutcomeKind = "retry"
	OutcomeManualReview OutcomeKind = "manual_review"
)

type ProcessOutcome struct {
	Kind         OutcomeKind
	TargetStatus string
	Resolution   string
	Reason       string
	Err          error
}

type Processor struct {
	WorkerID                       string
	XUI                            XUIClient
	CreditFn                       func(ctx context.Context, userID int64, amount int64, description, operationKey string) error
	DebitTxFn                      func(ctx context.Context, userID int64, operationKey string) (*db.WalletTransaction, error)
	CancelSubscriptionWithRefundFn func(ctx context.Context, subscriptionID int, userID int64, amount int64, operationKey string) (*db.RefundRequest, error)
	UpdateSubscriptionStatusFn     func(ctx context.Context, id int, status string) error
	GetSubscriptionByIDFn          func(ctx context.Context, id int) (*db.Subscription, error)
	GetSubscriptionByEmailFn       func(ctx context.Context, email string) (*db.Subscription, error)
	GetPurchaseRequestByIDFn       func(ctx context.Context, id int64) (*db.PurchaseRequest, error)
	CreateRefundRequestFn          func(ctx context.Context, r *db.RefundRequest) error
	BatchSize                      int
	MaxRetry                       int
}

func NewProcessor(workerID string, xuiClient XUIClient) *Processor {
	if workerID == "" {
		workerID = "reconcile_worker"
	}
	if !strings.Contains(workerID, ":") {
		workerID = fmt.Sprintf("%s:%s", workerID, generateSubID()[:8])
	}
	return &Processor{
		WorkerID:                       workerID,
		XUI:                            xuiClient,
		CreditFn:                       db.CreditWalletBalanceWithKey,
		CancelSubscriptionWithRefundFn: db.CancelSubscriptionWithRefund,
		UpdateSubscriptionStatusFn:     db.UpdateSubscriptionStatus,
		GetSubscriptionByIDFn:          db.GetSubscriptionByID,
		GetSubscriptionByEmailFn:       db.GetSubscriptionByEmail,
		GetPurchaseRequestByIDFn:       db.GetPurchaseRequestByID,
		CreateRefundRequestFn:          db.CreateRefundRequest,
		BatchSize:                      10,
		MaxRetry:                       5,
	}
}

func (p *Processor) cancelSubWithRefund(ctx context.Context, subscriptionID int, userID int64, amount int64, operationKey string) (*db.RefundRequest, error) {
	fn := p.CancelSubscriptionWithRefundFn
	if fn == nil {
		fn = db.CancelSubscriptionWithRefund
	}
	return fn(ctx, subscriptionID, userID, amount, operationKey)
}

func (p *Processor) updateSubStatus(ctx context.Context, id int, status string) error {
	fn := p.UpdateSubscriptionStatusFn
	if fn == nil {
		fn = db.UpdateSubscriptionStatus
	}
	return fn(ctx, id, status)
}

func (p *Processor) getSubByID(ctx context.Context, id int) (*db.Subscription, error) {
	fn := p.GetSubscriptionByIDFn
	if fn == nil {
		fn = db.GetSubscriptionByID
	}
	return fn(ctx, id)
}

func (p *Processor) getSubscriptionByEmail(ctx context.Context, email string) (*db.Subscription, error) {
	if p.GetSubscriptionByEmailFn != nil {
		return p.GetSubscriptionByEmailFn(ctx, email)
	}
	return db.GetSubscriptionByEmail(ctx, email)
}

func (p *Processor) getPurchaseRequestByID(ctx context.Context, id int64) (*db.PurchaseRequest, error) {
	if p.GetPurchaseRequestByIDFn != nil {
		return p.GetPurchaseRequestByIDFn(ctx, id)
	}
	return db.GetPurchaseRequestByID(ctx, id)
}

func (p *Processor) createRefundRequest(ctx context.Context, r *db.RefundRequest) error {
	fn := p.CreateRefundRequestFn
	if fn == nil {
		fn = db.CreateRefundRequest
	}
	return fn(ctx, r)
}

// Start launches a background loop that periodically processes pending reconciliation records.
func (p *Processor) Start(ctx context.Context, interval time.Duration) {
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
				_, _ = p.ProcessOnce(ctx)
			}
		}
	}()
}

// ProcessOnce claims a batch of pending reconciliation records and executes their recovery actions.
func (p *Processor) ProcessOnce(ctx context.Context) (int, error) {
	records, err := db.ClaimPendingReconciliationRecords(ctx, p.WorkerID, p.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to claim reconciliation records: %w", err)
	}
	if len(records) == 0 {
		return 0, nil
	}

	for _, rec := range records {
		p.processRecord(ctx, rec)
	}
	return len(records), nil
}

func (p *Processor) processRecord(ctx context.Context, rec *db.ReconciliationRecord) {
	var outcome ProcessOutcome

	switch rec.Kind {
	case KindPendingRefund:
		outcome = p.handlePendingRefund(ctx, rec)
	case KindPurchaseProvisioningUnknown, KindPurchaseRemoteCreatedDbFailed:
		outcome = p.handlePurchaseReconciliation(ctx, rec)
	case KindSubscriptionUpdateDbFailed:
		outcome = p.handleUpdateReconciliation(ctx, rec)
	case KindSubscriptionDeleteUnknown:
		outcome = p.handleDeleteReconciliation(ctx, rec)
	case KindSubscriptionCancellationDbFailed:
		outcome = p.handleSubscriptionCancellation(ctx, rec)
	case KindDirectPaymentProvisioningRetry:
		outcome = p.handleDirectPaymentProvisioning(ctx, rec)
	case KindSubscriptionRemoteMissing:
		outcome = p.handleSubscriptionRemoteMissing(ctx, rec)
	default:
		if rec.Status == "pending_refund" {
			outcome = p.handlePendingRefund(ctx, rec)
		} else {
			outcome = ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("unsupported reconciliation kind: %s", rec.Kind),
			}
		}
	}

	switch outcome.Kind {
	case OutcomeResolved:
		targetStatus := outcome.TargetStatus
		if targetStatus == "" {
			targetStatus = db.ReconciliationStatusResolvedVerified
		}
		if err := db.ResolveReconciliationRecordWithStatus(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version, targetStatus, outcome.Resolution); err != nil {
			log.Printf("[RECONCILE] Failed to mark record %d %s: %v", rec.ID, targetStatus, err)
		} else {
			log.Printf("[RECONCILE] Resolved record %d (op=%s, kind=%s) -> %s: %s", rec.ID, rec.OperationKey, rec.Kind, targetStatus, outcome.Resolution)
		}

	case OutcomeManualReview:
		if err := db.MarkReconciliationManualReview(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version, outcome.Reason); err != nil {
			log.Printf("[RECONCILE] Failed to mark record %d manual review: %v", rec.ID, err)
		} else {
			log.Printf("[RECONCILE] Record %d (op=%s, kind=%s) moved to manual review: %s", rec.ID, rec.OperationKey, rec.Kind, outcome.Reason)
		}

	case OutcomeRetry:
		if rec.AttemptCount >= p.MaxRetry {
			reason := fmt.Sprintf("exceeded %d attempts; last error: %v", p.MaxRetry, outcome.Err)
			if err := db.MarkReconciliationManualReview(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version, reason); err != nil {
				log.Printf("[RECONCILE] Failed to mark record %d manual review after max retries: %v", rec.ID, err)
			} else {
				log.Printf("[RECONCILE] Record %d moved to manual review after %d retries: %v", rec.ID, p.MaxRetry, outcome.Err)
			}
		} else {
			backoff := time.Duration(math.Pow(2, float64(rec.AttemptCount))) * 30 * time.Second
			errMsg := "unknown error"
			if outcome.Err != nil {
				errMsg = outcome.Err.Error()
			}
			if err := db.FailAndScheduleRetry(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version, errMsg, backoff); err != nil {
				log.Printf("[RECONCILE] Failed to schedule retry for record %d: %v", rec.ID, err)
			} else {
				log.Printf("[RECONCILE] Record %d failed (attempt %d/%d), retry in %v: %v", rec.ID, rec.AttemptCount, p.MaxRetry, backoff, errMsg)
			}
		}
	}
}

func (p *Processor) executeRefund(ctx context.Context, userID int64, amount int64, desc, refundOpKey string) error {
	if amount <= 0 {
		return nil
	}
	if userID <= 0 {
		return errors.New("user_id must be greater than 0 for refund")
	}
	if refundOpKey == "" {
		return errors.New("operation_key is required for refund")
	}

	creditFn := p.CreditFn
	if creditFn == nil {
		creditFn = db.CreditWalletBalanceWithKey
	}

	err := creditFn(ctx, userID, amount, desc, refundOpKey)
	if err == nil || errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
		return nil
	}
	return fmt.Errorf("refund failed for user %d (opKey=%s): %w", userID, refundOpKey, err)
}

func (p *Processor) handlePendingRefund(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodePendingRefund(rec.DesiredState, rec.UserID, rec.OperationKey)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid pending_refund payload: %v", err),
		}
	}

	if err := p.executeRefund(ctx, payload.UserID, payload.Amount, payload.Description, payload.OperationKey); err != nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  err,
		}
	}
	return ProcessOutcome{
		Kind:         OutcomeResolved,
		TargetStatus: db.ReconciliationStatusResolved,
		Resolution:   fmt.Sprintf("wallet refunded %d Toman (key: %s)", payload.Amount, payload.OperationKey),
	}
}

func verifyClientIdentity(remote *xui.XUIClientInfo, expectedEmail, expectedUUID, expectedSubID string) error {
	if remote == nil {
		return errors.New("remote client is nil")
	}
	if strings.TrimSpace(expectedEmail) == "" {
		return errors.New("expected email is required")
	}
	if remote.Email != expectedEmail {
		return fmt.Errorf("email mismatch: remote=%q expected=%q", remote.Email, expectedEmail)
	}

	// Automatic adoption requires strong intended identity: only email is NOT enough!
	if strings.TrimSpace(expectedUUID) == "" && strings.TrimSpace(expectedSubID) == "" {
		return errors.New("insufficient identity evidence: only email available for paid adoption; manual review required")
	}

	if strings.TrimSpace(expectedUUID) != "" {
		remoteUUID := remote.UUID
		if remoteUUID == "" {
			remoteUUID = remote.Password
		}
		if remoteUUID == "" {
			return errors.New("remote UUID missing while expected UUID is present; identity cannot be proven")
		}
		if remoteUUID != expectedUUID {
			return fmt.Errorf("UUID mismatch: remote=%q expected=%q", remoteUUID, expectedUUID)
		}
	}

	if strings.TrimSpace(expectedSubID) != "" {
		if remote.SubID == "" {
			return errors.New("remote SubID missing while expected SubID is present; identity cannot be proven")
		}
		if remote.SubID != expectedSubID {
			return fmt.Errorf("SubID mismatch: remote=%q expected=%q", remote.SubID, expectedSubID)
		}
	}

	return nil
}

func verifySubscriptionCommercialIdentity(sub *db.Subscription, expectedUserID int64, expectedUUID, expectedSubID string, expectedQuoteID *int64) error {
	if sub == nil {
		return errors.New("subscription is nil")
	}
	if sub.UserID != expectedUserID {
		return fmt.Errorf("user_id mismatch: sub.UserID=%d expected=%d", sub.UserID, expectedUserID)
	}
	if strings.TrimSpace(expectedUUID) != "" && sub.ClientUUID != expectedUUID {
		return fmt.Errorf("UUID mismatch: sub.ClientUUID=%q expected=%q", sub.ClientUUID, expectedUUID)
	}
	if strings.TrimSpace(expectedSubID) != "" && sub.SubID != expectedSubID {
		return fmt.Errorf("sub_id mismatch: sub.SubID=%q expected=%q", sub.SubID, expectedSubID)
	}
	if expectedQuoteID != nil && *expectedQuoteID > 0 {
		if sub.QuoteID == nil || *sub.QuoteID != *expectedQuoteID {
			subQuote := int64(0)
			if sub.QuoteID != nil {
				subQuote = *sub.QuoteID
			}
			return fmt.Errorf("quote_id mismatch: sub.QuoteID=%d expected=%d", subQuote, *expectedQuoteID)
		}
	}
	return nil
}

func (p *Processor) getCompletedDebitTransaction(ctx context.Context, userID int64, operationKey string) (*db.WalletTransaction, error) {
	if p.DebitTxFn != nil {
		return p.DebitTxFn(ctx, userID, operationKey)
	}
	return db.GetCompletedDebitTransaction(ctx, userID, operationKey)
}

func (p *Processor) handlePurchaseReconciliation(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodePurchaseProvisioning(rec.DesiredState, rec.UserID, rec.OperationKey)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid purchase provisioning payload: %v", err),
		}
	}

	if p.XUI == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  errors.New("xui client is not available"),
		}
	}

	remote, err := p.XUI.GetClientByEmail(payload.Email)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedPresent:
		// Client confirmed present remotely!
		// 1. Verify identity before adopting!
		if idErr := verifyClientIdentity(remote, payload.Email, payload.ExpectedUUID, payload.ExpectedSubID); idErr != nil {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("identity verification failed for remote client %s: %v", payload.Email, idErr),
			}
		}
		// 2. Check existing subscription in DB
		existing, checkErr := p.getSubscriptionByEmail(ctx, payload.Email)
		if checkErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to check existing db subscription: %w", checkErr),
			}
		}
		if existing != nil {
			if idErr := verifySubscriptionCommercialIdentity(existing, payload.UserID, payload.ExpectedUUID, payload.ExpectedSubID, payload.QuoteID); idErr != nil {
				return ProcessOutcome{
					Kind:   OutcomeManualReview,
					Reason: fmt.Sprintf("commercial identity verification failed for existing subscription %d: %v", existing.ID, idErr),
				}
			}
			return ProcessOutcome{
				Kind:         OutcomeResolved,
				TargetStatus: db.ReconciliationStatusResolvedVerified,
				Resolution:   fmt.Sprintf("remote client %s and verified subscription %d both exist", payload.Email, existing.ID),
			}
		}

		// 3. Adopt client into local DB
		if !walletRemoteMatches(remote, payload) {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: "remote wallet-purchase client does not match the exact durable desired state"}
		}
		displayName := payload.DisplayName
		if displayName == "" {
			displayName = payload.Email
		}
		ipLimit := remote.LimitIP
		if ipLimit <= 0 {
			ipLimit = payload.IPLimit
			if ipLimit <= 0 {
				ipLimit = 1
			}
		}

		sub := &db.Subscription{
			UserID:            payload.UserID,
			PlanID:            payload.PlanID,
			QuoteID:           payload.QuoteID,
			ClientEmail:       remote.Email,
			ClientUUID:        remote.UUID,
			SubID:             remote.SubID,
			Status:            db.SubscriptionStatusActive,
			PlanType:          db.PlanTypePaid,
			DisplayName:       displayName,
			IPLimit:           ipLimit,
			ExpireTime:        &remote.ExpiryTime,
			IsActive:          remote.Enable,
			StartDate:         time.Now().UTC(),
			TrafficLimitBytes: remote.TotalGB,
		}
		if remote.ExpiryTime > 0 {
			sub.EndDate = time.UnixMilli(remote.ExpiryTime)
		}

		if err := db.CreateSubscription(ctx, sub); err != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to adopt subscription %s: %w", payload.Email, err),
			}
		}
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolvedVerified,
			Resolution:   fmt.Sprintf("remote client %s confirmed, safely adopted into subscription %d", payload.Email, sub.ID),
		}

	case RemoteConfirmedAbsent:
		if phase, _ := rec.ObservedState["phase"].(string); phase == "ready" {
			return p.startWalletPurchaseProvisioning(ctx, rec, payload)
		}
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "wallet purchase create was previously attempted but the remote client is currently absent; financial and panel state require manual verification"}
	default: // RemotePresenceUnknown
		// Timeout or network error or nil client! Ambiguous! NEVER delete or refund.
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive remote existence check for %s: %w", payload.Email, err),
		}
	}
}

func (p *Processor) startWalletPurchaseProvisioning(ctx context.Context, rec *db.ReconciliationRecord, payload *PurchaseProvisioningPayload) ProcessOutcome {
	debitKey := payload.DebitOperationKey
	if debitKey == "" {
		debitKey = payload.OperationKey
	}
	debit, err := p.getCompletedDebitTransaction(ctx, payload.UserID, debitKey)
	if err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to verify wallet debit before create: %w", err)}
	}
	if debit == nil || debit.Type != "debit" || debit.Status != "completed" {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "wallet provisioning work has no matching completed debit"}
	}
	if debitAmount := abs(debit.Amount); debitAmount <= 0 || (payload.Price > 0 && debitAmount != payload.Price) {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "wallet debit amount does not match durable provisioning terms"}
	}
	if payload.ExpectedUUID == "" || payload.ExpectedSubID == "" || len(payload.InboundIDs) == 0 || payload.ExpiryTimeMilli == 0 {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "durable wallet provisioning snapshot is missing its identity or desired state"}
	}

	version, err := db.MarkReconciliationCreateAttempted(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version)
	if err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to persist create-attempt boundary: %w", err)}
	}
	rec.Version = version
	rec.ObservedState["phase"] = "create_attempted"

	comment := payload.PlanName
	if comment == "" {
		comment = payload.DisplayName
	}
	write := p.XUI.AddClientResult(xui.AddClientRequest{
		InboundIDs: append([]int(nil), payload.InboundIDs...),
		Client: xui.ClientConfig{
			ID: payload.ExpectedUUID, Email: payload.Email, SubID: payload.ExpectedSubID,
			Enable: true, ExpiryTime: payload.ExpiryTimeMilli, LimitIP: payload.IPLimit,
			TotalGB: payload.TotalBytes, Flow: payload.Flow, Group: payload.Group,
			TgID: payload.TelegramID, Comment: comment,
		},
	})
	if write.Outcome != xui.WriteSucceeded {
		if write.Outcome == xui.WriteDefinitiveFailure {
			version, resetErr := db.ResetReconciliationCreateAttempt(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version)
			if resetErr != nil {
				return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to reset confirmed no-write attempt: %w", resetErr)}
			}
			rec.Version = version
			rec.ObservedState["phase"] = "ready"
		}
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("wallet purchase create did not complete: %v", write.Err)}
	}

	remote, err := p.XUI.GetClientByEmail(payload.Email)
	if ClassifyRemoteClient(remote, err) != RemoteConfirmedPresent {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("created wallet client could not be verified: %v", err)}
	}
	if err := verifyClientIdentity(remote, payload.Email, payload.ExpectedUUID, payload.ExpectedSubID); err != nil {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: fmt.Sprintf("created wallet client identity could not be proven: %v", err)}
	}
	if !walletRemoteMatches(remote, payload) {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "created wallet client does not match the durable desired remote state"}
	}

	planID := payload.PlanID
	if planID != nil && *planID <= 0 {
		planID = nil
	}
	displayName := payload.DisplayName
	if displayName == "" {
		displayName = payload.Email
	}
	expiry := payload.ExpiryTimeMilli
	sub := &db.Subscription{
		UserID: payload.UserID, PlanID: planID, QuoteID: payload.QuoteID,
		ClientEmail: payload.Email, ClientUUID: payload.ExpectedUUID, SubID: payload.ExpectedSubID,
		Status: db.SubscriptionStatusActive, PlanType: db.PlanTypePaid,
		DisplayName: displayName, IPLimit: payload.IPLimit, ExpireTime: &expiry,
		IsActive: remote.Enable, StartDate: time.Now().UTC(), TrafficLimitBytes: payload.TotalBytes,
	}
	if err := db.CreateSubscription(ctx, sub); err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("verified wallet client exists but local subscription insert failed: %w", err)}
	}
	return ProcessOutcome{
		Kind: OutcomeResolved, TargetStatus: db.ReconciliationStatusResolvedVerified,
		Resolution: fmt.Sprintf("wallet purchase provisioned as verified subscription %d", sub.ID),
	}
}

func walletRemoteMatches(remote *xui.XUIClientInfo, payload *PurchaseProvisioningPayload) bool {
	comment := payload.PlanName
	if comment == "" {
		comment = payload.DisplayName
	}
	return remote != nil && remote.Email == payload.Email && remote.UUID == payload.ExpectedUUID && remote.SubID == payload.ExpectedSubID &&
		remote.Enable && remote.ExpiryTime == payload.ExpiryTimeMilli && remote.LimitIP == payload.IPLimit && remote.TotalGB == payload.TotalBytes &&
		remote.Flow == payload.Flow && remote.Group == payload.Group && remote.TgID == payload.TelegramID && remote.Comment == comment && sameIDs(remote.InboundIDs, payload.InboundIDs)
}

func sameIDs(actual, expected []int) bool {
	if len(actual) != len(expected) {
		return false
	}
	return allIDsPresent(actual, expected) && allIDsPresent(expected, actual)
}

func allIDsPresent(actual, expected []int) bool {
	actualSet := make(map[int]struct{}, len(actual))
	for _, id := range actual {
		actualSet[id] = struct{}{}
	}
	for _, id := range expected {
		if _, ok := actualSet[id]; !ok {
			return false
		}
	}
	return true
}

func abs(amount int64) int64 {
	if amount < 0 {
		return -amount
	}
	return amount
}

func (p *Processor) handleDeleteReconciliation(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodeSubscriptionDelete(rec.DesiredState, rec.SubscriptionID, rec.UserID, rec.OperationKey)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid subscription delete payload: %v", err),
		}
	}

	if p.XUI == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  errors.New("xui client is not available"),
		}
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedAbsent:
		// Step 1: Remote is confirmed absent.
		// Step 2: Persist local deleted state in DB.
		if payload.SubscriptionID != nil {
			if delErr := db.DeleteSubscription(ctx, int(*payload.SubscriptionID)); delErr != nil {
				return ProcessOutcome{
					Kind: OutcomeRetry,
					Err:  fmt.Errorf("failed to mark subscription %d deleted in DB: %w", *payload.SubscriptionID, delErr),
				}
			}
		}
		// Step 3: Perform/confirm compensation refund if required.
		if payload.RefundAmount > 0 && payload.UserID != nil {
			refundErr := p.executeRefund(ctx, *payload.UserID, payload.RefundAmount, "refund for cancelled subscription", payload.RefundOperationKey)
			if refundErr != nil {
				return ProcessOutcome{
					Kind: OutcomeRetry,
					Err:  refundErr, // Keep retryable!
				}
			}
		}
		// Step 4: Resolve.
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolved,
			Resolution:   fmt.Sprintf("remote client %s confirmed deleted, local record cleaned", payload.ClientEmail),
		}

	case RemoteConfirmedPresent:
		// Remote client still exists. Attempt delete once.
		delErr := p.XUI.DeleteClient(payload.ClientEmail)
		if delErr == nil || xui.IsNotFound(delErr) {
			if payload.SubscriptionID != nil {
				if dbDelErr := db.DeleteSubscription(ctx, int(*payload.SubscriptionID)); dbDelErr != nil {
					return ProcessOutcome{
						Kind: OutcomeRetry,
						Err:  fmt.Errorf("failed to mark subscription %d deleted in DB after remote deletion: %w", *payload.SubscriptionID, dbDelErr),
					}
				}
			}
			if payload.RefundAmount > 0 && payload.UserID != nil {
				refundErr := p.executeRefund(ctx, *payload.UserID, payload.RefundAmount, "refund for cancelled subscription", payload.RefundOperationKey)
				if refundErr != nil {
					return ProcessOutcome{
						Kind: OutcomeRetry,
						Err:  refundErr, // Keep retryable!
					}
				}
			}
			return ProcessOutcome{
				Kind:         OutcomeResolved,
				TargetStatus: db.ReconciliationStatusResolved,
				Resolution:   fmt.Sprintf("remote client %s deleted on retry", payload.ClientEmail),
			}
		}
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("remote client %s still present after retry delete: %w", payload.ClientEmail, delErr),
		}

	default: // RemotePresenceUnknown
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive delete check for %s: %w", payload.ClientEmail, err),
		}
	}
}

func (p *Processor) handleSubscriptionCancellation(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodeSubscriptionDelete(rec.DesiredState, rec.SubscriptionID, rec.UserID, rec.OperationKey)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid subscription cancellation payload: %v", err),
		}
	}

	if payload.SubscriptionID == nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("subscription cancellation missing subscription ID for %s: manual review required", payload.ClientEmail),
		}
	}

	if p.XUI == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  errors.New("xui client is not available"),
		}
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedAbsent:
		return p.reconcileAbsentSubscription(ctx, rec, payload)

	case RemoteConfirmedPresent:
		// Remote client still exists. Attempt delete once.
		delErr := p.XUI.DeleteClient(payload.ClientEmail)
		if delErr == nil || xui.IsNotFound(delErr) {
			return p.reconcileAbsentSubscription(ctx, rec, payload)
		}
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("remote client %s still present after retry delete: %w", payload.ClientEmail, delErr),
		}

	default: // RemotePresenceUnknown
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive delete check for %s: %w", payload.ClientEmail, err),
		}
	}
}

func (p *Processor) reconcileAbsentSubscription(ctx context.Context, rec *db.ReconciliationRecord, payload *SubscriptionDeletePayload) ProcessOutcome {
	if payload.SubscriptionID == nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("subscription cancellation missing subscription ID for %s: manual review required", payload.ClientEmail),
		}
	}

	subID := int(*payload.SubscriptionID)
	sub, err := p.getSubByID(ctx, subID)
	if err != nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("failed to fetch subscription %d: %w", subID, err),
		}
	}
	if sub == nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("remote client %s confirmed deleted, but local subscription %d not found in DB", payload.ClientEmail, subID),
		}
	}
	if payload.UserID != nil && sub.UserID != *payload.UserID {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("remote client %s confirmed deleted, but subscription %d user mismatch (db=%d, payload=%d)", payload.ClientEmail, subID, sub.UserID, *payload.UserID),
		}
	}

	// Step 1: Ensure subscription is marked CANCELLED (never deleted).
	// Step 2: Under no circumstances perform direct wallet credit or bypass the refund queue.
	//         Ensure refund request is recorded in refund_requests with status 'pending'.
	if payload.RefundAmount > 0 {
		userID := sub.UserID
		if payload.UserID != nil {
			userID = *payload.UserID
		}
		refundOpKey := payload.RefundOperationKey
		if refundOpKey == "" {
			refundOpKey = fmt.Sprintf("subscription_cancel_refund:%d", subID)
		}

		refundReq, cancelErr := p.cancelSubWithRefund(ctx, subID, userID, payload.RefundAmount, refundOpKey)
		if cancelErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("remote client %s confirmed deleted, but CancelSubscriptionWithRefund failed for sub %d: %w", payload.ClientEmail, subID, cancelErr),
			}
		}
		if refundReq == nil {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("remote client %s confirmed deleted, CancelSubscriptionWithRefund returned nil request for sub %d", payload.ClientEmail, subID),
			}
		}
	} else {
		if updateErr := p.updateSubStatus(ctx, subID, db.SubscriptionStatusCancelled); updateErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("remote client %s confirmed deleted, but UpdateSubscriptionStatus failed for sub %d: %w", payload.ClientEmail, subID, updateErr),
			}
		}
	}

	return ProcessOutcome{
		Kind:         OutcomeResolved,
		TargetStatus: db.ReconciliationStatusResolved,
		Resolution:   fmt.Sprintf("remote client %s confirmed deleted, subscription %d marked cancelled and refund recorded in pending queue", payload.ClientEmail, subID),
	}
}

func (p *Processor) handleUpdateReconciliation(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodeSubscriptionUpdate(rec.DesiredState, rec.SubscriptionID)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid subscription update payload: %v", err),
		}
	}

	sub, err := db.GetSubscriptionByID(ctx, int(payload.SubscriptionID))
	if err != nil || sub == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("subscription %d not found in db: %w", payload.SubscriptionID, err),
		}
	}

	if p.XUI == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  errors.New("xui client is not available"),
		}
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)
	if presence != RemoteConfirmedPresent {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("remote client %s not present or check inconclusive: %w", payload.ClientEmail, err),
		}
	}

	// Compare: Desired vs Observed Remote vs Current DB
	desiredIP := sub.IPLimit
	if payload.DesiredIPLimit != nil {
		desiredIP = *payload.DesiredIPLimit
	}
	desiredExpiry := int64(0)
	if sub.ExpireTime != nil {
		desiredExpiry = *sub.ExpireTime
	}
	if payload.DesiredExpireTime != nil {
		desiredExpiry = *payload.DesiredExpireTime
	}
	desiredActive := sub.IsActive
	if payload.DesiredIsActive != nil {
		desiredActive = *payload.DesiredIsActive
	}

	remoteMatchesDesired := (payload.DesiredIPLimit == nil || remote.LimitIP == desiredIP) &&
		(payload.DesiredExpireTime == nil || remote.ExpiryTime == desiredExpiry) &&
		(payload.DesiredIsActive == nil || remote.Enable == desiredActive)

	if remoteMatchesDesired {
		// Remote already has desired state! Commit to DB and resolve.
		sub.IPLimit = desiredIP
		sub.IsActive = desiredActive
		sub.ExpireTime = &desiredExpiry
		if desiredExpiry > 0 {
			sub.EndDate = time.UnixMilli(desiredExpiry)
		} else {
			sub.EndDate = time.Time{}
		}
		if !desiredActive {
			sub.Status = db.SubscriptionStatusDisabled
		} else if desiredExpiry > 0 && time.Now().After(time.UnixMilli(desiredExpiry)) {
			sub.Status = db.SubscriptionStatusExpired
		} else {
			sub.Status = db.SubscriptionStatusActive
		}
		sub.DesiredIPLimit = nil
		sub.DesiredExpireTime = nil
		sub.DesiredIsActive = nil
		sub.ReconciliationNote = ""

		if err := db.UpdateSubscription(ctx, sub); err != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to commit db subscription %d: %w", sub.ID, err),
			}
		}
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolvedVerified,
			Resolution:   fmt.Sprintf("subscription %d db state updated to match remote", sub.ID),
		}
	}

	// Check if remote matches previous state (remote write never happened or was rolled back)
	remoteMatchesPrevious := (payload.PreviousIPLimit == 0 || remote.LimitIP == payload.PreviousIPLimit) &&
		(payload.PreviousExpireTime == 0 || remote.ExpiryTime == payload.PreviousExpireTime) &&
		remote.Enable == payload.PreviousIsActive

	if remoteMatchesPrevious {
		// Retry the remote mutation once
		updateCfg := xui.ClientConfig{
			Email:      remote.Email,
			Enable:     desiredActive,
			ExpiryTime: desiredExpiry,
			LimitIP:    desiredIP,
			TotalGB:    remote.TotalGB,
		}
		res := p.XUI.UpdateClientResult(remote.Email, updateCfg)
		if res.Outcome == xui.WriteSucceeded {
			// Re-verify readback
			readback, rbErr := p.XUI.GetClientByEmail(remote.Email)
			if rbErr == nil && readback != nil &&
				readback.LimitIP == desiredIP &&
				readback.ExpiryTime == desiredExpiry &&
				readback.Enable == desiredActive {

				sub.IPLimit = desiredIP
				sub.IsActive = desiredActive
				sub.ExpireTime = &desiredExpiry
				if desiredExpiry > 0 {
					sub.EndDate = time.UnixMilli(desiredExpiry)
				} else {
					sub.EndDate = time.Time{}
				}
				if !desiredActive {
					sub.Status = db.SubscriptionStatusDisabled
				} else if desiredExpiry > 0 && time.Now().After(time.UnixMilli(desiredExpiry)) {
					sub.Status = db.SubscriptionStatusExpired
				} else {
					sub.Status = db.SubscriptionStatusActive
				}
				sub.DesiredIPLimit = nil
				sub.DesiredExpireTime = nil
				sub.DesiredIsActive = nil
				sub.ReconciliationNote = ""
				if err := db.UpdateSubscription(ctx, sub); err != nil {
					return ProcessOutcome{
						Kind: OutcomeRetry,
						Err:  fmt.Errorf("failed to commit db subscription %d after retry: %w", sub.ID, err),
					}
				}
				return ProcessOutcome{
					Kind:         OutcomeResolved,
					TargetStatus: db.ReconciliationStatusResolvedVerified,
					Resolution:   fmt.Sprintf("retried remote update for %s and verified readback", payload.ClientEmail),
				}
			}
		}
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("remote update retry failed or inconclusive for %s: %v", payload.ClientEmail, res.Err),
		}
	}

	// Remote state is in an unexpected divergent state! Move to manual review.
	return ProcessOutcome{
		Kind:   OutcomeManualReview,
		Reason: fmt.Sprintf("remote client %s state is unexpected (remote IP=%d, Expiry=%d, Active=%t)", payload.ClientEmail, remote.LimitIP, remote.ExpiryTime, remote.Enable),
	}
}

func (p *Processor) handleDirectPaymentProvisioning(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodeDirectPaymentProvisioning(rec.DesiredState, rec.PurchaseRequestID, rec.UserID, rec.OperationKey)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid direct payment provisioning payload: %v", err),
		}
	}

	// Automatic adoption requires strong intended identity: only email is NOT enough!
	if strings.TrimSpace(payload.ExpectedUUID) == "" && strings.TrimSpace(payload.ExpectedSubID) == "" {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: "legacy direct payment record missing required strong identity: manual review required",
		}
	}

	req, err := p.getPurchaseRequestByID(ctx, payload.PurchaseRequestID)
	if err != nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("failed to fetch purchase request: %w", err),
		}
	}
	if req == nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("purchase request %d not found in db", payload.PurchaseRequestID),
		}
	}
	reqAction := req.Type
	if reqAction == "new_subscription" {
		reqAction = "buy"
	}
	if payload.ActionType == "" {
		payload.ActionType = reqAction
	}
	if payload.ActionType == "new_subscription" {
		payload.ActionType = "buy"
	}
	if payload.ActionType != reqAction {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "durable provisioning action does not match the approved purchase request"}
	}
	if payload.ActionType != "buy" && payload.ActionType != "extend" && payload.ActionType != "upgrade_ip" {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: fmt.Sprintf("unsupported approved purchase action type %q", payload.ActionType)}
	}
	if req.ProvisioningStatus == db.PurchaseProvisioningSucceeded {
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolved,
			Resolution:   "already provisioned successfully",
		}
	}
	if req.Status != "approved" {
		if req.Status == "rejected" || req.Status == "cancelled" {
			return ProcessOutcome{
				Kind:         OutcomeResolved,
				TargetStatus: db.ReconciliationStatusSuperseded,
				Resolution:   fmt.Sprintf("purchase request %d %s; provisioning superseded", payload.PurchaseRequestID, req.Status),
			}
		}
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("purchase request %d still pending admin approval (status=%q); awaiting decision", payload.PurchaseRequestID, req.Status),
		}
	}
	if req.PriceToman == nil || *req.PriceToman != payload.AmountToman || req.QuoteID != nil && payload.QuoteID != nil && *req.QuoteID != *payload.QuoteID {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "durable provisioning amount or quote does not match the approved purchase request"}
	}
	debit, err := p.getCompletedDebitTransaction(ctx, payload.UserID, payload.FinancialOperationKey)
	if err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to verify direct-payment approval ledger entry: %w", err)}
	}
	if debit == nil || debit.Type != "debit" || debit.Status != "completed" || abs(debit.Amount) != payload.AmountToman {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct-payment approval has no matching completed financial audit entry"}
	}

	if p.XUI == nil {
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  errors.New("xui client not available"),
		}
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedPresent:
		if payload.ActionType == "extend" || payload.ActionType == "upgrade_ip" {
			return p.handleDirectSubscriptionUpdate(ctx, payload, req)
		}
		// Verify strong identity before adopting!
		if idErr := verifyClientIdentity(remote, payload.ClientEmail, payload.ExpectedUUID, payload.ExpectedSubID); idErr != nil {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("direct payment client identity verification failed: %v", idErr),
			}
		}
		existing, checkErr := p.getSubscriptionByEmail(ctx, payload.ClientEmail)
		if checkErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to check existing subscription: %w", checkErr),
			}
		}
		if existing != nil {
			if idErr := verifySubscriptionCommercialIdentity(existing, payload.UserID, payload.ExpectedUUID, payload.ExpectedSubID, payload.QuoteID); idErr != nil {
				return ProcessOutcome{
					Kind:   OutcomeManualReview,
					Reason: fmt.Sprintf("commercial identity verification failed for existing direct payment subscription %d: %v", existing.ID, idErr),
				}
			}
			if payload.ActionType == "buy" && !directRemoteMatches(remote, payload) {
				return ProcessOutcome{Kind: OutcomeManualReview, Reason: "remote direct-payment client does not match the durable approved desired state"}
			}
		} else {
			if !directRemoteMatches(remote, payload) {
				return ProcessOutcome{Kind: OutcomeManualReview, Reason: "remote direct-payment client does not match the durable approved desired state"}
			}
			clientUUID := payload.ExpectedUUID
			sub := &db.Subscription{
				UserID:            payload.UserID,
				PlanID:            payload.PlanID,
				QuoteID:           payload.QuoteID,
				ClientEmail:       payload.ClientEmail,
				ClientUUID:        clientUUID,
				SubID:             payload.ExpectedSubID,
				Status:            db.SubscriptionStatusActive,
				PlanType:          db.PlanTypePaid,
				DisplayName:       payload.CustomName,
				IPLimit:           payload.IPLimit,
				ExpireTime:        &remote.ExpiryTime,
				IsActive:          remote.Enable,
				StartDate:         time.Now().UTC(),
				TrafficLimitBytes: remote.TotalGB,
			}
			if remote.ExpiryTime > 0 {
				sub.EndDate = time.UnixMilli(remote.ExpiryTime)
			}
			if err := db.CreateSubscription(ctx, sub); err != nil {
				return ProcessOutcome{
					Kind: OutcomeRetry,
					Err:  fmt.Errorf("failed to create db subscription for remote client %s: %w", payload.ClientEmail, err),
				}
			}
		}

		if statusErr := db.SetPurchaseProvisioningStatus(ctx, payload.PurchaseRequestID, db.PurchaseProvisioningSucceeded); statusErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to set provisioning status succeeded: %w", statusErr),
			}
		}
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolvedVerified,
			Resolution:   fmt.Sprintf("direct payment client %s confirmed, adopted into subscription", payload.ClientEmail),
		}

	case RemoteConfirmedAbsent:
		if payload.ActionType != "buy" {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: "approved subscription update refers to a remote client that is absent"}
		}
		// Client absent remotely. Create client in XUI first.
		if len(payload.InboundIDs) == 0 || payload.ExpectedUUID == "" || payload.ExpectedSubID == "" || payload.ExpiryTimeMilli == 0 || payload.TotalBytes < 0 {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: "missing required immutable provisioning identity or desired state",
			}
		}
		if phase, _ := rec.ObservedState["phase"].(string); phase != "ready" {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: "remote client is absent after a previously attempted direct-payment create; automatic duplicate create is blocked"}
		}
		version, err := db.MarkReconciliationCreateAttempted(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version)
		if err != nil {
			return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to persist direct create-attempt boundary: %w", err)}
		}
		rec.Version = version
		rec.ObservedState["phase"] = "create_attempted"

		addReq := xui.AddClientRequest{
			InboundIDs: append([]int(nil), payload.InboundIDs...),
			Client: xui.ClientConfig{
				ID:         payload.ExpectedUUID,
				Email:      payload.ClientEmail,
				SubID:      payload.ExpectedSubID,
				Enable:     true,
				ExpiryTime: payload.ExpiryTimeMilli,
				LimitIP:    payload.IPLimit,
				TotalGB:    payload.TotalBytes,
				Flow:       payload.Flow,
				Group:      payload.Group,
				TgID:       payload.TelegramID,
				Comment:    payload.CustomName,
			},
		}

		writeRes := p.XUI.AddClientResult(addReq)
		if writeRes.Outcome != xui.WriteSucceeded {
			if writeRes.Outcome == xui.WriteDefinitiveFailure {
				version, resetErr := db.ResetReconciliationCreateAttempt(ctx, rec.ID, p.WorkerID, rec.Status, rec.Version)
				if resetErr != nil {
					return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to reset confirmed no-write attempt: %w", resetErr)}
				}
				rec.Version = version
				rec.ObservedState["phase"] = "ready"
			}
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to add remote client %s: %v", payload.ClientEmail, writeRes.Err),
			}
		}
		verifiedRemote, verifyErr := p.XUI.GetClientByEmail(payload.ClientEmail)
		if ClassifyRemoteClient(verifiedRemote, verifyErr) != RemoteConfirmedPresent {
			return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("direct payment client could not be verified after create: %v", verifyErr)}
		}
		if identityErr := verifyClientIdentity(verifiedRemote, payload.ClientEmail, payload.ExpectedUUID, payload.ExpectedSubID); identityErr != nil {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: fmt.Sprintf("direct payment create identity could not be proven: %v", identityErr)}
		}
		if !directRemoteMatches(verifiedRemote, payload) {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct payment client does not match durable desired state"}
		}

		// Remote created! Now create subscription in local DB
		expiryMilli := payload.ExpiryTimeMilli
		sub := &db.Subscription{
			UserID:            payload.UserID,
			PlanID:            payload.PlanID,
			QuoteID:           payload.QuoteID,
			ClientEmail:       payload.ClientEmail,
			ClientUUID:        payload.ExpectedUUID,
			SubID:             payload.ExpectedSubID,
			Status:            db.SubscriptionStatusActive,
			PlanType:          db.PlanTypePaid,
			DisplayName:       payload.CustomName,
			IPLimit:           payload.IPLimit,
			ExpireTime:        &expiryMilli,
			IsActive:          true,
			StartDate:         time.Now().UTC(),
			TrafficLimitBytes: payload.TotalBytes,
		}
		if expiryMilli > 0 {
			sub.EndDate = time.UnixMilli(expiryMilli)
		}

		if err := db.CreateSubscription(ctx, sub); err != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("remote client %s created but local db subscription failed: %w", payload.ClientEmail, err),
			}
		}

		if statusErr := db.SetPurchaseProvisioningStatus(ctx, payload.PurchaseRequestID, db.PurchaseProvisioningSucceeded); statusErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to set provisioning status succeeded: %w", statusErr),
			}
		}
		return ProcessOutcome{
			Kind:         OutcomeResolved,
			TargetStatus: db.ReconciliationStatusResolved,
			Resolution:   fmt.Sprintf("direct payment client %s created and provisioned successfully", payload.ClientEmail),
		}

	default: // RemotePresenceUnknown
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive check for direct payment client %s: %w", payload.ClientEmail, err),
		}
	}
}

func directRemoteMatches(remote *xui.XUIClientInfo, payload *DirectPaymentProvisioningPayload) bool {
	return remote != nil && remote.Email == payload.ClientEmail && remote.UUID == payload.ExpectedUUID && remote.SubID == payload.ExpectedSubID &&
		remote.Enable && remote.ExpiryTime == payload.ExpiryTimeMilli && remote.LimitIP == payload.IPLimit &&
		remote.TotalGB == payload.TotalBytes && remote.Flow == payload.Flow && remote.Group == payload.Group &&
		remote.TgID == payload.TelegramID && remote.Comment == payload.CustomName && sameIDs(remote.InboundIDs, payload.InboundIDs)
}

func (p *Processor) handleSubscriptionRemoteMissing(ctx context.Context, rec *db.ReconciliationRecord) ProcessOutcome {
	payload, err := DecodeSubscriptionRemoteMissing(rec.DesiredState, rec.SubscriptionID, rec.UserID)
	if err != nil {
		return ProcessOutcome{
			Kind:   OutcomeManualReview,
			Reason: fmt.Sprintf("invalid subscription remote missing payload: %v", err),
		}
	}
	return ProcessOutcome{
		Kind:   OutcomeManualReview,
		Reason: fmt.Sprintf("subscription %d (%s) is absent on 3x-ui panel: manual review required", payload.SubscriptionID, payload.ClientEmail),
	}
}

func (p *Processor) handleDirectSubscriptionUpdate(ctx context.Context, payload *DirectPaymentProvisioningPayload, req *db.PurchaseRequest) ProcessOutcome {
	if req.SubscriptionID == nil || payload.SubscriptionID == nil || *payload.SubscriptionID != *req.SubscriptionID {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct subscription update is missing its durable subscription identity"}
	}
	sub, err := db.GetSubscriptionByID(ctx, int(*req.SubscriptionID))
	if err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to load subscription for direct update: %w", err)}
	}
	if sub == nil || sub.UserID != payload.UserID || sub.ClientEmail != payload.ClientEmail {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct subscription update owner or email does not match the local subscription"}
	}
	if sub.ClientUUID == "" || sub.SubID == "" || sub.ClientUUID != payload.ExpectedUUID || sub.SubID != payload.ExpectedSubID {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct subscription update identity cannot be proven"}
	}
	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	if ClassifyRemoteClient(remote, err) != RemoteConfirmedPresent {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct subscription update remote client is absent or unknown"}
	}
	if err := verifyClientIdentity(remote, payload.ClientEmail, payload.ExpectedUUID, payload.ExpectedSubID); err != nil {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: fmt.Sprintf("direct update remote identity mismatch: %v", err)}
	}

	desiredIP := sub.IPLimit
	if payload.ActionType == "upgrade_ip" {
		if payload.DesiredIPLimit == nil || *payload.DesiredIPLimit <= 0 {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct IP upgrade has no valid durable target limit"}
		}
		desiredIP = *payload.DesiredIPLimit
	}
	desiredExpiry := payload.ExpiryTimeMilli
	if payload.ActionType == "upgrade_ip" && desiredExpiry == 0 && sub.ExpireTime != nil {
		desiredExpiry = *sub.ExpireTime
	}
	if desiredExpiry == 0 {
		return ProcessOutcome{Kind: OutcomeManualReview, Reason: "direct subscription update has no exact durable expiry value"}
	}

	if remote.ExpiryTime != desiredExpiry || remote.LimitIP != desiredIP || !remote.Enable {
		write := p.XUI.UpdateClientResult(payload.ClientEmail, xui.ClientConfig{
			Email: payload.ClientEmail, Enable: true, ExpiryTime: desiredExpiry,
			LimitIP: desiredIP, TotalGB: remote.TotalGB, SubID: payload.ExpectedSubID,
		})
		if write.Outcome != xui.WriteSucceeded {
			return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("direct subscription update did not complete: %v", write.Err)}
		}
		remote, err = p.XUI.GetClientByEmail(payload.ClientEmail)
		if ClassifyRemoteClient(remote, err) != RemoteConfirmedPresent {
			return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("direct subscription update readback failed: %v", err)}
		}
		if err := verifyClientIdentity(remote, payload.ClientEmail, payload.ExpectedUUID, payload.ExpectedSubID); err != nil {
			return ProcessOutcome{Kind: OutcomeManualReview, Reason: fmt.Sprintf("direct update readback identity mismatch: %v", err)}
		}
	}
	if remote.ExpiryTime != desiredExpiry || remote.LimitIP != desiredIP || !remote.Enable {
		return ProcessOutcome{Kind: OutcomeRetry, Err: errors.New("direct subscription update readback does not match durable target state")}
	}

	sub.ExpireTime = &desiredExpiry
	sub.IPLimit = desiredIP
	sub.IsActive = true
	if err := db.UpdateSubscription(ctx, sub); err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("panel update is verified but local subscription update failed: %w", err)}
	}
	if err := db.SetPurchaseProvisioningStatus(ctx, payload.PurchaseRequestID, db.PurchaseProvisioningSucceeded); err != nil {
		return ProcessOutcome{Kind: OutcomeRetry, Err: fmt.Errorf("failed to mark direct subscription update succeeded: %w", err)}
	}
	return ProcessOutcome{Kind: OutcomeResolved, TargetStatus: db.ReconciliationStatusResolvedVerified, Resolution: "direct subscription update verified in panel and database"}
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func generateSubID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
