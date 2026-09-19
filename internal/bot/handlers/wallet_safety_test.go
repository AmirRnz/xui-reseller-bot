package handlers

import (
	"context"
	"errors"
	"testing"

	"xui-reseller-bot/internal/bot"
	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"

	"gopkg.in/telebot.v3"
)

func TestWalletRemoteOutcomeSafety(t *testing.T) {
	unknown := &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("verification unavailable")}
	definitive := &xui.WriteError{Outcome: xui.WriteDefinitiveFailure, Err: errors.New("rejected")}
	tests := []struct {
		name       string
		err        error
		wantRefund bool
		wantRecon  bool
	}{
		{name: "success", err: nil},
		{name: "unknown", err: unknown, wantRecon: true},
		{name: "definitive failure", err: definitive, wantRefund: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := walletRemoteRefundAllowed(tt.err); got != tt.wantRefund {
				t.Fatalf("refund allowed=%t, want %t", got, tt.wantRefund)
			}
			if got := walletRemoteReconciliationRequired(tt.err); got != tt.wantRecon {
				t.Fatalf("reconciliation required=%t, want %t", got, tt.wantRecon)
			}
		})
	}
}

func TestNilXUIClientReturnsExplicitErrorAndTriggersRefund(t *testing.T) {
	bot.XUIClient = nil
	sub := &db.Subscription{ClientEmail: "test@example.com"}
	err := updateXUIFromSubscription(sub)
	if err == nil {
		t.Fatal("updateXUIFromSubscription with nil XUIClient must return an error, got nil")
	}
	if !errors.Is(err, ErrXUIClientUnavailable) {
		t.Fatalf("expected ErrXUIClientUnavailable, got %v", err)
	}

	renameErr := updateXUIRename("old@example.com", sub)
	if renameErr == nil {
		t.Fatal("updateXUIRename with nil XUIClient must return an error, got nil")
	}
	if !errors.Is(renameErr, ErrXUIClientUnavailable) {
		t.Fatalf("expected ErrXUIClientUnavailable, got %v", renameErr)
	}

	if !walletRemoteRefundAllowed(err) {
		t.Fatal("unavailable XUI client must be classified as a definitive remote failure allowing refund")
	}
	if walletRemoteReconciliationRequired(err) {
		t.Fatal("unavailable XUI client is a known failure and must not be marked unknown")
	}
}

