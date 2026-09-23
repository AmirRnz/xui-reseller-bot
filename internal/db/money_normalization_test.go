package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMoneyNormalizationAuditCapturesDeploymentEvidence(t *testing.T) {
	ctx := setupTestDB(t)
	state, err := GetMoneyNormalizationState(ctx)
	if err != nil {
		t.Fatalf("load money normalization state: %v", err)
	}
	if state.DatabaseName == "" || state.ConfirmationToken == "" || len(state.Report) == 0 {
		t.Fatalf("money audit is missing deployment identity or report: %+v", state)
	}
	var migrations []struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(state.CurrentSchemaVersions, &migrations); err != nil {
		t.Fatalf("decode current schema migrations: %v", err)
	}
	if len(migrations) == 0 || migrations[len(migrations)-1].Version < 14 {
		t.Fatalf("current migration history is missing from audit output: %s", state.CurrentSchemaVersions)
	}
	var report map[string]json.RawMessage
	if err := json.Unmarshal(state.Report, &report); err != nil {
		t.Fatalf("decode captured report: %v", err)
	}
	for _, key := range []string{"schema_migrations", "currency_name", "paid_plans", "wallet_balances", "transactions", "quotes", "purchase_requests"} {
		if _, ok := report[key]; !ok {
			t.Errorf("captured money audit has no %q section", key)
		}
	}
}

