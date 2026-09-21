package handlers

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/db"
)

func RegisterAdminSettings(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_settings", HandleAdminSettings, auth, admin)
	b.Handle("\fadmin_set_card", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_card_number", "لطفاً شماره کارت بانکی جهت دریافت واریزی‌ها را ارسال کنید:")
	}, auth, admin)
	b.Handle("\fadmin_set_card_owner", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_card_owner", "لطفاً نام صاحب کارت را ارسال کنید:")
	}, auth, admin)
	b.Handle("\fadmin_set_currency", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_currency_name", "لطفاً واحد پول نمایشی را وارد کنید (مثال: تومان یا IRR):")
	}, auth, admin)
	b.Handle("\fadmin_set_min_topup", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_min_topup", "لطفاً حداقل مبلغ شارژ کیف پول (به تومان) را ارسال کنید:")
	}, auth, admin)
	b.Handle("\fadmin_set_topup_desc", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_topup_description", "لطفاً متن و دستورالعمل شارژ کیف پول را ارسال کنید:")
	}, auth, admin)
	b.Handle("\fadmin_set_expiry_notify_days", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_expiry_notify_days", "لطفاً روزهای یادآوری پایان اشتراک را با کاما جدا کنید (مثال: 3,1):")
	}, auth, admin)
	b.Handle("\fadmin_set_test_reset_days", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_test_reset_days", "لطفاً دوره ریست سرویس تست (به روز) را ارسال کنید (مثال: 30). عدد 0 محدودیت را غیرفعال می‌کند:")
	}, auth, admin)
	b.Handle("\fadmin_set_support_username", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_support_username", "لطفاً آیدی پشتیبانی تلگرام را ارسال کنید (با یا بدون @):")
	}, auth, admin)
	b.Handle("\fadmin_set_ip_limit_factor", HandleAdminIPLimitSettings, auth, admin)
	b.Handle("\fadmin_ip_limit_settings", HandleAdminIPLimitSettings, auth, admin)
	b.Handle("\fadmin_set_ip_mode_exact", func(c telebot.Context) error {
		return setIPLimitMode(c, "exact")
	}, auth, admin)
	b.Handle("\fadmin_set_ip_mode_factor", func(c telebot.Context) error {
		return setIPLimitMode(c, "factor")
	}, auth, admin)
	b.Handle("\fadmin_set_ip_mode_comment", func(c telebot.Context) error {
		return setIPLimitMode(c, "comment")
	}, auth, admin)
	b.Handle("\fadmin_set_ip_factor_prompt", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_ip_limit_factor", "لطفاً ضریب محدودیت آی‌پی همزمان را ارسال کنید (مثلاً *2 یا +3). برای غیرفعال‌سازی 0 بفرستید:")
	}, auth, admin)
	b.Handle("\fadmin_sync_all_limits_prompt", HandleAdminSyncAllIPLimitsPrompt, auth, admin)
	b.Handle("\fadmin_sync_all_limits_confirm", HandleAdminSyncAllIPLimitsConfirm, auth, admin)
	b.Handle("\fadmin_reset_tests", HandleAdminResetTests, auth, admin)
}

func HandleAdminSettings(c telebot.Context) error {
	keys := []string{"card_number", "card_owner", "currency_name", "min_topup_amount", "test_reset_days", "support_username", "expiry_notify_days", "ip_limit_factor", "ip_limit_mode"}
	values := map[string]string{}
	for _, key := range keys {
		values[key], _ = db.GetSetting(context.Background(), key)
	}
	if values["test_reset_days"] == "" {
		values["test_reset_days"] = "30"
	}
	if values["ip_limit_factor"] == "" {
		values["ip_limit_factor"] = "none"
	}
	if values["ip_limit_mode"] == "" {
		values["ip_limit_mode"] = "factor"
	}

	text := fmt.Sprintf("⚙️ **تنظیمات سیستم و ربات**\n\n"+
		"💳 شماره کارت: `%s`\n"+
		"👤 نام صاحب کارت: %s\n"+
		"💱 واحد پول: %s\n"+
		"💰 حداقل شارژ: %s\n"+
		"⏱️ دوره ریست اکانت تست: %s روز\n"+
		"🆘 آیدی پشتیبانی: %s\n"+
		"🔔 روزهای اعلان انقضا: %s\n"+
		"🌐 حالت محدودیت آی‌پی همزمان: %s\n"+
		"🌐 ضریب محدودیت آی‌پی همزمان: %s",
		values["card_number"], values["card_owner"], values["currency_name"], values["min_topup_amount"], values["test_reset_days"], values["support_username"], values["expiry_notify_days"], strings.ToUpper(values["ip_limit_mode"]), values["ip_limit_factor"])

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("💳 شماره کارت", "admin_set_card"), menu.Data("👤 صاحب کارت", "admin_set_card_owner")),
		menu.Row(menu.Data("💱 واحد پول", "admin_set_currency"), menu.Data("💰 حداقل شارژ", "admin_set_min_topup")),
		menu.Row(menu.Data("📝 راهنمای شارژ", "admin_set_topup_desc"), menu.Data("⏱️ دوره ریست تست", "admin_set_test_reset_days")),
		menu.Row(menu.Data("🆘 آیدی پشتیبانی", "admin_set_support_username"), menu.Data("🔔 روزهای اعلان", "admin_set_expiry_notify_days")),
		menu.Row(menu.Data("🌐 تنظیمات محدودیت آی‌پی", "admin_set_ip_limit_factor")),
		menu.Row(menu.Data("🔄 ریست تست همه کاربران", "admin_reset_tests")),
		menu.Row(menu.Data("« بازگشت", "admin_menu")),
	)
	return maybeEditOrSend(c, text, menu)
}

