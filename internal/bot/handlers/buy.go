package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/services/pricing"
	"xui-reseller-bot/internal/services/reconcile"
)

func RegisterBuySub(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_buy_sub", HandleBuySubFlow, auth)
	b.Handle("\fselect_buy_plan", HandleSelectBuyPlan, auth)
	b.Handle("\fbuy_months", HandleBuyMonths, auth)
	b.Handle("\fbuy_custom_dur", HandleBuyCustomDuration, auth)
	b.Handle("\fbuy_ip_run", HandleBuyIPRun, auth)
	b.Handle("\fbuy_confirm", HandleBuyConfirm, auth)
	b.Handle("\fbuy_direct", HandleBuyDirectPayment, auth)
	b.Handle("\fbuy_cancel", HandleBuyCancel, auth)
	b.Handle("\fbuy_data_gb", HandleBuyDataGB, auth)
	b.Handle("\fbuy_data_gb_custom", HandleBuyDataGBCustom, auth)
	b.Handle("\fbuy_ip_run_limited", HandleBuyIPRunLimited, auth)
	b.Handle("\fbuy_auto_name", HandleBuyAutoName, auth)
}

func HandleBuySubFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	if bot.XUIClient == nil {
		return maybeEditOrSend(c, "⚠️ ارتباط با سرور سرویس‌دهنده موقتاً قطع است. لطفاً دقایقی دیگر مجدداً تلاش فرمایید.")
	}

	plans, err := db.GetPaidPlansForUser(context.Background(), user.ID, false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌های خرید.")
	}
	if len(plans) == 0 {
		return maybeEditOrSend(c, "در حال حاضر هیچ طرح خریدی موجود نیست.")
	}

	var text strings.Builder
	text.WriteString("💼 **طرح‌های خرید سرویس**:\n\n")
	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(plans)+1)
	for _, plan := range plans {
		if plan.IsLimited {
			text.WriteString(fmt.Sprintf("📦 **%s** (محدود)\nقیمت هر گیگابایت: %s تومان\nحداقل ترافیک: %d گیگابایت\nماهانه اضافه: +%s تومان\nدستگاه همزمان: %d تا سقف %d\nهزینه کاربر اضافه: +%s تومان/کاربر/ماه\n",
				plan.Name, persian.FormatMoney(plan.PricePerGBToman), plan.MinDataGB, persian.FormatMoney(plan.PricePerExtraMonthToman), plan.BaseIPLimit, plan.MaxIPLimit, persian.FormatMoney(plan.PricePerExtraIPToman)))
		} else {
			text.WriteString(fmt.Sprintf("📦 **%s** (نامحدود)\nقیمت پایه: %s تومان/ماهانه\nدستگاه همزمان: %d تا سقف %d\nهزینه کاربر اضافه: +%s تومان/کاربر/ماه\n",
				plan.Name, persian.FormatMoney(plan.BasePriceToman), plan.BaseIPLimit, plan.MaxIPLimit, persian.FormatMoney(plan.PricePerExtraIPToman)))
		}
		if plan.Description != "" {
			text.WriteString(fmt.Sprintf("%s\n", plan.Description))
		}
		text.WriteString("\n")
		rows = append(rows, menu.Row(menu.Data("📦 "+plan.Name, "select_buy_plan", fmt.Sprintf("%d", plan.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleSelectBuyPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	planID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("طرح نامعتبر است.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد.")
	}

	discountText := ""
	if len(plan.DiscountTiers) > 0 {
		discountText = "\n💰 تخفیف‌های خرید طولانی مدت:"
		for _, tier := range plan.DiscountTiers {
			discountText += fmt.Sprintf("\n  خرید %d ماهه و بیشتر: %s%% تخفیف", tier.Months, formatBasisPointPercent(tier.GetBasisPoints()))
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("۱ ماهه", "buy_months", fmt.Sprintf("1:%d", plan.ID)), menu.Data("۳ ماهه", "buy_months", fmt.Sprintf("3:%d", plan.ID))),
		menu.Row(menu.Data("۶ ماهه", "buy_months", fmt.Sprintf("6:%d", plan.ID)), menu.Data("✏️ مدت دلخواه", "buy_custom_dur", fmt.Sprintf("%d", plan.ID))),
		menu.Row(menu.Data("« بازگشت", "menu_buy_sub")),
	)

	priceLabel := fmt.Sprintf("قیمت پایه: %s تومان/ماهانه", persian.FormatMoney(plan.BasePriceToman))
	if plan.IsLimited {
		priceLabel = fmt.Sprintf("قیمت هر گیگابایت: %s تومان\nحداقل ترافیک: %d گیگابایت\nهزینه تمدید ماهانه اضافه: +%s تومان", persian.FormatMoney(plan.PricePerGBToman), plan.MinDataGB, persian.FormatMoney(plan.PricePerExtraMonthToman))
	}

	descText := ""
	if plan.Description != "" {
		descText = fmt.Sprintf("\n\n%s", plan.Description)
	}

	text := fmt.Sprintf("📦 **%s**\n%s%s%s\n\nمدت زمان سرویس را انتخاب کنید:", plan.Name, priceLabel, discountText, descText)
	return maybeEditOrSend(c, text, menu)
}

func HandleBuyCustomDuration(c telebot.Context) error {
	user := userFromContext(c)
	planID, err := parseInt64(callbackPayload(c))
	if user == nil || err != nil {
		return c.Send("طرح نامعتبر است.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_buy_months_text", map[string]interface{}{"plan_id": fmt.Sprintf("%d", planID)})
	return maybeEditOrSend(c, "لطفا تعداد ماه‌های مورد نظر خود را ارسال کنید (به عنوان مثال: 2).")
}

func handlePostDuration(c telebot.Context, plan *db.PaidPlan, months int) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	if plan.IsLimited {
		bot.FSM.SetState(user.TelegramID, "awaiting_buy_data_gb", map[string]interface{}{
			"plan_id": fmt.Sprintf("%d", plan.ID),
			"months":  fmt.Sprintf("%d", months),
		})
		return promptDataLimit(c, plan, months)
	}

	if plan.BaseIPLimit == plan.MaxIPLimit {
		price := calculatePaidPrice(plan, months, plan.BaseIPLimit, 0)
		bot.FSM.SetState(user.TelegramID, "awaiting_buy_custom_name", map[string]interface{}{
			"plan_id":  fmt.Sprintf("%d", plan.ID),
			"months":   fmt.Sprintf("%d", months),
			"ip_limit": fmt.Sprintf("%d", plan.BaseIPLimit),
			"price":    fmt.Sprintf("%d", price),
			"data_gb":  "0",
		})
		menu := &telebot.ReplyMarkup{}
		menu.Inline(
			menu.Row(menu.Data("🎲 انتخاب توسط ربات", "buy_auto_name")),
		)
		var ipLimitLabel string
		if plan.BaseIPLimit == 0 {
			ipLimitLabel = "کاربر همزمان نامحدود"
		} else {
			ipLimitLabel = fmt.Sprintf("%d کاربر همزمان", plan.BaseIPLimit)
		}
		return maybeEditOrSend(c, fmt.Sprintf(
			"📦 **%s**\n%d ماهه، %s\nقیمت: %s تومان\n\nلطفا نام دلخواه برای اشتراک خود را ارسال کنید (فقط حروف و عدد انگلیسی):\n(یک پسوند تصادفی ۶ کاراکتری به انتهای نام انتخابی شما اضافه خواهد شد)",
			plan.Name, months, ipLimitLabel, persian.FormatMoney(price)), menu)
	}

	return showIPChoices(c, plan.ID, months)
}

func ProcessBuyMonthsText(c telebot.Context, text string) error {
	user := userFromContext(c)
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("هیچ طرح خریدی انتخاب نشده است.")
	}
	months, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت (تعداد ماه) باشد.")
	}
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	bot.FSM.ClearState(user.TelegramID)
	return handlePostDuration(c, plan, months)
}

func HandleBuyMonths(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("مدت زمان نامعتبر است.")
	}
	months, err := strconv.Atoi(parts[0])
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت (تعداد ماه) باشد.")
	}
	planID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("طرح نامعتبر است.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	return handlePostDuration(c, plan, months)
}

