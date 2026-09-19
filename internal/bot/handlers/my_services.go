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
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

const servicesPageSize = 6

type deleteResolution string

const (
	deleteConfirmed              deleteResolution = "confirmed"
	deleteStillPresent           deleteResolution = "still_present"
	deleteReconciliationRequired deleteResolution = "reconciliation_required"
	deleteDefinitiveFailure      deleteResolution = "definitive_failure"
)

var errDeleteClientStillPresent = errors.New("x-ui client is still present after delete")

// resolveDeleteOutcome centralizes the safety boundary around an ambiguous
// remote delete.  It is deliberately callback-based so the decision can be
// tested without a live panel: only a typed NotFound readback confirms that a
// timed-out delete committed; an unavailable readback remains reconcilable.
func resolveDeleteOutcome(deleteErr error, verify func() (*xui.XUIClientInfo, error)) (deleteResolution, error) {
	switch {
	case deleteErr == nil, xui.IsNotFound(deleteErr):
		return deleteConfirmed, nil
	case !xui.IsUnknownOutcome(deleteErr):
		return deleteDefinitiveFailure, deleteErr
	}

	if verify == nil {
		return deleteReconciliationRequired, errors.New("delete verification was not available")
	}
	remote, verifyErr := verify()
	switch {
	case xui.IsNotFound(verifyErr):
		return deleteConfirmed, nil
	case verifyErr != nil:
		return deleteReconciliationRequired, verifyErr
	case remote == nil:
		return deleteReconciliationRequired, errors.New("delete verification returned no client state")
	default:
		return deleteStillPresent, errDeleteClientStillPresent
	}
}

// orchestrateDeleteOutcome applies the side-effect boundary for a delete. A
// confirmed remote absence is the only outcome that may continue with local
// cancellation; an ambiguous result is durable-reconciliation work and a
// definitive failure leaves both callbacks untouched. Keeping the callbacks
// injectable makes these invariants testable without constructing a Telegram
// context or a live database.
func orchestrateDeleteOutcome(
	deleteErr error,
	verify func() (*xui.XUIClientInfo, error),
	onConfirmed func() error,
	onReconciliation func(deleteErr, verifyErr error) error,
) (deleteResolution, error) {
	resolution, resolutionErr := resolveDeleteOutcome(deleteErr, verify)
	switch resolution {
	case deleteConfirmed:
		if onConfirmed != nil {
			if err := onConfirmed(); err != nil {
				return resolution, err
			}
		}
	case deleteReconciliationRequired:
		if onReconciliation != nil {
			if err := onReconciliation(deleteErr, resolutionErr); err != nil {
				return resolution, err
			}
		}
	}
	return resolution, resolutionErr
}

func RegisterMyServices(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_my_services", HandleMyServicesFlow, auth)
	b.Handle("\fsvc_page", HandleMyServicesPage, auth)
	b.Handle("\fsub_request_assign", HandleRequestPlanAssignment, auth)
	b.Handle("\fview_sub", HandleViewSubscription, auth)
	b.Handle("\fsub_get_link", HandleGetLink, auth)
	b.Handle("\fsub_toggle", HandleToggleSubscription, auth)
	b.Handle("\fsub_rename", HandleSubscriptionRenamePrompt, auth)
	b.Handle("\fsub_delete_confirm", HandleDeleteSubscriptionConfirm, auth)
	b.Handle("\fsub_delete", HandleDeleteSubscription, auth)

	// IP limit upgrading handlers
	b.Handle("\fsub_limit", HandleSubscriptionLimitMenu, auth)
	b.Handle("\fsub_limit_set", HandleSubscriptionLimitConfirmPrompt, auth)
	b.Handle("\fsub_limit_confirm", HandleSubscriptionLimitSetWallet, auth)
	b.Handle("\fsub_limit_direct", HandleSubscriptionLimitSetDirect, auth)

	// Extension handlers
	b.Handle("\fsub_extend", HandleSubscriptionExtendMenu, auth)
	b.Handle("\fsub_extend_custom", HandleExtendCustomMonthsPrompt, auth)
	b.Handle("\fsub_extend_run", HandleExtendSubscriptionConfirmPrompt, auth)
	b.Handle("\fsub_extend_confirm", HandleExtendSubscriptionWallet, auth)
	b.Handle("\fsub_extend_direct", HandleExtendSubscriptionDirect, auth)
}

// ─── Subscription List (paginated) ───────────────────────────────────────────

func HandleMyServicesFlow(c telebot.Context) error {
	return showServicesPage(c, 0)
}

func HandleMyServicesPage(c telebot.Context) error {
	page, _ := strconv.Atoi(callbackPayload(c))
	return showServicesPage(c, page)
}

