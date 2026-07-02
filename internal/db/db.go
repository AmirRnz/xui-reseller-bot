package db

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"xui-end-bot/internal/config"
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
	_, err := Pool.Exec(ctx, schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to execute schema.sql: %w", err)
	}

	return nil
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

