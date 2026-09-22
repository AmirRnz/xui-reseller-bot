package db

import (
	"context"
	"fmt"
	"log"
)

type Migration struct {
	Version int
	Name    string
	SQL     string
}

var migrations = []Migration{
	{
		Version: 2,
		Name:    "reconciliation_fields",
		SQL: `
ALTER TABLE reconciliation_records
    ADD COLUMN IF NOT EXISTS attempt_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS locked_by TEXT,
    ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resolution TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS manual_review_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_reconciliation_records_status_next_attempt
    ON reconciliation_records (status, next_attempt_at)
    WHERE status IN ('pending', 'pending_refund', 'reconciliation_required');
`,
	},
	{
		Version: 3,
		Name:    "purchase_quotes_and_integer_money",
		SQL: `
CREATE TABLE IF NOT EXISTS purchase_quotes (
    id BIGSERIAL PRIMARY KEY,
    quote_key TEXT UNIQUE NOT NULL,
    user_id BIGINT REFERENCES bot_users(id) ON DELETE SET NULL,
    plan_id BIGINT REFERENCES paid_plans(id) ON DELETE SET NULL,
    plan_name TEXT NOT NULL,
    months INT NOT NULL,
    duration_days INT NOT NULL,
    ip_limit INT NOT NULL,
    data_gb INT NOT NULL,
    base_price_toman BIGINT NOT NULL,
    extra_ip_price_toman BIGINT NOT NULL DEFAULT 0,
    extra_month_price_toman BIGINT NOT NULL DEFAULT 0,
    traffic_price_toman BIGINT NOT NULL DEFAULT 0,
    discount_toman BIGINT NOT NULL DEFAULT 0,
    final_price_toman BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'تومان',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_purchase_quotes_user ON purchase_quotes (user_id);
CREATE INDEX IF NOT EXISTS idx_purchase_quotes_key ON purchase_quotes (quote_key);

ALTER TABLE purchase_requests
    ADD COLUMN IF NOT EXISTS quote_id BIGINT REFERENCES purchase_quotes(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS price_toman BIGINT,
    ADD COLUMN IF NOT EXISTS operation_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS purchase_requests_operation_key_uq
    ON purchase_requests (operation_key)
    WHERE operation_key IS NOT NULL;

UPDATE purchase_requests SET price_toman = ROUND(price) WHERE price_toman IS NULL AND price IS NOT NULL;

ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS quote_id BIGINT REFERENCES purchase_quotes(id) ON DELETE SET NULL;
`,
	},
	{
		Version: 4,
		Name:    "notifications_outbox",
		SQL: `
CREATE TABLE IF NOT EXISTS notifications_outbox (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE CASCADE,
    user_id BIGINT REFERENCES bot_users(id) ON DELETE CASCADE,
    notification_type TEXT NOT NULL,
    effective_date DATE NOT NULL,
    channel TEXT NOT NULL DEFAULT 'telegram',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_at TIMESTAMPTZ,
    locked_by TEXT,
    sent_at TIMESTAMPTZ,
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_notifications_outbox_identity UNIQUE (subscription_id, notification_type, effective_date, channel)
);

CREATE INDEX IF NOT EXISTS idx_notifications_outbox_pending
    ON notifications_outbox (status, next_attempt_at)
    WHERE status IN ('pending', 'retryable');
`,
	},
	{
		Version: 5,
		Name:    "lifecycle_indexes",
		SQL: `
CREATE INDEX IF NOT EXISTS idx_subscriptions_user_status ON subscriptions (user_id, status);
CREATE INDEX IF NOT EXISTS idx_subscriptions_status ON subscriptions (status);
`,
	},
	{
		Version: 6,
		Name:    "reconciliation_cas_payment_intents_bulk_credits_and_integer_pricing",
		SQL: `
ALTER TABLE reconciliation_records
    ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 1;

ALTER TABLE paid_plans
    ADD COLUMN IF NOT EXISTS base_price_toman BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS price_per_extra_ip_toman BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS price_per_gb_toman BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS price_per_extra_month_toman BIGINT NOT NULL DEFAULT 0;

UPDATE paid_plans SET
    base_price_toman = COALESCE(base_price_toman, ROUND(base_price)::BIGINT, 0),
    price_per_extra_ip_toman = COALESCE(price_per_extra_ip_toman, ROUND(price_per_extra_ip)::BIGINT, 0),
    price_per_gb_toman = COALESCE(price_per_gb_toman, ROUND(price_per_gb)::BIGINT, 0),
    price_per_extra_month_toman = COALESCE(price_per_extra_month_toman, ROUND(price_per_extra_month)::BIGINT, 0)
WHERE base_price_toman = 0 AND base_price > 0;

CREATE TABLE IF NOT EXISTS payment_intents (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    intent_token TEXT UNIQUE NOT NULL,
    action_type TEXT NOT NULL,
    plan_id BIGINT REFERENCES paid_plans(id) ON DELETE SET NULL,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE SET NULL,
    quote_id BIGINT REFERENCES purchase_quotes(id) ON DELETE SET NULL,
    amount_toman BIGINT NOT NULL,
    months INT NOT NULL DEFAULT 1,
    ip_limit INT NOT NULL DEFAULT 1,
    data_gb INT NOT NULL DEFAULT 0,
    display_name TEXT NOT NULL DEFAULT '',
    client_email TEXT NOT NULL DEFAULT '',
    provisioning_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    status TEXT NOT NULL DEFAULT 'awaiting_receipt',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_intents_user_status ON payment_intents (user_id, status);

CREATE TABLE IF NOT EXISTS bulk_credit_operations (
    id BIGSERIAL PRIMARY KEY,
    operation_key TEXT UNIQUE NOT NULL,
    amount BIGINT NOT NULL,
    admin_id BIGINT,
    recipient_user_ids BIGINT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'completed',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`,
	},
	{
		Version: 7,
		Name:    "corrective_integer_toman_pricing_and_topup_idempotency",
		SQL: `
UPDATE paid_plans SET
    base_price_toman = CASE WHEN base_price_toman = 0 AND base_price > 0 THEN ROUND(base_price)::BIGINT ELSE base_price_toman END,
    price_per_extra_ip_toman = CASE WHEN price_per_extra_ip_toman = 0 AND price_per_extra_ip > 0 THEN ROUND(price_per_extra_ip)::BIGINT ELSE price_per_extra_ip_toman END,
    price_per_gb_toman = CASE WHEN price_per_gb_toman = 0 AND price_per_gb > 0 THEN ROUND(price_per_gb)::BIGINT ELSE price_per_gb_toman END,
    price_per_extra_month_toman = CASE WHEN price_per_extra_month_toman = 0 AND price_per_extra_month > 0 THEN ROUND(price_per_extra_month)::BIGINT ELSE price_per_extra_month_toman END
WHERE (base_price_toman = 0 AND base_price > 0)
   OR (price_per_extra_ip_toman = 0 AND price_per_extra_ip > 0)
   OR (price_per_gb_toman = 0 AND price_per_gb > 0)
   OR (price_per_extra_month_toman = 0 AND price_per_extra_month > 0);

ALTER TABLE topup_requests
    ADD COLUMN IF NOT EXISTS operation_key TEXT UNIQUE;
`,
	},
}