func showServicesPage(c telebot.Context, page int) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	subs, err := db.GetManageableSubscriptionsByUserID(context.Background(), user.ID)
	if err != nil {
		return c.Send("خطا در بارگذاری اشتراک‌ها.")
	}
	var paidSubs []*db.Subscription
	for _, sub := range subs {
		if sub.PlanType == db.PlanTypePaid {
			paidSubs = append(paidSubs, sub)
		}
	}
	subs = paidSubs

	if bot.XUIClient != nil {
		allClients, listErr := bot.XUIClient.ListClients()
		if listErr != nil {
			log.Printf("x-ui service reconciliation read failed: %v", listErr)
		} else {
			// subscriptions.user_id is the only ownership relation. The remote
			// group is metadata and must never import, filter, or transfer rows.
			existingClients := make(map[string]xui.XUIClientInfo, len(allClients))
			for _, client := range allClients {
				existingClients[client.Email] = client
			}
			for _, sub := range subs {
				if client, exists := existingClients[sub.ClientEmail]; exists {
					changed := false
					if devLimit, ok := parseDeviceLimitFromXUI(client); ok && devLimit != sub.IPLimit {
						log.Printf("Syncing device limit for %s during page load: DB had %d, XUI has %d", sub.ClientEmail, sub.IPLimit, devLimit)
						sub.IPLimit = devLimit
						changed = true
					}
					if syncActivationExpiry(sub, client) {
						changed = true
					}
					if changed {
						_ = db.UpdateSubscription(context.Background(), sub)
					}
				} else {
					// A missing infrastructure row is drift, never a reason to
					// delete the commercial subscription while displaying it.
					log.Printf("subscription drift: %s (subscription id %d) is absent from 3x-ui; preserving DB row", sub.ClientEmail, sub.ID)
				}
			}
		}
	}

	if len(subs) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("« بازگشت", "menu_main")),
		)
		return maybeEditOrSend(c, "📋 شما در حال حاضر هیچ اشتراکی ندارید.\nجهت شروع می‌توانید از گزینه‌های 🧪 تست رایگان یا 💼 خرید سرویس استفاده کنید.", menu)
	}

	totalPages := (len(subs) + servicesPageSize - 1) / servicesPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * servicesPageSize
	end := start + servicesPageSize
	if end > len(subs) {
		end = len(subs)
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("📋 **سرویس‌های من** (صفحه %d از %d)\n\n", page+1, totalPages))
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, sub := range subs[start:end] {
		icon := "🔴"
		if sub.IsActive {
			icon = "🟢"
		}
		var expires string
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			expires = "شروع پس از اولین اتصال"
		} else {
			expires = "تاریخ انقضا " + sub.EndDate.Format("2006-01-02")
		}
		text.WriteString(fmt.Sprintf("%s %s — %s\n", icon, sub.ClientEmail, expires))
		rows = append(rows, menu.Row(menu.Data(icon+" "+sub.DisplayName, "view_sub", fmt.Sprintf("%d", sub.ID))))
	}

	navRow := []telebot.Btn{}
	if page > 0 {
		navRow = append(navRow, menu.Data("◀️ قبلی", "svc_page", fmt.Sprintf("%d", page-1)))
	}
	if page < totalPages-1 {
		navRow = append(navRow, menu.Data("بعدی ▶️", "svc_page", fmt.Sprintf("%d", page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

// ─── Subscription Detail ──────────────────────────────────────────────────────

func HandleViewSubscription(c telebot.Context) error {
	sub, user, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	return showSubscriptionDetail(c, user, sub)
}

func showSubscriptionDetail(c telebot.Context, user *db.User, sub *db.Subscription) error {
	syncIPLimitFromXUI(sub)
	statusIcon := "🔴 غیرفعال"
	if sub.IsActive {
		statusIcon = "🟢 فعال"
	}

	var expiryStr = "انقضا: نامحدود"
	var trafficStr = ""

	if bot.XUIClient != nil {
		if traffic, err := bot.XUIClient.GetClientTraffic(sub.ClientEmail); err == nil && traffic != nil {
			downGB := float64(traffic.Down) / 1073741824
			upGB := float64(traffic.Up) / 1073741824
			usedGB := (float64(traffic.Down) + float64(traffic.Up)) / 1073741824

			if sub.TrafficLimitBytes > 0 {
				limitGB := float64(sub.TrafficLimitBytes) / 1073741824
				trafficStr = fmt.Sprintf("ترافیک: مصرف شده %.2f گیگابایت از %.2f گیگابایت (دانلود %.2f / آپلود %.2f)\n", usedGB, limitGB, downGB, upGB)
			} else {
				trafficStr = fmt.Sprintf("ترافیک: مصرف شده %.2f گیگابایت / نامحدود (دانلود %.2f / آپلود %.2f)\n", usedGB, downGB, upGB)
			}

			if traffic.ExpiryTime < 0 {
				durMs := -traffic.ExpiryTime
				days := durMs / (24 * 3600 * 1000)
				if days > 0 {
					expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d روز)", days)
				} else {
					hours := durMs / (3600 * 1000)
					expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
				}
			} else if traffic.ExpiryTime > 0 {
				expTime := time.UnixMilli(traffic.ExpiryTime)
				remaining := time.Until(expTime)
				days := int(remaining.Hours() / 24)
				if days >= 0 {
					expiryStr = fmt.Sprintf("انقضا: %s (%d روز باقی‌مانده)", expTime.Format("2006-01-02"), days)
				} else {
					expiryStr = fmt.Sprintf("انقضا: %s (منقضی شده)", expTime.Format("2006-01-02"))
				}
			}
		}
	}

	if expiryStr == "انقضا: نامحدود" && sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		durMs := -*sub.ExpireTime
		days := durMs / (24 * 3600 * 1000)
		if days > 0 {
			expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d روز)", days)
		} else {
			hours := durMs / (3600 * 1000)
			expiryStr = fmt.Sprintf("انقضا: شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
		}
	} else if expiryStr == "انقضا: نامحدود" && !sub.EndDate.IsZero() {
		remaining := time.Until(sub.EndDate)
		days := int(remaining.Hours() / 24)
		if days >= 0 {
			expiryStr = fmt.Sprintf("انقضا: %s (%d روز باقی‌مانده)", sub.EndDate.Format("2006-01-02"), days)
		} else {
			expiryStr = fmt.Sprintf("انقضا: %s (منقضی شده)", sub.EndDate.Format("2006-01-02"))
		}
	}

	displayIPLimit := sub.IPLimit

	var displayIPLimitStr string
	if displayIPLimit == 0 {
		displayIPLimitStr = "نامحدود"
	} else {
		displayIPLimitStr = fmt.Sprintf("%d", displayIPLimit)
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("📦 **%s**\n\n", sub.DisplayName))
	text.WriteString(fmt.Sprintf("📧 **ایمیل اشتراک:** `%s`\n", sub.ClientEmail))
	text.WriteString(fmt.Sprintf("⚡ **وضعیت سرویس:** %s\n", statusIcon))
	text.WriteString(fmt.Sprintf("👥 **کاربر همزمان:** %s\n", displayIPLimitStr))
	text.WriteString("⏳ " + expiryStr + "\n")
	if trafficStr != "" {
		text.WriteString("📊 " + trafficStr)
	}

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(
			menu.Data("🔗 دریافت لینک اتصال", "sub_get_link", fmt.Sprintf("%d", sub.ID)),
		),
	}
	if sub.PlanType == db.PlanTypePaid {
		if sub.PlanID != nil {
			plan, _ := paidPlanForSub(sub)
			var row telebot.Row
			if plan != nil && plan.MaxIPLimit > displayIPLimit && displayIPLimit > 0 {
				row = menu.Row(
					menu.Data("📶 افزایش کاربر همزمان", "sub_limit", fmt.Sprintf("%d", sub.ID)),
					menu.Data("⏳ تمدید سرویس", "sub_extend", fmt.Sprintf("%d", sub.ID)),
				)
			} else {
				row = menu.Row(
					menu.Data("⏳ تمدید سرویس", "sub_extend", fmt.Sprintf("%d", sub.ID)),
				)
			}
			rows = append(rows, row)
		} else {
			rows = append(rows, menu.Row(
				menu.Data("📥 درخواست تخصیص طرح", "sub_request_assign", fmt.Sprintf("%d", sub.ID)),
			))
		}
	}

	toggleText := "🔴 غیرفعال کردن"
	if !sub.IsActive {
		toggleText = "🟢 فعال کردن"
	}
	rows = append(rows, menu.Row(
		menu.Data(toggleText, "sub_toggle", fmt.Sprintf("%d", sub.ID)),
		menu.Data("🗑 حذف سرویس", "sub_delete_confirm", fmt.Sprintf("%d", sub.ID)),
	))
	rows = append(rows,
		menu.Row(menu.Data("« بازگشت", "menu_my_services")),
	)
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

// ─── Get Link ─────────────────────────────────────────────────────────────────

func HandleGetLink(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	if bot.XUIClient == nil {
		return c.Send("خطا: کلاینت x-ui متصل نیست.")
	}
	links, err := bot.XUIClient.GetSubscriptionLinks(sub.SubID)
	var subLink string
	if err == nil {
		for _, l := range links {
			if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
				subLink = l
				break
			}
		}
	}
	if subLink == "" {
		subLink = bot.XUIClient.SubscriptionURLFor(sub.SubID)
	}
	var expiryStr string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		expiryStr = "شروع پس از اولین اتصال"
	} else {
		expiryStr = sub.EndDate.Format("2006-01-02 15:04 UTC")
	}
	detailsMsg := fmt.Sprintf("🔗 اشتراک: **%s**\n📅 تاریخ انقضا: %s", sub.DisplayName, expiryStr)
	return sendSubscriptionResult(c, subLink, detailsMsg)
}