func promptDataLimit(c telebot.Context, plan *db.PaidPlan, months int) error {
	minGB := plan.MinDataGB
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB), "buy_data_gb", fmt.Sprintf("%d:%d:%d", minGB, plan.ID, months)),
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+10), "buy_data_gb", fmt.Sprintf("%d:%d:%d", minGB+10, plan.ID, months)),
		),
		menu.Row(
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+30), "buy_data_gb", fmt.Sprintf("%d:%d:%d", minGB+30, plan.ID, months)),
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+50), "buy_data_gb", fmt.Sprintf("%d:%d:%d", minGB+50, plan.ID, months)),
		),
		menu.Row(
			menu.Data("✏️ حجم دلخواه", "buy_data_gb_custom", fmt.Sprintf("%d:%d", plan.ID, months)),
		),
		menu.Row(
			menu.Data("« بازگشت", "select_buy_plan", fmt.Sprintf("%d", plan.ID)),
		),
	)
	text := fmt.Sprintf("📦 **%s** — %d ماهه\nلطفا ترافیک مورد نظر خود را انتخاب کنید یا مقدار دلخواه را بنویسید (حداقل: %d گیگابایت):", plan.Name, months, minGB)
	return maybeEditOrSend(c, text, menu)
}

func HandleBuyDataGBCustom(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
	}
	planID := parts[0]
	months := parts[1]

	bot.FSM.SetState(user.TelegramID, "awaiting_buy_data_gb_text", map[string]interface{}{
		"plan_id": planID,
		"months":  months,
	})
	return maybeEditOrSend(c, "لطفا حجم ترافیک مورد نظر خود را به گیگابایت به صورت عدد ارسال کنید (مثلا: 25).")
}

