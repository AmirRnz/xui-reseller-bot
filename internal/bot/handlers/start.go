package handlers

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

func RegisterStart(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc, adminCfg *config.AdminConfig) {
	b.Handle("/start", HandleStart, auth)
	b.Handle(telebot.OnText, HandleText, auth)
	b.Handle("\fpayment_intent_resume", HandlePaymentIntentResume, auth)
	b.Handle("\fpayment_intent_cancel_prompt", HandlePaymentIntentCancelPrompt, auth)
	b.Handle("\fpayment_intent_cancel_confirm", HandlePaymentIntentCancelConfirm, auth)
	b.Handle("\fmenu_main", func(c telebot.Context) error {
		user := userFromContext(c)
		if user == nil {
			return HandleStart(c)
		}
		return showMainMenu(c, user)
	}, auth)
	b.Handle("\fmenu_support", func(c telebot.Context) error {
		support, _ := db.GetSetting(context.Background(), "support_username")
		support = strings.TrimSpace(support)
		if support != "" {
			if !strings.HasPrefix(support, "@") {
				support = "@" + support
			}
			return c.Send(fmt.Sprintf("برای پشتیبانی لطفا با آی‌دی زیر در ارتباط باشید:\n%s", support))
		}
		return c.Send("برای پشتیبانی لطفا با ادمین در ارتباط باشید.")
	}, auth)

	handleRequestAccess := func(c telebot.Context) error {
		user := userFromContext(c)
		if user == nil {
			return c.Send("خطا.")
		}
		if user.Status != db.UserStatusPending {
			return c.Send("شما قبلا درخواست دسترسی داده‌اید یا تایید شده‌اید.")
		}

		err := c.Send("✅ درخواست دسترسی شما برای ادمین ارسال شد. پس از تایید به شما اطلاع داده خواهد شد.")
		msg := fmt.Sprintf("📝 درخواست دسترسی نمایندگی جدید:\nکاربر: %s\nآیدی عددی: `%d`\n\nبرای تایید به منوی ادمین مراجعه کنید.", userIdentifier(user), user.TelegramID)
		for _, adminID := range adminCfg.AdminIDs {
			adminUser := &telebot.User{ID: adminID}
			_, _ = bot.Bot.Send(adminUser, msg, telebot.ModeMarkdown)
		}
		return err
	}
	b.Handle("\fmenu_request_access", handleRequestAccess, auth)
	b.Handle("\frequest_access", handleRequestAccess, auth)
}

func HandleStart(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری. لطفا مجددا تلاش کنید.")
	}
	bot.FSM.ClearState(user.TelegramID) // Reset state on start

	if user.Status == db.UserStatusBanned {
		return c.Send("حساب کاربری شما مسدود شده است.")
	}

	return showMainMenu(c, user)
}

