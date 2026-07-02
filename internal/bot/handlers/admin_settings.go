package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
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
	b.Handle("\fadmin_set_group_name", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_group_name", "Send group name for subscriptions.")
	}, auth, admin)
	b.Handle("\fadmin_set_ip_limit_factor", func(c telebot.Context) error {
		return settingPrompt(c, "awaiting_setting_ip_limit_factor", "Send IP limit factor multiplier/addition (e.g. '*2' or '+3'). Send '0' or leave empty to disable.")
	}, auth, admin)
	b.Handle("\fadmin_reset_tests", HandleAdminResetTests, auth, admin)
}

func HandleAdminSettings(c telebot.Context) error {
	keys := []string{"card_number", "card_owner", "currency_name", "min_topup_amount", "test_reset_days", "support_username", "expiry_notify_days", "group_name", "ip_limit_factor"}
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

	text := fmt.Sprintf("⚙️ **Settings**\n\n💳 Card: %s\n👤 Owner: %s\n💱 Currency: %s\n💰 Minimum top-up: %s\n⏱️ Test reset days: %s\n🆘 Support username: %s\n🔔 Expiry notify days: %s\n👥 Group Name: %s\n🌐 IP Limit Factor: %s",
		values["card_number"], values["card_owner"], values["currency_name"], values["min_topup_amount"], values["test_reset_days"], values["support_username"], values["expiry_notify_days"], values["group_name"], values["ip_limit_factor"])

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("💳 Card", "admin_set_card"), menu.Data("👤 Owner", "admin_set_card_owner")),
		menu.Row(menu.Data("💱 Currency", "admin_set_currency"), menu.Data("💰 Min top-up", "admin_set_min_topup")),
		menu.Row(menu.Data("📝 Top-up text", "admin_set_topup_desc"), menu.Data("⏱️ Test reset days", "admin_set_test_reset_days")),
		menu.Row(menu.Data("🆘 Support User", "admin_set_support_username"), menu.Data("🔔 Expiry days", "admin_set_expiry_notify_days")),
		menu.Row(menu.Data("👥 Group Name", "admin_set_group_name"), menu.Data("🌐 IP Limit Factor", "admin_set_ip_limit_factor")),
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