func ProcessBuyDataGBText(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_buy_data_gb_text" {
		return c.Send("هیچ فرآیند خرید فعالی یافت نشد.")
	}
	gb, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || gb <= 0 {
		return c.Send("مقدار ترافیک باید یک عدد مثبت به گیگابایت باشد.")
	}

	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}
	if int64(gb) < plan.MinDataGB {
		return c.Send(fmt.Sprintf("حداقل حجم ترافیک مجاز برای این طرح %d گیگابایت است. لطفا مجددا عدد دیگری ارسال کنید:", plan.MinDataGB))
	}

	bot.FSM.ClearState(user.TelegramID)
	return showIPChoicesLimited(c, plan, months, gb)
}

func HandleBuyDataGB(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 {
		return c.Send("درخواست نامعتبر است.")
	}
	gb, _ := strconv.Atoi(parts[0])
	planID, _ := parseInt64(parts[1])
	months, _ := strconv.Atoi(parts[2])

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}
	return showIPChoicesLimited(c, plan, months, gb)
}

func showIPChoicesLimited(c telebot.Context, plan *db.PaidPlan, months int, gb int) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := plan.BaseIPLimit; ip <= plan.MaxIPLimit; ip++ {
		price := calculatePaidPrice(plan, months, ip, gb)
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("%d کاربر همزمان — %s تومان", ip, persian.FormatMoney(price)), "buy_ip_run_limited", fmt.Sprintf("%d:%d:%d:%d", ip, plan.ID, months, gb))))
		if len(rows) >= 10 {
			break
		}
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "buy_months", fmt.Sprintf("%d:%d", months, plan.ID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("📦 **%s** — %d گیگابایت، %d ماهه\nتعداد کاربر همزمان (محدودیت دستگاه) را انتخاب کنید:", plan.Name, gb, months), menu)
}

