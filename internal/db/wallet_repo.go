package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrWalletOperationAlreadyApplied = errors.New("wallet operation already applied")

// DebitWalletBalanceWithReconciliation atomically commits the wallet debit and
// its durable recovery work item before any remote XUI mutation can begin.
func DebitWalletBalanceWithReconciliation(ctx context.Context, userID int64, amount int64, description, operationKey string, record *ReconciliationRecord) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()
	if Pool == nil || record == nil {
		return errors.New("database and reconciliation work item are required")
	}
	if userID <= 0 || amount <= 0 || operationKey == "" || record.OperationKey == "" {
		return errors.New("valid debit and reconciliation operation keys are required")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var transactionID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
		VALUES ($1, $2, 'debit', 'completed', $3, $4)
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id
	`, userID, -amount, description, operationKey).Scan(&transactionID)
	newDebit := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		var matched bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transactions WHERE user_id = $1 AND operation_key = $2 AND type = 'debit' AND status = 'completed' AND amount = $3)`, userID, operationKey, -amount).Scan(&matched); err != nil {
			return err
		}
		if !matched {
			return errors.New("wallet operation key conflicts with a different ledger entry")
		}
	} else if err != nil {
		return err
	}

	if newDebit {
		tag, err := tx.Exec(ctx, `
			UPDATE bot_users SET wallet_balance = wallet_balance - $1, updated_at = NOW()
			WHERE id = $2 AND wallet_balance >= $1
		`, amount, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errors.New("insufficient balance")
		}
	}
	if err := createReconciliationRecordTx(ctx, tx, record); err != nil {
		return fmt.Errorf("failed to persist provisioning work with wallet debit: %w", err)
	}
	return tx.Commit(ctx)
}

