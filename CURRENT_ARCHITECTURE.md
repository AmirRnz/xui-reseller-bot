# Current Architecture — `xui-reseller-bot`

Updated 2026-09-23. This note describes the reseller legacy bot currently in this repository. It does not describe the separate backend, web panel, or international bots.

## Runtime authority

- PostgreSQL is authoritative for reseller/user ownership, orders, payment intents, integer-Toman quotes, wallet transactions, subscriptions, refunds, notifications, and reconciliation work.
- XUI is remote infrastructure state. A group value does not establish reseller ownership. Email alone is not enough to adopt a paid client; automatic adoption verifies the exact email, pre-persisted UUID, and SubID, then checks the durable desired state.
- New commerce uses `buy`, `extend`, `upgrade_ip`, and `topup`. New payment intents canonicalize `new_subscription` to `buy`. Historical rows are normalized by migration 9.
- After a payment intent exists, its action, plan/subscription, quote, amount, duration, limits, display name, email, and provisioning snapshot are authoritative. FSM state only locates the intent for the UI; it does not set receipt terms.

## Paid provisioning and crash recovery

- Wallet buy persists the exact remote identity and desired client fields before the XUI request. The wallet debit and reconciliation work item commit in one transaction.
- Direct-payment approval writes the financial audit entry and executable reconciliation work item, including the exact UUID/SubID and desired remote state, in one transaction.
- The worker stores `phase=ready` before any AddClient request and persists `create_attempted` before crossing that boundary. It can retry a confirmed no-write after resetting the phase. An unknown result keeps the attempted phase; if the remote client is absent later, the worker opens manual review instead of issuing a duplicate create.
- A remote client found after a crash is adopted only after exact identity and desired-state verification. A database insert failure leaves the durable work available for restart recovery.
- Approval leaves executable pending work in PostgreSQL. A new worker process can finish it without another admin callback.

## Money, refunds, and legacy values

- New buy, extend, IP upgrade, top-up, and refund paths use integer Toman (`BIGINT`/`int64`) and integer discount basis points. The shared Persian formatter displays amounts with `تومان`.
- Runtime commerce reads `BasePriceToman`, `PricePerExtraIPToman`, `PricePerGBToman`, `PricePerExtraMonthToman`, and `BasisPoints`. Legacy float fields remain in the model and DB as compatibility mirrors; they are not runtime price fallbacks.
- Migration 8 records an initial snapshot, but it cannot settle the edited migration 7 history: deployed databases may have integer plan prices copied from legacy floats, or may still have zero integer prices. Migration 13 captures migration versions, currency, plan values, wallet balances, representative transactions, quotes, and purchase requests, and flags rows that match the old migration 7 copy pattern.
- Migration 13 leaves the database pending until an operator reviews the report and explicitly selects `toman` or `rial` with `go run ./cmd/money-normalize -apply`. The report prints a database-specific confirmation token. Rial normalization converts legacy monetary columns and the copied plan prices atomically; Toman normalization fills missing integer plan prices. Both align compatibility float mirrors to integer prices and record the operator and decision. Reapplication is refused.
- Bot startup stays blocked while this decision is pending. Stop the bot, run `go run ./cmd/money-normalize -report`, review every deployment’s history, then apply the audited decision. The migration 7 match is a detection signal, not proof of the historical unit.
- The `currency_name` setting is not read during normal runtime. Migration 13 preserves its pre-normalization value in the audit snapshot; the operator action records the normalized display unit as `تومان`.
- A quote-backed cancellation can use its immutable quote. A legacy subscription without quote history creates a zero-suggestion manual refund request; an admin enters a positive amount, records a required audit note, and confirms before the idempotent wallet credit commits. The current plan catalog is never used to reconstruct a legacy historical refund.

## XUI readiness gate

- Startup checks the supported panel capability/version. The current gate accepts only the 3.8.5 release (with an optional `v` prefix or prerelease/build suffix), matching the pinned panel API contract and rejecting nearby versions such as 3.8.50. A failed check keeps Telegram available while disabling XUI mutations.
- Add, update/patch, delete, attach, and other correctness-critical writes fail closed when readiness is false and return a definitive no-write result for readiness failures.
- A periodic readiness check can restore mutation capability after the panel recovers; restart is not required.
- Panel application and HTTP errors can follow partial writes and are treated as unknown outcomes. Add reads back the client and repairs only verified missing inbound attachments; update transport failures verify desired state. Ambiguous writes are never repeated blindly.

