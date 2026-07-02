package handlers

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/qr"
	"xui-reseller-bot/internal/xui"
)

var safeNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{1,63}$`)
var unsafeCharsPattern = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func showMainMenu(c telebot.Context, user *db.User) error {
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	if user.Status == db.UserStatusBanned {
		return c.Send("حساب کاربری شما مسدود شده است.")
	}

	menu := &telebot.ReplyMarkup{}
	
	if user.IsApproved() {
		rows := []telebot.Row{
			menu.Row(menu.Data("🧪 تست رایگان", "menu_test_sub"), menu.Data("💼 خرید سرویس", "menu_buy_sub")),
			menu.Row(menu.Data("📋 سرویس‌های من", "menu_my_services"), menu.Data("👛 کیف پول", "menu_wallet")),
			menu.Row(menu.Data("🆘 پشتیبانی", "menu_support")),
		}
		menu.Inline(rows...)
		return maybeEditOrSend(c, "👋 به پنل کاربری خوش آمدید\nسرویس وی‌پی‌ان خود را مدیریت کنید یا سرویس جدید خریداری نمایید.", menu)
	} else {
		rows := []telebot.Row{
			menu.Row(menu.Data("🧪 دریافت تست", "menu_test_sub")),
			menu.Row(menu.Data("📝 درخواست دسترسی نمایندگی", "menu_request_access")),
			menu.Row(menu.Data("🆘 پشتیبانی", "menu_support")),
		}
		menu.Inline(rows...)
		return maybeEditOrSend(c, "👋 به ربات نمایندگی خوش آمدید\nبرای دسترسی به امکانات کامل خرید و مدیریت سرویس، لطفا درخواست دسترسی خود را ثبت کنید.", menu)
	}
}

func callbackPayload(c telebot.Context) string {
	data := c.Data()
	if data == "" && c.Callback() != nil {
		data = c.Callback().Data
	}
	data = strings.TrimPrefix(data, "\f")
	if i := strings.Index(data, "|"); i >= 0 {
		return data[i+1:]
	}
	return data
}

func callbackParts(c telebot.Context) []string {
	payload := callbackPayload(c)
	if payload == "" {
		return nil
	}
	return strings.Split(payload, "|")
}

func parseInt64(s string) (int64, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v, err
}

func parseFloat(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err
}

func sanitizeName(input string) string {
	input = strings.TrimSpace(input)
	input = strings.ReplaceAll(input, " ", "_")
	input = unsafeCharsPattern.ReplaceAllString(input, "_")
	input = strings.Trim(input, "_-")
	if len(input) > 48 {
		input = input[:48]
	}
	return input
}

func validateServiceName(input string) bool {
	return safeNamePattern.MatchString(input) && !strings.HasPrefix(strings.ToLower(input), "test_")
}

func randomToken(n int) string {
	if n <= 0 {
		n = 4
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		log.Panicf("CRITICAL: Failed to read from cryptographically secure random source: %v", err)
	}
	return strings.ToLower(hex.EncodeToString(buf))[:n]
}

func randomName() string {
	return "random" + randomToken(5)
}

func CalculateRefund(plan *db.PaidPlan, sub *db.Subscription, ipFactor float64, now time.Time) int64 {
	if plan == nil || sub == nil || sub.EndDate.IsZero() {
		return 0
	}
	totalMonths := int(sub.EndDate.Sub(sub.StartDate).Hours() / 24 / 30)
	if totalMonths < 1 {
		totalMonths = 1
	}
	remainingMonths := int(sub.EndDate.Sub(now).Hours() / 24 / 30)
	if remainingMonths <= 0 {
		return 0
	}

	dataGB := int(sub.TrafficLimitBytes / 1073741824)
	displayIPLimit := ReverseIPLimitFactor(sub.IPLimit, fmt.Sprintf("%f", ipFactor))

	totalPaid := calculatePaidPrice(plan, totalMonths, displayIPLimit, dataGB)
	return int64((totalPaid / float64(totalMonths)) * float64(remainingMonths))
}

func serviceGroup(user *db.User) string {
	if user != nil && user.ServiceName != nil {
		return *user.ServiceName
	}
	return ""
}

func userIdentifier(user *db.User) string {
	if user == nil {
		return "unknown"
	}
	if user.Username != "" {
		if strings.HasPrefix(user.Username, "@") {
			return user.Username
		}
		return "@" + user.Username
	}
	return fmt.Sprintf("%d", user.TelegramID)
}

func makeSubID() string {
	return randomToken(16)
}

func makeClientUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		token := randomToken(32)
		return fmt.Sprintf("%s-%s-%s-%s-%s", token[:8], token[8:12], token[12:16], token[16:20], token[20:32])
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:])
}

func calculatePaidPrice(plan *db.PaidPlan, months, ipLimit int, dataGB int) float64 {
	if plan == nil || months <= 0 {
		return 0
	}
	if ipLimit < plan.BaseIPLimit {
		ipLimit = plan.BaseIPLimit
	}
	extraIPs := ipLimit - plan.BaseIPLimit

	var basePrice float64
	if plan.IsLimited {
		basePrice = (float64(dataGB) * plan.PricePerGB) + float64(months-1)*plan.PricePerExtraMonth
	} else {
		basePrice = plan.BasePrice * float64(months)
	}

	extraIPPrice := float64(extraIPs) * plan.PricePerExtraIP * float64(months)
	subtotal := basePrice + extraIPPrice
	discount := bestDiscount(plan.DiscountTiers, months)
	return math.Round(subtotal*(1-discount/100)*100) / 100
}

func bestDiscount(tiers []db.DiscountTier, months int) float64 {
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Months < tiers[j].Months })
	best := 0.0
	for _, tier := range tiers {
		if months >= tier.Months && tier.Percent > best {
			best = tier.Percent
		}
	}
	return best
}

func validInboundIDs(ids []int) []int {
	if len(ids) == 0 || bot.XUIClient == nil {
		return ids
	}
	cached := bot.XUIClient.GetCachedInbounds()
	if len(cached) == 0 {
		return ids
	}
	known := map[int]bool{}
	for _, inbound := range cached {
		known[inbound.ID] = true
	}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if known[id] {
			out = append(out, id)
		}
	}
	return out
}

func CleanFlow(flow string) string {
	flow = strings.TrimSpace(flow)
	flow = strings.Trim(flow, `'"`)
	return flow
}

