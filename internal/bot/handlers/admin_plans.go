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
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

func RegisterAdminPlans(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_plans", HandleAdminPlans, auth, admin)
	b.Handle("\fadmin_refresh_inbounds", HandleAdminRefreshInbounds, auth, admin)
	b.Handle("\fadmin_create_test_plan", HandleCreateTestPlan, auth, admin)
	b.Handle("\fadmin_create_paid_plan", HandleCreatePaidPlan, auth, admin)
	b.Handle("\fadmin_view_plan", HandleAdminViewPlan, auth, admin)
	b.Handle("\fadmin_plan_toggle", HandleAdminPlanToggle, auth, admin)
	b.Handle("\fadmin_plan_delete", HandleAdminPlanDelete, auth, admin)
	b.Handle("\fadmin_plan_access", HandleAdminPlanAccessPrompt, auth, admin)
	b.Handle("\fadmin_plan_edit", HandleAdminPlanEdit, auth, admin)
	b.Handle("\fadmin_draft_edit", HandleAdminDraftEdit, auth, admin)
	b.Handle("\fadmin_draft_inbounds", HandleAdminDraftInboundsMenu, auth, admin)
	b.Handle("\fadmin_draft_toggle_inbound", HandleAdminDraftToggleInbound, auth, admin)
	b.Handle("\fadmin_draft_toggle_all", HandleAdminDraftToggleAll, auth, admin)
	b.Handle("\fadmin_draft_inbounds_done", HandleAdminDraftInboundsDone, auth, admin)
	b.Handle("\fadmin_draft_toggle_sync", HandleAdminDraftToggleSync, auth, admin)
	b.Handle("\fadmin_draft_toggle_limited", HandleAdminDraftToggleLimited, auth, admin)
	b.Handle("\fadmin_draft_set_flow", HandleAdminDraftSetFlow, auth, admin)
	b.Handle("\fadmin_draft_action", HandleAdminDraftAction, auth, admin)
}

func HandleAdminPlans(c telebot.Context) error {
	testPlans, _ := db.GetTestPlans(context.Background(), true)
	paidPlans, _ := db.GetPaidPlans(context.Background(), true)

	inboundNames := cachedInboundNames()

	var text strings.Builder
	text.WriteString("📋 **طرح‌های ربات**\n\n")
	text.WriteString("🧪 طرح‌های تست:\n")
	for _, p := range testPlans {
		enabledMark := "✅"
		if !p.Enabled {
			enabledMark = "🔴"
		}
		text.WriteString(fmt.Sprintf("%s #%d %s (حداکثر %d در روز، %s)\n",
			enabledMark, p.ID, p.Name, p.MaxPerDay, humanDuration(p.ExpireSeconds)))
	}
	text.WriteString("\n💼 طرح‌های خرید نقدی:\n")
	for _, p := range paidPlans {
		enabledMark := "✅"
		if !p.Enabled {
			enabledMark = "🔴"
		}
		var ipLabel string
		if p.BaseIPLimit == 0 && p.MaxIPLimit == 0 {
			ipLabel = "کاربر همزمان نامحدود"
		} else {
			ipLabel = fmt.Sprintf("%d-%d کاربر همزمان", p.BaseIPLimit, p.MaxIPLimit)
		}
		text.WriteString(fmt.Sprintf("%s #%d %s (قیمت پایه %s تومان، %s)\n",
			enabledMark, p.ID, p.Name, persian.FormatMoney(p.BasePriceToman), ipLabel))
	}

	if bot.XUIClient != nil {
		text.WriteString("\n📡 اینباندهای کش شده:\n")
		for _, inbound := range bot.XUIClient.GetCachedInbounds() {
			inboundNames[inbound.ID] = inbound.Remark
			text.WriteString(fmt.Sprintf("  شناسه %d: %s (%s:%d)\n", inbound.ID, inbound.Remark, inbound.Protocol, inbound.Port))
		}
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, p := range testPlans {
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("🧪 #%d %s", p.ID, p.Name), "admin_view_plan", fmt.Sprintf("test:%d", p.ID))))
	}
	for _, p := range paidPlans {
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("💼 #%d %s", p.ID, p.Name), "admin_view_plan", fmt.Sprintf("paid:%d", p.ID))))
	}
	rows = append(rows, menu.Row(
		menu.Data("➕ طرح تست جدید", "admin_create_test_plan"),
		menu.Data("➕ طرح خرید جدید", "admin_create_paid_plan"),
	))
	rows = append(rows, menu.Row(
		menu.Data("🔄 بازخوانی اینباندها", "admin_refresh_inbounds"),
	))
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "admin_menu")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminRefreshInbounds(c telebot.Context) error {
	if bot.XUIClient != nil && bot.XUIClient.Cache != nil {
		bot.XUIClient.Cache.RefreshSync()
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "🔄 کش اینباندها با موفقیت به‌روز شد."})
	return HandleAdminPlans(c)
}

func HandleCreateTestPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	draft := map[string]interface{}{
		"id":                int64(0),
		"type":              "test",
		"name":              "",
		"description":       "",
		"usage_description": "",
		"inbound_ids":       []int{},
		"expire_seconds":    int64(3600),
		"max_data_bytes":    int64(0),
		"flow":              "",
		"max_per_day":       1,
		"is_global":         true,
		"allowed_user_ids":  []int64{},
		"sync_subs":         true,
		"ip_limit":          1,
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_test_plan_menu", draft)
	return showAdminDraftTestPlanMenu(c, draft)
}

func HandleCreatePaidPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	draft := map[string]interface{}{
		"id":                          int64(0),
		"type":                        "paid",
		"name":                        "",
		"description":                 "",
		"usage_description":           "",
		"inbound_ids":                 []int{},
		"base_price_toman":            int64(0),
		"base_ip_limit":               1,
		"max_ip_limit":                1,
		"price_per_extra_ip_toman":    int64(0),
		"flow":                        "",
		"discount_tiers":              []db.DiscountTier{},
		"is_global":                   true,
		"allowed_user_ids":            []int64{},
		"sync_subs":                   true,
		"is_limited":                  false,
		"price_per_gb_toman":          int64(0),
		"min_data_gb":                 int64(0),
		"price_per_extra_month_toman": int64(0),
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_paid_plan_menu", draft)
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminPlanEdit(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("طرح نامعتبر است.")
	}

	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	ctx := context.Background()
	allowedUserIDs, _ := db.GetPlanUserAccess(ctx, planType, planID)

	if planType == db.PlanTypeTest {
		plan, err := db.GetTestPlanByID(ctx, planID)
		if err != nil || plan == nil {
			return c.Send("طرح تست یافت نشد.")
		}

		draft := map[string]interface{}{
			"id":                plan.ID,
			"type":              "test",
			"name":              plan.Name,
			"description":       plan.Description,
			"usage_description": plan.UsageDescription,
			"inbound_ids":       plan.InboundIDs,
			"expire_seconds":    plan.ExpireSeconds,
			"max_data_bytes":    plan.MaxDataBytes,
			"flow":              plan.Flow,
			"max_per_day":       plan.MaxPerDay,
			"is_global":         plan.IsGlobal,
			"allowed_user_ids":  allowedUserIDs,
			"sync_subs":         plan.SyncSubs,
			"ip_limit":          plan.IPLimit,
		}

		bot.FSM.SetState(user.TelegramID, "awaiting_admin_test_plan_menu", draft)
		return showAdminDraftTestPlanMenu(c, draft)
	} else {
		plan, err := db.GetPaidPlanByID(ctx, planID)
		if err != nil || plan == nil {
			return c.Send("طرح خرید یافت نشد.")
		}

		draft := map[string]interface{}{
			"id":                          plan.ID,
			"type":                        "paid",
			"name":                        plan.Name,
			"description":                 plan.Description,
			"usage_description":           plan.UsageDescription,
			"inbound_ids":                 plan.InboundIDs,
			"base_price_toman":            plan.BasePriceToman,
			"base_ip_limit":               plan.BaseIPLimit,
			"max_ip_limit":                plan.MaxIPLimit,
			"price_per_extra_ip_toman":    plan.PricePerExtraIPToman,
			"flow":                        plan.Flow,
			"discount_tiers":              plan.DiscountTiers,
			"is_global":                   plan.IsGlobal,
			"allowed_user_ids":            allowedUserIDs,
			"sync_subs":                   plan.SyncSubs,
			"is_limited":                  plan.IsLimited,
			"price_per_gb_toman":          plan.PricePerGBToman,
			"min_data_gb":                 plan.MinDataGB,
			"price_per_extra_month_toman": plan.PricePerExtraMonthToman,
		}

		bot.FSM.SetState(user.TelegramID, "awaiting_admin_paid_plan_menu", draft)
		return showAdminDraftPaidPlanMenu(c, draft)
	}
}

