package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

var (
	urlRegex   = regexp.MustCompile(`https?://[^\s"'<>]+`)
	vlessRegex = regexp.MustCompile(`(?:vless|vmess|trojan)://([^@\s]+)@`)
	uuidRegex  = regexp.MustCompile(`(?i)[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
)

// ExtractSubscriptionCandidates parses an arbitrary text input (such as a subscription URL,
// connection link, sub_id, or UUID) and returns a list of candidate subscription identifiers.
func ExtractSubscriptionCandidates(input string) []string {
	var candidates []string
	seen := make(map[string]bool)

	addCandidate := func(s string) {
		s = strings.TrimSpace(s)
		s = strings.Trim(s, " \t\r\n`'\"/\\?#.,")
		if s == "" || seen[s] {
			return
		}
		lower := strings.ToLower(s)
		if lower == "http" || lower == "https" || lower == "sub" || lower == "dub" || lower == "api" || lower == "json" {
			return
		}
		seen[s] = true
		candidates = append(candidates, s)
	}

	// 1. Check for UUID anywhere in the input
	for _, match := range uuidRegex.FindAllString(input, -1) {
		addCandidate(match)
	}

	// 2. Check for vless/vmess/trojan URLs
	if match := vlessRegex.FindStringSubmatch(input); len(match) > 1 {
		addCandidate(match[1])
	}

	// 3. Extract http(s) URLs
	for _, rawURL := range urlRegex.FindAllString(input, -1) {
		rawURL = strings.Trim(rawURL, " \t\r\n`'\".,")
		u, err := url.Parse(rawURL)
		if err == nil {
			path := strings.Trim(u.Path, "/")
			if path != "" {
				segments := strings.Split(path, "/")
				if len(segments) > 0 {
					addCandidate(segments[len(segments)-1])
				}
			}
			q := u.Query()
			for _, k := range []string{"token", "sub", "subId", "sub_id", "id", "uuid"} {
				if val := q.Get(k); val != "" {
					addCandidate(val)
				}
			}
		}
	}

	// 4. If input without scheme looks like domain/sub/... or domain/dub/...
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") && strings.Contains(trimmed, "/") {
		if u, err := url.Parse("http://" + trimmed); err == nil {
			path := strings.Trim(u.Path, "/")
			if path != "" {
				segments := strings.Split(path, "/")
				if len(segments) > 0 {
					addCandidate(segments[len(segments)-1])
				}
			}
		}
	}

	// 5. Add raw trimmed input if single token
	fields := strings.Fields(trimmed)
	if len(fields) == 1 {
		addCandidate(fields[0])
	}

	return candidates
}

// HandleExtendMenuFlow starts the flow where a reseller extends a subscription by sending its URL.
func HandleExtendMenuFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	if !user.IsApproved() {
		return c.Send("برای دسترسی به این بخش، ابتدا باید حساب نمایندگی شما تایید شود.")
	}
	bot.FSM.SetState(user.TelegramID, "awaiting_extend_sub_url", nil)
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("« بازگشت", "menu_main")),
	)
	return maybeEditOrSend(c, "🔗 لطفا لینک یا آدرس اشتراک (URL) را جهت تمدید ارسال کنید:\n\n(می‌توانید لینک کامل اشتراک، آدرس اتصال یا شناسه اشتراک را ارسال فرمایید)", menu)
}

