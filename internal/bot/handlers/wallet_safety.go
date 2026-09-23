package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/services/reconcile"
	"xui-reseller-bot/internal/xui"
)

var ErrXUIClientUnavailable = errors.New("پنل سرویس‌دهنده در دسترس نیست")

type walletRemoteOutcome string

const (
	walletRemoteSucceeded  walletRemoteOutcome = "succeeded"
	walletRemoteUnknown    walletRemoteOutcome = "unknown"
	walletRemoteDefinitive walletRemoteOutcome = "definitive_failure"
)

func classifyWalletRemoteOutcome(err error) walletRemoteOutcome {
	if err == nil {
		return walletRemoteSucceeded
	}
	if xui.IsUnknownOutcome(err) {
		return walletRemoteUnknown
	}
	return walletRemoteDefinitive
}

func walletRemoteRefundAllowed(err error) bool {
	return classifyWalletRemoteOutcome(err) == walletRemoteDefinitive
}

func walletRemoteReconciliationRequired(err error) bool {
	return classifyWalletRemoteOutcome(err) == walletRemoteUnknown
}

func walletLocalWriteReconciliationRequired(err error) bool {
	return err != nil
}

type subscriptionWalletState struct {
	ipLimit    int
	endDate    time.Time
	expireTime *int64
	isActive   bool
}

func snapshotSubscriptionWalletState(sub *db.Subscription) subscriptionWalletState {
	state := subscriptionWalletState{}
	if sub == nil {
		return state
	}
	state.ipLimit = sub.IPLimit
	state.endDate = sub.EndDate
	state.isActive = sub.IsActive
	if sub.ExpireTime != nil {
		expiry := *sub.ExpireTime
		state.expireTime = &expiry
	}
	return state
}

func restoreSubscriptionWalletState(sub *db.Subscription, state subscriptionWalletState) {
	if sub == nil {
		return
	}
	sub.IPLimit = state.ipLimit
	sub.EndDate = state.endDate
	sub.IsActive = state.isActive
	if state.expireTime == nil {
		sub.ExpireTime = nil
		return
	}
	expiry := *state.expireTime
	sub.ExpireTime = &expiry
}

type SafeRefundResult struct {
	Refunded                bool
	AlreadyRefunded         bool
	RefundErr               error
	ReconciliationPersisted bool
	ReconciliationErr       error
}

// safeRefundWallet credits the user's wallet with an idempotent operation key.
// If the credit succeeds or was already applied, it returns Refunded=true.
// If the DB credit fails, it records a durable pending_refund reconciliation marker
// and returns the outcome with explicit persistence observability.
func safeRefundWalletWithDeps(
	ctx context.Context,
	userID int64,
	amount int64,
	description string,
	originalOpKey string,
	refundOpKey string,
	subID *int64,
	extra map[string]any,
	creditFn func(ctx context.Context, userID int64, amount int64, description, operationKey string) error,
	persistReconFn func(ctx context.Context, record *db.ReconciliationRecord) error,
) SafeRefundResult {
	if creditFn == nil {
		creditFn = db.CreditWalletBalanceWithKey
	}
	if persistReconFn == nil {
		persistReconFn = db.CreateReconciliationRecord
	}

	err := creditFn(ctx, userID, amount, description, refundOpKey)
	if err == nil {
		return SafeRefundResult{
			Refunded:        true,
			AlreadyRefunded: false,
		}
	}
	if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
		return SafeRefundResult{
			Refunded:        true,
			AlreadyRefunded: true,
		}
	}

	record := reconcile.NewPendingRefundRecord(userID, subID, amount, refundOpKey, description, err.Error())
	record.DesiredState["action"] = "refund"
	record.DesiredState["original_operation_key"] = originalOpKey
	record.DesiredState["refund_operation_key"] = refundOpKey
	for k, v := range extra {
		record.DesiredState[k] = v
	}
	record.ObservedState = map[string]any{"refund_error": err.Error()}
	record.ErrorMessage = err.Error()
	if errors.Is(err, db.ErrWalletOperationConflict) {
		record.Status = db.ReconciliationStatusManualReview
		record.ManualReviewReason = "refund operation key exists with a different economic effect: " + err.Error()
	}
	recErr := persistReconFn(ctx, record)
	if recErr != nil {
		log.Printf("[CRITICAL] failed to persist pending refund reconciliation for user %d, key %s: %v", userID, refundOpKey, recErr)
		return SafeRefundResult{
			Refunded:                false,
			RefundErr:               err,
			ReconciliationPersisted: false,
			ReconciliationErr:       recErr,
		}
	}

	return SafeRefundResult{
		Refunded:                false,
		RefundErr:               err,
		ReconciliationPersisted: true,
	}
}