func showAdminDraftTestPlanMenu(c telebot.Context, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	description := draftGetString(draft, "description")
	usageDescription := draftGetString(draft, "usage_description")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	expireSeconds := draftGetInt64(draft, "expire_seconds")
	maxDataBytes := draftGetInt64(draft, "max_data_bytes")
	flow := draftGetString(draft, "flow")
	maxPerDay := draftGetInt(draft, "max_per_day")
	isGlobal := draftGetBool(draft, "is_global")
	syncSubs := draftGetBool(draft, "sync_subs")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")
	ipLimit := draftGetInt(draft, "ip_limit")

	inboundLabel := formatInboundLabel(inboundIDs)
	durationLabel := humanDuration(expireSeconds)
	dataLabel := "نامحدود"
	if maxDataBytes > 0 {
		dataLabel = fmt.Sprintf("%.2f گیگابایت", float64(maxDataBytes)/1073741824)
	}

	ipLimitLabel := fmt.Sprintf("%d کاربر همزمان", ipLimit)
	if ipLimit == 0 {
		ipLimitLabel = "نامحدود"
	}

	text := fmt.Sprintf("🧪 **پیکربندی پیش‌نویس طرح تست**\n\n"+
		"📝 نام: %s\n"+
		"📝 توضیحات: %s\n"+
		"📝 یادداشت‌های کاربری: %s\n"+
		"📡 اینباندها: %s\n"+
		"⏱️ مدت اعتبار: %s\n"+
		"💾 محدودیت حجم: %s\n"+
		"⚡ فلو: %s\n"+
		"📊 سقف مجاز روزانه: %d\n"+
		"👥 سطح دسترسی: %s\n"+
		"🔄 همگام‌سازی اشتراک‌های فعال: %t\n"+
		"🌐 محدودیت کاربران همزمان: %s\n",
		nonEmpty(name, "(تنظیم نشده)"),
		nonEmpty(description, "(تنظیم نشده)"),
		nonEmpty(usageDescription, "(تنظیم نشده)"),
		inboundLabel,
		durationLabel,
		dataLabel,
		nonEmpty(flow, "(پیش‌فرض/خالی)"),
		maxPerDay,
		formatAccessLabel(isGlobal, allowedUserIDs),
		syncSubs,
		ipLimitLabel)

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📝 نام", "admin_draft_edit", "test:name"), menu.Data("📝 توضیحات", "admin_draft_edit", "test:description"), menu.Data("📝 یادداشت‌ها", "admin_draft_edit", "test:usage_description")),
		menu.Row(menu.Data("📡 اینباندها", "admin_draft_inbounds", "test"), menu.Data("⏱️ مدت اعتبار", "admin_draft_edit", "test:duration")),
		menu.Row(menu.Data("💾 حجم مجاز", "admin_draft_edit", "test:max_data"), menu.Data("⚡ فلو", "admin_draft_edit", "test:flow")),
		menu.Row(menu.Data("📊 سقف روزانه", "admin_draft_edit", "test:max_per_day"), menu.Data("👥 دسترسی", "admin_draft_edit", "test:access")),
		menu.Row(menu.Data("🌐 کاربر همزمان", "admin_draft_edit", "test:ip_limit"), menu.Data("🔄 همگام‌سازی: "+toggleEmoji(syncSubs), "admin_draft_toggle_sync", "test")),
		menu.Row(menu.Data("💾 ذخیره طرح", "admin_draft_action", "test:save"), menu.Data("❌ انصراف", "admin_draft_action", "test:cancel")),
	)
	return maybeEditOrSend(c, text, menu)
}

func showAdminDraftPaidPlanMenu(c telebot.Context, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	description := draftGetString(draft, "description")
	usageDescription := draftGetString(draft, "usage_description")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	basePriceToman := draftGetInt64(draft, "base_price_toman")
	baseIP := draftGetInt(draft, "base_ip_limit")
	maxIP := draftGetInt(draft, "max_ip_limit")
	extraIPPriceToman := draftGetInt64(draft, "price_per_extra_ip_toman")
	flow := draftGetString(draft, "flow")
	discounts := draftGetDiscounts(draft, "discount_tiers")
	isGlobal := draftGetBool(draft, "is_global")
	syncSubs := draftGetBool(draft, "sync_subs")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")
	isLimited := draftGetBool(draft, "is_limited")
	pricePerGBToman := draftGetInt64(draft, "price_per_gb_toman")
	minDataGB := draftGetInt64(draft, "min_data_gb")
	pricePerExtraMonthToman := draftGetInt64(draft, "price_per_extra_month_toman")

	inboundLabel := formatInboundLabel(inboundIDs)
	discountLabel := formatDiscountLabel(discounts)

	var priceBlock string
	if isLimited {
		priceBlock = fmt.Sprintf(
			"💵 قیمت هر گیگابایت: %s تومان\n"+
				"💾 حداقل حجم: %d گیگابایت\n"+
				"⏱️ قیمت هر ماه اضافی: %s تومان\n",
			persian.FormatMoney(pricePerGBToman), minDataGB, persian.FormatMoney(pricePerExtraMonthToman))
	} else {
		priceBlock = fmt.Sprintf("💵 قیمت پایه: %s تومان\n", persian.FormatMoney(basePriceToman))
	}

	var ipLimitsLabel string
	if baseIP == 0 && maxIP == 0 {
		ipLimitsLabel = "کاربر همزمان نامحدود"
	} else {
		ipLimitsLabel = fmt.Sprintf("پایه %d - حداکثر %d", baseIP, maxIP)
	}

	planTypeLabel := "نامحدود"
	if isLimited {
		planTypeLabel = "حجمی (محدود)"
	}

	text := fmt.Sprintf("💼 **پیکربندی پیش‌نویس طرح خرید**\n\n"+
		"📝 نام: %s\n"+
		"📝 توضیحات: %s\n"+
		"📝 یادداشت‌های کاربری: %s\n"+
		"📡 اینباندها: %s\n"+
		"📊 نوع طرح: %s\n"+
		"%s"+
		"🌐 محدودیت کاربران همزمان: %s\n"+
		"💲 قیمت هر کاربر اضافی: %s تومان\n"+
		"⚡ فلو: %s\n"+
		"🏷️ تخفیف‌های دوره‌ای: %s\n"+
		"👥 سطح دسترسی: %s\n"+
		"🔄 همگام‌سازی اشتراک‌های فعال: %t\n",
		nonEmpty(name, "(تنظیم نشده)"),
		nonEmpty(description, "(تنظیم نشده)"),
		nonEmpty(usageDescription, "(تنظیم نشده)"),
		inboundLabel,
		planTypeLabel,
		priceBlock,
		ipLimitsLabel,
		persian.FormatMoney(extraIPPriceToman),
		nonEmpty(flow, "(پیش‌فرض/خالی)"),
		discountLabel,
		formatAccessLabel(isGlobal, allowedUserIDs),
		syncSubs)

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	rows = append(rows, menu.Row(menu.Data("📝 نام", "admin_draft_edit", "paid:name"), menu.Data("📝 توضیحات", "admin_draft_edit", "paid:description"), menu.Data("📝 یادداشت‌ها", "admin_draft_edit", "paid:usage_description")))
	rows = append(rows, menu.Row(menu.Data("📡 اینباندها", "admin_draft_inbounds", "paid")))

	typeLabel := "📊 نوع: نامحدود"
	if isLimited {
		typeLabel = "📊 نوع: حجمی (محدود)"
	}
	rows = append(rows, menu.Row(menu.Data(typeLabel, "admin_draft_toggle_limited")))

	if isLimited {
		rows = append(rows, menu.Row(menu.Data("💵 قیمت/گیگ", "admin_draft_edit", "paid:price_per_gb"), menu.Data("💾 حداقل حجم", "admin_draft_edit", "paid:min_data_gb")))
		rows = append(rows, menu.Row(menu.Data("⏱️ قیمت ماه اضافی", "admin_draft_edit", "paid:price_per_extra_month"), menu.Data("⚡ فلو", "admin_draft_edit", "paid:flow")))
	} else {
		rows = append(rows, menu.Row(menu.Data("💵 قیمت پایه", "admin_draft_edit", "paid:price"), menu.Data("⚡ فلو", "admin_draft_edit", "paid:flow")))
	}

	rows = append(rows, menu.Row(menu.Data("🌐 کاربران همزمان", "admin_draft_edit", "paid:ip_limits"), menu.Data("💲 قیمت کاربر اضافی", "admin_draft_edit", "paid:extra_ip")))
	rows = append(rows, menu.Row(menu.Data("🏷️ تخفیف‌ها", "admin_draft_edit", "paid:discounts"), menu.Data("👥 دسترسی", "admin_draft_edit", "paid:access")))
	rows = append(rows, menu.Row(menu.Data("🔄 همگام‌سازی: "+toggleEmoji(syncSubs), "admin_draft_toggle_sync", "paid")))
	rows = append(rows, menu.Row(menu.Data("💾 ذخیره طرح", "admin_draft_action", "paid:save"), menu.Data("❌ انصراف", "admin_draft_action", "paid:cancel")))

	menu.Inline(rows...)
	return maybeEditOrSend(c, text, menu)
}

