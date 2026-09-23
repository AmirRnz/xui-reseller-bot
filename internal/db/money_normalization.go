package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrMoneyNormalizationAlreadyApplied = errors.New("money normalization was already applied")

type MoneyNormalizationState struct {
	DatabaseName          string          `json:"database_name"`
	CapturedAt            time.Time       `json:"captured_at"`
	ConfirmationToken     string          `json:"confirmation_token"`
	Report                json.RawMessage `json:"report"`
	CurrentSchemaVersions json.RawMessage `json:"current_schema_migrations"`
	LegacyV7CopyCandidate bool            `json:"legacy_v7_copy_candidate"`
	SelectedUnit          string          `json:"selected_unit,omitempty"`
	NormalizedBy          string          `json:"normalized_by,omitempty"`
	NormalizedAt          *time.Time      `json:"normalized_at,omitempty"`
}

func GetMoneyNormalizationState(ctx context.Context) (*MoneyNormalizationState, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var state MoneyNormalizationState
	err := Pool.QueryRow(ctx, `
		SELECT current_database(), captured_at, report,
	       COALESCE((SELECT jsonb_agg(jsonb_build_object('version', version, 'name', name, 'applied_at', applied_at) ORDER BY version) FROM schema_migrations), '[]'::jsonb),
	       legacy_v7_copy_candidate,
	       COALESCE(selected_unit, ''), normalized_by, normalized_at
		FROM money_normalization_audit WHERE singleton = TRUE
	`).Scan(&state.DatabaseName, &state.CapturedAt, &state.Report, &state.CurrentSchemaVersions, &state.LegacyV7CopyCandidate,
		&state.SelectedUnit, &state.NormalizedBy, &state.NormalizedAt)
	if err != nil {
		return nil, fmt.Errorf("load money normalization audit: %w", err)
	}
	state.ConfirmationToken = moneyNormalizationToken(state.DatabaseName, state.CapturedAt)
	return &state, nil
}

func EnsureMoneyNormalizationComplete(ctx context.Context) error {
	state, err := GetMoneyNormalizationState(ctx)
	if err != nil {
		return err
	}
	if state.NormalizedAt == nil {
		return fmt.Errorf("money-unit audit is pending for database %q; run `go run ./cmd/money-normalize -report`, review the report, then explicitly apply a unit decision", state.DatabaseName)
	}
	return nil
}

