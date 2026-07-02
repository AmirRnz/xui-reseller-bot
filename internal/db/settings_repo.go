package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func GetSetting(ctx context.Context, key string) (string, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var value string
	err := Pool.QueryRow(ctx, "SELECT value FROM bot_settings WHERE key = $1", key).Scan(&value)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil // Or return a custom ErrSettingNotFound
		}
		return "", err
	}
	return value, nil
}

func SetSetting(ctx context.Context, key, value string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	query := `
		INSERT INTO bot_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
	`
	_, err := Pool.Exec(ctx, query, key, value)
	return err
}