func HandleAdminDraftToggleLimited(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	draft := state.Data
	isLimited := draftGetBool(draft, "is_limited")
	draft["is_limited"] = !isLimited

	bot.FSM.SetState(user.TelegramID, state.Step, draft)

	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftEdit(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}

	var draft map[string]interface{}
	if state.Step == "awaiting_admin_test_plan_menu" || state.Step == "awaiting_admin_paid_plan_menu" {
		draft = state.Data
	} else if d, ok := state.Data["draft"].(map[string]interface{}); ok {
		draft = d
	} else {
		return c.Send("وضعیت نامعتبر است.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("عملیات نامعتبر است.")
	}
	editingType := parts[0]
	editingField := parts[1]

	if editingField == "flow" {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("⚡ xtls-rprx-vision", "admin_draft_set_flow", editingType+":xtls-rprx-vision")),
			menu.Row(menu.Data("❌ بدون فلو (حذف)", "admin_draft_set_flow", editingType+":none")),
			menu.Row(menu.Data("✏️ مقدار سفارشی دستی", "admin_draft_set_flow", editingType+":custom")),
			menu.Row(menu.Data("⬅️ بازگشت", "admin_draft_set_flow", editingType+":back")),
		)
		currentFlow := draftGetString(draft, "flow")
		prompt := fmt.Sprintf("⚡ **انتخاب فلو**:\n\n**وضعیت فعلی**: %s", nonEmpty(currentFlow, "(پیش‌فرض/خالی)"))
		return maybeEditOrSend(c, prompt, menu)
	}

	inputStateData := map[string]interface{}{
		"editing_type":  editingType,
		"editing_field": editingField,
		"draft":         draft,
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_draft_input", inputStateData)

	var prompt string
	switch editingField {
	case "name":
		prompt = "📝 نام طرح را ارسال کنید (مثال: 'ماهانه استاندارد'):"
	case "description":
		prompt = "📝 توضیحات طرح را ارسال کنید (مثال: 'سرعت بالا، نامحدود'):"
	case "usage_description":
		prompt = "📝 نکات کاربری پس از خرید را ارسال کنید (مثال: راهنمای اتصال یا قوانین):"
	case "price":
		prompt = "💵 قیمت پایه را به تومان ارسال کنید (مثال: '200000'):"
	case "price_per_gb":
		prompt = "💵 قیمت هر گیگابایت را به تومان ارسال کنید (مثال: '10000'):"
	case "min_data_gb":
		prompt = "💾 حداقل حجم بر حسب گیگابایت را ارسال کنید (مثال: '10'):"
	case "price_per_extra_month":
		prompt = "⏱️ قیمت هر ماه اضافی را به تومان ارسال کنید (مثال: '20000'):"
	case "ip_limits":
		prompt = "🌐 محدودیت تعداد کاربر همزمان را به صورت 'پایه-حداکثر' (مثال: '1-6' یا '2-2') یا عدد '0' برای نامحدود ارسال کنید:"
	case "ip_limit":
		prompt = "🌐 محدودیت تعداد کاربر همزمان (مثال: '1' یا '2') یا عدد '0' برای نامحدود را ارسال کنید:"
	case "extra_ip":
		prompt = "💲 قیمت هر کاربر اضافی را به تومان ارسال کنید (مثال: '50000'):"
	case "discounts":
		prompt = "🏷️ تخفیف‌ها را به صورت 'ماه:درصد,ماه:درصد' (مثال: '3:10,6:20') یا '-' برای بدون تخفیف ارسال کنید:"
	case "access":
		prompt = "👥 شناسه‌های عددی تلگرام کاربران مجاز را با کاما جدا کنید یا '-' را برای دسترسی عمومی بفرستید:"
	case "duration":
		prompt = "⏱️ مدت اعتبار را بر حسب ساعت ارسال کنید (مثال: '24' یا '0.5' برای ۳۰ دقیقه):"
	case "max_data":
		prompt = "💾 سقف حجم بر حسب گیگابایت (مثال: '50' یا '0' برای نامحدود) را ارسال کنید:"
	case "max_per_day":
		prompt = "📊 سقف مجاز دریافت تست روزانه برای هر کاربر (مثال: '2') را ارسال کنید:"
	default:
		return c.Send("بخش ویرایش نامشخص است.")
	}

	prompt += "\n\nبرای انصراف از ویرایش این بخش، عبارت /cancel را ارسال کنید."
	return maybeEditOrSend(c, prompt)
}

