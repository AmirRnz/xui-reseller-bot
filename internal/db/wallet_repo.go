package db

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

var ErrWalletOperationAlreadyApplied = errors.New("wallet operation already applied")

func CreateWalletTransaction(ctx context.Context, tx *WalletTransaction) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amount := tx.Amount
	if tx.Status == "" {
		tx.Status = "completed"
	}
	var operationKey any
	if tx.OperationKey != "" {
		operationKey = tx.OperationKey
	}
	query := `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id, created_at, updated_at
	`
	err := Pool.QueryRow(ctx, query, tx.UserID, amount, tx.Type, tx.Status, tx.Description, tx.ReferenceType, tx.ReferenceID, operationKey).
		Scan(&tx.ID, &tx.CreatedAt, &tx.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) && tx.OperationKey != "" {
		return nil
	}
	return err
}

func UpdateWalletTransactionStatus(ctx context.Context, id int, status string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	_, err := Pool.Exec(ctx, `UPDATE transactions SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

func HasWalletOperation(ctx context.Context, operationKey string) (bool, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	var exists bool
	err := Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM transactions WHERE operation_key = $1)`, operationKey).Scan(&exists)
	return exists, err
}

func GetPendingDepositsCount(ctx context.Context) (int, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	var count int
	err := Pool.QueryRow(ctx, "SELECT count(*) FROM topup_requests WHERE status = 'pending'").Scan(&count)
	return count, err
}

func AddWalletBalance(ctx context.Context, userID int64, amount float64) error {
	return AddWalletBalanceWithKey(ctx, userID, amount, "wallet balance adjustment", "")
}

func AddWalletBalanceWithKey(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
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
	claimedOperation := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, $3, 'completed', $4, $5)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, amountInt, transactionType(amountInt), description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimedOperation = true
	}

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amountInt, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	if !claimedOperation {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, $3, 'completed', $4, NULL)
		`, userID, amountInt, transactionType(amountInt), description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func DebitWalletBalance(ctx context.Context, userID int64, amount float64, description string) error {
	return DebitWalletBalanceWithKey(ctx, userID, amount, description, "")
}

func DebitWalletBalanceWithKey(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
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
	claimedOperation := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'debit', 'completed', $3, $4)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, -amountInt, description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimedOperation = true
	}

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

	if !claimedOperation {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'debit', 'completed', $3, NULL)
		`, userID, -amountInt, description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func CreditWalletBalance(ctx context.Context, userID int64, amount float64, description string) error {
	return CreditWalletBalanceWithKey(ctx, userID, amount, description, "")
}

func CreditWalletBalanceWithKey(ctx context.Context, userID int64, amount float64, description, operationKey string) error {
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
	claimedOperation := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'credit', 'completed', $3, $4)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, amountInt, description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimedOperation = true
	}

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amountInt, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	if !claimedOperation {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'credit', 'completed', $3, NULL)
		`, userID, amountInt, description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func CreditAllApprovedUsers(ctx context.Context, amount float64, description string) (int64, error) {
	return CreditAllApprovedUsersWithKey(ctx, amount, description, "")
}

func CreditAllApprovedUsersWithKey(ctx context.Context, amount float64, description, operationKey string) (int64, error) {
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

	var rows pgx.Rows
	if operationKey == "" {
		rows, err = tx.Query(ctx, `
			UPDATE bot_users
			SET wallet_balance = wallet_balance + $1, updated_at = NOW()
			WHERE status IN ('approved', 'active')
			RETURNING id
		`, amountInt)
	} else {
		rows, err = tx.Query(ctx, `
			WITH candidates AS (
				SELECT u.id
				FROM bot_users u
				WHERE u.status IN ('approved', 'active')
				  AND NOT EXISTS (SELECT 1 FROM transactions t WHERE t.operation_key = $2 || ':' || u.id::text)
				FOR UPDATE
			), inserted AS (
				INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
				SELECT c.id, $1, 'credit', 'completed', $3, $2 || ':' || c.id::text
				FROM candidates c
				ON CONFLICT (operation_key) DO NOTHING
				RETURNING user_id
			)
			UPDATE bot_users u
			SET wallet_balance = u.wallet_balance + $1, updated_at = NOW()
			FROM inserted i
			WHERE u.id = i.user_id
			RETURNING u.id
		`, amountInt, operationKey, description)
	}
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

	if len(ids) > 0 && operationKey == "" {
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
		SELECT id, user_id, amount, type, status, description, reference_type, reference_id, COALESCE(operation_key, ''), created_at, updated_at
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
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Amount, &tx.Type, &tx.Status, &tx.Description, &tx.ReferenceType, &tx.ReferenceID, &tx.OperationKey, &tx.CreatedAt, &tx.UpdatedAt); err != nil {
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
