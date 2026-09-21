package pricing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"xui-reseller-bot/internal/db"
)

type PurchaseQuote struct {
	ID                   int64     `json:"id"`
	QuoteKey             string    `json:"quote_key"`
	UserID               int64     `json:"user_id"`
	PlanID               int64     `json:"plan_id"`
	PlanName             string    `json:"plan_name"`
	Months               int       `json:"months"`
	DurationDays         int       `json:"duration_days"`
	IPLimit              int       `json:"ip_limit"`
	DataGB               int       `json:"data_gb"`
	BasePriceToman       int64     `json:"base_price_toman"`
	ExtraIPPriceToman    int64     `json:"extra_ip_price_toman"`
	ExtraMonthPriceToman int64     `json:"extra_month_price_toman"`
	TrafficPriceToman    int64     `json:"traffic_price_toman"`
	DiscountToman        int64     `json:"discount_toman"`
	FinalPriceToman      int64     `json:"final_price_toman"`
	Currency             string    `json:"currency"`
	CreatedAt            time.Time `json:"created_at"`
}

type QuoteParams struct {
	UserID       int64
	Plan         *db.PaidPlan
	Months       int
	IPLimit      int
	DataGB       int
	OperationKey string
}

func GenerateQuoteKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("quote_%s_%d", hex.EncodeToString(b), time.Now().Unix())
}

// CalculateQuote computes deterministic integer pricing for a paid plan.
// All amounts are in Toman (تومان). Duration is strictly 30 days per month.
// Half-up integer rounding is used for any percentages.
func CalculateQuote(p QuoteParams) *PurchaseQuote {
	if p.Plan == nil || p.Months <= 0 {
		return &PurchaseQuote{Currency: "تومان"}
	}

	ipLimit := p.IPLimit
	if ipLimit < p.Plan.BaseIPLimit {
		ipLimit = p.Plan.BaseIPLimit
	}
	extraIPs := ipLimit - p.Plan.BaseIPLimit

	basePrice := int64(math.Round(p.Plan.BasePrice))
	pricePerExtraIP := int64(math.Round(p.Plan.PricePerExtraIP))
	pricePerGB := int64(math.Round(p.Plan.PricePerGB))
	pricePerExtraMonth := int64(math.Round(p.Plan.PricePerExtraMonth))

	var trafficCost int64
	var extraMonthCost int64
	var baseCost int64

	if p.Plan.IsLimited {
		trafficCost = int64(p.DataGB) * pricePerGB
		if p.Months > 1 {
			extraMonthCost = int64(p.Months-1) * pricePerExtraMonth
		}
		baseCost = trafficCost + extraMonthCost
	} else {
		baseCost = basePrice * int64(p.Months)
	}

	extraIPCost := int64(extraIPs) * pricePerExtraIP * int64(p.Months)
	subtotal := baseCost + extraIPCost

	discountPercent := bestDiscountPercent(p.Plan.DiscountTiers, p.Months)
	var discountAmount int64
	if discountPercent > 0 {
		// Half-up integer rounding: (subtotal * percent*100 + 5000) / 10000
		discountAmount = (subtotal*int64(math.Round(discountPercent*100)) + 5000) / 10000
	}
	if discountAmount > subtotal {
		discountAmount = subtotal
	}
	finalPrice := subtotal - discountAmount

	key := p.OperationKey
	if key == "" {
		key = GenerateQuoteKey()
	}

	return &PurchaseQuote{
		QuoteKey:             key,
		UserID:               p.UserID,
		PlanID:               p.Plan.ID,
		PlanName:             p.Plan.Name,
		Months:               p.Months,
		DurationDays:         p.Months * 30,
		IPLimit:              ipLimit,
		DataGB:               p.DataGB,
		BasePriceToman:       baseCost,
		ExtraIPPriceToman:    extraIPCost,
		ExtraMonthPriceToman: extraMonthCost,
		TrafficPriceToman:    trafficCost,
		DiscountToman:        discountAmount,
		FinalPriceToman:      finalPrice,
		Currency:             "تومان",
		CreatedAt:            time.Now().UTC(),
	}
}

func bestDiscountPercent(tiers []db.DiscountTier, months int) float64 {
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].Months < tiers[j].Months })
	best := 0.0
	for _, tier := range tiers {
		if months >= tier.Months && tier.Percent > best {
			best = tier.Percent
		}
	}
	return best
}