// ProcessExtendSubURL processes the subscription URL or identifier sent by a reseller.
func ProcessExtendSubURL(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	if !user.IsApproved() {
		return c.Send("برای دسترسی به این بخش، ابتدا باید حساب نمایندگی شما تایید شود.")
	}

	candidates := ExtractSubscriptionCandidates(text)
	if len(candidates) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("❌ لینک یا شناسه معتبری در متن ارسالی یافت نشد.\nلطفا لینک صحیح اشتراک را ارسال فرمایید:", menu)
	}

	var sub *db.Subscription
	for _, candidate := range candidates {
		s, err := db.FindSubscriptionByIdentifier(context.Background(), candidate)
		if err == nil && s != nil {
			sub = s
			break
		}
	}

	// Fallback: If not found in DB, search 3X-UI panel and adopt if owned
	if sub == nil && bot.XUIClient != nil {
		for _, candidate := range candidates {
			remote, err := bot.XUIClient.FindClientBySubID(candidate)
			if err != nil || remote == nil {
				remote, _ = bot.XUIClient.GetClientByEmail(candidate)
			}
			if remote != nil {
				isOwner := remote.TgID == user.TelegramID || isConfiguredAdmin(user.TelegramID)
				if !isOwner && user.ServiceName != nil && *user.ServiceName != "" {
					prefix := strings.ToLower(*user.ServiceName) + "_"
					if strings.HasPrefix(strings.ToLower(remote.Email), prefix) {
						isOwner = true
					}
				}
				if isOwner {
					adopted := &db.Subscription{
						UserID:            user.ID,
						DisplayName:       remote.Email,
						ClientEmail:       remote.Email,
						ClientUUID:        remote.UUID,
						SubID:             remote.SubID,
						PlanType:          db.PlanTypePaid,
						IPLimit:           remote.LimitIP,
						TrafficLimitBytes: remote.TotalGB,
						IsActive:          remote.Enable,
						Status:            db.SubscriptionStatusActive,
					}
					if remote.ExpiryTime != 0 {
						exp := remote.ExpiryTime
						adopted.ExpireTime = &exp
						if exp > 0 {
							adopted.EndDate = time.UnixMilli(exp).UTC()
						}
					}
					if createErr := db.CreateSubscription(context.Background(), adopted); createErr == nil {
						sub = adopted
						break
					}
				}
			}
		}
	}

	if sub == nil {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("❌ اشتراکی با این مشخصات یافت نشد.\nلطفا از صحت لینک یا شناسه ارسالی اطمینان حاصل کرده و مجددا ارسال فرمایید:", menu)
	}

	if sub.UserID != user.ID && !isConfiguredAdmin(user.TelegramID) {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("⛔ این اشتراک متعلق به حساب کاربری شما نیست و امکان تمدید آن را ندارید.", menu)
	}

	if !db.IsExtensionEligibleState(sub.Status, sub.IsActive, sub.ExpireTime, nowUTC()) {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("⚠️ وضعیت سرویس برای تمدید مناسب نیست؛ سرویس‌های لغو شده یا حذف شده قابل تمدید نیستند.", menu)
	}

	bot.FSM.ClearState(user.TelegramID)
	return showExtendPlanSelection(c, user, sub)
}

