package xui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) doRequest(method, endpoint string, body any, responseObj any) error {
	return c.doRequestContext(context.Background(), method, endpoint, body, responseObj)
}

func (c *Client) doRequestContext(ctx context.Context, method, endpoint string, body any, responseObj any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := string(bodyBytes)
		if resp.StatusCode == http.StatusNotFound {
			return &NotFoundError{StatusCode: resp.StatusCode, Message: message}
		}
		return &HTTPStatusError{StatusCode: resp.StatusCode, Message: message}
	}
	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return nil
	}

	var apiResp struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		if responseObj != nil {
			if directErr := json.Unmarshal(bodyBytes, responseObj); directErr == nil {
				return nil
			}
		}
		return fmt.Errorf("invalid x-ui API response: %w", err)
	}

	if !apiResp.Success {
		if apiResp.Msg == "" {
			apiResp.Msg = "unknown x-ui API error"
		}
		if isNotFoundMessage(apiResp.Msg) {
			return &NotFoundError{Message: apiResp.Msg}
		}
		return &PanelAPIError{Message: apiResp.Msg}
	}

	if responseObj != nil && len(apiResp.Obj) > 0 && string(apiResp.Obj) != "null" {
		if err := json.Unmarshal(apiResp.Obj, responseObj); err != nil {
			return fmt.Errorf("failed to decode response object: %w", err)
		}
	}

	return nil
}

type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "x-ui returned an HTTP error"
	}
	return fmt.Sprintf("API returned non-2xx status %d: %s", e.StatusCode, e.Message)
}

type PanelAPIError struct {
	Message string
}

func (e *PanelAPIError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "unknown x-ui API error"
	}
	return "API error: " + e.Message
}

func isNotFoundMessage(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "not found") || strings.Contains(message, "does not exist")
}

func pathEscape(s string) string {
	return url.PathEscape(s)
}
