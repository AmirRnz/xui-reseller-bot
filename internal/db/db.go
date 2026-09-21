package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"xui-reseller-bot/internal/config"
)

var Pool *pgxpool.Pool

//go:embed schema.sql
var schemaSQL string

func Connect(ctx context.Context, cfg *config.DatabaseConfig) error {
	if cfg == nil || strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("database url is empty")
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return fmt.Errorf("unable to parse database url: %w", err)
	}

	// Optimize for 1GB / 1 CPU Core server
	poolConfig.MaxConns = 5
	poolConfig.MinConns = 0

	Pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("unable to create connection pool: %w", err)
	}

	if err := Pool.Ping(ctx); err != nil {
		return fmt.Errorf("unable to ping database: %w", err)
	}

	return nil
}

func Migrate(ctx context.Context) error {
	return runMigrations(ctx)
}

func dbCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 10*time.Second)
}

func NormalizeIPLimits(ctx context.Context) error {
	var migrated string
	err := Pool.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = 'ip_limit_migrated'`).Scan(&migrated)
	if err == nil && migrated == "true" {
		return nil
	}

	var factor string
	err = Pool.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = 'ip_limit_factor'`).Scan(&factor)
	if err != nil {
		factor = ""
	}

	factor = strings.TrimSpace(factor)
	if factor != "" && strings.HasPrefix(factor, "*") {
		var mult int
		_, err := fmt.Sscanf(factor, "*%d", &mult)
		if err == nil && mult > 1 {
			_, err = Pool.Exec(ctx, `
				UPDATE subscriptions 
				SET ip_limit = ip_limit / $1 
				WHERE ip_limit >= $1
			`, mult)
			if err != nil {
				return fmt.Errorf("failed to normalize subscriptions ip_limit: %w", err)
			}
		}
	}

	_, err = Pool.Exec(ctx, `
		INSERT INTO bot_settings (key, value, updated_at) 
		VALUES ('ip_limit_migrated', 'true', NOW())
		ON CONFLICT (key) DO UPDATE SET value = 'true', updated_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("failed to save ip_limit_migrated setting: %w", err)
	}

	return nil
}
