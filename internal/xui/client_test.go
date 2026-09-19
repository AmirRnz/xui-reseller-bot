package xui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"xui-reseller-bot/internal/config"
)

func TestGetClientByEmailReturnsTypedNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "client does not exist", http.StatusNotFound)
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	_, err = client.GetClientByEmail("missing@example.com")
	if !IsNotFound(err) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected typed not-found error, got %v", err)
	}
}

func TestGetSubscriptionLinksUsesPublicSubscriptionBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel/api/clients/subLinks/sub123" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     []string{"http://panel.internal:18104/sub/sub123"},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{
		BaseURL:             server.URL,
		SubscriptionBaseURL: "https://subs.example.com:9443",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub123")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0] != "https://subs.example.com:9443/sub/sub123" {
		t.Fatalf("unexpected subscription link: %s", links[0])
	}
}

func TestAddClientTimeoutAfterRemoteCommitIsVerifiedWithoutRetry(t *testing.T) {
	var mu sync.Mutex
	committed := false
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/add":
			mu.Lock()
			committed = true
			addCalls++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		case "/panel/api/clients/get/ambiguous@example.com":
			mu.Lock()
			isCommitted := committed
			mu.Unlock()
			if !isCommitted {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"obj":     map[string]any{"email": "ambiguous@example.com", "subId": "sub-1", "expiryTime": -3600000, "enable": true, "limitIp": 1},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.httpClient.Timeout = 10 * time.Millisecond
	result := client.AddClientResult(AddClientRequest{Client: ClientConfig{
		Email: "ambiguous@example.com", SubID: "sub-1", ExpiryTime: -3600000, Enable: true, LimitIP: 1,
	}})
	if result.Outcome != WriteSucceeded || result.Err != nil {
		t.Fatalf("expected verified success, got %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if addCalls != 1 {
		t.Fatalf("expected exactly one create call, got %d", addCalls)
	}
}

func TestAddClientTimeoutWithPartialInboundsDoesNotReportFalseSuccess(t *testing.T) {
	var mu sync.Mutex
	addCalls := 0
	attachCalls := 0
	remote := XUIClientInfo{
		Email: "partial@example.com", SubID: "sub-partial", ExpiryTime: -3600000,
		Enable: true, LimitIP: 1, TotalGB: 1073741824, InboundIDs: []int{1},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/add":
			mu.Lock()
			addCalls++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		case "/panel/api/clients/get/partial@example.com":
			mu.Lock()
			current := remote
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": current})
		case "/panel/api/clients/partial@example.com/attach":
			mu.Lock()
			attachCalls++
			mu.Unlock()
			// The panel acknowledged the repair request but did not attach the
			// missing inbound. AddClientResult must require a second readback.
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.httpClient.Timeout = 10 * time.Millisecond
	result := client.AddClientResult(AddClientRequest{Client: ClientConfig{
		Email: "partial@example.com", SubID: "sub-partial", ExpiryTime: -3600000,
		Enable: true, LimitIP: 1, TotalGB: 1073741824,
	}, InboundIDs: []int{1, 2}})
	if result.Outcome != WriteUnknown || !IsUnknownOutcome(result.Err) {
		t.Fatalf("expected unknown outcome for incomplete inbound readback, got %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if addCalls != 1 {
		t.Fatalf("expected exactly one create call, got %d", addCalls)
	}
	if attachCalls != 1 {
		t.Fatalf("expected one safe attachment repair, got %d", attachCalls)
	}
}

func TestUpdateClientMergesFullRemoteState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/get/preserve@example.com":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": map[string]any{
				"id": 9, "email": "preserve@example.com", "subId": "remote-sub", "uuid": "remote-uuid", "password": "remote-password",
				"auth": "remote-auth", "totalGB": 100, "expiryTime": 1000, "enable": true, "limitIp": 2, "limitHwid": 7,
				"comment": "manual comment", "group": "manually changed", "flow": "xtls-rprx-vision", "reset": 3,
				"resetDay": 4, "resetMax": 5, "trafficReset": "daily", "trafficResetDay": 2,
			}})
		case "/panel/api/clients/update/preserve@example.com":
			var got ClientConfig
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			if got.LimitHWID != 7 || got.Comment != "manual comment" || got.Group != "manually changed" || got.TrafficReset != "daily" || got.ResetDay != 4 {
				t.Fatalf("unrelated fields were overwritten: %+v", got)
			}
			if got.ExpiryTime != 2000 || got.LimitIP != 3 || got.Enable {
				t.Fatalf("owned fields were not applied: %+v", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if err := client.UpdateClient("preserve@example.com", ClientConfig{Email: "preserve@example.com", Enable: false, ExpiryTime: 2000, LimitIP: 3, TotalGB: 100, SubID: "remote-sub"}); err != nil {
		t.Fatalf("UpdateClient failed: %v", err)
	}
}

func TestUpdateClientTimeoutAfterRemoteCommitIsVerifiedWithoutRetry(t *testing.T) {
	var mu sync.Mutex
	remote := XUIClientInfo{Email: "update-ambiguous@example.com", SubID: "sub-update", ExpiryTime: 1000, Enable: true, LimitIP: 1}
	updateCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/get/update-ambiguous@example.com":
			mu.Lock()
			current := remote
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": current})
		case "/panel/api/clients/update/update-ambiguous@example.com":
			var desired ClientConfig
			if err := json.NewDecoder(r.Body).Decode(&desired); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			mu.Lock()
			remote.Enable = desired.Enable
			remote.ExpiryTime = desired.ExpiryTime
			remote.LimitIP = desired.LimitIP
			updateCalls++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.httpClient.Timeout = 10 * time.Millisecond
	result := client.UpdateClientResult("update-ambiguous@example.com", ClientConfig{
		Email: "update-ambiguous@example.com", SubID: "sub-update", ExpiryTime: 2000, Enable: false, LimitIP: 3,
	})
	if result.Outcome != WriteSucceeded || result.Err != nil {
		t.Fatalf("expected verified update success, got %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if updateCalls != 1 {
		t.Fatalf("expected exactly one update call, got %d", updateCalls)
	}
}

func TestGetSubscriptionLinksBuildsFallbackWithSubscriptionPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     []string{},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{
		BaseURL:             server.URL,
		SubscriptionBaseURL: "subs.example.com",
		SubscriptionPath:    "/custom-sub/",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub456")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0] != "https://subs.example.com/custom-sub/sub456" {
		t.Fatalf("unexpected fallback link: %s", links[0])
	}
}

func TestGetSubscriptionLinksKeepsLinksWhenNoPublicBaseURLIsSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj":     "http://panel.internal/sub/sub123\nhttp://panel.internal/json/sub123",
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	links, err := client.GetSubscriptionLinks("sub123")
	if err != nil {
		t.Fatalf("GetSubscriptionLinks failed: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d", len(links))
	}
	if links[0] != "http://panel.internal/sub/sub123" || links[1] != "http://panel.internal/json/sub123" {
		t.Fatalf("unexpected links: %#v", links)
	}
}

func TestBulkAttachDetach(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/panel/api/clients/bulkAttach" && r.URL.Path != "/panel/api/clients/bulkDetach" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var req BulkAttachRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if len(req.Emails) != 2 || req.Emails[0] != "alice" || req.Emails[1] != "bob" {
			t.Fatalf("unexpected emails: %v", req.Emails)
		}
		if len(req.InboundIDs) != 2 || req.InboundIDs[0] != 7 || req.InboundIDs[1] != 9 {
			t.Fatalf("unexpected inbound IDs: %v", req.InboundIDs)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"msg":     "OK",
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := BulkAttachRequest{
		Emails:     []string{"alice", "bob"},
		InboundIDs: []int{7, 9},
	}

	if err := client.BulkAttach(req); err != nil {
		t.Fatalf("BulkAttach failed: %v", err)
	}

	if err := client.BulkDetach(req); err != nil {
		t.Fatalf("BulkDetach failed: %v", err)
	}
}

func TestBulkCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Fatalf("expected POST method, got %s", r.Method)
		}
		if r.URL.Path != "/panel/api/clients/bulkCreate" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var req []BulkCreateItem
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		if len(req) != 2 {
			t.Fatalf("expected 2 items, got %d", len(req))
		}
		if req[0].Client.Email != "alice" || req[1].Client.Email != "bob" {
			t.Fatalf("unexpected emails in bulk request")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": map[string]any{
				"created": 2,
				"skipped": []any{},
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(&config.XUIConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	items := []BulkCreateItem{
		{Client: ClientConfig{Email: "alice"}, InboundIDs: []int{1}},
		{Client: ClientConfig{Email: "bob"}, InboundIDs: []int{2}},
	}

	resp, err := client.BulkCreate(items)
	if err != nil {
		t.Fatalf("BulkCreate failed: %v", err)
	}
	if resp.Created != 2 {
		t.Fatalf("expected 2 created, got %d", resp.Created)
	}
}