## Free test issuance

- Test claims atomically reserve the user's daily quota in PostgreSQL before contacting XUI. The cap is the approved plan's `MaxPerDay` or the configured unapproved-reseller limit; quota dates roll over at 00:00 UTC.
- A reservation is released only after a confirmed no-write or confirmed cleanup. Unknown XUI state or unresolved cleanup retains the reservation and is surfaced to the user, preventing a retry from creating a duplicate test service.
- Quota lookup and reservation errors fail closed. Test service cleanup after a subscription insert failure is verified through panel readback; unresolved creation or cleanup is recorded for manual reconciliation.
- Bulk test issuance remains disabled; its callbacks explicitly report that status.

## Reconciliation contracts and terminal actions

`internal/services/reconcile/contracts.go` defines payload decoders and constructors. `Processor.processRecord` handles:

| Kind | Processor handler |
| --- | --- |
| `pending_refund` | `handlePendingRefund` |
| `purchase_provisioning_unknown` | `handlePurchaseReconciliation` |
| `purchase_remote_created_db_failed` | `handlePurchaseReconciliation` |
| `subscription_update_db_failed` | `handleUpdateReconciliation` |
| `subscription_delete_unknown` | `handleDeleteReconciliation` |
| `subscription_cancellation_db_failure` | `handleSubscriptionCancellation` |
| `direct_payment_provisioning_retry` | `handleDirectPaymentProvisioning` |
| `subscription_remote_missing` | `handleSubscriptionRemoteMissing` |
| `subscription_claim_adoption` | `handleSubscriptionClaimAdoption` |

Unknown kinds move to manual review. CAS/version checks protect worker transitions. `pending_refund` can become verified only after the matching completed ledger credit is found. Generic manual close refuses financial obligations. Explicit `manual_waiver` records admin, timestamp, reason, amount, and operation key, and remains visibly distinct from verified resolution and manual review.

Intentional legacy policies: old subscriptions without quote history remain zero-suggestion requests until an admin enters an amount; ambiguous or unprovable remote provisioning goes to manual review; creating a second direct-payment intent is refused while an externally payable receipt intent remains active. `/cancel` clears the chat FSM but preserves the payment intent and offers resume/send-receipt or an explicitly confirmed cancellation for a checkout the customer says they did not pay. A cancelled intent remains auditable and no longer occupies the active-intent slot.

Admin Sync All treats the PostgreSQL IP limit as authoritative and transforms it for XUI according to the current setting. It never divides or rewrites database IP limits based on that setting. A one-time repair is available through `go run ./cmd/ip-limit-repair -dry-run -factor <factor>`; review divisible and excluded rows, then apply with the printed token, the same factor, and an operator identifier. The repair records original/new values and refuses a second run.

## Other workers and presentation

- The scheduler writes idempotent notifications to the transactional outbox; the outbox worker delivers them after restart.
- The first-use worker preserves first-use expiry semantics and records reconciliation work when remote/local updates diverge.
- Telegram customer/admin wording is Persian for this pass. Technical logs, protocol/API names, callback keys, enum values, and identifiers remain English. No locale or multicurrency runtime was added.

## Database changes in the Legacy Exit Gate pass

- Migration 8: one-time legacy currency/pricing/wallet preflight report.
- Migration 9: durable purchase snapshots, historical action canonicalization, and a partial unique active-intent index.
- Migration 10: audited reconciliation manual-action fields.
- Migration 11: refund approval amount, admin audit note, and approval timestamp.
- Migration 12: integer basis-point discount migration from preflighted legacy percentage values.
- Migration 13: payment-intent cancellation audit fields and an operator-gated money-unit audit/normalization record.
- Migration 14: durable one-time IP-limit repair audit and row history.

## Verification record

Regression coverage includes malformed/partial XUI write responses, exact supported-version matching, readiness context cancellation, concurrent daily quota reservations, and bounded plan conversions. Database-backed cases require an isolated `TEST_DATABASE_URL`; without it, they skip.
