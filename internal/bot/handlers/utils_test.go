package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"xui-reseller-bot/internal/db"
)

func TestBuyExtendAndIPUpgradePricingUsesIntegerToman(t *testing.T) {
	plan := &db.PaidPlan{
		BasePrice: 99999, BasePriceToman: 10000,
		PricePerExtraIP: 88888, PricePerExtraIPToman: 2000,
		BaseIPLimit: 1, MaxIPLimit: 3,
	}
	for _, action := range []string{"buy", "extend"} {
		if got := calculatePaidPrice(plan, 3, 1, 0); got != 30000 {
			t.Fatalf("%s price = %d; want 30000 from integer-Toman base", action, got)
		}
	}
	if got := calculateIPUpgradePrice(plan, 1, 3, 2); got != 8000 {
		t.Fatalf("IP-upgrade price = %d; want 8000 from integer-Toman extra-IP price", got)
	}
	if got := calculateIPUpgradePrice(plan, 3, 2, 2); got != 0 {
		t.Fatalf("non-upgrade limit change must not produce a charge, got %d", got)
	}
}

func TestFormatMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "bold conversion",
			input:    "**bold text**",
			expected: "*bold text*",
		},
		{
			name:     "bold with unescaped underscore",
			input:    "**bold** with _underscore_",
			expected: "*bold* with \\_underscore\\_",
		},
		{
			name:     "bold with unescaped asterisk",
			input:    "**bold** with *single*",
			expected: "*bold* with \\*single\\*",
		},
		{
			name:     "code span preservation",
			input:    "`code_span_with_underscore` and **bold**",
			expected: "`code_span_with_underscore` and *bold*",
		},
		{
			name:     "code block preservation",
			input:    "```\ncode_block_with_*\n``` and **bold**",
			expected: "```\ncode_block_with_*\n``` and *bold*",
		},
		{
			name:     "brackets escaping",
			input:    "some [brackets] here",
			expected: "some \\[brackets] here",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := FormatMarkdown(tc.input)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}

func TestApplyIPLimitFactor(t *testing.T) {
	tests := []struct {
		name     string
		ipLimit  int
		factor   string
		expected int
	}{
		{"empty factor", 2, "", 2},
		{"invalid format", 2, "abc", 2},
		{"multiplier *2", 2, "*2", 4},
		{"multiplier *3", 3, "*3", 9},
		{"multiplier *0", 2, "*0", 2},
		{"addition +3", 2, "+3", 5},
		{"addition +0", 2, "+0", 2},
		{"addition -3 invalid sign", 2, "-3", 2},
		{"zero limit with *2", 0, "*2", 0},
		{"zero limit with +3", 0, "+3", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ApplyIPLimitFactor(tc.ipLimit, tc.factor)
			if res != tc.expected {
				t.Errorf("ApplyIPLimitFactor(%d, %q) = %d; expected %d", tc.ipLimit, tc.factor, res, tc.expected)
			}
		})
	}
}

func TestReverseIPLimitFactor(t *testing.T) {
	tests := []struct {
		name     string
		adjusted int
		factor   string
		expected int
	}{
		{"empty factor", 4, "", 4},
		{"invalid format", 4, "abc", 4},
		{"multiplier *2", 4, "*2", 2},
		{"multiplier *2 division round down", 5, "*2", 2},
		{"multiplier *2 underflow limit", 1, "*2", 1},
		{"addition +3", 5, "+3", 2},
		{"addition +5 underflow limit", 3, "+5", 1},
		{"zero limit with *2", 0, "*2", 0},
		{"zero limit with +3", 0, "+3", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ReverseIPLimitFactor(tc.adjusted, tc.factor)
			if res != tc.expected {
				t.Errorf("ReverseIPLimitFactor(%d, %q) = %d; expected %d", tc.adjusted, tc.factor, res, tc.expected)
			}
		})
	}
}

func TestParseDevicesFromComment(t *testing.T) {
	tests := []struct {
		name     string
		comment  string
		expected int
		found    bool
	}{
		{"no devices", "created by reseller bot, gold plan", 0, false},
		{"simple devices", "created by reseller bot, devices: 2", 2, true},
		{"devices with spaces", "created by reseller bot, devices:  3", 3, true},
		{"devices in middle", "created by reseller bot, devices: 4, some user", 4, true},
		{"invalid number", "created by reseller bot, devices: abc", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, ok := parseDevicesFromComment(tc.comment)
			if ok != tc.found || res != tc.expected {
				t.Errorf("parseDevicesFromComment(%q) = (%d, %t); expected (%d, %t)", tc.comment, res, ok, tc.expected, tc.found)
			}
		})
	}
}

