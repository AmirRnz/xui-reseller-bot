package handlers

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
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
		if lower == "http" || lower == "https" || lower == "sub" || lower == "api" || lower == "json" {
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

	// 4. If input without scheme looks like domain/sub/...
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

	if sub == nil {
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("❌ اشتراکی با این مشخصات یافت نشد.\nلطفا از صحت لینک یا شناسه ارسالی اطمینان حاصل کرده و مجددا ارسال فرمایید:", menu)
	}

	if sub.UserID != user.ID {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("⛔ این اشتراک متعلق به حساب کاربری شما نیست و امکان تمدید آن را ندارید.", menu)
	}

	if sub.PlanType != db.PlanTypePaid {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("⚠️ تمدید فقط برای سرویس‌های خریداری شده امکان‌پذیر است.", menu)
	}

	if !db.IsExtensionEligibleState(sub.Status, sub.IsActive, sub.ExpireTime, nowUTC()) {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("⚠️ وضعیت سرویس برای تمدید مناسب نیست؛ سرویس‌های لغو شده یا در حال تغییر قابل تمدید نیستند.", menu)
	}

	plan, err := paidPlanForSub(sub)
	if err != nil || plan == nil || !plan.Enabled {
		bot.FSM.ClearState(user.TelegramID)
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(menu.Data("« بازگشت", "menu_main")))
		return c.Send("طرح مربوط به این اشتراک یافت نشد یا در حال حاضر غیرفعال است.", menu)
	}

	bot.FSM.ClearState(user.TelegramID)
	return showSubscriptionExtendMenuFor(c, sub, plan)
}

func showSubscriptionExtendMenuFor(c telebot.Context, sub *db.Subscription, plan *db.PaidPlan) error {
	displayIPLimit := sub.IPLimit

	priceFor := func(months int) int64 {
		dataGB := int(sub.TrafficLimitBytes / 1073741824)
		return calculatePaidPrice(plan, months, displayIPLimit, dataGB)
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(
			menu.Data(fmt.Sprintf("۱ ماهه — %s", persian.FormatMoney(priceFor(1))), "sub_extend_run", fmt.Sprintf("1:%d", sub.ID)),
			menu.Data(fmt.Sprintf("۳ ماهه — %s", persian.FormatMoney(priceFor(3))), "sub_extend_run", fmt.Sprintf("3:%d", sub.ID)),
		),
		menu.Row(
			menu.Data(fmt.Sprintf("۶ ماهه — %s", persian.FormatMoney(priceFor(6))), "sub_extend_run", fmt.Sprintf("6:%d", sub.ID)),
			menu.Data("✏️ مدت دلخواه", "sub_extend_custom", fmt.Sprintf("%d", sub.ID)),
		),
		menu.Row(
			menu.Data("« بازگشت", "view_sub", fmt.Sprintf("%d", sub.ID)),
			menu.Data("🏠 منوی اصلی", "menu_main"),
		),
	)

	var expiryLabel string
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
			durMs := -*sub.ExpireTime
			days := durMs / (24 * 3600 * 1000)
			if days > 0 {
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", days)
			} else {
				hours := durMs / (3600 * 1000)
				expiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d ساعت)", hours)
			}
		} else {
			expiryLabel = "شروع پس از اولین اتصال"
		}
	} else {
		expiryLabel = sub.EndDate.Format("2006-01-02")
	}
	return maybeEditOrSend(c, fmt.Sprintf("⏳ تمدید سرویس **%s**\nتاریخ انقضای فعلی: %s\n\nمدت زمان تمدید را انتخاب کنید:", sub.DisplayName, expiryLabel), menu)
}
