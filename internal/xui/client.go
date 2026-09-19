package xui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xui-reseller-bot/internal/config"
)

type Client struct {
	baseURL             string
	subscriptionBaseURL string
	subscriptionPath    string
	apiToken            string
	httpClient          *http.Client
	Cache               *InboundCache
}

type WriteOutcome string

const (
	WriteSucceeded         WriteOutcome = "success"
	WriteDefinitiveFailure WriteOutcome = "definitive_failure"
	WriteUnknown           WriteOutcome = "unknown"
)

type WriteResult struct {
	Outcome WriteOutcome
	Err     error
}

type WriteError struct {
	Outcome WriteOutcome
	Err     error
}

func (e *WriteError) Error() string { return e.Err.Error() }
func (e *WriteError) Unwrap() error { return e.Err }

func IsUnknownOutcome(err error) bool {
	var writeErr *WriteError
	return errors.As(err, &writeErr) && writeErr.Outcome == WriteUnknown
}

func IsDefinitiveFailure(err error) bool {
	var writeErr *WriteError
	return errors.As(err, &writeErr) && writeErr.Outcome == WriteDefinitiveFailure
}

func NewClient(cfg *config.XUIConfig) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("x-ui config is nil")
	}

	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(cfg.URL, "/")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("x-ui url is empty")
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	subscriptionBaseURL := normalizePublicURL(cfg.SubscriptionBaseURL)
	subscriptionPath := normalizeSubscriptionPath(cfg.SubscriptionPath)

	var tr *http.Transport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = t.Clone()
	} else {
		tr = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,
		}
	}
	tr.MaxIdleConns = 100
	tr.MaxIdleConnsPerHost = 20

	return &Client{
		baseURL:             baseURL,
		subscriptionBaseURL: subscriptionBaseURL,
		subscriptionPath:    subscriptionPath,
		apiToken:            strings.TrimSpace(cfg.APIToken),
		httpClient: &http.Client{
			Timeout:   45 * time.Second,
			Jar:       jar,
			Transport: tr,
		},
	}, nil
}

func (c *Client) Login() error {
	if c.apiToken == "" {
		return fmt.Errorf("x-ui api_token is empty")
	}
	return nil
}

func (c *Client) GetInbounds() ([]Inbound, error) {
	var inbounds []Inbound
	if err := c.doRequest("GET", "/panel/api/inbounds/options", nil, &inbounds); err != nil {
		return nil, err
	}
	return inbounds, nil
}

func (c *Client) GetCachedInbounds() []Inbound {
	if c.Cache != nil {
		return c.Cache.GetAll()
	}
	return nil
}

func (c *Client) AddClient(req AddClientRequest) error {
	return c.AddClientResult(req).Err
}

func (c *Client) UpdateClient(email string, client ClientConfig) error {
	return c.UpdateClientResult(email, client).Err
}

func (c *Client) AddClientResult(req AddClientRequest) WriteResult {
	err := c.doRequest("POST", "/panel/api/clients/add", req, nil)
	if err == nil {
		return WriteResult{Outcome: WriteSucceeded}
	}
	if !isTimeoutError(err) {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: err}}
	}

	// A timeout is ambiguous. Read the client back before deciding whether the
	// create committed; never issue a second non-idempotent create blindly.
	remote, verifyErr := c.GetClientByEmail(req.Client.Email)
	if verifyErr == nil && remote != nil && clientMatchesAdd(*remote, req.Client) {
		return WriteResult{Outcome: WriteSucceeded}
	}
	unknownErr := fmt.Errorf("x-ui add client outcome is unknown for %s: %w", req.Client.Email, err)
	if verifyErr != nil {
		unknownErr = fmt.Errorf("x-ui add client outcome is unknown for %s: %w (verification: %v)", req.Client.Email, err, verifyErr)
	}
	return WriteResult{Outcome: WriteUnknown, Err: &WriteError{Outcome: WriteUnknown, Err: unknownErr}}
}

