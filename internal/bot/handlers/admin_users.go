package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
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
		return c.Send("Failed to load users.")
	}

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👥 **Users** (%d total, page %d/%d)\n\n", total, page+1, max(totalPages, 1)))
	for _, user := range users {
		statusIcon := "⏳"
		switch user.Status {
		case "approved", "active":
			statusIcon = "✅"
		case "banned":
			statusIcon = "🚫"
		}
		text.WriteString(fmt.Sprintf("%s #%d @%s %s — %d\n", statusIcon, user.ID, user.Username, user.Status, user.WalletBalance))
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
		navRow = append(navRow, menu.Data("◀️ Prev", "admin_users_page", fmt.Sprintf("%d", page-1)))
	}
	if totalPages > 0 && page < totalPages-1 {
		navRow = append(navRow, menu.Data("Next ▶️", "admin_users_page", fmt.Sprintf("%d", page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	rows = append(rows,
		menu.Row(menu.Data("📢 Bulk credit approved", "bulk_credit")),
		menu.Row(menu.Data("« Back", "admin_menu")),
	)
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminViewUser(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("Invalid user.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("User not found.")
	}
	return showAdminViewUser(c, user)
}

func showAdminViewUser(c telebot.Context, user *db.User) error {
	subs, _ := db.GetSubscriptionsByUserID(context.Background(), user.ID)

	var text strings.Builder
	text.WriteString(fmt.Sprintf("👤 **User #%d**\n\n", user.ID))
	text.WriteString(fmt.Sprintf("🆔 **Telegram ID**: `%d`\n", user.TelegramID))
	text.WriteString(fmt.Sprintf("🌐 **Username**: @%s\n", user.Username))
	text.WriteString(fmt.Sprintf("📝 **Name**: %s %s\n", user.FirstName, user.LastName))
	text.WriteString(fmt.Sprintf("⚡ **Status**: %s\n", user.Status))
	text.WriteString(fmt.Sprintf("💼 **Service**: %s\n", user.ServiceNameValue()))
	text.WriteString(fmt.Sprintf("👛 **Balance**: %d\n", user.WalletBalance))
	text.WriteString(fmt.Sprintf("📦 **Subscriptions**: %d\n", len(subs)))

	menu := &telebot.ReplyMarkup{}
	rows := []telebot.Row{
		menu.Row(menu.Data("👥 View User's Clients", "admin_user_clients", fmt.Sprintf("%d", user.ID))),
		menu.Row(menu.Data("💳 Manual credit", "admin_user_credit", fmt.Sprintf("%d", user.ID))),
	}
	if user.Status == db.UserStatusBanned {
		rows = append(rows, menu.Row(menu.Data("✅ Unban", "admin_user_unban", fmt.Sprintf("%d", user.ID))))
	} else if user.Status == db.UserStatusPending {
		rows = append(rows, menu.Row(menu.Data("✅ Approve", "admin_user_approve", fmt.Sprintf("%d", user.ID))))
		rows = append(rows, menu.Row(menu.Data("🚫 Ban", "admin_user_ban", fmt.Sprintf("%d", user.ID))))
	} else {
		rows = append(rows, menu.Row(menu.Data("🚫 Ban", "admin_user_ban", fmt.Sprintf("%d", user.ID))))
	}
	rows = append(rows, menu.Row(menu.Data("« Back", "admin_users")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminUserBan(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("Invalid user.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("User not found.")
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, db.UserStatusBanned); err != nil {
		return c.Send("Failed to ban user.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("🚫 User #%d banned.", user.ID)})
	user.Status = db.UserStatusBanned
	return showAdminViewUser(c, user)
}

func HandleAdminUserUnban(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("Invalid user.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("User not found.")
	}
	status := db.UserStatusApproved
	if user.ServiceNameValue() == "" {
		status = db.UserStatusApprovedNamePending
	}
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, status); err != nil {
		return c.Send("Failed to unban user.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ User #%d unbanned.", user.ID)})
	user.Status = status
	return showAdminViewUser(c, user)
}

func HandleAdminUserApprove(c telebot.Context) error {
	userID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("Invalid user.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("User not found.")
	}
	
	if err := db.UpdateUserStatusByID(context.Background(), user.ID, db.UserStatusApproved); err != nil {
		return c.Send("Failed to approve user.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ User #%d approved.", user.ID)})
	user.Status = db.UserStatusApproved
	
	if bot.Bot != nil {
		msg := "✅ درخواست دسترسی شما تایید شد.\n\nلطفا نام سرویس (شناسه نمایندگی) خود را وارد کنید.\nاین نام باید فقط شامل حروف انگلیسی، اعداد، خط تیره (-) و آندرلاین (_) باشد و بین ۳ تا ۳۲ کاراکتر باشد."
		bot.FSM.SetState(user.TelegramID, "awaiting_service_name", nil)
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, msg)
	}
	
	return showAdminViewUser(c, user)
}

func HandleAdminUserCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	userID, err := parseInt64(callbackPayload(c))
	if admin == nil || err != nil {
		return c.Send("Invalid user.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_manual_credit", map[string]interface{}{"target_user_id": fmt.Sprintf("%d", userID)})
	return maybeEditOrSend(c, "Enter amount to credit this user.")
}

func HandleBulkCreditPrompt(c telebot.Context) error {
	admin := userFromContext(c)
	if admin == nil {
		return c.Send("Could not load admin account.")
	}
	bot.FSM.SetState(admin.TelegramID, "awaiting_bulk_credit_amount", nil)
	return maybeEditOrSend(c, "Enter amount to credit all approved users.")
}

func ProcessBulkCredit(c telebot.Context, amountStr string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("You do not have permission to use this command.")
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(amountStr), 64)
	if err != nil || amount <= 0 {
		return c.Send("Invalid amount. Enter a positive number.")
	}
	count, err := db.CreditAllApprovedUsers(context.Background(), amount, "admin bulk credit")
	if err != nil {
		return c.Send("Failed to credit approved users.")
	}
	bot.FSM.ClearState(admin.TelegramID)
	_ = c.Send(fmt.Sprintf("✅ Credited %.0f to %d approved users.", amount, count))
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
		return c.Send("Invalid user.")
	}
	user, err := db.GetUserByID(context.Background(), userID)
	if err != nil || user == nil {
		return c.Send("User not found.")
	}
	
	subs, err := db.GetSubscriptionsByUserID(context.Background(), user.ID)
	if err != nil {
		return c.Send("Failed to get clients.")
	}
	
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	
	var text strings.Builder
	text.WriteString(fmt.Sprintf("👥 **Clients for @%s**\n\n", user.Username))
	
	hasUnassigned := false
	for _, sub := range subs {
		if sub.PlanID == nil {
			hasUnassigned = true
			break
		}
	}
	
	if hasUnassigned {
		text.WriteString("⚠️ **Unassigned Clients (Need Plan)**\n")
		for _, sub := range subs {
			if sub.PlanID == nil {
				text.WriteString(fmt.Sprintf("🔸 %s\n", sub.ClientEmail))
				rows = append(rows, menu.Row(
					menu.Data(fmt.Sprintf("Assign Plan: %s", sub.ClientEmail), "admin_assign_plan", fmt.Sprintf("%d", sub.ID)),
				))
			}
		}
		text.WriteString("\n")
	}
	
	text.WriteString("✅ **Assigned Clients**\n")
	for _, sub := range subs {
		if sub.PlanID != nil {
			text.WriteString(fmt.Sprintf("🔹 %s\n", sub.ClientEmail))
		}
	}
	if len(subs) == 0 {
		text.WriteString("No clients found.\n")
	}
	
	rows = append(rows, menu.Row(menu.Data("« Back to User", "admin_view_user", fmt.Sprintf("%d", user.ID))))
	menu.Inline(rows...)
	return maybeEditOrSend(c, text.String(), menu)
}

func HandleAdminAssignPlanPrompt(c telebot.Context) error {
	subID, err := parseInt64(callbackPayload(c))
	if err != nil {
		return c.Send("Invalid subscription.")
	}
	
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("Subscription not found.")
	}
	
	paidPlans, err := db.GetPaidPlans(context.Background(), false)
	if err != nil {
		return c.Send("Failed to load plans.")
	}
	
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	for _, plan := range paidPlans {
		rows = append(rows, menu.Row(
			menu.Data(plan.Name, "admin_assign_plan_confirm", fmt.Sprintf("%d:%d", sub.ID, plan.ID)),
		))
	}
	rows = append(rows, menu.Row(menu.Data("« Cancel", "admin_user_clients", fmt.Sprintf("%d", sub.UserID))))
	menu.Inline(rows...)
	
	return maybeEditOrSend(c, fmt.Sprintf("Please select a plan to assign to **%s**:", sub.ClientEmail), menu)
}

func HandleAdminAssignPlanConfirm(c telebot.Context) error {
	parts := strings.Split(callbackPayload(c), ":")
	if len(parts) != 2 {
		return c.Send("Invalid request.")
	}
	subID, _ := parseInt64(parts[0])
	planID, _ := parseInt64(parts[1])
	
	sub, err := db.GetSubscriptionByID(context.Background(), int(subID))
	if err != nil || sub == nil {
		return c.Send("Subscription not found.")
	}
	
	plan, err := db.GetPaidPlanByID(context.Background(), planID)
	if err != nil || plan == nil {
		return c.Send("Plan not found.")
	}
	
	pID := int(plan.ID)
	sub.PlanID = &pID
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		return c.Send("Failed to update subscription.")
	}
	
	user, _ := db.GetUserByID(context.Background(), sub.UserID)
	if user != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, fmt.Sprintf("✅ اشتراک %s با موفقیت به طرح %s متصل شد. هم‌اکنون می‌توانید از امکانات تمدید و ارتقا استفاده کنید.", sub.ClientEmail, plan.Name))
	}
	
	_ = c.Respond(&telebot.CallbackResponse{Text: "Plan assigned successfully."})
	
	if c.Callback() != nil {
		c.Callback().Data = fmt.Sprintf("\fadmin_user_clients|%d", sub.UserID)
	}
	return HandleAdminUserClients(c)
}