// ─── Toggle ───────────────────────────────────────────────────────────────────

func HandleToggleSubscription(c telebot.Context) error {
	sub, user, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	sub.IsActive = !sub.IsActive
	if err := updateXUIFromSubscription(sub); err != nil {
		if xui.IsUnknownOutcome(err) {
			desiredActive := sub.IsActive
			if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, &sub.IPLimit, sub.ExpireTime, &desiredActive, "toggle has unknown 3x-ui outcome"); recErr != nil {
				log.Printf("[CRITICAL] failed to mark toggle reconciliation for subscription %d: %v", sub.ID, recErr)
			}
		}
		return c.Send("خطا در اعمال تغییرات در پنل. لطفا مجددا تلاش کنید.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		desiredActive := sub.IsActive
		if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, &sub.IPLimit, sub.ExpireTime, &desiredActive, "3x-ui toggle succeeded but database update failed"); recErr != nil {
			log.Printf("[CRITICAL] failed to mark DB-after-remote toggle reconciliation for subscription %d: %v", sub.ID, recErr)
		}
		return c.Send("خطا در ذخیره‌سازی وضعیت.")
	}

	state := "غیرفعال"
	if sub.IsActive {
		state = "فعال"
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("سرویس شما %s شد.", state)})
	return showSubscriptionDetail(c, user, sub)
}

// ─── Rename ───────────────────────────────────────────────────────────────────

func HandleSubscriptionRenamePrompt(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessSubscriptionRename(c telebot.Context, newName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

// ─── Delete (with confirmation) ───────────────────────────────────────────────

func HandleDeleteSubscriptionConfirm(c telebot.Context) error {
	sub, user, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("⚠️ **تایید حذف سرویس %s**\n\nآیا از حذف این سرویس اطمینان دارید؟", sub.DisplayName))

	var refundAmount int64 = 0
	if sub.PlanType == db.PlanTypePaid {
		plan, _ := paidPlanForSub(sub)
		factorStr, _ := db.GetSetting(context.Background(), "ip_limit_factor")
		var factor float64
		if factorStr != "" {
			factor, _ = strconv.ParseFloat(factorStr, 64)
		}
		refundAmount = CalculateRefund(plan, sub, factor, nowUTC())
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	if refundAmount > 0 {
		text.WriteString(fmt.Sprintf("\n\nمبلغ عودتی تقریبی شما بابت %d ماه کامل باقیمانده: **%d %s**\nمبلغ پس از تایید مدیریت به کیف پول شما اضافه خواهد شد.",
			int(sub.EndDate.Sub(nowUTC()).Hours()/24/30), refundAmount, currency))
	} else {
		text.WriteString("\n\nبا حذف این سرویس هیچ مبلغی به کیف پول شما عودت داده نخواهد شد (زمان باقیمانده کافی نیست یا سرویس تست است).")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_delete_sub_confirm", map[string]interface{}{
		"sub_id":        fmt.Sprintf("%d", sub.ID),
		"refund_amount": fmt.Sprintf("%d", refundAmount),
	})

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("بله، حذف کن", "sub_delete")),
		menu.Row(menu.Data("❌ انصراف", "view_sub", fmt.Sprintf("%d", sub.ID))),
	)

	return maybeEditOrSend(c, text.String(), menu)
}