func HandleText(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}

	text := strings.TrimSpace(c.Text())
	if text == "" {
		return c.Send("لطفا متن مربوط به مرحله فعلی را ارسال کنید.")
	}

	if strings.HasPrefix(text, "/") {
		if text == "/cancel" || text == "/cancel@bot" {
			bot.FSM.ClearState(user.TelegramID)
			if intent, err := db.GetLatestActivePaymentIntent(context.Background(), user.ID); err == nil && intent != nil {
				return showPaymentIntentRecovery(c, intent)
			}
			return showMainMenu(c, user)
		}
		bot.FSM.ClearState(user.TelegramID) // Clear state to execute command
		// let execution fallthrough to standard command routers
	} else if state := bot.FSM.GetState(user.TelegramID); state != nil {
		switch state.Step {
		case "awaiting_buy_months_text":
			return ProcessBuyMonthsText(c, text)
		case "awaiting_buy_data_gb_text":
			return ProcessBuyDataGBText(c, text)
		case "awaiting_buy_custom_name":
			return ProcessBuyCustomName(c, text)
		case "awaiting_test_custom_name":
			return ProcessTestCustomName(c, text)
		case "awaiting_multiple_tests_base_name":
			return ProcessMultipleTestsBaseName(c, text)
		case "awaiting_multiple_tests_count":
			return ProcessMultipleTestsCount(c, text)
		case "awaiting_receipt":
			return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
		case "awaiting_purchase_receipt":
			return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
		case "awaiting_topup_amount":
			return ProcessTopupApprovalAmount(c, text)
		case "awaiting_manual_credit":
			return ProcessManualCreditAmount(c, text)
		case "awaiting_bulk_credit_amount":
			return ProcessBulkCredit(c, text)
		case "awaiting_setting_card_number":
			return ProcessSettingText(c, "card_number", text)
		case "awaiting_setting_card_owner":
			return ProcessSettingText(c, "card_owner", text)
		case "awaiting_setting_min_topup":
			return ProcessSettingText(c, "min_topup_amount", text)
		case "awaiting_setting_topup_description":
			return ProcessSettingText(c, "topup_description", text)
		case "awaiting_setting_test_global_description":
			return ProcessSettingText(c, "test_global_description", text)
		case "awaiting_setting_expiry_notify_days":
			return ProcessSettingText(c, "expiry_notify_days", text)
		case "awaiting_setting_test_reset_days":
			return ProcessSettingText(c, "test_reset_days", text)
		case "awaiting_setting_ip_limit_factor":
			return ProcessSettingText(c, "ip_limit_factor", text)
		case "awaiting_setting_support_username":
			return ProcessSettingText(c, "support_username", text)
		case "awaiting_sub_rename":
			return ProcessSubscriptionRename(c, text)
		case "awaiting_extend_months_text":
			return ProcessExtendMonthsText(c, text)
		case "awaiting_admin_draft_input":
			return ProcessAdminDraftInput(c, text)
		case "awaiting_admin_plan_access":
			return ProcessAdminPlanAccess(c, text)
		case "awaiting_service_name":
			return ProcessServiceName(c, text)
		case "awaiting_admin_reconcile_close_reason":
			return ProcessAdminReconcileCloseReason(c, text)
		case "awaiting_admin_reconcile_waiver_reason":
			return ProcessAdminReconcileWaiveReason(c, text)
		case "awaiting_admin_refund_amount":
			return ProcessAdminRefundAmountText(c, text)
		case "awaiting_admin_refund_note":
			return ProcessAdminRefundNoteText(c, text)
		}
	}

	switch strings.ToLower(text) {
	case "free test", "/test", "تست رایگان":
		return HandleTestSubFlow(c)
	case "buy", "buy subscription", "/buy", "خرید سرویس":
		return HandleBuySubFlow(c)
	case "my services", "/services", "سرویس‌های من":
		return HandleMyServicesFlow(c)
	case "wallet", "/wallet", "کیف پول":
		return HandleWalletFlow(c)
	}

	return showMainMenu(c, user)
}

func isConfiguredAdmin(id int64) bool {
	if config.Global == nil {
		return false
	}
	for _, adminID := range config.Global.Admin.AdminIDs {
		if id == adminID {
			return true
		}
	}
	return false
}

func ProcessServiceName(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا.")
	}

	validPattern := regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)
	if !validPattern.MatchString(text) {
		return c.Send("نام نامعتبر است. فقط حروف انگلیسی، اعداد، خط تیره (-) و آندرلاین (_) مجاز است. طول نام باید بین ۳ تا ۳۲ کاراکتر باشد.\nلطفا دوباره وارد کنید:")
	}

	err := db.UpdateUserServiceName(context.Background(), user.TelegramID, text)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique constraint") || strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return c.Send("این نام قبلا توسط نماینده دیگری استفاده شده است. لطفا نام دیگری انتخاب کنید:")
		}
		return c.Send("خطا در ثبت نام سرویس.")
	}

	bot.FSM.ClearState(user.TelegramID)
	user.ServiceName = &text
	return showMainMenu(c, user)
}
