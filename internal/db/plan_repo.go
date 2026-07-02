package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func GetTestPlansForUser(ctx context.Context, userID int64, includeDisabled bool) ([]*TestPlan, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT p.id, p.name, p.description, p.usage_description, p.inbound_ids, p.expire_seconds, p.max_data_bytes, p.flow, p.max_per_day, p.is_global, p.enabled, p.sync_subs, p.created_at, p.updated_at
		FROM test_plans p
		WHERE ($2 OR p.enabled)
		  AND (
			p.is_global
			OR EXISTS (
				SELECT 1 FROM plan_user_access a
				WHERE a.plan_type = 'test' AND a.plan_id = p.id AND a.user_id = $1
			)
		  )
		ORDER BY p.id
	`
	rows, err := Pool.Query(ctx, query, userID, includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []*TestPlan
	for rows.Next() {
		p, err := scanTestPlanRows(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func GetTestPlans(ctx context.Context, includeDisabled bool) ([]*TestPlan, error) {
	return GetTestPlansForUser(ctx, 0, includeDisabled)
}

func GetTestPlanByID(ctx context.Context, id int64) (*TestPlan, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, `
		SELECT id, name, description, usage_description, inbound_ids, expire_seconds, max_data_bytes, flow, max_per_day, is_global, enabled, sync_subs, created_at, updated_at
		FROM test_plans WHERE id = $1
	`, id)
	p, err := scanTestPlanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func CreateTestPlan(ctx context.Context, p *TestPlan) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	inboundJSON, err := json.Marshal(p.InboundIDs)
	if err != nil {
		return err
	}
	if p.MaxPerDay <= 0 {
		p.MaxPerDay = 1
	}
	query := `
		INSERT INTO test_plans (name, description, usage_description, inbound_ids, expire_seconds, max_data_bytes, flow, max_per_day, is_global, enabled, sync_subs)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at
	`
	return Pool.QueryRow(ctx, query, p.Name, p.Description, p.UsageDescription, string(inboundJSON), p.ExpireSeconds, p.MaxDataBytes, p.Flow, p.MaxPerDay, p.IsGlobal, p.Enabled, p.SyncSubs).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

func UpdateTestPlan(ctx context.Context, p *TestPlan) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	inboundJSON, err := json.Marshal(p.InboundIDs)
	if err != nil {
		return err
	}
	_, err = Pool.Exec(ctx, `
		UPDATE test_plans
		SET name = $1, description = $2, usage_description = $3, inbound_ids = $4::jsonb, expire_seconds = $5, max_data_bytes = $6,
			flow = $7, max_per_day = $8, is_global = $9, enabled = $10, sync_subs = $11, updated_at = NOW()
		WHERE id = $12
	`, p.Name, p.Description, p.UsageDescription, string(inboundJSON), p.ExpireSeconds, p.MaxDataBytes, p.Flow, p.MaxPerDay, p.IsGlobal, p.Enabled, p.SyncSubs, p.ID)
	return err
}

func GetPaidPlansForUser(ctx context.Context, userID int64, includeDisabled bool) ([]*PaidPlan, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		SELECT p.id, p.name, p.description, p.usage_description, p.inbound_ids, p.base_price, p.base_ip_limit, p.max_ip_limit, p.price_per_extra_ip, p.flow, p.discount_tiers, p.is_global, p.enabled, p.sync_subs, p.is_limited, p.price_per_gb, p.min_data_gb, p.price_per_extra_month, p.created_at, p.updated_at
		FROM paid_plans p
		WHERE ($2 OR p.enabled)
		  AND (
			p.is_global
			OR EXISTS (
				SELECT 1 FROM plan_user_access a
				WHERE a.plan_type = 'paid' AND a.plan_id = p.id AND a.user_id = $1
			)
		  )
		ORDER BY p.id
	`
	rows, err := Pool.Query(ctx, query, userID, includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []*PaidPlan
	for rows.Next() {
		p, err := scanPaidPlanRows(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func GetPaidPlans(ctx context.Context, includeDisabled bool) ([]*PaidPlan, error) {
	return GetPaidPlansForUser(ctx, 0, includeDisabled)
}

func GetPaidPlanByID(ctx context.Context, id int64) (*PaidPlan, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	row := Pool.QueryRow(ctx, `
		SELECT id, name, description, usage_description, inbound_ids, base_price, base_ip_limit, max_ip_limit, price_per_extra_ip, flow, discount_tiers, is_global, enabled, sync_subs, is_limited, price_per_gb, min_data_gb, price_per_extra_month, created_at, updated_at
		FROM paid_plans WHERE id = $1
	`, id)
	p, err := scanPaidPlanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func CreatePaidPlan(ctx context.Context, p *PaidPlan) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	inboundJSON, err := json.Marshal(p.InboundIDs)
	if err != nil {
		return err
	}
	tierJSON, err := json.Marshal(p.DiscountTiers)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO paid_plans (name, description, usage_description, inbound_ids, base_price, base_ip_limit, max_ip_limit, price_per_extra_ip, flow, discount_tiers, is_global, enabled, sync_subs, is_limited, price_per_gb, min_data_gb, price_per_extra_month)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id, created_at, updated_at
	`
	return Pool.QueryRow(ctx, query, p.Name, p.Description, p.UsageDescription, string(inboundJSON), p.BasePrice, p.BaseIPLimit, p.MaxIPLimit, p.PricePerExtraIP, p.Flow, string(tierJSON), p.IsGlobal, p.Enabled, p.SyncSubs, p.IsLimited, p.PricePerGB, p.MinDataGB, p.PricePerExtraMonth).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

func UpdatePaidPlan(ctx context.Context, p *PaidPlan) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	inboundJSON, err := json.Marshal(p.InboundIDs)
	if err != nil {
		return err
	}
	tierJSON, err := json.Marshal(p.DiscountTiers)
	if err != nil {
		return err
	}
	_, err = Pool.Exec(ctx, `
		UPDATE paid_plans
		SET name = $1, description = $2, usage_description = $3, inbound_ids = $4::jsonb, base_price = $5, base_ip_limit = $6, max_ip_limit = $7,
			price_per_extra_ip = $8, flow = $9, discount_tiers = $10::jsonb, is_global = $11, enabled = $12, sync_subs = $13,
			is_limited = $14, price_per_gb = $15, min_data_gb = $16, price_per_extra_month = $17, updated_at = NOW()
		WHERE id = $18
	`, p.Name, p.Description, p.UsageDescription, string(inboundJSON), p.BasePrice, p.BaseIPLimit, p.MaxIPLimit, p.PricePerExtraIP, p.Flow, string(tierJSON), p.IsGlobal, p.Enabled, p.SyncSubs, p.IsLimited, p.PricePerGB, p.MinDataGB, p.PricePerExtraMonth, p.ID)
	return err
}

func SetPlanEnabled(ctx context.Context, planType string, planID int64, enabled bool) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	switch planType {
	case PlanTypeTest:
		_, err := Pool.Exec(ctx, `UPDATE test_plans SET enabled = $1, updated_at = NOW() WHERE id = $2`, enabled, planID)
		return err
	case PlanTypePaid:
		_, err := Pool.Exec(ctx, `UPDATE paid_plans SET enabled = $1, updated_at = NOW() WHERE id = $2`, enabled, planID)
		return err
	default:
		return fmt.Errorf("unknown plan type %q", planType)
	}
}

func SetPlanGlobal(ctx context.Context, planType string, planID int64, global bool) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	switch planType {
	case PlanTypeTest:
		_, err := Pool.Exec(ctx, `UPDATE test_plans SET is_global = $1, updated_at = NOW() WHERE id = $2`, global, planID)
		return err
	case PlanTypePaid:
		_, err := Pool.Exec(ctx, `UPDATE paid_plans SET is_global = $1, updated_at = NOW() WHERE id = $2`, global, planID)
		return err
	default:
		return fmt.Errorf("unknown plan type %q", planType)
	}
}

func DeletePlan(ctx context.Context, planType string, planID int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM plan_user_access WHERE plan_type = $1 AND plan_id = $2`, planType, planID); err != nil {
		return err
	}
	switch planType {
	case PlanTypeTest:
		if _, err := tx.Exec(ctx, `DELETE FROM test_usage WHERE plan_id = $1`, planID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM test_plans WHERE id = $1`, planID); err != nil {
			return err
		}
	case PlanTypePaid:
		if _, err := tx.Exec(ctx, `DELETE FROM paid_plans WHERE id = $1`, planID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown plan type %q", planType)
	}
	return tx.Commit(ctx)
}

func SetPlanUserAccess(ctx context.Context, planType string, planID int64, userIDs []int64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM plan_user_access WHERE plan_type = $1 AND plan_id = $2`, planType, planID); err != nil {
		return err
	}
	if len(userIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plan_user_access (plan_type, plan_id, user_id)
			SELECT $1, $2, unnest($3::BIGINT[])
			ON CONFLICT DO NOTHING
		`, planType, planID, userIDs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func GetPlanUserAccess(ctx context.Context, planType string, planID int64) ([]int64, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	rows, err := Pool.Query(ctx, `SELECT user_id FROM plan_user_access WHERE plan_type = $1 AND plan_id = $2 ORDER BY user_id`, planType, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func GetPlansByType(ctx context.Context, planType string) ([]*Plan, error) {
	if planType == PlanTypeTest {
		testPlans, err := GetTestPlans(ctx, false)
		if err != nil {
			return nil, err
		}
		plans := make([]*Plan, 0, len(testPlans))
		for _, p := range testPlans {
			inboundID := 0
			if len(p.InboundIDs) > 0 {
				inboundID = p.InboundIDs[0]
			}
			plans = append(plans, &Plan{
				ID:           int(p.ID),
				Name:         p.Name,
				Type:         PlanTypeTest,
				DurationDays: int(p.ExpireSeconds / 86400),
				TrafficGB:    float64(p.MaxDataBytes) / 1073741824,
				IPLimit:      1,
				InboundID:    inboundID,
				CreatedAt:    p.CreatedAt,
			})
		}
		return plans, nil
	}

	paidPlans, err := GetPaidPlans(ctx, false)
	if err != nil {
		return nil, err
	}
	plans := make([]*Plan, 0, len(paidPlans))
	for _, p := range paidPlans {
		inboundID := 0
		if len(p.InboundIDs) > 0 {
			inboundID = p.InboundIDs[0]
		}
		plans = append(plans, &Plan{
			ID:        int(p.ID),
			Name:      p.Name,
			Type:      PlanTypePaid,
			IPLimit:   p.BaseIPLimit,
			Price:     p.BasePrice,
			InboundID: inboundID,
			CreatedAt: p.CreatedAt,
		})
	}
	return plans, nil
}

func GetPlanByID(ctx context.Context, id int) (*Plan, error) {
	if p, err := GetTestPlanByID(ctx, int64(id)); err != nil {
		return nil, err
	} else if p != nil {
		inboundID := 0
		if len(p.InboundIDs) > 0 {
			inboundID = p.InboundIDs[0]
		}
		return &Plan{ID: int(p.ID), Name: p.Name, Type: PlanTypeTest, DurationDays: int(p.ExpireSeconds / 86400), TrafficGB: float64(p.MaxDataBytes) / 1073741824, IPLimit: 1, InboundID: inboundID, CreatedAt: p.CreatedAt}, nil
	}

	if p, err := GetPaidPlanByID(ctx, int64(id)); err != nil {
		return nil, err
	} else if p != nil {
		inboundID := 0
		if len(p.InboundIDs) > 0 {
			inboundID = p.InboundIDs[0]
		}
		return &Plan{ID: int(p.ID), Name: p.Name, Type: PlanTypePaid, IPLimit: p.BaseIPLimit, Price: p.BasePrice, InboundID: inboundID, CreatedAt: p.CreatedAt}, nil
	}
	return nil, pgx.ErrNoRows
}

func CreatePlan(ctx context.Context, p *Plan) error {
	if p.Type == PlanTypeTest {
		testPlan := &TestPlan{
			Name:          p.Name,
			InboundIDs:    []int{p.InboundID},
			ExpireSeconds: int64(p.DurationDays) * 86400,
			MaxDataBytes:  int64(p.TrafficGB * 1073741824),
			MaxPerDay:     1,
			IsGlobal:      true,
			Enabled:       true,
		}
		if err := CreateTestPlan(ctx, testPlan); err != nil {
			return err
		}
		p.ID = int(testPlan.ID)
		p.CreatedAt = testPlan.CreatedAt
		return nil
	}

	paidPlan := &PaidPlan{
		Name:            p.Name,
		InboundIDs:      []int{p.InboundID},
		BasePrice:       p.Price,
		BaseIPLimit:     p.IPLimit,
		MaxIPLimit:      p.IPLimit,
		PricePerExtraIP: 0,
		IsGlobal:        true,
		Enabled:         true,
	}
	if err := CreatePaidPlan(ctx, paidPlan); err != nil {
		return err
	}
	p.ID = int(paidPlan.ID)
	p.CreatedAt = paidPlan.CreatedAt
	return nil
}

func scanTestPlanRows(rows pgx.Rows) (*TestPlan, error) {
	var inboundJSON []byte
	p := &TestPlan{}
	err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.UsageDescription, &inboundJSON, &p.ExpireSeconds, &p.MaxDataBytes, &p.Flow, &p.MaxPerDay, &p.IsGlobal, &p.Enabled, &p.SyncSubs, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(inboundJSON) > 0 {
		_ = json.Unmarshal(inboundJSON, &p.InboundIDs)
	}
	return p, nil
}

func scanTestPlanRow(row pgx.Row) (*TestPlan, error) {
	var inboundJSON []byte
	p := &TestPlan{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.UsageDescription, &inboundJSON, &p.ExpireSeconds, &p.MaxDataBytes, &p.Flow, &p.MaxPerDay, &p.IsGlobal, &p.Enabled, &p.SyncSubs, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(inboundJSON) > 0 {
		_ = json.Unmarshal(inboundJSON, &p.InboundIDs)
	}
	return p, nil
}

func scanPaidPlanRows(rows pgx.Rows) (*PaidPlan, error) {
	var inboundJSON, tierJSON []byte
	p := &PaidPlan{}
	err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.UsageDescription, &inboundJSON, &p.BasePrice, &p.BaseIPLimit, &p.MaxIPLimit, &p.PricePerExtraIP, &p.Flow, &tierJSON, &p.IsGlobal, &p.Enabled, &p.SyncSubs, &p.IsLimited, &p.PricePerGB, &p.MinDataGB, &p.PricePerExtraMonth, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(inboundJSON) > 0 {
		_ = json.Unmarshal(inboundJSON, &p.InboundIDs)
	}
	if len(tierJSON) > 0 {
		_ = json.Unmarshal(tierJSON, &p.DiscountTiers)
	}
	return p, nil
}

func scanPaidPlanRow(row pgx.Row) (*PaidPlan, error) {
	var inboundJSON, tierJSON []byte
	p := &PaidPlan{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.UsageDescription, &inboundJSON, &p.BasePrice, &p.BaseIPLimit, &p.MaxIPLimit, &p.PricePerExtraIP, &p.Flow, &tierJSON, &p.IsGlobal, &p.Enabled, &p.SyncSubs, &p.IsLimited, &p.PricePerGB, &p.MinDataGB, &p.PricePerExtraMonth, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(inboundJSON) > 0 {
		_ = json.Unmarshal(inboundJSON, &p.InboundIDs)
	}
	if len(tierJSON) > 0 {
		_ = json.Unmarshal(tierJSON, &p.DiscountTiers)
	}
	return p, nil
}