func HandleDeleteSubscription(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_delete_sub_confirm" {
		return c.Send("درخواست حذف معتبر نیست.")
	}
	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["sub_id"]))
	refundAmountStr := fmt.Sprintf("%v", state.Data["refund_amount"])
	refundAmount, _ := strconv.ParseInt(refundAmountStr, 10, 64)

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}

	// Delete from panel
	if bot.XUIClient != nil {
		deleteErr := bot.XUIClient.DeleteClient(sub.ClientEmail)
		resolution, _ := orchestrateDeleteOutcome(
			deleteErr,
			func() (*xui.XUIClientInfo, error) {
				return bot.XUIClient.GetClientByEmail(sub.ClientEmail)
			},
			nil,
			func(deleteErr, verifyErr error) error {
				persistDeleteReconciliation(sub, user, refundAmount, deleteErr, verifyErr)
				return nil
			},
		)
		switch resolution {
		case deleteConfirmed:
			// A confirmed missing client is already in the desired remote state.
		case deleteStillPresent:
			// The client is still present, so this attempt definitely did not
			// apply. Keep all financial/commercial state unchanged.
			return c.Send("حذف اشتراک از پنل تایید نشد؛ اشتراک در دیتابیس و وضعیت مالی شما بدون تغییر باقی ماند.")
		case deleteReconciliationRequired:
			return c.Send("نتیجه حذف اشتراک از پنل نامشخص است؛ هیچ تغییر مالی انجام نشد و عملیات برای تطبیق ثبت شد.")
		default:
			return c.Send("خطا در حذف اشتراک از پنل 3x-ui. لطفا با پشتیبانی تماس بگیرید.")
		}
	}

	var req *db.RefundRequest
	if refundAmount > 0 {
		req, err = db.CancelSubscriptionWithRefund(context.Background(), sub.ID, user.ID, refundAmount, fmt.Sprintf("subscription_cancel_refund:%d", sub.ID))
		if err != nil {
			log.Printf("[CRITICAL] subscription %d was removed remotely but cancellation/refund DB transaction failed: %v", sub.ID, err)
			subID64 := int64(sub.ID)
			if recErr := db.CreateReconciliationRecord(context.Background(), &db.ReconciliationRecord{
				OperationKey:   fmt.Sprintf("subscription_cancel_reconciliation:%d", sub.ID),
				Kind:           "subscription_cancellation_db_failure",
				UserID:         &user.ID,
				SubscriptionID: &subID64,
				DesiredState:   map[string]any{"status": db.SubscriptionStatusCancelled, "refund_amount": refundAmount},
				ObservedState:  map[string]any{"remote_deleted": true},
				ErrorMessage:   err.Error(),
			}); recErr != nil {
				log.Printf("[CRITICAL] failed to persist cancellation reconciliation for subscription %d: %v", sub.ID, recErr)
			}
			return c.Send("حذف در پنل انجام شد اما ثبت لغو و استرداد در دیتابیس ناموفق بود؛ لطفا با پشتیبانی تماس بگیرید.")
		}
		if req == nil || req.ID == 0 {
			log.Printf("[CRITICAL] cancellation refund request for subscription %d has no valid ID", sub.ID)
			return c.Send("لغو سرویس ثبت شد اما درخواست استرداد شناسه معتبر ندارد؛ لطفا با پشتیبانی تماس بگیرید.")
		}
	} else if err := db.UpdateSubscriptionStatus(context.Background(), sub.ID, db.SubscriptionStatusCancelled); err != nil {
		log.Printf("[CRITICAL] subscription %d was removed remotely but cancellation DB update failed: %v", sub.ID, err)
		subID64 := int64(sub.ID)
		if recErr := db.CreateReconciliationRecord(context.Background(), &db.ReconciliationRecord{
			OperationKey:   fmt.Sprintf("subscription_cancel_reconciliation:%d", sub.ID),
			Kind:           "subscription_cancellation_db_failure",
			UserID:         &user.ID,
			SubscriptionID: &subID64,
			DesiredState:   map[string]any{"status": db.SubscriptionStatusCancelled},
			ObservedState:  map[string]any{"remote_deleted": true},
			ErrorMessage:   err.Error(),
		}); recErr != nil {
			log.Printf("[CRITICAL] failed to persist cancellation reconciliation for subscription %d: %v", sub.ID, recErr)
		}
		return c.Send("حذف در پنل انجام شد اما ثبت لغو در دیتابیس ناموفق بود؛ لطفا با پشتیبانی تماس بگیرید.")
	}

	bot.FSM.ClearState(user.TelegramID)

	if req != nil && req.ID != 0 && req.Status == "pending" {

		// Notify Admins
		if config.Global != nil {
			for _, adminID := range config.Global.Admin.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(
					menu.Row(
						menu.Data("تایید استرداد", "admin_approve_refund", fmt.Sprintf("%d", req.ID)),
						menu.Data("رد استرداد", "admin_reject_refund", fmt.Sprintf("%d", req.ID)),
					),
				)
				currency, _ := db.GetSetting(context.Background(), "currency_name")
				if currency == "" {
					currency = "IRR"
				}
				caption := fmt.Sprintf("📥 **درخواست استرداد وجه حذف سرویس #%d**\n\nکاربر: @%s (%d)\nایمیل اشتراک حذف شده: `%s`\nمبلغ درخواستی: %d %s",
					req.ID, user.Username, user.TelegramID, sub.ClientEmail, refundAmount, currency)
				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, caption, menu)
			}
		}

		_ = c.Send("سرویس با موفقیت حذف شد. درخواست استرداد وجه برای تایید به مدیریت ارسال گردید.")
	} else {
		_ = c.Send("سرویس با موفقیت حذف شد.")
	}

	return HandleMyServicesFlow(c)
}

