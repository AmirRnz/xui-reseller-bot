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
ALTER TABLE topup_requests
    ADD COLUMN IF NOT EXISTS operation_key TEXT UNIQUE;
`,
	},
	{
		Version: 8,
		Name:    "legacy_currency_unit_preflight_report",
		SQL: `
CREATE TABLE IF NOT EXISTS currency_unit_preflight (
    id BIGSERIAL PRIMARY KEY,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    currency_name TEXT NOT NULL,
    paid_plan_values JSONB NOT NULL DEFAULT '[]'::jsonb,
    wallet_values JSONB NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO currency_unit_preflight (currency_name, paid_plan_values, wallet_values)
SELECT
    COALESCE((SELECT value FROM bot_settings WHERE key = 'currency_name'), '<missing>'),
    COALESCE((SELECT jsonb_agg(jsonb_build_object(
        'plan_id', id, 'name', name, 'base_price', base_price,
        'base_price_toman', base_price_toman,
        'price_per_extra_ip', price_per_extra_ip,
        'price_per_extra_ip_toman', price_per_extra_ip_toman,
        'price_per_gb', price_per_gb, 'price_per_gb_toman', price_per_gb_toman,
        'price_per_extra_month', price_per_extra_month,
        'price_per_extra_month_toman', price_per_extra_month_toman,
        'discount_tiers', discount_tiers
    ) ORDER BY id) FROM (SELECT * FROM paid_plans ORDER BY id LIMIT 20) p), '[]'::jsonb),
    jsonb_build_object(
        'user_count', (SELECT COUNT(*) FROM bot_users),
        'wallet_balance_sum', (SELECT COALESCE(SUM(wallet_balance), 0) FROM bot_users),
        'wallet_balance_min', (SELECT COALESCE(MIN(wallet_balance), 0) FROM bot_users),
        'wallet_balance_max', (SELECT COALESCE(MAX(wallet_balance), 0) FROM bot_users),
        'sample', COALESCE((SELECT jsonb_agg(jsonb_build_object('user_id', id, 'wallet_balance', wallet_balance) ORDER BY id)
                            FROM (SELECT id, wallet_balance FROM bot_users ORDER BY id LIMIT 20) u), '[]'::jsonb)
    );
`,
	},
	{
		Version: 9,
		Name:    "durable_purchase_snapshots_and_active_payment_intent_invariant",
		SQL: `
ALTER TABLE purchase_requests
    ADD COLUMN IF NOT EXISTS provisioning_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE payment_intents SET action_type = 'buy' WHERE action_type = 'new_subscription';
UPDATE purchase_requests SET type = 'buy' WHERE type = 'new_subscription';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM payment_intents
        WHERE status = 'awaiting_receipt'
        GROUP BY user_id HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'multiple active payment intents exist for a user; resolve them explicitly before applying migration 9';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS payment_intents_one_awaiting_receipt_per_user
    ON payment_intents (user_id)
    WHERE status = 'awaiting_receipt';
`,
	},
	{
		Version: 10,
		Name:    "auditable_reconciliation_manual_actions",
		SQL: `
ALTER TABLE reconciliation_records
    ADD COLUMN IF NOT EXISTS manual_admin_id BIGINT,
    ADD COLUMN IF NOT EXISTS manual_action_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS manual_action_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS manual_action_amount BIGINT,
    ADD COLUMN IF NOT EXISTS manual_action_operation_key TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version: 11,
		Name:    "auditable_editable_refund_approval",
		SQL: `
ALTER TABLE refund_requests
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS audit_note TEXT NOT NULL DEFAULT '';
`,
	},
	{
		Version: 12,
		Name:    "integer_discount_basis_points_from_preflighted_legacy_values",
		SQL: `
UPDATE paid_plans p
SET discount_tiers = (
    SELECT COALESCE(jsonb_agg(
        CASE
            WHEN tier ? 'basis_points' THEN tier
            WHEN COALESCE(tier->>'percent', '') ~ '^[0-9]+([.][0-9]+)?$'
                THEN jsonb_set(tier, '{basis_points}', to_jsonb(ROUND((tier->>'percent')::numeric * 100)::BIGINT), TRUE)
            ELSE tier
        END ORDER BY ordinal
    ), '[]'::jsonb)
    FROM jsonb_array_elements(COALESCE(p.discount_tiers, '[]'::jsonb)) WITH ORDINALITY AS t(tier, ordinal)
)
WHERE p.discount_tiers IS NOT NULL;
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
