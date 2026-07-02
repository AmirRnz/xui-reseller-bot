package handlers

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-end-bot/internal/bot"
	"xui-end-bot/internal/db"
	"xui-end-bot/internal/xui"
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

	plans, err := db.GetPaidPlansForUser(context.Background(), user.ID, false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌های خرید.")
	}
	if len(plans) == 0 {
		return maybeEditOrSend(c, "در حال حاضر هیچ طرح خریدی موجود نیست.")
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	var text strings.Builder
	text.WriteString("💼 **طرح‌های خرید سرویس**:\n\n")
	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(plans)+1)
	for _, plan := range plans {
		if plan.IsLimited {
			text.WriteString(fmt.Sprintf("📦 **%s** (محدود)\nقیمت هر گیگابایت: %.0f %s\nحداقل ترافیک: %d گیگابایت\nماهانه اضافه: +%.0f %s\nدستگاه همزمان: %d تا سقف %d\nهزینه کاربر اضافه: +%.0f %s/کاربر/ماه\n",
				plan.Name, plan.PricePerGB, currency, plan.MinDataGB, plan.PricePerExtraMonth, currency, plan.BaseIPLimit, plan.MaxIPLimit, plan.PricePerExtraIP, currency))
		} else {
			text.WriteString(fmt.Sprintf("📦 **%s** (نامحدود)\nقیمت پایه: %.0f %s/ماهانه\nدستگاه همزمان: %d تا سقف %d\nهزینه کاربر اضافه: +%.0f %s/کاربر/ماه\n",
				plan.Name, plan.BasePrice, currency, plan.BaseIPLimit, plan.MaxIPLimit, plan.PricePerExtraIP, currency))
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	discountText := ""
	if len(plan.DiscountTiers) > 0 {
		discountText = "\n💰 تخفیف‌های خرید طولانی مدت:"
		for _, tier := range plan.DiscountTiers {
			discountText += fmt.Sprintf("\n  خرید %d ماهه و بیشتر: %.0f%% تخفیف", tier.Months, tier.Percent)
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("۱ ماهه", "buy_months", fmt.Sprintf("1:%d", plan.ID)), menu.Data("۳ ماهه", "buy_months", fmt.Sprintf("3:%d", plan.ID))),
		menu.Row(menu.Data("۶ ماهه", "buy_months", fmt.Sprintf("6:%d", plan.ID)), menu.Data("✏️ مدت دلخواه", "buy_custom_dur", fmt.Sprintf("%d", plan.ID))),
		menu.Row(menu.Data("« بازگشت", "menu_buy_sub")),
	)

	priceLabel := fmt.Sprintf("قیمت پایه: %.0f %s/ماهانه", plan.BasePrice, currency)
	if plan.IsLimited {
		priceLabel = fmt.Sprintf("قیمت هر گیگابایت: %.0f %s\nحداقل ترافیک: %d گیگابایت\nهزینه تمدید ماهانه اضافه: +%.0f %s", plan.PricePerGB, currency, plan.MinDataGB, plan.PricePerExtraMonth, currency)
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := plan.BaseIPLimit; ip <= plan.MaxIPLimit; ip++ {
		price := calculatePaidPrice(plan, months, ip, gb)
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("%d کاربر همزمان — %.0f %s", ip, price, currency), "buy_ip_run_limited", fmt.Sprintf("%d:%d:%d:%d", ip, plan.ID, months, gb))))
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
		"price":    fmt.Sprintf("%.2f", price),
		"data_gb":  fmt.Sprintf("%d", gb),
	})

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🎲 انتخاب توسط ربات", "buy_auto_name")),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"📦 **%s**\n%d گیگابایت، %d ماهه، %d کاربر همزمان\nقیمت: %.0f %s\n\nلطفا نام دلخواه برای اشتراک خود را ارسال کنید (فقط حروف و عدد انگلیسی):\n(یک پسوند تصادفی ۶ کاراکتری به انتهای نام انتخابی شما اضافه خواهد شد)",
		plan.Name, gb, months, ipLimit, price, currency), menu)
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := plan.BaseIPLimit; ip <= plan.MaxIPLimit; ip++ {
		price := calculatePaidPrice(plan, months, ip, 0)
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("%d کاربر همزمان — %.0f %s", ip, price, currency), "buy_ip_run", fmt.Sprintf("%d:%d:%d", ip, plan.ID, months))))
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
		"price":    fmt.Sprintf("%.2f", price),
		"data_gb":  "0",
	})

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🎲 انتخاب توسط ربات", "buy_auto_name")),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"📦 **%s**\n%d ماهه، %d کاربر همزمان\nقیمت: %.0f %s\n\nلطفا نام دلخواه برای اشتراک خود را ارسال کنید (فقط حروف و عدد انگلیسی):\n(یک پسوند تصادفی ۶ کاراکتری به انتهای نام انتخابی شما اضافه خواهد شد)",
		plan.Name, months, ipLimit, price, currency), menu)
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

	price := calculatePaidPrice(plan, months, ipLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_buy_confirm", map[string]interface{}{
		"plan_id":      fmt.Sprintf("%d", plan.ID),
		"months":       fmt.Sprintf("%d", months),
		"ip_limit":     fmt.Sprintf("%d", ipLimit),
		"price":        fmt.Sprintf("%.2f", price),
		"custom_name":  name,
		"email":        email,
		"data_gb":      fmt.Sprintf("%d", dataGB),
		"type":         "buy",
	})

	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "buy_confirm"),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "buy_direct"),
		),
		menu.Row(
			menu.Data("❌ انصراف", "buy_cancel"),
		),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **خلاصه فاکتور خرید**\n\nطرح: %s\nاشتراک: %s\nمدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\nکاربر همزمان: %d\nسقف ترافیک: %s\nمبلغ کل: %.0f %s\n\nموجودی کیف پول شما: %d %s\n\nنحوه پرداخت را انتخاب کنید:",
		plan.Name, email, months, ipLimit, dataLabel, price, currency,
		user.WalletBalance, currency,
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

	price := calculatePaidPrice(plan, months, ipLimit, dataGB)
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_buy_confirm", map[string]interface{}{
		"plan_id":      fmt.Sprintf("%d", plan.ID),
		"months":       fmt.Sprintf("%d", months),
		"ip_limit":     fmt.Sprintf("%d", ipLimit),
		"price":        fmt.Sprintf("%.2f", price),
		"custom_name":  baseName,
		"email":        email,
		"data_gb":      fmt.Sprintf("%d", dataGB),
		"type":         "buy",
	})

	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "buy_confirm"),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "buy_direct"),
		),
		menu.Row(
			menu.Data("❌ انصراف", "buy_cancel"),
		),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **خلاصه فاکتور خرید**\n\nطرح: %s\nاشتراک: %s\nمدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\nکاربر همزمان: %d\nسقف ترافیک: %s\nمبلغ کل: %.0f %s\n\nموجودی کیف پول شما: %d %s\n\nنحوه پرداخت را انتخاب کنید:",
		plan.Name, email, months, ipLimit, dataLabel, price, currency,
		user.WalletBalance, currency,
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

	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	email := fmt.Sprintf("%v", state.Data["email"])
	name := fmt.Sprintf("%v", state.Data["custom_name"])
	var dataGB int
	if val, ok := state.Data["data_gb"]; ok && val != "" {
		dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", val))
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

	price := calculatePaidPrice(plan, months, ipLimit, dataGB)
	if err := db.DebitWalletBalance(context.Background(), user.ID, price, "subscription purchase: "+email); err != nil {
		return c.Send("موجودی کیف پول شما کافی نیست. لطفا ابتدا کیف پول خود را شارژ کنید یا از گزینه پرداخت مستقیم استفاده کنید.")
	}

	if err := createPaidSubscription(c, user, plan, email, name, months, ipLimit, price, dataGB); err != nil {
		_ = db.CreditWalletBalance(context.Background(), user.ID, price, "refund for failed purchase: "+email)
		return c.Send("خطا در ایجاد اشتراک در پنل. مبلغ کسر شده به کیف پول شما عودت داده شد. " + err.Error())
	}
	return nil
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

	priceStr := fmt.Sprintf("%v", state.Data["price"])
	price, _ := strconv.ParseFloat(priceStr, 64)

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	// Change state step to awaiting_purchase_receipt so HandleReceiptPhoto will catch it
	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", state.Data)

	var text strings.Builder
	text.WriteString("💳 **راهنمای پرداخت مستقیم (کارت به کارت)**\n\n")
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%.0f %s**\n\n", price, currency))
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

func createPaidSubscription(c telebot.Context, user *db.User, plan *db.PaidPlan, email, displayName string, months int, ipLimit int, price float64, dataGB int) error {
	if bot.XUIClient == nil {
		return fmt.Errorf("x-ui client is not initialized")
	}
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return fmt.Errorf("این طرح هیچ کانکشن معتبری ندارد")
	}

	expireMilli := -int64(months * 30 * 24 * 3600 * 1000)
	totalBytes := int64(dataGB) * 1073741824
	subID := makeSubID()
	clientUUID := makeClientUUID()
	comment := fmt.Sprintf("created by xui-end-bot, %s, %s", plan.Name, userIdentifier(user))
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	adjustedIPLimit := ApplyIPLimitFactor(ipLimit, factor)
	client := newClientConfig(email, serviceGroup(user), user.TelegramID, totalBytes, expireMilli, adjustedIPLimit, plan.Flow, subID, clientUUID, comment)

	err := bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: inboundIDs})
	if err != nil {
		log.Printf("XUI AddClient failed: %v. Refreshing cache and retrying...", err)
		if bot.XUIClient.Cache != nil {
			bot.XUIClient.Cache.RefreshSync()
			newInboundIDs := validInboundIDs(plan.InboundIDs)
			if !intSlicesEqual(newInboundIDs, inboundIDs) {
				if len(newInboundIDs) == 0 {
					return fmt.Errorf("این طرح پس از بروزرسانی هیچ کانکشن معتبری ندارد")
				}
				err = bot.XUIClient.AddClient(xui.AddClientRequest{Client: client, InboundIDs: newInboundIDs})
			}
		}
	}
	if err != nil {
		return err
	}

	planID := int(plan.ID)
	sub := &db.Subscription{
		UserID:            user.ID,
		PlanID:            &planID,
		ClientEmail:       email,
		ClientUUID:        clientUUID,
		SubID:             subID,
		Status:            "active",
		PlanType:          db.PlanTypePaid,
		DisplayName:       displayName,
		IPLimit:           adjustedIPLimit,
		ExpireTime:        &expireMilli,
		IsActive:          true,
		StartDate:         nowUTC(),
		EndDate:           time.Time{},
		TrafficLimitBytes: totalBytes,
	}
	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		log.Printf("[CRITICAL] Database save failed for subscription %s: %v. Rolling back panel client.", email, err)
		go func() {
			var deleteErr error
			for i := 0; i < 5; i++ {
				if deleteErr = bot.XUIClient.DeleteClient(email); deleteErr == nil {
					log.Printf("Rollback successful: Deleted client %s from panel", email)
					return
				}
				time.Sleep(time.Duration(1<<i) * time.Second)
			}
			log.Printf("[ALERT] CRITICAL: Failed to delete client %s from panel after 5 retries: %v. Client is orphaned on panel!", email, deleteErr)
		}()
		return err
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
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
	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}

	detailsMsg := fmt.Sprintf("✅ اشتراک شما با موفقیت فعال شد!\n📦 طرح: %s\n⏱️ مدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\n📊 سقف ترافیک: %s\n💰 هزینه پرداخت شده: %.0f %s",
		plan.Name, months, dataLabel, price, currency)

	if plan.UsageDescription != "" {
		detailsMsg += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.UsageDescription)
	}

	if err := sendSubscriptionResult(c, subLink, detailsMsg); err != nil {
		_ = c.Send(detailsMsg + "\n`" + subLink + "`", telebot.ModeMarkdown)
	}
	return showMainMenu(c, user)
}

