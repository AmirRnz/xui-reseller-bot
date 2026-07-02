package xui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func (c *Client) doRequest(method, endpoint string, body any, responseObj any) error {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	req, err := http.NewRequest(method, c.baseURL+endpoint, reqBody)
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
		return fmt.Errorf("API returned non-2xx status %d: %s", resp.StatusCode, string(bodyBytes))
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
			return json.Unmarshal(bodyBytes, responseObj)
		}
		return nil
	}

	if !apiResp.Success {
		if apiResp.Msg == "" {
			apiResp.Msg = "unknown x-ui API error"
		}
		return fmt.Errorf("API error: %s", apiResp.Msg)
	}

	if responseObj != nil && len(apiResp.Obj) > 0 && string(apiResp.Obj) != "null" {
		if err := json.Unmarshal(apiResp.Obj, responseObj); err != nil {
			return fmt.Errorf("failed to decode response object: %w", err)
		}
	}

	return nil
}

func pathEscape(s string) string {
	return url.PathEscape(s)
}
