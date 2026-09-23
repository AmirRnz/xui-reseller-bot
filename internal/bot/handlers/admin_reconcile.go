package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/services/reconcile"
)

func RegisterAdminReconcile(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_reconcile", HandleAdminReconcile, auth, admin)
	b.Handle("\fadmin_reconcile_run", HandleAdminReconcileRun, auth, admin)
	b.Handle("\fadmin_reconcile_manual", HandleAdminReconcileManual, auth, admin)
	b.Handle("\fadmin_reconcile_detail", HandleAdminReconcileDetail, auth, admin)
	b.Handle("\fadmin_reconcile_retry", HandleAdminReconcileRetry, auth, admin)
	b.Handle("\fadmin_reconcile_mark_manual", HandleAdminReconcileMarkManual, auth, admin)
	b.Handle("\fadmin_reconcile_close", HandleAdminReconcileClosePrompt, auth, admin)
	b.Handle("\fadmin_reconcile_verify", HandleAdminReconcileVerify, auth, admin)
	b.Handle("\fadmin_reconcile_waive", HandleAdminReconcileWaivePrompt, auth, admin)
}

func HandleAdminReconcile(c telebot.Context) error {
	_ = c.Respond()
	stats, err := db.GetReconciliationStats(context.Background())
	if err != nil {
		log.Printf("[ERROR] Failed to fetch reconciliation stats: %v", err)
		return maybeEditOrSend(c, "خطا در دریافت اطلاعات وضعیت تطبیق سیستم.")
	}

	ageText := "هیچ مورد معلقی وجود ندارد"
	if stats.OldestPendingAgeSeconds > 0 {
		dur := time.Duration(stats.OldestPendingAgeSeconds) * time.Second
		if dur > 24*time.Hour {
			ageText = fmt.Sprintf("%d روز", int(dur.Hours()/24))
		} else if dur > time.Hour {
			ageText = fmt.Sprintf("%d ساعت", int(dur.Hours()))
		} else {
			ageText = fmt.Sprintf("%d دقیقه", int(dur.Minutes()))
		}
	}

	var sb strings.Builder
	sb.WriteString("🔄 **وضعیت تطبیق و عملیات سیستم**\n\n")
	sb.WriteString(fmt.Sprintf("▫️ کل موارد در انتظار تطبیق: **%d**\n", stats.PendingCount))
	sb.WriteString(fmt.Sprintf("▫️ استردادهای در انتظار کیف پول: **%d**\n", stats.PendingRefundCount))
	sb.WriteString(fmt.Sprintf("▫️ عدم قطعیت ایجاد سرویس در پنل: **%d**\n", stats.ProvisioningUnknownCount))
	sb.WriteString(fmt.Sprintf("▫️ موارد نیازمند اقدام دستی ادمین: **%d**\n", stats.ManualReviewCount))
	sb.WriteString(fmt.Sprintf("▫️ سن قدیمی‌ترین مورد معلق: **%s**\n\n", ageText))

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("⚡️ اجرای فوری فرآیند تطبیق", "admin_reconcile_run")),
		menu.Row(menu.Data("⚠️ موارد نیازمند بررسی دستی", "admin_reconcile_manual")),
		menu.Row(menu.Data("« بازگشت به پنل مدیریت", "admin_menu")),
	)
	return maybeEditOrSend(c, sb.String(), menu)
}

func HandleAdminReconcileRun(c telebot.Context) error {
	_ = c.Respond(&telebot.CallbackResponse{Text: "در حال اجرای فرآیند تطبیق..."})
	processor := reconcile.NewProcessor("admin_trigger", bot.XUIClient)
	count, err := processor.ProcessOnce(context.Background())
	if err != nil {
		log.Printf("[ERROR] Reconcile run failed: %v", err)
		return maybeEditOrSend(c, "اجرای فرآیند تطبیق با خطا مواجه شد.")
	}
	_ = c.Send(fmt.Sprintf("✅ فرآیند تطبیق اجرا شد. **%d** مورد بررسی و پردازش گردید.", count))
	return HandleAdminReconcile(c)
}

