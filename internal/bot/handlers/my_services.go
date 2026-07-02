package handlers

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/config"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
)

const servicesPageSize = 6

func RegisterMyServices(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_my_services", HandleMyServicesFlow, auth)
	b.Handle("\fsvc_page", HandleMyServicesPage, auth)
	b.Handle("\fsvc_claim", HandleClaimSubscriptionPrompt, auth)
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
	subs, err := db.GetSubscriptionsByUserID(context.Background(), user.ID)
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
		clients, err := bot.XUIClient.ListClients()
		if err == nil {
			existingClients := make(map[string]bool)
			for _, client := range clients {
				if client.SubID != "" {
					existingClients[client.SubID] = true
				}
			}

			var activeSubs []*db.Subscription
			for _, sub := range subs {
				if existingClients[sub.SubID] {
					activeSubs = append(activeSubs, sub)
				} else {
					log.Printf("Deleting orphan subscription %s (SubID: %s) from DB because it no longer exists on 3x-ui.", sub.ClientEmail, sub.SubID)
					_ = db.DeleteSubscription(context.Background(), sub.ID)
				}
			}
			subs = activeSubs
		}
	}

	if len(subs) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("➕ ثبت اشتراک خریداری شده", "svc_claim")),
			menu.Row(menu.Data("« بازگشت", "menu_main")),
		)
		return maybeEditOrSend(c, "📋 شما در حال حاضر هیچ اشتراکی ندارید.\nجهت شروع می‌توانید از گزینه‌های 🧪 تست رایگان یا 💼 خرید سرویس استفاده کنید. یا اگر از قبل اشتراکی دارید، آن را ثبت کنید تا در ربات نمایش داده شود.", menu)
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
		expires := sub.EndDate.Format("2006-01-02")
		text.WriteString(fmt.Sprintf("%s %s — تاریخ انقضا %s\n", icon, sub.ClientEmail, expires))
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
	rows = append(rows, menu.Row(menu.Data("➕ ثبت اشتراک خریداری شده", "svc_claim")))
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

	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

	var text strings.Builder
	text.WriteString(fmt.Sprintf("📦 **%s**\n\n", sub.DisplayName))
	text.WriteString(fmt.Sprintf("📧 **ایمیل اشتراک:** `%s`\n", sub.ClientEmail))
	text.WriteString(fmt.Sprintf("⚡ **وضعیت سرویس:** %s\n", statusIcon))
	text.WriteString(fmt.Sprintf("👥 **کاربر همزمان:** %d\n", displayIPLimit))
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
		rows = append(rows, menu.Row(
			menu.Data("📶 افزایش کاربر همزمان", "sub_limit", fmt.Sprintf("%d", sub.ID)),
			menu.Data("⏳ تمدید سرویس", "sub_extend", fmt.Sprintf("%d", sub.ID)),
		))
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
	detailsMsg := fmt.Sprintf("🔗 اشتراک: **%s**\n📅 تاریخ انقضا: %s", sub.DisplayName, sub.EndDate.Format("2006-01-02 15:04 UTC"))
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
		return c.Send("خطا در اعمال تغییرات در پنل. لطفا مجددا تلاش کنید.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
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
	if currency == "" { currency = "IRR" }
	
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
		err := bot.XUIClient.DeleteClient(sub.ClientEmail)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return c.Send("خطا در حذف اشتراک از پنل 3x-ui. لطفا با پشتیبانی تماس بگیرید.")
		}
	}
	
	// Delete from DB
	err = db.DeleteSubscription(context.Background(), sub.ID)
	if err != nil {
		return c.Send("خطا در حذف اشتراک از دیتابیس.")
	}
	
	bot.FSM.ClearState(user.TelegramID)
	
	if refundAmount > 0 {
		req := &db.RefundRequest{
			UserID: user.ID,
			CalculatedAmount: refundAmount,
			Status: "pending",
		}
		// Try to link sub ID, but we just deleted it so it might be null or we can store its ID
		subIDVal := int64(sub.ID)
		req.SubscriptionID = &subIDVal
		
		db.CreateRefundRequest(context.Background(), req)
		
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
				if currency == "" { currency = "IRR" }
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
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

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

	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := float64(newLimit-displayIPLimit) * plan.PricePerExtraIP * float64(months)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "sub_limit_confirm", callbackPayload(c)),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "sub_limit_direct", callbackPayload(c)),
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
	if len(parts) != 2 {
		return c.Send("درخواست ارتقای کاربر نامعتبر است.")
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

	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

	months := monthsRemainingFrom(sub.EndDate)
	if months < 1 {
		months = 1
	}
	cost := float64(newLimit-displayIPLimit) * plan.PricePerExtraIP * float64(months)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	if err := db.DebitWalletBalance(context.Background(), user.ID, cost, "IP limit increase for sub ID: "+strconv.Itoa(int(sub.ID))); err != nil {
		return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه این ارتقا %.0f %s می‌باشد.", cost, currency))
	}

	oldLimit := sub.IPLimit
	sub.IPLimit = ApplyIPLimitFactor(newLimit, factor)
	if err := updateXUIFromSubscription(sub); err != nil {
		sub.IPLimit = oldLimit
		_ = db.CreditWalletBalance(context.Background(), user.ID, cost, "refund failed IP upgrade")
		return c.Send("خطا در بروزرسانی پنل. مبلغ ارتقا به کیف پول شما برگشت داده شد.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		_ = db.CreditWalletBalance(context.Background(), user.ID, cost, "refund failed IP upgrade save")
		return c.Send("خطا در ذخیره سازی دیتابیس. مبلغ ارتقا به کیف پول شما برگشت داده شد.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ تعداد کاربر همزمان به %d افزایش یافت.", newLimit)})
	_ = c.Send(fmt.Sprintf("✅ ارتقا با موفقیت انجام شد. سقف کاربر همزمان به %d کاربر افزایش یافت. هزینه کسر شده: %.0f %s.", newLimit, cost, currency))
	return showSubscriptionDetail(c, user, sub)
}