func persistDeleteReconciliation(sub *db.Subscription, user *db.User, refundAmount int64, deleteErr, verifyErr error) {
	if sub == nil || user == nil {
		return
	}
	subID := int64(sub.ID)
	observed := map[string]any{"delete_outcome": "unknown"}
	if deleteErr != nil {
		observed["delete_error"] = deleteErr.Error()
	}
	if verifyErr != nil {
		observed["verification_error"] = verifyErr.Error()
	}
	if err := db.CreateReconciliationRecord(context.Background(), &db.ReconciliationRecord{
		OperationKey:   fmt.Sprintf("subscription_delete_reconciliation:%d", sub.ID),
		Kind:           "subscription_delete_unknown",
		UserID:         &user.ID,
		SubscriptionID: &subID,
		DesiredState: map[string]any{
			"remote":        "absent",
			"status":        db.SubscriptionStatusCancelled,
			"refund_amount": refundAmount,
		},
		ObservedState: observed,
		ErrorMessage:  deleteErr.Error(),
	}); err != nil {
		log.Printf("[CRITICAL] failed to persist delete reconciliation for subscription %d: %v", sub.ID, err)
	}
}

// ─── Increase IP limit ────────────────────────────────────────────────────────

func HandleSubscriptionLimitMenu(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	syncIPLimitFromXUI(sub)
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تغییر سقف کاربر همزمان فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مرتبط یافت نشد.")
	}
	displayIPLimit := sub.IPLimit

	if displayIPLimit >= plan.MaxIPLimit {
		return c.Send(fmt.Sprintf("اشتراک شما در حال حاضر در حداکثر سقف کاربر همزمان مجاز طرح خود (%d کاربر) قرار دارد.", plan.MaxIPLimit))
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := displayIPLimit + 1; ip <= plan.MaxIPLimit; ip++ {
		months := monthsRemainingFrom(sub.EndDate)
		if months < 1 {
			months = 1
		}
		cost := float64(ip-displayIPLimit) * plan.PricePerExtraIP * float64(months)
		rows = append(rows, menu.Row(menu.Data(
			fmt.Sprintf("%d کاربر همزمان — هزینه: %.0f %s", ip, cost, currency),
			"sub_limit_set", fmt.Sprintf("%d:%d", ip, sub.ID),
		)))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "view_sub", fmt.Sprintf("%d", sub.ID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("📶 ارتقای تعداد کاربران همزمان برای **%s**\nتعداد فعلی: %d کاربر", sub.DisplayName, displayIPLimit), menu)
}

func HandleSubscriptionLimitConfirmPrompt(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست ارتقای کاربر نامعتبر است.")
	}
	newLimit, _ := strconv.Atoi(parts[0])
	_, _ = parseInt64(parts[1])

	sub, user, ok := loadOwnedSubscriptionFromPair(c)
	if !ok {
		return nil
	}

	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	displayIPLimit := sub.IPLimit

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := float64(newLimit-displayIPLimit) * plan.PricePerExtraIP * float64(months)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	operationID := makeSubID()
	confirmPayload := fmt.Sprintf("%d:%d:%s", newLimit, sub.ID, operationID)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "sub_limit_confirm", confirmPayload),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "sub_limit_direct", confirmPayload),
		),
		menu.Row(
			menu.Data("❌ انصراف", "view_sub", fmt.Sprintf("%d", sub.ID)),
		),
	)

	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **ارتقای کاربر همزمان سرویس %s**\n\nتعداد کاربر جدید: %d دستگاه همزمان\nتعداد کاربر فعلی: %d دستگاه همزمان\nهزینه ارتقا (تا پایان دوره): **%.0f %s**\n\nموجودی کیف پول شما: %d %s\n\nنحوه پرداخت ارتقا را انتخاب کنید:",
		sub.DisplayName, newLimit, displayIPLimit, cost, currency, user.WalletBalance, currency,
	), menu)
}

func HandleSubscriptionLimitSetWallet(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه ارتقا منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	newLimit, _ := strconv.Atoi(parts[0])
	subID, _ := parseInt64(parts[1])

	unlock := bot.Locker.Lock(fmt.Sprintf("sub:%d", subID))
	defer unlock()

	sub, user, ok := loadOwnedSubscriptionFromPair(c)
	if !ok {
		return nil
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	displayIPLimit := sub.IPLimit

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := float64(newLimit-displayIPLimit) * plan.PricePerExtraIP * float64(months)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	operationKey := fmt.Sprintf("wallet_upgrade_ip:%s", parts[2])
	if already, checkErr := db.HasWalletOperation(context.Background(), operationKey); checkErr != nil {
		return c.Send("خطا در بررسی وضعیت عملیات مالی.")
	} else if already {
		return c.Send("این ارتقا قبلا پردازش شده یا در وضعیت تطبیق قرار دارد.")
	}
	if err := db.DebitWalletBalanceWithKey(context.Background(), user.ID, cost, "IP limit increase for sub ID: "+strconv.Itoa(int(sub.ID)), operationKey); err != nil {
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این ارتقا قبلا پردازش شده یا در وضعیت تطبیق قرار دارد.")
		}
		return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه این ارتقا %.0f %s می‌باشد.", cost, currency))
	}

	oldLimit := sub.IPLimit
	sub.IPLimit = newLimit
	if err := updateXUIFromSubscription(sub); err != nil {
		sub.IPLimit = oldLimit
		if xui.IsUnknownOutcome(err) {
			desiredActive := sub.IsActive
			if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, &newLimit, sub.ExpireTime, &desiredActive, "wallet IP upgrade has unknown 3x-ui outcome"); recErr != nil {
				log.Printf("[CRITICAL] failed to mark IP upgrade reconciliation for subscription %d: %v", sub.ID, recErr)
			}
			return c.Send("نتیجه ارتقای پنل نامشخص است؛ مبلغ بازگردانده نشد و سرویس برای تطبیق ثبت شد.")
		}
		_ = db.CreditWalletBalanceWithKey(context.Background(), user.ID, cost, "refund failed IP upgrade", operationKey+":refund")
		return c.Send("خطا در بروزرسانی پنل. مبلغ ارتقا به کیف پول شما برگشت داده شد.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		desiredActive := sub.IsActive
		if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, &newLimit, sub.ExpireTime, &desiredActive, "3x-ui IP upgrade succeeded but database update failed"); recErr != nil {
			log.Printf("[CRITICAL] failed to mark DB-after-remote IP upgrade reconciliation for subscription %d: %v", sub.ID, recErr)
		}
		return c.Send("ارتقا در پنل انجام شد اما ثبت آن در دیتابیس ناموفق بود؛ مبلغ بازگردانده نشد و وضعیت برای تطبیق ثبت شد.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ تعداد کاربر همزمان به %d افزایش یافت.", newLimit)})
	_ = c.Send(fmt.Sprintf("✅ ارتقا با موفقیت انجام شد. سقف کاربر همزمان به %d کاربر افزایش یافت. هزینه کسر شده: %.0f %s.", newLimit, cost, currency))
	return showSubscriptionDetail(c, user, sub)
}