func TestRemoteCreateSuccessDbFailureCompensation(t *testing.T) {
	user := &db.User{ID: 100, TelegramID: 1000}
	plan := &db.PaidPlan{ID: 1, Name: "Test Plan"}
	client := xui.ClientConfig{Email: "user@example.com", ID: "uuid-1", SubID: "sub-1"}
	inbounds := []int{1, 2}
	dbErr := errors.New("db insert duplicate key error")

	// a. remote create succeeds -> DB insert fails -> remote delete confirmed absent -> exactly one refund allowed
	t.Run("Scenario A: delete confirmed absent -> exactly one refund allowed", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_a", dbErr,
			func(email string) error {
				deleteCalls++
				return nil // delete succeeded cleanly
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, nil
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) (bool, error) {
				refundCalls++
				if refKey != "op_a:refund" {
					t.Fatalf("unexpected refund key: %s", refKey)
				}
				return true, nil
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				return nil
			},
		)

		if res.Outcome != CompensationRefunded || !res.Refunded {
			t.Fatalf("expected refunded outcome, got %+v", res)
		}
		if deleteCalls != 1 {
			t.Fatalf("expected 1 delete call, got %d", deleteCalls)
		}
		if verifyCalls != 0 {
			t.Fatalf("expected 0 verify calls on clean delete, got %d", verifyCalls)
		}
		if refundCalls != 1 {
			t.Fatalf("expected exactly one refund call, got %d", refundCalls)
		}
		if reconCalls != 0 {
			t.Fatalf("expected 0 reconciliation calls on clean refund, got %d", reconCalls)
		}
	})

	// b. remote create succeeds -> DB insert fails -> delete times out -> verification unavailable -> no refund, reconciliation required
	t.Run("Scenario B: delete timeout -> verify unavailable -> no refund, reconciliation required", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0
		var savedRec *db.ReconciliationRecord

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_b", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, errors.New("verify endpoint unavailable")
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) (bool, error) {
				refundCalls++
				return true, nil
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				savedRec = rec
				return nil
			},
		)

		if res.Outcome != CompensationReconciliationRequired || res.Refunded {
			t.Fatalf("expected reconciliation_required without refund, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 0 {
			t.Fatalf("refund must NEVER be issued when delete outcome is unknown, got %d calls", refundCalls)
		}
		if reconCalls != 1 || savedRec == nil {
			t.Fatalf("expected 1 reconciliation record persisted, got %d", reconCalls)
		}
		if savedRec.DesiredState["action"] != "confirm_delete_and_refund" {
			t.Fatalf("expected confirm_delete_and_refund action, got %v", savedRec.DesiredState["action"])
		}
		if savedRec.DesiredState["client_id"] != client.ID || savedRec.DesiredState["email"] != client.Email {
			t.Fatalf("reconciliation record must retain exact client identity: %+v", savedRec.DesiredState)
		}
	})

	// c. remote create succeeds -> DB insert fails -> delete timeout -> GET confirms absent -> exactly one refund
	t.Run("Scenario C: delete timeout -> GET confirms absent -> exactly one refund", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_c", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return nil, xui.ErrNotFound // confirmed absent on readback
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) (bool, error) {
				refundCalls++
				if refKey != "op_c:refund" {
					t.Fatalf("unexpected refund key: %s", refKey)
				}
				return true, nil
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				return nil
			},
		)

		if res.Outcome != CompensationRefunded || !res.Refunded {
			t.Fatalf("expected refunded outcome after GET confirmed absent, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 1 {
			t.Fatalf("expected exactly 1 refund call, got %d", refundCalls)
		}
		if reconCalls != 0 {
			t.Fatalf("expected 0 reconciliation calls on confirmed absence refund, got %d", reconCalls)
		}
	})

	// d. remote create succeeds -> DB insert fails -> GET confirms client still present -> no refund, reconciliation required
	t.Run("Scenario D: GET confirms client still present -> no refund, reconciliation required", func(t *testing.T) {
		refundCalls := 0
		reconCalls := 0
		deleteCalls := 0
		verifyCalls := 0
		var savedRec *db.ReconciliationRecord

		res := compensateRemoteCreateDbFailure(
			nil, user, plan, client, inbounds, "display", 500, "op_d", dbErr,
			func(email string) error {
				deleteCalls++
				return &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("delete timeout")}
			},
			func(email string) (*xui.XUIClientInfo, error) {
				verifyCalls++
				return &xui.XUIClientInfo{Email: email}, nil // client still present
			},
			func(ctx context.Context, userID int64, amount float64, desc, origKey, refKey string, subID *int64, extra map[string]any) (bool, error) {
				refundCalls++
				return true, nil
			},
			func(ctx context.Context, rec *db.ReconciliationRecord) error {
				reconCalls++
				savedRec = rec
				return nil
			},
		)

		if res.Outcome != CompensationClientStillPresent || res.Refunded {
			t.Fatalf("expected client_still_present without refund, got %+v", res)
		}
		if deleteCalls != 1 || verifyCalls != 1 {
			t.Fatalf("expected 1 delete and 1 verify call, got %d, %d", deleteCalls, verifyCalls)
		}
		if refundCalls != 0 {
			t.Fatalf("must NEVER refund if client is still present on panel, got %d calls", refundCalls)
		}
		if reconCalls != 1 || savedRec == nil {
			t.Fatalf("expected 1 reconciliation record persisted, got %d", reconCalls)
		}
		if savedRec.DesiredState["action"] != "adopt_subscription_or_delete" {
			t.Fatalf("expected adopt_subscription_or_delete action, got %v", savedRec.DesiredState["action"])
		}
		if savedRec.DesiredState["client_id"] != client.ID {
			t.Fatalf("reconciliation record must preserve client identity: %+v", savedRec.DesiredState)
		}
	})
}