func HandleBuyIPRunLimited(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 4 {
		return c.Send("درخواست خرید نامعتبر است.")
	}
	ipLimit, _ := strconv.Atoi(parts[0])
	planID, _ := parseInt64(parts[1])
	months, _ := strconv.Atoi(parts[2])
	gb, _ := strconv.Atoi(parts[3])

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد.")
	}

	price := calculatePaidPrice(plan, months, ipLimit, gb)
	bot.FSM.SetState(user.TelegramID, "awaiting_buy_custom_name", map[string]interface{}{
		"plan_id":  fmt.Sprintf("%d", plan.ID),
		"months":   fmt.Sprintf("%d", months),
		"ip_limit": fmt.Sprintf("%d", ipLimit),
		"price":    fmt.Sprintf("%d", price),
		"data_gb":  fmt.Sprintf("%d", gb),
	})

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🎲 انتخاب توسط ربات", "buy_auto_name")),
	)
	var ipLimitLabel string
	if ipLimit == 0 {
		ipLimitLabel = "کاربر همزمان نامحدود"
	} else {
		ipLimitLabel = fmt.Sprintf("%d کاربر همزمان", ipLimit)
	}
	return maybeEditOrSend(c, fmt.Sprintf(
		"📦 **%s**\n%d گیگابایت، %d ماهه، %s\nقیمت: %s تومان\n\nلطفا نام دلخواه برای اشتراک خود را ارسال کنید (فقط حروف و عدد انگلیسی):\n(یک پسوند تصادفی ۶ کاراکتری به انتهای نام انتخابی شما اضافه خواهد شد)",
		plan.Name, gb, months, ipLimitLabel, persian.FormatMoney(price)), menu)
}

func showIPChoices(c telebot.Context, planID int64, months int) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	if plan.MaxIPLimit < plan.BaseIPLimit {
		plan.MaxIPLimit = plan.BaseIPLimit
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := plan.BaseIPLimit; ip <= plan.MaxIPLimit; ip++ {
		price := calculatePaidPrice(plan, months, ip, 0)
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("%d کاربر همزمان — %s تومان", ip, persian.FormatMoney(price)), "buy_ip_run", fmt.Sprintf("%d:%d:%d", ip, plan.ID, months))))
		if len(rows) >= 10 {
			break
		}
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "select_buy_plan", fmt.Sprintf("%d", planID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("📦 **%s** — %d ماهه\nتعداد کاربر همزمان (محدودیت دستگاه) را انتخاب کنید:", plan.Name, months), menu)
}

func HandleBuyIPRun(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 {
		return c.Send("درخواست خرید نامعتبر است.")
	}
	ipLimit, err := strconv.Atoi(parts[0])
	if err != nil {
		return c.Send("محدودیت کاربر نامعتبر است.")
	}
	planID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("طرح نامعتبر است.")
	}
	months, err := strconv.Atoi(parts[2])
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید مثبت باشد.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	if ipLimit < plan.BaseIPLimit {
		return c.Send(fmt.Sprintf("حداقل کاربر همزمان برای این طرح %d است.", plan.BaseIPLimit))
	}
	if ipLimit > plan.MaxIPLimit {
		return c.Send(fmt.Sprintf("حداکثر کاربر همزمان برای این طرح %d است.", plan.MaxIPLimit))
	}

	price := calculatePaidPrice(plan, months, ipLimit, 0)
	bot.FSM.SetState(user.TelegramID, "awaiting_buy_custom_name", map[string]interface{}{
		"plan_id":  fmt.Sprintf("%d", plan.ID),
		"months":   fmt.Sprintf("%d", months),
		"ip_limit": fmt.Sprintf("%d", ipLimit),
		"price":    fmt.Sprintf("%d", price),
		"data_gb":  "0",
	})

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🎲 انتخاب توسط ربات", "buy_auto_name")),
	)
	var ipLimitLabel string
	if ipLimit == 0 {
		ipLimitLabel = "کاربر همزمان نامحدود"
	} else {
		ipLimitLabel = fmt.Sprintf("%d کاربر همزمان", ipLimit)
	}
	return maybeEditOrSend(c, fmt.Sprintf(
		"📦 **%s**\n%d ماهه، %s\nقیمت: %s تومان\n\nلطفا نام دلخواه برای اشتراک خود را ارسال کنید (فقط حروف و عدد انگلیسی):\n(یک پسوند تصادفی ۶ کاراکتری به انتهای نام انتخابی شما اضافه خواهد شد)",
		plan.Name, months, ipLimitLabel, persian.FormatMoney(price)), menu)
}

