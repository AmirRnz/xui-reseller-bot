package handlers

import (
	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot/persian"
)

func RegisterAdminMenu(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("/admin", HandleAdminMenu, auth, admin)
	b.Handle("\fadmin_menu", HandleAdminMenu, auth, admin)
	b.Handle("\fadmin_flow", HandleAdminFlow, auth, admin)
	b.Handle("\fadmin_pending_topups", HandleAdminPendingTopups, auth, admin)

	RegisterAdminUsers(b, auth, admin)
	RegisterAdminSettings(b, auth, admin)
	RegisterAdminPlans(b, auth, admin)
	RegisterAdminStats(b, auth, admin)
	RegisterAdminReconcile(b, auth, admin)
}

func HandleAdminMenu(c telebot.Context) error {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data(persian.BtnAdminUsers, "admin_users"), menu.Data(persian.BtnAdminPlans, "admin_plans")),
		menu.Row(menu.Data(persian.BtnAdminSettings, "admin_settings"), menu.Data(persian.BtnAdminStats, "admin_stats")),
		menu.Row(
			menu.Data(persian.BtnAdminTopups, "admin_pending_topups"),
		),
		menu.Row(menu.Data("🧾 رسیدهای نیازمند بررسی دستی", "admin_manual_payment_reviews")),
		menu.Row(menu.Data(persian.BtnAdminReconcile, "admin_reconcile")),
		menu.Row(menu.Data(persian.BtnBack, "menu_main")),
	)
	return maybeEditOrSend(c, "⚙️ **پنل مدیریت ربات**", menu)
}

func HandleAdminFlow(c telebot.Context) error {
	switch callbackPayload(c) {
	case "users":
		return HandleAdminUsers(c)
	case "plans":
		return HandleAdminPlans(c)
	case "settings":
		return HandleAdminSettings(c)
	case "stats":
		return HandleAdminStats(c)
	case "reconcile":
		return HandleAdminReconcile(c)
	default:
		return HandleAdminMenu(c)
	}
}
