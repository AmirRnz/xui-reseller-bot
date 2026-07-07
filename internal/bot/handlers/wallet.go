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
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

var walletAdminCfg *config.AdminConfig

func RegisterWallet(b *telebot.Bot, auth telebot.MiddlewareFunc, admin telebot.MiddlewareFunc, adminCfg *config.AdminConfig) {
	walletAdminCfg = adminCfg
	b.Handle("\fmenu_wallet", HandleWalletFlow, auth)
	b.Handle("\fbtn_topup", HandleTopupInstructions, auth)
	b.Handle("\fadmin_list_topups", HandleAdminPendingTopups, auth, admin)
	b.Handle("\fadmin_pending_topups", HandleAdminPendingTopups, auth, admin)
	b.Handle("\fadmin_approve_topup", HandleAdminApproveTopup, auth, admin)
	b.Handle("\fadmin_reject_topup", HandleAdminRejectTopup, auth, admin)
	b.Handle("\fadmin_approve_purchase", HandleAdminApprovePurchase, auth, admin)
	b.Handle("\fadmin_reject_purchase", HandleAdminRejectPurchase, auth, admin)
	b.Handle("\fadmin_approve_refund", HandleAdminApproveRefund, auth, admin)
	b.Handle("\fadmin_reject_refund", HandleAdminRejectRefund, auth, admin)
	b.Handle(telebot.OnPhoto, HandleReceiptPhoto, auth)
}

func HandleWalletFlow(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری اطلاعات حساب کاربری.")
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	menu := &telebot.ReplyMarkup{}
	row := []telebot.Row{menu.Row(menu.Data("📥 شارژ کیف پول", "btn_topup"))}
	if isConfiguredAdmin(c.Sender().ID) {
		row = append(row, menu.Row(menu.Data("⏳ تراکنش‌های در انتظار شارژ", "admin_list_topups")))
	}
	rows := append(row, menu.Row(menu.Data("« بازگشت", "menu_main")))
	menu.Inline(rows...)
	return maybeEditOrSend(c, fmt.Sprintf("👛 **موجودی کیف پول شما:** %d %s", user.WalletBalance, currency), menu)
}

func HandleTopupInstructions(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	card, _ := db.GetSetting(context.Background(), "card_number")
	owner, _ := db.GetSetting(context.Background(), "card_owner")
	desc, _ := db.GetSetting(context.Background(), "topup_description")
	minAmount, _ := db.GetSetting(context.Background(), "min_topup_amount")

	bot.FSM.SetState(user.TelegramID, "awaiting_receipt", nil)
	text := "لطفا پس از واریز مبلغ مورد نظر، تصویر رسید پرداخت (فیش واریزی) خود را در قالب عکس ارسال کنید."
	if card != "" {
		text += "\n\nشماره کارت جهت واریز:\n`" + card + "`"
	}
	if owner != "" {
		text += "\nنام صاحب کارت: **" + owner + "**"
	}
	if minAmount != "" && minAmount != "0" {
		text += "\nحداقل مبلغ شارژ مجاز: " + minAmount
	}
	if desc != "" {
		text += "\n\n" + desc
	}
	return maybeEditOrSend(c, text)
}