func ProcessAdminDraftInput(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("شما دسترسی به این دستور را ندارید.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_admin_draft_input" {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}

	editingType := state.Data["editing_type"].(string)
	editingField := state.Data["editing_field"].(string)
	draft, ok := state.Data["draft"].(map[string]interface{})
	if !ok {
		return c.Send("خطا در پردازش اطلاعات پیش‌نویس طرح.")
	}

	text = strings.TrimSpace(text)
	if text == "/cancel" {
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+editingType+"_plan_menu", draft)
		if editingType == "test" {
			return showAdminDraftTestPlanMenu(c, draft)
		}
		return showAdminDraftPaidPlanMenu(c, draft)
	}

	switch editingField {
	case "name":
		if text == "" {
			return c.Send("نام طرح نمی‌تواند خالی باشد. لطفاً یک نام معتبر ارسال کنید:")
		}
		draft["name"] = text
	case "description":
		draft["description"] = text
	case "usage_description":
		draft["usage_description"] = text
	case "price":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val < 0 {
			return c.Send("قیمت باید عددی مثبت یا صفر باشد. لطفاً قیمت معتبری وارد کنید:")
		}
		draft["base_price_toman"] = val
	case "price_per_gb":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val < 0 {
			return c.Send("قیمت هر گیگابایت باید عددی مثبت یا صفر باشد. لطفاً قیمت معتبری وارد کنید:")
		}
		draft["price_per_gb_toman"] = val
	case "min_data_gb":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val <= 0 {
			return c.Send("حداقل حجم باید یک عدد صحیح مثبت بر حسب گیگابایت باشد. لطفاً مقدار معتبری وارد کنید:")
		}
		draft["min_data_gb"] = val
	case "price_per_extra_month":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val < 0 {
			return c.Send("قیمت هر ماه اضافی باید عددی مثبت یا صفر باشد. لطفاً قیمت معتبری وارد کنید:")
		}
		draft["price_per_extra_month_toman"] = val
	case "ip_limits":
		textLower := strings.ToLower(strings.TrimSpace(text))
		if textLower == "0" || textLower == "0-0" || textLower == "unlimited" || textLower == "-" {
			draft["base_ip_limit"] = 0
			draft["max_ip_limit"] = 0
			break
		}
		parts := strings.Split(text, "-")
		if len(parts) != 2 {
			return c.Send("محدودیت تعداد کاربر باید به صورت 'پایه-حداکثر' (مثلاً '1-6') یا '0' برای نامحدود باشد. مجدداً تلاش کنید:")
		}
		baseIP, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		maxIP, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || baseIP < 0 || maxIP < baseIP {
			return c.Send("محدودیت تعداد کاربر نامعتبر است. حداکثر کاربر باید بزرگتر یا مساوی پایه و نامنفی باشد:")
		}
		if baseIP == 0 && maxIP != 0 {
			return c.Send("اگر حداقل کاربر ۰ (نامحدود) باشد، حداکثر کاربر نیز باید ۰ باشد. مجدداً تلاش کنید:")
		}
		draft["base_ip_limit"] = baseIP
		draft["max_ip_limit"] = maxIP
	case "ip_limit":
		val, err := strconv.Atoi(text)
		if err != nil || val < 0 {
			return c.Send("محدودیت تعداد کاربر باید صفر (نامحدود) یا عدد صحیح مثبت باشد. مجدداً تلاش کنید:")
		}
		draft["ip_limit"] = val
	case "extra_ip":
		val, err := strconv.ParseInt(text, 10, 64)
		if err != nil || val < 0 {
			return c.Send("قیمت هر کاربر اضافی باید صفر یا عددی مثبت باشد. مجدداً تلاش کنید:")
		}
		draft["price_per_extra_ip_toman"] = val
	case "flow":
		if text == "-" {
			draft["flow"] = ""
		} else {
			draft["flow"] = CleanFlow(text)
		}
	case "discounts":
		if text == "-" {
			draft["discount_tiers"] = []db.DiscountTier{}
		} else {
			tiers, err := parseDiscounts(text)
			if err != nil {
				return c.Send("قالب تخفیف‌ها نامعتبر است. از فرمت 'ماه:درصد,ماه:درصد' (مثلاً '3:10,6:20') یا '-' برای بدون تخفیف استفاده کنید:")
			}
			draft["discount_tiers"] = tiers
		}
	case "access":
		if text == "-" || text == "" {
			draft["is_global"] = true
			draft["allowed_user_ids"] = []int64{}
		} else {
			userIDs, err := parseInternalUserIDs(text)
			if err != nil {
				return c.Send("شناسه‌های کاربری نامعتبر است. شناسه‌های عددی تلگرام را با کاما جدا کنید یا '-' را برای دسترسی همگانی بفرستید:")
			}
			draft["is_global"] = false
			draft["allowed_user_ids"] = userIDs
		}
	case "duration":
		hours, err := strconv.ParseFloat(text, 64)
		if err != nil || hours <= 0 {
			return c.Send("مدت زمان باید عددی مثبت بر حسب ساعت باشد (مثلاً '2' یا '0.5'). مجدداً تلاش کنید:")
		}
		draft["expire_seconds"] = int64(hours * 3600)
	case "max_data":
		gb, err := strconv.ParseFloat(text, 64)
		if err != nil || gb < 0 {
			return c.Send("سقف حجم باید صفر یا عددی مثبت بر حسب گیگابایت باشد. مجدداً تلاش کنید:")
		}
		draft["max_data_bytes"] = int64(gb * 1073741824)
	case "max_per_day":
		val, err := strconv.Atoi(text)
		if err != nil || val < 0 {
			return c.Send("سقف مجاز روزانه باید صفر یا یک عدد صحیح مثبت باشد. مجدداً تلاش کنید:")
		}
		draft["max_per_day"] = val
	default:
		return c.Send("بخش ویرایش نامشخص است.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+editingType+"_plan_menu", draft)
	if editingType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftToggleSync(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	planType := callbackPayload(c)
	draft := state.Data
	syncSubs := draftGetBool(draft, "sync_subs")
	draft["sync_subs"] = !syncSubs

	bot.FSM.SetState(user.TelegramID, state.Step, draft)

	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftSetFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("عملیات نامعتبر است.")
	}

	planType := parts[0]
	choice := parts[1]

	var draft map[string]interface{}
	if state.Step == "awaiting_admin_test_plan_menu" || state.Step == "awaiting_admin_paid_plan_menu" {
		draft = state.Data
	} else if d, ok := state.Data["draft"].(map[string]interface{}); ok {
		draft = d
	} else {
		draft = state.Data
	}

	if draft == nil {
		return c.Send("خطا در بارگذاری اطلاعات پیش‌نویس طرح.")
	}

	switch choice {
	case "xtls-rprx-vision":
		draft["flow"] = "xtls-rprx-vision"
		_ = c.Respond(&telebot.CallbackResponse{Text: "⚡ فلو روی xtls-rprx-vision تنظیم شد."})
	case "none":
		draft["flow"] = ""
		_ = c.Respond(&telebot.CallbackResponse{Text: "⚡ فلو حذف شد."})
	case "custom":
		inputStateData := map[string]interface{}{
			"editing_type":  planType,
			"editing_field": "flow",
			"draft":         draft,
		}
		bot.FSM.SetState(user.TelegramID, "awaiting_admin_draft_input", inputStateData)
		prompt := "⚡ مقدار سفارشی فلو را ارسال کنید (مثال: 'xtls-rprx-vision-less'):\n\nبرای انصراف /cancel را بفرستید."
		return maybeEditOrSend(c, prompt)
	case "back":
		// Do nothing
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_admin_"+planType+"_plan_menu", draft)
	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminDraftAction(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("عملیات نامعتبر است.")
	}
	planType := parts[0]
	action := parts[1]

	if action == "cancel" {
		bot.FSM.ClearState(user.TelegramID)
		_ = c.Respond(&telebot.CallbackResponse{Text: "❌ ویرایش طرح لغو شد."})
		return HandleAdminPlans(c)
	}

	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	draft := state.Data

	if action == "save" {
		name := draftGetString(draft, "name")
		if name == "" {
			_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ وارد کردن نام طرح الزامی است."})
			return c.Send("نام طرح الزامی است. لطفاً پیش از ذخیره آن را تنظیم کنید.")
		}

		inboundIDs := draftGetIntSlice(draft, "inbound_ids")
		if len(inboundIDs) == 0 {
			_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ حداقل یک اینباند باید انتخاب شود."})
			return c.Send("حداقل یک شناسه اینباند باید انتخاب شود. لطفاً پیش از ذخیره اینباندها را انتخاب کنید.")
		}

		if planType == "paid" {
			isLimited := draftGetBool(draft, "is_limited")
			if isLimited {
				pricePerGB := draftGetInt64(draft, "price_per_gb_toman")
				if pricePerGB <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ قیمت هر گیگابایت باید عددی مثبت باشد."})
					return c.Send("قیمت هر گیگابایت باید عددی مثبت باشد. لطفاً پیش از ذخیره آن را تنظیم کنید.")
				}
				minData := draftGetInt64(draft, "min_data_gb")
				if minData <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ حداقل حجم باید عددی مثبت باشد."})
					return c.Send("حداقل حجم باید عددی مثبت باشد. لطفاً پیش از ذخیره آن را تنظیم کنید.")
				}
			} else {
				basePrice := draftGetInt64(draft, "base_price_toman")
				if basePrice <= 0 {
					_ = c.Respond(&telebot.CallbackResponse{Text: "⚠️ قیمت پایه باید عددی مثبت باشد."})
					return c.Send("قیمت پایه باید عددی مثبت باشد. لطفاً پیش از ذخیره آن را تنظیم کنید.")
				}
			}
		}

		bot.FSM.ClearState(user.TelegramID)
		_ = c.Respond(&telebot.CallbackResponse{Text: "💾 طرح ذخیره شد."})
		return SaveDraftPlan(c, planType, draft)
	}

	return c.Send("عملیات نامعتبر است.")
}

func SaveDraftPlan(c telebot.Context, planType string, draft map[string]interface{}) error {
	name := draftGetString(draft, "name")
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")
	isGlobal := draftGetBool(draft, "is_global")
	allowedUserIDs := draftGetInt64Slice(draft, "allowed_user_ids")
	id := draftGetInt64(draft, "id")

	ctx := context.Background()

	if planType == "test" {
		expireSeconds := draftGetInt64(draft, "expire_seconds")
		maxDataBytes := draftGetInt64(draft, "max_data_bytes")
		flow := draftGetString(draft, "flow")
		maxPerDay := draftGetInt(draft, "max_per_day")
		description := draftGetString(draft, "description")
		usageDescription := draftGetString(draft, "usage_description")

		plan := &db.TestPlan{
			ID:               id,
			Name:             name,
			Description:      description,
			UsageDescription: usageDescription,
			InboundIDs:       inboundIDs,
			ExpireSeconds:    expireSeconds,
			MaxDataBytes:     maxDataBytes,
			Flow:             flow,
			MaxPerDay:        maxPerDay,
			IsGlobal:         isGlobal,
			Enabled:          true,
			SyncSubs:         draftGetBool(draft, "sync_subs"),
			IPLimit:          draftGetInt(draft, "ip_limit"),
		}

		if id > 0 {
			orig, err := db.GetTestPlanByID(ctx, id)
			if err != nil {
				log.Printf("[ERROR] Failed to retrieve test plan %d: %v", id, err)
				return c.Send("خطا در بازیابی اطلاعات طرح.")
			}

			if err := db.UpdateTestPlan(ctx, plan); err != nil {
				log.Printf("[ERROR] Failed to update test plan %d: %v", id, err)
				return c.Send("خطا در به‌روزرسانی طرح تست.")
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypeTest, id, allowedUserIDs); err != nil {
				log.Printf("[ERROR] Failed to save test plan %d user access: %v", id, err)
				return c.Send("طرح به‌روزرسانی شد، اما ذخیره دسترسی اختصاصی ناموفق بود.")
			}

			if orig != nil && draftGetBool(draft, "sync_subs") {
				added, removed := diffIntSlices(orig.InboundIDs, plan.InboundIDs)
				if len(added) > 0 || len(removed) > 0 {
					go syncPlanSubscriptions(id, db.PlanTypeTest, added, removed)
				}
			}

			_ = c.Send(fmt.Sprintf("✅ طرح تست شماره %d با موفقیت به‌روزرسانی شد: %s", id, name))
		} else {
			if err := db.CreateTestPlan(ctx, plan); err != nil {
				log.Printf("[ERROR] Failed to create test plan: %v", err)
				return c.Send("خطا در ایجاد طرح تست.")
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypeTest, plan.ID, allowedUserIDs); err != nil {
				log.Printf("[ERROR] Failed to save new test plan user access: %v", err)
				return c.Send("طرح ایجاد شد، اما ذخیره دسترسی اختصاصی ناموفق بود.")
			}

			notifyApprovedUsers("🧪 طرح تست جدید در دسترس قرار گرفت: " + plan.Name)
			_ = c.Send(fmt.Sprintf("✅ طرح تست شماره %d با موفقیت ایجاد شد: %s", plan.ID, name))
		}

	} else {
		description := draftGetString(draft, "description")
		usageDescription := draftGetString(draft, "usage_description")
		basePriceToman := draftGetInt64(draft, "base_price_toman")
		baseIP := draftGetInt(draft, "base_ip_limit")
		maxIP := draftGetInt(draft, "max_ip_limit")
		extraIPToman := draftGetInt64(draft, "price_per_extra_ip_toman")
		flow := draftGetString(draft, "flow")
		discounts := draftGetDiscounts(draft, "discount_tiers")
		isLimited := draftGetBool(draft, "is_limited")
		pricePerGBToman := draftGetInt64(draft, "price_per_gb_toman")
		minData := draftGetInt64(draft, "min_data_gb")
		priceExtraMonthToman := draftGetInt64(draft, "price_per_extra_month_toman")

		plan := &db.PaidPlan{
			ID:                      id,
			Name:                    name,
			Description:             description,
			UsageDescription:        usageDescription,
			InboundIDs:              inboundIDs,
			BasePrice:               float64(basePriceToman), // legacy compatibility mirror
			BasePriceToman:          basePriceToman,
			BaseIPLimit:             baseIP,
			MaxIPLimit:              maxIP,
			PricePerExtraIP:         float64(extraIPToman), // legacy compatibility mirror
			PricePerExtraIPToman:    extraIPToman,
			Flow:                    flow,
			DiscountTiers:           discounts,
			IsGlobal:                isGlobal,
			Enabled:                 true,
			SyncSubs:                draftGetBool(draft, "sync_subs"),
			IsLimited:               isLimited,
			PricePerGB:              float64(pricePerGBToman), // legacy compatibility mirror
			PricePerGBToman:         pricePerGBToman,
			MinDataGB:               minData,
			PricePerExtraMonth:      float64(priceExtraMonthToman), // legacy compatibility mirror
			PricePerExtraMonthToman: priceExtraMonthToman,
		}

		if id > 0 {
			orig, err := db.GetPaidPlanByID(ctx, id)
			if err != nil {
				log.Printf("[ERROR] Failed to retrieve paid plan %d: %v", id, err)
				return c.Send("خطا در بازیابی اطلاعات طرح.")
			}

			if err := db.UpdatePaidPlan(ctx, plan); err != nil {
				log.Printf("[ERROR] Failed to update paid plan %d: %v", id, err)
				return c.Send("خطا در به‌روزرسانی طرح خرید.")
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypePaid, id, allowedUserIDs); err != nil {
				log.Printf("[ERROR] Failed to save paid plan %d user access: %v", id, err)
				return c.Send("طرح به‌روزرسانی شد، اما ذخیره دسترسی اختصاصی ناموفق بود.")
			}

			if orig != nil && draftGetBool(draft, "sync_subs") {
				added, removed := diffIntSlices(orig.InboundIDs, plan.InboundIDs)
				if len(added) > 0 || len(removed) > 0 {
					go syncPlanSubscriptions(id, db.PlanTypePaid, added, removed)
				}
			}

			_ = c.Send(fmt.Sprintf("✅ طرح خرید شماره %d با موفقیت به‌روزرسانی شد: %s", id, name))
		} else {
			if err := db.CreatePaidPlan(ctx, plan); err != nil {
				log.Printf("[ERROR] Failed to create paid plan: %v", err)
				return c.Send("خطا در ایجاد طرح خرید.")
			}

			if err := db.SetPlanUserAccess(ctx, db.PlanTypePaid, plan.ID, allowedUserIDs); err != nil {
				log.Printf("[ERROR] Failed to save new paid plan user access: %v", err)
				return c.Send("طرح ایجاد شد، اما ذخیره دسترسی اختصاصی ناموفق بود.")
			}

			notifyApprovedUsers("💼 طرح خرید جدید در دسترس قرار گرفت: " + plan.Name)
			_ = c.Send(fmt.Sprintf("✅ طرح خرید شماره %d با موفقیت ایجاد شد: %s", plan.ID, name))
		}
	}

	return HandleAdminPlans(c)
}

func syncPlanSubscriptions(planID int64, planType string, addedIDs, removedIDs []int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERY] syncPlanSubscriptions panicked: %v", r)
		}
	}()

	ctx := context.Background()
	subs, err := db.GetActiveSubscriptionsByPlan(ctx, planType, planID)
	if err != nil {
		log.Printf("Sync error: failed to fetch active subscriptions for plan %s:%d: %v", planType, planID, err)
		return
	}
	if len(subs) == 0 {
		return
	}

	emails := make([]string, 0, len(subs))
	for _, sub := range subs {
		emails = append(emails, sub.ClientEmail)
	}

	if bot.XUIClient == nil {
		log.Printf("Sync error: XUIClient is nil")
		return
	}

	if bot.XUIClient.Cache != nil {
		bot.XUIClient.Cache.RefreshSync()
	}
	cachedInbounds := bot.XUIClient.GetCachedInbounds()
	known := make(map[int]bool)
	for _, inbound := range cachedInbounds {
		known[inbound.ID] = true
	}

	validAdded := make([]int, 0, len(addedIDs))
	for _, id := range addedIDs {
		if known[id] {
			validAdded = append(validAdded, id)
		}
	}

	if len(validAdded) > 0 {
		req := xui.BulkAttachRequest{
			Emails:     emails,
			InboundIDs: validAdded,
		}
		if err := bot.XUIClient.BulkAttach(req); err != nil {
			log.Printf("Sync error: BulkAttach failed: %v", err)
		} else {
			log.Printf("Sync success: BulkAttach of %d client(s) to inbound(s) %v completed", len(emails), validAdded)
		}
	}

	validRemoved := make([]int, 0, len(removedIDs))
	for _, id := range removedIDs {
		if known[id] {
			validRemoved = append(validRemoved, id)
		}
	}

	if len(validRemoved) > 0 {
		req := xui.BulkAttachRequest{
			Emails:     emails,
			InboundIDs: validRemoved,
		}
		if err := bot.XUIClient.BulkDetach(req); err != nil {
			log.Printf("Sync error: BulkDetach failed: %v", err)
		} else {
			log.Printf("Sync success: BulkDetach of %d client(s) from inbound(s) %v completed", len(emails), validRemoved)
		}
	}
}

