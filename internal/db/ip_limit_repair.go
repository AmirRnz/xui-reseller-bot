package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

var ErrIPLimitRepairAlreadyApplied = errors.New("an IP-limit repair has already been applied to this deployment")

type IPLimitRepairRow struct {
	SubscriptionID int64  `json:"subscription_id"`
	ClientEmail    string `json:"client_email"`
	CurrentLimit   int    `json:"current_ip_limit"`
	RepairedLimit  int    `json:"proposed_ip_limit"`
}

type IPLimitRepairPreview struct {
	DatabaseName string             `json:"database_name"`
	Factor       int                `json:"factor"`
	PreviewToken string             `json:"preview_token"`
	Affected     []IPLimitRepairRow `json:"affected_rows"`
	Excluded     []IPLimitRepairRow `json:"non_divisible_rows_review_required"`
}

type ipLimitRepairQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func PreviewLegacyIPLimitRepair(ctx context.Context, factor int) (*IPLimitRepairPreview, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if factor <= 1 {
		return nil, errors.New("repair factor must be greater than 1")
	}
	return loadIPLimitRepairPreview(ctx, Pool, factor)
}

func ApplyLegacyIPLimitRepair(ctx context.Context, factor int, operator, confirmationToken string) (*IPLimitRepairPreview, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	operator = strings.TrimSpace(operator)
	if factor <= 1 {
		return nil, errors.New("repair factor must be greater than 1")
	}
	if operator == "" {
		return nil, errors.New("operator is required")
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `LOCK TABLE subscriptions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, fmt.Errorf("lock subscriptions for IP repair: %w", err)
	}
	var alreadyApplied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ip_limit_repair_runs)`).Scan(&alreadyApplied); err != nil {
		return nil, fmt.Errorf("check prior IP repair: %w", err)
	}
	if alreadyApplied {
		return nil, ErrIPLimitRepairAlreadyApplied
	}
	preview, err := loadIPLimitRepairPreview(ctx, tx, factor)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(confirmationToken) != preview.PreviewToken {
		return nil, fmt.Errorf("confirmation token does not match the current dry-run; expected %q", preview.PreviewToken)
	}
	if len(preview.Affected) == 0 {
		return nil, errors.New("dry-run found no divisible IP limits to repair")
	}
	report, err := json.Marshal(preview)
	if err != nil {
		return nil, fmt.Errorf("encode IP repair report: %w", err)
	}
	var runID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO ip_limit_repair_runs (factor, operator, preview_token, report, affected_count)
		VALUES ($1, $2, $3, $4::jsonb, $5) RETURNING id
	`, factor, operator, preview.PreviewToken, report, len(preview.Affected)).Scan(&runID); err != nil {
		return nil, fmt.Errorf("record IP repair run: %w", err)
	}
	for _, row := range preview.Affected {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ip_limit_repair_rows (subscription_id, run_id, original_ip_limit, repaired_ip_limit)
			VALUES ($1, $2, $3, $4)
		`, row.SubscriptionID, runID, row.CurrentLimit, row.RepairedLimit); err != nil {
			return nil, fmt.Errorf("record repaired IP limit for subscription %d: %w", row.SubscriptionID, err)
		}
		result, err := tx.Exec(ctx, `UPDATE subscriptions SET ip_limit = $1, updated_at = NOW() WHERE id = $2 AND ip_limit = $3`, row.RepairedLimit, row.SubscriptionID, row.CurrentLimit)
		if err != nil {
			return nil, fmt.Errorf("repair IP limit for subscription %d: %w", row.SubscriptionID, err)
		}
		if result.RowsAffected() != 1 {
			return nil, fmt.Errorf("subscription %d changed after preview; transaction rolled back", row.SubscriptionID)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE ip_limit_repair_runs SET applied_at = NOW() WHERE id = $1`, runID); err != nil {
		return nil, fmt.Errorf("mark IP repair applied: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit IP repair: %w", err)
	}
	return preview, nil
}

func loadIPLimitRepairPreview(ctx context.Context, queryer ipLimitRepairQueryer, factor int) (*IPLimitRepairPreview, error) {
	preview := &IPLimitRepairPreview{Factor: factor, Affected: []IPLimitRepairRow{}, Excluded: []IPLimitRepairRow{}}
	if err := queryer.QueryRow(ctx, `SELECT current_database()`).Scan(&preview.DatabaseName); err != nil {
		return nil, fmt.Errorf("read database name: %w", err)
	}
	rows, err := queryer.Query(ctx, `SELECT id, client_email, ip_limit FROM subscriptions WHERE ip_limit >= $1 ORDER BY id`, factor)
	if err != nil {
		return nil, fmt.Errorf("read candidate IP limits: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row IPLimitRepairRow
		if err := rows.Scan(&row.SubscriptionID, &row.ClientEmail, &row.CurrentLimit); err != nil {
			return nil, fmt.Errorf("scan candidate IP limit: %w", err)
		}
		if row.CurrentLimit%factor == 0 {
			row.RepairedLimit = row.CurrentLimit / factor
			preview.Affected = append(preview.Affected, row)
		} else {
			preview.Excluded = append(preview.Excluded, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read candidate IP limits: %w", err)
	}
	preview.PreviewToken = ipLimitRepairToken(preview)
	return preview, nil
}

func ipLimitRepairToken(preview *IPLimitRepairPreview) string {
	var material bytes.Buffer
	fmt.Fprintf(&material, "%s|%d|", preview.DatabaseName, preview.Factor)
	for _, rows := range [][]IPLimitRepairRow{preview.Affected, preview.Excluded} {
		for _, row := range rows {
			fmt.Fprintf(&material, "%d:%s:%d:%d;", row.SubscriptionID, row.ClientEmail, row.CurrentLimit, row.RepairedLimit)
		}
		material.WriteByte('|')
	}
	digest := sha256.Sum256(material.Bytes())
	return fmt.Sprintf("%s:%d:%d:%s", preview.DatabaseName, preview.Factor, len(preview.Affected), hex.EncodeToString(digest[:12]))
}