func ProcessBuyCustomName(c telebot.Context, customName string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("هیچ فرآیند خرید فعالی یافت نشد.")
	}

	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	dataGBVal := state.Data["data_gb"]
	var dataGB int
	if dataGBVal != nil {
		dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", dataGBVal))
	}

	if months <= 0 {
		return c.Send("مدت زمان باید مثبت باشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد.")
	}

	name := sanitizeName(customName)
	if name == "" {
		return c.Send("نام نامعتبر است. از حروف، اعداد یا خط تیره انگلیسی استفاده کنید.")
	}
	var email string
	for {
		email = fmt.Sprintf("%s_%s", name, randomToken(6))
		if existing, _ := db.GetSubscriptionByEmail(context.Background(), email); existing == nil {
			break
		}
	}

	operationToken := makeSubID()
	opKey := fmt.Sprintf("wallet_purchase:%s", operationToken)
	quote := pricing.CalculateQuote(pricing.QuoteParams{
		UserID:       user.ID,
		Plan:         plan,
		Months:       months,
		IPLimit:      ipLimit,
		DataGB:       dataGB,
		OperationKey: opKey,
	})
	if err := pricing.SaveQuote(context.Background(), quote); err != nil {
		log.Printf("[ERROR] failed to persist quote for user %d: %v", user.ID, err)
		return c.Send("خطا در ایجاد پیش‌فاکتور خرید. لطفا مجددا تلاش کنید.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_buy_confirm", map[string]interface{}{
		"plan_id":         fmt.Sprintf("%d", plan.ID),
		"quote_id":        fmt.Sprintf("%d", quote.ID),
		"quote_key":       quote.QuoteKey,
		"months":          fmt.Sprintf("%d", months),
		"ip_limit":        fmt.Sprintf("%d", ipLimit),
		"price":           fmt.Sprintf("%d", quote.FinalPriceToman),
		"price_toman":     fmt.Sprintf("%d", quote.FinalPriceToman),
		"custom_name":     name,
		"email":           email,
		"data_gb":         fmt.Sprintf("%d", dataGB),
		"type":            "buy",
		"operation_token": operationToken,
		"operation_key":   opKey,
	})

	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "buy_confirm", operationToken),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "buy_direct", operationToken),
		),
		menu.Row(
			menu.Data("❌ انصراف", "buy_cancel"),
		),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **خلاصه فاکتور خرید**\n\nطرح: %s\nاشتراک: %s\nمدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\nکاربر همزمان: %s\nسقف ترافیک: %s\nمبلغ کل: %s\n\nموجودی کیف پول شما: %s\n\nنحوه پرداخت را انتخاب کنید:",
		plan.Name, email, months, persian.FormatIPLimit(ipLimit), dataLabel,
		persian.FormatMoney(quote.FinalPriceToman),
		persian.FormatMoney(user.WalletBalance),
	), menu)
}

