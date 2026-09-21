package reconcile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type XUIClient interface {
	GetClientByEmail(email string) (*xui.XUIClientInfo, error)
	DeleteClient(email string) error
	AddClient(req xui.AddClientRequest) error
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
	case "pending_refund":
		resolution, err = p.handlePendingRefund(ctx, rec)
	case "purchase_provisioning_unknown", "purchase_remote_created_db_failed":
		resolution, err = p.handlePurchaseReconciliation(ctx, rec)
	case "subscription_update_db_failed":
		resolution, err = p.handleUpdateReconciliation(ctx, rec)
	case "subscription_delete_unknown", "subscription_cancellation_db_failure":
		resolution, err = p.handleDeleteReconciliation(ctx, rec)
	case "direct_payment_provisioning_retry":
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

func (p *Processor) handlePendingRefund(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	if rec.UserID == nil {
		return "", errors.New("user_id is missing in pending_refund record")
	}
	desired := rec.DesiredState
	amountVal, ok := desired["amount"]
	if !ok {
		return "", errors.New("amount missing in desired_state")
	}

	var amount float64
	switch v := amountVal.(type) {
	case float64:
		amount = v
	case int64:
		amount = float64(v)
	case int:
		amount = float64(v)
	case string:
		amount, _ = strconv.ParseFloat(v, 64)
	}

	if amount <= 0 {
		return "", errors.New("invalid refund amount in desired_state")
	}

	refundOpKey := fmt.Sprintf("%v", desired["refund_operation_key"])
	if refundOpKey == "" {
		refundOpKey = rec.OperationKey + ":refund"
	}
	desc := fmt.Sprintf("%v", desired["description"])
	if desc == "" {
		desc = "reconciliation wallet refund"
	}

	creditFn := p.CreditFn
	if creditFn == nil {
		creditFn = db.CreditWalletBalanceWithKey
	}

	err := creditFn(ctx, *rec.UserID, amount, desc, refundOpKey)
	if err == nil || errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
		return fmt.Sprintf("wallet refunded %.0f Toman (key: %s)", amount, refundOpKey), nil
	}
	return "", err
}

func (p *Processor) handlePurchaseReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	desired := rec.DesiredState
	email := fmt.Sprintf("%v", desired["email"])
	if email == "" {
		return "", errors.New("email is missing in purchase reconciliation desired_state")
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(email)
	switch {
	case err == nil && remote != nil:
		// Remote client confirmed on panel!
		existing, checkErr := db.GetSubscriptionByEmail(ctx, email)
		if checkErr != nil {
			return "", fmt.Errorf("failed to check existing db subscription: %w", checkErr)
		}
		if existing != nil {
			return fmt.Sprintf("remote client %s and subscription %d both exist", email, existing.ID), nil
		}

		userIDVal := rec.UserID
		if userIDVal == nil {
			return "", errors.New("user_id missing for subscription adoption")
		}

		var planID *int
		if pID, ok := desired["plan_id"]; ok && pID != nil {
			if idInt, err := strconv.Atoi(fmt.Sprintf("%v", pID)); err == nil {
				planID = &idInt
			}
		}

		displayName := fmt.Sprintf("%v", desired["display_name"])
		if displayName == "" {
			displayName = email
		}
		ipLimit := remote.LimitIP
		if ipLimit <= 0 {
			ipLimit = 1
		}

		sub := &db.Subscription{
			UserID:            *userIDVal,
			PlanID:            planID,
			ClientEmail:       remote.Email,
			ClientUUID:        remote.UUID,
			SubID:             remote.SubID,
			Status:            "active",
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
			return "", fmt.Errorf("failed to adopt subscription %s: %w", email, err)
		}
		return fmt.Sprintf("remote client %s confirmed, adopted into subscription %d", email, sub.ID), nil

	case xui.IsNotFound(err):
		priceVal, ok := desired["price"]
		if !ok {
			return "", errors.New("price missing in desired_state for refund")
		}
		var price float64
		switch v := priceVal.(type) {
		case float64:
			price = v
		case int64:
			price = float64(v)
		case string:
			price, _ = strconv.ParseFloat(v, 64)
		}

		if price <= 0 || rec.UserID == nil {
			return "remote client absent, no refund needed", nil
		}

		refundOpKey := fmt.Sprintf("%v", desired["refund_operation_key"])
		if refundOpKey == "" {
			refundOpKey = rec.OperationKey + ":refund"
		}

		creditFn := p.CreditFn
		if creditFn == nil {
			creditFn = db.CreditWalletBalanceWithKey
		}

		err := creditFn(ctx, *rec.UserID, price, "refund for failed purchase: "+email, refundOpKey)
		if err == nil || errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return fmt.Sprintf("remote client absent, refunded %.0f Toman (key: %s)", price, refundOpKey), nil
		}
		return "", err

	default:
		return "", fmt.Errorf("inconclusive remote existence check for %s: %w", email, err)
	}
}