func (c *Client) UpdateClientResult(email string, client ClientConfig) WriteResult {
	current, err := c.GetClientByEmail(email)
	if err != nil {
		outcome := WriteDefinitiveFailure
		if isTimeoutError(err) {
			outcome = WriteUnknown
		}
		return WriteResult{Outcome: outcome, Err: &WriteError{Outcome: outcome, Err: fmt.Errorf("cannot read current x-ui client %s: %w", email, err)}}
	}
	merged := mergeClientConfig(*current, client)
	endpoint := "/panel/api/clients/update/" + pathEscape(email)
	err = c.doRequest("POST", endpoint, merged, nil)
	if err == nil {
		return WriteResult{Outcome: WriteSucceeded}
	}
	if !isTimeoutError(err) {
		return WriteResult{Outcome: WriteDefinitiveFailure, Err: &WriteError{Outcome: WriteDefinitiveFailure, Err: err}}
	}

	remote, verifyErr := c.GetClientByEmail(email)
	if verifyErr == nil && remote != nil && clientMatchesUpdate(*remote, merged) {
		return WriteResult{Outcome: WriteSucceeded}
	}
	unknownErr := fmt.Errorf("x-ui update client outcome is unknown for %s: %w", email, err)
	if verifyErr != nil {
		unknownErr = fmt.Errorf("x-ui update client outcome is unknown for %s: %w (verification: %v)", email, err, verifyErr)
	}
	return WriteResult{Outcome: WriteUnknown, Err: &WriteError{Outcome: WriteUnknown, Err: unknownErr}}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout() || errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

func wrapWriteError(err error) error {
	if err == nil {
		return nil
	}
	outcome := WriteDefinitiveFailure
	if isTimeoutError(err) {
		outcome = WriteUnknown
	}
	return &WriteError{Outcome: outcome, Err: err}
}

func clientMatchesAdd(remote XUIClientInfo, desired ClientConfig) bool {
	return remote.Email == desired.Email &&
		(remote.SubID == "" || desired.SubID == "" || remote.SubID == desired.SubID) &&
		(remote.ExpiryTime == desired.ExpiryTime || desired.ExpiryTime == 0) &&
		remote.Enable == desired.Enable && remote.LimitIP == desired.LimitIP
}

func clientMatchesUpdate(remote XUIClientInfo, desired ClientConfig) bool {
	return remote.Email == desired.Email && remote.Enable == desired.Enable &&
		remote.ExpiryTime == desired.ExpiryTime && remote.LimitIP == desired.LimitIP &&
		(remote.SubID == desired.SubID || desired.SubID == "") &&
		(remote.TotalGB == desired.TotalGB || desired.TotalGB == 0)
}

func mergeClientConfig(current XUIClientInfo, desired ClientConfig) ClientConfig {
	merged := ClientConfig{
		ID: strconv.Itoa(current.ID), Email: current.Email, Enable: current.Enable,
		ExpiryTime: current.ExpiryTime, Flow: current.Flow, Group: current.Group,
		LimitIP: current.LimitIP, Reset: current.Reset, Security: current.Security,
		SubID: current.SubID, TgID: current.TgID, TotalGB: current.TotalGB,
		Comment: current.Comment, Password: current.Password, Auth: current.Auth,
		LimitHWID: current.LimitHWID, ResetDay: current.ResetDay, ResetMax: current.ResetMax,
		KeepAlive: current.KeepAlive, ForwardedPorts: current.ForwardedPorts,
		PrivateKey: current.PrivateKey, PublicKey: current.PublicKey, PreSharedKey: current.PreSharedKey,
		AllowedIPs: current.AllowedIPs, AllowedIPsByInbound: current.AllowedIPsByInbound,
		Secret: current.Secret, AdTag: current.AdTag, TrafficReset: current.TrafficReset,
		TrafficResetDay: current.TrafficResetDay, Reverse: current.Reverse,
	}
	// These are the fields controlled by the bot. Metadata and newer panel
	// fields remain from the full current record.
	merged.Email = desired.Email
	merged.Enable = desired.Enable
	merged.ExpiryTime = desired.ExpiryTime
	merged.LimitIP = desired.LimitIP
	merged.SubID = desired.SubID
	merged.TgID = desired.TgID
	merged.TotalGB = desired.TotalGB
	if desired.Flow != "" {
		merged.Flow = desired.Flow
	}
	return merged
}

func (c *Client) DeleteClient(email string) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/del/"+pathEscape(email)+"?keepTraffic=0", nil, nil))
}

func (c *Client) AttachClient(email string, inboundIDs []int) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/"+pathEscape(email)+"/attach", attachRequest{InboundIDs: inboundIDs}, nil))
}

func (c *Client) GetSubscriptionLinks(subID string) ([]string, error) {
	var obj any
	err := c.doRequest("GET", "/panel/api/clients/subLinks/"+pathEscape(subID), nil, &obj)
	if err != nil {
		return nil, err
	}

	switch v := obj.(type) {
	case string:
		return c.publicSubscriptionLinks(subID, splitSubscriptionLinks(v)), nil
	case []any:
		links := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				links = append(links, splitSubscriptionLinks(s)...)
			}
		}
		return c.publicSubscriptionLinks(subID, links), nil
	case []string:
		return c.publicSubscriptionLinks(subID, v), nil
	default:
		return c.publicSubscriptionLinks(subID, nil), nil
	}
}