func TestSnapshotInt64_FloatAndScientificNotation(t *testing.T) {
	snapshot := map[string]any{
		"expiry_float":   float64(-2592000000),
		"expiry_sci":     "-2.592e+09",
		"expiry_int":     int64(-2592000000),
		"telegram_id":    float64(96937669),
		"total_bytes":    float64(10737418240),
		"total_bytes_str": "1.073741824e+10",
		"zero_float":     float64(0),
		"zero_int":       0,
		"json_number":    json.Number("-2592000000"),
	}

	if got := snapshotInt64(snapshot, "expiry_float"); got != -2592000000 {
		t.Fatalf("expected -2592000000 from float64, got %d", got)
	}
	if got := snapshotInt64(snapshot, "expiry_sci"); got != -2592000000 {
		t.Fatalf("expected -2592000000 from scientific string, got %d", got)
	}
	if got := snapshotInt64(snapshot, "expiry_int"); got != -2592000000 {
		t.Fatalf("expected -2592000000 from int64, got %d", got)
	}
	if got := snapshotInt64(snapshot, "telegram_id"); got != 96937669 {
		t.Fatalf("expected 96937669 from float64, got %d", got)
	}
	if got := snapshotInt64(snapshot, "total_bytes"); got != 10737418240 {
		t.Fatalf("expected 10737418240 from float64, got %d", got)
	}
	if got := snapshotInt64(snapshot, "total_bytes_str"); got != 10737418240 {
		t.Fatalf("expected 10737418240 from string, got %d", got)
	}
	if got := snapshotInt64(snapshot, "zero_float"); got != 0 {
		t.Fatalf("expected 0 from float64(0), got %d", got)
	}
	if got := snapshotInt64(snapshot, "zero_int"); got != 0 {
		t.Fatalf("expected 0 from 0, got %d", got)
	}
	if got := snapshotInt64(snapshot, "json_number"); got != -2592000000 {
		t.Fatalf("expected -2592000000 from json.Number, got %d", got)
	}
	if got := snapshotInt64(snapshot, "nonexistent"); got != 0 {
		t.Fatalf("expected 0 for missing key, got %d", got)
	}
}

func TestSnapshotIntSlice(t *testing.T) {
	s1 := snapshotIntSlice([]int{1, 2, 3})
	if len(s1) != 3 || s1[0] != 1 || s1[1] != 2 || s1[2] != 3 {
		t.Fatalf("unexpected result from []int: %+v", s1)
	}

	s2 := snapshotIntSlice([]float64{1, 2, 3})
	if len(s2) != 3 || s2[0] != 1 || s2[1] != 2 || s2[2] != 3 {
		t.Fatalf("unexpected result from []float64: %+v", s2)
	}

	s3 := snapshotIntSlice([]any{float64(4), "5", int64(6)})
	if len(s3) != 3 || s3[0] != 4 || s3[1] != 5 || s3[2] != 6 {
		t.Fatalf("unexpected result from []any: %+v", s3)
	}

	s4 := snapshotIntSlice(nil)
	if s4 != nil {
		t.Fatalf("expected nil from nil input, got %+v", s4)
	}
}

func TestFormatPlanIPLimitDescription(t *testing.T) {
	// Infinite and free IP limit
	p1 := &db.PaidPlan{BaseIPLimit: 0, MaxIPLimit: 0}
	desc1 := formatPlanIPLimitDescription(p1)
	if !strings.Contains(desc1, "نامحدود (رایگان)") {
		t.Fatalf("expected 'نامحدود (رایگان)' for Base=0, Max=0, got %q", desc1)
	}

	// Single fixed IP limit
	p2 := &db.PaidPlan{BaseIPLimit: 2, MaxIPLimit: 2}
	desc2 := formatPlanIPLimitDescription(p2)
	if !strings.Contains(desc2, "اتصال IP همزمان: 2") || strings.Contains(desc2, "تا سقف") {
		t.Fatalf("expected 'اتصال IP همزمان: 2' without extra range, got %q", desc2)
	}

	// Range IP limit with extra cost
	p3 := &db.PaidPlan{BaseIPLimit: 1, MaxIPLimit: 5, PricePerExtraIPToman: 15000}
	desc3 := formatPlanIPLimitDescription(p3)
	if !strings.Contains(desc3, "1 تا سقف 5") || !strings.Contains(desc3, "15,000") {
		t.Fatalf("expected range and extra price, got %q", desc3)
	}
}