func SaveQuote(ctx context.Context, q *PurchaseQuote) error {
	if q == nil || db.Pool == nil {
		return errors.New("invalid quote or database pool")
	}
	return db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_quotes (
			quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb,
			base_price_toman, extra_ip_price_toman, extra_month_price_toman, traffic_price_toman,
			discount_toman, final_price_toman, currency, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (quote_key) DO UPDATE SET
			final_price_toman = EXCLUDED.final_price_toman
		RETURNING id
	`, q.QuoteKey, q.UserID, q.PlanID, q.PlanName, q.Months, q.DurationDays, q.IPLimit, q.DataGB,
		q.BasePriceToman, q.ExtraIPPriceToman, q.ExtraMonthPriceToman, q.TrafficPriceToman,
		q.DiscountToman, q.FinalPriceToman, q.Currency, q.CreatedAt).Scan(&q.ID)
}

func GetQuoteByKey(ctx context.Context, quoteKey string) (*PurchaseQuote, error) {
	if db.Pool == nil {
		return nil, errors.New("database pool is nil")
	}
	q := &PurchaseQuote{}
	err := db.Pool.QueryRow(ctx, `
		SELECT id, quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb,
		       base_price_toman, extra_ip_price_toman, extra_month_price_toman, traffic_price_toman,
		       discount_toman, final_price_toman, currency, created_at
		FROM purchase_quotes
		WHERE quote_key = $1
	`, quoteKey).Scan(
		&q.ID, &q.QuoteKey, &q.UserID, &q.PlanID, &q.PlanName, &q.Months, &q.DurationDays, &q.IPLimit, &q.DataGB,
		&q.BasePriceToman, &q.ExtraIPPriceToman, &q.ExtraMonthPriceToman, &q.TrafficPriceToman,
		&q.DiscountToman, &q.FinalPriceToman, &q.Currency, &q.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return q, err
}

func GetQuoteByID(ctx context.Context, id int64) (*PurchaseQuote, error) {
	if db.Pool == nil {
		return nil, errors.New("database pool is nil")
	}
	q := &PurchaseQuote{}
	err := db.Pool.QueryRow(ctx, `
		SELECT id, quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb,
		       base_price_toman, extra_ip_price_toman, extra_month_price_toman, traffic_price_toman,
		       discount_toman, final_price_toman, currency, created_at
		FROM purchase_quotes
		WHERE id = $1
	`, id).Scan(
		&q.ID, &q.QuoteKey, &q.UserID, &q.PlanID, &q.PlanName, &q.Months, &q.DurationDays, &q.IPLimit, &q.DataGB,
		&q.BasePriceToman, &q.ExtraIPPriceToman, &q.ExtraMonthPriceToman, &q.TrafficPriceToman,
		&q.DiscountToman, &q.FinalPriceToman, &q.Currency, &q.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return q, err
}

// CalculateRefund computes refund amount in Toman based on historical quote and remaining service time.
// For pre-activation services (negative ExpireTime / no EndDate), returns full 100% refund.
// For active services, computes pro-rated daily refund based on remaining full days.
// If quote is unavailable (legacy), calculates fallback refund using subscription's current state.
func CalculateRefund(quote *PurchaseQuote, sub *db.Subscription, now time.Time) (int64, string) {
	if sub == nil {
		return 0, "اشتراک نامعتبر است"
	}

	// Case 1: Pre-activation service (never connected)
	if (sub.ExpireTime != nil && *sub.ExpireTime < 0) || sub.EndDate.IsZero() {
		if quote != nil && quote.FinalPriceToman > 0 {
			return quote.FinalPriceToman, "استرداد کامل (پیش از اولین اتصال)"
		}
		return 0, "سرویس فاقد فاکتور خرید اولیه است"
	}

	// Case 2: Expired service
	if !sub.EndDate.IsZero() && now.After(sub.EndDate) {
		return 0, "اشتراک منقضی شده است و امکان استرداد وجود ندارد"
	}

	// Case 3: Active service with historical quote
	if quote != nil && quote.FinalPriceToman > 0 && quote.DurationDays > 0 {
		hoursRemaining := sub.EndDate.Sub(now).Hours()
		daysRemaining := int64(math.Floor(hoursRemaining / 24.0))
		if daysRemaining <= 0 {
			return 0, "مدت زمان باقی‌مانده کمتر از یک روز است"
		}
		if daysRemaining >= int64(quote.DurationDays) {
			return quote.FinalPriceToman, "استرداد کامل"
		}

		refundAmount := (quote.FinalPriceToman * daysRemaining) / int64(quote.DurationDays)
		if refundAmount > quote.FinalPriceToman {
			refundAmount = quote.FinalPriceToman
		}
		return refundAmount, fmt.Sprintf("استرداد بر اساس %d روز باقی‌مانده از کل %d روز", daysRemaining, quote.DurationDays)
	}

	// Case 4: Legacy subscription without quote
	return 0, "سرویس قدیمی فاقد فاکتور ثبت‌شده برای محاسبه استرداد خودکار است (نیازمند بررسی ادمین)"
}