func HandleReceiptPhoto(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("user_receipt:%d", user.ID))
	defer unlock()

	state := bot.FSM.GetState(user.TelegramID)
	if state == nil {
		return c.Send("هیچ فرآیند فعالی برای ارسال رسید وجود ندارد. لطفا ابتدا درخواست پرداخت خود را ثبت کنید.")
	}

	if c.Message() == nil || c.Message().Photo == nil {
		return c.Send("لطفا رسید پرداخت را به صورت تصویر (عکس) ارسال کنید.")
	}
	fileID := c.Message().Photo.FileID

	if state.Step == "awaiting_receipt" {
		req := &db.TopupRequest{
			UserID:         user.ID,
			TelegramFileID: fileID,
			Status:         "pending",
		}
		if err := db.CreateTopupRequest(context.Background(), req); err != nil {
			return c.Send("خطا در ثبت درخواست افزایش موجودی.")
		}
		bot.FSM.ClearState(user.TelegramID)

		if walletAdminCfg != nil {
			for _, adminID := range walletAdminCfg.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(menu.Row(
					menu.Data("تایید", "admin_approve_topup", fmt.Sprintf("%d", req.ID)),
					menu.Data("رد", "admin_reject_topup", fmt.Sprintf("%d", req.ID)),
				))
				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: fmt.Sprintf("درخواست افزایش موجودی کیف پول #%d\nکاربر: @%s\nشناسه تلگرام: %d", req.ID, user.Username, user.TelegramID)}, menu)
			}
		}
		_ = c.Send("رسید شما دریافت شد. لطفا منتظر بررسی و تایید ادمین بمانید.")
		return showMainMenu(c, user)

	} else if state.Step == "awaiting_purchase_receipt" {
		pType := fmt.Sprintf("%v", state.Data["type"])
		priceStr := fmt.Sprintf("%v", state.Data["price"])
		price, _ := strconv.ParseFloat(priceStr, 64)

		var planIDPtr *int64
		if pidStr, ok := state.Data["plan_id"]; ok && pidStr != "" {
			pid, _ := parseInt64(fmt.Sprintf("%v", pidStr))
			planIDPtr = &pid
		}

		var subIDPtr *int64
		if sidStr, ok := state.Data["subscription_id"]; ok && sidStr != "" {
			sid, _ := parseInt64(fmt.Sprintf("%v", sidStr))
			subIDPtr = &sid
		}

		var months int
		if mStr, ok := state.Data["months"]; ok && mStr != "" {
			months, _ = strconv.Atoi(fmt.Sprintf("%v", mStr))
		}

		var ipLimit int
		if ipStr, ok := state.Data["ip_limit"]; ok && ipStr != "" {
			ipLimit, _ = strconv.Atoi(fmt.Sprintf("%v", ipStr))
		}

		var dataGB int
		if gbStr, ok := state.Data["data_gb"]; ok && gbStr != "" {
			dataGB, _ = strconv.Atoi(fmt.Sprintf("%v", gbStr))
		}

		customName := ""
		if cn, ok := state.Data["custom_name"]; ok {
			customName = fmt.Sprintf("%v", cn)
		}

		email := ""
		if em, ok := state.Data["email"]; ok {
			email = fmt.Sprintf("%v", em)
		}

		req := &db.PurchaseRequest{
			UserID:         user.ID,
			Type:           pType,
			PlanID:         planIDPtr,
			SubscriptionID: subIDPtr,
			Price:          price,
			Months:         months,
			IPLimit:        ipLimit,
			DataGB:         dataGB,
			CustomName:     customName,
			ClientEmail:    email,
			TelegramFileID: fileID,
			Status:         "pending",
		}

		if err := db.CreatePurchaseRequest(context.Background(), req); err != nil {
			log.Printf("Failed to create purchase request: %v", err)
			return c.Send("خطا در ثبت درخواست خرید مستقیم.")
		}
		bot.FSM.ClearState(user.TelegramID)

		currency, _ := db.GetSetting(context.Background(), "currency_name")
		if currency == "" {
			currency = "IRR"
		}

		if walletAdminCfg != nil {
			for _, adminID := range walletAdminCfg.AdminIDs {
				menu := &telebot.ReplyMarkup{}
				menu.Inline(menu.Row(
					menu.Data("تایید خرید", "admin_approve_purchase", fmt.Sprintf("%d", req.ID)),
					menu.Data("رد خرید", "admin_reject_purchase", fmt.Sprintf("%d", req.ID)),
				))

				var details string
				switch pType {
				case "buy":
					details = fmt.Sprintf("خرید سرویس جدید\nطرح: %s\nایمیل: %s\nمدت: %d ماه\nکاربر همزمان: %d\nحجم: %d گیگابایت", customName, email, months, ipLimit, dataGB)
				case "extend":
					details = fmt.Sprintf("تمدید سرویس\nشناسه اشتراک: %d\nمدت تمدید: %d ماه", *subIDPtr, months)
				case "upgrade_ip":
					details = fmt.Sprintf("ارتقای تعداد کاربر همزمان\nشناسه اشتراک: %d\nتعداد کاربر جدید: %d", *subIDPtr, ipLimit)
				}

				caption := fmt.Sprintf("📥 درخواست خرید مستقیم #%d\nکاربر: @%s (%d)\nنوع: %s\nمبلغ: %.0f %s\n\nجزئیات:\n%s",
					req.ID, user.Username, user.TelegramID, pType, price, currency, details)

				_, _ = bot.Bot.Send(&telebot.User{ID: adminID}, &telebot.Photo{File: telebot.File{FileID: fileID}, Caption: caption}, menu)
			}
		}

		_ = c.Send("رسید پرداخت شما دریافت شد. پس از بررسی ادمین، سرویس شما فعال شده و مشخصات آن برایتان ارسال خواهد شد.")
		return showMainMenu(c, user)
	}

	return c.Send("مرحله نامعتبر است. لطفا مجددا تلاش کنید.")
}

