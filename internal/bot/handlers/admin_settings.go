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
		return settingPrompt(c, "awaiting_setting_card_number", "Send card number.")
	}, auth, admin)
	b.Handle("\fadmin_set_card_owner", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_card_owner", "Send card owner name.")
	}, auth, admin)
	b.Handle("\fadmin_set_currency", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_currency_name", "Send currency name, e.g. IRR.")
	}, auth, admin)
	b.Handle("\fadmin_set_min_topup", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_min_topup", "Send minimum top-up amount.")
	}, auth, admin)
	b.Handle("\fadmin_set_topup_desc", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_topup_description", "Send top-up instructions text.")
	}, auth, admin)
	b.Handle("\fadmin_set_expiry_notify_days", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_expiry_notify_days", "Send expiry notification days as comma-separated values, e.g. 3,1.")
	}, auth, admin)
	b.Handle("\fadmin_set_test_reset_days", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_test_reset_days", "Send test reset period in days (e.g. 30). Use 0 to disable reset limit.")
	}, auth, admin)
	b.Handle("\fadmin_set_support_username", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_support_username", "Send support Telegram username (with or without @).")
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
		return settingPrompt(c, "awaiting_setting_ip_limit_factor", "Send IP limit factor multiplier/addition (e.g. '*2' or '+3'). Send '0' or leave empty to disable.")
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

	text := fmt.Sprintf("⚙️ **Settings**\n\n💳 Card: %s\n👤 Owner: %s\n💱 Currency: %s\n💰 Minimum top-up: %s\n⏱️ Test reset days: %s\n🆘 Support username: %s\n🔔 Expiry notify days: %s\n🌐 IP Limit Mode: %s\n🌐 IP Limit Factor: %s",
		values["card_number"], values["card_owner"], values["currency_name"], values["min_topup_amount"], values["test_reset_days"], values["support_username"], values["expiry_notify_days"], strings.ToUpper(values["ip_limit_mode"]), values["ip_limit_factor"])

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("💳 Card", "admin_set_card"), menu.Data("👤 Owner", "admin_set_card_owner")),
		menu.Row(menu.Data("💱 Currency", "admin_set_currency"), menu.Data("💰 Min top-up", "admin_set_min_topup")),
		menu.Row(menu.Data("📝 Top-up text", "admin_set_topup_desc"), menu.Data("⏱️ Test reset days", "admin_set_test_reset_days")),
		menu.Row(menu.Data("🆘 Support User", "admin_set_support_username"), menu.Data("🔔 Expiry days", "admin_set_expiry_notify_days")),
		menu.Row(menu.Data("🌐 IP Limit Settings", "admin_set_ip_limit_factor")),
		menu.Row(menu.Data("🔄 Reset All User Tests", "admin_reset_tests")),
		menu.Row(menu.Data("« Back", "admin_menu")),
	)
	return maybeEditOrSend(c, text, menu)
}

func settingPrompt(c telebot.Context, step, prompt string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("Could not load admin account.")
	}
	bot.FSM.SetState(user.TelegramID, step, nil)
	return maybeEditOrSend(c, prompt)
}

func ProcessSettingText(c telebot.Context, key string, value string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("You do not have permission to use this command.")
	}
	value = strings.TrimSpace(value)
	switch key {
	case "min_topup_amount":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return c.Send("Minimum top-up must be a number.")
		}
	case "test_reset_days":
		v, err := strconv.Atoi(value)
		if err != nil || v < 0 {
			return c.Send("Test reset days must be zero or a positive integer.")
		}
	case "expiry_notify_days":
		for _, part := range strings.Split(value, ",") {
			v, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || v <= 0 {
				return c.Send("Expiry notification days must be positive integers separated by commas.")
			}
		}
	case "ip_limit_factor":
		if value != "" && value != "0" {
			if !strings.HasPrefix(value, "*") && !strings.HasPrefix(value, "+") {
				return c.Send("IP limit factor must start with '*' or '+' followed by a number (e.g. '*2' or '+3'). Send '0' or leave empty to disable.")
			}
			numStr := value[1:]
			num, err := strconv.Atoi(numStr)
			if err != nil || num < 0 {
				return c.Send("Invalid factor number. It must be a positive integer.")
			}
			if strings.HasPrefix(value, "*") && num == 0 {
				return c.Send("Multiplier cannot be 0.")
			}
		} else {
			value = ""
		}
	}
	if err := db.SetSetting(context.Background(), key, value); err != nil {
		return c.Send("Failed to save setting.")
	}
	bot.FSM.ClearState(user.TelegramID)
	_ = c.Send("✅ Setting saved.")
	if key == "ip_limit_factor" {
		return HandleAdminIPLimitSettings(c)
	}
	return HandleAdminSettings(c)
}

func HandleAdminResetTests(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}
	_, err := db.Pool.Exec(context.Background(), "DELETE FROM test_usage")
	if err != nil {
		return c.Send("Failed to reset tests: " + err.Error())
	}
	return c.Respond(&telebot.CallbackResponse{Text: "✅ تمامی تست‌های کاربران با موفقیت ریست شد.", ShowAlert: true})
}