func HandleAdminDraftInboundsMenu(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	planType := callbackPayload(c)
	draft := state.Data

	return showAdminDraftInboundsMenu(c, planType, draft)
}

func showAdminDraftInboundsMenu(c telebot.Context, planType string, draft map[string]interface{}) error {
	if bot.XUIClient == nil {
		return c.Send("ارتباط با پنل سرویس‌دهنده برقرار نیست.")
	}

	cached := bot.XUIClient.GetCachedInbounds()
	selectedIDs := draftGetIntSlice(draft, "inbound_ids")

	selectedMap := make(map[int]bool)
	for _, id := range selectedIDs {
		selectedMap[id] = true
	}

	var text strings.Builder
	text.WriteString("📡 **انتخاب اینباندهای طرح**\n\n")
	text.WriteString("اینباندهایی که می‌خواهید به این طرح متصل شوند را انتخاب کنید. می‌توانید از دکمه 'انتخاب همه' استفاده کرده و سپس موارد دلخواه را تغییر دهید.\n\n")
	text.WriteString("اینباندهای انتخاب‌شده فعلی:\n")

	inboundNames := cachedInboundNames()
	if len(selectedIDs) == 0 {
		text.WriteString("  _(هیچ اینباندی انتخاب نشده)_\n")
	} else {
		for _, id := range selectedIDs {
			if name, ok := inboundNames[id]; ok && name != "" {
				text.WriteString(fmt.Sprintf("  ✅ شناسه %d: %s\n", id, name))
			} else {
				text.WriteString(fmt.Sprintf("  ✅ شناسه %d\n", id))
			}
		}
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	for _, inbound := range cached {
		isSelected := selectedMap[inbound.ID]
		mark := "❌"
		if isSelected {
			mark = "✅"
		}
		btnText := fmt.Sprintf("%s شناسه %d: %s (%s:%d)", mark, inbound.ID, inbound.Remark, inbound.Protocol, inbound.Port)
		rows = append(rows, menu.Row(menu.Data(btnText, "admin_draft_toggle_inbound", fmt.Sprintf("%s:%d", planType, inbound.ID))))
	}

	allSelected := true
	for _, inbound := range cached {
		if !selectedMap[inbound.ID] {
			allSelected = false
			break
		}
	}

	var actionRow telebot.Row
	if allSelected && len(cached) > 0 {
		actionRow = menu.Row(menu.Data("❌ لغو انتخاب همه", "admin_draft_toggle_all", planType+":deselect"))
	} else {
		actionRow = menu.Row(menu.Data("✅ انتخاب همه", "admin_draft_toggle_all", planType+":select"))
	}
	rows = append(rows, actionRow)
	rows = append(rows, menu.Row(menu.Data("💾 تایید و بازگشت", "admin_draft_inbounds_done", planType)))

	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminDraftToggleInbound(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("پارامتر نامعتبر است.")
	}
	planType := parts[0]
	inboundID, err := strconv.Atoi(parts[1])
	if err != nil {
		return c.Send("شناسه اینباند نامعتبر است.")
	}

	draft := state.Data
	inboundIDs := draftGetIntSlice(draft, "inbound_ids")

	found := false
	newIDs := make([]int, 0, len(inboundIDs))
	for _, id := range inboundIDs {
		if id == inboundID {
			found = true
		} else {
			newIDs = append(newIDs, id)
		}
	}
	if !found {
		newIDs = append(newIDs, inboundID)
	}
	draft["inbound_ids"] = newIDs

	bot.FSM.SetState(user.TelegramID, state.Step, draft)
	_ = c.Respond(&telebot.CallbackResponse{Text: "وضعیت اینباند تغییر یافت."})
	return showAdminDraftInboundsMenu(c, planType, draft)
}

func HandleAdminDraftToggleAll(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	payload := callbackPayload(c)
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return c.Send("پارامتر نامعتبر است.")
	}
	planType := parts[0]
	action := parts[1]

	draft := state.Data

	if action == "select" {
		if bot.XUIClient != nil {
			cached := bot.XUIClient.GetCachedInbounds()
			newIDs := make([]int, 0, len(cached))
			for _, inbound := range cached {
				newIDs = append(newIDs, inbound.ID)
			}
			draft["inbound_ids"] = newIDs
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: "تمامی اینباندها انتخاب شدند."})
	} else {
		draft["inbound_ids"] = []int{}
		_ = c.Respond(&telebot.CallbackResponse{Text: "انتخاب تمامی اینباندها لغو شد."})
	}

	bot.FSM.SetState(user.TelegramID, state.Step, draft)
	return showAdminDraftInboundsMenu(c, planType, draft)
}