func (p *Processor) handleUpdateReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	if rec.SubscriptionID == nil {
		return "", errors.New("subscription_id missing for update reconciliation")
	}

	sub, err := db.GetSubscriptionByID(ctx, int(*rec.SubscriptionID))
	if err != nil || sub == nil {
		return "", fmt.Errorf("subscription not found: %w", err)
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(sub.ClientEmail)
	if err != nil {
		return "", fmt.Errorf("failed to get remote client %s: %w", sub.ClientEmail, err)
	}

	sub.IPLimit = remote.LimitIP
	sub.IsActive = remote.Enable
	sub.ExpireTime = &remote.ExpiryTime
	if remote.ExpiryTime > 0 {
		sub.EndDate = time.UnixMilli(remote.ExpiryTime)
	}
	sub.Status = db.SubscriptionStatusActive
	sub.DesiredIPLimit = nil
	sub.DesiredExpireTime = nil
	sub.DesiredIsActive = nil
	sub.ReconciliationNote = ""

	if err := db.UpdateSubscription(ctx, sub); err != nil {
		return "", fmt.Errorf("failed to sync db subscription %d: %w", sub.ID, err)
	}
	return fmt.Sprintf("synced subscription %d with remote panel state", sub.ID), nil
}

func (p *Processor) handleDeleteReconciliation(ctx context.Context, rec *db.ReconciliationRecord) (string, error) {
	desired := rec.DesiredState
	email := fmt.Sprintf("%v", desired["email"])
	if email == "" {
		return "", errors.New("email missing for delete reconciliation")
	}

	if p.XUI == nil {
		return "", errors.New("xui client is not available")
	}

	remote, err := p.XUI.GetClientByEmail(email)
	if xui.IsNotFound(err) || remote == nil {
		if rec.SubscriptionID != nil {
			_ = db.DeleteSubscription(ctx, int(*rec.SubscriptionID))
		}
		p.tryRefundFromDesired(ctx, rec)
		return fmt.Sprintf("remote client %s confirmed deleted, local record cleaned", email), nil
	}
	if err != nil {
		return "", fmt.Errorf("inconclusive delete check for %s: %w", email, err)
	}

	delErr := p.XUI.DeleteClient(email)
	if delErr == nil || xui.IsNotFound(delErr) {
		if rec.SubscriptionID != nil {
			_ = db.DeleteSubscription(ctx, int(*rec.SubscriptionID))
		}
		p.tryRefundFromDesired(ctx, rec)
		return fmt.Sprintf("remote client %s deleted on retry", email), nil
	}

	return "", fmt.Errorf("remote client %s still present after retry delete: %w", email, delErr)
}

func (p *Processor) tryRefundFromDesired(ctx context.Context, rec *db.ReconciliationRecord) {
	if rec.UserID == nil || rec.DesiredState == nil {
		return
	}
	amountVal, ok := rec.DesiredState["amount"]
	if !ok {
		amountVal, ok = rec.DesiredState["refund_amount"]
	}
	if !ok {
		return
	}
	var amount float64
	switch v := amountVal.(type) {
	case float64:
		amount = v
	case int64:
		amount = float64(v)
	case int:
		amount = float64(v)
	case string:
		amount, _ = strconv.ParseFloat(v, 64)
	}
	if amount <= 0 {
		return
	}
	refundOpKey := fmt.Sprintf("%v", rec.DesiredState["refund_operation_key"])
	if refundOpKey == "" {
		refundOpKey = rec.OperationKey + ":refund"
	}
	creditFn := p.CreditFn
	if creditFn == nil {
		creditFn = db.CreditWalletBalanceWithKey
	}
	_ = creditFn(ctx, *rec.UserID, amount, "refund for cancelled subscription", refundOpKey)
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
	if err == nil && remote != nil {
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
				return "", fmt.Errorf("failed to save subscription: %w", err)
			}
		}
		_ = db.SetPurchaseProvisioningStatus(ctx, req.ID, db.PurchaseProvisioningSucceeded)
		return fmt.Sprintf("adopted existing remote client %s for purchase request %d", req.ClientEmail, req.ID), nil
	}

	if req.PlanID == nil {
		return "", errors.New("plan_id missing in purchase request")
	}
	plan, err := db.GetPaidPlanByID(ctx, *req.PlanID)
	if err != nil || plan == nil {
		return "", fmt.Errorf("paid plan not found: %w", err)
	}

	clientUUID := generateUUID()
	subID := fmt.Sprintf("sub_%d_%d", req.ID, time.Now().UnixNano()%1000000)
	var expireMilli int64 = -int64(req.Months * 30 * 24 * 3600 * 1000)
	var trafficBytes int64
	if plan.IsLimited {
		trafficBytes = int64(req.DataGB) * 1073741824
	}

	addReq := xui.AddClientRequest{
		InboundIDs: plan.InboundIDs,
		Client: xui.ClientConfig{
			ID:         clientUUID,
			Email:      req.ClientEmail,
			SubID:      subID,
			Enable:     true,
			ExpiryTime: expireMilli,
			LimitIP:    req.IPLimit,
			TotalGB:    trafficBytes,
			Flow:       plan.Flow,
		},
	}
	if err := p.XUI.AddClient(addReq); err != nil {
		return "", fmt.Errorf("failed to add client on panel: %w", err)
	}

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
		SubID:             subID,
		Status:            db.SubscriptionStatusActive,
		PlanType:          db.PlanTypePaid,
		DisplayName:       req.CustomName,
		IPLimit:           req.IPLimit,
		ExpireTime:        &expireMilli,
		IsActive:          true,
		StartDate:         time.Now().UTC(),
		TrafficLimitBytes: trafficBytes,
	}
	if err := db.CreateSubscription(ctx, sub); err != nil {
		return "", fmt.Errorf("client created on panel but DB subscription save failed: %w", err)
	}
	_ = db.SetPurchaseProvisioningStatus(ctx, req.ID, db.PurchaseProvisioningSucceeded)
	return fmt.Sprintf("successfully provisioned purchase request %d", req.ID), nil
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:])
}
