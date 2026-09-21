package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Supported reconciliation kinds
const (
	KindPendingRefund                    = "pending_refund"
	KindPurchaseProvisioningUnknown      = "purchase_provisioning_unknown"
	KindPurchaseRemoteCreatedDbFailed    = "purchase_remote_created_db_failed"
	KindSubscriptionUpdateDbFailed       = "subscription_update_db_failed"
	KindSubscriptionDeleteUnknown        = "subscription_delete_unknown"
	KindSubscriptionCancellationDbFailed = "subscription_cancellation_db_failure"
	KindDirectPaymentProvisioningRetry   = "direct_payment_provisioning_retry"
	KindSubscriptionRemoteMissing        = "subscription_remote_missing"
)

// PendingRefundPayload specifies the strict contract for pending wallet refunds.
type PendingRefundPayload struct {
	UserID       int64  `json:"user_id"`
	Amount       int64  `json:"amount"`
	OperationKey string `json:"operation_key"`
	Description  string `json:"description"`
	Reason       string `json:"reason,omitempty"`
}

func (p *PendingRefundPayload) Validate() error {
	if p.UserID <= 0 {
		return errors.New("user_id must be greater than 0")
	}
	if p.Amount <= 0 {
		return errors.New("amount must be greater than 0")
	}
	if strings.TrimSpace(p.OperationKey) == "" {
		return errors.New("operation_key is required")
	}
	return nil
}

// PurchaseProvisioningPayload defines the contract for resolving uncertain purchases or remote-create DB failures.
type PurchaseProvisioningPayload struct {
	UserID             int64  `json:"user_id"`
	PurchaseRequestID  *int64 `json:"purchase_request_id,omitempty"`
	QuoteID            *int64 `json:"quote_id,omitempty"`
	OperationKey       string `json:"operation_key"`
	Email              string `json:"email"`
	ExpectedUUID       string `json:"expected_uuid,omitempty"`
	ExpectedSubID      string `json:"expected_sub_id,omitempty"`
	PlanID             *int   `json:"plan_id,omitempty"`
	InboundIDs         []int  `json:"inbound_ids,omitempty"`
	Months             int    `json:"months,omitempty"`
	IPLimit            int    `json:"ip_limit,omitempty"`
	DataGB             int    `json:"data_gb,omitempty"`
	Price              int64  `json:"price"`
	RefundOperationKey string `json:"refund_operation_key,omitempty"`
	DisplayName        string `json:"display_name,omitempty"`
}

func (p *PurchaseProvisioningPayload) Validate() error {
	if p.UserID <= 0 {
		return errors.New("user_id must be greater than 0")
	}
	if strings.TrimSpace(p.Email) == "" {
		return errors.New("email is required")
	}
	if strings.TrimSpace(p.OperationKey) == "" {
		return errors.New("operation_key is required")
	}
	return nil
}

// SubscriptionUpdateDbFailedPayload defines the contract for reconciling mutations where XUI state was changed but DB update was uncertain.
type SubscriptionUpdateDbFailedPayload struct {
	SubscriptionID     int64  `json:"subscription_id"`
	UserID             int64  `json:"user_id"`
	ClientEmail        string `json:"client_email"`
	DesiredIPLimit     *int   `json:"desired_ip_limit,omitempty"`
	DesiredExpireTime  *int64 `json:"desired_expire_time,omitempty"`
	DesiredIsActive    *bool  `json:"desired_is_active,omitempty"`
	PreviousIPLimit    int    `json:"previous_ip_limit,omitempty"`
	PreviousExpireTime int64  `json:"previous_expire_time,omitempty"`
	PreviousIsActive   bool   `json:"previous_is_active,omitempty"`
}

func (p *SubscriptionUpdateDbFailedPayload) Validate() error {
	if p.SubscriptionID <= 0 {
		return errors.New("subscription_id must be greater than 0")
	}
	if strings.TrimSpace(p.ClientEmail) == "" {
		return errors.New("client_email is required")
	}
	if p.DesiredIPLimit == nil && p.DesiredExpireTime == nil && p.DesiredIsActive == nil {
		return errors.New("at least one desired field must be set")
	}
	return nil
}