func CreateWalletTransaction(ctx context.Context, tx *WalletTransaction) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	amount := tx.Amount
	query := `
		INSERT INTO transactions (user_id, amount, type, status, description, reference_type, reference_id, operation_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id, created_at, updated_at
	`
	if tx.Status == "" {
		tx.Status = "completed"
	}
	err := Pool.QueryRow(ctx, query, tx.UserID, amount, tx.Type, tx.Status, tx.Description, tx.ReferenceType, tx.ReferenceID, tx.OperationKey).
		Scan(&tx.ID, &tx.CreatedAt, &tx.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) && tx.OperationKey != "" {
		return ErrWalletOperationAlreadyApplied
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

func AddWalletBalance(ctx context.Context, userID int64, amount int64) error {
	return AddWalletBalanceWithKey(ctx, userID, amount, "wallet balance adjustment", "")
}

func AddWalletBalanceWithKey(ctx context.Context, userID int64, amount int64, description, operationKey string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if amount == 0 {
		return nil
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	claimed := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'credit', 'completed', $3, $4)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, amount, description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimed = true
	}

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amount, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	if !claimed {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description)
			VALUES ($1, $2, $3, 'completed', $4)
		`, userID, amount, transactionType(amount), description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func DebitWalletBalance(ctx context.Context, userID int64, amount int64, description string) error {
	return DebitWalletBalanceWithKey(ctx, userID, amount, description, "")
}

func DebitWalletBalanceWithKey(ctx context.Context, userID int64, amount int64, description, operationKey string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if amount <= 0 {
		return errors.New("amount must be positive")
	}

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	claimed := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'debit', 'completed', $3, $4)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, -amount, description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimed = true
	}

	tag, err := tx.Exec(ctx, `
		UPDATE bot_users
		SET wallet_balance = wallet_balance - $1, updated_at = NOW()
		WHERE id = $2 AND wallet_balance >= $1
	`, amount, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("insufficient balance")
	}

	if !claimed {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description)
			VALUES ($1, $2, 'debit', 'completed', $3)
		`, userID, -amount, description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func CreditWalletBalance(ctx context.Context, userID int64, amount int64, description string) error {
	return CreditWalletBalanceWithKey(ctx, userID, amount, description, "")
}

func CreditWalletBalanceWithKey(ctx context.Context, userID int64, amount int64, description, operationKey string) error {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if amount <= 0 {
		return errors.New("amount must be positive")
	}

	if Pool == nil {
		return errors.New("database pool is not initialized")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	claimed := false
	if operationKey != "" {
		var insertedID int64
		err = tx.QueryRow(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
			VALUES ($1, $2, 'credit', 'completed', $3, $4)
			ON CONFLICT (operation_key) DO NOTHING
			RETURNING id
		`, userID, amount, description, operationKey).Scan(&insertedID)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return ErrWalletOperationAlreadyApplied
		}
		if err != nil {
			return err
		}
		claimed = true
	}

	tag, err := tx.Exec(ctx, `UPDATE bot_users SET wallet_balance = wallet_balance + $1, updated_at = NOW() WHERE id = $2`, amount, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	if !claimed {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description)
			VALUES ($1, $2, 'credit', 'completed', $3)
		`, userID, amount, description)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func CreditAllApprovedUsers(ctx context.Context, amount int64, description string) (int64, error) {
	return CreditAllApprovedUsersWithKey(ctx, amount, description, "")
}

func CreditAllApprovedUsersWithKey(ctx context.Context, amount int64, description, operationKey string) (int64, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return 0, errors.New("database pool is not initialized")
	}
	if amount <= 0 {
		return 0, errors.New("amount must be positive")
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if operationKey != "" {
		var opID int64
		var opStatus string
		var recipientUserIDs []int64

		err := tx.QueryRow(ctx, `
			SELECT id, status, recipient_user_ids
			FROM bulk_credit_operations
			WHERE operation_key = $1
			FOR UPDATE
		`, operationKey).Scan(&opID, &opStatus, &recipientUserIDs)

		if err == nil {
			if opStatus == "completed" {
				_ = tx.Rollback(ctx)
				return 0, ErrWalletOperationAlreadyApplied
			}
			// Operation exists but not completed; resume using snapshotted recipients.
		} else if errors.Is(err, pgx.ErrNoRows) {
			rows, qErr := tx.Query(ctx, `
				SELECT id
				FROM bot_users
				WHERE status IN ('active', 'approved')
				ORDER BY id
				FOR UPDATE
			`)
			if qErr != nil {
				return 0, qErr
			}
			recipientUserIDs = make([]int64, 0)
			for rows.Next() {
				var uid int64
				if sErr := rows.Scan(&uid); sErr != nil {
					rows.Close()
					return 0, sErr
				}
				recipientUserIDs = append(recipientUserIDs, uid)
			}
			if rErr := rows.Err(); rErr != nil {
				return 0, rErr
			}

			if len(recipientUserIDs) == 0 {
				_, err = tx.Exec(ctx, `
					INSERT INTO bulk_credit_operations (operation_key, amount, admin_id, recipient_user_ids, status, created_at)
					VALUES ($1, $2, NULL, '{}', 'completed', NOW())
				`, operationKey, amount)
				if err != nil {
					return 0, err
				}
				return 0, tx.Commit(ctx)
			}

			err = tx.QueryRow(ctx, `
				INSERT INTO bulk_credit_operations (operation_key, amount, admin_id, recipient_user_ids, status, created_at)
				VALUES ($1, $2, NULL, $3, 'in_progress', NOW())
				RETURNING id
			`, operationKey, amount, recipientUserIDs).Scan(&opID)
			if err != nil {
				return 0, err
			}
		} else {
			return 0, err
		}

		tag, err := tx.Exec(ctx, `
			WITH inserted_txs AS (
				INSERT INTO transactions (user_id, amount, type, status, description, operation_key)
				SELECT id, $1, 'credit', 'completed', $2, $3 || ':user:' || id
				FROM bot_users
				WHERE id = ANY($4)
				ON CONFLICT (operation_key) DO NOTHING
				RETURNING user_id
			)
			UPDATE bot_users u
			SET wallet_balance = u.wallet_balance + $1, updated_at = NOW()
			FROM inserted_txs i
			WHERE u.id = i.user_id
		`, amount, description, operationKey, recipientUserIDs)
		if err != nil {
			return 0, err
		}

		_, err = tx.Exec(ctx, `
			UPDATE bulk_credit_operations
			SET status = 'completed'
			WHERE id = $1
		`, opID)
		if err != nil {
			return 0, err
		}

		count := tag.RowsAffected()
		return count, tx.Commit(ctx)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE bot_users
		SET wallet_balance = wallet_balance + $1, updated_at = NOW()
		WHERE status IN ('active', 'approved')
	`, amount)
	if err != nil {
		return 0, err
	}

	count := tag.RowsAffected()
	if count > 0 {
		_, err = tx.Exec(ctx, `
			INSERT INTO transactions (user_id, amount, type, status, description)
			SELECT id, $1, 'credit', 'completed', $2
			FROM bot_users
			WHERE status IN ('active', 'approved')
		`, amount, description)
		if err != nil {
			return 0, err
		}
	}

	return count, tx.Commit(ctx)
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

func GetCompletedDebitTransaction(ctx context.Context, userID int64, operationKey string) (*WalletTransaction, error) {
	ctx, cancel := dbCtx(ctx)
	defer cancel()

	if Pool == nil {
		return nil, errors.New("database pool is not initialized")
	}
	if operationKey == "" {
		return nil, errors.New("operation_key is required")
	}

	tx := &WalletTransaction{}
	err := Pool.QueryRow(ctx, `
		SELECT id, user_id, amount, type, status, description, reference_type, reference_id, COALESCE(operation_key, ''), created_at, updated_at
		FROM transactions
		WHERE user_id = $1 AND operation_key = $2 AND type = 'debit' AND status = 'completed'
		LIMIT 1
	`, userID, operationKey).Scan(&tx.ID, &tx.UserID, &tx.Amount, &tx.Type, &tx.Status, &tx.Description, &tx.ReferenceType, &tx.ReferenceID, &tx.OperationKey, &tx.CreatedAt, &tx.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return tx, err
}

func transactionType(amount int64) string {
	if amount >= 0 {
		return "credit"
	}
	return "debit"
}

func FormatMoney(amount int64) string {
	return fmt.Sprintf("%d", amount)
}