func HandleAdminDraftInboundsDone(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("دسترسی غیرمجاز است.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند ویرایش فعالی یافت نشد.")
	}
	if state.Step == "awaiting_admin_draft_input" {
		return c.Send("لطفاً ابتدا ورودی متنی فعلی را تکمیل کنید یا با /cancel انصراف دهید.")
	}

	planType := callbackPayload(c)
	draft := state.Data

	_ = c.Respond(&telebot.CallbackResponse{Text: "اینباندها ذخیره شدند."})

	if planType == "test" {
		return showAdminDraftTestPlanMenu(c, draft)
	}
	return showAdminDraftPaidPlanMenu(c, draft)
}

func HandleAdminViewPlan(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("طرح نامعتبر است.")
	}
	return showAdminViewPlan(c, planType, planID)
}

func showAdminViewPlan(c telebot.Context, planType string, planID int64) error {
	inboundNames := cachedInboundNames()
	inboundLabel := func(ids []int) string {
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			if name, ok := inboundNames[id]; ok && name != "" {
				parts = append(parts, fmt.Sprintf("%d (%s)", id, name))
			} else {
				parts = append(parts, fmt.Sprintf("%d", id))
			}
		}
		return strings.Join(parts, ", ")
	}

	var text string
	var enabled bool
	if planType == db.PlanTypeTest {
		plan, err := db.GetTestPlanByID(context.Background(), planID)
		if err != nil || plan == nil {
			return c.Send("طرح تست یافت نشد.")
		}
		enabled = plan.Enabled
		access, _ := db.GetPlanUserAccess(context.Background(), planType, planID)
		ipLimitLabel := fmt.Sprintf("%d کاربر همزمان", plan.IPLimit)
		if plan.IPLimit == 0 {
			ipLimitLabel = "نامحدود"
		}
		text = fmt.Sprintf("🧪 **طرح تست شماره %d**\n"+
			"نام: %s\nتوضیحات: %s\nنکات کاربری: %s\nوضعیت فعال: %t\nدسترسی عمومی: %t\n"+
			"اینباندها: %s\nمدت اعتبار: %s\nسقف حجم: %.2f گیگابایت\n"+
			"فلو: %s\nسقف روزانه: %d\nمحدودیت کاربران همزمان: %s\nکاربران اختصاصی: %s",
			plan.ID, plan.Name, plan.Description, plan.UsageDescription, plan.Enabled, plan.IsGlobal,
			inboundLabel(plan.InboundIDs), humanDuration(plan.ExpireSeconds),
			float64(plan.MaxDataBytes)/1073741824, nonEmpty(plan.Flow, "پیش‌فرض/خالی"), plan.MaxPerDay, ipLimitLabel, formatAccessLabel(plan.IsGlobal, access))
	} else {
		plan, err := db.GetPaidPlanByID(context.Background(), planID)
		if err != nil || plan == nil {
			return c.Send("طرح خرید یافت نشد.")
		}
		enabled = plan.Enabled
		access, _ := db.GetPlanUserAccess(context.Background(), planType, planID)

		var priceBlock string
		if plan.IsLimited {
			priceBlock = fmt.Sprintf("نوع طرح: حجمی (محدود)\nقیمت هر گیگابایت: %s تومان\nحداقل حجم: %d گیگابایت\nقیمت ماه اضافی: %s تومان",
				persian.FormatMoney(plan.PricePerGBToman), plan.MinDataGB, persian.FormatMoney(plan.PricePerExtraMonthToman))
		} else {
			priceBlock = fmt.Sprintf("نوع طرح: نامحدود\nقیمت پایه: %s تومان", persian.FormatMoney(plan.BasePriceToman))
		}

		ipLabel := fmt.Sprintf("%d-%d کاربر همزمان", plan.BaseIPLimit, plan.MaxIPLimit)
		if plan.BaseIPLimit == 0 && plan.MaxIPLimit == 0 {
			ipLabel = "کاربر همزمان نامحدود"
		}

		text = fmt.Sprintf("💼 **طرح خرید شماره %d**\n"+
			"نام: %s\nتوضیحات: %s\nنکات کاربری: %s\nوضعیت فعال: %t\nدسترسی عمومی: %t\n"+
			"اینباندها: %s\n%s\nمحدودیت کاربران: %s\n"+
			"قیمت کاربر اضافی: %s تومان\nفلو: %s\nتخفیف‌ها: %s\nکاربران اختصاصی: %s",
			plan.ID, plan.Name, plan.Description, plan.UsageDescription, plan.Enabled, plan.IsGlobal,
			inboundLabel(plan.InboundIDs), priceBlock, ipLabel,
			persian.FormatMoney(plan.PricePerExtraIPToman), nonEmpty(plan.Flow, "پیش‌فرض/خالی"), formatDiscountLabel(plan.DiscountTiers), formatAccessLabel(plan.IsGlobal, access))
	}

	toggleText := "🔴 غیرفعال‌سازی"
	if !enabled {
		toggleText = "✅ فعال‌سازی"
	}
	ref := fmt.Sprintf("%s:%d", planType, planID)
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📝 ویرایش طرح", "admin_plan_edit", ref)),
		menu.Row(menu.Data(toggleText, "admin_plan_toggle", ref), menu.Data("👥 تنظیم دسترسی اختصاصی", "admin_plan_access", ref)),
		menu.Row(menu.Data("🗑 حذف طرح", "admin_plan_delete", ref)),
		menu.Row(menu.Data("« بازگشت", "admin_plans")),
	)
	return maybeEditOrSend(c, text, menu)
}

