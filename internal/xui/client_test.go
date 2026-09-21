package xui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMergeClientConfigPreservesUnmanagedMetadata(t *testing.T) {
	current := XUIClientInfo{
		ID:         42,
		UUID:       "current-uuid-42",
		Email:      "user@example.com",
		SubID:      "sub-abc",
		TgID:       987654321,
		TotalGB:    50 * 1024 * 1024 * 1024,
		Flow:       "xtls-rprx-vision",
		Group:      "vip-group",
		Comment:    "vip customer",
		LimitIP:    2,
		LimitHWID:  3,
		Enable:     true,
		ExpiryTime: 1700000000000,
		Password:   "pwd-123",
		Auth:       "auth-456",
		Reset:      1,
		ResetDay:   5,
		ResetMax:   10,
	}

	// 1. IP-only update: desired only changes LimitIP, leaving other metadata empty/zero
	desiredIPOnly := ClientConfig{
		Email:      "user@example.com",
		LimitIP:    5,
		Enable:     true,
		ExpiryTime: 1700000000000,
	}
	mergedIP := mergeClientConfig(current, desiredIPOnly)
	if mergedIP.LimitIP != 5 {
		t.Errorf("expected LimitIP 5, got %d", mergedIP.LimitIP)
	}
	if mergedIP.SubID != "sub-abc" || mergedIP.TgID != 987654321 || mergedIP.TotalGB != 50*1024*1024*1024 {
		t.Errorf("metadata overwritten in IP-only update: %+v", mergedIP)
	}
	if mergedIP.Flow != "xtls-rprx-vision" || mergedIP.Group != "vip-group" || mergedIP.Comment != "vip customer" {
		t.Errorf("flow/group/comment overwritten in IP-only update: %+v", mergedIP)
	}
	if mergedIP.LimitHWID != 3 || mergedIP.ID != "current-uuid-42" || mergedIP.Password != "pwd-123" || mergedIP.Auth != "auth-456" {
		t.Errorf("hwid/id/credentials overwritten in IP-only update: %+v", mergedIP)
	}

	// 2. Expiry-only update
	desiredExpiryOnly := ClientConfig{
		Email:      "user@example.com",
		ExpiryTime: 1800000000000,
		Enable:     true,
		LimitIP:    2,
	}
	mergedExpiry := mergeClientConfig(current, desiredExpiryOnly)
	if mergedExpiry.ExpiryTime != 1800000000000 {
		t.Errorf("expected ExpiryTime 1800000000000, got %d", mergedExpiry.ExpiryTime)
	}
	if mergedExpiry.SubID != "sub-abc" || mergedExpiry.TotalGB != 50*1024*1024*1024 || mergedExpiry.LimitHWID != 3 {
		t.Errorf("metadata overwritten in expiry-only update: %+v", mergedExpiry)
	}

	// 3. Enable-only update
	desiredEnableOnly := ClientConfig{
		Email:      "user@example.com",
		Enable:     false,
		ExpiryTime: 1700000000000,
		LimitIP:    2,
	}
	mergedEnable := mergeClientConfig(current, desiredEnableOnly)
	if mergedEnable.Enable != false {
		t.Errorf("expected Enable false, got %v", mergedEnable.Enable)
	}
	if mergedEnable.SubID != "sub-abc" || mergedEnable.TgID != 987654321 || mergedEnable.Group != "vip-group" {
		t.Errorf("metadata overwritten in enable-only update: %+v", mergedEnable)
	}

	// 4. Desired explicit overrides
	desiredOverrides := ClientConfig{
		Email:     "new@example.com",
		SubID:     "new-sub",
		TgID:      111222,
		TotalGB:   100,
		Flow:      "new-flow",
		LimitHWID: 10,
		Group:     "new-group",
		Comment:   "new-comment",
		ID:        "custom-uuid",
	}
	mergedOverrides := mergeClientConfig(current, desiredOverrides)
	if mergedOverrides.Email != "new@example.com" || mergedOverrides.SubID != "new-sub" || mergedOverrides.TgID != 111222 {
		t.Errorf("explicit overrides not applied: %+v", mergedOverrides)
	}
	if mergedOverrides.TotalGB != 100 || mergedOverrides.Flow != "new-flow" || mergedOverrides.LimitHWID != 10 || mergedOverrides.Group != "new-group" || mergedOverrides.Comment != "new-comment" || mergedOverrides.ID != "custom-uuid" {
		t.Errorf("explicit overrides not applied: %+v", mergedOverrides)
	}
}

