package persian

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatMoney(t *testing.T) {
	tests := []struct {
		amount   int64
		expected string
	}{
		{0, "0 تومان"},
		{500, "500 تومان"},
		{1000, "1,000 تومان"},
		{50000, "50,000 تومان"},
		{1234567, "1,234,567 تومان"},
		{-5000, "-5,000 تومان"},
	}

	for _, tt := range tests {
		got := FormatMoney(tt.amount)
		if got != tt.expected {
			t.Errorf("FormatMoney(%d) = %q, want %q", tt.amount, got, tt.expected)
		}
	}
}

func TestFormatTraffic(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "نامحدود"},
		{-10, "نامحدود"},
		{1073741824, "1 گیگابایت"},
		{2147483648, "2 گیگابایت"},
		{1610612736, "1.5 گیگابایت"},
	}

	for _, tt := range tests {
		got := FormatTraffic(tt.bytes)
		if got != tt.expected {
			t.Errorf("FormatTraffic(%d) = %q, want %q", tt.bytes, got, tt.expected)
		}
	}
}

func TestFormatIPLimit(t *testing.T) {
	tests := []struct {
		limit    int
		expected string
	}{
		{0, "نامحدود"},
		{-1, "نامحدود"},
		{1, "حداکثر 1 آی‌پی همزمان"},
		{2, "حداکثر 2 آی‌پی همزمان"},
		{5, "حداکثر 5 آی‌پی همزمان"},
	}

	for _, tt := range tests {
		got := FormatIPLimit(tt.limit)
		if got != tt.expected {
			t.Errorf("FormatIPLimit(%d) = %q, want %q", tt.limit, got, tt.expected)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		days     int
		expected string
	}{
		{0, "نامشخص"},
		{-5, "نامشخص"},
		{15, "15 روز"},
		{30, "1 ماه (30 روز)"},
		{60, "2 ماه (60 روز)"},
		{45, "45 روز"},
	}

	for _, tt := range tests {
		got := FormatDuration(tt.days)
		if got != tt.expected {
			t.Errorf("FormatDuration(%d) = %q, want %q", tt.days, got, tt.expected)
		}
	}
}

func TestSafeErrorMessage(t *testing.T) {
	msg := SafeErrorMessage(nil, "")
	if !strings.Contains(msg, "موفقیت") {
		t.Errorf("expected success message, got %q", msg)
	}

	err := errors.New("database connection refused: raw internal detail")
	msgErr := SafeErrorMessage(err, "TRK-12345")
	if strings.Contains(msgErr, "connection refused") || strings.Contains(msgErr, "raw internal detail") {
		t.Errorf("leak detected in SafeErrorMessage: %q", msgErr)
	}
	if !strings.Contains(msgErr, "TRK-12345") {
		t.Errorf("tracking ID missing in SafeErrorMessage: %q", msgErr)
	}
}
