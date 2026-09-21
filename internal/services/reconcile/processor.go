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
	"xui-reseller-bot/internal/services/pricing"
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
	Kind       OutcomeKind
	Resolution string
	Reason     string
	Err        error
}

type Processor struct {
	WorkerID  string
	XUI       XUIClient
	CreditFn  func(ctx context.Context, userID int64, amount int64, description, operationKey string) error
	BatchSize int
	MaxRetry  int
}

func NewProcessor(workerID string, xuiClient XUIClient) *Processor {
	if workerID == "" {
		workerID = fmt.Sprintf("worker_%d", time.Now().UnixNano())
	}
	return &Processor{
		WorkerID:  workerID,
		XUI:       xuiClient,
		CreditFn:  db.CreditWalletBalanceWithKey,
		BatchSize: 10,
		MaxRetry:  5,
	}
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
	case KindSubscriptionDeleteUnknown, KindSubscriptionCancellationDbFailed:
		outcome = p.handleDeleteReconciliation(ctx, rec)
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
		if err := db.ResolveReconciliationRecord(ctx, rec.ID, outcome.Resolution); err != nil {
			log.Printf("[RECONCILE] Failed to mark record %d resolved: %v", rec.ID, err)
		} else {
			log.Printf("[RECONCILE] Resolved record %d (op=%s, kind=%s): %s", rec.ID, rec.OperationKey, rec.Kind, outcome.Resolution)
		}

	case OutcomeManualReview:
		if err := db.MarkReconciliationManualReview(ctx, rec.ID, outcome.Reason); err != nil {
			log.Printf("[RECONCILE] Failed to mark record %d manual review: %v", rec.ID, err)
		} else {
			log.Printf("[RECONCILE] Record %d (op=%s, kind=%s) moved to manual review: %s", rec.ID, rec.OperationKey, rec.Kind, outcome.Reason)
		}

	case OutcomeRetry:
		if rec.AttemptCount >= p.MaxRetry {
			reason := fmt.Sprintf("exceeded %d attempts; last error: %v", p.MaxRetry, outcome.Err)
			if err := db.MarkReconciliationManualReview(ctx, rec.ID, reason); err != nil {
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
			if err := db.FailAndScheduleRetry(ctx, rec.ID, errMsg, backoff); err != nil {
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
		Kind:       OutcomeResolved,
		Resolution: fmt.Sprintf("wallet refunded %d Toman (key: %s)", payload.Amount, payload.OperationKey),
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
		existing, checkErr := db.GetSubscriptionByEmail(ctx, payload.Email)
		if checkErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to check existing db subscription: %w", checkErr),
			}
		}
		if existing != nil {
			return ProcessOutcome{
				Kind:       OutcomeResolved,
				Resolution: fmt.Sprintf("remote client %s and subscription %d both exist", payload.Email, existing.ID),
			}
		}

		// 3. Adopt client into local DB
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
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("remote client %s confirmed, safely adopted into subscription %d", payload.Email, sub.ID),
		}

	case RemoteConfirmedAbsent:
		// Remote client is confirmed absent!
		// Derive exact refund amount from immutable quote or ledger transaction.
		var refundAmount int64
		var amountProven bool

		if payload.QuoteID != nil && *payload.QuoteID > 0 {
			quote, qErr := pricing.GetQuoteByID(ctx, *payload.QuoteID)
			if qErr == nil && quote != nil {
				refundAmount = quote.FinalPriceToman
				amountProven = true
			} else {
				// Quote was specified but could not be loaded from DB!
				return ProcessOutcome{
					Kind:   OutcomeManualReview,
					Reason: fmt.Sprintf("quote %d could not be verified for absent client %s; manual review required", *payload.QuoteID, payload.Email),
				}
			}
		}

		if !amountProven {
			// Check ledger for debit transaction by debit operation key
			debitKey := payload.DebitOperationKey
			if debitKey == "" {
				debitKey = payload.OperationKey
			}
			if debitKey != "" {
				tx, txErr := db.GetCompletedDebitTransaction(ctx, payload.UserID, debitKey)
				if txErr == nil && tx != nil {
					refundAmount = tx.Amount
					if refundAmount < 0 {
						refundAmount = -refundAmount
					}
					amountProven = true
				}
			}
		}

		if !amountProven && payload.Price > 0 {
			// If payload has a positive price but no quote or transaction proves debit, move to manual review
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("unproven debit amount (%d Toman) for remote absent client %s; manual review required", payload.Price, payload.Email),
			}
		}

		if !amountProven {
			// Cannot prove any debit occurred
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("cannot prove debit amount for remote absent client %s; manual review required", payload.Email),
			}
		}

		if refundAmount <= 0 {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("proven amount is non-positive (%d) for remote absent client %s; manual review required", refundAmount, payload.Email),
			}
		}

		refundOpKey := payload.RefundOperationKey
		if refundOpKey == "" {
			refundOpKey = payload.OperationKey + ":refund"
		}

		refundErr := p.executeRefund(ctx, payload.UserID, refundAmount, "refund for failed purchase: "+payload.Email, refundOpKey)
		if refundErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  refundErr, // Must remain retryable!
			}
		}
		return ProcessOutcome{
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("remote client absent, refunded %d Toman (key: %s)", refundAmount, refundOpKey),
		}

	default: // RemotePresenceUnknown
		// Timeout or network error or nil client! Ambiguous! NEVER delete or refund.
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive remote existence check for %s: %w", payload.Email, err),
		}
	}
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
		// Step 2: Persist local cancellation/deleted state in DB.
		if payload.SubscriptionID != nil {
			if delErr := db.DeleteSubscription(ctx, int(*payload.SubscriptionID)); delErr != nil {
				return ProcessOutcome{
					Kind: OutcomeRetry,
					Err:  fmt.Errorf("failed to mark subscription %d deleted in DB: %w", *payload.SubscriptionID, delErr),
				}
			}
		}
		// Step 3: Perform/confirm refund if required.
		if payload.RefundAmount > 0 && payload.UserID != nil {
			refundErr := p.executeRefund(ctx, *payload.UserID, payload.RefundAmount, "refund for cancelled subscription", payload.RefundOperationKey)
			if refundErr != nil {
				return ProcessOutcome{
					Kind: OutcomeRetry,
					Err:  refundErr, // Keep retryable! Never resolve with failed refund!
				}
			}
		}
		// Step 4: Resolve.
		return ProcessOutcome{
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("remote client %s confirmed deleted, local record cleaned", payload.ClientEmail),
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
				Kind:       OutcomeResolved,
				Resolution: fmt.Sprintf("remote client %s deleted on retry", payload.ClientEmail),
			}
		}
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("remote client %s still present after retry delete: %w", payload.ClientEmail, delErr),
		}

	default: // RemotePresenceUnknown
		// Timeout or network error! Ambiguous! NEVER assume deleted.
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive delete check for %s: %w", payload.ClientEmail, err),
		}
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
		}
		sub.Status = db.SubscriptionStatusActive
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
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("subscription %d db state updated to match remote", sub.ID),
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
				}
				sub.Status = db.SubscriptionStatusActive
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
					Kind:       OutcomeResolved,
					Resolution: fmt.Sprintf("retried remote update for %s and verified readback", payload.ClientEmail),
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

	req, err := db.GetPurchaseRequestByID(ctx, payload.PurchaseRequestID)
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
	if req.ProvisioningStatus == db.PurchaseProvisioningSucceeded {
		return ProcessOutcome{
			Kind:       OutcomeResolved,
			Resolution: "already provisioned successfully",
		}
	}
	if req.Status != "approved" {
		return ProcessOutcome{
			Kind:       OutcomeResolved,
			Resolution: "purchase request not approved; skipping provisioning",
		}
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
		// Verify strong identity before adopting!
		if idErr := verifyClientIdentity(remote, payload.ClientEmail, payload.ExpectedUUID, payload.ExpectedSubID); idErr != nil {
			return ProcessOutcome{
				Kind:   OutcomeManualReview,
				Reason: fmt.Sprintf("direct payment client identity verification failed: %v", idErr),
			}
		}

		existing, checkErr := db.GetSubscriptionByEmail(ctx, payload.ClientEmail)
		if checkErr != nil {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to check existing subscription: %w", checkErr),
			}
		}
		clientUUID := remote.UUID
		if clientUUID == "" {
			clientUUID = payload.ExpectedUUID
		}
		if existing == nil {
			sub := &db.Subscription{
				UserID:            payload.UserID,
				PlanID:            payload.PlanID,
				QuoteID:           payload.QuoteID,
				ClientEmail:       payload.ClientEmail,
				ClientUUID:        clientUUID,
				SubID:             remote.SubID,
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
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("direct payment client %s confirmed, adopted into subscription", payload.ClientEmail),
		}

	case RemoteConfirmedAbsent:
		// Client absent remotely. Create client in XUI first.
		var inbounds []int
		if payload.PlanID != nil {
			paidPlan, pErr := db.GetPaidPlanByID(ctx, int64(*payload.PlanID))
			if pErr == nil && paidPlan != nil {
				inbounds = paidPlan.InboundIDs
			}
		}

		// Exact first-use lazy expiry value (negative duration in milliseconds)
		expiryMilli := int64(0)
		if payload.Months > 0 {
			expiryMilli = -int64(payload.Months * 30 * 24 * 3600 * 1000)
		}
		totalBytes := int64(payload.DataGB) * 1024 * 1024 * 1024

		newUUID := payload.ExpectedUUID
		if newUUID == "" {
			newUUID = generateUUID()
		}
		newSubID := payload.ExpectedSubID
		if newSubID == "" {
			newSubID = generateSubID()
		}

		addReq := xui.AddClientRequest{
			InboundIDs: inbounds,
			Client: xui.ClientConfig{
				ID:         newUUID,
				Email:      payload.ClientEmail,
				SubID:      newSubID,
				Enable:     true,
				ExpiryTime: expiryMilli,
				LimitIP:    payload.IPLimit,
				TotalGB:    totalBytes,
			},
		}

		writeRes := p.XUI.AddClientResult(addReq)
		if writeRes.Outcome != xui.WriteSucceeded {
			return ProcessOutcome{
				Kind: OutcomeRetry,
				Err:  fmt.Errorf("failed to add remote client %s: %v", payload.ClientEmail, writeRes.Err),
			}
		}

		// Remote created! Now create subscription in local DB
		sub := &db.Subscription{
			UserID:            payload.UserID,
			PlanID:            payload.PlanID,
			QuoteID:           payload.QuoteID,
			ClientEmail:       payload.ClientEmail,
			ClientUUID:        newUUID,
			SubID:             newSubID,
			Status:            db.SubscriptionStatusActive,
			PlanType:          db.PlanTypePaid,
			DisplayName:       payload.CustomName,
			IPLimit:           payload.IPLimit,
			ExpireTime:        &expiryMilli,
			IsActive:          true,
			StartDate:         time.Now().UTC(),
			TrafficLimitBytes: totalBytes,
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
			Kind:       OutcomeResolved,
			Resolution: fmt.Sprintf("direct payment client %s created and provisioned successfully", payload.ClientEmail),
		}

	default: // RemotePresenceUnknown
		return ProcessOutcome{
			Kind: OutcomeRetry,
			Err:  fmt.Errorf("inconclusive check for direct payment client %s: %w", payload.ClientEmail, err),
		}
	}
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
