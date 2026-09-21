package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
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

type Processor struct {
	WorkerID  string
	XUI       XUIClient
	CreditFn  func(ctx context.Context, userID int64, amount float64, description, operationKey string) error
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
	var err error
	var resolution string

	switch rec.Kind {
	case KindPendingRefund:
		resolution, err = p.handlePendingRefund(ctx, rec)
	case KindPurchaseProvisioningUnknown, KindPurchaseRemoteCreatedDbFailed:
		resolution, err = p.handlePurchaseReconciliation(ctx, rec)
	case KindSubscriptionUpdateDbFailed:
		resolution, err = p.handleUpdateReconciliation(ctx, rec)
	case KindSubscriptionDeleteUnknown, KindSubscriptionCancellationDbFailed:
		resolution, err = p.handleDeleteReconciliation(ctx, rec)
	case KindDirectPaymentProvisioningRetry:
		resolution, err = p.handleDirectPaymentProvisioning(ctx, rec)
	default:
		if rec.Status == "pending_refund" {
			resolution, err = p.handlePendingRefund(ctx, rec)
		} else {
			_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("unsupported reconciliation kind: %s", rec.Kind))
			return
		}
	}

	if err == nil && resolution != "" {
		_ = db.ResolveReconciliationRecord(ctx, rec.ID, resolution)
		log.Printf("[RECONCILE] Resolved record %d (op=%s, kind=%s): %s", rec.ID, rec.OperationKey, rec.Kind, resolution)
		return
	}

	if err != nil {
		if rec.AttemptCount >= p.MaxRetry {
			_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("exceeded %d attempts; last error: %v", p.MaxRetry, err))
			log.Printf("[RECONCILE] Record %d moved to manual review: %v", rec.ID, err)
		} else {
			backoff := time.Duration(math.Pow(2, float64(rec.AttemptCount))) * 30 * time.Second
			_ = db.FailAndScheduleRetry(ctx, rec.ID, err.Error(), backoff)
			log.Printf("[RECONCILE] Record %d failed (attempt %d/%d), retry in %v: %v", rec.ID, rec.AttemptCount, p.MaxRetry, backoff, err)
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

	err := creditFn(ctx, userID, float64(amount), desc, refundOpKey)
	if err == nil || errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
		return nil
	}
	return fmt.Errorf("refund failed for user %d (opKey=%s): %w", userID, refundOpKey, err)
}

func (p *Processor) handlePendingRefund(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	payload, err := DecodePendingRefund(rec.DesiredState, rec.UserID, rec.OperationKey)
	if err != nil {
		_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("invalid pending_refund payload: %v", err))
		return "manual review: invalid payload", nil
	}

	if err := p.executeRefund(ctx, payload.UserID, payload.Amount, payload.Description, payload.OperationKey); err != nil {
		return "", err
	}
	return fmt.Sprintf("wallet refunded %d Toman (key: %s)", payload.Amount, payload.OperationKey), nil
}

func verifyClientIdentity(remote *xui.XUIClientInfo, expectedEmail, expectedUUID, expectedSubID string) error {
	if remote == nil {
		return errors.New("remote client is nil")
	}
	if remote.Email != expectedEmail {
		return fmt.Errorf("email mismatch: remote=%q expected=%q", remote.Email, expectedEmail)
	}
	if expectedUUID != "" {
		remoteUUID := remote.UUID
		if remoteUUID == "" {
			remoteUUID = remote.Password
		}
		if remoteUUID != "" && remoteUUID != expectedUUID {
			return fmt.Errorf("UUID mismatch: remote=%q expected=%q", remoteUUID, expectedUUID)
		}
	}
	if expectedSubID != "" && remote.SubID != "" && remote.SubID != expectedSubID {
		return fmt.Errorf("SubID mismatch: remote=%q expected=%q", remote.SubID, expectedSubID)
	}
	return nil
}

