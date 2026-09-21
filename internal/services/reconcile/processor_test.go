package reconcile

import (
	"context"
	"errors"
	"testing"

	"xui-reseller-bot/internal/db"
	"xui-reseller-bot/internal/xui"
)

type mockXUI struct {
	client       *xui.XUIClientInfo
	err          error
	delErr       error
	updateRes    xui.WriteResult
	updatedState *xui.ClientConfig
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

func (m *mockXUI) AddClientResult(req xui.AddClientRequest) xui.WriteResult {
	return xui.WriteResult{Outcome: xui.WriteSucceeded}
}

func (m *mockXUI) UpdateClientResult(email string, client xui.ClientConfig) xui.WriteResult {
	m.updatedState = &client
	if m.updateRes.Outcome != "" {
		return m.updateRes
	}
	return xui.WriteResult{Outcome: xui.WriteSucceeded}
}

func TestClassifyRemoteClient(t *testing.T) {
	// 1. Explicit not found
	if s := ClassifyRemoteClient(nil, &xui.NotFoundError{StatusCode: 404}); s != RemoteConfirmedAbsent {
		t.Fatalf("expected RemoteConfirmedAbsent, got %v", s)
	}

	// 2. Timeout error with nil client
	if s := ClassifyRemoteClient(nil, errors.New("timeout connecting to XUI")); s != RemotePresenceUnknown {
		t.Fatalf("expected RemotePresenceUnknown for timeout, got %v", s)
	}

	// 3. Nil client with nil error (anomalous)
	if s := ClassifyRemoteClient(nil, nil); s != RemotePresenceUnknown {
		t.Fatalf("expected RemotePresenceUnknown for anomalous nil client, got %v", s)
	}

	// 4. Client present with nil error
	client := &xui.XUIClientInfo{Email: "test@example.com"}
	if s := ClassifyRemoteClient(client, nil); s != RemoteConfirmedPresent {
		t.Fatalf("expected RemoteConfirmedPresent, got %v", s)
	}
}

func TestHandlePendingRefund_Success(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:key",
			"description":          "test refund",
		},
	}

	credited := false
	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			if uID != userID || amount != 50000 || key != "op_test_refund:key" {
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

func TestHandlePendingRefund_RefundFails_ReturnsError(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund_fail",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:fail_key",
		},
	}

	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			return errors.New("db connection timeout during refund")
		},
	}

	_, err := p.handlePendingRefund(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error when refund fails")
	}
}

func TestHandlePendingRefund_AlreadyApplied_Succeeds(t *testing.T) {
	userID := int64(123)
	rec := &db.ReconciliationRecord{
		ID:           1,
		OperationKey: "op_test_refund_dup",
		Kind:         KindPendingRefund,
		UserID:       &userID,
		DesiredState: map[string]any{
			"amount":               50000,
			"refund_operation_key": "op_test_refund:dup_key",
		},
	}

	p := &Processor{
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			return db.ErrWalletOperationAlreadyApplied
		},
	}

	res, err := p.handlePendingRefund(context.Background(), rec)
	if err != nil {
		t.Fatalf("expected safe success for already-applied refund, got: %v", err)
	}
	if res == "" {
		t.Fatal("expected resolution string")
	}
}

func TestHandleDeleteReconciliation_Timeout_NoDeletionOrRefund(t *testing.T) {
	userID := int64(999)
	subID := int64(888)
	rec := &db.ReconciliationRecord{
		ID:             10,
		SubscriptionID: &subID,
		UserID:         &userID,
		OperationKey:   "op_del_timeout",
		Kind:           KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "timeout_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_timeout:refund",
		},
	}

	refundCalled := false
	p := &Processor{
		XUI: &mockXUI{
			err: errors.New("timeout from XUI"),
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			refundCalled = true
			return nil
		},
	}

	_, err := p.handleDeleteReconciliation(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error when XUI check times out")
	}
	if refundCalled {
		t.Fatal("CRITICAL: refund must NOT be executed when XUI check times out!")
	}
}