func TestRialNormalizationConvertsLegacyMoneyOnceInTransaction(t *testing.T) {
	ctx := setupTestDB(t)
	tx, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin isolated normalization transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	nowID := time.Now().UnixNano()
	var userID, planID, quoteID int64
	if err := tx.QueryRow(ctx, `INSERT INTO bot_users (telegram_id, username, status, wallet_balance) VALUES ($1, $2, 'approved', 1000000) RETURNING id`, nowID, fmt.Sprintf("money_norm_%d", nowID)).Scan(&userID); err != nil {
		t.Fatalf("create wallet fixture: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO paid_plans (name, base_price, base_price_toman) VALUES ($1, 1000000, 1000000) RETURNING id`, fmt.Sprintf("money_plan_%d", nowID)).Scan(&planID); err != nil {
		t.Fatalf("create old-v7 price fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO transactions (user_id, amount, type) VALUES ($1, 100000, 'credit')`, userID); err != nil {
		t.Fatalf("create ledger fixture: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO purchase_quotes (quote_key, user_id, plan_id, plan_name, months, duration_days, ip_limit, data_gb, base_price_toman, final_price_toman)
		VALUES ($1, $2, $3, 'Money fixture', 1, 30, 1, 0, 100000, 100000) RETURNING id
	`, fmt.Sprintf("money_quote_%d", nowID), userID, planID).Scan(&quoteID); err != nil {
		t.Fatalf("create quote fixture: %v", err)
	}
	if err := applyLegacyMoneyUnit(ctx, tx, "rial"); err != nil {
		t.Fatalf("normalize fixture as Rial: %v", err)
	}

	var walletBalance, planPrice, transactionAmount, quoteAmount int64
	var floatPrice float64
	if err := tx.QueryRow(ctx, `SELECT wallet_balance FROM bot_users WHERE id = $1`, userID).Scan(&walletBalance); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT base_price_toman, base_price FROM paid_plans WHERE id = $1`, planID).Scan(&planPrice, &floatPrice); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT amount FROM transactions WHERE user_id = $1`, userID).Scan(&transactionAmount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT final_price_toman FROM purchase_quotes WHERE id = $1`, quoteID).Scan(&quoteAmount); err != nil {
		t.Fatal(err)
	}
	if walletBalance != 100000 || planPrice != 100000 || floatPrice != 100000 || transactionAmount != 10000 || quoteAmount != 10000 {
		t.Fatalf("Rial values were not converted consistently: wallet=%d plan=%d mirror=%v transaction=%d quote=%d", walletBalance, planPrice, floatPrice, transactionAmount, quoteAmount)
	}
}

func TestScaleMoneyJSONChangesMoneyFieldsAndKeepsIdentityFields(t *testing.T) {
	raw := []byte(`{"amount_toman":10000,"identity":{"id":123,"base_price":5000},"unrelated":7}`)
	scaled, err := scaleMoneyJSON(raw)
	if err != nil {
		t.Fatalf("scale JSON money: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(scaled, &result); err != nil {
		t.Fatalf("decode scaled JSON: %v", err)
	}
	identity := result["identity"].(map[string]any)
	if result["amount_toman"] != float64(1000) || identity["id"] != float64(123) || identity["base_price"] != float64(500) || result["unrelated"] != float64(7) {
		t.Fatalf("unexpected JSON money normalization: %s", scaled)
	}
}

func TestV5MoneyUpgradeWaitsForOperatorUnitDecision(t *testing.T) {
	ctx := setupTestDB(t)
	originalPool := Pool
	schema := fmt.Sprintf("money_v5_%d", time.Now().UnixNano())
	if _, err := originalPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create isolated v5 schema: %v", err)
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
		t.Fatalf("connect isolated v5 schema: %v", err)
	}
	Pool = isolatedPool
	t.Cleanup(func() {
		Pool = originalPool
		isolatedPool.Close()
		_, _ = originalPool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	legacySchema := `
CREATE TABLE schema_migrations (version INT PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO schema_migrations (version, name) VALUES (1, 'baseline'), (2, 'reconciliation_fields'), (3, 'purchase_quotes'), (4, 'notifications'), (5, 'lifecycle_indexes');
CREATE TABLE bot_users (id BIGSERIAL PRIMARY KEY, telegram_id BIGINT UNIQUE NOT NULL, username TEXT NOT NULL DEFAULT '', wallet_balance BIGINT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE bot_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO bot_settings (key, value) VALUES ('currency_name', 'IRR'), ('min_topup_amount', '50000');
CREATE TABLE paid_plans (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, base_price NUMERIC(14,2) NOT NULL DEFAULT 0, base_ip_limit INT NOT NULL DEFAULT 1, max_ip_limit INT NOT NULL DEFAULT 1, price_per_extra_ip NUMERIC(14,2) NOT NULL DEFAULT 0, price_per_gb NUMERIC(14,2) NOT NULL DEFAULT 0, price_per_extra_month NUMERIC(14,2) NOT NULL DEFAULT 0, discount_tiers JSONB NOT NULL DEFAULT '[]'::jsonb);
CREATE TABLE subscriptions (id BIGSERIAL PRIMARY KEY, client_email TEXT UNIQUE NOT NULL, ip_limit INT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE reconciliation_records (id BIGSERIAL PRIMARY KEY, operation_key TEXT UNIQUE NOT NULL, kind TEXT NOT NULL, desired_state JSONB NOT NULL DEFAULT '{}'::jsonb, observed_state JSONB NOT NULL DEFAULT '{}'::jsonb, status TEXT NOT NULL DEFAULT 'pending', error_message TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE topup_requests (id BIGSERIAL PRIMARY KEY, user_id BIGINT, amount BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE transactions (id BIGSERIAL PRIMARY KEY, user_id BIGINT, amount BIGINT NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'completed', description TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE purchase_quotes (id BIGSERIAL PRIMARY KEY, quote_key TEXT UNIQUE NOT NULL, user_id BIGINT, plan_id BIGINT, plan_name TEXT NOT NULL, months INT NOT NULL, duration_days INT NOT NULL, ip_limit INT NOT NULL, data_gb INT NOT NULL, base_price_toman BIGINT NOT NULL, extra_ip_price_toman BIGINT NOT NULL DEFAULT 0, extra_month_price_toman BIGINT NOT NULL DEFAULT 0, traffic_price_toman BIGINT NOT NULL DEFAULT 0, discount_toman BIGINT NOT NULL DEFAULT 0, final_price_toman BIGINT NOT NULL, currency TEXT NOT NULL DEFAULT 'تومان', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE purchase_requests (id BIGSERIAL PRIMARY KEY, user_id BIGINT, type TEXT NOT NULL, plan_id BIGINT, subscription_id BIGINT, quote_id BIGINT, price NUMERIC(14,2) NOT NULL DEFAULT 0, price_toman BIGINT, months INT NOT NULL DEFAULT 0, ip_limit INT NOT NULL DEFAULT 0, data_gb INT NOT NULL DEFAULT 0, custom_name TEXT NOT NULL DEFAULT '', client_email TEXT NOT NULL DEFAULT '', telegram_file_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', provisioning_status TEXT NOT NULL DEFAULT 'pending', operation_key TEXT, admin_id BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
CREATE TABLE refund_requests (id BIGSERIAL PRIMARY KEY, user_id BIGINT, calculated_amount BIGINT NOT NULL DEFAULT 0, approved_amount BIGINT, status TEXT NOT NULL DEFAULT 'pending', admin_id BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
INSERT INTO paid_plans (name, base_price, price_per_extra_ip, price_per_gb, price_per_extra_month) VALUES ('Legacy Rial plan', 1000000, 200000, 50000, 300000);
INSERT INTO bot_users (telegram_id, username, wallet_balance) VALUES (818181818, 'legacy_money_user', 750000);
`
	if _, err := Pool.Exec(ctx, legacySchema); err != nil {
		t.Fatalf("create v5 production-style fixture: %v", err)
	}
	if err := Migrate(ctx); err != nil {
		t.Fatalf("upgrade v5 fixture: %v", err)
	}
	state, err := GetMoneyNormalizationState(ctx)
	if err != nil {
		t.Fatalf("read upgraded audit: %v", err)
	}
	var v5Report map[string]json.RawMessage
	if err := json.Unmarshal(state.Report, &v5Report); err != nil {
		t.Fatalf("decode v5 money report: %v", err)
	}
	if _, ok := v5Report["money_settings"]; !ok {
		t.Fatal("v5 money report omitted monetary settings")
	}
	if state.NormalizedAt != nil {
		t.Fatal("v5 upgrade must remain blocked until an operator chooses the historical unit")
	}
	if err := EnsureMoneyNormalizationComplete(ctx); err == nil {
		t.Fatal("commerce readiness passed before an operator unit decision")
	}
	var planPrice int64
	if err := Pool.QueryRow(ctx, `SELECT base_price_toman FROM paid_plans WHERE name = 'Legacy Rial plan'`).Scan(&planPrice); err != nil {
		t.Fatal(err)
	}
	if planPrice != 0 {
		t.Fatalf("migration guessed an integer price before operator approval: %d", planPrice)
	}
	if _, err := NormalizeLegacyMoney(ctx, "rial", "test-operator", state.ConfirmationToken); err != nil {
		t.Fatalf("apply explicit Rial decision: %v", err)
	}
	if err := EnsureMoneyNormalizationComplete(ctx); err != nil {
		t.Fatalf("commerce readiness remains blocked after normalization: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT base_price_toman FROM paid_plans WHERE name = 'Legacy Rial plan'`).Scan(&planPrice); err != nil {
		t.Fatal(err)
	}
	if planPrice != 100000 {
		t.Fatalf("operator-selected Rial conversion produced %d, want 100000", planPrice)
	}
	var topupMinimum, currencyName string
	if err := Pool.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = 'min_topup_amount'`).Scan(&topupMinimum); err != nil {
		t.Fatal(err)
	}
	if err := Pool.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = 'currency_name'`).Scan(&currencyName); err != nil {
		t.Fatal(err)
	}
	if topupMinimum != "5000" || currencyName != "تومان" {
		t.Fatalf("money settings were not normalized: minimum=%q currency=%q", topupMinimum, currencyName)
	}
	var candidateID, excludedID int64
	if err := Pool.QueryRow(ctx, `INSERT INTO subscriptions (client_email, ip_limit) VALUES ('repair_candidate@example.test', 20000) RETURNING id`).Scan(&candidateID); err != nil {
		t.Fatalf("create divisible IP limit fixture: %v", err)
	}
	if err := Pool.QueryRow(ctx, `INSERT INTO subscriptions (client_email, ip_limit) VALUES ('repair_excluded@example.test', 15001) RETURNING id`).Scan(&excludedID); err != nil {
		t.Fatalf("create non-divisible IP limit fixture: %v", err)
	}
	preview, err := PreviewLegacyIPLimitRepair(ctx, 100)
	if err != nil {
		t.Fatalf("preview one-time IP repair: %v", err)
	}
	if len(preview.Affected) != 1 || preview.Affected[0].SubscriptionID != candidateID || preview.Affected[0].RepairedLimit != 200 || len(preview.Excluded) != 1 || preview.Excluded[0].SubscriptionID != excludedID {
		t.Fatalf("dry-run did not report candidates and excluded rows: %+v", preview)
	}
	if _, err := ApplyLegacyIPLimitRepair(ctx, 100, "test-operator", preview.PreviewToken); err != nil {
		t.Fatalf("apply explicitly confirmed IP repair: %v", err)
	}
	var repairedLimit, excludedLimit int
	if err := Pool.QueryRow(ctx, `SELECT ip_limit FROM subscriptions WHERE id = $1`, candidateID).Scan(&repairedLimit); err != nil {
		t.Fatal(err)
	}
	if err := Pool.QueryRow(ctx, `SELECT ip_limit FROM subscriptions WHERE id = $1`, excludedID).Scan(&excludedLimit); err != nil {
		t.Fatal(err)
	}
	if repairedLimit != 200 || excludedLimit != 15001 {
		t.Fatalf("IP repair changed unexpected values: candidate=%d excluded=%d", repairedLimit, excludedLimit)
	}
	if _, err := ApplyLegacyIPLimitRepair(ctx, 100, "test-operator", preview.PreviewToken); !errors.Is(err, ErrIPLimitRepairAlreadyApplied) {
		t.Fatalf("reapplying IP repair should be refused, got: %v", err)
	}
}
