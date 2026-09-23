package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
)

const usersPageSize = 15

func RegisterAdminUsers(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc) {
	b.Handle("\fadmin_users", HandleAdminUsers, auth, admin)
	b.Handle("\fadmin_users_page", HandleAdminUsersPage, auth, admin)
	b.Handle("\fadmin_view_user", HandleAdminViewUser, auth, admin)
	b.Handle("\fadmin_user_ban", HandleAdminUserBan, auth, admin)
	b.Handle("\fadmin_user_unban", HandleAdminUserUnban, auth, admin)
	b.Handle("\fadmin_user_approve", HandleAdminUserApprove, auth, admin)
	b.Handle("\fapprove_user", HandleAdminUserApprove, auth, admin)
	b.Handle("\fadmin_user_credit", HandleAdminUserCreditPrompt, auth, admin)
	b.Handle("\fbulk_credit", HandleBulkCreditPrompt, auth, admin)
	b.Handle("\fadmin_user_clients", HandleAdminUserClients, auth, admin)
	b.Handle("\fadmin_assign_plan", HandleAdminAssignPlanPrompt, auth, admin)
	b.Handle("\fadmin_assign_plan_confirm", HandleAdminAssignPlanConfirm, auth, admin)
}

func HandleAdminUsers(c telebot.Context) error {
	return showAdminUsersPage(c, 0)
}

func HandleAdminUsersPage(c telebot.Context) error {
	page, _ := strconv.Atoi(callbackPayload(c))
	return showAdminUsersPage(c, page)
}