func HandleSubscriptionLimitSetDirect(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
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

	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

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

	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)

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

	expiryLabel := sub.EndDate.Format("2006-01-02")
	if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		durMs := -*sub.ExpireTime
		days := durMs / (24 * 3600 * 1000)
		if days > 0 {
			expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", days)
		} else {
			hours := durMs / (3600 * 1000)
			expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
		}
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
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	payload := fmt.Sprintf("%d:%d", months, sub.ID)
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
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
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
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)
	cost := calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	if err := db.DebitWalletBalance(context.Background(), user.ID, cost, "subscription extension: "+sub.ClientEmail); err != nil {
		return c.Send(fmt.Sprintf("موجودی کیف پول شما کافی نیست. هزینه تمدید %.0f %s می‌باشد.", cost, currency))
	}

	oldEnd := sub.EndDate
	var oldExpireTime *int64
	if sub.ExpireTime != nil {
		val := *sub.ExpireTime
		oldExpireTime = &val
	}

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

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		_ = db.CreditWalletBalance(context.Background(), user.ID, cost, "refund failed extension")
		return c.Send("خطا در بروزرسانی پنل. مبلغ تمدید به کیف پول شما بازگردانده شد.")
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		_ = db.CreditWalletBalance(context.Background(), user.ID, cost, "refund failed extension save")
		return c.Send("خطا در ذخیره‌سازی دیتابیس. مبلغ تمدید به کیف پول شما بازگردانده شد.")
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
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
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
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, factor)
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
	if len(parts) != 2 {
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
	comment := fmt.Sprintf("created by xui-end-bot, %s, %s", planName, userIdentifier(user))
	client := newClientConfig(email, group, tgID, total, expireMilli, sub.IPLimit, flow, sub.SubID, sub.ClientUUID, comment)
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

func HandleClaimSubscriptionPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_claim_subscription_link")

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("❌ انصراف", "menu_my_services")),
	)

	return maybeEditOrSend(c, "🔗 لطفا لینک اشتراک خریداری شده خود را ارسال کنید:\n\nمثال:\n`https://sub.domain.com/sub/xxxxxx`", menu)
}

