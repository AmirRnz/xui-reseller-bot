package xui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"xui-end-bot/internal/config"
)

type Client struct {
	baseURL             string
	subscriptionBaseURL string
	subscriptionPath    string
	apiToken            string
	httpClient          *http.Client
	Cache               *InboundCache
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
	return c.doRequest("POST", "/panel/api/clients/add", req, nil)
}

func (c *Client) UpdateClient(email string, client ClientConfig) error {
	endpoint := "/panel/api/clients/update/" + pathEscape(email)
	// Current 3x-ui accepts the raw client payload; older mocks in this repo
	// expect {"client": ...}. doRequestWithFallback keeps both compatible.
	err := c.doRequest("POST", endpoint, client, nil)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) {
			// Do not retry on network/timeout errors to avoid double penalty timeouts on offline hosts
			return err
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return err
		}
		return c.doRequest("POST", endpoint, UpdateClientRequest{Client: client}, nil)
	}
	return nil
}

func (c *Client) DeleteClient(email string) error {
	return c.doRequest("POST", "/panel/api/clients/del/"+pathEscape(email)+"?keepTraffic=0", nil, nil)
}

func (c *Client) AttachClient(email string, inboundIDs []int) error {
	return c.doRequest("POST", "/panel/api/clients/"+pathEscape(email)+"/attach", attachRequest{InboundIDs: inboundIDs}, nil)
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
	return c.doRequest("POST", "/panel/api/clients/bulkAttach", req, nil)
}

func (c *Client) BulkDetach(req BulkAttachRequest) error {
	return c.doRequest("POST", "/panel/api/clients/bulkDetach", req, nil)
}

type BulkCreateItem struct {
	Client     ClientConfig `json:"client"`
	InboundIDs []int        `json:"inboundIds"`
}

type BulkCreateResponse struct {
	Created int `json:"created"`
	Skipped []BulkCreateSkipped `json:"skipped"`
}

type BulkCreateSkipped struct {
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

func (c *Client) BulkCreate(req []BulkCreateItem) (*BulkCreateResponse, error) {
	var resp BulkCreateResponse
	if err := c.doRequest("POST", "/panel/api/clients/bulkCreate", req, &resp); err != nil {
		return nil, err
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