func HandleAdminPlanToggle(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("طرح نامعتبر است.")
	}
	enabled := false
	if planType == db.PlanTypeTest {
		plan, _ := db.GetTestPlanByID(context.Background(), planID)
		if plan == nil {
			return c.Send("طرح یافت نشد.")
		}
		enabled = !plan.Enabled
	} else {
		plan, _ := db.GetPaidPlanByID(context.Background(), planID)
		if plan == nil {
			return c.Send("طرح یافت نشد.")
		}
		enabled = !plan.Enabled
	}
	if err := db.SetPlanEnabled(context.Background(), planType, planID, enabled); err != nil {
		return c.Send("خطا در تغییر وضعیت طرح.")
	}
	status := "غیرفعال"
	if enabled {
		status = "فعال"
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ طرح %s شد.", status)})
	return showAdminViewPlan(c, planType, planID)
}

func HandleAdminPlanDelete(c telebot.Context) error {
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok {
		return c.Send("طرح نامعتبر است.")
	}
	if err := db.DeletePlan(context.Background(), planType, planID); err != nil {
		log.Printf("[ERROR] Failed to delete plan %s %d: %v", planType, planID, err)
		return c.Send("خطا در حذف طرح.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: "🗑 طرح حذف شد."})
	return HandleAdminPlans(c)
}

func HandleAdminPlanAccessPrompt(c telebot.Context) error {
	user := userFromContext(c)
	planType, planID, ok := parsePlanRef(callbackPayload(c))
	if !ok || user == nil {
		return c.Send("طرح نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_admin_plan_access", map[string]interface{}{
		"plan_type": planType,
		"plan_id":   fmt.Sprintf("%d", planID),
	})
	return maybeEditOrSend(c, "شناسه‌های عددی تلگرام کاربران مجاز را با کاما جدا کنید تا دسترسی اختصاصی تنظیم شود.\nبرای دسترسی عمومی و همگانی، عبارت '-' را بفرستید.")
}

func ProcessAdminPlanAccess(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil || !isConfiguredAdmin(user.TelegramID) {
		return c.Send("شما دسترسی به این دستور را ندارید.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("فرآیند تنظیم دسترسی فعالی یافت نشد.")
	}
	planType := fmt.Sprintf("%v", state.Data["plan_type"])
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	userIDs, err := parseInternalUserIDs(text)
	if err != nil {
		return c.Send("شناسه‌های کاربری نامعتبر است. از شناسه‌های عددی جداشده با کاما استفاده کنید.")
	}
	if err := db.SetPlanUserAccess(context.Background(), planType, planID, userIDs); err != nil {
		return c.Send("خطا در ذخیره دسترسی اختصاصی.")
	}
	if err := db.SetPlanGlobal(context.Background(), planType, planID, len(userIDs) == 0); err != nil {
		return c.Send("دسترسی ذخیره شد، اما به‌روزرسانی وضعیت عمومی طرح ناموفق بود.")
	}
	bot.FSM.ClearState(user.TelegramID)
	if len(userIDs) == 0 {
		_ = c.Send("✅ طرح اکنون به صورت عمومی برای تمامی کاربران فعال است.")
	} else {
		_ = c.Send(fmt.Sprintf("✅ دسترسی اختصاصی طرح برای %d کاربر با موفقیت تنظیم شد.", len(userIDs)))
	}
	return showAdminViewPlan(c, planType, planID)
}

func notifyApprovedUsers(text string) {
	go func() {
		if bot.Bot == nil {
			return
		}
		users, err := db.ListApprovedUsers(context.Background())
		if err != nil {
			return
		}
		for _, user := range users {
			_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, text)
			time.Sleep(35 * time.Millisecond)
		}
	}()
}

// ─── Draft Getters ────────────────────────────────────────────────────────────

func draftGetString(m map[string]interface{}, key string) string {
	if val, ok := m[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

func draftGetInt(m map[string]interface{}, key string) int {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}

func draftGetInt64(m map[string]interface{}, key string) int64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case int64:
			return v
		case int:
			return int64(v)
		case float64:
			return int64(v)
		}
	}
	return 0
}

func draftGetFloat64(m map[string]interface{}, key string) float64 {
	if val, ok := m[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return 0.0
}

func draftGetBool(m map[string]interface{}, key string) bool {
	if val, ok := m[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}

func draftGetIntSlice(m map[string]interface{}, key string) []int {
	if val, ok := m[key]; ok {
		if slice, ok := val.([]int); ok {
			return slice
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]int, 0, len(slice))
			for _, item := range slice {
				switch v := item.(type) {
				case float64:
					out = append(out, int(v))
				case int:
					out = append(out, v)
				case int64:
					out = append(out, int(v))
				}
			}
			return out
		}
	}
	return []int{}
}

func draftGetInt64Slice(m map[string]interface{}, key string) []int64 {
	if val, ok := m[key]; ok {
		if slice, ok := val.([]int64); ok {
			return slice
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]int64, 0, len(slice))
			for _, item := range slice {
				switch v := item.(type) {
				case float64:
					out = append(out, int64(v))
				case int64:
					out = append(out, v)
				case int:
					out = append(out, int64(v))
				}
			}
			return out
		}
	}
	return []int64{}
}