func HandleSubscriptionLimitSetDirect(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه ارتقا منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	newLimit, _ := strconv.Atoi(parts[0])
	_, _ = parseInt64(parts[1])

	sub, user, ok := loadOwnedSubscriptionFromPair(c)
	if !ok {
		return nil
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	displayIPLimit := sub.IPLimit

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := float64(newLimit-displayIPLimit) * plan.PricePerExtraIP * float64(months)

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	// Change state to awaiting_purchase_receipt with IP upgrade metadata
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", map[string]interface{}{
		"type":            "upgrade_ip",
		"subscription_id": fmt.Sprintf("%d", sub.ID),
		"ip_limit":        fmt.Sprintf("%d", newLimit),
		"price":           fmt.Sprintf("%.2f", cost),
	})

	var text strings.Builder
	text.WriteString("💳 **پرداخت مستقیم برای ارتقای تعداد کاربران همزمان**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%.0f %s**\n\n", cost, currency))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز، **رسید پرداخت (تصویر فیش)** را در همینجا ارسال کنید تا ارتقا پس از تایید ادمین اعمال شود.")

	return maybeEditOrSend(c, text.String())
}

// ─── Extend Subscription ─────────────────────────────────────────────────────

func HandleSubscriptionExtendMenu(c telebot.Context) error {
	sub, _, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}
	syncIPLimitFromXUI(sub)
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تمدید فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	displayIPLimit := sub.IPLimit

	priceFor := func(months int) float64 {
		dataGB := int(sub.TrafficLimitBytes / 1073741824)
		return calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data(fmt.Sprintf("۱ ماهه — %.0f %s", priceFor(1), currency), "sub_extend_run", fmt.Sprintf("1:%d", sub.ID)),
			menu.Data(fmt.Sprintf("۳ ماهه — %.0f %s", priceFor(3), currency), "sub_extend_run", fmt.Sprintf("3:%d", sub.ID)),
		),
		menu.Row(
			menu.Data(fmt.Sprintf("۶ ماهه — %.0f %s", priceFor(6), currency), "sub_extend_run", fmt.Sprintf("6:%d", sub.ID)),
			menu.Data("✏️ مدت دلخواه", "sub_extend_custom", fmt.Sprintf("%d", sub.ID)),
		),
		menu.Row(menu.Data("« بازگشت", "view_sub", fmt.Sprintf("%d", sub.ID))),
	)

	var expiryLabel string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			durMs := -*sub.ExpireTime
			days := durMs / (24 * 3600 * 1000)
			if days > 0 {
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", days)
			} else {
				hours := durMs / (3600 * 1000)
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
			}
		} else {
			expiryLabel = "شروع پس از اولین اتصال"
		}
	} else {
		expiryLabel = sub.EndDate.Format("2006-01-02")
	}
	return maybeEditOrSend(c, fmt.Sprintf("⏳ تمدید سرویس **%s**\nتاریخ انقضای فعلی: %s\n\nمدت زمان تمدید را انتخاب کنید:", sub.DisplayName, expiryLabel), menu)
}

func HandleExtendCustomMonthsPrompt(c telebot.Context) error {
	user := userFromContext(c)
	subID, err := parseInt64(callbackPayload(c))
	if user == nil || err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_extend_months_text", map[string]interface{}{"sub_id": fmt.Sprintf("%d", subID)})
	return maybeEditOrSend(c, "لطفا تعداد ماه‌های مورد نظر برای تمدید را ارسال کنید (به عنوان مثال: 2):")
}

func ProcessExtendMonthsText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند تمدید فعالی یافت نشد.")
	}
	months, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || months <= 0 {
		return c.Send("تعداد ماه‌ها باید یک عدد مثبت باشد.")
	}
	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["sub_id"]))
	bot.FSM.ClearState(user.TelegramID)
	return showExtendConfirmation(c, user, int(subID), months)
}

func HandleExtendSubscriptionConfirmPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست تمدید نامعتبر است.")
	}
	months, err := strconv.Atoi(parts[0])
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت باشد.")
	}
	subID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}
	return showExtendConfirmation(c, user, int(subID), months)
}

func showExtendConfirmation(c telebot.Context, user *db.User, subID int, months int) error {
	sub, err := db.GetSubscriptionByID(context.Background(), subID)
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	syncIPLimitFromXUI(sub)
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح مرتبط یافت نشد.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := sub.IPLimit
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	payload := fmt.Sprintf("%d:%d:%s", months, sub.ID, makeSubID())
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "sub_extend_confirm", payload),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "sub_extend_direct", payload),
		),
		menu.Row(
			menu.Data("❌ انصراف", "view_sub", fmt.Sprintf("%d", sub.ID)),
		),
	)

	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **تمدید سرویس %s**\n\nمدت تمدید: %d ماه\nهزینه تمدید: **%.0f %s**\n\nموجودی کیف پول شما: %d %s\n\nنحوه پرداخت هزینه تمدید را انتخاب کنید:",
		sub.DisplayName, months, cost, currency, user.WalletBalance, currency,
	), menu)
}