func runMigrations(ctx context.Context) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return fmt.Errorf("database pool is not initialized")
	}

	// Serialize concurrent migration runs during parallel testing
	_, _ = Pool.Exec(ctx, `SELECT pg_advisory_lock(742948214)`)
	defer func() {
		_, _ = Pool.Exec(ctx, `SELECT pg_advisory_unlock(742948214)`)
	}()

	_, err := Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	var baselineApplied bool
	err = Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 1)`).Scan(&baselineApplied)
	if err != nil {
		return fmt.Errorf("failed to check baseline migration status: %w", err)
	}

	if !baselineApplied {
		_, err := Pool.Exec(ctx, schemaSQL)
		if err != nil {
			return fmt.Errorf("failed to execute baseline schema.sql: %w", err)
		}
		_, err = Pool.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES (1, 'baseline') ON CONFLICT (version) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("failed to record baseline migration: %w", err)
		}
		log.Println("[MIGRATE] Applied version 1: baseline")
	}

	for _, m := range migrations {
		var alreadyApplied bool
		err = Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.Version).Scan(&alreadyApplied)
		if err != nil {
			return fmt.Errorf("failed to check migration %d status: %w", m.Version, err)
		}
		if alreadyApplied {
			continue
		}

		tx, err := Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("failed to begin tx for migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to apply migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to record migration %d (%s): %w", m.Version, m.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", m.Version, err)
		}
		log.Printf("[MIGRATE] Applied version %d: %s", m.Version, m.Name)
	}

	return nil
}