// showExtendPlanSelection displays the plan options available for extending the subscription.
func showExtendPlanSelection(c telebot.Context, user *db.User, sub *db.Subscription) error {
	plans, err := db.GetPaidPlansForUser(context.Background(), user.ID, false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌های موجود.")
	}
	if len(plans) == 0 {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return maybeEditOrSend(c, "در حال حاضر هیچ طرح فعالی برای تمدید موجود نیست.", menu)
	}

	var expiryLabel string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			durMs := -*sub.ExpireTime
			days := durMs / (24 * 3600 * 1000)
			if days > 0 {
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (%d روز)", days)
			} else {
				expiryLabel = "شروع پس از اولین اتصال"
			}
		} else {
			expiryLabel = "شروع پس از اولین اتصال"
		}
	} else {
		expiryLabel = sub.EndDate.Format("2006-01-02")
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("🔄 **تمدید اشتراک %s**\n", sub.DisplayName))
	text.WriteString(fmt.Sprintf("تاریخ انقضای فعلی: %s\n\n", expiryLabel))
	text.WriteString("لطفا طرح مورد نظر خود را برای تمدید انتخاب کنید:\n\n")

	menu := &telebot.ReplyMarkup{}
	rows := make([]telebot.Row, 0, len(plans)+1)
	for _, plan := range plans {
		if plan.IsLimited {
			text.WriteString(fmt.Sprintf("📦 **%s** (محدود)\nقیمت هر گیگابایت: %s\nحداقل ترافیک: %d گیگابایت\nماهانه اضافه: +%s\n%s",
				plan.Name, persian.FormatMoney(plan.PricePerGBToman), plan.MinDataGB, persian.FormatMoney(plan.PricePerExtraMonthToman), formatPlanIPLimitDescription(plan)))
		} else {
			text.WriteString(fmt.Sprintf("📦 **%s** (نامحدود)\nقیمت پایه: %s/ماهانه\n%s",
				plan.Name, persian.FormatMoney(plan.BasePriceToman), formatPlanIPLimitDescription(plan)))
		}
		if plan.Description != "" {
			text.WriteString(fmt.Sprintf("%s\n", plan.Description))
		}
		text.WriteString("\n")
		rows = append(rows, menu.Row(menu.Data("📦 "+plan.Name, "extend_select_plan", fmt.Sprintf("%d:%d", sub.ID, plan.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

// HandleExtendSelectPlan handles when a reseller selects a plan for extending a subscription.
func HandleExtendSelectPlan(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات کاربر.")
	}

	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID, err := parseInt64(parts[0])
	if err != nil {
		return c.Send("شناسه اشتراک نامعتبر است.")
	}
	planID, err := parseInt64(parts[1])
	if err != nil {
		return c.Send("شناسه طرح نامعتبر است.")
	}

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || (sub.UserID != user.ID && !isConfiguredAdmin(user.TelegramID)) {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح مورد نظر یافت نشد یا غیرفعال است.")
	}

	discountText := ""
	if len(plan.DiscountTiers) > 0 {
		discountText = "\n💰 تخفیف‌های تمدید طولانی مدت:"
		for _, tier := range plan.DiscountTiers {
			discountText += fmt.Sprintf("\n  تمدید %d ماهه و بیشتر: %s%% تخفیف", tier.Months, formatBasisPointPercent(tier.GetBasisPoints()))
		}
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("۱ ماهه", "extend_months", fmt.Sprintf("%d:%d:1", sub.ID, plan.ID)),
			menu.Data("۳ ماهه", "extend_months", fmt.Sprintf("%d:%d:3", sub.ID, plan.ID)),
		),
		menu.Row(
			menu.Data("۶ ماهه", "extend_months", fmt.Sprintf("%d:%d:6", sub.ID, plan.ID)),
			menu.Data("✏️ مدت دلخواه", "extend_custom_dur", fmt.Sprintf("%d:%d", sub.ID, plan.ID)),
		),
		menu.Row(
			menu.Data("« بازگشت", "extend_back_sub", fmt.Sprintf("%d", sub.ID)),
		),
	)

	priceLabel := fmt.Sprintf("قیمت پایه: %s/ماهانه", persian.FormatMoney(plan.BasePriceToman))
	if plan.IsLimited {
		priceLabel = fmt.Sprintf("قیمت هر گیگابایت: %s\nحداقل ترافیک: %d گیگابایت\nهزینه ماهانه اضافه: +%s", persian.FormatMoney(plan.PricePerGBToman), plan.MinDataGB, persian.FormatMoney(plan.PricePerExtraMonthToman))
	}

	descText := ""
	if plan.Description != "" {
		descText = fmt.Sprintf("\n\n%s", plan.Description)
	}

	text := fmt.Sprintf("🔄 **تمدید %s**\nطرح انتخابی: **%s**\n%s%s%s\n\nمدت زمان تمدید را انتخاب کنید:",
		sub.DisplayName, plan.Name, priceLabel, discountText, descText)
	return maybeEditOrSend(c, text, menu)
}

// HandleExtendBackSub navigates back to plan selection for a subscription.
func HandleExtendBackSub(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا.")
	}
	subID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || (sub.UserID != user.ID && !isConfiguredAdmin(user.TelegramID)) {
		return c.Send("اشتراک یافت نشد.")
	}
	return showExtendPlanSelection(c, user, sub)
}

// HandleExtendCustomDuration prompts the reseller to enter custom months.
func HandleExtendCustomDuration(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID := parts[0]
	planID := parts[1]

	bot.FSM.SetState(user.TelegramID, "awaiting_extend_months_text", map[string]interface{}{
		"sub_id":  subID,
		"plan_id": planID,
	})
	return maybeEditOrSend(c, "لطفا تعداد ماه‌های مورد نظر برای تمدید را ارسال کنید (به عنوان مثال: 2):")
}

// ProcessExtendMonthsText handles custom months text input.
func ProcessExtendMonthsText(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_extend_months_text" {
		return c.Send("فرآیند تمدید فعالی یافت نشد.")
	}
	months, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || months <= 0 || months > 120 {
		return c.Send("تعداد ماه‌ها باید یک عدد بین ۱ تا ۱۲۰ باشد.")
	}

	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["sub_id"]))
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	bot.FSM.ClearState(user.TelegramID)
	return handlePostExtendDuration(c, user, sub, plan, months)
}