func HandleExtendSubscriptionWallet(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه تمدید منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	months, _ := strconv.Atoi(parts[0])
	subID, _ := parseInt64(parts[1])

	unlock := bot.Locker.Lock(fmt.Sprintf("sub:%d", subID))
	defer unlock()

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	syncIPLimitFromXUI(sub)
	if sub.PlanType != db.PlanTypePaid {
		return c.Send("تمدید فقط برای سرویس‌های خریداری شده امکان‌پذیر است.")
	}
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := sub.IPLimit
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	operationKey := fmt.Sprintf("wallet_extend:%s", parts[2])
	if already, checkErr := db.HasWalletOperation(context.Background(), operationKey); checkErr != nil {
		return c.Send("خطا در بررسی وضعیت عملیات مالی.")
	} else if already {
		return c.Send("این تمدید قبلا پردازش شده یا در وضعیت تطبیق قرار دارد.")
	}
	if err := db.DebitWalletBalanceWithKey(context.Background(), user.ID, cost, "subscription extension: "+sub.ClientEmail, operationKey); err != nil {
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این تمدید قبلا پردازش شده یا در وضعیت تطبیق قرار دارد.")
		}
		return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه تمدید %.0f %s می‌باشد.", cost, currency))
	}

	oldEnd := sub.EndDate
	var oldExpireTime *int64
	if sub.ExpireTime != nil {
		val := *sub.ExpireTime
		oldExpireTime = &val
	}
	oldIsActive := sub.IsActive

	var newExpiryMilli int64
	var newExpiryLabel string

	if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		newDuration := -(*sub.ExpireTime) + int64(months)*30*24*3600*1000
		newExpiryMilli = -newDuration
		sub.ExpireTime = &newExpiryMilli
		sub.EndDate = time.Time{}
		newExpiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", newDuration/(24*3600*1000))
	} else {
		if sub.EndDate.Before(nowUTC()) {
			sub.EndDate = nowUTC()
		}
		sub.EndDate = sub.EndDate.AddDate(0, months, 0)
		newExpiryMilli = sub.EndDate.UnixMilli()
		sub.ExpireTime = &newExpiryMilli
		newExpiryLabel = sub.EndDate.Format("2006-01-02")
	}

	sub.IsActive = true
	desiredExpireTime := sub.ExpireTime
	desiredActive := sub.IsActive

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		if xui.IsUnknownOutcome(err) {
			if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, nil, desiredExpireTime, &desiredActive, "wallet extension has unknown 3x-ui outcome"); recErr != nil {
				log.Printf("[CRITICAL] failed to mark extension reconciliation for subscription %d: %v", sub.ID, recErr)
			}
			return c.Send("نتیجه تمدید در پنل نامشخص است؛ مبلغ بازگردانده نشد و وضعیت برای تطبیق ثبت شد.")
		}
		_ = db.CreditWalletBalanceWithKey(context.Background(), user.ID, cost, "refund failed extension", operationKey+":refund")
		return c.Send("خطا در بروزرسانی پنل. مبلغ تمدید به کیف پول شما بازگردانده شد.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		if recErr := db.MarkSubscriptionReconciliationRequired(context.Background(), sub.ID, nil, desiredExpireTime, &desiredActive, "3x-ui extension succeeded but database update failed"); recErr != nil {
			log.Printf("[CRITICAL] failed to mark DB-after-remote extension reconciliation for subscription %d: %v", sub.ID, recErr)
		}
		return c.Send("تمدید در پنل انجام شد اما ثبت آن در دیتابیس ناموفق بود؛ مبلغ بازگردانده نشد و وضعیت برای تطبیق ثبت شد.")
	}

	_ = c.Send(fmt.Sprintf("✅ سرویس با موفقیت تمدید شد. انقضای جدید: %s\nمبلغ پرداخت شده: %.0f %s.",
		newExpiryLabel, cost, currency))
	return showSubscriptionDetail(c, user, sub)
}

func HandleExtendSubscriptionDirect(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return c.Send("این تاییدیه تمدید منقضی شده است؛ لطفا فرآیند را دوباره شروع کنید.")
	}
	months, _ := strconv.Atoi(parts[0])
	subID, _ := parseInt64(parts[1])

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		return c.Send("اشتراک یافت نشد.")
	}
	syncIPLimitFromXUI(sub)
	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}
	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := sub.IPLimit
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	// Change state to awaiting_purchase_receipt with Extend metadata
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", map[string]interface{}{
		"type":            "extend",
		"subscription_id": fmt.Sprintf("%d", sub.ID),
		"months":          fmt.Sprintf("%d", months),
		"price":           fmt.Sprintf("%.2f", cost),
	})

	var text strings.Builder
	text.WriteString("💳 **پرداخت مستقیم برای تمدید سرویس**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%.0f %s**\n\n", cost, currency))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز، **رسید پرداخت (تصویر فیش)** را در همینجا ارسال کنید تا سرویس پس از تایید ادمین تمدید شود.")

	return maybeEditOrSend(c, text.String())
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func loadOwnedSubscription(c telebot.Context) (*db.Subscription, *db.User, bool) {
	subID, err := parseInt64(callbackPayload(c))
	if err != nil || subID == 0 {
		_ = c.Send("اشتراک نامعتبر است.")
		return nil, nil, false
	}
	user := userFromContext(c)
	if user == nil {
		_ = c.Send("خطا در بارگذاری حساب کاربری.")
		return nil, nil, false
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		_ = c.Send("اشتراک یافت نشد.")
		return nil, nil, false
	}
	return sub, user, true
}