func draftGetDiscounts(m map[string]interface{}, key string) []db.DiscountTier {
	if val, ok := m[key]; ok {
		if tiers, ok := val.([]db.DiscountTier); ok {
			return tiers
		}
		if slice, ok := val.([]interface{}); ok {
			out := make([]db.DiscountTier, 0, len(slice))
			for _, item := range slice {
				if d, ok := item.(db.DiscountTier); ok {
					out = append(out, d)
				} else if dMap, ok := item.(map[string]interface{}); ok {
					months := 0
					basisPoints := int64(0)
					if mVal, ok := dMap["months"]; ok {
						if mInt, ok := mVal.(float64); ok {
							months = int(mInt)
						} else if mInt, ok := mVal.(int); ok {
							months = mInt
						}
					}
					if pVal, ok := dMap["basis_points"]; ok {
						basisPoints, _ = coerceAnyInt64(pVal)
					}
					if months > 0 {
						out = append(out, db.DiscountTier{Months: months, BasisPoints: basisPoints})
					}
				}
			}
			return out
		}
	}
	return []db.DiscountTier{}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func splitSpec(text string) []string {
	raw := strings.Split(text, "|")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		parts = append(parts, strings.TrimSpace(part))
	}
	return parts
}

func parseInboundIDs(text string) ([]int, error) {
	chunks := strings.Split(text, ",")
	ids := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		id, err := strconv.Atoi(strings.TrimSpace(chunk))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid inbound id")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseInternalUserIDs(text string) ([]int64, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil, nil
	}
	chunks := strings.Split(text, ",")
	ids := make([]int64, 0, len(chunks))
	for _, chunk := range chunks {
		id, err := parseInt64(strings.TrimSpace(chunk))
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid user id")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parsePlanRef(payload string) (string, int64, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	planType := strings.TrimSpace(parts[0])
	if planType != db.PlanTypeTest && planType != db.PlanTypePaid {
		return "", 0, false
	}
	planID, err := parseInt64(parts[1])
	if err != nil || planID <= 0 {
		return "", 0, false
	}
	return planType, planID, true
}

func parseDiscounts(text string) ([]db.DiscountTier, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" {
		return nil, nil
	}
	chunks := strings.Split(text, ",")
	tiers := make([]db.DiscountTier, 0, len(chunks))
	for _, chunk := range chunks {
		pair := strings.Split(strings.TrimSpace(chunk), ":")
		if len(pair) != 2 {
			return nil, fmt.Errorf("invalid discount")
		}
		months, err := strconv.Atoi(strings.TrimSpace(pair[0]))
		if err != nil || months <= 0 {
			return nil, err
		}
		bp, err := parsePercentBasisPoints(strings.TrimSpace(pair[1]))
		if err != nil || bp < 0 || bp > 10000 {
			return nil, fmt.Errorf("invalid discount basis points")
		}
		tiers = append(tiers, db.DiscountTier{Months: months, BasisPoints: bp})
	}
	return tiers, nil
}

func parsePercentBasisPoints(value string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("invalid percent")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > 100 {
		return 0, fmt.Errorf("invalid percent")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if parts[1] == "" || len(parts[1]) > 2 {
			return 0, fmt.Errorf("percent precision must be at most two decimals")
		}
		fraction, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid percent")
		}
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	if whole == 100 && fraction != 0 {
		return 0, fmt.Errorf("percent exceeds 100")
	}
	return whole*100 + fraction, nil
}

func cachedInboundNames() map[int]string {
	names := make(map[int]string)
	if bot.XUIClient != nil {
		for _, inbound := range bot.XUIClient.GetCachedInbounds() {
			names[inbound.ID] = inbound.Remark
		}
	}
	return names
}

func humanDuration(seconds int64) string {
	if seconds <= 0 {
		return "نامحدود"
	}
	hours := seconds / 3600
	if hours < 24 {
		return fmt.Sprintf("%d ساعت", hours)
	}
	days := hours / 24
	if days < 7 {
		return fmt.Sprintf("%d روز", days)
	}
	weeks := days / 7
	if weeks < 5 {
		return fmt.Sprintf("%d هفته", weeks)
	}
	months := days / 30
	return fmt.Sprintf("%d ماه", months)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func formatInboundLabel(ids []int) string {
	if len(ids) == 0 {
		return "(هیچ موردی انتخاب نشده)"
	}
	inboundNames := cachedInboundNames()
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := inboundNames[id]; ok && name != "" {
			parts = append(parts, fmt.Sprintf("%d (%s)", id, name))
		} else {
			parts = append(parts, fmt.Sprintf("%d", id))
		}
	}
	return strings.Join(parts, ", ")
}

func formatDiscountLabel(tiers []db.DiscountTier) string {
	if len(tiers) == 0 {
		return "ندارد"
	}
	parts := make([]string, 0, len(tiers))
	for _, t := range tiers {
		parts = append(parts, fmt.Sprintf("%d ماهه: %s%%", t.Months, formatBasisPointPercent(t.GetBasisPoints())))
	}
	return strings.Join(parts, ", ")
}

func formatAccessLabel(isGlobal bool, allowedUserIDs []int64) string {
	if isGlobal {
		return "عمومی (همه کاربران)"
	}
	if len(allowedUserIDs) == 0 {
		return "اختصاصی (بدون کاربر مجاز)"
	}
	parts := make([]string, 0, len(allowedUserIDs))
	for _, id := range allowedUserIDs {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return fmt.Sprintf("اختصاصی (کاربران: %s)", strings.Join(parts, ", "))
}

func toggleEmoji(val bool) string {
	if val {
		return "✅"
	}
	return "❌"
}
