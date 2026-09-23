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
- Migration 8 records a one-time preflight snapshot of the existing `currency_name`, representative paid-plan pricing/discount values, and wallet balances. It does not convert or relabel historical money. Migration 12 converts legacy percent discounts to basis points after that snapshot.
- The `currency_name` setting is not read during normal runtime. Historical settings can remain in old databases for audit.
- A quote-backed cancellation can use its immutable quote. A legacy subscription without quote history creates a zero-suggestion manual refund request; an admin enters a positive amount, records a required audit note, and confirms before the idempotent wallet credit commits. The current plan catalog is never used to reconstruct a legacy historical refund.

## XUI readiness gate

- Startup checks the supported panel capability/version. The current gate accepts the normalized 3.8.5 version family, matching the pinned panel API contract. A failed check keeps Telegram available while disabling XUI mutations.
- Add, update/patch, delete, attach, and other correctness-critical writes fail closed when readiness is false and return a definitive no-write result for readiness failures.
- A periodic readiness check can restore mutation capability after the panel recovers; restart is not required.
- Write timeouts remain unknown outcomes and are verified through remote reads. They are never treated as successful writes by themselves.

## Reconciliation contracts and terminal actions

`internal/services/reconcile/contracts.go` defines payload decoders and constructors. `Processor.processRecord` handles:

| Kind | Processor handler |
| --- | --- |
| `pending_refund` | `handlePendingRefund` |
| `purchase_provisioning_unknown` | `handlePurchaseReconciliation` |
| `purchase_remote_created_db_failed` | `handlePurchaseReconciliation` |
| `subscription_update_db_failed` | `handleUpdateReconciliation` |
| `subscription_delete_unknown` | `handleDeleteReconciliation` |
| `subscription_cancellation_db_failure` | `handleDeleteReconciliation` |
| `direct_payment_provisioning_retry` | `handleDirectPaymentProvisioning` |
| `subscription_remote_missing` | `handleSubscriptionRemoteMissing` |
| `subscription_claim_adoption` | `handleSubscriptionClaimAdoption` |

Unknown kinds move to manual review. CAS/version checks protect worker transitions. `pending_refund` can become verified only after the matching completed ledger credit is found. Generic manual close refuses financial obligations. Explicit `manual_waiver` records admin, timestamp, reason, amount, and operation key, and remains visibly distinct from verified resolution and manual review.

Intentional legacy policies: old subscriptions without quote history remain zero-suggestion requests until an admin enters an amount; ambiguous or unprovable remote provisioning goes to manual review; creating a second direct-payment intent is refused while an externally payable receipt intent remains active.

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

## Verification record

The former baseline inventory listed passing commands without tying them to a current commit; that inventory was removed. This document makes no pass claim. Use the current commit’s GitHub Actions run and the completion report for verification results. `go test` runs without `TEST_DATABASE_URL` skip DB-backed cases.
