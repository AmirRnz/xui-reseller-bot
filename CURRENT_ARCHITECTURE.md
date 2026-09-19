# Current Architecture — `xui-resell-bot`

Updated 2026-09-19 after the remote-create compensation, nil-client cancellation blocking, and wallet refund reconciliation pass.

## Supported 3x-ui pin

**3x-ui panel: `v3.8.5` (stable).** The configured panel reported `currentVersion=3.8.5`, `latestVersion=v3.8.5`, and `updateAvailable=false` from the read-only `GET /panel/api/server/getPanelUpdateInfo` endpoint. The checked-in OpenAPI document describes the API compatibility line as `3.x`.

## DB → bot → 3x-ui flow

1. Startup loads `config.yaml`, connects to PostgreSQL, applies the embedded `internal/db/schema.sql` migration, and normalizes legacy IP-limit values.
2. The bot creates an API-token 3x-ui client and starts an inbound cache. The cache refreshes inbound options from 3x-ui periodically and supplies valid inbound IDs to handlers.
3. A scheduler runs once on startup if today’s run is not recorded, then at the next UTC midnight. It reads expiring subscriptions from PostgreSQL and sends Telegram notifications.
4. Telebot starts in webhook or long-poll mode. Authentication/admin middleware, an in-memory FSM, and per-user locking protect callback and message flows.
5. PostgreSQL is the commercial record for users, plans, subscriptions, wallet balance, transactions, top-ups, purchase requests, refund requests, reconciliation records, settings, and test usage. Subscription cancellation/deletion is represented by an auditable lifecycle status; rows are not physically removed by service management.
6. Test and wallet-paid subscription creation validates a DB plan, calls `clients/add`, and inserts the DB subscription. Add/update writes classify success, definitive failure, or unknown outcome. Timeout writes perform a read-after-write lookup by email; creates are never blindly retried. An uncertain paid operation is retained as retryable/reconciliation state rather than refunded as a confirmed failure.
7. Remote create success with local DB failure compensation: If 3x-ui creation succeeds but local DB insertion fails, fire-and-forget goroutines are eliminated. The system attempts synchronous compensating deletion and verifies remote state via `resolveDeleteOutcome`. If remote deletion is confirmed absent (`deleteConfirmed`), an idempotent wallet refund is issued. If deletion outcome is unknown (`deleteReconciliationRequired`) or the client remains present (`deleteStillPresent`), no refund is issued; the exact remote identity (email, UUID, subID, inbounds, expiry, limits, totalGB, plan/user info) is retained and persisted to `reconciliation_records` with an explicit commercial desired action (`confirm_delete_and_refund` or `adopt_subscription_or_delete`).
8. Missing/nil XUI client safety: If `bot.XUIClient == nil`, handlers return typed `ErrXUIClientUnavailable`. Operations requiring XUI fail explicitly rather than claiming success; wallet extensions/upgrades issue idempotent refunds without mutating DB state. In `HandleDeleteSubscription` (reseller cancellation), a nil XUI client immediately blocks cancellation with an explicit error before any DB cancellation or refund is initiated, leaving commercial state untouched.
9. Wallet refund persistence failures: If `CreditWalletBalanceWithKey` fails during refund, the operation does not claim to have refunded; a critical `pending_refund` reconciliation record is created for manual or background reconciliation.
10. Service management is DB-owned by `subscriptions.user_id`. 3x-ui `group` is metadata only: My Services never adopts unknown clients or filters ownership by group, and a missing remote client is logged as drift while the DB row remains visible. Broad updates fetch the complete current client and merge only bot-owned fields, preserving comments, group, HWID limits, and newer fields.
11. Wallet mutations and admin approvals use durable operation keys with database uniqueness. Direct-payment approval records payment approval separately from provisioning status, so provisioning failure cannot erase the approved-payment transaction. Remote-success/DB-failure upgrade paths mark the subscription `reconciliation_required` and retain desired state for repair.

## Baseline test inventory

- `go test -count=1 -v ./...` (with `TEST_DATABASE_URL` pointing to isolated PostgreSQL):
  - `internal/bot/handlers`: **PASS** (100% pass, including `TestNilXUIClientReturnsExplicitErrorAndTriggersRefund`, `TestRemoteCreateSuccessDbFailureCompensation` Scenarios A, B, C, D, `TestSafeRefundWalletOutcome`, and `TestNilXUIClientBlocksCancellationAndRefund`).
  - `internal/db`: **PASS** (8/8 tests pass, including `TestConcurrencyStress`, `TestSettingsRepo`, `TestSubscriptionRepo`, `TestCancelSubscriptionWithRefundIsAuditable`, `TestDeletePlanWithUsage`, `TestPlanSyncSubs`, `TestPurchaseRollbackAndClaim`, `TestWalletOperationKeyIsConcurrentIdempotent`).
  - `internal/fsm`: **PASS**.
  - `internal/xui`: **PASS**.
  - `tests/e2e:TestE2ESuite`: Skipped when running isolated DB unit tests without the dedicated end-to-end bot harness.
- `go vet ./...` — **PASS**, no diagnostics.
- `go test -race -count=1 ./...` — **BLOCKED before tests**: CGO is disabled and no C compiler is available in this environment.