func NormalizeLegacyMoney(ctx context.Context, unit, operator, confirmationToken string) (*MoneyNormalizationState, error) {
	ctx, cancel := moneyNormalizationCtx(ctx)
	defer cancel()

	unit = strings.ToLower(strings.TrimSpace(unit))
	operator = strings.TrimSpace(operator)
	if unit != "toman" && unit != "rial" {
		return nil, errors.New("unit must be either 'toman' or 'rial'")
	}
	if operator == "" {
		return nil, errors.New("operator must identify the person making the unit decision")
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var state MoneyNormalizationState
	err = tx.QueryRow(ctx, `
		SELECT current_database(), captured_at, report, legacy_v7_copy_candidate,
		       COALESCE(selected_unit, ''), normalized_by, normalized_at
		FROM money_normalization_audit WHERE singleton = TRUE FOR UPDATE
	`).Scan(&state.DatabaseName, &state.CapturedAt, &state.Report, &state.LegacyV7CopyCandidate,
		&state.SelectedUnit, &state.NormalizedBy, &state.NormalizedAt)
	if err != nil {
		return nil, fmt.Errorf("load money normalization audit: %w", err)
	}
	state.ConfirmationToken = moneyNormalizationToken(state.DatabaseName, state.CapturedAt)
	if !strings.EqualFold(strings.TrimSpace(confirmationToken), state.ConfirmationToken) {
		return nil, fmt.Errorf("confirmation token does not match this database audit; expected %q", state.ConfirmationToken)
	}
	if state.NormalizedAt != nil {
		return nil, ErrMoneyNormalizationAlreadyApplied
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE paid_plans, bot_users, transactions, topup_requests, bulk_credit_operations, purchase_quotes, purchase_requests, payment_intents, reconciliation_records, bot_settings IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, fmt.Errorf("lock monetary data for normalization: %w", err)
	}

	if err := applyLegacyMoneyUnit(ctx, tx, unit); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE money_normalization_audit
		SET selected_unit = $1, normalized_by = $2, normalized_at = NOW()
		WHERE singleton = TRUE
	`, unit, operator); err != nil {
		return nil, fmt.Errorf("record money unit decision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit money normalization: %w", err)
	}
	return GetMoneyNormalizationState(ctx)
}

func moneyNormalizationCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 5*time.Minute)
}

func moneyNormalizationToken(databaseName string, capturedAt time.Time) string {
	return databaseName + ":" + capturedAt.UTC().Format("20060102T150405.000000Z")
}

// applyLegacyMoneyUnit is kept separate so its SQL effects can be verified in
// a transaction that is rolled back by integration tests.
func applyLegacyMoneyUnit(ctx context.Context, tx pgx.Tx, unit string) error {
	planQuery := `
		UPDATE paid_plans
		SET base_price_toman = CASE
		        WHEN $1 = 'rial' AND base_price > 0 AND (base_price_toman = 0 OR base_price_toman = ROUND(base_price)::BIGINT) THEN ROUND(base_price / 10)::BIGINT
		        WHEN $1 = 'toman' AND base_price_toman = 0 AND base_price > 0 THEN ROUND(base_price)::BIGINT
		        ELSE base_price_toman END,
		    price_per_extra_ip_toman = CASE
		        WHEN $1 = 'rial' AND price_per_extra_ip > 0 AND (price_per_extra_ip_toman = 0 OR price_per_extra_ip_toman = ROUND(price_per_extra_ip)::BIGINT) THEN ROUND(price_per_extra_ip / 10)::BIGINT
		        WHEN $1 = 'toman' AND price_per_extra_ip_toman = 0 AND price_per_extra_ip > 0 THEN ROUND(price_per_extra_ip)::BIGINT
		        ELSE price_per_extra_ip_toman END,
		    price_per_gb_toman = CASE
		        WHEN $1 = 'rial' AND price_per_gb > 0 AND (price_per_gb_toman = 0 OR price_per_gb_toman = ROUND(price_per_gb)::BIGINT) THEN ROUND(price_per_gb / 10)::BIGINT
		        WHEN $1 = 'toman' AND price_per_gb_toman = 0 AND price_per_gb > 0 THEN ROUND(price_per_gb)::BIGINT
		        ELSE price_per_gb_toman END,
		    price_per_extra_month_toman = CASE
		        WHEN $1 = 'rial' AND price_per_extra_month > 0 AND (price_per_extra_month_toman = 0 OR price_per_extra_month_toman = ROUND(price_per_extra_month)::BIGINT) THEN ROUND(price_per_extra_month / 10)::BIGINT
		        WHEN $1 = 'toman' AND price_per_extra_month_toman = 0 AND price_per_extra_month > 0 THEN ROUND(price_per_extra_month)::BIGINT
		        ELSE price_per_extra_month_toman END
	`
	if _, err := tx.Exec(ctx, planQuery, unit); err != nil {
		return fmt.Errorf("normalize paid-plan integer prices: %w", err)
	}
	if unit == "rial" {
		statements := []string{
			`UPDATE bot_users SET wallet_balance = ROUND(wallet_balance::NUMERIC / 10)::BIGINT WHERE wallet_balance <> 0`,
			`UPDATE transactions SET amount = ROUND(amount::NUMERIC / 10)::BIGINT WHERE amount <> 0`,
			`UPDATE topup_requests SET amount = ROUND(amount::NUMERIC / 10)::BIGINT WHERE amount IS NOT NULL AND amount <> 0`,
			`UPDATE bulk_credit_operations SET amount = ROUND(amount::NUMERIC / 10)::BIGINT WHERE amount <> 0`,
			`UPDATE purchase_quotes SET base_price_toman = ROUND(base_price_toman::NUMERIC / 10)::BIGINT, extra_ip_price_toman = ROUND(extra_ip_price_toman::NUMERIC / 10)::BIGINT, extra_month_price_toman = ROUND(extra_month_price_toman::NUMERIC / 10)::BIGINT, traffic_price_toman = ROUND(traffic_price_toman::NUMERIC / 10)::BIGINT, discount_toman = ROUND(discount_toman::NUMERIC / 10)::BIGINT, final_price_toman = ROUND(final_price_toman::NUMERIC / 10)::BIGINT`,
			`UPDATE purchase_requests SET price = ROUND(price::NUMERIC / 10, 2), price_toman = CASE WHEN price_toman IS NULL THEN NULL ELSE ROUND(price_toman::NUMERIC / 10)::BIGINT END`,
			`UPDATE payment_intents SET amount_toman = ROUND(amount_toman::NUMERIC / 10)::BIGINT`,
			`UPDATE reconciliation_records SET manual_action_amount = CASE WHEN manual_action_amount IS NULL THEN NULL ELSE ROUND(manual_action_amount::NUMERIC / 10)::BIGINT END`,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return fmt.Errorf("normalize historical monetary values: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE bot_settings SET value = ROUND(value::NUMERIC / 10)::BIGINT::TEXT, updated_at = NOW() WHERE key = 'min_topup_amount' AND value ~ '^[0-9]+([.][0-9]+)?$'`); err != nil {
			return fmt.Errorf("normalize minimum top-up setting: %w", err)
		}
		var hasRefundRequests bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass('refund_requests') IS NOT NULL`).Scan(&hasRefundRequests); err != nil {
			return fmt.Errorf("check refund request table: %w", err)
		}
		if hasRefundRequests {
			if _, err := tx.Exec(ctx, `LOCK TABLE refund_requests IN SHARE ROW EXCLUSIVE MODE`); err != nil {
				return fmt.Errorf("lock refund requests for normalization: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE refund_requests SET calculated_amount = ROUND(calculated_amount::NUMERIC / 10)::BIGINT, approved_amount = CASE WHEN approved_amount IS NULL THEN NULL ELSE ROUND(approved_amount::NUMERIC / 10)::BIGINT END`); err != nil {
				return fmt.Errorf("normalize refund amounts: %w", err)
			}
		}
		for _, column := range []struct{ table, field string }{
			{"payment_intents", "provisioning_snapshot"},
			{"purchase_requests", "provisioning_snapshot"},
			{"reconciliation_records", "desired_state"},
			{"reconciliation_records", "observed_state"},
		} {
			if err := scaleMoneyJSONColumn(ctx, tx, column.table, column.field); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_settings (key, value, updated_at) VALUES ('currency_name', 'تومان', NOW()) ON CONFLICT (key) DO UPDATE SET value = 'تومان', updated_at = NOW()`); err != nil {
		return fmt.Errorf("record normalized currency name: %w", err)
	}
	// Legacy floats are compatibility mirrors after normalization; the report
	// captured their original values before they are made deterministic.
	if _, err := tx.Exec(ctx, `
		UPDATE paid_plans SET base_price = base_price_toman,
		    price_per_extra_ip = price_per_extra_ip_toman,
		    price_per_gb = price_per_gb_toman,
		    price_per_extra_month = price_per_extra_month_toman
	`); err != nil {
		return fmt.Errorf("refresh paid-plan compatibility mirrors: %w", err)
	}
	return nil
}

func scaleMoneyJSONColumn(ctx context.Context, tx pgx.Tx, table, field string) error {
	// Table and field names are selected only from the static list above.
	type snapshot struct {
		id  int64
		raw []byte
	}
	var lastID int64
	for {
		query := fmt.Sprintf(`SELECT id, %s FROM %s WHERE id > $1 ORDER BY id LIMIT 500 FOR UPDATE`, field, table)
		rows, err := tx.Query(ctx, query, lastID)
		if err != nil {
			return fmt.Errorf("read %s.%s snapshots: %w", table, field, err)
		}
		var records []snapshot
		for rows.Next() {
			var row snapshot
			if err := rows.Scan(&row.id, &row.raw); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s.%s snapshot: %w", table, field, err)
			}
			records = append(records, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("read %s.%s snapshots: %w", table, field, err)
		}
		rows.Close()
		if len(records) == 0 {
			return nil
		}
		for _, row := range records {
			scaled, err := scaleMoneyJSON(row.raw)
			if err != nil {
				return fmt.Errorf("normalize %s.%s for row %d: %w", table, field, row.id, err)
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1::jsonb WHERE id = $2`, table, field), scaled, row.id); err != nil {
				return fmt.Errorf("write %s.%s for row %d: %w", table, field, row.id, err)
			}
		}
		lastID = records[len(records)-1].id
	}
}

func scaleMoneyJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value = scaleMoneyJSONValue(value, false)
	return json.Marshal(value)
}

func scaleMoneyJSONValue(value any, force bool) any {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			lower := strings.ToLower(key)
			moneyField := strings.Contains(lower, "price") || strings.Contains(lower, "amount") || strings.Contains(lower, "balance")
			if moneyField {
				node[key] = scaleMoneyJSONValue(child, true)
			} else {
				node[key] = scaleMoneyJSONValue(child, force)
			}
		}
		return node
	case []any:
		for index, child := range node {
			node[index] = scaleMoneyJSONValue(child, force)
		}
		return node
	case json.Number:
		if force {
			if scaled, ok := divideMoneyNumber(string(node)); ok {
				return scaled
			}
		}
		return node
	case string:
		if force {
			if scaled, ok := divideMoneyNumber(node); ok {
				return scaled.String()
			}
		}
		return node
	default:
		return value
	}
}

func divideMoneyNumber(input string) (json.Number, bool) {
	rational, ok := new(big.Rat).SetString(input)
	if !ok {
		return "", false
	}
	denominator := new(big.Int).Mul(rational.Denom(), big.NewInt(10))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(rational.Num(), denominator, remainder)
	absRemainder := new(big.Int).Abs(remainder)
	if new(big.Int).Mul(absRemainder, big.NewInt(2)).Cmp(denominator) >= 0 {
		if rational.Num().Sign() < 0 {
			quotient.Sub(quotient, big.NewInt(1))
		} else {
			quotient.Add(quotient, big.NewInt(1))
		}
	}
	return json.Number(quotient.String()), true
}