func loadOwnedSubscriptionFromPair(c telebot.Context) (*db.Subscription, *db.User, bool) {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) < 2 || len(parts) > 3 {
		_ = c.Send("درخواست اشتراک نامعتبر است.")
		return nil, nil, false
	}
	subID, err := parseInt64(parts[1])
	if err != nil || subID == 0 {
		_ = c.Send("اشتراک نامعتبر است.")
		return nil, nil, false
	}
	user := userFromContext(c)
	if user == nil {
		_ = c.Send("خطا در بارگذاری حساب کاربری.")
		return nil, nil, false
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || sub.UserID != user.ID {
		_ = c.Send("اشتراک یافت نشد.")
		return nil, nil, false
	}
	return sub, user, true
}

func paidPlanForSub(sub *db.Subscription) (*db.PaidPlan, error) {
	if sub == nil || sub.PlanID == nil {
		return nil, nil
	}
	return db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID))
}

func updateXUIFromSubscription(sub *db.Subscription) error {
	if bot.XUIClient == nil {
		return nil
	}
	client := clientConfigFromSubscription(sub, sub.ClientEmail)
	client.Enable = sub.IsActive
	return bot.XUIClient.UpdateClient(sub.ClientEmail, client)
}

func updateXUIRename(oldEmail string, sub *db.Subscription) error {
	if bot.XUIClient == nil {
		return nil
	}
	client := clientConfigFromSubscription(sub, sub.ClientEmail)
	client.Enable = sub.IsActive
	return bot.XUIClient.UpdateClient(oldEmail, client)
}

func clientConfigFromSubscription(sub *db.Subscription, email string) xui.ClientConfig {
	expireMilli := int64(0)
	if sub.ExpireTime != nil {
		expireMilli = *sub.ExpireTime
	} else if !sub.EndDate.IsZero() {
		expireMilli = sub.EndDate.UnixMilli()
	}
	user, _ := db.GetUserByID(context.Background(), sub.UserID)
	group := ""
	tgID := int64(0)
	if user != nil {
		group = serviceGroup(user)
		tgID = user.TelegramID
	}
	flow := ""
	total := sub.TrafficLimitBytes
	if sub.PlanID != nil {
		if sub.PlanType == db.PlanTypeTest {
			if plan, _ := db.GetTestPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
				flow = plan.Flow
				if total == 0 {
					total = plan.MaxDataBytes
				}
			}
		} else if plan, _ := db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
			flow = plan.Flow
		}
	}
	if total == 0 && bot.XUIClient != nil {
		if traffic, err := bot.XUIClient.GetClientTraffic(sub.ClientEmail); err == nil && traffic != nil && traffic.Total > 0 {
			total = traffic.Total
		}
	}
	planName := "unlimited"
	if sub.PlanType == db.PlanTypeTest {
		planName = "test"
		if sub.PlanID != nil {
			if plan, _ := db.GetTestPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
				planName = plan.Name
			}
		}
	} else if sub.PlanID != nil {
		if plan, _ := db.GetPaidPlanByID(context.Background(), int64(*sub.PlanID)); plan != nil {
			planName = plan.Name
		}
	}
	client := prepareClientConfig(email, group, tgID, total, expireMilli, sub.IPLimit, flow, sub.SubID, sub.ClientUUID, planName, user)
	return client
}

func monthsRemainingFrom(expire time.Time) int {
	now := nowUTC()
	if expire.Before(now) {
		return 0
	}
	days := int(expire.Sub(now).Hours() / 24)
	months := days / 30
	if months < 1 {
		months = 1
	}
	return months
}

func HandleRequestPlanAssignment(c telebot.Context) error {
	sub, user, ok := loadOwnedSubscription(c)
	if !ok {
		return nil
	}

	if sub.PlanID != nil {
		return c.Send("این اشتراک قبلاً به یک طرح متصل شده است.")
	}

	// Notify Admins
	if config.Global != nil {
		for _, adminID := range config.Global.Admin.AdminIDs {
			menu := &telebot.ReplyMarkup{}
			menu.Inline(
				menu.Row(
					menu.Data("تخصیص طرح", "admin_assign_plan", fmt.Sprintf("%d", sub.ID)),
				),
			)
			caption := fmt.Sprintf("📥 **درخواست تخصیص طرح**\n\nنماینده: @%s (%d)\nایمیل اشتراک: `%s`\nشناسه اشتراک: `%s`\n\nلطفا برای این اشتراک یک طرح انتخاب کنید:",
				user.Username, user.TelegramID, sub.ClientEmail, sub.SubID)
			_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, FormatMarkdown(caption), menu, telebot.ModeMarkdown)
		}
	}

	return c.Send("درخواست شما برای تخصیص طرح با موفقیت به مدیریت ارسال شد. پس از بررسی، امکانات تمدید و ارتقا فعال خواهد شد.")
}

func syncActivationExpiry(sub *db.Subscription, client xui.XUIClientInfo) bool {
	if client.ExpiryTime > 0 && (sub.ExpireTime == nil || *sub.ExpireTime <= 0 || sub.EndDate.IsZero()) {
		log.Printf("Syncing activation expiry time for %s: XUI has %s", sub.ClientEmail, time.UnixMilli(client.ExpiryTime).Format("2006-01-02"))
		val := client.ExpiryTime
		sub.ExpireTime = &val
		sub.EndDate = time.UnixMilli(client.ExpiryTime)
		return true
	}
	return false
}

func syncIPLimitFromXUI(sub *db.Subscription) {
	if bot.XUIClient == nil {
		return
	}
	clients, err := bot.XUIClient.ListClients()
	if err != nil {
		log.Printf("XUI ListClients failed during sync: %v", err)
		return
	}
	for _, client := range clients {
		if client.Email == sub.ClientEmail {
			changed := false
			if devLimit, ok := parseDeviceLimitFromXUI(client); ok && devLimit != sub.IPLimit {
				log.Printf("Syncing device limit for %s: DB had %d, XUI has %d", sub.ClientEmail, sub.IPLimit, devLimit)
				sub.IPLimit = devLimit
				changed = true
			}
			if syncActivationExpiry(sub, client) {
				changed = true
			}
			if changed {
				_ = db.UpdateSubscription(context.Background(), sub)
			}
			break
		}
	}
}
