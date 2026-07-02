package handlers

import (
	"gopkg.in/telebot.v3"
)

func RegisterAdminMenu(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("/admin", HandleAdminMenu, auth, admin)
	b.Handle("\fadmin_menu", HandleAdminMenu, auth, admin)
	b.Handle("\fadmin_flow", HandleAdminFlow, auth, admin)
	b.Handle("\fadmin_pending_topups", HandleAdminPendingTopups, auth, admin)
	b.Handle("\fadmin_pending_claims", HandleAdminPendingClaims, auth, admin)

	RegisterAdminUsers(b, auth, admin)
	RegisterAdminSettings(b, auth, admin)
	RegisterAdminPlans(b, auth, admin)
	RegisterAdminStats(b, auth, admin)
}

func HandleAdminMenu(c telebot.Context) error {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("👥 Users", "admin_users"), menu.Data("📋 Plans", "admin_plans")),
		menu.Row(menu.Data("⚙️ Settings", "admin_settings"), menu.Data("📊 Stats", "admin_stats")),
		menu.Row(
			menu.Data("📥 Pending Top-Ups", "admin_pending_topups"),
			menu.Data("🔍 Pending Claims", "admin_pending_claims"),
		),
	)
	return maybeEditOrSend(c, "⚙️ **Admin Panel**", menu)
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
	default:
		return HandleAdminMenu(c)
	}
}