// HandleExtendMonths handles clicking one of the preset duration buttons.
func HandleExtendMonths(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID, _ := parseInt64(parts[0])
	planID, _ := parseInt64(parts[1])
	months, err := strconv.Atoi(parts[2])
	if err != nil || months <= 0 {
		return c.Send("مدت زمان باید یک عدد مثبت باشد.")
	}

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	return handlePostExtendDuration(c, user, sub, plan, months)
}

func handlePostExtendDuration(c telebot.Context, user *db.User, sub *db.Subscription, plan *db.PaidPlan, months int) error {
	if plan.IsLimited {
		return promptExtendDataLimit(c, sub, plan, months)
	}
	return handlePostExtendData(c, user, sub, plan, months, 0)
}

func promptExtendDataLimit(c telebot.Context, sub *db.Subscription, plan *db.PaidPlan, months int) error {
	minGB := int(plan.MinDataGB)
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB), "extend_data_gb", fmt.Sprintf("%d:%d:%d:%d", sub.ID, plan.ID, months, minGB)),
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+10), "extend_data_gb", fmt.Sprintf("%d:%d:%d:%d", sub.ID, plan.ID, months, minGB+10)),
		),
		menu.Row(
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+30), "extend_data_gb", fmt.Sprintf("%d:%d:%d:%d", sub.ID, plan.ID, months, minGB+30)),
			menu.Data(fmt.Sprintf("%d گیگابایت", minGB+50), "extend_data_gb", fmt.Sprintf("%d:%d:%d:%d", sub.ID, plan.ID, months, minGB+50)),
		),
		menu.Row(
			menu.Data("✏️ حجم دلخواه", "extend_data_gb_custom", fmt.Sprintf("%d:%d:%d", sub.ID, plan.ID, months)),
		),
		menu.Row(
			menu.Data("« بازگشت", "extend_select_plan", fmt.Sprintf("%d:%d", sub.ID, plan.ID)),
		),
	)
	text := fmt.Sprintf("🔄 **تمدید %s**\nطرح: **%s** — %d ماهه\nلطفا ترافیک مورد نظر خود را انتخاب کنید یا مقدار دلخواه را بنویسید (حداقل: %d گیگابایت):",
		sub.DisplayName, plan.Name, months, minGB)
	return maybeEditOrSend(c, text, menu)
}

// HandleExtendDataGBCustom prompts for custom traffic limit.
func HandleExtendDataGBCustom(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 3 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID := parts[0]
	planID := parts[1]
	months := parts[2]

	bot.FSM.SetState(user.TelegramID, "awaiting_extend_data_gb_text", map[string]interface{}{
		"sub_id":  subID,
		"plan_id": planID,
		"months":  months,
	})
	return maybeEditOrSend(c, "لطفا حجم ترافیک مورد نظر خود را به گیگابایت به صورت عدد ارسال کنید (مثلا: 30):")
}

// ProcessExtendDataGBText processes the custom data GB text input.
func ProcessExtendDataGBText(c telebot.Context, text string) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_extend_data_gb_text" {
		return c.Send("هیچ فرآیند تمدیدی یافت نشد.")
	}
	gb, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || gb <= 0 {
		return c.Send("مقدار ترافیک باید یک عدد مثبت به گیگابایت باشد.")
	}

	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["sub_id"]))
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}
	if int64(gb) < plan.MinDataGB {
		return c.Send(fmt.Sprintf("حداقل حجم ترافیک مجاز برای این طرح %d گیگابایت است. لطفا مجددا عدد دیگری ارسال کنید:", plan.MinDataGB))
	}

	bot.FSM.ClearState(user.TelegramID)
	return handlePostExtendData(c, user, sub, plan, months, gb)
}

