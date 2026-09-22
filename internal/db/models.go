package db

import (
	"math"
	"time"
)

const (
	UserStatusPending             = "pending"
	UserStatusApprovedNamePending = "approved_name_pending"
	UserStatusActive              = "active"
	UserStatusApproved            = "approved"
	UserStatusBanned              = "banned"

	PlanTypeTest = "test"
	PlanTypePaid = "paid"

	SubscriptionStatusActive         = "active"
	SubscriptionStatusDisabled       = "disabled"
	SubscriptionStatusExpired        = "expired"
	SubscriptionStatusCancelled      = "cancelled"
	SubscriptionStatusDeleted        = "deleted"
	SubscriptionStatusReconciliation = "reconciliation_required"

	PurchaseProvisioningPending   = "pending"
	PurchaseProvisioningSucceeded = "succeeded"
	PurchaseProvisioningRetryable = "retryable"
	PurchaseProvisioningFailed    = "failed"
)

type User struct {
	ID            int64     `json:"id"`
	TelegramID    int64     `json:"telegram_id"`
	Username      string    `json:"username"`
	FirstName     string    `json:"first_name"`
	LastName      string    `json:"last_name"`
	Language      string    `json:"language"`
	Status        string    `json:"status"`
	ServiceName   *string   `json:"service_name"`
	WalletBalance int64     `json:"wallet_balance"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (u *User) ServiceNameValue() string {
	if u == nil || u.ServiceName == nil {
		return ""
	}
	return *u.ServiceName
}

func (u *User) IsApproved() bool {
	return u != nil && (u.Status == UserStatusActive || u.Status == UserStatusApproved)
}

func IsValidSubscriptionStatus(status string) bool {
	switch status {
	case SubscriptionStatusActive,
		SubscriptionStatusDisabled,
		SubscriptionStatusExpired,
		SubscriptionStatusCancelled,
		SubscriptionStatusDeleted,
		SubscriptionStatusReconciliation:
		return true
	default:
		return false
	}
}

type WalletTransaction struct {
	ID            int64     `json:"id"`
	UserID        int64     `json:"user_id"`
	Amount        int64     `json:"amount"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	Description   string    `json:"description"`
	ReferenceType string    `json:"reference_type"`
	ReferenceID   *int64    `json:"reference_id"`
	OperationKey  string    `json:"operation_key"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type DiscountTier struct {
	Months      int     `json:"months"`
	Percent     float64 `json:"percent"`
	BasisPoints int64   `json:"basis_points,omitempty"`
}

func (d DiscountTier) GetBasisPoints() int64 {
	if d.BasisPoints > 0 {
		return d.BasisPoints
	}
	return int64(math.Round(d.Percent * 100))
}

type TestPlan struct {
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	UsageDescription string    `json:"usage_description"`
	InboundIDs       []int     `json:"inbound_ids"`
	ExpireSeconds    int64     `json:"expire_seconds"`
	MaxDataBytes     int64     `json:"max_data_bytes"`
	Flow             string    `json:"flow"`
	MaxPerDay        int       `json:"max_per_day"`
	IsGlobal         bool      `json:"is_global"`
	Enabled          bool      `json:"enabled"`
	SyncSubs         bool      `json:"sync_subs"`
	IPLimit          int       `json:"ip_limit"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type PaidPlan struct {
	ID                      int64          `json:"id"`
	Name                    string         `json:"name"`
	Description             string         `json:"description"`
	UsageDescription        string         `json:"usage_description"`
	InboundIDs              []int          `json:"inbound_ids"`
	BasePrice               float64        `json:"base_price"`
	BasePriceToman          int64          `json:"base_price_toman"`
	BaseIPLimit             int            `json:"base_ip_limit"`
	MaxIPLimit              int            `json:"max_ip_limit"`
	PricePerExtraIP         float64        `json:"price_per_extra_ip"`
	PricePerExtraIPToman    int64          `json:"price_per_extra_ip_toman"`
	Flow                    string         `json:"flow"`
	DiscountTiers           []DiscountTier `json:"discount_tiers"`
	IsGlobal                bool           `json:"is_global"`
	Enabled                 bool           `json:"enabled"`
	SyncSubs                bool           `json:"sync_subs"`
	IsLimited               bool           `json:"is_limited"`
	PricePerGB              float64        `json:"price_per_gb"`
	PricePerGBToman         int64          `json:"price_per_gb_toman"`
	MinDataGB               int64          `json:"min_data_gb"`
	PricePerExtraMonth      float64        `json:"price_per_extra_month"`
	PricePerExtraMonthToman int64          `json:"price_per_extra_month_toman"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
}

type Plan struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	DurationDays  int       `json:"duration_days"`
	TrafficGB     float64   `json:"traffic_gb"`
	IPLimit       int       `json:"ip_limit"`
	Price         float64   `json:"price"`
	DiscountTiers []int     `json:"discount_tiers"`
	InboundID     int       `json:"inbound_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type Subscription struct {
	ID                 int       `json:"id"`
	UserID             int64     `json:"user_id"`
	PlanID             *int      `json:"plan_id"`
	QuoteID            *int64    `json:"quote_id"`
	ClientEmail        string    `json:"client_email"`
	ClientUUID         string    `json:"client_uuid"`
	SubID              string    `json:"sub_id"`
	Status             string    `json:"status"`
	PlanType           string    `json:"plan_type"`
	DisplayName        string    `json:"display_name"`
	IPLimit            int       `json:"ip_limit"`
	ExpireTime         *int64    `json:"expire_time"`
	IsActive           bool      `json:"is_active"`
	StartDate          time.Time `json:"start_date"`
	EndDate            time.Time `json:"end_date"`
	TrafficLimitBytes  int64     `json:"traffic_limit_bytes"`
	DesiredIPLimit     *int      `json:"desired_ip_limit"`
	DesiredExpireTime  *int64    `json:"desired_expire_time"`
	DesiredIsActive    *bool     `json:"desired_is_active"`
	ReconciliationNote string    `json:"reconciliation_note"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type TopupRequest struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	TelegramFileID string    `json:"telegram_file_id"`
	Status         string    `json:"status"`
	Amount         *int64    `json:"amount"`
	AdminID        *int64    `json:"admin_id"`
	OperationKey   *string   `json:"operation_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type PurchaseRequest struct {
	ID                 int64     `json:"id"`
	UserID             int64     `json:"user_id"`
	Type               string    `json:"type"` // 'buy', 'extend', 'upgrade_ip'
	PlanID             *int64    `json:"plan_id"`
	SubscriptionID     *int64    `json:"subscription_id"`
	QuoteID            *int64    `json:"quote_id"`
	PriceToman         *int64    `json:"price_toman"`
	Price              float64   `json:"price"`
	Months             int       `json:"months"`
	IPLimit            int       `json:"ip_limit"`
	DataGB             int       `json:"data_gb"`
	CustomName         string    `json:"custom_name"`
	ClientEmail        string    `json:"client_email"`
	TelegramFileID     string    `json:"telegram_file_id"`
	Status             string    `json:"status"` // 'pending', 'approved', 'rejected'
	ProvisioningStatus string    `json:"provisioning_status"`
	OperationKey       string    `json:"operation_key"`
	AdminID            *int64    `json:"admin_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type RefundRequest struct {
	ID               int64     `json:"id"`
	UserID           int64     `json:"user_id"`
	SubscriptionID   *int64    `json:"subscription_id"`
	CalculatedAmount int64     `json:"calculated_amount"`
	ApprovedAmount   *int64    `json:"approved_amount"`
	Status           string    `json:"status"` // 'pending', 'approved', 'rejected'
	AdminID          *int64    `json:"admin_id"`
	OperationKey     string    `json:"operation_key"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
