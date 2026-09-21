# Current Architecture — `xui-resell-bot`

Updated 2026-09-21 after the Persian translation, integer Toman pricing snapshots, reconciliation worker, and starvation fix pass.

## Supported 3x-ui pin

**3x-ui panel: `v3.8.5` (stable).** The configured panel reported `currentVersion=3.8.5`, `latestVersion=v3.8.5`, and `updateAvailable=false` from the read-only `GET /panel/api/server/getPanelUpdateInfo` endpoint. The checked-in OpenAPI document describes the API compatibility line as `3.x`.

## Persian-Only Presentation Layer

Both bots are Persian-only (`internal/bot/persian`).
- Zero English strings in any customer or admin Telegram flows.
- Reusable message builders and formatters for prices (`FormatMoney`), IP limits (`FormatIPLimit`), traffic (`FormatTrafficBytes`), dates (`FormatPersianDate`), and lifecycle notifications.
- No multilingual runtime, locale branching, or i18n package.

## Integer Toman Accounting & Pricing Snapshots

- **Currency Unit**: Strictly Persian Toman (`"تومان"`). Default setting and arithmetic strictly use integer Toman.
- **Quote Snapshots**: Before debiting wallet balances or submitting direct payment requests, a quote snapshot is generated via `pricing.CalculateQuote` and persisted to `pricing_quotes`.
- **Auditable Lifecycle**: The `quote_id` is linked to `purchase_requests.quote_id` and `subscriptions.quote_id`.
- **Refunds**: Refund calculations (`pricing.CalculateRefund`) use historical paid amounts from the persisted quote snapshot rather than current catalog prices.

## DB → bot → 3x-ui flow

1. **Startup**: Loads `config.yaml`, connects to PostgreSQL, runs migrations (`internal/db/migrations.go` including versioned migration system), and normalizes configuration.
2. **XUI Client & Cache**: Creates an API-token 3x-ui client and starts an inbound cache (`internal/xui/cache.go`).
3. **Background Workers**:
   - **Scheduler**: Runs periodically, checking expiring subscriptions from PostgreSQL and enqueueing idempotent notifications via transactional outbox.
   - **Outbox Worker**: (`internal/services/outbox/outbox.go`) Polls pending outbox records and delivers Telegram notifications with retry and exponential backoff.
   - **First-Use Sync Worker**: (`internal/services/sync/sync_worker.go`) Checks unactivated subscriptions (`remote.ExpiryTime <= 0`) for first connection. Query uses `ORDER BY updated_at ASC NULLS FIRST, id ASC LIMIT 50` and touches `updated_at = NOW()` to prevent starvation.
   - **Reconciliation Worker**: (`internal/services/reconcile/processor.go`) Periodically claims pending reconciliation records (`ClaimPendingReconciliationRecords`), retrying failed/ambiguous provisioning (`direct_payment_provisioning_retry`), compensating deletions, and processing pending refunds.
4. **Telebot**: Starts in webhook or long-poll mode with authentication/admin middleware, FSM, and per-user locking.
5. **PostgreSQL Authority**: Commercial source of truth for users, plans, subscriptions, wallet balances, transactions, purchase requests, quotes, outbox events, and reconciliation records. Subscription rows are not physically removed by service management.
6. **Remote Create Compensation**: If 3x-ui creation succeeds but DB insertion fails, synchronous compensating deletion is attempted. If outcome is ambiguous, a reconciliation record is created.
7. **Direct Payment Provisioning**: When an admin approves a direct payment, payment approval is recorded immediately. If remote provisioning fails with a retryable outcome, a `direct_payment_provisioning_retry` reconciliation record is created for the worker.
8. **Cancellation & Refunds**: Cancellation in reseller bot immediately checks for active XUI connection. If remote delete is ambiguous or DB fails, reconciliation records are created.

## Baseline test inventory

- `go test -count=1 -v ./...`:
  - `internal/bot`: **PASS**
  - `internal/bot/handlers`: **PASS** (100% pass)
  - `internal/db`: **PASS** (100% pass)
  - `internal/fsm`: **PASS**
  - `internal/xui`: **PASS**
  - `internal/services/pricing`: **PASS**
  - `internal/services/reconcile`: **PASS**
  - `tests/e2e`: **PASS**
- `go vet ./...` — **PASS**, no diagnostics.
- `gofmt -l .` — **PASS**, zero unformatted files.
- `.github/workflows/ci.yml` — Automated CI with PostgreSQL service container running gofmt, go vet, unit/DB/e2e tests with race detection, and binary compilation.
