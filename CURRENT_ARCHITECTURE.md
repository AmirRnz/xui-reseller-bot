# Current Architecture — `xui-resell-bot`

Updated 2026-09-21 after the stabilization, correctness, typed reconciliation contracts, and release hardening pass.

## Supported 3x-ui pin

**3x-ui panel: `v3.8.5` (stable).** The configured panel reported `currentVersion=3.8.5`, `latestVersion=v3.8.5`, and `updateAvailable=false` from the read-only `GET /panel/api/server/getPanelUpdateInfo` endpoint. The checked-in OpenAPI document describes the API compatibility line as `3.x`.

## Persian-Only Presentation Layer

Both bots are Persian-only (`internal/bot/persian`).
- Zero English strings in any customer or admin Telegram flows.
- Reusable message builders and formatters for prices (`FormatMoney`), IP limits (`FormatIPLimit` with explicit concurrent IP terminology `حداکثر N آی‌پی همزمان`), traffic (`FormatTraffic`), dates, and lifecycle notifications.
- No multilingual runtime, locale branching, or i18n package.
- Customer-safe error messages with tracking IDs (`SafeErrorMessage`), zero raw `err.Error()` / `%v` leaks.

## Integer Toman Accounting & Pricing Snapshots

- **Currency Unit**: Strictly Persian Toman (`"تومان"`). All prices, debits, credits, and refunds strictly use integer Toman (`int64` / `BIGINT`).
- **Quote Snapshots**: Before debiting wallet balances or submitting direct payment requests, an immutable quote snapshot is generated via `pricing.CalculateQuote` and persisted to `pricing_quotes`.
- **Auditable Lifecycle**: The `quote_id` is linked to `purchase_requests.quote_id` and `subscriptions.quote_id`.
- **Checkout Integrity**: Purchases charge exact stored `quote.FinalPriceToman` rather than recalculating from live plan catalog.

## DB → bot → 3x-ui flow

1. **Startup**: Loads `config.yaml`, connects to PostgreSQL, runs versioned migrations (`internal/db/migrations.go`), and normalizes configuration.
2. **XUI Client & Cache**: Creates an API-token 3x-ui client and starts an inbound cache (`internal/xui/cache.go`). Includes comprehensive contract tests for all API endpoints (`GetInbounds`, `AddClient`, `UpdateClient`, `DeleteClient`, `GetClientByEmail`, `CheckReadiness`).
3. **Background Workers**:
   - **Scheduler**: Runs periodically, checking expiring subscriptions from PostgreSQL and enqueueing idempotent notifications via transactional outbox. Only advances `last_scheduler_run` upon complete success.
   - **Outbox Worker**: (`internal/services/outbox/outbox.go`) Polls pending outbox records and delivers Telegram notifications with typed `telebot.FloodError` backoff handling.
   - **First-Use Sync Worker**: (`internal/services/sync/sync_worker.go`) Checks unactivated subscriptions (`remote.ExpiryTime <= 0`) for first connection without starvation. On remote missing client or activation DB update failure, persists durable reconciliation records.
   - **Reconciliation Worker**: (`internal/services/reconcile/processor.go`):
     - Uses typed reconciliation contracts (`internal/services/reconcile/contracts.go`) with strict schema validation.
     - Classifies remote client state (`ClassifyRemoteClient`).
     - Performs safe remote adoption (`verifyClientIdentity`).
     - Performs 3-way desired-vs-observed update comparison.
     - Executes idempotent wallet refunds (`db.ErrWalletOperationAlreadyApplied`).
4. **Telebot**: Starts in webhook or long-poll mode with authentication/admin middleware, FSM, and per-user locking.
5. **PostgreSQL Authority**: Commercial source of truth for users, plans, subscriptions, wallet balances, transactions, purchase requests, quotes, outbox events, and reconciliation records. Subscription rows are preserved for historical audit.
6. **Remote Create Compensation**: If 3x-ui creation succeeds but DB insertion fails, synchronous compensating deletion is attempted. If outcome is ambiguous, a reconciliation record is created.
7. **Direct Payment Provisioning**: When an admin approves a direct payment, payment approval is recorded immediately. If remote provisioning fails, a `direct_payment_provisioning_retry` reconciliation record is created for the worker.
8. **Cancellation & Refunds**: Cancellation in reseller bot immediately checks for active XUI connection. If remote delete is ambiguous or DB fails, reconciliation records are created.
9. **Admin Reconciliation UI**: Telegram admin interface with inspection (`admin_reconcile_detail`), immediate retry (`admin_reconcile_retry`), manual review flag (`admin_reconcile_mark_manual`), and auditable manual close requiring reason input (`admin_reconcile_close`).

## Baseline test inventory

- `go test -count=1 -v ./...`:
  - `internal/bot`: **PASS**
  - `internal/bot/handlers`: **PASS** (100% pass)
  - `internal/bot/persian`: **PASS** (100% pass)
  - `internal/db`: **PASS** (100% pass)
  - `internal/fsm`: **PASS**
  - `internal/services/outbox`: **PASS** (100% pass)
  - `internal/services/pricing`: **PASS** (100% pass)
  - `internal/services/reconcile`: **PASS** (100% pass)
  - `internal/xui`: **PASS** (100% pass including contract tests)
  - `tests/e2e`: **PASS** (100% pass)
- `go vet ./...` — **PASS**, zero diagnostics.
- `gofmt -l .` — **PASS**, zero unformatted files.
- `.github/workflows/ci.yml` — Automated CI with PostgreSQL service container running gofmt, go vet, unit/DB/e2e tests with race detection, and binary compilation.
