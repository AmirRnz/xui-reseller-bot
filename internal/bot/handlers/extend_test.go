package handlers

import (
	"reflect"
	"testing"
)

func TestExtractSubscriptionCandidates(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "standard 3x-ui sub URL",
			input:    "https://vpn.example.com:2096/sub/a89f928e01bd9",
			contains: "a89f928e01bd9",
		},
		{
			name:     "sub URL with trailing slash",
			input:    "https://vpn.example.com:2096/sub/a89f928e01bd9/",
			contains: "a89f928e01bd9",
		},
		{
			name:     "sub URL with query parameters",
			input:    "https://vpn.example.com:2096/sub/a89f928e01bd9?format=clash",
			contains: "a89f928e01bd9",
		},
		{
			name:     "URL with query param token",
			input:    "https://vpn.example.com/api?token=token12345",
			contains: "token12345",
		},
		{
			name:     "URL with custom sub prefix",
			input:    "https://vpn.example.com/prefix/sub/my-sub-token",
			contains: "my-sub-token",
		},
		{
			name:     "URL without scheme",
			input:    "vpn.example.com/sub/my-custom-sub-id",
			contains: "my-custom-sub-id",
		},
		{
			name:     "direct sub ID token",
			input:    "d1f8a846c98a002",
			contains: "d1f8a846c98a002",
		},
		{
			name:     "direct UUID",
			input:    "67e0e719-74d1-419b-a3dc-c99026210f9a",
			contains: "67e0e719-74d1-419b-a3dc-c99026210f9a",
		},
		{
			name:     "vless URL with UUID",
			input:    "vless://67e0e719-74d1-419b-a3dc-c99026210f9a@1.2.3.4:443?type=tcp&security=none#Device1",
			contains: "67e0e719-74d1-419b-a3dc-c99026210f9a",
		},
		{
			name:     "Persian message with embedded link",
			input:    "سلام لطفا این اشتراک را تمدید کنید: https://sub.domain.ir:8443/sub/subtoken9988 ممنون",
			contains: "subtoken9988",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := ExtractSubscriptionCandidates(tt.input)
			found := false
			for _, c := range candidates {
				if c == tt.contains {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("ExtractSubscriptionCandidates(%q) = %v, wanted it to contain %q", tt.input, candidates, tt.contains)
			}
		})
	}
}

func TestExtractSubscriptionCandidatesEmptyOrInvalid(t *testing.T) {
	if got := ExtractSubscriptionCandidates(""); len(got) != 0 {
		t.Errorf("expected empty candidates for empty input, got %v", got)
	}
	if got := ExtractSubscriptionCandidates("   \n\t  "); len(got) != 0 {
		t.Errorf("expected empty candidates for whitespace, got %v", got)
	}
}

func TestExtractSubscriptionCandidatesOrderingAndDedup(t *testing.T) {
	input := "https://example.com/sub/uuid-like-token?sub=uuid-like-token"
	candidates := ExtractSubscriptionCandidates(input)
	seen := make(map[string]int)
	for _, c := range candidates {
		seen[c]++
		if seen[c] > 1 {
			t.Errorf("duplicate candidate %q found in %v", c, candidates)
		}
	}
	if !reflect.DeepEqual(candidates, []string{"uuid-like-token", "https://example.com/sub/uuid-like-token?sub=uuid-like-token"}) {
		if len(candidates) == 0 || candidates[0] != "uuid-like-token" {
			t.Errorf("expected 'uuid-like-token' to be primary candidate, got %v", candidates)
		}
	}
}
