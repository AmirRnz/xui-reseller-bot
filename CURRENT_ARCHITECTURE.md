# Current Architecture — `xui-resell-bot`

Updated 2026-09-22 after fixing production blockers, reconciliation CAS & terminal-state protection, durable payment intents, integer pricing, reseller cancellation recovery, and release hardening pass.

## Supported 3x-ui pin

**3x-ui panel: `v3.8.5` (stable).** The configured panel reported `currentVersion=3.8.5`, `latestVersion=v3.8.5`, and `updateAvailable=false` from the read-only `GET /panel/api/server/getPanelUpdateInfo` endpoint. The checked-in OpenAPI document describes the API compatibility line as `3.x`.

## Persian-Only Presentation Layer

Both bots are Persian-only (`internal/bot/persian`).
- Zero English strings in any customer or admin Telegram flows.
- Strict Persian terminology: `Flow` is translated to `فلو`.
- Admin Reconciliation UI maps reconciliation kinds (`purchase_provisioning` -> `ایجاد اشتراک خرید`, etc.) and statuses (`pending` -> `در انتظار پردازش`, etc.) to clean Persian labels.
- Reusable message builders and formatters for prices (`FormatMoney`), IP limits (`FormatIPLimit` with explicit concurrent IP terminology `حداکثر N آی‌پی همزمان`), traffic (`FormatTraffic`), dates, and lifecycle notifications.
- No multilingual runtime, locale branching, or i18n package.
- Customer-safe error messages with tracking IDs, zero raw `err.Error()` / `%v` leaks.

## Integer Toman Accounting & Pricing Snapshots

- **Currency Unit**: Strictly Persian Toman (`"تومان"`). All prices, debits, credits, and refunds strictly use integer Toman (`int64` / `BIGINT`).
- **Integer Pricing Fields**: `PaidPlan` models feature `BasePriceToman`, `PricePerExtraIPToman`, `PricePerGBToman`, and `PricePerExtraMonthToman` to eliminate floating-point arithmetic.
- **Basis Points Discounts**: `DiscountTier` uses `BasisPoints int64` (`10000 = 100%`) for exact integer math.
- **Quote Snapshots**: Before debiting wallet balances or submitting direct payment requests, an immutable quote snapshot is generated via `pricing.CalculateQuote` and persisted to `pricing_quotes`.
- **Auditable Lifecycle**: The `quote_id` is linked to `purchase_requests.quote_id` and `subscriptions.quote_id`.
- **Checkout Integrity**: Purchases charge exact stored `quote.FinalPriceToman` rather than recalculating from live plan catalog. Quote equality verification strictly asserts user and plan identity.

## Database Migrations (Version 6)

- **Payment Intents**: `payment_intents` table tracks durable checkout intents (`card_number`, `amount_toman`, `intent_type`, `status`) before presenting bank/card details, ensuring receipt submission survives bot restarts.
- **Bulk Credit Operations**: `bulk_credit_operations` table tracks atomic bulk credit batches (`operation_key`, `amount_toman`, `recipient_count`, `recipient_user_ids`) with idempotency guards.
- **Reconciliation CAS & Terminal Protection**: State transition CAS ensures reconciliation records can only transition from active pending/retryable states (`pending`, `pending_refund`, `reconciliation_required`). Updates to terminal records (`resolved`, `resolved_verified`, `superseded`, `failed_terminal`, `manually_closed`, `manual_waiver`) are rejected.

## Safe 3x-ui ClientPatch & Bounded Pagination

- **ClientPatch Semantics**: Targeted updates via `ClientPatch` use pointers to distinguish between zero-value changes (`limitIp=0`, `expiryTime=0`) and unset fields. Unmanaged metadata (`subId`, `flow`, `group`, `tgId`, `comment`, `limitHwid`) is strictly preserved from full remote readback.
- **Strict Timeout Verification**: On remote timeout during client update, readback verification requires patched fields to match before reporting `WriteSucceeded`. Mismatched readback or missing client returns `WriteUnknown`.
- **Bounded Pagination**: `FindClientBySubID` uses targeted paged endpoint (`/panel/api/clients/list/paged?search={subId}&pageSize=10&page={page}`) bounded to at most 3 pages (`page=1..3`). Unconstrained `ListClients()` fallback scans have been completely eliminated from user-facing paths.

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
     - Resolves records with explicit target terminal statuses (`resolved`, `resolved_verified`, `superseded`).
     - Executes idempotent wallet refunds (`db.ErrWalletOperationAlreadyApplied`).
4. **Telebot**: Starts in webhook or long-poll mode with authentication/admin middleware, FSM, and per-user locking.
5. **PostgreSQL Authority**: Commercial source of truth for users, plans, subscriptions, wallet balances, transactions, purchase requests, quotes, outbox events, payment intents, and reconciliation records. Subscription rows are preserved for historical audit.
6. **Remote Create Compensation**: If 3x-ui creation succeeds but DB insertion fails, synchronous compensating deletion is attempted. If outcome is ambiguous, a typed reconciliation record is created.
7. **Direct Payment Provisioning**: When an admin approves a direct payment, payment approval is recorded immediately. If remote provisioning fails, a `direct_payment_provisioning_retry` reconciliation record is created for the worker.
8. **Cancellation & Refunds**: Cancellation in reseller bot immediately checks for active XUI connection. If remote delete is ambiguous or DB fails, reconciliation records are created (`subscription_cancellation_db_failure` vs `subscription_delete_unknown`). Legacy subscriptions without quotes route to admin manual refund review with `CalculatedAmount = 0` to preserve accounting safety.
9. **Admin Reconciliation UI**: Telegram admin interface with inspection (`admin_reconcile_detail`), structured state summary (`formatStateSummary`), immediate retry (`admin_reconcile_retry`), manual review flag (`admin_reconcile_mark_manual`), and auditable manual close requiring reason input (`admin_reconcile_close`).

## Baseline test inventory

- `go test -count=1 -p 1 -v ./...`:
  - `internal/bot`: **PASS**
  - `internal/bot/handlers`: **PASS** (100% pass)
  - `internal/bot/persian`: **PASS** (100% pass)
  - `internal/db`: **PASS** (100% pass including concurrency, migration v6, wallet credit idempotency, and payment intents)
  - `internal/fsm`: **PASS**
  - `internal/services/outbox`: **PASS** (100% pass)
  - `internal/services/pricing`: **PASS** (100% pass including integer pricing and basis points)
  - `internal/services/reconcile`: **PASS** (100% pass including contracts, identity checks, and ProcessOnce integration)
  - `internal/services/sync`: **PASS** (100% pass)
  - `internal/xui`: **PASS** (100% pass including contract tests, ClientPatch, and bounded pagination)
  - `tests/e2e`: **PASS** (100% pass with JSON mock telegram server and Postgres sequence resets)
- `go vet ./...` — **PASS**, zero diagnostics.
- `gofmt -l .` — **PASS**, zero unformatted files.
- `.github/workflows/ci.yml` — Automated CI with PostgreSQL service container running gofmt, go vet, isolated unit/DB/e2e tests with race detection (`go test -v -race -count=1 -p 1 ./...`), and binary compilation.