func HandleAdminPendingTopups(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqs, err := db.GetPendingTopupRequests(context.Background())
	if err != nil || len(reqs) == 0 {
		return c.Send("هیچ درخواست شارژ در انتظاری وجود ندارد.")
	}
	for _, req := range reqs {
		user, _ := db.GetUserByID(context.Background(), req.UserID)
		username := "unknown"
		if user != nil {
			username = user.Username
		}
		menu := &telebot.ReplyMarkup{}
		menu.Inline(menu.Row(
			menu.Data("تایید", "admin_approve_topup", fmt.Sprintf("%d", req.ID)),
			menu.Data("رد", "admin_reject_topup", fmt.Sprintf("%d", req.ID)),
		))
		_, _ = bot.Bot.Send(c.Sender(), &telebot.Photo{File: telebot.File{FileID: req.TelegramFileID}, Caption: fmt.Sprintf("درخواست افزایش موجودی کیف پول #%d\nکاربر: @%s", req.ID, username)}, menu)
	}
	return nil
}

func HandleAdminApproveTopup(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}
	req, err := db.GetTopupRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		return c.Send("درخواست شارژ یافت نشد یا قبلا بررسی شده است.")
	}
	admin := userFromContext(c)
	bot.FSM.SetState(admin.TelegramID, "awaiting_topup_amount", map[string]interface{}{"req_id": fmt.Sprintf("%d", reqID)})
	return maybeEditOrSend(c, fmt.Sprintf("لطفا مبلغ تایید شده برای درخواست شارژ #%d را ارسال کنید:", reqID))
}

func ProcessTopupApprovalAmount(c telebot.Context, amountText string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	state := bot.FSM.GetState(admin.TelegramID)
	if state == nil {
		return c.Send("فرآیند تایید شارژ فعالی وجود ندارد.")
	}
	reqID, _ := parseInt64(fmt.Sprintf("%v", state.Data["req_id"]))
	amount, err := parseFloat(amountText)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. یک عدد مثبت وارد کنید:")
	}
	minAmountStr, _ := db.GetSetting(context.Background(), "min_topup_amount")
	if minAmount, err := strconv.ParseFloat(strings.TrimSpace(minAmountStr), 64); err == nil && minAmount > 0 && amount < minAmount {
		return c.Send(fmt.Sprintf("مبلغ وارد شده کمتر از حداقل شارژ مجاز %.0f است.", minAmount))
	}

	req, err := db.ApproveTopupRequest(context.Background(), reqID, admin.TelegramID, amount)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست شارژ.")
	}
	bot.FSM.ClearState(admin.TelegramID)

	target, _ := db.GetUserByID(context.Background(), req.UserID)
	if target != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, fmt.Sprintf("کیف پول شما با موفقیت به مبلغ %.0f شارژ شد.", amount))
	}
	_ = c.Send(fmt.Sprintf("✅ درخواست شارژ شماره #%d با مبلغ %.0f تایید شد.", reqID, amount))
	return HandleAdminMenu(c)
}

func HandleAdminRejectTopup(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}
	req, err := db.RejectTopupRequest(context.Background(), reqID, c.Sender().ID)
	if err != nil {
		return c.Send("خطا در رد درخواست شارژ.")
	}
	if req == nil {
		return c.Send("درخواست شارژ یافت نشد.")
	}
	target, _ := db.GetUserByID(context.Background(), req.UserID)
	if target != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, "درخواست افزایش موجودی کیف پول شما رد شد.")
	}
	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست شارژ #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست شارژ #%d رد شد.", reqID))
}