func HandleBuyAutoName(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_buy_custom_name" {
		return c.Send("هیچ فرآیند خرید فعالی یافت نشد.")
	}

	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	dataGBVal := state.Data["data_gb"]
	var dataGB int
	if dataGBVal != nil {
		dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", dataGBVal))
	}

	if months <= 0 {
		return c.Send("مدت زمان باید مثبت باشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد.")
	}

	baseName := ""
	if user.Username != "" {
		baseName = user.Username
	} else {
		baseName = fmt.Sprintf("%d", user.TelegramID)
	}
	baseName = sanitizeName(baseName)
	if baseName == "" {
		baseName = fmt.Sprintf("%d", user.TelegramID)
	}

	var email string
	for {
		email = fmt.Sprintf("%s_%s", baseName, randomToken(6))
		if existing, _ := db.GetSubscriptionByEmail(context.Background(), email); existing == nil {
			break
		}
	}

	operationToken := makeSubID()
	opKey := fmt.Sprintf("wallet_purchase:%s", operationToken)
	quote := pricing.CalculateQuote(pricing.QuoteParams{
		UserID:       user.ID,
		Plan:         plan,
		Months:       months,
		IPLimit:      ipLimit,
		DataGB:       dataGB,
		OperationKey: opKey,
	})
	if err := pricing.SaveQuote(context.Background(), quote); err != nil {
		log.Printf("[ERROR] failed to persist quote for user %d: %v", user.ID, err)
		return c.Send("خطا در ایجاد پیش‌فاکتور خرید. لطفا مجددا تلاش کنید.")
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_buy_confirm", map[string]interface{}{
		"plan_id":         fmt.Sprintf("%d", plan.ID),
		"plan_name":       plan.Name,
		"inbound_ids":     plan.InboundIDs,
		"flow":            plan.Flow,
		"quote_id":        fmt.Sprintf("%d", quote.ID),
		"quote_key":       quote.QuoteKey,
		"months":          fmt.Sprintf("%d", months),
		"ip_limit":        fmt.Sprintf("%d", ipLimit),
		"price":           fmt.Sprintf("%d", quote.FinalPriceToman),
		"price_toman":     fmt.Sprintf("%d", quote.FinalPriceToman),
		"custom_name":     baseName,
		"email":           email,
		"data_gb":         fmt.Sprintf("%d", dataGB),
		"type":            "buy",
		"operation_token": operationToken,
		"operation_key":   opKey,
	})

	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "buy_confirm", operationToken),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "buy_direct", operationToken),
		),
		menu.Row(
			menu.Data("❌ انصراف", "buy_cancel"),
		),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **خلاصه فاکتور خرید**\n\nطرح: %s\nاشتراک: %s\nمدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\nکاربر همزمان: %s\nسقف ترافیک: %s\nمبلغ کل: %s\n\nموجودی کیف پول شما: %s\n\nنحوه پرداخت را انتخاب کنید:",
		plan.Name, email, months, persian.FormatIPLimit(ipLimit), dataLabel,
		persian.FormatMoney(quote.FinalPriceToman),
		persian.FormatMoney(user.WalletBalance),
	), menu)
}

