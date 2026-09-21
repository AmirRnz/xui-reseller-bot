package pricing

import (
	"testing"
	"time"

	"xui-reseller-bot/internal/db"
)

func TestCalculateQuote_FixedPlan(t *testing.T) {
	plan := &db.PaidPlan{
		ID:              1,
		Name:            "Plan 1",
		BasePrice:       50000,
		BaseIPLimit:     1,
		MaxIPLimit:      5,
		PricePerExtraIP: 15000,
		DiscountTiers: []db.DiscountTier{
			{Months: 3, Percent: 10},
			{Months: 6, Percent: 20},
		},
	}

	// 1 month, base IP
	q1 := CalculateQuote(QuoteParams{
		UserID:  100,
		Plan:    plan,
		Months:  1,
		IPLimit: 1,
	})
	if q1.FinalPriceToman != 50000 {
		t.Fatalf("expected 50000, got %d", q1.FinalPriceToman)
	}
	if q1.DurationDays != 30 {
		t.Fatalf("expected 30 days, got %d", q1.DurationDays)
	}

	// 1 month, 2 IPs (1 extra IP)
	q2 := CalculateQuote(QuoteParams{
		UserID:  100,
		Plan:    plan,
		Months:  1,
		IPLimit: 2,
	})
	// 50000 + 15000 = 65000
	if q2.FinalPriceToman != 65000 {
		t.Fatalf("expected 65000, got %d", q2.FinalPriceToman)
	}

	// 3 months, 2 IPs, 10% discount
	// subtotal = (50000 * 3) + (15000 * 3) = 150000 + 45000 = 195000
	// discount = 10% of 195000 = 19500
	// final = 175500
	q3 := CalculateQuote(QuoteParams{
		UserID:  100,
		Plan:    plan,
		Months:  3,
		IPLimit: 2,
	})
	if q3.DiscountToman != 19500 {
		t.Fatalf("expected discount 19500, got %d", q3.DiscountToman)
	}
	if q3.FinalPriceToman != 175500 {
		t.Fatalf("expected 175500, got %d", q3.FinalPriceToman)
	}
	if q3.DurationDays != 90 {
		t.Fatalf("expected 90 days, got %d", q3.DurationDays)
	}
}

func TestCalculateQuote_LimitedPlan(t *testing.T) {
	plan := &db.PaidPlan{
		ID:                 2,
		Name:               "Limited 100GB",
		IsLimited:          true,
		PricePerGB:         1000,
		MinDataGB:          10,
		PricePerExtraMonth: 20000,
		BaseIPLimit:        1,
		MaxIPLimit:         3,
		PricePerExtraIP:    10000,
	}

	// 50 GB, 1 month, 1 IP
	// traffic = 50 * 1000 = 50000
	// extra month = 0
	// extra IP = 0
	q1 := CalculateQuote(QuoteParams{
		UserID:  100,
		Plan:    plan,
		Months:  1,
		IPLimit: 1,
		DataGB:  50,
	})
	if q1.FinalPriceToman != 50000 {
		t.Fatalf("expected 50000, got %d", q1.FinalPriceToman)
	}

	// 50 GB, 3 months (2 extra months = 40000), 2 IPs (1 extra IP = 3 * 10000 = 30000)
	// subtotal = 50000 + 40000 + 30000 = 120000
	q2 := CalculateQuote(QuoteParams{
		UserID:  100,
		Plan:    plan,
		Months:  3,
		IPLimit: 2,
		DataGB:  50,
	})
	if q2.FinalPriceToman != 120000 {
		t.Fatalf("expected 120000, got %d", q2.FinalPriceToman)
	}
}

func TestCalculateRefund(t *testing.T) {
	quote := &PurchaseQuote{
		FinalPriceToman: 90000,
		Months:          3,
		DurationDays:    90,
	}

	now := time.Now()

	// Pre-activation subscription (negative expireTime)
	negExpire := int64(-90 * 24 * 3600 * 1000)
	subPre := &db.Subscription{
		ExpireTime: &negExpire,
		EndDate:    time.Time{},
	}
	refund, reason := CalculateRefund(quote, subPre, now)
	if refund != 90000 {
		t.Fatalf("expected full refund 90000, got %d (%s)", refund, reason)
	}

	// Active subscription with 45 days remaining out of 90
	// daily rate = 90000 / 90 = 1000
	// refund = 45 * 1000 = 45000
	subActive := &db.Subscription{
		EndDate: now.Add(45 * 24 * time.Hour),
	}
	refundActive, _ := CalculateRefund(quote, subActive, now)
	if refundActive != 45000 {
		t.Fatalf("expected 45000, got %d", refundActive)
	}

	// Expired subscription
	subExpired := &db.Subscription{
		EndDate: now.Add(-2 * time.Hour),
	}
	refundExp, _ := CalculateRefund(quote, subExpired, now)
	if refundExp != 0 {
		t.Fatalf("expected 0 refund for expired, got %d", refundExp)
	}

	// Legacy subscription without quote
	refundLegacy, _ := CalculateRefund(nil, subActive, now)
	if refundLegacy != 0 {
		t.Fatalf("expected 0 for legacy without quote, got %d", refundLegacy)
	}
}