func HandleAdminApprovePurchase(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("purchase_req:%d", reqID))
	defer unlock()

	req, err := db.GetPurchaseRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		return c.Send("درخواست خرید یافت نشد یا قبلا پردازش شده است.")
	}

	user, err := db.GetUserByID(context.Background(), req.UserID)
	if err != nil || user == nil {
		return c.Send("کاربر یافت نشد.")
	}

	adminUser := userFromContext(c)
	req, err = db.ApprovePurchaseRequest(context.Background(), reqID, adminUser.TelegramID)
	if err != nil || req == nil {
		return c.Send("خطا در تایید درخواست خرید.")
	}

	var activationErr error
	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	switch req.Type {
	case "buy":
		plan, err := db.GetPaidPlanByID(context.Background(), *req.PlanID)
		if err != nil || plan == nil {
			activationErr = fmt.Errorf("طرح خرید یافت نشد")
			break
		}
		activationErr = createSubscriptionFromApprovedRequest(user, plan, req)

	case "extend":
		sub, err := db.GetSubscriptionByID(context.Background(), int(*req.SubscriptionID))
		if err != nil || sub == nil {
			activationErr = fmt.Errorf("اشتراک یافت نشد")
			break
		}
		activationErr = extendSubscriptionFromApprovedRequest(user, sub, req)

	case "upgrade_ip":
		sub, err := db.GetSubscriptionByID(context.Background(), int(*req.SubscriptionID))
		if err != nil || sub == nil {
			activationErr = fmt.Errorf("اشتراک یافت نشد")
			break
		}
		activationErr = upgradeSubscriptionIPFromApprovedRequest(user, sub, req)
	}

	if activationErr != nil {
		log.Printf("[CRITICAL] Activation failed for purchase request #%d: %v", reqID, activationErr)
		_ = db.RollbackPurchaseRequest(context.Background(), reqID)
		return c.Send("خطا در تایید درخواست خرید: " + activationErr.Error() + ". وضعیت درخواست به حالت در انتظار برگشت داده شد.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID)})
	return c.Edit(fmt.Sprintf("✅ درخواست خرید #%d تایید و فعال شد.", reqID))
}

func HandleAdminRejectPurchase(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	req, err := db.RejectPurchaseRequest(context.Background(), reqID, c.Sender().ID)
	if err != nil {
		return c.Send("خطا در رد درخواست.")
	}
	if req == nil {
		return c.Send("درخواست یافت نشد.")
	}

	user, _ := db.GetUserByID(context.Background(), req.UserID)
	if user != nil {
		var actionLabel string
		switch req.Type {
		case "buy":
			actionLabel = "خرید سرویس"
		case "extend":
			actionLabel = "تمدید سرویس"
		case "upgrade_ip":
			actionLabel = "ارتقای تعداد کاربر همزمان"
		case "claim":
			actionLabel = "ثبت اشتراک قدیمی"
		}
		msg := fmt.Sprintf("❌ درخواست پرداخت مستقیم شما برای **%s** به مبلغ %.0f توسط ادمین رد شد. لطفا رسید واریزی خود را بررسی کنید یا با پشتیبانی در ارتباط باشید.", actionLabel, req.Price)
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست خرید #%d رد شد.", reqID))
}

func HandleAdminApproveRefund(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("refund_req:%d", reqID))
	defer unlock()

	req, err := db.GetRefundRequestByID(context.Background(), reqID)
	if err != nil || req == nil || req.Status != "pending" {
		return c.Send("درخواست استرداد یافت نشد یا قبلا پردازش شده است.")
	}

	adminUser := userFromContext(c)
	req, err = db.ApproveRefundRequest(context.Background(), reqID, adminUser.TelegramID, req.CalculatedAmount)
	if err != nil || req == nil {
		return c.Send("خطا در تایید استرداد.")
	}

	if err := db.CreditWalletBalance(context.Background(), req.UserID, float64(req.CalculatedAmount), fmt.Sprintf("Refund approved for request #%d", req.ID)); err != nil {
		_ = db.RollbackRefundRequest(context.Background(), reqID)
		return c.Send("خطا در شارژ کیف پول کاربر. وضعیت به در انتظار بازگشت داده شد.")
	}

	user, _ := db.GetUserByID(context.Background(), req.UserID)
	if user != nil {
		currency, _ := db.GetSetting(context.Background(), "currency_name")
		if currency == "" { currency = "IRR" }
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, fmt.Sprintf("✅ مبلغ %d %s بابت لغو سرویس به کیف پول شما اضافه شد.", req.CalculatedAmount, currency))
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("✅ درخواست استرداد #%d تایید شد.", reqID)})
	return c.Edit(fmt.Sprintf("✅ درخواست استرداد #%d تایید شد و مبلغ مربوطه به کیف پول کاربر واریز گردید.", reqID))
}