func HandleAdminIPLimitSettings(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	mode, _ := db.GetSetting(context.Background(), "ip_limit_mode")
	if mode == "" {
		mode = "factor"
	}
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	if factor == "" {
		factor = "none"
	}

	text := fmt.Sprintf("🌐 **IP Limit & Device Settings**\n\n"+
		"Choose how IP limits are written to the 3x-ui panel:\n\n"+
		"🔸 **Exact Mode**: Saves raw device limit directly to panel's LimitIP.\n"+
		"🔸 **Factor Mode**: Multiplies device limit by a factor (e.g. *100) before saving to LimitIP.\n"+
		"🔸 **Comment Mode**: Saves 0 (unlimited) as LimitIP, and tracks allowed devices in XUI comments.\n\n"+
		"⚡ **Current Mode:** `%s`\n"+
		"⚙️ **Current Factor:** `%s` (only used in Factor Mode)",
		strings.ToUpper(mode), factor)

	menu := &telebot.ReplyMarkup{}
	
	var btnExact, btnFactor, btnComment telebot.Btn
	if mode == "exact" {
		btnExact = menu.Data("Exact Mode ✅", "admin_set_ip_mode_exact")
	} else {
		btnExact = menu.Data("Exact Mode", "admin_set_ip_mode_exact")
	}
	if mode == "factor" {
		btnFactor = menu.Data("Factor Mode ✅", "admin_set_ip_mode_factor")
	} else {
		btnFactor = menu.Data("Factor Mode", "admin_set_ip_mode_factor")
	}
	if mode == "comment" {
		btnComment = menu.Data("Comment Mode ✅", "admin_set_ip_mode_comment")
	} else {
		btnComment = menu.Data("Comment Mode", "admin_set_ip_mode_comment")
	}

	menu.Inline(
		menu.Row(btnExact),
		menu.Row(btnFactor),
		menu.Row(btnComment),
		menu.Row(menu.Data("✏️ Edit Factor", "admin_set_ip_factor_prompt")),
		menu.Row(menu.Data("🔄 Apply & Sync All Clients", "admin_sync_all_limits_prompt")),
		menu.Row(menu.Data("« Back to Settings", "admin_settings")),
	)

	return maybeEditOrSend(c, text, menu)
}

func setIPLimitMode(c telebot.Context, mode string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}
	if err := db.SetSetting(context.Background(), "ip_limit_mode", mode); err != nil {
		return c.Send("Failed to save mode.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "✅ Mode set to " + strings.ToUpper(mode)})
	return HandleAdminIPLimitSettings(c)
}

func HandleAdminSyncAllIPLimitsPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	mode, _ := db.GetSetting(context.Background(), "ip_limit_mode")
	if mode == "" {
		mode = "factor"
	}
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")

	text := fmt.Sprintf("⚠️ **Warning: Apply & Sync All Clients**\n\n"+
		"This action will update ALL active subscriptions on the 3x-ui panel to match the current mode (%s).\n\n"+
		"It will also normalize any old database limits (e.g. dividing by %s if they were stored with the factor).\n\n"+
		"Do you want to proceed?", strings.ToUpper(mode), factor)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("✅ Yes, Sync All", "admin_sync_all_limits_confirm")),
		menu.Row(menu.Data("❌ Cancel", "admin_ip_limit_settings")),
	)

	return maybeEditOrSend(c, text, menu)
}

func HandleAdminSyncAllIPLimitsConfirm(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("Permission denied.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: "🔄 Sync started..."})
	_ = maybeEditOrSend(c, "🔄 Synchronizing all clients to panel. Please wait...")

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
		return c.Send("Failed to load active subscriptions: " + err.Error())
	}

	// 3. Sync to XUI
	successCount := 0
	var syncErrors []string

	for _, sub := range subs {
		if err := updateXUIFromSubscription(sub); err != nil {
			log.Printf("Sync error for client %s: %v", sub.ClientEmail, err)
			syncErrors = append(syncErrors, fmt.Sprintf("Email %s: %v", sub.ClientEmail, err))
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
	report.WriteString("✅ **Sync Completed**\n\n")
	report.WriteString(fmt.Sprintf("⚡ **Active Mode:** `%s`\n", strings.ToUpper(mode)))
	report.WriteString(fmt.Sprintf("👥 **Successfully synced:** %d / %d clients\n", successCount, len(subs)))

	if len(syncErrors) > 0 {
		report.WriteString("\n⚠️ **Errors:**\n")
		for i, errStr := range syncErrors {
			if i >= 10 {
				report.WriteString(fmt.Sprintf("... and %d more errors", len(syncErrors)-10))
				break
			}
			report.WriteString(fmt.Sprintf("- %s\n", errStr))
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("« Back to IP Settings", "admin_ip_limit_settings")),
	)

	return maybeEditOrSend(c, report.String(), menu)
}

