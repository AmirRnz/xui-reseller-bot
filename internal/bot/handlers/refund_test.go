package handlers

import (
	"testing"
	"time"

	"xui-reseller-bot/internal/db"
)

func TestCalculateRefund(t *testing.T) {
	plan := &db.PaidPlan{
		BasePrice:       1000,
		BaseIPLimit:     1,
		PricePerExtraIP: 500,
	}

	now := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	
	// Example 1: 3 months total, 1 month remaining
	sub1 := &db.Subscription{
		StartDate: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2023, 7, 1, 0, 0, 0, 0, time.UTC),
		IPLimit:   2,
	}
	
	// totalPaid = base(1000) * 3 + extraIP(1 * 500 * 3) = 3000 + 1500 = 4500
	// 4500 / 3 = 1500 per month
	// remaining: 1 month
	// expected refund: 1500
	
	refund := CalculateRefund(plan, sub1, 1.0, now)
	if refund != 1500 {
		t.Errorf("Expected 1500, got %d", refund)
	}
	
	// Example 2: 6 months total, 4 months remaining
	sub2 := &db.Subscription{
		StartDate: time.Date(2023, 4, 1, 0, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
		IPLimit:   1,
	}
	
	// totalPaid = base(1000) * 6 = 6000
	// 6000 / 6 = 1000 per month
	// remaining = 4 months
	// expected refund = 4000
	
	refund = CalculateRefund(plan, sub2, 1.0, now)
	if refund != 4000 {
		t.Errorf("Expected 4000, got %d", refund)
	}
}