func (p *Processor) handlePurchaseReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	payload, err := DecodePurchaseProvisioning(rec.DesiredState, rec.UserID, rec.OperationKey)
	if err != nil {
		_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("invalid purchase provisioning payload: %v", err))
		return "manual review: invalid payload", nil
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(payload.Email)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedPresent:
		// Client confirmed present remotely!
		// 1. Verify identity before adopting!
		if idErr := verifyClientIdentity(remote, payload.Email, payload.ExpectedUUID, payload.ExpectedSubID); idErr != nil {
			_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("identity verification failed for remote client %s: %v", payload.Email, idErr))
			return "manual review: remote client identity mismatch", nil
		}

		// 2. Check existing subscription in DB
		existing, checkErr := db.GetSubscriptionByEmail(ctx, payload.Email)
		if checkErr != nil {
			return "", fmt.Errorf("failed to check existing db subscription: %w", checkErr)
		}
		if existing != nil {
			return fmt.Sprintf("remote client %s and subscription %d both exist", payload.Email, existing.ID), nil
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
			return "", fmt.Errorf("failed to adopt subscription %s: %w", payload.Email, err)
		}
		return fmt.Sprintf("remote client %s confirmed, safely adopted into subscription %d", payload.Email, sub.ID), nil

	case RemoteConfirmedAbsent:
		// Remote client is confirmed absent! Safe to refund.
		if payload.Price <= 0 {
			return "remote client absent, no refund needed", nil
		}

		refundErr := p.executeRefund(ctx, payload.UserID, payload.Price, "refund for failed purchase: "+payload.Email, payload.RefundOperationKey)
		if refundErr != nil {
			return "", refundErr // Must remain retryable!
		}
		return fmt.Sprintf("remote client absent, refunded %d Toman (key: %s)", payload.Price, payload.RefundOperationKey), nil

	default: // RemotePresenceUnknown
		// Timeout or network error or nil client! Ambiguous! NEVER delete or refund.
		return "", fmt.Errorf("inconclusive remote existence check for %s: %w", payload.Email, err)
	}
}

func (p *Processor) handleDeleteReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	payload, err := DecodeSubscriptionDelete(rec.DesiredState, rec.SubscriptionID, rec.UserID, rec.OperationKey)
	if err != nil {
		_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("invalid subscription delete payload: %v", err))
		return "manual review: invalid payload", nil
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedAbsent:
		// Confirmed absent remotely!
		if payload.SubscriptionID != nil {
			_ = db.DeleteSubscription(ctx, int(*payload.SubscriptionID))
		}
		if payload.RefundAmount > 0 && payload.UserID != nil {
			refundErr := p.executeRefund(ctx, *payload.UserID, payload.RefundAmount, "refund for cancelled subscription", payload.RefundOperationKey)
			if refundErr != nil {
				return "", refundErr // Keep retryable! Never resolve with failed refund!
			}
		}
		return fmt.Sprintf("remote client %s confirmed deleted, local record cleaned", payload.ClientEmail), nil

	case RemoteConfirmedPresent:
		// Remote client still exists. Attempt delete once.
		delErr := p.XUI.DeleteClient(payload.ClientEmail)
		if delErr == nil || xui.IsNotFound(delErr) {
			if payload.SubscriptionID != nil {
				_ = db.DeleteSubscription(ctx, int(*payload.SubscriptionID))
			}
			if payload.RefundAmount > 0 && payload.UserID != nil {
				refundErr := p.executeRefund(ctx, *payload.UserID, payload.RefundAmount, "refund for cancelled subscription", payload.RefundOperationKey)
				if refundErr != nil {
					return "", refundErr // Keep retryable!
				}
			}
			return fmt.Sprintf("remote client %s deleted on retry", payload.ClientEmail), nil
		}
		return "", fmt.Errorf("remote client %s still present after retry delete: %w", payload.ClientEmail, delErr)

	default: // RemotePresenceUnknown
		// Timeout or network error! Ambiguous! NEVER assume deleted.
		return "", fmt.Errorf("inconclusive delete check for %s: %w", payload.ClientEmail, err)
	}
}