func settingPrompt(c telebot.Context, step, prompt string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("امکان بارگذاری حساب ادمین وجود ندارد.")
	}
	bot.FSM.SetState(user.TelegramID, step, nil)
	return maybeEditOrSend(c, prompt)
}

func ProcessSettingText(c telebot.Context, key string, value string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("شما دسترسی به این بخش را ندارید.")
	}
	value = strings.TrimSpace(value)
	switch key {
	case "min_topup_amount":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return c.Send("حداقل مبلغ شارژ باید یک عدد معتبر باشد.")
		}
	case "test_reset_days":
		v, err := strconv.Atoi(value)
		if err != nil || v < 0 {
			return c.Send("دوره ریست تست باید صفر یا یک عدد صحیح مثبت باشد.")
		}
	case "expiry_notify_days":
		for _, part := range strings.Split(value, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || v <= 0 {
				return c.Send("روزهای اعلان انقضا باید اعداد صحیح مثبت جداشده با کاما باشند (مثال: 3,1).")
			}
		}
	case "ip_limit_factor":
		if value != "" && value != "0" {
			if !strings.HasPrefix(value, "*") && !strings.HasPrefix(value, "+") {
				return c.Send("ضریب محدودیت آی‌پی همزمان باید با '*' یا '+' شروع شده و شامل یک عدد باشد (مثلاً '*2' یا '+3'). برای غیرفعال‌سازی عدد 0 بفرستید.")
			}
			numStr := value[1:]
			num, err := strconv.Atoi(numStr)
			if err != nil || num < 0 {
				return c.Send("عدد ضریب نامعتبر است. باید یک عدد صحیح مثبت باشد.")
			}
			if strings.HasPrefix(value, "*") && num == 0 {
				return c.Send("ضریب ضرب نمی‌تواند صفر باشد.")
			}
		} else {
			value = ""
		}
	}
	if err := db.SetSetting(context.Background(), key, value); err != nil {
		return c.Send("خطا در ذخیره‌سازی تنظیمات.")
	}
	bot.FSM.ClearState(user.TelegramID)
	_ = c.Send("✅ تنظیمات با موفقیت ذخیره شد.")
	if key == "ip_limit_factor" {
		return HandleAdminIPLimitSettings(c)
	}
	return HandleAdminSettings(c)
}

func HandleAdminResetTests(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}
	_, err := db.Pool.Exec(context.Background(), "DELETE FROM test_usage")
	if err != nil {
		log.Printf("[ERROR] Failed to reset test usage: %v", err)
		return c.Send("خطا در بازنشانی تست‌ها.")
	}
	return c.Respond(&telebot.CallbackResponse{Text: "✅ تمامی تست‌های کاربران با موفقیت ریست شد.", ShowAlert: true})
}

func HandleAdminIPLimitSettings(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	mode, _ := db.GetSetting(context.Background(), "ip_limit_mode")
	if mode == "" {
		mode = "factor"
	}
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	if factor == "" {
		factor = "none"
	}

	text := fmt.Sprintf("🌐 **تنظیمات محدودیت آی‌پی همزمان (IP Limit)**\n\n"+
		"نحوه اعمال محدودیت آی‌پی همزمان در پنل 3x-ui را انتخاب کنید:\n\n"+
		"🔸 **حالت دقیق (Exact)**: محدودیت مجاز عیناً به عنوان LimitIP در پنل ثبت می‌شود.\n"+
		"🔸 **حالت ضریب (Factor)**: محدودیت مجاز در یک ضریب (مثلاً *100) ضرب شده و در LimitIP ثبت می‌شود.\n"+
		"🔸 **حالت کامنت (Comment)**: مقدار LimitIP عدد 0 (نامحدود) قرار می‌گیرد و محدودیت در کامنت پنل ثبت می‌شود.\n\n"+
		"⚡ **حالت فعلی:** `%s`\n"+
		"⚙️ **ضریب فعلی:** `%s` (فقط در حالت ضریب استفاده می‌شود)",
		strings.ToUpper(mode), factor)

	menu := &telebot.ReplyMarkup{}

	var btnExact, btnFactor, btnComment telebot.Btn
	if mode == "exact" {
		btnExact = menu.Data("حالت دقیق ✅", "admin_set_ip_mode_exact")
	} else {
		btnExact = menu.Data("حالت دقیق", "admin_set_ip_mode_exact")
	}
	if mode == "factor" {
		btnFactor = menu.Data("حالت ضریب ✅", "admin_set_ip_mode_factor")
	} else {
		btnFactor = menu.Data("حالت ضریب", "admin_set_ip_mode_factor")
	}
	if mode == "comment" {
		btnComment = menu.Data("حالت کامنت ✅", "admin_set_ip_mode_comment")
	} else {
		btnComment = menu.Data("حالت کامنت", "admin_set_ip_mode_comment")
	}

	menu.Inline(
		menu.Row(btnExact),
		menu.Row(btnFactor),
		menu.Row(btnComment),
		menu.Row(menu.Data("✏️ ویرایش ضریب", "admin_set_ip_factor_prompt")),
		menu.Row(menu.Data("🔄 اعمال و همگام‌سازی همه کلاینت‌ها", "admin_sync_all_limits_prompt")),
		menu.Row(menu.Data("« بازگشت به تنظیمات", "admin_settings")),
	)

	return maybeEditOrSend(c, text, menu)
}