func safeRefundWallet(
	ctx context.Context,
	userID int64,
	amount int64,
	description string,
	originalOpKey string,
	refundOpKey string,
	subID *int64,
	extra map[string]any,
) SafeRefundResult {
	return safeRefundWalletWithDeps(ctx, userID, amount, description, originalOpKey, refundOpKey, subID, extra, nil, nil)
}

type RemoteCreateCompensationOutcome string

const (
	CompensationRefunded               RemoteCreateCompensationOutcome = "refunded"
	CompensationRefundPending          RemoteCreateCompensationOutcome = "refund_pending"
	CompensationReconciliationRequired RemoteCreateCompensationOutcome = "reconciliation_required"
	CompensationClientStillPresent     RemoteCreateCompensationOutcome = "client_still_present"
)

type RemoteCreateCompensationResult struct {
	Outcome   RemoteCreateCompensationOutcome
	DeleteErr error
	VerifyErr error
	DBErr     error
	RefundErr error
	ReconErr  error
	Refunded  bool
}

type paidSubscriptionCompensationError struct {
	Result RemoteCreateCompensationResult
}

func (e *paidSubscriptionCompensationError) Error() string {
	if e == nil {
		return "compensation error"
	}
	return fmt.Sprintf("subscription DB save failed (%v); compensation outcome: %s", e.Result.DBErr, e.Result.Outcome)
}

func formatCompensationUserMessage(res RemoteCreateCompensationResult, operationKey string) string {
	switch res.Outcome {
	case CompensationRefunded:
		return "خطا در ثبت نهایی اشتراک در دیتابیس رخ داد. سرویس ایجاد شده در پنل خنثی شد و مبلغ پرداختی به کیف پول شما عودت داده شد."
	case CompensationRefundPending:
		if res.ReconErr != nil {
			return fmt.Sprintf("سرویس ایجاد شده در پنل با موفقیت حذف شد، اما استرداد وجه به کیف پول ناموفق بود و ثبت خودکار درخواست تطبیق نیز با خطا مواجه شد (%v). هیچ درخواستی به‌طور خودکار ثبت نشده است؛ لطفاً فوراً با شناسه پیگیری زیر با پشتیبانی تماس بگیرید:\n%s", res.ReconErr, operationKey)
		}
		return fmt.Sprintf("سرویس ایجاد شده در پنل با موفقیت حذف شد، اما عودت خودکار مبلغ به کیف پول انجام نشد. درخواست استرداد وجه برای بررسی پشتیبانی ثبت شد؛ شناسه پیگیری شما:\n%s", operationKey)
	case CompensationReconciliationRequired:
		if res.ReconErr != nil {
			return fmt.Sprintf("خطا در ثبت نهایی اشتراک رخ داد و وضعیت حذف سرویس از پنل نامشخص است؛ همچنین ثبت خودکار درخواست در سیستم نیز با خطا مواجه شد (%v). هیچ درخواستی به‌طور خودکار ثبت نشده است. لطفا فورا با پشتیبانی تماس گرفته و شناسه پیگیری زیر را ارسال کنید:\n%s", res.ReconErr, operationKey)
		}
		return "خطا در ثبت نهایی اشتراک رخ داد و وضعیت حذف سرویس از پنل نامشخص است؛ جهت حفظ حقوق شما، مبلغ در کیف پول محفوظ ماند و درخواست برای بررسی پشتیبانی ثبت شد."
	case CompensationClientStillPresent:
		if res.ReconErr != nil {
			return fmt.Sprintf("سرویس در پنل فعال شد اما ثبت آن در سیستم با خطا مواجه گردید؛ همچنین ثبت خودکار وضعیت برای پشتیبانی نیز با خطا مواجه شد (%v). هیچ درخواستی به‌طور خودکار ثبت نشده است. لطفا فورا با پشتیبانی تماس گرفته و شناسه پیگیری زیر را ارسال کنید:\n%s", res.ReconErr, operationKey)
		}
		return "سرویس در پنل فعال شد اما ثبت آن در سیستم با خطا مواجه گردید. سرویس در سرور فعال باقی مانده و هزینه کسر شده برای بررسی و تطبیق توسط پشتیبانی ثبت شد."
	default:
		return "خطا در پردازش اشتراک. وضعیت جهت بررسی ثبت شد."
	}
}

