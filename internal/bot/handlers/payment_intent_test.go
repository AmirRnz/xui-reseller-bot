package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v3"
	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/config"
	"xui-reseller-bot/internal/db"
)

type paymentIntentTestContext struct {
	telebot.Context
	text   string
	values map[string]interface{}
	sent   interface{}
	opts   []interface{}
}

func (c *paymentIntentTestContext) Text() string                { return c.text }
func (c *paymentIntentTestContext) Get(key string) interface{}  { return c.values[key] }
func (c *paymentIntentTestContext) Callback() *telebot.Callback { return nil }
func (c *paymentIntentTestContext) Send(what interface{}, opts ...interface{}) error {
	c.sent = what
	c.opts = opts
	return nil
}

func TestCancelCommandOffersRecoveryAndKeepsActiveIntent(t *testing.T) {
	testURL := os.Getenv("TEST_DATABASE_URL")
	if testURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	cfg := config.DatabaseConfig{URL: testURL}
	if err := db.Connect(ctx, &cfg); err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(func() {
		if db.Pool != nil {
			db.Pool.Close()
			db.Pool = nil
		}
	})
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	tgID := time.Now().UnixNano()
	var userID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status) VALUES ($1, $2, 'approved') RETURNING id`, tgID, fmt.Sprintf("cancel_cmd_%d", tgID)).Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_intents WHERE user_id = $1`, userID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM bot_users WHERE id = $1`, userID)
	}()
	intent, err := db.CreatePaymentIntent(ctx, &db.PaymentIntent{
		UserID: userID, IntentToken: fmt.Sprintf("cancel_cmd_%d", tgID), ActionType: "buy", AmountToman: 125000,
		Status: db.IntentStatusAwaitingReceipt,
	})
	if err != nil {
		t.Fatalf("create active intent: %v", err)
	}
	user := &db.User{ID: userID, TelegramID: tgID}
	bot.FSM.SetState(tgID, "awaiting_purchase_receipt", map[string]interface{}{"intent_id": intent.ID})
	c := &paymentIntentTestContext{text: "/cancel", values: map[string]interface{}{"user": user}}
	if err := HandleText(c); err != nil {
		t.Fatalf("handle /cancel: %v", err)
	}
	if _, err := db.GetLatestActivePaymentIntent(ctx, userID); err != nil {
		t.Fatalf("/cancel changed the durable payment intent: %v", err)
	}
	if state := bot.FSM.GetState(tgID); state != nil {
		t.Fatalf("/cancel did not clear transient FSM state: %+v", state)
	}
	if !strings.Contains(fmt.Sprint(c.sent), "لغو") || !strings.Contains(fmt.Sprint(c.sent), "رسید") {
		t.Fatalf("/cancel did not show durable recovery choices: %v", c.sent)
	}
	if len(c.opts) == 0 {
		t.Fatal("/cancel recovery response is missing its action buttons")
	}
}