func setIPLimitMode(c telebot.Context, mode string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}
	if err := db.SetSetting(context.Background(), "ip_limit_mode", mode); err != nil {
		return c.Send("خطا در ذخیره حالت.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "✅ حالت روی " + strings.ToUpper(mode) + " تنظیم شد."})
	return HandleAdminIPLimitSettings(c)
}

func HandleAdminSyncAllIPLimitsPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	mode, _ := db.GetSetting(context.Background(), "ip_limit_mode")
	if mode == "" {
		mode = "factor"
	}
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")

	text := fmt.Sprintf("⚠️ **هشدار: اعمال و همگام‌سازی تمامی کاربران**\n\n"+
		"این عملیات تمامی اشتراک‌های فعال را در پنل 3x-ui مطابق با حالت فعلی (%s) به‌روزرسانی می‌کند.\n\n"+
		"همچنین در صورت نیاز مقادیر قبلی دیتابیس را نرمال‌سازی می‌کند (مثلاً تقسیم بر %s).\n\n"+
		"آیا از انجام این عملیات اطمینان دارید؟", strings.ToUpper(mode), factor)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("✅ بله، همگام‌سازی شود", "admin_sync_all_limits_confirm")),
		menu.Row(menu.Data("❌ انصراف", "admin_ip_limit_settings")),
	)

	return maybeEditOrSend(c, text, menu)
}

func HandleAdminSyncAllIPLimitsConfirm(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: "🔄 شروع همگام‌سازی..."})
	_ = maybeEditOrSend(c, "🔄 در حال همگام‌سازی تمامی کلاینت‌ها با پنل... لطفاً شکیبا باشید.")

	ctx := context.Background()

	// 1. Normalize database limits if needed
	factor, _ := db.GetSetting(ctx, "ip_limit_factor")
	factor = strings.TrimSpace(factor)
	if factor != "" && strings.HasPrefix(factor, "*") {
		var mult int
		_, err := fmt.Sscanf(factor, "*%d", &mult)
		if err == nil && mult > 1 {
			_, err = db.Pool.Exec(ctx, `
				UPDATE subscriptions 
				SET ip_limit = ip_limit / $1 
				WHERE ip_limit >= $1
			`, mult)
			if err != nil {
				log.Printf("Sync error during DB normalization: %v", err)
			}
		}
	}

	// 2. Fetch all active subscriptions
	subs, err := db.GetActiveSubscriptions(ctx)
	if err != nil {
		log.Printf("[ERROR] Failed to fetch active subscriptions: %v", err)
		return c.Send("خطا در بارگذاری اشتراک‌های فعال.")
	}

	// 3. Sync to XUI
	successCount := 0
	var syncErrors []string

	for _, sub := range subs {
		if err := updateXUIFromSubscription(sub); err != nil {
			log.Printf("Sync error for client %s: %v", sub.ClientEmail, err)
			syncErrors = append(syncErrors, fmt.Sprintf("ایمیل %s: %v", sub.ClientEmail, err))
		} else {
			successCount++
		}
	}

	// 4. Report result
	mode, _ := db.GetSetting(ctx, "ip_limit_mode")
	if mode == "" {
		mode = "factor"
	}

	var report strings.Builder
	report.WriteString("✅ **همگام‌سازی به پایان رسید**\n\n")
	report.WriteString(fmt.Sprintf("⚡ **حالت فعال:** `%s`\n", strings.ToUpper(mode)))
	report.WriteString(fmt.Sprintf("👥 **با موفقیت همگام‌سازی شد:** %d از %d کلاینت\n", successCount, len(subs)))

	if len(syncErrors) > 0 {
		report.WriteString("\n⚠️ **خطاها:**\n")
		for i, errStr := range syncErrors {
			if i >= 10 {
				report.WriteString(fmt.Sprintf("... و %d خطای دیگر", len(syncErrors)-10))
				break
			}
			report.WriteString(fmt.Sprintf("- %s\n", errStr))
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("« بازگشت به تنظیمات آی‌پی", "admin_ip_limit_settings")),
	)

	return maybeEditOrSend(c, report.String(), menu)
}