func compensateRemoteCreateDbFailure(
	ctx context.Context,
	user *db.User,
	plan *db.PaidPlan,
	client xui.ClientConfig,
	inboundIDs []int,
	displayName string,
	price int64,
	operationKey string,
	dbErr error,
	deleteFn func(email string) error,
	verifyFn func(email string) (*xui.XUIClientInfo, error),
	refundFn func(ctx context.Context, userID int64, amount int64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult,
	persistReconFn func(ctx context.Context, rec *db.ReconciliationRecord) error,
) RemoteCreateCompensationResult {
	if deleteFn == nil {
		deleteFn = func(e string) error {
			return ErrXUIClientUnavailable
		}
	}
	if refundFn == nil {
		refundFn = safeRefundWallet
	}
	if persistReconFn == nil {
		persistReconFn = db.CreateReconciliationRecord
	}

	deleteErr := deleteFn(client.Email)
	resolution, resErr := resolveDeleteOutcome(deleteErr, func() (*xui.XUIClientInfo, error) {
		if verifyFn == nil {
			return nil, errors.New("delete verification was not available")
		}
		return verifyFn(client.Email)
	})

	switch resolution {
	case deleteConfirmed:
		refundRes := refundFn(ctx, user.ID, price, "refund for failed purchase subscription persistence: "+client.Email, operationKey, operationKey+":refund", nil, map[string]any{"email": client.Email, "plan_id": plan.ID})
		if refundRes.Refunded {
			return RemoteCreateCompensationResult{
				Outcome:   CompensationRefunded,
				Refunded:  true,
				DBErr:     dbErr,
				DeleteErr: deleteErr,
			}
		}
		return RemoteCreateCompensationResult{
			Outcome:   CompensationRefundPending,
			Refunded:  false,
			DBErr:     dbErr,
			DeleteErr: deleteErr,
			RefundErr: refundRes.RefundErr,
			ReconErr:  refundRes.ReconciliationErr,
		}

	case deleteReconciliationRequired:
		planID := int(plan.ID)
		months := 1
		if client.ExpiryTime < 0 {
			months = int((-client.ExpiryTime) / (30 * 24 * 3600 * 1000))
		}
		if months <= 0 {
			months = 1
		}
		dataGB := int(client.TotalGB / (1024 * 1024 * 1024))
		payload := &reconcile.PurchaseProvisioningPayload{
			UserID:             user.ID,
			OperationKey:       operationKey,
			DebitOperationKey:  operationKey,
			Email:              client.Email,
			ExpectedUUID:       client.ID,
			ExpectedSubID:      client.SubID,
			PlanID:             &planID,
			InboundIDs:         inboundIDs,
			Months:             months,
			IPLimit:            client.LimitIP,
			DataGB:             dataGB,
			Price:              price,
			RefundOperationKey: operationKey + ":refund",
			DisplayName:        displayName,
		}

		observed := map[string]any{
			"remote_created": true,
			"delete_outcome": "unknown",
		}
		if deleteErr != nil {
			observed["delete_error"] = deleteErr.Error()
		}
		if resErr != nil {
			observed["resolution_error"] = resErr.Error()
		}
		rec := reconcile.NewPurchaseRemoteCreatedDbFailedRecord(payload)
		rec.DesiredState["action"] = "confirm_delete_and_refund"
		rec.DesiredState["client_id"] = client.ID
		rec.DesiredState["client_uuid"] = client.ID
		rec.DesiredState["sub_id"] = client.SubID
		rec.DesiredState["plan_name"] = plan.Name
		rec.DesiredState["enable"] = client.Enable
		rec.DesiredState["expiry_time"] = client.ExpiryTime
		rec.DesiredState["limit_ip"] = client.LimitIP
		rec.DesiredState["total_gb"] = client.TotalGB
		if dbErr != nil {
			rec.DesiredState["db_error"] = dbErr.Error()
		}
		rec.OperationKey = operationKey + ":compensating_delete_unknown"
		rec.ObservedState = observed
		rec.Status = "reconciliation_required"
		rec.ErrorMessage = fmt.Sprintf("local DB failed: %v; remote delete outcome unknown: %v", dbErr, resErr)
		reconErr := persistReconFn(ctx, rec)
		return RemoteCreateCompensationResult{
			Outcome:   CompensationReconciliationRequired,
			DeleteErr: deleteErr,
			VerifyErr: resErr,
			DBErr:     dbErr,
			ReconErr:  reconErr,
			Refunded:  false,
		}

	default: // deleteStillPresent or deleteDefinitiveFailure
		planID := int(plan.ID)
		months := 1
		if client.ExpiryTime < 0 {
			months = int((-client.ExpiryTime) / (30 * 24 * 3600 * 1000))
		}
		if months <= 0 {
			months = 1
		}
		dataGB := int(client.TotalGB / (1024 * 1024 * 1024))
		payload := &reconcile.PurchaseProvisioningPayload{
			UserID:             user.ID,
			OperationKey:       operationKey,
			DebitOperationKey:  operationKey,
			Email:              client.Email,
			ExpectedUUID:       client.ID,
			ExpectedSubID:      client.SubID,
			PlanID:             &planID,
			InboundIDs:         inboundIDs,
			Months:             months,
			IPLimit:            client.LimitIP,
			DataGB:             dataGB,
			Price:              price,
			RefundOperationKey: operationKey + ":refund",
			DisplayName:        displayName,
		}

		observed := map[string]any{
			"remote_created": true,
			"client_present": true,
		}
		if deleteErr != nil {
			observed["delete_error"] = deleteErr.Error()
		}
		rec := reconcile.NewPurchaseRemoteCreatedDbFailedRecord(payload)
		rec.DesiredState["action"] = "adopt_subscription_or_delete"
		rec.DesiredState["client_id"] = client.ID
		rec.DesiredState["client_uuid"] = client.ID
		rec.DesiredState["sub_id"] = client.SubID
		rec.DesiredState["plan_name"] = plan.Name
		rec.DesiredState["enable"] = client.Enable
		rec.DesiredState["expiry_time"] = client.ExpiryTime
		rec.DesiredState["limit_ip"] = client.LimitIP
		rec.DesiredState["total_gb"] = client.TotalGB
		if dbErr != nil {
			rec.DesiredState["db_error"] = dbErr.Error()
		}
		rec.OperationKey = operationKey + ":client_present_db_failed"
		rec.ObservedState = observed
		rec.Status = "reconciliation_required"
		rec.ErrorMessage = fmt.Sprintf("local DB failed: %v; remote client remains present on panel", dbErr)
		reconErr := persistReconFn(ctx, rec)
		return RemoteCreateCompensationResult{
			Outcome:   CompensationClientStillPresent,
			DeleteErr: deleteErr,
			DBErr:     dbErr,
			ReconErr:  reconErr,
			Refunded:  false,
		}
	}
}
