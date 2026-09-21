package reconcile

import (
	"context"
	"errors"
	"testing"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type mockXUI struct {
	client *xui.XUIClientInfo
	err    error
	delErr error
}

func (m *mockXUI) GetClientByEmail(email string) (*xui.XUIClientInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.client == nil {
		return nil, &xui.NotFoundError{StatusCode: 404, Message: "not found"}
	}
	return m.client, nil
}

func (m *mockXUI) DeleteClient(email string) error {
	return m.delErr
}

func (m *mockXUI) AddClient(req xui.AddClientRequest) error {
	return nil
}

func TestHandlePendingRefund_Success(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund",
		Kind:         "pending_refund",
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000.0,
			"refund_operation_key": "op_test_refund:key",
			"description":          "test refund",
		},
	}

	credited := false
	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			if uID != userID || amount != 50000.0 || key != "op_test_refund:key" {
				t.Fatalf("unexpected credit args: uid=%d, amount=%f, key=%s", uID, amount, key)
			}
			credited = true
			return nil
		},
	}

	res, err := p.handlePendingRefund(context.Background(), rec)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !credited {
		t.Fatal("expected CreditFn to be called")
	}
	if res == "" {
		t.Fatal("expected non-empty resolution")
	}
}

func TestHandlePurchaseReconciliation_RemoteAbsent_Refunds(t *testing.T) {
	userID := int64(456)
	rec := &db.ReconciliationRecord{
		ID:           2,
		OperationKey: "op_unknown_create",
		Kind:         "purchase_provisioning_unknown",
		UserID:       &userID,
		DesiredState: map[string]any{
			"email":                "absent@example.com",
			"price":                60000.0,
			"refund_operation_key": "op_unknown_create:refund",
		},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			if uID != userID || amount != 60000.0 {
				t.Fatalf("unexpected refund args: uid=%d, amount=%f", uID, amount)
			}
			refunded = true
			return nil
		},
	}

	res, err := p.handlePurchaseReconciliation(context.Background(), rec)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !refunded {
		t.Fatal("expected refund for absent remote client")
	}
	if res == "" {
		t.Fatal("expected resolution string")
	}
}

func TestHandlePurchaseReconciliation_Inconclusive_NoRefund(t *testing.T) {
	userID := int64(789)
	rec := &db.ReconciliationRecord{
		ID:           3,
		OperationKey: "op_timeout",
		Kind:         "purchase_provisioning_unknown",
		UserID:       &userID,
		DesiredState: map[string]any{
			"email": "timeout@example.com",
			"price": 70000.0,
		},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			err: &xui.WriteError{Outcome: xui.WriteUnknown, Err: errors.New("timeout connecting to master")},
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			refunded = true
			return nil
		},
	}

	_, err := p.handlePurchaseReconciliation(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error on inconclusive check")
	}
	if refunded {
		t.Fatal("CRITICAL: refund issued despite ambiguous XUI outcome!")
	}
}