func HandleAdminReconcileManual(c telebot.Context) error {
	_ = c.Respond()
	records, err := db.GetManualReviewReconciliationRecords(context.Background(), 10)
	if err != nil || len(records) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "admin_reconcile")))
		return maybeEditOrSend(c, "✅ هیچ موردی نیازمند بررسی دستی وجود ندارد.", menu)
	}

	var sb strings.Builder
	sb.WriteString("⚠️ **موارد نیازمند بررسی دستی ادمین:**\n\n")
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	for i, r := range records {
		sb.WriteString(fmt.Sprintf("%d. شناسه رکورد: `%d`\n", i+1, r.ID))
		sb.WriteString(fmt.Sprintf("   کلید: `%s`\n", r.OperationKey))
		sb.WriteString(fmt.Sprintf("   نوع: %s (`%s`)\n", formatReconcileKind(r.Kind), r.Kind))
		if r.UserID != nil {
			sb.WriteString(fmt.Sprintf("   کاربر: %d\n", *r.UserID))
		}
		sb.WriteString(fmt.Sprintf("   دلیل بررسی: %s\n\n", r.ManualReviewReason))

		btnText := fmt.Sprintf("🔍 بررسی رکورد %d", r.ID)
		rows = append(rows, menu.Row(menu.Data(btnText, "admin_reconcile_detail", fmt.Sprintf("%d", r.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "admin_reconcile")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, sb.String(), menu)
}

func HandleAdminReconcileDetail(c telebot.Context) error {
	_ = c.Respond()
	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}

	r, err := db.GetReconciliationRecordByID(context.Background(), id)
	if err != nil || r == nil {
		log.Printf("[ERROR] Failed to load reconciliation record %d: %v", id, err)
		return maybeEditOrSend(c, "رکورد مورد نظر یافت نشد.")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 **جزئیات رکورد تطبیق #%d**\n\n", r.ID))
	sb.WriteString(fmt.Sprintf("▫️ **کلید عملیات**: `%s`\n", r.OperationKey))
	sb.WriteString(fmt.Sprintf("▫️ **نوع**: %s (`%s`)\n", formatReconcileKind(r.Kind), r.Kind))
	sb.WriteString(fmt.Sprintf("▫️ **وضعیت**: %s (`%s`)\n", formatReconcileStatus(r.Status), r.Status))
	if r.UserID != nil {
		sb.WriteString(fmt.Sprintf("▫️ **کاربر**: `%d`\n", *r.UserID))
	}
	if r.SubscriptionID != nil {
		sb.WriteString(fmt.Sprintf("▫️ **اشتراک**: `%d`\n", *r.SubscriptionID))
	}
	sb.WriteString(fmt.Sprintf("▫️ **تعداد تلاش**: %d\n", r.AttemptCount))
	if r.ErrorMessage != "" {
		sb.WriteString(fmt.Sprintf("▫️ **پیام خطا**: `%s`\n", r.ErrorMessage))
	}
	if r.ManualReviewReason != "" {
		sb.WriteString(fmt.Sprintf("▫️ **دلیل بازبینی دستی**: %s\n", r.ManualReviewReason))
	}

	if desiredSummary := formatStateSummary(r.DesiredState); desiredSummary != "" {
		sb.WriteString(fmt.Sprintf("\n📋 **خلاصه وضعیت مطلوب**:\n%s\n", desiredSummary))
	}
	if observedSummary := formatStateSummary(r.ObservedState); observedSummary != "" {
		sb.WriteString(fmt.Sprintf("\n👁 **خلاصه وضعیت مشاهده شده**:\n%s\n", observedSummary))
	}

	if r.ManualAdminID != nil {
		sb.WriteString(fmt.Sprintf("▫️ مدیر اقدام‌کننده: `%d`\n", *r.ManualAdminID))
	}
	if r.ManualActionAt != nil {
		sb.WriteString(fmt.Sprintf("▫️ زمان اقدام: `%s`\n", r.ManualActionAt.Format(time.RFC3339)))
	}
	if r.ManualActionReason != "" {
		sb.WriteString(fmt.Sprintf("▫️ توضیح اقدام: %s\n", r.ManualActionReason))
	}
	if r.ManualActionAmount != nil {
		sb.WriteString(fmt.Sprintf("▫️ مبلغ اقدام: %s تومان\n", persian.FormatMoney(*r.ManualActionAmount)))
	}
	if r.ManualActionOperationKey != "" {
		sb.WriteString(fmt.Sprintf("▫️ کلید مالی: `%s`\n", r.ManualActionOperationKey))
	}

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(menu.Data("⚡ تلاش مجدد اکنون", "admin_reconcile_retry", fmt.Sprintf("%d", r.ID))),
		menu.Row(menu.Data("⚠️ انتقال به بازبینی دستی", "admin_reconcile_mark_manual", fmt.Sprintf("%d", r.ID))),
	}
	if isFinancialReconcileRecord(r) {
		if r.Kind == reconcile.KindPendingRefund {
			rows = append(rows, menu.Row(menu.Data("✅ اثر مالی راستی‌آزمایی شد", "admin_reconcile_verify", fmt.Sprintf("%d", r.ID))))
		}
		rows = append(rows, menu.Row(menu.Data("🚫 صرف‌نظر دستی با ثبت دلیل", "admin_reconcile_waive", fmt.Sprintf("%d", r.ID))))
	} else {
		rows = append(rows, menu.Row(menu.Data("✅ بستن دستی با ذکر دلیل", "admin_reconcile_close", fmt.Sprintf("%d", r.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت به لیست", "admin_reconcile_manual")))
	menu.Inline(rows...)

	return maybeEditOrSend(c, sb.String(), menu)
}

func isFinancialReconcileKind(kind string) bool {
	switch kind {
	case reconcile.KindPendingRefund, reconcile.KindPurchaseProvisioningUnknown, reconcile.KindPurchaseRemoteCreatedDbFailed, reconcile.KindDirectPaymentProvisioningRetry:
		return true
	default:
		return false
	}
}

func isFinancialReconcileRecord(record *db.ReconciliationRecord) bool {
	if record == nil || isFinancialReconcileKind(record.Kind) {
		return record != nil
	}
	for _, key := range []string{"refund_amount", "amount", "price"} {
		if value, ok := coerceAnyInt64(record.DesiredState[key]); ok && value > 0 {
			return true
		}
	}
	return false
}

func HandleAdminReconcileVerify(c telebot.Context) error {
	id, err := strconv.ParseInt(callbackPayload(c), 10, 64)
	if err != nil || id <= 0 {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}
	if err := db.ResolvePendingRefundVerified(context.Background(), id, c.Sender().ID); err != nil {
		if errors.Is(err, db.ErrNoVerifiedFinancialEffect) {
			return maybeEditOrSend(c, "اثر مالی متناظر در دفتر کیف پول ثبت نشده است؛ رکورد همچنان باز می‌ماند.")
		}
		return maybeEditOrSend(c, "این رکورد از مسیر تطبیق مالی قابل تایید نیست.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "اثر مالی با دفتر کیف پول تطبیق داده شد."})
	return HandleAdminReconcileDetail(c)
}

func HandleAdminReconcileWaivePrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	id, err := strconv.ParseInt(callbackPayload(c), 10, 64)
	if err != nil || id <= 0 {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_reconcile_waiver_reason", map[string]interface{}{"record_id": id})
	_ = c.Respond()
	return maybeEditOrSend(c, fmt.Sprintf("🚫 دلیل صرف‌نظر دستی از تعهد مالی رکورد #%d را بنویسید. مدیر، زمان، مبلغ و کلید عملیات ثبت می‌شوند.\nبرای انصراف /cancel را بفرستید.", id))
}

func ProcessAdminReconcileWaiveReason(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_reconcile_waiver_reason" {
		return c.Send("هیچ فرآیند فعالی یافت نشد.")
	}
	id, ok := coerceAnyInt64(state.Data["record_id"])
	if !ok || id <= 0 {
		bot.FSM.ClearState(user.TelegramID)
		return c.Send("شناسه رکورد نامعتبر است.")
	}
	reason := strings.TrimSpace(text)
	if reason == "" {
		return c.Send("برای صرف‌نظر دستی، ثبت دلیل الزامی است.")
	}
	if err := db.ManuallyWaiveReconciliationRecord(context.Background(), id, user.TelegramID, reason); err != nil {
		return c.Send("ثبت صرف‌نظر دستی انجام نشد؛ رکورد در صف رسیدگی باقی ماند.")
	}
	bot.FSM.ClearState(user.TelegramID)
	_ = c.Send(fmt.Sprintf("🚫 رکورد #%d با صرف‌نظر دستی و ثبت اطلاعات حسابرسی نهایی شد.", id))
	return HandleAdminReconcileManual(c)
}

func HandleAdminReconcileRetry(c telebot.Context) error {
	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}

	if err := db.ResetReconciliationForRetry(context.Background(), id); err != nil {
		log.Printf("[ERROR] Failed to reset reconciliation record %d for retry: %v", id, err)
		if errors.Is(err, db.ErrReconciliationNotRetryable) {
			return maybeEditOrSend(c, "این رکورد در وضعیت قابل تلاش مجدد نیست یا قبلا نهایی شده است.")
		}
		return maybeEditOrSend(c, "خطا در تنظیم مجدد رکورد برای تلاش دوباره.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: "رکورد در صف تلاش مجدد قرار گرفت."})
	_ = c.Send("✅ رکورد با موفقیت در صف پردازش مجدد قرار گرفت.")
	return HandleAdminReconcileDetail(c)
}

func HandleAdminReconcileMarkManual(c telebot.Context) error {
	user := userFromContext(c)
	adminID := int64(0)
	if user != nil {
		adminID = user.TelegramID
	}

	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}

	reason := fmt.Sprintf("marked for manual review by admin %d", adminID)
	if err := db.MarkReconciliationManualReview(context.Background(), id, "", "", 0, reason); err != nil {
		log.Printf("[ERROR] Failed to mark reconciliation record %d as manual review: %v", id, err)
		return maybeEditOrSend(c, "خطا در انتقال رکورد به بازبینی دستی.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: "رکورد به بازبینی دستی منتقل شد."})
	_ = c.Send("⚠️ رکورد به بخش بازبینی دستی منتقل گردید.")
	return HandleAdminReconcileDetail(c)
}

func HandleAdminReconcileClosePrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_reconcile_close_reason", map[string]interface{}{
		"record_id": id,
	})

	_ = c.Respond()
	return maybeEditOrSend(c, fmt.Sprintf("📝 لطفاً **دلیل بستن دستی رکورد #%d** را ارسال کنید:\n(این دلیل در گزارش‌های سیستم ثبت و ذخیره خواهد شد)\n\nجهت انصراف عبارت /cancel را ارسال کنید.", id))
}

func ProcessAdminReconcileCloseReason(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_reconcile_close_reason" {
		return c.Send("هیچ فرآیند فعالی یافت نشد.")
	}

	recordID, ok := state.Data["record_id"].(int64)
	if !ok {
		if idFloat, ok := state.Data["record_id"].(float64); ok {
			recordID = int64(idFloat)
		} else {
			bot.FSM.ClearState(user.TelegramID)
			return c.Send("شناسه رکورد نامعتبر است.")
		}
	}

	reason := strings.TrimSpace(text)
	if reason == "" {
		return c.Send("دلیل بستن رکورد نمی‌تواند خالی باشد. لطفاً توضیح مناسبی ارسال فرمایید:")
	}

	bot.FSM.ClearState(user.TelegramID)

	reasonWithUser := fmt.Sprintf("(%s): %s", user.Username, reason)
	if err := db.ManuallyCloseReconciliationRecord(context.Background(), recordID, user.TelegramID, reasonWithUser); err != nil {
		log.Printf("[ERROR] Failed to manually close reconciliation record %d: %v", recordID, err)
		return c.Send("خطا در بستن دستی رکورد.")
	}

	_ = c.Send(fmt.Sprintf("✅ رکورد #%d با موفقیت با ثبت دلیل ادمین بسته شد.", recordID))
	return HandleAdminReconcileManual(c)
}

func formatReconcileKind(kind string) string {
	switch kind {
	case reconcile.KindPendingRefund, "wallet_refund":
		return "استرداد در انتظار کیف پول"
	case reconcile.KindPurchaseProvisioningUnknown, "purchase_provisioning":
		return "عدم قطعیت ایجاد سرویس در پنل"
	case reconcile.KindPurchaseRemoteCreatedDbFailed:
		return "ایجاد در پنل با خطای ثبت دیتابیس"
	case reconcile.KindSubscriptionUpdateDbFailed:
		return "بروزرسانی در پنل با خطای دیتابیس"
	case reconcile.KindSubscriptionDeleteUnknown, "subscription_delete":
		return "عدم قطعیت حذف سرویس در پنل"
	case reconcile.KindSubscriptionCancellationDbFailed:
		return "لغو سرویس با خطای ثبت دیتابیس"
	case reconcile.KindDirectPaymentProvisioningRetry, "direct_payment_provisioning":
		return "پرداخت مستقیم و ایجاد سرویس"
	case reconcile.KindSubscriptionRemoteMissing:
		return "ناپدید شدن سرویس در پنل (مفقود)"
	case "purchase_request_db_failed_compensated":
		return "ایجاد سرویس با خطای دیتابیس (جبران‌شده)"
	case "subscription_ip_change":
		return "تغییر آی‌پی اشتراک"
	case "subscription_traffic_reset":
		return "ریست ترافیک اشتراک"
	case "subscription_extend":
		return "تمدید اشتراک"
	case "bulk_credit":
		return "شارژ گروهی کیف پول"
	default:
		return kind
	}
}

func formatReconcileStatus(status string) string {
	switch status {
	case db.ReconciliationStatusPending:
		return "در انتظار بررسی"
	case "in_progress":
		return "در حال پردازش"
	case db.ReconciliationStatusResolved, "completed":
		return "حل‌شده"
	case db.ReconciliationStatusResolvedVerified:
		return "حل‌شده و راستی‌آزمایی‌شده"
	case "failed":
		return "ناموفق"
	case db.ReconciliationStatusFailedTerminal:
		return "ناموفق نهایی"
	case db.ReconciliationStatusManualReview:
		return "نیازمند بررسی دستی ادمین"
	case db.ReconciliationStatusManuallyClosed, "manually_resolved":
		return "حل‌شده به صورت دستی"
	case db.ReconciliationStatusManualWaiver:
		return "صرف‌نظر دستی"
	case db.ReconciliationStatusRequired:
		return "نیازمند تطبیق"
	case db.ReconciliationStatusPendingRefund:
		return "در انتظار استرداد وجه"
	case db.ReconciliationStatusSuperseded:
		return "منسوخ‌شده"
	case "compensated":
		return "جبران‌شده"
	default:
		return status
	}
}

func formatStateSummary(state map[string]any) string {
	if len(state) == 0 {
		return ""
	}
	var lines []string
	if email, ok := state["client_email"].(string); ok && email != "" {
		lines = append(lines, fmt.Sprintf("▫️ ایمیل: `%s`", email))
	} else if email, ok := state["email"].(string); ok && email != "" {
		lines = append(lines, fmt.Sprintf("▫️ ایمیل: `%s`", email))
	}
	if amt, ok := coerceAnyInt64(state["amount"]); ok && amt > 0 {
		lines = append(lines, fmt.Sprintf("▫️ مبلغ: %s تومان", persian.FormatMoney(amt)))
	} else if amt, ok := coerceAnyInt64(state["price"]); ok && amt > 0 {
		lines = append(lines, fmt.Sprintf("▫️ مبلغ: %s تومان", persian.FormatMoney(amt)))
	} else if amt, ok := coerceAnyInt64(state["refund_amount"]); ok && amt > 0 {
		lines = append(lines, fmt.Sprintf("▫️ مبلغ استرداد: %s تومان", persian.FormatMoney(amt)))
	}
	if months, ok := coerceAnyInt64(state["months"]); ok && months > 0 {
		lines = append(lines, fmt.Sprintf("▫️ مدت: %d ماه", months))
	}
	if ipLimit, ok := coerceAnyInt64(state["ip_limit"]); ok && ipLimit > 0 {
		lines = append(lines, fmt.Sprintf("▫️ سقف آی‌پی: %d", ipLimit))
	}
	if dataGB, ok := coerceAnyInt64(state["data_gb"]); ok && dataGB > 0 {
		lines = append(lines, fmt.Sprintf("▫️ سقف ترافیک: %d گیگابایت", dataGB))
	}
	if reason, ok := state["reason"].(string); ok && reason != "" {
		lines = append(lines, fmt.Sprintf("▫️ علت: %s", reason))
	}
	if outcome, ok := state["outcome"].(string); ok && outcome != "" {
		lines = append(lines, fmt.Sprintf("▫️ نتیجه: %s", outcome))
	}
	if del, ok := state["remote_deleted"].(bool); ok && del {
		lines = append(lines, "▫️ وضعیت پنل: از پنل حذف شده")
	}
	if cre, ok := state["remote_created"].(bool); ok && cre {
		lines = append(lines, "▫️ وضعیت پنل: در پنل ایجاد شده")
	}

	if len(lines) == 0 {
		for k, v := range state {
			if v != nil && fmt.Sprintf("%v", v) != "" {
				lines = append(lines, fmt.Sprintf("▫️ %s: `%v`", k, v))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func coerceAnyInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case int64:
		return val, true
	case int:
		return int64(val), true
	case float64:
		return int64(val), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		return n, err == nil
	}
	return 0, false
}