func newClientConfig(email, group string, telegramID int64, totalBytes int64, expiryMilli int64, limitIP int, flow string, subID string, clientUUID string, comment string) xui.ClientConfig {
	if limitIP <= 0 {
		limitIP = 1
	}
	flow = CleanFlow(flow)
	return xui.ClientConfig{
		ID:         clientUUID,
		Email:      email,
		Enable:     true,
		ExpiryTime: expiryMilli,
		Flow:       flow,
		Group:      group,
		LimitIP:    limitIP,
		Reset:      0,
		Security:   "auto",
		SubID:      subID,
		TgID:       telegramID,
		TotalGB:    totalBytes,
		Comment:    comment,
		Password:   clientUUID,
		Auth:       clientUUID,
	}
}

func sendSubscriptionResult(c telebot.Context, link string, detailsMsg string) error {
	if link == "" {
		return c.Send(FormatMarkdown(detailsMsg)+"\nNo subscription link was found.", telebot.ModeMarkdown)
	}

	// Send QR code photo with the link as caption (formatted to be copyable on click)
	png, err := qr.GenerateQR(link)
	if err == nil {
		photo := &telebot.Photo{
			File:    telebot.FromReader(bytes.NewReader(png)),
			Caption: fmt.Sprintf("`%s`", link),
		}
		_ = c.Send(photo, telebot.ModeMarkdown)
	} else {
		_ = c.Send(fmt.Sprintf("`%s`", link), telebot.ModeMarkdown)
	}

	// Send details message
	if detailsMsg != "" {
		_ = c.Send(FormatMarkdown(detailsMsg), telebot.ModeMarkdown)
	}
	return nil
}

func sendSubscriptionResultTo(recipientID int64, link string, detailsMsg string) error {
	if bot.Bot == nil {
		return fmt.Errorf("bot is not initialized")
	}
	user := &telebot.User{ID: recipientID}

	if link != "" {
		png, err := qr.GenerateQR(link)
		if err == nil {
			photo := &telebot.Photo{
				File:    telebot.FromReader(bytes.NewReader(png)),
				Caption: fmt.Sprintf("`%s`", link),
			}
			_, _ = bot.Bot.Send(user, photo, telebot.ModeMarkdown)
		} else {
			_, _ = bot.Bot.Send(user, fmt.Sprintf("`%s`", link), telebot.ModeMarkdown)
		}
	}

	if detailsMsg != "" {
		_, _ = bot.Bot.Send(user, FormatMarkdown(detailsMsg), telebot.ModeMarkdown)
	}
	return nil
}