func TestClientPatchSemantics(t *testing.T) {
	current := XUIClientInfo{
		ID:         10,
		UUID:       "uuid-1234",
		Email:      "patch@example.com",
		SubID:      "sub-keep-me",
		Flow:       "xtls-rprx-vision",
		Group:      "reseller-group",
		Enable:     true,
		ExpiryTime: 1700000000000,
		LimitIP:    5,
		TotalGB:    50 * 1024 * 1024 * 1024,
		TgID:       12345678,
		LimitHWID:  2,
	}

	t.Run("nil fields preserve current values, subID and flow intact", func(t *testing.T) {
		newExpiry := int64(1800000000000)
		patch := ClientPatch{
			ExpiryTime: &newExpiry,
		}
		merged := mergeClientConfigWithPatch(current, patch)
		if merged.ExpiryTime != 1800000000000 {
			t.Fatalf("expected ExpiryTime to be patched, got %d", merged.ExpiryTime)
		}
		if !merged.Enable {
			t.Fatalf("expected Enable to remain true, got false")
		}
		if merged.LimitIP != 5 {
			t.Fatalf("expected LimitIP to remain 5, got %d", merged.LimitIP)
		}
		if merged.TotalGB != 50*1024*1024*1024 {
			t.Fatalf("expected TotalGB to remain preserved, got %d", merged.TotalGB)
		}
		if merged.SubID != "sub-keep-me" {
			t.Fatalf("expected SubID to be preserved, got %q", merged.SubID)
		}
		if merged.Flow != "xtls-rprx-vision" {
			t.Fatalf("expected Flow to be preserved, got %q", merged.Flow)
		}
		if merged.Group != "reseller-group" {
			t.Fatalf("expected Group to be preserved, got %q", merged.Group)
		}
		if merged.TgID != 12345678 {
			t.Fatalf("expected TgID to be preserved, got %d", merged.TgID)
		}
	})

	t.Run("non-nil zero values are correctly applied", func(t *testing.T) {
		zeroIP := 0
		zeroExpiry := int64(0)
		falseEnable := false
		zeroGB := int64(0)
		patch := ClientPatch{
			LimitIP:    &zeroIP,
			ExpiryTime: &zeroExpiry,
			Enable:     &falseEnable,
			TotalGB:    &zeroGB,
		}
		merged := mergeClientConfigWithPatch(current, patch)
		if merged.LimitIP != 0 {
			t.Fatalf("expected LimitIP 0 (unlimited), got %d", merged.LimitIP)
		}
		if merged.ExpiryTime != 0 {
			t.Fatalf("expected ExpiryTime 0, got %d", merged.ExpiryTime)
		}
		if merged.Enable != false {
			t.Fatalf("expected Enable false, got %v", merged.Enable)
		}
		if merged.TotalGB != 0 {
			t.Fatalf("expected TotalGB 0, got %d", merged.TotalGB)
		}
		// Metadata must still be preserved
		if merged.SubID != "sub-keep-me" || merged.Flow != "xtls-rprx-vision" {
			t.Fatalf("metadata corrupted when applying zero values: SubID=%q Flow=%q", merged.SubID, merged.Flow)
		}
	})
}

func TestUpdateClientPatchTimeoutVerification(t *testing.T) {
	t.Run("timeout followed by matching readback returns WriteSucceeded", func(t *testing.T) {
		limitIP := 3
		remote := XUIClientInfo{
			Email:   "timeout_ok@example.com",
			UUID:    "uuid-1",
			LimitIP: 3,
			Enable:  true,
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/panel/api/clients/get/timeout_ok@example.com":
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": remote})
			case "/panel/api/clients/update/timeout_ok@example.com":
				time.Sleep(100 * time.Millisecond) // Trigger client timeout
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		client.httpClient.Timeout = 10 * time.Millisecond

		res := client.UpdateClientPatchResult("timeout_ok@example.com", ClientPatch{LimitIP: &limitIP})
		if res.Outcome != WriteSucceeded {
			t.Fatalf("expected WriteSucceeded when readback matches patched fields, got %v: %v", res.Outcome, res.Err)
		}
	})

	t.Run("timeout followed by mismatched readback returns WriteUnknown", func(t *testing.T) {
		limitIP := 3
		remote := XUIClientInfo{
			Email:   "timeout_mismatch@example.com",
			UUID:    "uuid-2",
			LimitIP: 1, // Does NOT match patched field!
			Enable:  true,
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/panel/api/clients/get/timeout_mismatch@example.com":
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": remote})
			case "/panel/api/clients/update/timeout_mismatch@example.com":
				time.Sleep(100 * time.Millisecond)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		client.httpClient.Timeout = 10 * time.Millisecond

		res := client.UpdateClientPatchResult("timeout_mismatch@example.com", ClientPatch{LimitIP: &limitIP})
		if res.Outcome != WriteUnknown {
			t.Fatalf("expected WriteUnknown when readback does not match patched fields, got %v", res.Outcome)
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "timeout verification failed") {
			t.Fatalf("expected error to mention timeout verification failed, got %v", res.Err)
		}
	})

	t.Run("timeout followed by missing client returns WriteUnknown", func(t *testing.T) {
		limitIP := 3
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/panel/api/clients/get/timeout_missing@example.com":
				calls++
				if calls == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": XUIClientInfo{Email: "timeout_missing@example.com"}})
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "msg": "Client not found"})
				}
			case "/panel/api/clients/update/timeout_missing@example.com":
				time.Sleep(100 * time.Millisecond)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		client.httpClient.Timeout = 10 * time.Millisecond

		res := client.UpdateClientPatchResult("timeout_missing@example.com", ClientPatch{LimitIP: &limitIP})
		if res.Outcome != WriteUnknown {
			t.Fatalf("expected WriteUnknown on missing client readback, got %v", res.Outcome)
		}
	})
}