func TestHandleDeleteReconciliation_NotFound_Refunds(t *testing.T) {
	userID := int64(999)
	rec := &db.ReconciliationRecord{
		ID:           11,
		UserID:       &userID,
		OperationKey: "op_del_absent",
		Kind:         KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "absent_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_absent:refund",
		},
	}

	refundCalled := false
	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			refundCalled = true
			return nil
		},
	}

	res, err := p.handleDeleteReconciliation(context.Background(), rec)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !refundCalled {
		t.Fatal("expected refund when remote client is confirmed absent")
	}
	if res == "" {
		t.Fatal("expected resolution")
	}
}

func TestHandleDeleteReconciliation_RefundFails_ReturnsError(t *testing.T) {
	userID := int64(999)
	rec := &db.ReconciliationRecord{
		ID:           12,
		UserID:       &userID,
		OperationKey: "op_del_absent_fail_refund",
		Kind:         KindSubscriptionDeleteUnknown,
		DesiredState: map[string]any{
			"client_email":         "absent_delete@example.com",
			"refund_amount":        100000,
			"refund_operation_key": "op_del_absent:refund_fail",
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			return errors.New("wallet credit db failure")
		},
	}

	_, err := p.handleDeleteReconciliation(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error when refund fails during deletion reconciliation")
	}
}

func TestHandlePurchaseReconciliation_RemoteAbsent_Refunds(t *testing.T) {
	userID := int64(456)
	rec := &db.ReconciliationRecord{
		ID:           2,
		OperationKey: "op_unknown_create",
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email":                "absent@example.com",
			"price":                60000,
			"refund_operation_key": "op_unknown_create:refund",
		},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			client: nil, // 404
		},
		CreditFn: func(ctx context.Context, uID int64, amount float64, desc, key string) error {
			if uID != userID || amount != 60000 {
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
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email": "timeout@example.com",
			"price": 70000,
		},
	}

	refunded := false
	p := &Processor{
		XUI: &mockXUI{
			err: errors.New("timeout connecting to master"),
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

func TestHandlePurchaseReconciliation_MismatchedUUID_MovesToManualReview(t *testing.T) {
	userID := int64(789)
	rec := &db.ReconciliationRecord{
		ID:           4,
		OperationKey: "op_uuid_mismatch",
		Kind:         KindPurchaseProvisioningUnknown,
		UserID:       &userID,
		DesiredState: map[string]any{
			"email":         "mismatch@example.com",
			"expected_uuid": "expected-uuid-1111",
			"price":         70000,
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: &xui.XUIClientInfo{
				Email: "mismatch@example.com",
				UUID:  "different-uuid-2222",
			},
		},
	}

	res, err := p.handlePurchaseReconciliation(context.Background(), rec)
	if err != nil {
		t.Fatalf("expected manual review resolution, got error: %v", err)
	}
	if res != "manual review: remote client identity mismatch" {
		t.Fatalf("expected manual review for identity mismatch, got: %q", res)
	}
}

func TestHandleUpdateReconciliation_DivergentState_MovesToManualReview(t *testing.T) {
	newIP := 5
	rec := &db.ReconciliationRecord{
		ID:             5,
		SubscriptionID: nil,
		OperationKey:   "op_update_invalid",
		Kind:           KindSubscriptionUpdateDbFailed,
		DesiredState: map[string]any{
			"subscription_id":  55,
			"client_email":     "update@example.com",
			"desired_ip_limit": newIP,
		},
	}

	p := &Processor{
		XUI: &mockXUI{
			client: &xui.XUIClientInfo{
				Email:   "update@example.com",
				LimitIP: 2,
			},
		},
	}

	_, err := p.handleUpdateReconciliation(context.Background(), rec)
	if err == nil {
		t.Fatal("expected error when DB subscription lookup fails")
	}
}
