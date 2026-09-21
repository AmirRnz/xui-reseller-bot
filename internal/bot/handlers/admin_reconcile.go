package handlers

import (
	"context"
	"fmt"
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
	b.Handle("\fadmin_reconcile_resolve", HandleAdminReconcileResolve, auth, admin)
}

func HandleAdminReconcile(c telebot.Context) error {
	_ = c.Respond()
	stats, err := db.GetReconciliationStats(context.Background())
	if err != nil {
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
		return maybeEditOrSend(c, fmt.Sprintf("اجرای فرآیند با خطا مواجه شد: %v", err))
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
		sb.WriteString(fmt.Sprintf("%d. شناسه: `%s`\n", i+1, r.OperationKey))
		sb.WriteString(fmt.Sprintf("   نوع: %s\n", r.Kind))
		if r.UserID != nil {
			sb.WriteString(fmt.Sprintf("   کاربر: %d\n", *r.UserID))
		}
		sb.WriteString(fmt.Sprintf("   دلیل بررسی: %s\n\n", r.ManualReviewReason))

		btnText := fmt.Sprintf("علامت‌گذاری %s به عنوان حل‌شده", r.OperationKey)
		if len(btnText) > 30 {
			btnText = fmt.Sprintf("حل مشکل رکورد %d", r.ID)
		}
		rows = append(rows, menu.Row(menu.Data(btnText, "admin_reconcile_resolve", fmt.Sprintf("%d", r.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "admin_reconcile")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, sb.String(), menu)
}

func HandleAdminReconcileResolve(c telebot.Context) error {
	idStr := callbackPayload(c)
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = c.Respond(&telebot.CallbackResponse{Text: "شناسه رکورد نامعتبر است."})
		return maybeEditOrSend(c, "شناسه رکورد نامعتبر است.")
	}
	if err := db.ResolveReconciliationRecord(context.Background(), id, "manually resolved by admin"); err != nil {
		_ = c.Respond(&telebot.CallbackResponse{Text: "خطا در ثبت وضعیت رکورد."})
		return maybeEditOrSend(c, "خطا در ثبت وضعیت رکورد: "+err.Error())
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "رکورد حل شد."})
	_ = c.Send("رکورد با موفقیت به عنوان بررسی و حل‌شده ثبت گردید.")
	return HandleAdminReconcileManual(c)
}