func showAdminUsersPage(c telebot.Context, page int) error {
	total, _ := db.GetAllUsersCount(context.Background())
	totalPages := (total + usersPageSize - 1) / usersPageSize
	if page < 0 {
		page = 0
	}
	if totalPages > 0 && page >= totalPages {
		page = totalPages - 1
	}
	offset := page * usersPageSize

	users, err := db.ListUsers(context.Background(), usersPageSize, offset)
	if err != nil {
		return c.Send("خطا در بارگذاری لیست کاربران.")
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👥 **کاربران و نمایندگان** (%d کاربر کل، صفحه %d از %d)\n\n", total, page+1, max(totalPages, 1)))
	for _, user := range users {
		statusIcon := "⏳"
		statusText := "در انتظار"
		switch user.Status {
		case "approved", "active":
			statusIcon = "✅"
			statusText = "تایید شده"
		case "banned":
			statusIcon = "🚫"
			statusText = "مسدود"
		}
		text.WriteString(fmt.Sprintf("%s #%d @%s (%s) — %s\n", statusIcon, user.ID, user.Username, statusText, persian.FormatMoney(int64(user.WalletBalance))))
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, user := range users {
		name := user.ServiceNameValue()
		if name == "" {
			name = user.Username
		}
		if name == "" {
			name = fmt.Sprintf("user_%d", user.ID)
		}
		rows = append(rows, menu.Row(menu.Data(fmt.Sprintf("#%d %s", user.ID, name), "admin_view_user", fmt.Sprintf("%d", user.ID))))
	}

	navRow := []telebot.Btn{}
	if page > 0 {
		navRow = append(navRow, menu.Data("◀️ صفحه قبل", "admin_users_page", fmt.Sprintf("%d", page-1)))
	}
	if totalPages > 0 && page < totalPages-1 {
		navRow = append(navRow, menu.Data("صفحه بعد ▶️", "admin_users_page", fmt.Sprintf("%d", page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows,
		menu.Row(menu.Data("📢 شارژ همگانی تاییدشدگان", "bulk_credit")),
		menu.Row(menu.Data("« بازگشت", "admin_menu")),
	)
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminViewUser(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	return showAdminViewUser(c, user)
}

func showAdminViewUser(c telebot.Context, user *db.User) error {
	subs, _ := db.GetSubscriptionsByUserID(context.Background(), user.ID)

	statusText := user.Status
	switch user.Status {
	case "approved":
		statusText = "تایید شده"
	case "pending":
		statusText = "در انتظار تایید"
	case "banned":
		statusText = "مسدود شده"
	case "approved_name_pending":
		statusText = "تایید شده (در انتظار ثبت نام)"
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👤 **مشخصات نماینده #%d**\n\n", user.ID))
	text.WriteString(fmt.Sprintf("🆔 **شناسه عددی تلگرام**: `%d`\n", user.TelegramID))
	text.WriteString(fmt.Sprintf("🌐 **نام کاربری**: @%s\n", user.Username))
	text.WriteString(fmt.Sprintf("📝 **نام**: %s %s\n", user.FirstName, user.LastName))
	text.WriteString(fmt.Sprintf("⚡ **وضعیت حساب**: %s\n", statusText))
	text.WriteString(fmt.Sprintf("💼 **نام برند نماینده**: %s\n", user.ServiceNameValue()))
	text.WriteString(fmt.Sprintf("👛 **موجودی کیف پول**: %s\n", persian.FormatMoney(int64(user.WalletBalance))))
	text.WriteString(fmt.Sprintf("📦 **تعداد اشتراک‌ها**: %d\n", len(subs)))

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(menu.Data("👥 مشاهده کلاینت‌های کاربر", "admin_user_clients", fmt.Sprintf("%d", user.ID))),
		menu.Row(menu.Data("💳 افزایش موجودی دستی", "admin_user_credit", fmt.Sprintf("%d", user.ID))),
	}
	if user.Status == db.UserStatusBanned {
		rows = append(rows, menu.Row(menu.Data("✅ رفع مسدودیت (آنبن)", "admin_user_unban", fmt.Sprintf("%d", user.ID))))
	} else if user.Status == db.UserStatusPending {
		rows = append(rows, menu.Row(menu.Data("✅ تایید نماینده", "admin_user_approve", fmt.Sprintf("%d", user.ID))))
		rows = append(rows, menu.Row(menu.Data("🚫 مسدودسازی (بن)", "admin_user_ban", fmt.Sprintf("%d", user.ID))))
	} else {
		rows = append(rows, menu.Row(menu.Data("🚫 مسدودسازی (بن)", "admin_user_ban", fmt.Sprintf("%d", user.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« بازگشت به لیست", "admin_users")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminUserBan(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, db.UserStatusBanned); err != nil {
		return c.Send("خطا در مسدود کردن کاربر.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("🚫 کاربر #%d مسدود شد.", user.ID)})
	user.Status = db.UserStatusBanned
	return showAdminViewUser(c, user)
}

func HandleAdminUserUnban(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	status := db.UserStatusApproved
	if user.ServiceNameValue() == "" {
		status = db.UserStatusApprovedNamePending
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, status); err != nil {
		return c.Send("خطا در رفع مسدودیت کاربر.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ کاربر #%d از مسدودیت خارج شد.", user.ID)})
	user.Status = status
	return showAdminViewUser(c, user)
}

func HandleAdminUserApprove(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}
	if user.Status == db.UserStatusApproved || user.Status == db.UserStatusApprovedNamePending {
		return c.Send("این کاربر قبلاً تایید شده است.")
	}

	if err := db.UpdateUserStatusByID(context.Background(), user.ID, db.UserStatusApproved); err != nil {
		return c.Send("خطا در تایید کاربر.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ کاربر #%d تایید شد.", user.ID)})
	user.Status = db.UserStatusApproved

	if bot.Bot != nil {
		msg := "✅ درخواست نمایندگی شما تایید شد.\n\nلطفاً نام برند/سرویس خود را وارد کنید.\nاین نام باید فقط شامل حروف انگلیسی، اعداد، خط تیره (-) و آندرلاین (_) باشد و بین ۳ تا ۳۲ کاراکتر باشد:"
		bot.FSM.SetState(user.TelegramID, "awaiting_service_name", nil)
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, msg)
	}

	return showAdminViewUser(c, user)
}

func HandleAdminUserCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	userID, err := parseInt64(callbackPayload(c))
	if admin == nil || err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_manual_credit", map[string]interface{}{"target_user_id": fmt.Sprintf("%d", userID), "operation_key": "manual_admin_credit:" + makeSubID()})
	return maybeEditOrSend(c, "لطفاً مبلغ مورد نظر جهت شارژ حساب این کاربر را به تومان وارد کنید:")
}

func HandleBulkCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	if admin == nil {
		return c.Send("امکان بارگذاری حساب ادمین وجود ندارد.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_bulk_credit_amount", map[string]interface{}{"operation_key": "bulk_admin_credit:" + makeSubID()})
	return maybeEditOrSend(c, "لطفاً مبلغ مورد نظر جهت شارژ همگانی کلیه نمایندگان تایید شده را به تومان وارد کنید:")
}

func ProcessBulkCredit(c telebot.Context, amountStr string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی به این دستور را ندارید.")
	}
	amount, err := strconv.ParseInt(strings.TrimSpace(amountStr), 10, 64)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. لطفاً یک عدد صحیح مثبت به تومان وارد کنید.")
	}
	var operationKey string
	state := bot.FSM.GetState(admin.TelegramID)
	if state != nil && state.Data != nil {
		if k, ok := state.Data["operation_key"]; ok && k != nil && k != "" && k != "<nil>" {
			operationKey = fmt.Sprintf("%v", k)
		}
	}
	if operationKey == "" {
		operationKey = "bulk_admin_credit:" + makeSubID()
	}
	count, err := db.CreditAllApprovedUsersWithKey(context.Background(), amount, "admin bulk credit", operationKey)
	if err != nil {
		if errors.Is(err, db.ErrWalletOperationAlreadyApplied) {
			return c.Send("این عملیات قبلاً اعمال شده است.")
		}
		return c.Send("خطا در افزایش موجودی همگانی کاربران.")
	}
	bot.FSM.ClearState(admin.TelegramID)
	_ = c.Send(fmt.Sprintf("✅ مبلغ %s با موفقیت به حساب %d کاربر تایید شده افزوده شد.", persian.FormatMoney(amount), count))
	return HandleAdminUsers(c)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func HandleAdminUserClients(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("کاربر نامعتبر است.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("کاربر مورد نظر یافت نشد.")
	}

	subs, err := db.GetSubscriptionsByUserID(context.Background(), user.ID)
	if err != nil {
		return c.Send("خطا در دریافت کلاینت‌های کاربر.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👥 **کلاینت‌های کاربر @%s**\n\n", user.Username))

	hasUnassigned := false
	for _, sub := range subs {
		if sub.PlanID == nil {
			hasUnassigned = true
			break
		}
	}

	if hasUnassigned {
		text.WriteString("⚠️ **کلاینت‌های فاقد طرح (نیاز به انتساب طرح)**\n")
		for _, sub := range subs {
			if sub.PlanID == nil {
				text.WriteString(fmt.Sprintf("🔸 %s\n", sub.ClientEmail))
				rows = append(rows, menu.Row(
					menu.Data(fmt.Sprintf("انتساب طرح: %s", sub.ClientEmail), "admin_assign_plan", fmt.Sprintf("%d", sub.ID)),
				))
			}
		}
		text.WriteString("\n")
	}

	text.WriteString("✅ **کلاینت‌های دارای طرح**\n")
	for _, sub := range subs {
		if sub.PlanID != nil {
			text.WriteString(fmt.Sprintf("🔹 %s\n", sub.ClientEmail))
		}
	}
	if len(subs) == 0 {
		text.WriteString("هیچ کلاینتی یافت نشد.\n")
	}

	rows = append(rows, menu.Row(menu.Data("« بازگشت به کاربر", "admin_view_user", fmt.Sprintf("%d", user.ID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminAssignPlanPrompt(c telebot.Context) error {
	subID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("اشتراک نامعتبر است.")
	}

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک مورد نظر یافت نشد.")
	}

	paidPlans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil {
		return c.Send("خطا در بارگذاری طرح‌ها.")
	}

	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, plan := range paidPlans {
		rows = append(rows, menu.Row(
			menu.Data(plan.Name, "admin_assign_plan_confirm", fmt.Sprintf("%d:%d", sub.ID, plan.ID)),
		))
	}
	rows = append(rows, menu.Row(menu.Data("« انصراف", "admin_user_clients", fmt.Sprintf("%d", sub.UserID))))
	menu.Inline(rows...)

	return maybeEditOrSend(c, fmt.Sprintf("لطفاً طرح مورد نظر جهت انتساب به **%s** را انتخاب کنید:", sub.ClientEmail), menu)
}

func HandleAdminAssignPlanConfirm(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("درخواست نامعتبر است.")
	}
	subID, _ := parseInt64(parts[0])
	planID, _ := parseInt64(parts[1])

	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("اشتراک مورد نظر یافت نشد.")
	}

	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("طرح مورد نظر یافت نشد.")
	}

	pID := int(plan.ID)
	sub.PlanID = &pID
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		return c.Send("خطا در به‌روزرسانی اشتراک.")
	}

	user, _ := db.GetUserByID(context.Background(), sub.UserID)
	if user != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, fmt.Sprintf("✅ اشتراک %s با موفقیت به طرح %s متصل شد. هم‌اکنون می‌توانید از امکانات تمدید و ارتقا استفاده کنید.", sub.ClientEmail, plan.Name))
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: "✅ طرح با موفقیت منتسب شد."})

	if c.Callback() != nil {
		c.Callback().Data = fmt.Sprintf("\fadmin_user_clients|%d", sub.UserID)
	}
	return HandleAdminUserClients(c)
}
