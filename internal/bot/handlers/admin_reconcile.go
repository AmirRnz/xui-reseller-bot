package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
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
		sb.WriteString(fmt.Sprintf("   نوع: %s\n", r.Kind))
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
	sb.WriteString(fmt.Sprintf("▫️ **نوع**: `%s`\n", r.Kind))
	sb.WriteString(fmt.Sprintf("▫️ **وضعیت**: `%s`\n", r.Status))
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

	desiredJSON, _ := json.MarshalIndent(r.DesiredState, "", "  ")
	observedJSON, _ := json.MarshalIndent(r.ObservedState, "", "  ")

	if len(desiredJSON) > 0 && string(desiredJSON) != "{}" {
		sb.WriteString(fmt.Sprintf("\n📋 **وضعیت مطلوب**:\n```json\n%s\n```\n", string(desiredJSON)))
	}
	if len(observedJSON) > 0 && string(observedJSON) != "{}" {
		sb.WriteString(fmt.Sprintf("👁 **وضعیت مشاهده شده**:\n```json\n%s\n```\n", string(observedJSON)))
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("⚡ تلاش مجدد اکنون", "admin_reconcile_retry", fmt.Sprintf("%d", r.ID))),
		menu.Row(menu.Data("⚠️ انتقال به بازبینی دستی", "admin_reconcile_mark_manual", fmt.Sprintf("%d", r.ID))),
		menu.Row(menu.Data("✅ بستن دستی با ذکر دلیل", "admin_reconcile_close", fmt.Sprintf("%d", r.ID))),
		menu.Row(menu.Data("« بازگشت به لیست", "admin_reconcile_manual")),
	)

	return maybeEditOrSend(c, sb.String(), menu)
}

func HandleAdminReconcileRetry(c telebot.Context) error {
	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}

	if err := db.ResetReconciliationForRetry(context.Background(), id); err != nil {
		log.Printf("[ERROR] Failed to reset reconciliation record %d for retry: %v", id, err)
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
	if err := db.MarkReconciliationManualReview(context.Background(), id, reason); err != nil {
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
