package handlers

import (
	"context"
	"fmt"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/db"
)

func RegisterAdminStats(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_stats", HandleAdminStats, auth, admin)
}

func HandleAdminStats(c telebot.Context) error {
	ctx := context.Background()

	totalUsers, _ := db.GetAllUsersCount(ctx)
	approvedUsers, _ := db.GetApprovedUsersCount(ctx)
	activeSubs, _ := db.GetActiveSubscriptionsCount(ctx)
	pendingTopups, _ := db.GetPendingDepositsCount(ctx)
	testsToday, _ := db.GetTestsCreatedToday(ctx)
	monthlyRevenue, _ := db.GetMonthlyRevenue(ctx)

	currency, _ := db.GetSetting(ctx, "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	text := fmt.Sprintf(
		"📊 **Bot Statistics**\n\n"+
			"👥 Total users: %d\n"+
			"✅ Approved users: %d\n"+
			"📦 Active subscriptions: %d\n"+
			"🧪 Test subs created today: %d\n"+
			"📥 Pending top-ups: %d\n"+
			"💰 Revenue this month: %.0f %s",
		totalUsers, approvedUsers, activeSubs, testsToday, pendingTopups, monthlyRevenue, currency)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📥 Pending Top-Ups", "admin_pending_topups")),
		menu.Row(menu.Data("« Back", "admin_menu")),
	)
	return maybeEditOrSend(c, text, menu)
}