func (p *Processor) handleUpdateReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	payload, err := DecodeSubscriptionUpdate(rec.DesiredState, rec.SubscriptionID)
	if err != nil {
		_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("invalid subscription update payload: %v", err))
		return "manual review: invalid payload", nil
	}

	sub, err := db.GetSubscriptionByID(ctx, int(payload.SubscriptionID))
	if err != nil || sub == nil {
		return "", fmt.Errorf("subscription %d not found in db: %w", payload.SubscriptionID, err)
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(payload.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)
	if presence != RemoteConfirmedPresent {
		return "", fmt.Errorf("remote client %s not present or check inconclusive: %w", payload.ClientEmail, err)
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
			return "", fmt.Errorf("failed to commit db subscription %d: %w", sub.ID, err)
		}
		return fmt.Sprintf("remote client %s already has desired state, synced db", payload.ClientEmail), nil
	}

	// Check if remote is at previous state and safe to retry
	remoteMatchesPrevious := (payload.DesiredIPLimit == nil || remote.LimitIP == payload.PreviousIPLimit) &&
		(payload.DesiredExpireTime == nil || remote.ExpiryTime == payload.PreviousExpireTime) &&
		(payload.DesiredIsActive == nil || remote.Enable == payload.PreviousIsActive)

	if remoteMatchesPrevious {
		// Remote is at previous state. Retry mutation using full client merge preserving non-bot fields.
		clientConfig := xui.ClientConfig{
			ID:         remote.UUID,
			Email:      remote.Email,
			SubID:      remote.SubID,
			Enable:     desiredActive,
			ExpiryTime: desiredExpiry,
			LimitIP:    desiredIP,
			TotalGB:    remote.TotalGB,
			Flow:       remote.Flow,
		}
		res := p.XUI.UpdateClientResult(payload.ClientEmail, clientConfig)
		if res.Outcome == xui.WriteSucceeded {
			// Verify readback
			verifyRemote, verifyErr := p.XUI.GetClientByEmail(payload.ClientEmail)
			if verifyErr == nil && verifyRemote != nil &&
				(payload.DesiredIPLimit == nil || verifyRemote.LimitIP == desiredIP) &&
				(payload.DesiredExpireTime == nil || verifyRemote.ExpiryTime == desiredExpiry) &&
				(payload.DesiredIsActive == nil || verifyRemote.Enable == desiredActive) {
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
					return "", fmt.Errorf("failed to commit db subscription %d after retry: %w", sub.ID, err)
				}
				return fmt.Sprintf("retried remote update for %s and verified readback", payload.ClientEmail), nil
			}
		}
		return "", fmt.Errorf("remote update retry failed or inconclusive for %s: %v", payload.ClientEmail, res.Err)
	}

	// Remote state is in an unexpected divergent state! Move to manual review.
	_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("remote client %s state is unexpected (remote IP=%d, Expiry=%d, Active=%t)", payload.ClientEmail, remote.LimitIP, remote.ExpiryTime, remote.Enable))
	return "manual review: divergent remote state", nil
}

