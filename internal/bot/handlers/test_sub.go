package handlers

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

func RegisterTestSub(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_test_sub", HandleTestSubFlow, auth)
	b.Handle("\fselect_test_plan", HandleSelectTestPlan, auth)
	b.Handle("\fts_custom", HandleTestCustom, auth)
	b.Handle("\fts_random", HandleTestRandom, auth)
	b.Handle("\fts_multi", HandleMultipleTestsChoice, auth)
	b.Handle("\fts_multi_custom", HandleMultipleTestsCustom, auth)
	b.Handle("\fts_multi_random", HandleMultipleTestsRandom, auth)
	b.Handle("\fts_multi_run", HandleMultipleTestsRun, auth)

	// Backward-compatible callbacks
	b.Handle("\fmultiple_tests_custom", HandleMultipleTestsCustom, auth)
	b.Handle("\fmultiple_tests_random", HandleMultipleTestsRandom, auth)
}

func HandleTestSubFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	plans, err := db.GetTestPlansForUser(context.Background(), user.ID, false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌های تست.")
	}
	if len(plans) == 0 {
		return maybeEditOrSend(c, "در حال حاضر هیچ طرح تستی موجود نیست.")
	}

	var text strings.Builder
	text.WriteString("🧪 **اشتراک‌های تست رایگان**:\n\n")

	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(plans)+1)
	unapprovedLimitStr, _ := db.GetSetting(context.Background(), "unapproved_test_limit_per_plan")
	unapprovedLimit, _ := strconv.Atoi(unapprovedLimitStr)
	if unapprovedLimit <= 0 {
		unapprovedLimit = 1
	}

	for _, plan := range plans {
		usedCount, _ := db.GetTestUsageToday(context.Background(), user.ID, plan.ID)
		
		unapprovedLimitStr, _ := db.GetSetting(context.Background(), "unapproved_test_limit_per_plan")
		limit := DetermineTestLimit(user.IsApproved(), plan.MaxPerDay, unapprovedLimitStr)

		canClaim := usedCount < limit
		var statusText string
		var btnLabel string
		if canClaim {
			statusText = fmt.Sprintf("مجاز به دریافت (%d از %d استفاده شده)", usedCount, limit)
			btnLabel = plan.Name
		} else {
			statusText = fmt.Sprintf("سقف دریافت روزانه تکمیل شده (%d از %d)", usedCount, limit)
			btnLabel = fmt.Sprintf("%s (غیرمجاز)", plan.Name)
		}

		var dataLimitStr string
		if plan.MaxDataBytes == 0 {
			dataLimitStr = "نامحدود"
		} else {
			dataLimitStr = fmt.Sprintf("%.2f گیگابایت", float64(plan.MaxDataBytes)/1073741824)
		}
		durationStr := humanDuration(plan.ExpireSeconds)
		text.WriteString(fmt.Sprintf("📦 **%s**\n⏱️ مدت اعتبار: %s (پس از اولین اتصال)\n📊 حجم مجاز: %s\n🔄 وضعیت: %s\n",
			plan.Name, durationStr, dataLimitStr, statusText))
		if plan.Description != "" {
			text.WriteString(fmt.Sprintf("%s\n", plan.Description))
		}
		text.WriteString("\n")
		rows = append(rows, menu.Row(menu.Data(btnLabel, "select_test_plan", fmt.Sprintf("%d", plan.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, strings.TrimSpace(text.String()), menu)
}

func HandleSelectTestPlan(c telebot.Context) error {
	planID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("طرح تست نامعتبر است.")
	}
	plan, err := db.GetTestPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح تست یافت نشد.")
	}

	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	usedCount, _ := db.GetTestUsageToday(context.Background(), user.ID, plan.ID)
	
	limitStr, _ := db.GetSetting(context.Background(), "unapproved_test_limit_per_plan")
	limit := DetermineTestLimit(user.IsApproved(), plan.MaxPerDay, limitStr)

	if usedCount >= limit {
		return c.Respond(&telebot.CallbackResponse{
			Text:      "سقف دریافت تست روزانه شما برای این طرح تکمیل شده است. لطفا فردا مجددا تلاش کنید.",
			ShowAlert: true,
		})
	}

	email := fmt.Sprintf("test_%s_%s", randomName(), randomToken(4))
	return generateTestSubscription(c, user, plan.ID, email)
}

func HandleTestCustom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessTestCustomName(c telebot.Context, customName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func HandleTestRandom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsChoice(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsCustom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func HandleMultipleTestsRandom(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func ProcessMultipleTestsBaseName(c telebot.Context, baseName string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func ProcessMultipleTestsCount(c telebot.Context, countStr string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func HandleMultipleTestsRun(c telebot.Context) error {
	return c.Respond(&telebot.CallbackResponse{Text: "این امکان غیرفعال شده است.", ShowAlert: true})
}

func generateMultipleTests(c telebot.Context, user *db.User, planID int64, count int, naming, base string) error {
	return c.Send("این امکان غیرفعال شده است.")
}

func generateTestSubscription(c telebot.Context, user *db.User, planID int64, email string) error {
	unlock := bot.Locker.Lock(fmt.Sprintf("user_test:%d", user.ID))
	defer unlock()

	plan, err := db.GetTestPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح تست یافت نشد.")
	}
	usedCount, _ := db.GetTestUsageToday(context.Background(), user.ID, plan.ID)
	
	limitStr, _ := db.GetSetting(context.Background(), "unapproved_test_limit_per_plan")
	limit := DetermineTestLimit(user.IsApproved(), plan.MaxPerDay, limitStr)

	if usedCount >= limit {
		return c.Send("سقف دریافت تست روزانه شما برای این طرح تکمیل شده است. لطفا فردا مجددا تلاش کنید.")
	}

	if err := createAndSendTest(c, user, plan, email); err != nil {
		return err
	}
	_ = db.IncrementTestUsageToday(context.Background(), user.ID, plan.ID, 1)
	return showMainMenu(c, user)
}

func createAndSendTest(c telebot.Context, user *db.User, plan *db.TestPlan, email string) error {
	if bot.XUIClient == nil {
		return c.Send("خطا: کلاینت x-ui متصل نیست.")
	}
	if existing, _ := db.GetSubscriptionByEmail(context.Background(), email); existing != nil {
		return c.Send("نام تولید شده قبلا انتخاب شده است. لطفا مجددا تلاش کنید.")
	}

	expireMilli := -int64(plan.ExpireSeconds * 1000)
	var expireAt time.Time
	subID := makeSubID()
	clientUUID := makeClientUUID()
	comment := fmt.Sprintf("created by xui-reseller-bot, %s, %s", plan.Name, userIdentifier(user))
	client := newClientConfig(email, serviceGroup(user), user.TelegramID, plan.MaxDataBytes, expireMilli, plan.IPLimit, plan.Flow, subID, clientUUID, comment)
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return c.Send("این طرح تست هیچ کانکشن معتبری ندارد.")
	}

	err := bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
	if err != nil {
		log.Printf("XUI AddClient failed: %v. Refreshing cache and retrying...", err)
		if bot.XUIClient.Cache != nil {
			bot.XUIClient.Cache.RefreshSync()
			newInboundIDs := validInboundIDs(plan.InboundIDs)
			if !intSlicesEqual(newInboundIDs, inboundIDs) {
				if len(newInboundIDs) == 0 {
					return c.Send("خطا: بعد از بازخوانی کانکشن‌ها، کانکشن معتبری پیدا نشد.")
				}
				inboundIDs = newInboundIDs
				err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
			}
		}
	}
	if err != nil {
		return c.Send("خطا در ایجاد اشتراک تست در پنل: " + err.Error())
	}

	planID := int(plan.ID)
	sub := &db.Subscription{
		UserID:      user.ID,
		PlanID:      &planID,
		ClientEmail: email,
		ClientUUID:  clientUUID,
		SubID:       subID,
		Status:      "active",
		PlanType:    db.PlanTypeTest,
		DisplayName: email,
		IPLimit:     plan.IPLimit,
		ExpireTime:  &expireMilli,
		IsActive:    true,
		StartDate:   nowUTC(),
		EndDate:     expireAt,
	}
	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		_ = bot.XUIClient.DeleteClient(email)
		return c.Send("خطا در ذخیره‌سازی اشتراک تست. عملیات در پنل خنثی شد.")
	}

	links, err := bot.XUIClient.GetSubscriptionLinks(subID)
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
		subLink = bot.XUIClient.SubscriptionURLFor(subID)
	}
	var dataLimitStr string
	if plan.MaxDataBytes == 0 {
		dataLimitStr = "نامحدود"
	} else {
		dataLimitStr = fmt.Sprintf("%.2f گیگابایت", float64(plan.MaxDataBytes)/1073741824)
	}
	durationStr := humanDuration(plan.ExpireSeconds)
	detailsMsg := fmt.Sprintf("✅ اشتراک تست رایگان شما آماده شد!\n📦 طرح: %s\n⏱️ مدت اعتبار: %s (پس از اولین اتصال شروع می‌شود)\n📊 حجم مجاز: %s", plan.Name, durationStr, dataLimitStr)

	if plan.UsageDescription != "" {
		detailsMsg += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.UsageDescription)
	}

	if err := sendSubscriptionResult(c, subLink, detailsMsg); err != nil {
		_ = c.Send(detailsMsg + "\n`" + subLink + "`", telebot.ModeMarkdown)
	}
	return nil
}

func getTestResetDays() int {
	resetDaysStr, _ := db.GetSetting(context.Background(), "test_reset_days")
	resetDays, err := strconv.Atoi(strings.TrimSpace(resetDaysStr))
	if err != nil || resetDays < 0 {
		return 30 // default to 30 days
	}
	return resetDays
}

func nextUTCReset() string {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	return next.Format("15:04")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func DetermineTestLimit(isApproved bool, planMaxPerDay int, unapprovedSettingStr string) int {
	if isApproved {
		return planMaxPerDay
	}
	limit, _ := strconv.Atoi(unapprovedSettingStr)
	if limit <= 0 {
		return 1
	}
	return limit
}