func HandleBuyConfirm(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("user:%d", user.ID))
	defer unlock()

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_buy_confirm" {
		return c.Send("هیچ خریدی در انتظار تایید نیست.")
	}
	callbackToken := strings.TrimSpace(callbackPayload(c))
	stateToken := strings.TrimSpace(fmt.Sprintf("%v", state.Data["operation_token"]))
	if stateToken != "" && callbackToken != stateToken {
		return c.Send("این تاییدیه منقضی شده است؛ لطفا خلاصه خرید فعلی را دوباره باز کنید.")
	}

	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	email := fmt.Sprintf("%v", state.Data["email"])
	name := fmt.Sprintf("%v", state.Data["custom_name"])
	var dataGB int
	if val, ok := state.Data["data_gb"]; ok && val != "" {
		dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", val))
	}
	operationKey := fmt.Sprintf("%v", state.Data["operation_key"])
	if operationKey == "" {
		operationKey = newOperationKey("wallet_purchase")
		state.Data["operation_key"] = operationKey
	}

	if !bot.FSM.CompareAndClearState(user.TelegramID, "awaiting_buy_confirm") {
		return c.Send("هیچ خریدی در انتظار تایید نیست.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد.")
	}
	if existing, _ := db.GetSubscriptionByEmail(context.Background(), email); existing != nil {
		return c.Send("این نام اشتراک در همین حین توسط شخص دیگری گرفته شد. لطفا فرآیند خرید را مجددا شروع کنید.")
	}

	var quote *pricing.PurchaseQuote
	var quoteErr error
	if qIDStr, ok := state.Data["quote_id"]; ok && qIDStr != "" {
		if qID, err := strconv.ParseInt(fmt.Sprintf("%v", qIDStr), 10, 64); err == nil && qID > 0 {
			quote, quoteErr = pricing.GetQuoteByID(context.Background(), qID)
		}
	}
	if quote == nil && quoteErr == nil {
		if quoteKey := fmt.Sprintf("%v", state.Data["quote_key"]); quoteKey != "" {
			quote, quoteErr = pricing.GetQuoteByKey(context.Background(), quoteKey)
		}
	}
	if quoteErr != nil || quote == nil {
		log.Printf("[ERROR] valid quote not found in db for user %d (err: %v)", user.ID, quoteErr)
		return c.Send("پیش‌فاکتور معتبر یافت نشد. لطفا فرآیند خرید را مجددا شروع کنید.")
	}
	if quote.UserID != user.ID {
		log.Printf("[SECURITY] quote %d belongs to user %d, but user %d attempted to confirm", quote.ID, quote.UserID, user.ID)
		return c.Send("پیش‌فاکتور متعلق به شما نیست.")
	}

	priceToman := quote.FinalPriceToman
	quoteID := &quote.ID

	if priceToman <= 0 {
		return c.Send("مبلغ پیش‌فاکتور معتبر نیست.")
	}
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return c.Send("این طرح هیچ کانکشن معتبری ندارد.")
	}
	planIDValue := int(plan.ID)
	provisioning := &reconcile.PurchaseProvisioningPayload{
		UserID: user.ID, QuoteID: quoteID, OperationKey: operationKey,
		DebitOperationKey: operationKey, Email: email, ExpectedUUID: makeClientUUID(),
		ExpectedSubID: makeSubID(), PlanID: &planIDValue, InboundIDs: inboundIDs,
		Months: months, IPLimit: ipLimit, DataGB: dataGB, Price: priceToman,
		ExpiryTimeMilli: -int64(months) * 30 * 24 * 3600 * 1000,
		TotalBytes:      int64(dataGB) * 1073741824, Flow: CleanFlow(plan.Flow),
		Group: serviceGroup(user), TelegramID: user.TelegramID, PlanName: plan.Name,
		RefundOperationKey: operationKey + ":refund", DisplayName: name,
	}
	workItem := reconcile.NewPurchaseProvisioningRecord(provisioning)
	workItem.ObservedState = map[string]any{"outcome": "wallet_debit_pending", "phase": "ready"}
	if err := db.DebitWalletBalanceWithReconciliation(context.Background(), user.ID, priceToman, "subscription purchase: "+email, operationKey, workItem); err != nil {
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این خرید قبلا پردازش شده یا در وضعیت تطبیق قرار دارد.")
		}
		return c.Send("موجودی کیف پول شما کافی نیست. لطفا ابتدا کیف پول خود را شارژ کنید یا از گزینه پرداخت مستقیم استفاده کنید.")
	}
	processor := reconcile.NewProcessor("wallet_buy", bot.XUIClient)
	_, _ = processor.ProcessOnce(context.Background())
	if subscription, lookupErr := db.GetSubscriptionByEmail(context.Background(), email); lookupErr == nil && subscription != nil {
		return c.Send("اشتراک با موفقیت فعال شد.")
	}
	return c.Send("درخواست خرید و برداشت کیف پول به‌صورت امن ثبت شد. فعال‌سازی سرویس پس از آماده‌شدن پنل ادامه می‌یابد.")
}