func maybeEditOrSend(c telebot.Context, text string, opts ...interface{}) error {
	var hasParseMode bool
	for _, opt := range opts {
		if _, ok := opt.(telebot.ParseMode); ok {
			hasParseMode = true
			break
		}
	}
	formattedText := FormatMarkdown(text)
	if !hasParseMode {
		opts = append(opts, telebot.ModeMarkdown)
	}

	if c.Callback() != nil {
		if err := c.Edit(formattedText, opts...); err == nil {
			return nil
		}
	}
	return c.Send(formattedText, opts...)
}

// FormatMarkdown formats a standard markdown string to Telegram legacy Markdown format.
// It converts double asterisks (**) to single asterisks (*) for bold text,
// preserves code blocks (```) and code spans (`),
// and escapes unescaped markdown characters like _, *, and [ to prevent Telegram parsing errors.
func FormatMarkdown(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	insideCodeBlock := false
	insideCodeSpan := false
	insideBold := false

	for i := 0; i < n; i++ {
		// Code block (```)
		if i+2 < n && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			insideCodeBlock = !insideCodeBlock
			result.WriteString("```")
			i += 2
			continue
		}

		if insideCodeBlock {
			result.WriteRune(runes[i])
			continue
		}

		// Code span (`)
		if runes[i] == '`' {
			insideCodeSpan = !insideCodeSpan
			result.WriteRune('`')
			continue
		}

		if insideCodeSpan {
			result.WriteRune(runes[i])
			continue
		}

		// Bold (**)
		if runes[i] == '*' && i+1 < n && runes[i+1] == '*' {
			insideBold = !insideBold
			result.WriteRune('*') // Convert ** to * for Telegram Markdown V1
			i++
			continue
		}

		// Escape unescaped markdown entities when not inside code blocks or code spans
		if runes[i] == '_' {
			result.WriteString("\\_")
		} else if runes[i] == '*' {
			result.WriteString("\\*")
		} else if runes[i] == '[' {
			result.WriteString("\\[")
		} else {
			result.WriteRune(runes[i])
		}
	}

	return result.String()
}

func userFromContext(c telebot.Context) *db.User {
	if v := c.Get("user"); v != nil {
		if user, ok := v.(*db.User); ok {
			return user
		}
	}
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[int]int)
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
		if m[x] < 0 {
			return false
		}
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func diffIntSlices(oldSlice, newSlice []int) (added, removed []int) {
	oldMap := make(map[int]bool)
	for _, x := range oldSlice {
		oldMap[x] = true
	}
	newMap := make(map[int]bool)
	for _, x := range newSlice {
		newMap[x] = true
	}
	for x := range newMap {
		if !oldMap[x] {
			added = append(added, x)
		}
	}
	for x := range oldMap {
		if !newMap[x] {
			removed = append(removed, x)
		}
	}
	return
}

func ApplyIPLimitFactor(ipLimit int, factorSetting string) int {
	factorSetting = strings.TrimSpace(factorSetting)
	if factorSetting == "" {
		return ipLimit
	}
	if strings.HasPrefix(factorSetting, "*") {
		num, err := strconv.Atoi(factorSetting[1:])
		if err == nil && num > 0 {
			return ipLimit * num
		}
	} else if strings.HasPrefix(factorSetting, "+") {
		num, err := strconv.Atoi(factorSetting[1:])
		if err == nil && num >= 0 {
			return ipLimit + num
		}
	}
	return ipLimit
}

func ReverseIPLimitFactor(adjustedIPLimit int, factorSetting string) int {
	factorSetting = strings.TrimSpace(factorSetting)
	if factorSetting == "" {
		return adjustedIPLimit
	}
	if strings.HasPrefix(factorSetting, "*") {
		num, err := strconv.Atoi(factorSetting[1:])
		if err == nil && num > 0 {
			res := adjustedIPLimit / num
			if res < 1 {
				return 1
			}
			return res
		}
	} else if strings.HasPrefix(factorSetting, "+") {
		num, err := strconv.Atoi(factorSetting[1:])
		if err == nil && num >= 0 {
			res := adjustedIPLimit - num
			if res < 1 {
				return 1
			}
			return res
		}
	}
	return adjustedIPLimit
}