func TestSafeRefundWalletOutcome(t *testing.T) {
	// When DB pool is not initialized, CreditWalletBalanceWithKey will fail.
	// safeRefundWallet must return false and NOT claim the money was refunded.
	ctx := context.Background()
	refunded, err := safeRefundWallet(ctx, 100, 500, "test refund", "orig_key", "ref_key", nil, nil)
	if refunded {
		t.Fatal("safeRefundWallet must not report success when database write fails")
	}
	if err == nil {
		t.Fatal("safeRefundWallet must return the database failure error")
	}
}

type mockDeleteContext struct {
	telebot.Context
	user     *db.User
	sentText string
}

func (m *mockDeleteContext) Get(key string) any {
	if key == "db_user" {
		return m.user
	}
	return nil
}

func (m *mockDeleteContext) Sender() *telebot.User {
	if m.user != nil {
		return &telebot.User{ID: m.user.TelegramID}
	}
	return &telebot.User{ID: 12345}
}

func (m *mockDeleteContext) Send(what any, opts ...any) error {
	if s, ok := what.(string); ok {
		m.sentText = s
	}
	return nil
}

func TestNilXUIClientBlocksCancellationAndRefund(t *testing.T) {
	bot.XUIClient = nil
	user := &db.User{ID: 999, TelegramID: 999999}
	subID := 12345
	bot.FSM.SetState(user.TelegramID, "awaiting_delete_sub_confirm", map[string]interface{}{
		"sub_id":        "12345",
		"refund_amount": "50000",
	})
	defer bot.FSM.ClearState(user.TelegramID)

	mockCtx := &mockDeleteContext{user: user}

	// If DB is connected, insert sub and test; otherwise test that HandleDeleteSubscription
	// returns without cancelling or refunding when XUI is nil.
	if db.Pool != nil {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, subID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM refund_requests WHERE subscription_id = $1`, subID)
		_, _ = db.Pool.Exec(ctx, `
			INSERT INTO subscriptions (id, user_id, client_email, client_uuid, sub_id, status, plan_type, display_name, ip_limit, is_active, start_date)
			VALUES ($1, $2, 'nil_xui_test@example.com', 'uuid-12345', 'sub-12345', 'active', 'paid', 'Test Sub', 1, true, NOW())
		`, subID, user.ID)
		defer func() {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, subID)
			_, _ = db.Pool.Exec(ctx, `DELETE FROM refund_requests WHERE subscription_id = $1`, subID)
		}()

		err := HandleDeleteSubscription(mockCtx)
		if err != nil {
			t.Fatalf("HandleDeleteSubscription returned unexpected error: %v", err)
		}

		// Verify user received failure message
		if mockCtx.sentText == "" {
			t.Fatal("expected failure message sent to user, got empty string")
		}

		// Invariant 1: Local subscription MUST NOT be cancelled
		sub, err := db.GetSubscriptionByID(ctx, subID)
		if err != nil || sub == nil {
			t.Fatalf("subscription must exist: %v", err)
		}
		if sub.Status != "active" {
			t.Fatalf("subscription status must remain active, got %s", sub.Status)
		}

		// Invariant 2: No refund request must be created
		var refundCount int
		err = db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM refund_requests WHERE subscription_id = $1`, subID).Scan(&refundCount)
		if err != nil {
			t.Fatalf("failed to query refund_requests: %v", err)
		}
		if refundCount != 0 {
			t.Fatalf("expected 0 refund requests, got %d", refundCount)
		}
	} else {
		// When DB is not connected, HandleDeleteSubscription still must not panic or proceed
		err := HandleDeleteSubscription(mockCtx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