func HandleAdminRejectRefund(c telebot.Context) error {
	if !isConfiguredAdmin(c.Sender().ID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	reqID, err := parseInt64(callbackPayload(c))
	if err != nil || reqID == 0 {
		return c.Send("درخواست نامعتبر.")
	}

	unlock := bot.Locker.Lock(fmt.Sprintf("refund_req:%d", reqID))
	defer unlock()

	req, err := db.RejectRefundRequest(context.Background(), reqID, c.Sender().ID)
	if err != nil {
		return c.Send("خطا در رد استرداد.")
	}
	if req == nil {
		return c.Send("درخواست یافت نشد.")
	}

	user, _ := db.GetUserByID(context.Background(), req.UserID)
	if user != nil {
		_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, "❌ درخواست استرداد وجه شما بابت لغو سرویس توسط مدیریت رد شد.")
	}

	_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("❌ درخواست استرداد #%d رد شد.", reqID)})
	return c.Edit(fmt.Sprintf("❌ درخواست استرداد #%d رد شد.", reqID))
}

func createSubscriptionFromApprovedRequest(user *db.User, plan *db.PaidPlan, req *db.PurchaseRequest) error {
	if bot.XUIClient == nil {
		return fmt.Errorf("x-ui client is not initialized")
	}
	inboundIDs := validInboundIDs(plan.InboundIDs)
	if len(inboundIDs) == 0 {
		return fmt.Errorf("این طرح هیچ کانکشن معتبری ندارد")
	}

	expireMilli := -int64(req.Months * 30 * 24 * 3600 * 1000)
	totalBytes := int64(req.DataGB) * 1073741824
	subID := makeSubID()
	clientUUID := makeClientUUID()
	comment := fmt.Sprintf("created by xui-reseller-bot, %s, %s", plan.Name, userIdentifier(user))
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	adjustedIPLimit := ApplyIPLimitFactor(req.IPLimit, factor)
	client := newClientConfig(req.ClientEmail, serviceGroup(user), user.TelegramID, totalBytes, expireMilli, adjustedIPLimit, plan.Flow, subID, clientUUID, comment)

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
		ClientEmail:       req.ClientEmail,
		ClientUUID:        clientUUID,
		SubID:             subID,
		Status:            "active",
		PlanType:          db.PlanTypePaid,
		DisplayName:       req.CustomName,
		IPLimit:           adjustedIPLimit,
		ExpireTime:        &expireMilli,
		IsActive:          true,
		StartDate:         nowUTC(),
		EndDate:           time.Time{},
		TrafficLimitBytes: totalBytes,
	}
	if err := db.CreateSubscription(context.Background(), sub); err != nil {
		log.Printf("[CRITICAL] Database save failed for subscription %s: %v. Rolling back panel client.", req.ClientEmail, err)
		go func() {
			var deleteErr error
			for i := 0; i < 5; i++ {
				if deleteErr = bot.XUIClient.DeleteClient(req.ClientEmail); deleteErr == nil {
					log.Printf("Rollback successful: Deleted client %s from panel", req.ClientEmail)
					return
				}
				time.Sleep(time.Duration(1<<i) * time.Second)
			}
			log.Printf("[ALERT] CRITICAL: Failed to delete client %s from panel after 5 retries: %v. Client is orphaned on panel!", req.ClientEmail, deleteErr)
		}()
		return err
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

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}
	var dataLabel = "نامحدود"
	if plan.IsLimited {
		dataLabel = fmt.Sprintf("%d گیگابایت", req.DataGB)
	}

	detailsMsg := fmt.Sprintf("✅ پرداخت شما تایید و اشتراک با موفقیت فعال شد!\n📦 طرح: %s\n⏱️ مدت زمان: %d ماهه (پس از اولین اتصال شروع می‌شود)\n📊 سقف ترافیک: %s\n💰 هزینه پرداخت شده: %.0f %s",
		plan.Name, req.Months, dataLabel, req.Price, currency)

	if plan.UsageDescription != "" {
		detailsMsg += fmt.Sprintf("\n\nنکات استفاده:\n%s", plan.UsageDescription)
	}

	_ = sendSubscriptionResultTo(user.TelegramID, subLink, detailsMsg)
	return nil
}