func TestFindClientBySubID_BoundedPagination(t *testing.T) {
	t.Run("found on page 2", func(t *testing.T) {
		pageCalls := 0
		listClientsCalled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/panel/api/clients/list" {
				listClientsCalled = true
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []any{}})
				return
			}
			if r.URL.Path == "/panel/api/clients/list/paged" {
				pageCalls++
				page := r.URL.Query().Get("page")
				if page == "1" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"success": true,
						"obj": map[string]any{
							"filtered": 25,
							"items": []map[string]any{
								{"email": "other@example.com", "subId": "sub-other"},
							},
						},
					})
					return
				}
				if page == "2" {
					_ = json.NewEncoder(w).Encode(map[string]any{
						"success": true,
						"obj": map[string]any{
							"filtered": 25,
							"items": []map[string]any{
								{"email": "target@example.com", "subId": "target-sub"},
							},
						},
					})
					return
				}
			}
			if r.URL.Path == "/panel/api/clients/get/target@example.com" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"obj": map[string]any{
						"email":  "target@example.com",
						"subId":  "target-sub",
						"enable": true,
					},
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		found, err := client.FindClientBySubID("target-sub")
		if err != nil {
			t.Fatalf("expected to find client on page 2, got err: %v", err)
		}
		if found.Email != "target@example.com" {
			t.Fatalf("expected target@example.com, got %s", found.Email)
		}
		if pageCalls != 2 {
			t.Fatalf("expected 2 page calls, got %d", pageCalls)
		}
		if listClientsCalled {
			t.Fatalf("ListClients fleet scan was called, expected none")
		}
	})

	t.Run("not found bounded to 3 pages max and never calls ListClients", func(t *testing.T) {
		pageCalls := 0
		listClientsCalled := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/panel/api/clients/list" {
				listClientsCalled = true
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": []any{}})
				return
			}
			if r.URL.Path == "/panel/api/clients/list/paged" {
				pageCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"obj": map[string]any{
						"filtered": 100,
						"items": []map[string]any{
							{"email": "other@example.com", "subId": "sub-other"},
						},
					},
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		found, err := client.FindClientBySubID("non-existent")
		if !IsNotFound(err) {
			t.Fatalf("expected ErrNotFound, got found=%v err=%v", found, err)
		}
		if pageCalls != 3 {
			t.Fatalf("expected bounded 3 page calls, got %d", pageCalls)
		}
		if listClientsCalled {
			t.Fatalf("ListClients fleet scan must not be called")
		}
	})

	t.Run("early stop when page results exhausted", func(t *testing.T) {
		pageCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/panel/api/clients/list/paged" {
				pageCalls++
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success": true,
					"obj": map[string]any{
						"filtered": 3,
						"items": []map[string]any{
							{"email": "other@example.com", "subId": "sub-other"},
						},
					},
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		client, _ := NewClient(&config.XUIConfig{BaseURL: server.URL})
		_, err := client.FindClientBySubID("non-existent")
		if !IsNotFound(err) {
			t.Fatalf("expected ErrNotFound, got err=%v", err)
		}
		if pageCalls != 1 {
			t.Fatalf("expected early stop after page 1 (filtered=3 <= 10), got %d calls", pageCalls)
		}
	})
}
