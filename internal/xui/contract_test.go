package xui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"xui-reseller-bot/internal/config"
)

func TestContract_CheckReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/server/getPanelUpdateInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"obj": map[string]any{
					"currentVersion":  "3.8.5",
					"latestVersion":   "v3.8.5",
					"updateAvailable": false,
				},
			})
		case "/panel/api/inbounds/options":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"obj": []map[string]any{
					{"id": 1, "remark": "Inbound 1", "port": 443, "protocol": "vless"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{
		BaseURL:  server.URL,
		APIToken: "test_token_123",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	readiness, err := client.CheckReadiness(context.Background())
	if err != nil {
		t.Fatalf("CheckReadiness failed: %v", err)
	}
	if !readiness.MasterReachable || !readiness.TokenValid || !readiness.VersionOK {
		t.Fatalf("unexpected readiness status: %+v", readiness)
	}
	if readiness.Version != "3.8.5" || readiness.InboundsCount != 1 {
		t.Fatalf("unexpected version or inbounds: version=%s, inbounds=%d", readiness.Version, readiness.InboundsCount)
	}
}

func TestContract_GetInbounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel/api/inbounds/options" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": []map[string]any{
				{"id": 1, "remark": "Vless Reality", "port": 443, "protocol": "vless"},
				{"id": 2, "remark": "VMess WS", "port": 8080, "protocol": "vmess"},
			},
		})
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{BaseURL: server.URL, APIToken: "token"})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	inbounds, err := client.GetInbounds()
	if err != nil {
		t.Fatalf("GetInbounds failed: %v", err)
	}
	if len(inbounds) != 2 {
		t.Fatalf("expected 2 inbounds, got %d", len(inbounds))
	}
	if inbounds[0].ID != 1 || inbounds[0].Protocol != "vless" {
		t.Errorf("unexpected inbound[0]: %+v", inbounds[0])
	}
	if inbounds[1].ID != 2 || inbounds[1].Remark != "VMess WS" {
		t.Errorf("unexpected inbound[1]: %+v", inbounds[1])
	}
}

func TestContract_GetClientByEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel/api/clients/get/test@example.com" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"obj": map[string]any{
				"id":         42,
				"email":      "test@example.com",
				"enable":     true,
				"limitIp":    2,
				"total":      10737418240,
				"expiryTime": 1735689600000,
				"subId":      "sub123",
			},
		})
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{BaseURL: server.URL, APIToken: "token"})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	c, err := client.GetClientByEmail("test@example.com")
	if err != nil {
		t.Fatalf("GetClientByEmail failed: %v", err)
	}
	if c.Email != "test@example.com" || c.LimitIP != 2 || !c.Enable || c.SubID != "sub123" {
		t.Errorf("unexpected client data: %+v", c)
	}
}

func TestContract_AddClient(t *testing.T) {
	var receivedBody AddClientRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel/api/clients/add" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"msg":     "client added",
		})
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{BaseURL: server.URL, APIToken: "token"})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	req := AddClientRequest{
		Client: ClientConfig{
			Email:      "newuser@example.com",
			Enable:     true,
			LimitIP:    3,
			TotalGB:    21474836480,
			ExpiryTime: -2592000000,
			SubID:      "subnew",
		},
		InboundIDs: []int{1, 2},
	}
	err = client.AddClient(req)
	if err != nil {
		t.Fatalf("AddClient failed: %v", err)
	}
	if receivedBody.Client.Email != "newuser@example.com" || len(receivedBody.InboundIDs) != 2 {
		t.Errorf("received body mismatch: %+v", receivedBody)
	}
}

func TestContract_DeleteClient(t *testing.T) {
	deletedEmail := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		deletedEmail = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"msg":     "client deleted",
		})
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{BaseURL: server.URL, APIToken: "token"})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	err = client.DeleteClient("todelete@example.com")
	if err != nil {
		t.Fatalf("DeleteClient failed: %v", err)
	}
	if deletedEmail != "/panel/api/clients/del/todelete@example.com" {
		t.Errorf("unexpected delete path: %s", deletedEmail)
	}
}

func TestContract_UpdatePreservesNonBotFields(t *testing.T) {
	var receivedClient map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/get/test@example.com":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"obj": map[string]any{
					"id":         1,
					"uuid":       "uuid-test-1",
					"email":      "test@example.com",
					"subId":      "sub-test-1",
					"tgId":       12345678,
					"totalGB":    107374182400,
					"flow":       "xtls-rprx-vision",
					"group":      "test-group",
					"enable":     true,
					"expiryTime": 1700000000000,
					"limitIp":    1,
					"limitHwid":  2,
					"comment":    "preserved comment",
					"inboundIds": []int{1, 2},
				},
			})
		case "/panel/api/clients/update/test@example.com":
			_ = json.NewDecoder(r.Body).Decode(&receivedClient)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"msg":     "updated",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newReadyClient(&config.XUIConfig{
		BaseURL:  server.URL,
		APIToken: "token",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// 1. IP-only update
	err = client.UpdateClient("test@example.com", ClientConfig{
		Email:      "test@example.com",
		LimitIP:    3,
		Enable:     true,
		ExpiryTime: 1700000000000,
	})
	if err != nil {
		t.Fatalf("UpdateClient IP-only failed: %v", err)
	}
	if receivedClient["limitIp"] != float64(3) && receivedClient["limitIp"] != 3 {
		t.Fatalf("expected limitIp to be updated to 3, got %v", receivedClient["limitIp"])
	}
	if receivedClient["subId"] != "sub-test-1" || receivedClient["group"] != "test-group" || receivedClient["comment"] != "preserved comment" {
		t.Fatalf("unmanaged string metadata overwritten in IP-only update: %+v", receivedClient)
	}
	if receivedClient["limitHwid"] != float64(2) && receivedClient["limitHwid"] != 2 {
		t.Fatalf("expected limitHwid to be preserved as 2, got %v", receivedClient["limitHwid"])
	}
	if receivedClient["totalGB"] != float64(107374182400) && receivedClient["totalGB"] != int64(107374182400) {
		t.Fatalf("expected totalGB to be preserved, got %v", receivedClient["totalGB"])
	}
	if receivedClient["id"] != "uuid-test-1" {
		t.Fatalf("expected id to be preserved as uuid-test-1, got %v", receivedClient["id"])
	}

	// 2. Expiry-only update
	err = client.UpdateClient("test@example.com", ClientConfig{
		Email:      "test@example.com",
		ExpiryTime: 1800000000000,
		Enable:     true,
		LimitIP:    1,
	})
	if err != nil {
		t.Fatalf("UpdateClient expiry-only failed: %v", err)
	}
	if receivedClient["subId"] != "sub-test-1" || receivedClient["flow"] != "xtls-rprx-vision" || receivedClient["group"] != "test-group" {
		t.Fatalf("unmanaged metadata overwritten in expiry-only update: %+v", receivedClient)
	}

	// 3. Enable-only update
	err = client.UpdateClient("test@example.com", ClientConfig{
		Email:      "test@example.com",
		Enable:     false,
		ExpiryTime: 1700000000000,
		LimitIP:    1,
	})
	if err != nil {
		t.Fatalf("UpdateClient enable-only failed: %v", err)
	}
	if receivedClient["enable"] != false {
		t.Fatalf("expected enable to be false, got %v", receivedClient["enable"])
	}
	if receivedClient["subId"] != "sub-test-1" || receivedClient["comment"] != "preserved comment" || receivedClient["limitHwid"] != float64(2) {
		t.Fatalf("unmanaged metadata overwritten in enable-only update: %+v", receivedClient)
	}
}
