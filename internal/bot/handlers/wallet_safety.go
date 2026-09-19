package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

var ErrXUIClientUnavailable = errors.New("x-ui client is not initialized")

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
	amount float64,
	description string,
	originalOpKey string,
	refundOpKey string,
	subID *int64,
	extra map[string]any,
	creditFn func(ctx context.Context, userID int64, amount float64, description, operationKey string) error,
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

	desired := map[string]any{
		"action":                 "refund",
		"original_operation_key": originalOpKey,
		"refund_operation_key":   refundOpKey,
		"user_id":                userID,
		"amount":                 amount,
		"description":            description,
	}
	for k, v := range extra {
		desired[k] = v
	}

	record := &db.ReconciliationRecord{
		OperationKey:   refundOpKey,
		Kind:           "pending_refund",
		UserID:         &userID,
		SubscriptionID: subID,
		DesiredState:   desired,
		ObservedState:  map[string]any{"refund_error": err.Error()},
		Status:         "pending_refund",
		ErrorMessage:   err.Error(),
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
	amount float64,
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
	price float64,
	operationKey string,
	dbErr error,
	deleteFn func(email string) error,
	verifyFn func(email string) (*xui.XUIClientInfo, error),
	refundFn func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) SafeRefundResult,
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
			Outcome:   CompensationReconciliationRequired,
			Refunded:  false,
			DBErr:     dbErr,
			DeleteErr: deleteErr,
			RefundErr: refundRes.RefundErr,
			ReconErr:  refundRes.ReconciliationErr,
		}

	case deleteReconciliationRequired:
		desired := map[string]any{
			"action":               "confirm_delete_and_refund",
			"email":                client.Email,
			"client_id":            client.ID,
			"client_uuid":          client.ID,
			"sub_id":               client.SubID,
			"inbound_ids":          inboundIDs,
			"enable":               client.Enable,
			"expiry_time":          client.ExpiryTime,
			"limit_ip":             client.LimitIP,
			"total_gb":             client.TotalGB,
			"plan_id":              plan.ID,
			"plan_name":            plan.Name,
			"user_id":              user.ID,
			"display_name":         displayName,
			"price":                price,
			"operation_key":        operationKey,
			"refund_operation_key": operationKey + ":refund",
			"db_error":             dbErr.Error(),
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
		rec := &db.ReconciliationRecord{
			OperationKey:  operationKey + ":compensating_delete_unknown",
			Kind:          "purchase_remote_created_db_failed",
			UserID:        &user.ID,
			DesiredState:  desired,
			ObservedState: observed,
			Status:        "reconciliation_required",
			ErrorMessage:  fmt.Sprintf("local DB failed: %v; remote delete outcome unknown: %v", dbErr, resErr),
		}
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
		desired := map[string]any{
			"action":               "adopt_subscription_or_delete",
			"email":                client.Email,
			"client_id":            client.ID,
			"client_uuid":          client.ID,
			"sub_id":               client.SubID,
			"inbound_ids":          inboundIDs,
			"enable":               client.Enable,
			"expiry_time":          client.ExpiryTime,
			"limit_ip":             client.LimitIP,
			"total_gb":             client.TotalGB,
			"plan_id":              plan.ID,
			"plan_name":            plan.Name,
			"user_id":              user.ID,
			"display_name":         displayName,
			"price":                price,
			"operation_key":        operationKey,
			"refund_operation_key": operationKey + ":refund",
			"db_error":             dbErr.Error(),
		}
		observed := map[string]any{
			"remote_created": true,
			"client_present": true,
		}
		if deleteErr != nil {
			observed["delete_error"] = deleteErr.Error()
		}
		rec := &db.ReconciliationRecord{
			OperationKey:  operationKey + ":client_present_db_failed",
			Kind:          "purchase_remote_created_db_failed",
			UserID:        &user.ID,
			DesiredState:  desired,
			ObservedState: observed,
			Status:        "reconciliation_required",
			ErrorMessage:  fmt.Sprintf("local DB failed: %v; remote client remains present on panel", dbErr),
		}
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
