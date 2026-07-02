package db

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

func CreateWalletTransaction(ctx context.Context, tx *WalletTransaction) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amount := tx.Amount
	query := `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at
	`
	if tx.Status == "" {
		tx.Status = "completed"
	}
	return Pool.QueryRow(ctx, query, tx.UserID, amount, tx.Type, tx.Status, tx.Description, tx.ReferenceType, tx.ReferenceID).
		Scan(&tx.ID, &tx.CreatedAt, &tx.UpdatedAt)
}

func UpdateWalletTransactionStatus(ctx context.Context, id int, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

func GetPendingDepositsCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM topup_requests WHERE status = 'pending'").Scan(&count)
	return count, err
}

func AddWalletBalance(ctx context.Context, userID int64, amount float64) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amountInt := moneyToInt(amount)
	if amountInt == 0 {
		return nil
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amountInt, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description)
		VALUES ($1, $2, $3, 'completed', $4)
	`, userID, amountInt, transactionType(amountInt), "wallet balance adjustment")
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func DebitWalletBalance(ctx context.Context, userID int64, amount float64, description string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amountInt := moneyToInt(amount)
	if amountInt <= 0 {
		return errors.New("amount must be positive")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE bot_users
		SET wallet_balance = wallet_balance - $1, updated_at = NOW()
		WHERE id = $2 AND wallet_balance >= $1
	`, amountInt, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("insufficient balance")
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description)
		VALUES ($1, $2, 'debit', 'completed', $3)
	`, userID, -amountInt, description)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func CreditWalletBalance(ctx context.Context, userID int64, amount float64, description string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amountInt := moneyToInt(amount)
	if amountInt <= 0 {
		return errors.New("amount must be positive")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amountInt, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description)
		VALUES ($1, $2, 'credit', 'completed', $3)
	`, userID, amountInt, description)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func CreditAllApprovedUsers(ctx context.Context, amount float64, description string) (int64, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amountInt := moneyToInt(amount)
	if amountInt <= 0 {
		return 0, errors.New("amount must be positive")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		UPDATE bot_users
		SET wallet_balance = wallet_balance + $1, updated_at = NOW()
		WHERE status IN ('approved', 'active')
		RETURNING id
	`, amountInt)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return 0, rows.Err()
	}

	if len(ids) > 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description)
			SELECT unnest($1::BIGINT[]), $2, 'credit', 'completed', $3
		`, ids, amountInt, description)
		if err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

func GetWalletTransactions(ctx context.Context, userID int64, limit int) ([]*WalletTransaction, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := Pool.Query(ctx, `
		SELECT id, user_id, amount, type, status, description, reference_type, reference_id, created_at, updated_at
		FROM transactions
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txs []*WalletTransaction
	for rows.Next() {
		tx := &WalletTransaction{}
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Amount, &tx.Type, &tx.Status, &tx.Description, &tx.ReferenceType, &tx.ReferenceID, &tx.CreatedAt, &tx.UpdatedAt); err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, rows.Err()
}

func transactionType(amount int64) string {
	if amount >= 0 {
		return "credit"
	}
	return "debit"
}

func FormatMoney(amount float64) string {
	return fmt.Sprintf("%.0f", amount)
}

func moneyToInt(amount float64) int64 {
	return int64(math.Round(amount))
}