func extendSubscriptionFromApprovedRequest(user *db.User, sub *db.Subscription, req *db.PurchaseRequest) error {
	oldEnd := sub.EndDate
	var oldExpireTime *int64
	if sub.ExpireTime != nil {
		val := *sub.ExpireTime
		oldExpireTime = &val
	}
	oldIsActive := sub.IsActive

	var newExpiryMilli int64
	var newExpiryLabel string

	if sub.ExpireTime != nil && *sub.ExpireTime < 0 {
		newDuration := -(*sub.ExpireTime) + int64(req.Months)*30*24*3600*1000
		newExpiryMilli = -newDuration
		sub.ExpireTime = &newExpiryMilli
		sub.EndDate = time.Time{}
		newExpiryLabel = fmt.Sprintf("شروع پس از اولین اتصال (مدت زمان %d روز)", newDuration/(24*3600*1000))
	} else {
		if sub.EndDate.Before(nowUTC()) {
			sub.EndDate = nowUTC()
		}
		sub.EndDate = sub.EndDate.AddDate(0, req.Months, 0)
		newExpiryMilli = sub.EndDate.UnixMilli()
		sub.ExpireTime = &newExpiryMilli
		newExpiryLabel = sub.EndDate.Format("2006-01-02")
	}

	sub.IsActive = true

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		return fmt.Errorf("خطا در بروزرسانی پنل: %w", err)
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.EndDate = oldEnd
		sub.ExpireTime = oldExpireTime
		sub.IsActive = oldIsActive
		return fmt.Errorf("خطا در ذخیره‌سازی دیتابیس: %w", err)
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و اشتراک **%s** به مدت %d ماه تمدید شد.\nتاریخ انقضای جدید: %s\nمبلغ پرداخت شده: %.0f %s.",
		sub.DisplayName, req.Months, newExpiryLabel, req.Price, currency)
	_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	return nil
}

func upgradeSubscriptionIPFromApprovedRequest(user *db.User, sub *db.Subscription, req *db.PurchaseRequest) error {
	oldLimit := sub.IPLimit
	factor, _ := db.GetSetting(context.Background(), "ip_limit_factor")
	sub.IPLimit = ApplyIPLimitFactor(req.IPLimit, factor)

	if err := updateXUIFromSubscription(sub); err != nil {
		sub.IPLimit = oldLimit
		return fmt.Errorf("خطا در بروزرسانی پنل: %w", err)
	}
	if err := db.UpdateSubscription(context.Background(), sub); err != nil {
		sub.IPLimit = oldLimit
		return fmt.Errorf("خطا در ذخیره‌سازی دیتابیس: %w", err)
	}

	currency, _ := db.GetSetting(context.Background(), "currency_name")
	if currency == "" {
		currency = "IRR"
	}

	msg := fmt.Sprintf("✅ پرداخت شما تایید و سقف کاربر همزمان اشتراک **%s** به %d دستگاه ارتقا یافت.\nهزینه ارتقا پرداخت شده: %.0f %s.",
		sub.DisplayName, req.IPLimit, req.Price, currency)
	_, _ = bot.Bot.Send(&telebot.User{ID: user.TelegramID}, FormatMarkdown(msg), telebot.ModeMarkdown)
	return nil
}

func ProcessManualCreditAmount(c telebot.Context, amountText string) error {
	admin := userFromContext(c)
	if admin == nil || !isConfiguredAdmin(admin.TelegramID) {
		return c.Send("شما دسترسی لازم برای این کار را ندارید.")
	}
	state := bot.FSM.GetState(admin.TelegramID)
	if state == nil {
		return c.Send("فرآیند افزایش موجودی دستی فعالی وجود ندارد.")
	}
	targetID, _ := parseInt64(fmt.Sprintf("%v", state.Data["target_user_id"]))
	amount, err := parseFloat(amountText)
	if err != nil || amount <= 0 {
		return c.Send("مبلغ نامعتبر است. یک عدد مثبت وارد کنید:")
	}
	target, err := db.GetUserByID(context.Background(), targetID)
	if err != nil || target == nil {
		return c.Send("کاربر یافت نشد.")
	}
	if err := db.CreditWalletBalance(context.Background(), target.ID, amount, "manual admin credit"); err != nil {
		return c.Send("خطا در افزایش موجودی کاربر.")
	}
	bot.FSM.ClearState(admin.TelegramID)
	_, _ = bot.Bot.Send(&telebot.User{ID: target.TelegramID}, fmt.Sprintf("کیف پول شما به مبلغ %.0f شارژ شد.", amount))
	_ = c.Send(fmt.Sprintf("✅ کیف پول کاربر #%d به مبلغ %.0f شارژ شد.", target.ID, amount))
	target, _ = db.GetUserByID(context.Background(), targetID)
	if target != nil {
		return showAdminViewUser(c, target)
	}
	return nil
}



