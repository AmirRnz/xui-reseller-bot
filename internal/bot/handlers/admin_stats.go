package handlers

import (
	"context"
	"fmt"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
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
		currency = "تومان"
	}

	text := fmt.Sprintf(
		"📊 **آمار و وضعیت ربات**\n\n"+
			"👥 کل کاربران: %d\n"+
			"✅ کاربران تایید شده: %d\n"+
			"📦 اشتراک‌های فعال: %d\n"+
			"🧪 تست‌های ایجاد شده امروز: %d\n"+
			"📥 درخواست‌های شارژ در انتظار: %d\n"+
			"💰 درآمد این ماه: %s",
		totalUsers, approvedUsers, activeSubs, testsToday, pendingTopups, persian.FormatMoney(int64(monthlyRevenue)))

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📥 شارژهای در انتظار", "admin_pending_topups")),
		menu.Row(menu.Data("« بازگشت", "admin_menu")),
	)
	return maybeEditOrSend(c, text, menu)
}