// HandleExtendDataGB handles preset data limit button clicks.
func HandleExtendDataGB(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 4 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID, _ := parseInt64(parts[0])
	planID, _ := parseInt64(parts[1])
	months, _ := strconv.Atoi(parts[2])
	gb, _ := strconv.Atoi(parts[3])

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	return handlePostExtendData(c, user, sub, plan, months, gb)
}

func handlePostExtendData(c telebot.Context, user *db.User, sub *db.Subscription, plan *db.PaidPlan, months int, gb int) error {
	if plan.BaseIPLimit == plan.MaxIPLimit {
		return showExtendConfirmation(c, user, sub, plan, months, gb, plan.BaseIPLimit)
	}
	return showExtendIPChoices(c, sub, plan, months, gb)
}

func showExtendIPChoices(c telebot.Context, sub *db.Subscription, plan *db.PaidPlan, months int, gb int) error {
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for ip := plan.BaseIPLimit; ip <= plan.MaxIPLimit; ip++ {
		price := calculatePaidPrice(plan, months, ip, gb)
		var label string
		if ip == 0 {
			label = fmt.Sprintf("IP همزمان نامحدود — %s", persian.FormatMoney(price))
		} else {
			label = fmt.Sprintf("%d IP همزمان — %s", ip, persian.FormatMoney(price))
		}
		rows = append(rows, menu.Row(menu.Data(label, "extend_ip_run", fmt.Sprintf("%d:%d:%d:%d:%d", sub.ID, plan.ID, months, gb, ip))))
		if len(rows) >= 10 {
			break
		}
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت", "extend_select_plan", fmt.Sprintf("%d:%d", sub.ID, plan.ID))))
	menu.Inline(rows...)

	dataInfo := ""
	if plan.IsLimited {
		dataInfo = fmt.Sprintf(" — %d گیگابایت", gb)
	}
	return maybeEditOrSend(c, fmt.Sprintf("🔄 **تمدید %s**\nطرح: %s%s، %d ماهه\nتعداد IP همزمان (محدودیت IP) را انتخاب کنید:",
		sub.DisplayName, plan.Name, dataInfo, months), menu)
}

// HandleExtendIPRun handles IP limit selection for extension.
func HandleExtendIPRun(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 5 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID, _ := parseInt64(parts[0])
	planID, _ := parseInt64(parts[1])
	months, _ := strconv.Atoi(parts[2])
	gb, _ := strconv.Atoi(parts[3])
	ipLimit, _ := strconv.Atoi(parts[4])

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک یافت نشد.")
	}
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح یافت نشد.")
	}

	return showExtendConfirmation(c, user, sub, plan, months, gb, ipLimit)
}

func showExtendConfirmation(c telebot.Context, user *db.User, sub *db.Subscription, plan *db.PaidPlan, months int, gb int, ipLimit int) error {
	cost := calculatePaidPrice(plan, months, ipLimit, gb)
	now := nowUTC()
	newExpiryMilli, err := db.CalculateExtendedExpiry(sub.ExpireTime, months, now)
	if err != nil {
		return c.Send("تاریخ انقضای فعلی برای تمدید معتبر نیست.")
	}

	var newExpiryLabel string
	if newExpiryMilli < 0 {
		durDays := (-newExpiryMilli) / (24 * 3600 * 1000)
		newExpiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (%d روز)", durDays)
	} else {
		newExpiryLabel = time.UnixMilli(newExpiryMilli).UTC().Format("2006-01-02")
	}

	operationToken := makeSubID()
	opKey := fmt.Sprintf("wallet_extend_v2:%s", operationToken)

	bot.FSM.SetState(user.TelegramID, "awaiting_extend_confirm", map[string]interface{}{
		"subscription_id": fmt.Sprintf("%d", sub.ID),
		"plan_id":         fmt.Sprintf("%d", plan.ID),
		"months":          fmt.Sprintf("%d", months),
		"ip_limit":        fmt.Sprintf("%d", ipLimit),
		"data_gb":         fmt.Sprintf("%d", gb),
		"price":           fmt.Sprintf("%d", cost),
		"price_toman":     fmt.Sprintf("%d", cost),
		"type":            "extend",
		"operation_token": operationToken,
		"operation_key":   opKey,
	})

	dataLabel := "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", gb)
	}

	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data("👛 پرداخت از کیف پول", "extend_confirm", operationToken),
			menu.Data("💳 پرداخت مستقیم (کارت به کارت)", "extend_direct", operationToken),
		),
		menu.Row(
			menu.Data("❌ انصراف", "menu_main"),
		),
	)

	return maybeEditOrSend(c, fmt.Sprintf(
		"🧾 **خلاصه فاکتور تمدید**\n\nاشتراک: **%s**\nطرح: **%s**\nمدت زمان تمدید: %d ماه\nتاریخ انقضای جدید: %s\nسقف ترافیک: %s\nIP همزمان: %s\n\nمبلغ کل: **%s**\nموجودی کیف پول شما: %s\n\nنحوه پرداخت را انتخاب کنید:",
		sub.DisplayName, plan.Name, months, newExpiryLabel, dataLabel,
		persian.FormatIPLimit(ipLimit),
		persian.FormatMoney(cost),
		persian.FormatMoney(user.WalletBalance),
	), menu)
}