func ProcessClaimSubscriptionLink(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}

	text = strings.TrimSpace(text)
	u, err := url.Parse(text)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.Send("لینک وارد شده نامعتبر است. لطفا یک لینک معتبر با قالب http/https ارسال کنید.")
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return c.Send("لینک وارد شده نامعتبر است. شناسه اشتراک یافت نشد.")
	}
	subID := parts[len(parts)-1]

	unlock := bot.Locker.Lock(fmt.Sprintf("claim_sub:%s", subID))
	defer unlock()

	// 1. Check if subID already exists in local DB
	existingSub, err := db.GetSubscriptionBySubID(context.Background(), subID)
	if err != nil {
		log.Printf("Error checking DB for subID %s: %v", subID, err)
	}
	if existingSub != nil {
		bot.FSM.ClearState(user.TelegramID)
		if existingSub.UserID == user.ID {
			return c.Send("این اشتراک در حال حاضر در لیست سرویس‌های شما قرار دارد.")
		}
		return c.Send("این اشتراک قبلا توسط کاربر دیگری ثبت شده است. در صورت نیاز با پشتیبانی در ارتباط باشید.")
	}

	// Check if there is already a pending claim for this subID
	pendingExists, err := db.HasPendingClaimRequest(context.Background(), subID)
	if err != nil {
		log.Printf("Error checking DB for pending claim subID %s: %v", subID, err)
	}
	if pendingExists {
		bot.FSM.ClearState(user.TelegramID)
		return c.Send("درخواست ثبت برای این اشتراک قبلا ثبت شده است و در انتظار بررسی ادمین می‌باشد.")
	}

	// 2. Fetch all clients from 3x-ui to verify it exists
	if bot.XUIClient == nil {
		return c.Send("خطا: کلاینت x-ui متصل نیست.")
	}

	clients, err := bot.XUIClient.ListClients()
	if err != nil {
		log.Printf("XUI ListClients failed: %v", err)
		return c.Send("خطا در دریافت لیست اشتراک‌ها از پنل. لطفا مجددا تلاش کنید.")
	}

	var targetClient *xui.XUIClientInfo
	for _, client := range clients {
		if client.SubID == subID {
			targetClient = &client
			break
		}
	}

	if targetClient == nil {
		return c.Send("اشتراک معتبری با این مشخصات در پنل یافت نشد. لطفا مطمئن شوید لینک ارسال شده صحیح است.")
	}

	// 3. Check if client email already exists in local DB (avoid UNIQUE violation on client_email)
	existingEmailSub, err := db.GetSubscriptionByEmail(context.Background(), targetClient.Email)
	if err != nil {
		log.Printf("Error checking DB for email %s: %v", targetClient.Email, err)
	}
	if existingEmailSub != nil {
		bot.FSM.ClearState(user.TelegramID)
		if existingEmailSub.UserID == user.ID {
			return c.Send("این اشتراک در حال حاضر در لیست سرویس‌های شما قرار دارد.")
		}
		return c.Send("این اشتراک قبلا توسط کاربر دیگری ثبت شده است. در صورت نیاز با پشتیبانی در ارتباط باشید.")
	}

	// 4. Create pending claim PurchaseRequest
	req := &db.PurchaseRequest{
		UserID:         user.ID,
		Type:           "claim",
		PlanID:         nil,
		SubscriptionID: nil,
		Price:          0,
		Months:         0,
		IPLimit:        targetClient.LimitIP,
		DataGB:         int(targetClient.TotalGB / 1073741824),
		CustomName:     subID,
		ClientEmail:    targetClient.Email,
		TelegramFileID: "claim",
		Status:         "pending",
	}

	if err := db.CreatePurchaseRequest(context.Background(), req); err != nil {
		log.Printf("Failed to create claim purchase request: %v", err)
		return c.Send("خطا در ثبت درخواست ثبت اشتراک دستی.")
	}

	bot.FSM.ClearState(user.TelegramID)

	// Notify User
	_ = c.Send("📥 درخواست ثبت اشتراک شما ثبت شد و در انتظار تایید ادمین می‌باشد.\nپس از تایید ادمین، سرویس به بخش «سرویس‌های من» اضافه خواهد شد.")

	// Notify Admins with Plan selection buttons
	paidPlans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil {
		log.Printf("Failed to fetch paid plans for claim approval menu: %v", err)
	}

	if config.Global != nil {
		for _, adminID := range config.Global.Admin.AdminIDs {
			menu := &telebot.ReplyMarkup{}
			var rows []telebot.Row
			for _, plan := range paidPlans {
				rows = append(rows, menu.Row(
					menu.Data(fmt.Sprintf("طرح: %s", plan.Name), "admin_claim_assign", fmt.Sprintf("%d:%d", req.ID, plan.ID)),
				))
			}
			rows = append(rows, menu.Row(
				menu.Data("❌ رد درخواست", "admin_reject_purchase", fmt.Sprintf("%d", req.ID)),
			))
			menu.Inline(rows...)

			caption := fmt.Sprintf("📥 **درخواست ثبت اشتراک دستی #%d**\n\nکاربر: @%s (%d)\nایمیل اشتراک: `%s`\nشناسه اشتراک: `%s`\nکاربر همزمان: %d\nحجم: %d گیگابایت\n\nلطفا یکی از طرح‌های زیر را برای این اشتراک انتخاب کنید تا تایید شود:",
				req.ID, user.Username, user.TelegramID, req.ClientEmail, req.CustomName, req.IPLimit, req.DataGB)

			_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, FormatMarkdown(caption), menu, telebot.ModeMarkdown)
		}
	}

	return showMainMenu(c, user)
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
			if client.LimitIP > 0 && client.LimitIP != sub.IPLimit {
				log.Printf("Syncing IP limit for %s: DB had %d, XUI has %d", sub.ClientEmail, sub.IPLimit, client.LimitIP)
				sub.IPLimit = client.LimitIP
				_ = db.UpdateSubscription(context.Background(), sub)
			}
			break
		}
	}
}


