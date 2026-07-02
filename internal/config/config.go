package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var Global *Config

type Config struct {
	Bot      BotConfig      `yaml:"bot"`
	Database DatabaseConfig `yaml:"database"`
	XUI      XUIConfig      `yaml:"xui"`
	Admin    AdminConfig    `yaml:"admin"`
}

type BotConfig struct {
	Token         string `yaml:"token"`
	WebhookDomain string `yaml:"webhook_domain"`
	WebhookPort   int    `yaml:"webhook_port"`
	TLSKeyFile    string `yaml:"tls_key_file"`
	TLSCertFile   string `yaml:"tls_cert_file"`
}

type DatabaseConfig struct {
	URL    string `yaml:"url"`
	Host   string `yaml:"host"`
	Port   int    `yaml:"port"`
	User   string `yaml:"user"`
	DBName string `yaml:"dbname"`
}

type XUIConfig struct {
	URL                 string `yaml:"url"`
	BaseURL             string `yaml:"base_url"`
	SubscriptionBaseURL string `yaml:"subscription_base_url"`
	SubscriptionPath    string `yaml:"subscription_path"`
	APIToken            string `yaml:"api_token"`
}

type AdminConfig struct {
	AdminIDs []int64 `yaml:"admin_ids"`
}

func Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if cfg.XUI.URL == "" {
		cfg.XUI.URL = cfg.XUI.BaseURL
	}
	if cfg.XUI.BaseURL == "" {
		cfg.XUI.BaseURL = cfg.XUI.URL
	}
	if cfg.XUI.SubscriptionPath == "" {
		cfg.XUI.SubscriptionPath = "/sub/"
	}
	if cfg.Bot.WebhookPort == 0 {
		cfg.Bot.WebhookPort = 88
	}
	if err := fillDatabaseURLParts(&cfg.Database); err != nil {
		return err
	}

	Global = &cfg
	return nil
}

func fillDatabaseURLParts(cfg *DatabaseConfig) error {
	if cfg == nil || cfg.URL == "" {
		return nil
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("failed to parse database URL: %w", err)
	}
	if cfg.Host == "" {
		cfg.Host = parsed.Hostname()
	}
	if cfg.Port == 0 {
		if portStr := parsed.Port(); portStr != "" {
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return fmt.Errorf("invalid port in database URL: %w", err)
			}
			cfg.Port = port
		}
	}
	if cfg.User == "" && parsed.User != nil {
		cfg.User = parsed.User.Username()
	}
	if cfg.DBName == "" {
		cfg.DBName = strings.TrimPrefix(parsed.Path, "/")
	}
	return nil
}
