package sync

import (
	"context"
	"testing"
	"time"

	"xui-reseller-bot/internal/xui"
)

type mockXUIClient struct {
	getClientByEmailFn func(email string) (*xui.XUIClientInfo, error)
	listClientsFn      func() ([]xui.XUIClientInfo, error)
}

func (m *mockXUIClient) GetClientByEmail(email string) (*xui.XUIClientInfo, error) {
	if m.getClientByEmailFn != nil {
		return m.getClientByEmailFn(email)
	}
	return nil, xui.ErrNotFound
}

func (m *mockXUIClient) ListClients() ([]xui.XUIClientInfo, error) {
	if m.listClientsFn != nil {
		return m.listClientsFn()
	}
	return nil, nil
}

func TestNewSyncWorker_DefaultInterval(t *testing.T) {
	worker := NewSyncWorker(nil, 0)
	if worker.Interval != 5*time.Minute {
		t.Fatalf("expected default 5m interval, got %v", worker.Interval)
	}

	workerCustom := NewSyncWorker(nil, 10*time.Minute)
	if workerCustom.Interval != 10*time.Minute {
		t.Fatalf("expected 10m interval, got %v", workerCustom.Interval)
	}
}

func TestSyncWorker_RunSync_NilPoolDoesNotPanic(t *testing.T) {
	worker := NewSyncWorker(&mockXUIClient{}, time.Minute)
	// Should return gracefully without panic even if db.Pool is nil
	worker.RunSync(context.Background())
}
