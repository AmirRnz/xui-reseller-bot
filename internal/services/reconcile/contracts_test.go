package reconcile

import (
	"testing"
)

func TestPayloadRoundTrips(t *testing.T) {
	// 1. PendingRefund
	subID := int64(12)
	refRec := NewPendingRefundRecord(101, &subID, 50000, "op_ref_1", "test refund", "refund reason")
	decodedRef, err := DecodePendingRefund(refRec.DesiredState, refRec.UserID, refRec.OperationKey)
	if err != nil {
		t.Fatalf("DecodePendingRefund failed: %v", err)
	}
	if decodedRef.UserID != 101 || decodedRef.Amount != 50000 || decodedRef.OperationKey != "op_ref_1" || decodedRef.Description != "test refund" {
		t.Fatalf("PendingRefund mismatch: %+v", decodedRef)
	}

	// 2. PurchaseProvisioning
	planID := 3
	qID := int64(45)
	reqID := int64(78)
	purchasePayload := &PurchaseProvisioningPayload{
		UserID:             102,
		PurchaseRequestID:  &reqID,
		QuoteID:            &qID,
		OperationKey:       "prov_op_1",
		DebitOperationKey:  "prov_op_1",
		Email:              "user@test.com",
		ExpectedUUID:       "uuid-1234",
		ExpectedSubID:      "sub-5678",
		PlanID:             &planID,
		Months:             1,
		IPLimit:            2,
		DataGB:             50,
		Price:              75000,
		RefundOperationKey: "prov_op_1:refund",
		DisplayName:        "My Test Sub",
	}
	purchaseRec := NewPurchaseProvisioningRecord(purchasePayload)
	decodedPurchase, err := DecodePurchaseProvisioning(purchaseRec.DesiredState, purchaseRec.UserID, purchaseRec.OperationKey)
	if err != nil {
		t.Fatalf("DecodePurchaseProvisioning failed: %v", err)
	}
	if decodedPurchase.UserID != 102 || *decodedPurchase.QuoteID != 45 || decodedPurchase.Email != "user@test.com" ||
		decodedPurchase.ExpectedUUID != "uuid-1234" || decodedPurchase.ExpectedSubID != "sub-5678" || decodedPurchase.Price != 75000 {
		t.Fatalf("PurchaseProvisioning mismatch: %+v", decodedPurchase)
	}

	// 3. SubscriptionUpdate
	desiredIP := 3
	desiredExp := int64(1750000000000)
	desiredActive := true
	updatePayload := &SubscriptionUpdateDbFailedPayload{
		SubscriptionID:    201,
		UserID:            103,
		ClientEmail:       "update@test.com",
		DesiredIPLimit:    &desiredIP,
		DesiredExpireTime: &desiredExp,
		DesiredIsActive:   &desiredActive,
	}
	updateRec := NewSubscriptionUpdateRecord(updatePayload)
	decodedUpdate, err := DecodeSubscriptionUpdate(updateRec.DesiredState, updateRec.SubscriptionID)
	if err != nil {
		t.Fatalf("DecodeSubscriptionUpdate failed: %v", err)
	}
	if decodedUpdate.SubscriptionID != 201 || decodedUpdate.ClientEmail != "update@test.com" ||
		*decodedUpdate.DesiredIPLimit != 3 || *decodedUpdate.DesiredExpireTime != desiredExp || !*decodedUpdate.DesiredIsActive {
		t.Fatalf("SubscriptionUpdate mismatch: %+v", decodedUpdate)
	}

	// 4. SubscriptionDelete
	delSubID := int64(301)
	delUserID := int64(104)
	deletePayload := &SubscriptionDeletePayload{
		SubscriptionID:     &delSubID,
		UserID:             &delUserID,
		ClientEmail:        "delete@test.com",
		RefundAmount:       30000,
		RefundOperationKey: "del_op_1:refund",
		Reason:             "customer request",
	}
	deleteRec := NewSubscriptionDeleteRecord(deletePayload)
	decodedDelete, err := DecodeSubscriptionDelete(deleteRec.DesiredState, deleteRec.SubscriptionID, deleteRec.UserID, deleteRec.OperationKey)
	if err != nil {
		t.Fatalf("DecodeSubscriptionDelete failed: %v", err)
	}
	if *decodedDelete.SubscriptionID != 301 || *decodedDelete.UserID != 104 || decodedDelete.RefundAmount != 30000 || decodedDelete.ClientEmail != "delete@test.com" {
		t.Fatalf("SubscriptionDelete mismatch: %+v", decodedDelete)
	}

	// 5. DirectPaymentProvisioning
	directPayload := &DirectPaymentProvisioningPayload{
		PurchaseRequestID: 401,
		UserID:            105,
		QuoteID:           &qID,
		OperationKey:      "direct_op_1",
		ClientEmail:       "direct@test.com",
		ExpectedUUID:      "uuid-direct-1",
		ExpectedSubID:     "sub-direct-1",
		Months:            2,
		IPLimit:           1,
		DataGB:            30,
		CustomName:        "Direct Sub",
	}
	directRec := NewDirectPaymentProvisioningRecord(directPayload)
	decodedDirect, err := DecodeDirectPaymentProvisioning(directRec.DesiredState, directRec.PurchaseRequestID, directRec.UserID, directRec.OperationKey)
	if err != nil {
		t.Fatalf("DecodeDirectPaymentProvisioning failed: %v", err)
	}
	if decodedDirect.PurchaseRequestID != 401 || decodedDirect.UserID != 105 || decodedDirect.ExpectedUUID != "uuid-direct-1" ||
		decodedDirect.ExpectedSubID != "sub-direct-1" || decodedDirect.ClientEmail != "direct@test.com" {
		t.Fatalf("DirectPaymentProvisioning mismatch: %+v", decodedDirect)
	}

	// 6. SubscriptionRemoteMissing
	missingRec := NewSubscriptionRemoteMissingRecord(501, 106, "missing@test.com", "remote client absent on panel during sync")
	decodedMissing, err := DecodeSubscriptionRemoteMissing(missingRec.DesiredState, missingRec.SubscriptionID, missingRec.UserID)
	if err != nil {
		t.Fatalf("DecodeSubscriptionRemoteMissing failed: %v", err)
	}
	if decodedMissing.SubscriptionID != 501 || decodedMissing.UserID != 106 || decodedMissing.ClientEmail != "missing@test.com" {
		t.Fatalf("SubscriptionRemoteMissing mismatch: %+v", decodedMissing)
	}
}
