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

	client, err := NewClient(&config.XUIConfig{
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

func TestContract_UpdatePreservesNonBotFields(t *testing.T) {
	var receivedClient map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/panel/api/clients/get/test@example.com":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"obj": map[string]any{
					"id":         1,
					"email":      "test@example.com",
					"enable":     true,
					"limitIp":    1,
					"limitHwid":  2, // Custom non-bot field!
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

	client, err := NewClient(&config.XUIConfig{
		BaseURL:  server.URL,
		APIToken: "token",
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Update only LimitIP to 3
	err = client.UpdateClient("test@example.com", ClientConfig{
		Email:   "test@example.com",
		LimitIP: 3,
	})
	if err != nil {
		t.Fatalf("UpdateClient failed: %v", err)
	}

	// Verify that limitHwid and comment were preserved
	if receivedClient["limitHwid"] != float64(2) && receivedClient["limitHwid"] != 2 {
		t.Fatalf("expected limitHwid to be preserved as 2, got %v", receivedClient["limitHwid"])
	}
	if receivedClient["comment"] != "preserved comment" {
		t.Fatalf("expected comment to be preserved, got %v", receivedClient["comment"])
	}
	if receivedClient["limitIp"] != float64(3) && receivedClient["limitIp"] != 3 {
		t.Fatalf("expected limitIp to be updated to 3, got %v", receivedClient["limitIp"])
	}
}
