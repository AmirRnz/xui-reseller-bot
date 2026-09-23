package reconcile

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"xui-reseller-bot/internal/db"
)

// setupIsolatedTestDBForReconcile gives worker integration tests a private
// schema so ProcessOnce cannot claim unrelated durable work left by other tests.
func setupIsolatedTestDBForReconcile(t *testing.T) context.Context {
	t.Helper()
	ctx := setupTestDBForReconcile(t)
	originalPool := db.Pool
	schema := fmt.Sprintf("reconcile_test_%d", time.Now().UnixNano())
	if _, err := originalPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create isolated reconcile schema: %v", err)
	}
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	poolConfig.MaxConns = 1
	poolConfig.MinConns = 0
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	isolatedPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect isolated reconcile schema: %v", err)
	}
	db.Pool = isolatedPool
	t.Cleanup(func() {
		db.Pool = originalPool
		isolatedPool.Close()
		_, _ = originalPool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate isolated reconcile schema: %v", err)
	}
	return ctx
}