func (c *Client) GetClientTraffic(email string) (*ClientTraffic, error) {
	var traffic ClientTraffic
	if err := c.doRequest("GET", "/panel/api/clients/traffic/"+pathEscape(email), nil, &traffic); err != nil {
		return nil, err
	}
	return &traffic, nil
}

func (c *Client) publicSubscriptionLinks(subID string, links []string) []string {
	if c.subscriptionBaseURL == "" {
		return cleanSubscriptionLinks(links)
	}

	cleaned := cleanSubscriptionLinks(links)
	if len(cleaned) == 0 {
		return []string{c.SubscriptionURLFor(subID)}
	}

	out := make([]string, 0, len(cleaned))
	for _, link := range cleaned {
		out = append(out, c.publicSubscriptionLink(subID, link))
	}
	return out
}

func (c *Client) publicSubscriptionLink(subID, link string) string {
	parsed, err := url.Parse(link)
	if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		base, baseErr := url.Parse(c.subscriptionBaseURL)
		if baseErr != nil || base.Host == "" {
			return link
		}
		if base.Path != "" && base.Path != "/" {
			return c.SubscriptionURLFor(subID)
		}
		parsed.Scheme = base.Scheme
		parsed.Host = base.Host
		return parsed.String()
	}

	if !strings.Contains(link, "://") {
		return c.SubscriptionURLFor(link)
	}
	return link
}

func (c *Client) SubscriptionURLFor(subID string) string {
	base, err := url.Parse(c.subscriptionBaseURL)
	if err != nil || base.Host == "" {
		return subID
	}

	prefix := base.Path
	if prefix == "" || prefix == "/" {
		prefix = c.subscriptionPath
	}
	base.Path = joinURLPath(prefix, subID)
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func splitSubscriptionLinks(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	})
	links := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			links = append(links, field)
		}
	}
	return links
}

func cleanSubscriptionLinks(links []string) []string {
	if len(links) == 0 {
		return nil
	}
	out := make([]string, 0, len(links))
	seen := map[string]bool{}
	for _, link := range links {
		link = strings.TrimSpace(link)
		if link == "" || seen[link] {
			continue
		}
		seen[link] = true
		out = append(out, link)
	}
	return out
}

func normalizePublicURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	return strings.TrimRight(value, "/")
}

func normalizeSubscriptionPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/sub/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value
}

func joinURLPath(prefix, subID string) string {
	prefix = normalizeSubscriptionPath(prefix)
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(subID, "/")
}

type BulkAttachRequest struct {
	Emails     []string `json:"emails"`
	InboundIDs []int    `json:"inboundIds"`
}

func (c *Client) BulkAttach(req BulkAttachRequest) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/bulkAttach", req, nil))
}

func (c *Client) BulkDetach(req BulkAttachRequest) error {
	return wrapWriteError(c.doRequest("POST", "/panel/api/clients/bulkDetach", req, nil))
}

type BulkCreateItem struct {
	Client     ClientConfig `json:"client"`
	InboundIDs []int        `json:"inboundIds"`
}

type BulkCreateResponse struct {
	Created int                 `json:"created"`
	Skipped []BulkCreateSkipped `json:"skipped"`
}

type BulkCreateSkipped struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

func (c *Client) BulkCreate(req []BulkCreateItem) (*BulkCreateResponse, error) {
	var resp BulkCreateResponse
	if err := c.doRequest("POST", "/panel/api/clients/bulkCreate", req, &resp); err != nil {
		return nil, wrapWriteError(err)
	}
	return &resp, nil
}

func (c *Client) ListClients() ([]XUIClientInfo, error) {
	var clients []XUIClientInfo
	if err := c.doRequest("GET", "/panel/api/clients/list", nil, &clients); err != nil {
		return nil, err
	}
	return clients, nil
}

func (c *Client) GetClientEmailsByGroup(group string) ([]string, error) {
	var emails []string
	if err := c.doRequest("GET", "/panel/api/clients/groups/"+pathEscape(group)+"/emails", nil, &emails); err != nil {
		return nil, err
	}
	return emails, nil
}

func (c *Client) GetClientByEmail(email string) (*XUIClientInfo, error) {
	var client XUIClientInfo
	if err := c.doRequest("GET", "/panel/api/clients/get/"+pathEscape(email), nil, &client); err != nil {
		return nil, err
	}
	return &client, nil
}
