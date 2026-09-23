package handlers

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/bot/persian"
	"xui-reseller-bot/internal/db"
)

const unpaidCheckoutCancellationReason = "customer_confirmed_not_paid"

func showPaymentIntentRecovery(c telebot.Context, intent *db.PaymentIntent) error {
	if intent == nil {
		return c.Send("پرداخت ناتمامی پیدا نشد.")
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("ادامه / ارسال رسید", "payment_intent_resume", fmt.Sprintf("%d", intent.ID))),
		menu.Row(menu.Data("لغو پرداختِ انجام‌نشده", "payment_intent_cancel_prompt", fmt.Sprintf("%d", intent.ID))),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"یک پرداخت ناتمام دارید (شناسه #%d، مبلغ %s تومان).\n\nاگر وجه را واریز کرده‌اید، «ادامه / ارسال رسید» را بزنید و تصویر رسید را بفرستید. گزینه لغو را فقط زمانی انتخاب کنید که پرداخت را انجام نداده‌اید. سابقه پرداخت لغوشده برای بررسی احتمالی ادمین نگه‌داری می‌شود.",
		intent.ID, persian.FormatMoney(intent.AmountToman)), menu)
}

func HandlePaymentIntentResume(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	intent, err := activePaymentIntentForAction(c, user)
	if err != nil || intent == nil {
		return maybeEditOrSend(c, "پرداخت فعالی برای ادامه وجود ندارد.")
	}
	step := "awaiting_purchase_receipt"
	if intent.ActionType == "topup" {
		step = "awaiting_receipt"
	}
	bot.FSM.SetState(user.TelegramID, step, map[string]interface{}{
		"intent_id":       intent.ID,
		"intent_token":    intent.IntentToken,
		"operation_token": intent.IntentToken,
	})
	menu := &telebot.ReplyMarkup{}
	menu.Inline(menu.Row(menu.Data("لغو پرداختِ انجام‌نشده", "payment_intent_cancel_prompt", fmt.Sprintf("%d", intent.ID))))
	return maybeEditOrSend(c, fmt.Sprintf(
		"پرداخت #%d با مبلغ %s تومان ادامه دارد. اگر پرداخت را انجام داده‌اید، تصویر رسید را همینجا بفرستید. اگر هنوز واریز نکرده‌اید و قصد ادامه ندارید، فقط از گزینه لغو استفاده کنید.",
		intent.ID, persian.FormatMoney(intent.AmountToman)), menu)
}

func HandlePaymentIntentCancelPrompt(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	intent, err := activePaymentIntentForAction(c, user)
	if err != nil || intent == nil {
		return maybeEditOrSend(c, "پرداخت فعالی برای لغو وجود ندارد.")
	}
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("تأیید: پرداخت نکرده‌ام", "payment_intent_cancel_confirm", fmt.Sprintf("%d", intent.ID))),
		menu.Row(menu.Data("بازگشت / ارسال رسید", "payment_intent_resume", fmt.Sprintf("%d", intent.ID))),
	)
	return maybeEditOrSend(c, fmt.Sprintf(
		"لغو فقط برای پرداختی است که هنوز انجام نشده است. اگر وجه را واریز کرده‌اید، لغو نکنید و رسید را ارسال کنید. با تأیید، checkout با مبلغ %s تومان به‌صورت دائمی با وضعیت لغوشده ثبت می‌شود.",
		persian.FormatMoney(intent.AmountToman)), menu)
}

func HandlePaymentIntentCancelConfirm(c telebot.Context) error {
	user := userFromContext(c)
	if user == nil {
		return c.Send("خطا در بارگذاری حساب کاربری.")
	}
	intent, err := activePaymentIntentForAction(c, user)
	if err != nil || intent == nil {
		return maybeEditOrSend(c, "پرداخت فعالی برای لغو وجود ندارد. ممکن است رسید آن قبلاً ثبت شده باشد.")
	}
	if err := db.CancelPaymentIntent(context.Background(), intent.ID, user.ID, unpaidCheckoutCancellationReason); err != nil {
		return maybeEditOrSend(c, "این پرداخت دیگر قابل لغو نیست یا قبلاً رسید آن ثبت شده است. برای بررسی با پشتیبانی تماس بگیرید.")
	}
	bot.FSM.ClearState(user.TelegramID)
	return maybeEditOrSend(c, "پرداخت ناتمام لغو شد و سابقه آن برای بررسی احتمالی بعدی نگه‌داری شد. اکنون می‌توانید checkout تازه‌ای آغاز کنید.")
}

func activePaymentIntentForAction(c telebot.Context, user *db.User) (*db.PaymentIntent, error) {
	parts := callbackParts(c)
	if len(parts) > 0 {
		intentID, err := parseInt64(parts[0])
		if err != nil {
			return nil, err
		}
		intent, err := db.GetPaymentIntentByID(context.Background(), intentID)
		if err != nil {
			return nil, err
		}
		if intent.UserID != user.ID || intent.Status != db.IntentStatusAwaitingReceipt {
			return nil, db.ErrPaymentIntentNotCancellable
		}
		return intent, nil
	}
	return db.GetLatestActivePaymentIntent(context.Background(), user.ID)
}

func paymentIntentCreateFailure(c telebot.Context, user *db.User, fallback string) error {
	if user != nil {
		if intent, err := db.GetLatestActivePaymentIntent(context.Background(), user.ID); err == nil && intent != nil {
			return showPaymentIntentRecovery(c, intent)
		}
	}
	return maybeEditOrSend(c, fallback)
}

func paymentIntentCreateFailureForError(c telebot.Context, user *db.User, fallback string, cause error) error {
	if errors.Is(cause, db.ErrSubscriptionMutationInProgress) {
		return maybeEditOrSend(c, "پرداخت یا تغییر قبلی این سرویس هنوز در حال بررسی یا همگام‌سازی است. لطفاً تا پایان همان درخواست صبر کنید.")
	}
	return paymentIntentCreateFailure(c, user, fallback)
}