func (p *Processor) handleDirectPaymentProvisioning(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	if rec.PurchaseRequestID == nil {
		return "", errors.New("purchase_request_id is missing")
	}
	req, err := db.GetPurchaseRequestByID(ctx, *rec.PurchaseRequestID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch purchase request: %w", err)
	}
	if req == nil {
		return "purchase request not found; resolved", nil
	}
	if req.ProvisioningStatus == db.PurchaseProvisioningSucceeded {
		return "already provisioned successfully", nil
	}
	if req.Status != "approved" {
		return "purchase request not approved; skipping provisioning", nil
	}

	if p.XUI == nil {
		return "", errors.New("xui client not available")
	}

	remote, err := p.XUI.GetClientByEmail(req.ClientEmail)
	presence := ClassifyRemoteClient(remote, err)

	switch presence {
	case RemoteConfirmedPresent:
		// Verify identity before adopting!
		if idErr := verifyClientIdentity(remote, req.ClientEmail, "", ""); idErr != nil {
			_ = db.MarkReconciliationManualReview(ctx, rec.ID, fmt.Sprintf("direct payment client identity verification failed: %v", idErr))
			return "manual review: identity mismatch", nil
		}

		existing, checkErr := db.GetSubscriptionByEmail(ctx, req.ClientEmail)
		if checkErr != nil {
			return "", fmt.Errorf("failed to check existing subscription: %w", checkErr)
		}
		clientUUID := remote.UUID
		if clientUUID == "" {
			clientUUID = fmt.Sprintf("%d", remote.ID)
		}
		if existing == nil {
			var planID *int
			if req.PlanID != nil {
				id := int(*req.PlanID)
				planID = &id
			}
			sub := &db.Subscription{
				UserID:            req.UserID,
				PlanID:            planID,
				QuoteID:           req.QuoteID,
				ClientEmail:       req.ClientEmail,
				ClientUUID:        clientUUID,
				SubID:             remote.SubID,
				Status:            db.SubscriptionStatusActive,
				PlanType:          db.PlanTypePaid,
				DisplayName:       req.CustomName,
				IPLimit:           req.IPLimit,
				ExpireTime:        &remote.ExpiryTime,
				IsActive:          remote.Enable,
				StartDate:         time.Now().UTC(),
				TrafficLimitBytes: remote.TotalGB,
			}
			if remote.ExpiryTime > 0 {
				sub.EndDate = time.UnixMilli(remote.ExpiryTime)
			}
			if err := db.CreateSubscription(ctx, sub); err != nil {
				_ = db.CreateReconciliationRecord(ctx, &db.ReconciliationRecord{
					UserID:            &req.UserID,
					PurchaseRequestID: &req.ID,
					Kind:              KindPurchaseRemoteCreatedDbFailed,
					Status:            "reconciliation_required",
					OperationKey:      fmt.Sprintf("direct_pay_db_fail:%d", req.ID),
					DesiredState: map[string]any{
						"user_id":             req.UserID,
						"purchase_request_id": req.ID,
						"email":               req.ClientEmail,
						"uuid":                remote.UUID,
						"sub_id":              remote.SubID,
						"ip_limit":            req.IPLimit,
						"months":              req.Months,
						"display_name":        req.CustomName,
					},
				})
				return "", fmt.Errorf("failed to create db subscription for remote client %s: %w", req.ClientEmail, err)
			}
		}

		if statusErr := db.SetPurchaseProvisioningStatus(ctx, req.ID, db.PurchaseProvisioningSucceeded); statusErr != nil {
			return "", fmt.Errorf("failed to set provisioning status succeeded: %w", statusErr)
		}
		return fmt.Sprintf("direct payment client %s confirmed, adopted into subscription", req.ClientEmail), nil

	case RemoteConfirmedAbsent:
		// Client absent remotely. Create client in XUI first.
		var inbounds []int
		if req.PlanID != nil {
			paidPlan, pErr := db.GetPaidPlanByID(ctx, int64(*req.PlanID))
			if pErr == nil && paidPlan != nil {
				inbounds = paidPlan.InboundIDs
			}
		}
		expiryMilli := int64(0)
		if req.Months > 0 {
			expiryMilli = time.Now().Add(time.Duration(req.Months) * 30 * 24 * time.Hour).UnixMilli()
		}
		totalBytes := int64(req.DataGB) * 1024 * 1024 * 1024
		newUUID := generateUUID()
		newSubID := generateSubID()

		addReq := xui.AddClientRequest{
			InboundIDs: inbounds,
			Client: xui.ClientConfig{
				ID:         newUUID,
				Email:      req.ClientEmail,
				SubID:      newSubID,
				Enable:     true,
				ExpiryTime: expiryMilli,
				LimitIP:    req.IPLimit,
				TotalGB:    totalBytes,
			},
		}

		writeRes := p.XUI.AddClientResult(addReq)
		if writeRes.Outcome != xui.WriteSucceeded {
			return "", fmt.Errorf("failed to add remote client %s: %v", req.ClientEmail, writeRes.Err)
		}

		// Remote created! Now create subscription in local DB
		var planID *int
		if req.PlanID != nil {
			id := int(*req.PlanID)
			planID = &id
		}
		sub := &db.Subscription{
			UserID:            req.UserID,
			PlanID:            planID,
			QuoteID:           req.QuoteID,
			ClientEmail:       req.ClientEmail,
			ClientUUID:        newUUID,
			SubID:             newSubID,
			Status:            db.SubscriptionStatusActive,
			PlanType:          db.PlanTypePaid,
			DisplayName:       req.CustomName,
			IPLimit:           req.IPLimit,
			ExpireTime:        &expiryMilli,
			IsActive:          true,
			StartDate:         time.Now().UTC(),
			TrafficLimitBytes: totalBytes,
		}
		if expiryMilli > 0 {
			sub.EndDate = time.UnixMilli(expiryMilli)
		}

		if err := db.CreateSubscription(ctx, sub); err != nil {
			_ = db.CreateReconciliationRecord(ctx, &db.ReconciliationRecord{
				UserID:            &req.UserID,
				PurchaseRequestID: &req.ID,
				Kind:              KindPurchaseRemoteCreatedDbFailed,
				Status:            "reconciliation_required",
				OperationKey:      fmt.Sprintf("direct_pay_db_fail:%d", req.ID),
				DesiredState: map[string]any{
					"user_id":             req.UserID,
					"purchase_request_id": req.ID,
					"email":               req.ClientEmail,
					"uuid":                newUUID,
					"sub_id":              newSubID,
					"ip_limit":            req.IPLimit,
					"months":              req.Months,
					"display_name":        req.CustomName,
				},
			})
			return "", fmt.Errorf("remote client %s created but local db subscription failed: %w", req.ClientEmail, err)
		}

		if statusErr := db.SetPurchaseProvisioningStatus(ctx, req.ID, db.PurchaseProvisioningSucceeded); statusErr != nil {
			return "", fmt.Errorf("failed to set provisioning status succeeded: %w", statusErr)
		}
		return fmt.Sprintf("direct payment client %s created and provisioned successfully", req.ClientEmail), nil

	default: // RemotePresenceUnknown
		return "", fmt.Errorf("inconclusive check for direct payment client %s: %w", req.ClientEmail, err)
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