// HandleExtendConfirmWallet executes extension payment via wallet debit.
func HandleExtendConfirmWallet(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_extend_confirm" {
		return c.Send("هیچ فرآیند تمدیدی در انتظار تایید یافت نشد.")
	}
	callbackToken := strings.TrimSpace(callbackPayload(c))
	stateToken := strings.TrimSpace(fmt.Sprintf("%v", state.Data["operation_token"]))
	if stateToken != "" && callbackToken != stateToken {
		return c.Send("این تاییدیه منقضی شده است؛ لطفا فرآیند تمدید را دوباره شروع کنید.")
	}

	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["subscription_id"]))
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	dataGB, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["data_gb"]))
	cost, _ := strconv.ParseInt(fmt.Sprintf("%v", state.Data["price_toman"]), 10, 64)
	opKey := fmt.Sprintf("%v", state.Data["operation_key"])

	if subID <= 0 || planID <= 0 || months <= 0 || cost <= 0 {
		return c.Send("اطلاعات تمدید نامعتبر است.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("user:%d", user.ID))
	defer unlock()

	// Refresh user balance
	user, err := db.GetUserByID(context.Background(), user.ID)
	if err != nil || user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	if user.WalletBalance < cost {
		return c.Send(fmt.Sprintf("❌ موجودی کیف پول شما کافی نیست.\nموجودی فعلی: %s\nمبلغ تمدید: %s",
			persian.FormatMoney(user.WalletBalance), persian.FormatMoney(cost)))
	}

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || (sub.UserID != user.ID && !isConfiguredAdmin(user.TelegramID)) {
		return c.Send("اشتراک یافت نشد یا متعلق به شما نیست.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح انتخابی یافت نشد یا غیرفعال است.")
	}

	newExpiryMilli, err := db.CalculateExtendedExpiry(sub.ExpireTime, months, nowUTC())
	if err != nil {
		return c.Send("محاسبه تاریخ انقضا با خطا مواجه شد.")
	}

	// Atomically debit wallet balance
	err = db.DebitWalletBalanceWithKey(context.Background(), user.ID, cost, opKey, fmt.Sprintf("تمدید اشتراک: %s", sub.DisplayName))
	if err != nil {
		if errors.Is(err, db.ErrInsufficientWalletBalance) {
			return c.Send("موجودی کیف پول شما کافی نیست.")
		}
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این عملیات تمدید قبلاً انجام شده است.")
		}
		log.Printf("[ERROR] debit wallet for extend failed: %v", err)
		return c.Send("خطا در کسر موجودی کیف پول. مبلغی کسر نشد.")
	}

	// Update panel
	desiredTotalBytes := int64(0)
	if plan.IsLimited && dataGB > 0 {
		desiredTotalBytes = int64(dataGB) * 1024 * 1024 * 1024
	}
	if bot.XUIClient != nil {
		enableTrue := true
		patch := xui.ClientPatch{
			Enable:     &enableTrue,
			ExpiryTime: &newExpiryMilli,
			LimitIP:    &ipLimit,
			TotalGB:    &desiredTotalBytes,
		}
		write := bot.XUIClient.UpdateClientPatchResult(sub.ClientEmail, patch)
		if write.Outcome != xui.WriteSucceeded {
			log.Printf("[WARN] panel update patch for sub %d: %v", sub.ID, write.Err)
		}
		if len(plan.InboundIDs) > 0 {
			_ = bot.XUIClient.AttachClient(sub.ClientEmail, plan.InboundIDs)
		}
	}

	// Update local DB
	sub.ExpireTime = &newExpiryMilli
	sub.IPLimit = ipLimit
	sub.TrafficLimitBytes = desiredTotalBytes
	planIDInt := int(plan.ID)
	sub.PlanID = &planIDInt
	sub.IsActive = true
	sub.Status = db.SubscriptionStatusActive
	if newExpiryMilli > 0 {
		sub.EndDate = time.UnixMilli(newExpiryMilli).UTC()
	} else {
		sub.EndDate = time.Time{}
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		log.Printf("[ERROR] update local subscription after extend: %v", err)
	}

	bot.FSM.ClearState(user.TelegramID)

	// Delivery
	var subLink string
	if bot.XUIClient != nil && sub.SubID != "" {
		links, _ := bot.XUIClient.GetSubscriptionLinks(sub.SubID)
		for _, link := range links {
			if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
				subLink = link
				break
			}
		}
		if subLink == "" {
			subLink = bot.XUIClient.SubscriptionURLFor(sub.SubID)
		}
	}

	durationLabel := fmt.Sprintf("%d ماه", months)
	dataLabel := "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", dataGB)
	}
	ipLabel := persian.FormatIPLimit(ipLimit)

	details := fmt.Sprintf("✅ اشتراک **%s** با موفقیت تمدید شد!\n\n📦 طرح: %s\n⏱️ مدت تمدید: %s\n📊 سقف ترافیک: %s\n👥 سقف کاربر: %s\n💰 هزینه: %s",
		sub.DisplayName, plan.Name, durationLabel, dataLabel, ipLabel, persian.FormatMoney(cost))
	if plan.UsageDescription != "" {
		details += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.UsageDescription)
	}

	if subLink != "" {
		_ = sendSubscriptionResult(c, subLink, details)
	} else {
		_ = c.Send(FormatMarkdown(details), telebot.ModeMarkdown)
	}

	return showMainMenu(c, user)
}

