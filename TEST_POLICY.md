# Test Plan Policy & Repository Differences

This document describes the test plan policy, eligibility rules, quota resets, and lifecycle behavior across both `xui-end-bot` and `xui-resell-bot`.

---

## 1. Overview & Business Model Distinction

The two bot repositories serve different commercial audiences with distinct test subscription requirements:

| Dimension | `xui-end-bot` (End-User Bot) | `xui-resell-bot` (Reseller Bot) |
| :--- | :--- | :--- |
| **Audience** | End-user retail consumers | Commercial resellers / VPN distributors |
| **Purpose of Test** | Personal trial to test connectivity before buying | Demo accounts to distribute to prospective buyers |
| **Eligibility Model** | Cooldown reset period (`test_reset_days`) | Daily quota (`MaxPerDay` / `today` usage) |
| **Reseller Approval Impact**| N/A (all users are retail customers) | Unapproved resellers get lower trial limit; approved resellers get full `MaxPerDay` |
| **Batch Issuance** | Single test issuance | Bulk/batch generation supported (`fts_multi`) |

---

## 2. End-Bot Policy (`xui-end-bot`)

### 2.1 Eligibility & Cooldown
- **Tracking**: Tracked in `test_usage` table by `(user_id, plan_id)`.
- **Reset Logic**: Governed by the `test_reset_days` bot setting (default: 30 days):
  - When a user claims a test plan, the timestamp is stored.
  - The user cannot claim another test plan of the same type until `last_claimed_at + (test_reset_days * 24h)` has passed.
  - If `test_reset_days <= 0`, trial claims are strictly one-time per user per plan.
- **Limits**: 1 trial subscription per eligible period.

### 2.2 Lifecycle & Accounting
- Test subscriptions are recorded in PostgreSQL with `plan_type = 'test'`.
- First-connection duration: Expiry time is initialized as a negative value (`-expire_seconds * 1000`), meaning the countdown only begins upon the user's first connection to 3x-ui.
- Traffic limit is strictly enforced via `plan.MaxDataBytes`.

---

## 3. Reseller-Bot Policy (`xui-resell-bot`)

### 3.1 Daily Quotas & Approval Status
- **Tracking**: Tracked in `test_usage` table with daily timestamps (`GetTestUsageToday`).
- **Approval Tiers**:
  - **Unapproved Resellers**: Capped by the global setting `unapproved_test_limit_per_plan` (default: 1 test per day per plan).
  - **Approved Resellers**: Capped by the individual plan's `plan.MaxPerDay` setting configured by the administrator.
- **Reset Logic**: Quotas reset every day at 00:00 UTC.

### 3.2 Bulk / Multi-Test Generation
- Approved resellers can generate multiple test links in a single operation (`HandleMultipleTestsRun`).
- The system validates that `requested_count <= (limit - used_today)`.
- If an individual test creation fails during a batch, previously created tests remain valid and the error is cleanly reported.

---

## 4. Verification & Testing Standards

All automated tests and E2E suites verify these policies:
- `xui-end-bot` tests verify `test_reset_days` cooldown enforcement and one-trial-per-cooldown invariants.
- `xui-resell-bot` tests verify `DetermineTestLimit`, approval-state branching, and daily quota resets.