func HandleBuyDirectPayment(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_buy_confirm" {
		return c.Send("هیچ خریدی در انتظار تایید یافت نشد.")
	}
	callbackToken := strings.TrimSpace(callbackPayload(c))
	stateToken := strings.TrimSpace(fmt.Sprintf("%v", state.Data["operation_token"]))
	if stateToken != "" && callbackToken != stateToken {
		return c.Send("این تاییدیه منقضی شده است؛ لطفا خلاصه خرید فعلی را دوباره باز کنید.")
	}

	var priceToman int64
	if ptStr, ok := state.Data["price_toman"]; ok && ptStr != "" {
		priceToman, _ = strconv.ParseInt(fmt.Sprintf("%v", ptStr), 10, 64)
	}
	if priceToman <= 0 {
		return c.Send("مبلغ خرید معتبر نیست؛ لطفا خلاصه خرید را دوباره باز کنید.")
	}

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")

	var quoteID *int64
	if qIDStr, ok := state.Data["quote_id"]; ok && qIDStr != nil {
		if qVal, err := strconv.ParseInt(fmt.Sprintf("%v", qIDStr), 10, 64); err == nil && qVal > 0 {
			quoteID = &qVal
		}
	}
	var planIDPtr *int64
	if pIDStr, ok := state.Data["plan_id"]; ok && pIDStr != nil {
		if pVal, err := strconv.ParseInt(fmt.Sprintf("%v", pIDStr), 10, 64); err == nil && pVal > 0 {
			planIDPtr = &pVal
		}
	}
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	if months <= 0 {
		months = 1
	}
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	if ipLimit <= 0 {
		ipLimit = 1
	}
	dataGB, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["data_gb"]))
	customName := fmt.Sprintf("%v", state.Data["custom_name"])
	email := fmt.Sprintf("%v", state.Data["email"])

	intentToken := stateToken
	if intentToken == "" {
		intentToken = fmt.Sprintf("intent_%d_%d", user.ID, time.Now().UnixNano())
	}

	if planIDPtr != nil {
		if p, err := db.GetPaidPlanByID(context.Background(), *planIDPtr); err == nil && p != nil {
			if _, ok := state.Data["inbound_ids"]; !ok {
				state.Data["inbound_ids"] = p.InboundIDs
			}
			if _, ok := state.Data["flow"]; !ok {
				state.Data["flow"] = p.Flow
			}
			if _, ok := state.Data["plan_name"]; !ok {
				state.Data["plan_name"] = p.Name
			}
		}
	}
	if uuid, ok := state.Data["client_uuid"].(string); !ok || strings.TrimSpace(uuid) == "" {
		state.Data["client_uuid"] = makeClientUUID()
	}
	if subID, ok := state.Data["sub_id"].(string); !ok || strings.TrimSpace(subID) == "" {
		state.Data["sub_id"] = makeSubID()
	}
	state.Data["expiry_time_milli"] = -int64(months) * 30 * 24 * 3600 * 1000
	state.Data["total_bytes"] = int64(dataGB) * 1073741824
	state.Data["group"] = serviceGroup(user)
	state.Data["telegram_id"] = user.TelegramID

	intent := &db.PaymentIntent{
		UserID:               user.ID,
		IntentToken:          intentToken,
		ActionType:           "buy",
		PlanID:               planIDPtr,
		QuoteID:              quoteID,
		AmountToman:          priceToman,
		Months:               months,
		IPLimit:              ipLimit,
		DataGB:               dataGB,
		DisplayName:          customName,
		ClientEmail:          email,
		ProvisioningSnapshot: state.Data,
		Status:               db.IntentStatusAwaitingReceipt,
	}
	createdIntent, err := db.CreatePaymentIntent(context.Background(), intent)
	if err != nil {
		log.Printf("[INTENT] Failed to create payment intent for user %d: %v", user.ID, err)
		return c.Send("عملیات با خطا مواجه شد. لطفا مجددا تلاش کنید.")
	}
	state.Data["intent_id"] = fmt.Sprintf("%d", createdIntent.ID)
	state.Data["intent_token"] = createdIntent.IntentToken

	// Change state step to awaiting_purchase_receipt so HandleReceiptPhoto will catch it
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", state.Data)

	var text strings.Builder
	text.WriteString("💳 **راهنمای پرداخت مستقیم (کارت به کارت)**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%s**\n\n", persian.FormatMoney(priceToman)))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز وجه، **تصویر رسید (عکس فیش واریزی)** را در همینجا ارسال کنید تا سرویس شما پس از تایید ادمین به صورت خودکار فعال شود.")

	return maybeEditOrSend(c, text.String())
}

func HandleBuyCancel(c telebot.Context) error {
	user := userFromContext(c)
	if user != nil {
		bot.FSM.ClearState(user.TelegramID)
		_ = maybeEditOrSend(c, "❌ فرآیند خرید لغو شد.")
		return showMainMenu(c, user)
	}
	return maybeEditOrSend(c, "❌ فرآیند خرید لغو شد.")
}