// SubscriptionDeletePayload defines the contract for uncertain deletion or cancellation.
type SubscriptionDeletePayload struct {
	SubscriptionID     *int64 `json:"subscription_id,omitempty"`
	UserID             *int64 `json:"user_id,omitempty"`
	ClientEmail        string `json:"client_email"`
	RefundAmount       int64  `json:"refund_amount,omitempty"`
	RefundOperationKey string `json:"refund_operation_key,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

func (p *SubscriptionDeletePayload) Validate() error {
	if strings.TrimSpace(p.ClientEmail) == "" {
		return errors.New("client_email is required")
	}
	if p.RefundAmount > 0 && (p.UserID == nil || *p.UserID <= 0) {
		return errors.New("user_id is required when refund_amount is greater than 0")
	}
	return nil
}

// DirectPaymentProvisioningPayload defines the contract for direct payment provisioning retries.
type DirectPaymentProvisioningPayload struct {
	PurchaseRequestID int64  `json:"purchase_request_id"`
	UserID            int64  `json:"user_id"`
	QuoteID           *int64 `json:"quote_id,omitempty"`
	OperationKey      string `json:"operation_key"`
	ClientEmail       string `json:"client_email"`
	ExpectedUUID      string `json:"expected_uuid,omitempty"`
	ExpectedSubID     string `json:"expected_sub_id,omitempty"`
	PlanID            *int   `json:"plan_id,omitempty"`
	Months            int    `json:"months,omitempty"`
	IPLimit           int    `json:"ip_limit,omitempty"`
	DataGB            int    `json:"data_gb,omitempty"`
	CustomName        string `json:"custom_name,omitempty"`
}

func (p *DirectPaymentProvisioningPayload) Validate() error {
	if p.PurchaseRequestID <= 0 {
		return errors.New("purchase_request_id must be greater than 0")
	}
	if p.UserID <= 0 {
		return errors.New("user_id must be greater than 0")
	}
	if strings.TrimSpace(p.ClientEmail) == "" {
		return errors.New("client_email is required")
	}
	return nil
}

// Helper to coerce an untyped value from a map into an int64 safely.
func coerceInt64(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case int64:
		return val, true
	case int:
		return int64(val), true
	case int32:
		return int64(val), true
	case float64:
		return int64(val), true
	case float32:
		return int64(val), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err == nil {
			return n, true
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err == nil {
			return int64(f), true
		}
	case json.Number:
		n, err := val.Int64()
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// Helper to coerce string value from map.
func coerceString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", v))
}

// DecodePendingRefund decodes and normalizes raw desired_state into PendingRefundPayload.
func DecodePendingRefund(raw map[string]any, fallbackUserID *int64, fallbackOpKey string) (*PendingRefundPayload, error) {
	if raw == nil {
		raw = make(map[string]any)
	}
	p := &PendingRefundPayload{}

	if uid, ok := coerceInt64(raw["user_id"]); ok && uid > 0 {
		p.UserID = uid
	} else if fallbackUserID != nil && *fallbackUserID > 0 {
		p.UserID = *fallbackUserID
	}

	if amt, ok := coerceInt64(raw["amount"]); ok {
		p.Amount = amt
	} else if amt, ok := coerceInt64(raw["refund_amount"]); ok {
		p.Amount = amt
	} else if amt, ok := coerceInt64(raw["price"]); ok {
		p.Amount = amt
	}

	if key := coerceString(raw["operation_key"]); key != "" {
		p.OperationKey = key
	} else if key := coerceString(raw["refund_operation_key"]); key != "" {
		p.OperationKey = key
	} else if fallbackOpKey != "" {
		p.OperationKey = fallbackOpKey + ":refund"
	}

	p.Description = coerceString(raw["description"])
	if p.Description == "" {
		p.Description = "reconciliation wallet refund"
	}
	p.Reason = coerceString(raw["reason"])

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid pending_refund payload: %w", err)
	}
	return p, nil
}

// DecodePurchaseProvisioning decodes raw desired_state into PurchaseProvisioningPayload.
func DecodePurchaseProvisioning(raw map[string]any, fallbackUserID *int64, fallbackOpKey string) (*PurchaseProvisioningPayload, error) {
	if raw == nil {
		raw = make(map[string]any)
	}
	p := &PurchaseProvisioningPayload{}

	if uid, ok := coerceInt64(raw["user_id"]); ok && uid > 0 {
		p.UserID = uid
	} else if fallbackUserID != nil && *fallbackUserID > 0 {
		p.UserID = *fallbackUserID
	}

	if reqID, ok := coerceInt64(raw["purchase_request_id"]); ok && reqID > 0 {
		p.PurchaseRequestID = &reqID
	}
	if qID, ok := coerceInt64(raw["quote_id"]); ok && qID > 0 {
		p.QuoteID = &qID
	}

	p.OperationKey = coerceString(raw["operation_key"])
	if p.OperationKey == "" {
		p.OperationKey = fallbackOpKey
	}

	p.Email = coerceString(raw["email"])
	if p.Email == "" {
		p.Email = coerceString(raw["client_email"])
	}

	p.ExpectedUUID = coerceString(raw["expected_uuid"])
	if p.ExpectedUUID == "" {
		p.ExpectedUUID = coerceString(raw["uuid"])
	}
	if p.ExpectedUUID == "" {
		p.ExpectedUUID = coerceString(raw["client_uuid"])
	}

	p.ExpectedSubID = coerceString(raw["expected_sub_id"])
	if p.ExpectedSubID == "" {
		p.ExpectedSubID = coerceString(raw["sub_id"])
	}

	if planIDVal, ok := coerceInt64(raw["plan_id"]); ok {
		id := int(planIDVal)
		p.PlanID = &id
	}

	if months, ok := coerceInt64(raw["months"]); ok {
		p.Months = int(months)
	}
	if ipLimit, ok := coerceInt64(raw["ip_limit"]); ok {
		p.IPLimit = int(ipLimit)
	}
	if dataGB, ok := coerceInt64(raw["data_gb"]); ok {
		p.DataGB = int(dataGB)
	}

	if price, ok := coerceInt64(raw["price"]); ok {
		p.Price = price
	} else if price, ok := coerceInt64(raw["amount"]); ok {
		p.Price = price
	}

	p.RefundOperationKey = coerceString(raw["refund_operation_key"])
	if p.RefundOperationKey == "" && p.OperationKey != "" {
		p.RefundOperationKey = p.OperationKey + ":refund"
	}
	p.DisplayName = coerceString(raw["display_name"])

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid purchase provisioning payload: %w", err)
	}
	return p, nil
}

// DecodeSubscriptionDelete decodes raw desired_state into SubscriptionDeletePayload.
func DecodeSubscriptionDelete(raw map[string]any, fallbackSubID *int64, fallbackUserID *int64, fallbackOpKey string) (*SubscriptionDeletePayload, error) {
	if raw == nil {
		raw = make(map[string]any)
	}
	p := &SubscriptionDeletePayload{}

	if subID, ok := coerceInt64(raw["subscription_id"]); ok && subID > 0 {
		p.SubscriptionID = &subID
	} else if fallbackSubID != nil && *fallbackSubID > 0 {
		p.SubscriptionID = fallbackSubID
	}

	if uid, ok := coerceInt64(raw["user_id"]); ok && uid > 0 {
		p.UserID = &uid
	} else if fallbackUserID != nil && *fallbackUserID > 0 {
		p.UserID = fallbackUserID
	}

	p.ClientEmail = coerceString(raw["email"])
	if p.ClientEmail == "" {
		p.ClientEmail = coerceString(raw["client_email"])
	}

	if amt, ok := coerceInt64(raw["refund_amount"]); ok {
		p.RefundAmount = amt
	} else if amt, ok := coerceInt64(raw["amount"]); ok {
		p.RefundAmount = amt
	}

	p.RefundOperationKey = coerceString(raw["refund_operation_key"])
	if p.RefundOperationKey == "" && fallbackOpKey != "" {
		p.RefundOperationKey = fallbackOpKey + ":refund"
	}

	p.Reason = coerceString(raw["reason"])

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid subscription delete payload: %w", err)
	}
	return p, nil
}

// DecodeSubscriptionUpdate decodes raw desired_state into SubscriptionUpdateDbFailedPayload.
func DecodeSubscriptionUpdate(raw map[string]any, fallbackSubID *int64) (*SubscriptionUpdateDbFailedPayload, error) {
	if raw == nil {
		raw = make(map[string]any)
	}
	p := &SubscriptionUpdateDbFailedPayload{}

	if subID, ok := coerceInt64(raw["subscription_id"]); ok && subID > 0 {
		p.SubscriptionID = subID
	} else if fallbackSubID != nil && *fallbackSubID > 0 {
		p.SubscriptionID = *fallbackSubID
	}

	if uid, ok := coerceInt64(raw["user_id"]); ok && uid > 0 {
		p.UserID = uid
	}
	p.ClientEmail = coerceString(raw["client_email"])
	if p.ClientEmail == "" {
		p.ClientEmail = coerceString(raw["email"])
	}

	if v, ok := coerceInt64(raw["desired_ip_limit"]); ok {
		ip := int(v)
		p.DesiredIPLimit = &ip
	}
	if v, ok := coerceInt64(raw["desired_expire_time"]); ok {
		p.DesiredExpireTime = &v
	}
	if v, ok := raw["desired_is_active"].(bool); ok {
		p.DesiredIsActive = &v
	}

	if v, ok := coerceInt64(raw["previous_ip_limit"]); ok {
		p.PreviousIPLimit = int(v)
	}
	if v, ok := coerceInt64(raw["previous_expire_time"]); ok {
		p.PreviousExpireTime = v
	}
	if v, ok := raw["previous_is_active"].(bool); ok {
		p.PreviousIsActive = v
	}

	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid subscription update payload: %w", err)
	}
	return p, nil
}
