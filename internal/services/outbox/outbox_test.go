package outbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v3"
)

type mockSender struct {
	lastRecipient telebot.Recipient
	lastWhat      interface{}
	sendErr       error
	sendCount     int
}

func (m *mockSender) Send(to telebot.Recipient, what interface{}, opts ...interface{}) (*telebot.Message, error) {
	m.sendCount++
	m.lastRecipient = to
	m.lastWhat = what
	return nil, m.sendErr
}

func TestOutboxPayloadSerialization(t *testing.T) {
	payload := map[string]any{
		"days_left":    3,
		"client_email": "test-user-30d",
		"end_date":     "2026-10-01",
	}

	bytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded["client_email"] != "test-user-30d" {
		t.Errorf("expected test-user-30d, got %v", decoded["client_email"])
	}
}

func TestWorkerNilSender(t *testing.T) {
	w := NewWorker(nil)
	if w.Sender != nil {
		t.Errorf("expected sender to be nil")
	}
}

func TestFloodErrorDetection(t *testing.T) {
	floodErr := telebot.FloodError{
		RetryAfter: 42,
	}

	var target telebot.FloodError
	if !errors.As(floodErr, &target) {
		t.Errorf("errors.As failed to match telebot.FloodError")
	}
	if target.RetryAfter != 42 {
		t.Errorf("expected RetryAfter = 42, got %d", target.RetryAfter)
	}

	retryDur := time.Duration(target.RetryAfter) * time.Second
	if retryDur != 42*time.Second {
		t.Errorf("expected 42s, got %v", retryDur)
	}
}

func TestNotificationMessageFormat(t *testing.T) {
	daysLeft := 1
	clientEmail := "client@example.com"
	endDate := "2026-10-15"

	var msgText strings.Builder
	msgText.WriteString("⚠️ **هشدار انقضای اشتراک**\n\n")
	if daysLeft == 1 {
		msgText.WriteString("سرویس شما **فردا** منقضی خواهد شد.\n")
	} else {
		msgText.WriteString("سرویس شما تا **چند روز دیگر** منقضی خواهد شد.\n")
	}
	msgText.WriteString("🔹 نام سرویس: `" + clientEmail + "`\n")
	msgText.WriteString("📅 تاریخ انقضا: " + endDate + "\n\n")

	content := msgText.String()
	if !strings.Contains(content, "فردا") {
		t.Errorf("expected reminder for tomorrow, got %s", content)
	}
	if !strings.Contains(content, clientEmail) {
		t.Errorf("expected email in message, got %s", content)
	}
}
