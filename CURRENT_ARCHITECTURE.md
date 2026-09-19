# Current Architecture — `xui-resell-bot`

Updated 2026-09-19 after the reseller safety and reconciliation changes.

## Supported 3x-ui pin

**3x-ui panel: `v3.8.5` (stable).** The configured panel reported `currentVersion=3.8.5`, `latestVersion=v3.8.5`, and `updateAvailable=false` from the read-only `GET /panel/api/server/getPanelUpdateInfo` endpoint. The checked-in OpenAPI document describes the API compatibility line as `3.x`.

## DB → bot → 3x-ui flow

1. Startup loads `config.yaml`, connects to PostgreSQL, applies the embedded `internal/db/schema.sql` migration, and normalizes legacy IP-limit values.
2. The bot creates an API-token 3x-ui client and starts an inbound cache. The cache refreshes inbound options from 3x-ui periodically and supplies valid inbound IDs to handlers.
3. A scheduler runs once on startup if today’s run is not recorded, then at the next UTC midnight. It reads expiring subscriptions from PostgreSQL and sends Telegram notifications.
4. Telebot starts in webhook or long-poll mode. Authentication/admin middleware, an in-memory FSM, and per-user locking protect callback and message flows.
5. PostgreSQL is the commercial record for users, plans, subscriptions, wallet balance, transactions, top-ups, purchase requests, refund requests, reconciliation records, settings, and test usage. Subscription cancellation/deletion is represented by an auditable lifecycle status; rows are not physically removed by service management.
6. Test and wallet-paid subscription creation validates a DB plan, calls `clients/add`, and inserts the DB subscription. Add/update writes classify success, definitive failure, or unknown outcome. Timeout writes perform a read-after-write lookup by email; creates are never blindly retried. An uncertain paid operation is retained as retryable/reconciliation state rather than refunded as a confirmed failure.
7. Service management is DB-owned by `subscriptions.user_id`. 3x-ui `group` is metadata only: My Services never adopts unknown clients or filters ownership by group, and a missing remote client is logged as drift while the DB row remains visible. Broad updates fetch the complete current client and merge only bot-owned fields, preserving comments, group, HWID limits, and newer fields.
8. Wallet mutations and admin approvals use durable operation keys with database uniqueness. Direct-payment approval records payment approval separately from provisioning status, so provisioning failure cannot erase the approved-payment transaction. Remote-success/DB-failure upgrade paths mark the subscription `reconciliation_required` and retain desired state for repair.

## Baseline test inventory

- `go test -count=1 -v ./...` — **PASS, exit 0** for the available local tests; DB-backed tests remain skipped because the configured database is not an isolated authenticated test database.
  - Passing test packages: `internal/bot`, `internal/bot/handlers`, `internal/fsm`, `internal/xui`.
  - Skipped DB tests: `TestConcurrencyStress`, `TestSettingsRepo`, `TestSubscriptionRepo`, `TestDeletePlanWithUsage`, `TestPlanSyncSubs`, `TestPurchaseRollbackAndClaim`.
  - Skipped integration suite: `tests/e2e:TestE2ESuite`.
  - Skip reason for all DB-backed tests: configured local PostgreSQL `localhost:5432/xui_bot` could not authenticate user `xui_bot` (SQLSTATE `28P01`). The e2e suite uses mocked Telegram/3x-ui services but its DB setup is not isolated and would truncate the configured database if it connected successfully.
- `go vet ./...` — **PASS**, no diagnostics.
- `go test -race -count=1 ./...` — **BLOCKED before tests**: CGO is disabled and no C compiler is available in this environment.

No test failures were observed. DB-backed unit/integration behavior remains unverified until an isolated test database is supplied.