// HandleExtendDirectPayment creates a payment intent for direct card-to-card renewal.
func HandleExtendDirectPayment(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}
	state := bot.FSM.GetState(user.TelegramID)
	if state == nil || state.Step != "awaiting_extend_confirm" {
		return c.Send("هیچ فرآیند تمدیدی در انتظار تایید یافت نشد.")
	}
	callbackToken := strings.TrimSpace(callbackPayload(c))
	stateToken := strings.TrimSpace(fmt.Sprintf("%v", state.Data["operation_token"]))
	if stateToken != "" && callbackToken != stateToken {
		return c.Send("این تاییدیه منقضی شده است؛ لطفا فرآیند تمدید را دوباره شروع کنید.")
	}

	subID, _ := parseInt64(fmt.Sprintf("%v", state.Data["subscription_id"]))
	planID, _ := parseInt64(fmt.Sprintf("%v", state.Data["plan_id"]))
	months, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["months"]))
	ipLimit, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["ip_limit"]))
	dataGB, _ := strconv.Atoi(fmt.Sprintf("%v", state.Data["data_gb"]))
	cost, _ := strconv.ParseInt(fmt.Sprintf("%v", state.Data["price_toman"]), 10, 64)
	operationToken := stateToken

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil || (sub.UserID != user.ID && !isConfiguredAdmin(user.TelegramID)) {
		return c.Send("اشتراک یافت نشد.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil || !plan.Enabled {
		return c.Send("طرح یافت نشد.")
	}

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")

	subID64 := int64(sub.ID)
	planID64 := int64(plan.ID)
	expectedExpiry := int64(0)
	if sub.ExpireTime != nil {
		expectedExpiry = *sub.ExpireTime
	}

	extendSnapshot := map[string]interface{}{
		"type":                             "extend",
		"subscription_id":                  fmt.Sprintf("%d", sub.ID),
		"plan_id":                          fmt.Sprintf("%d", plan.ID),
		"months":                           fmt.Sprintf("%d", months),
		"ip_limit":                         fmt.Sprintf("%d", ipLimit),
		"data_gb":                          fmt.Sprintf("%d", dataGB),
		"price_toman":                      fmt.Sprintf("%d", cost),
		"expected_ip_limit":                sub.IPLimit,
		"expected_expire_time_milli":       expectedExpiry,
		"expected_is_active":               sub.IsActive,
		"expected_subscription_updated_at": sub.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"expected_plan_updated_at":         plan.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"operation_key":                    operationKeyFromToken("direct_extend", operationToken),
		"operation_token":                  operationToken,
	}

	extendIntent := &db.PaymentIntent{
		UserID:               user.ID,
		IntentToken:          operationToken,
		ActionType:           "extend",
		SubscriptionID:       &subID64,
		PlanID:               &planID64,
		AmountToman:          cost,
		Months:               months,
		IPLimit:              ipLimit,
		DataGB:               dataGB,
		ClientEmail:          sub.ClientEmail,
		ProvisioningSnapshot: extendSnapshot,
		Status:               db.IntentStatusAwaitingReceipt,
	}

	if _, err := db.CreatePaymentIntent(context.Background(), extendIntent); err != nil {
		log.Printf("[INTENT] Failed to create payment intent for user %d extend: %v", user.ID, err)
		return paymentIntentCreateFailureForError(c, user, "عملیات با خطا مواجه شد. لطفاً مجدداً تلاش کنید یا با پشتیبانی در ارتباط باشید.", err)
	}

	bot.FSM.SetState(user.TelegramID, "awaiting_purchase_receipt", extendSnapshot)

	var text strings.Builder
	text.WriteString("💳 **پرداخت مستقیم برای تمدید سرویس**\n\n")
	text.WriteString(fmt.Sprintf("سرویس: **%s**\n", sub.DisplayName))
	text.WriteString(fmt.Sprintf("طرح: **%s** (%d ماهه)\n", plan.Name, months))
	text.WriteString(fmt.Sprintf("مبلغ قابل پرداخت: **%s**\n\n", persian.FormatMoney(cost)))
	if card != "" {
		text.WriteString(fmt.Sprintf("شماره کارت جهت واریز:\n`%s`\n", card))
	}
	if owner != "" {
		text.WriteString(fmt.Sprintf("نام صاحب کارت: **%s**\n", owner))
	}
	if desc != "" {
		text.WriteString(fmt.Sprintf("\n%s\n", desc))
	}
	text.WriteString("\n⚠️ لطفا پس از واریز، **رسید پرداخت (تصویر فیش)** را در همینجا ارسال کنید تا سرویس پس از تایید ادمین تمدید شود.")

	return maybeEditOrSend(c, text.String())
}

// RegisterExtendHandlers registers all extension-related callbacks.
func RegisterExtendHandlers(b *telebot.Bot, auth telebot.MiddlewareFunc) {
	b.Handle("\fmenu_extend", HandleExtendMenuFlow, auth)
	b.Handle("\fextend_select_plan", HandleExtendSelectPlan, auth)
	b.Handle("\fextend_back_sub", HandleExtendBackSub, auth)
	b.Handle("\fextend_months", HandleExtendMonths, auth)
	b.Handle("\fextend_custom_dur", HandleExtendCustomDuration, auth)
	b.Handle("\fextend_data_gb", HandleExtendDataGB, auth)
	b.Handle("\fextend_data_gb_custom", HandleExtendDataGBCustom, auth)
	b.Handle("\fextend_ip_run", HandleExtendIPRun, auth)
	b.Handle("\fextend_confirm", HandleExtendConfirmWallet, auth)
	b.Handle("\fextend_direct", HandleExtendDirectPayment, auth)
}
