# Transition Bot to Reseller Mode

This plan outlines the steps required to transition the current `xui-resell-bot` from an end-user-facing bot into a reseller-oriented bot, adapting its features according to the new workflow.

## Proposed Changes

### Database Schema Updates
- **`bot_users` table**: No major changes needed as `service_name` already exists and is unique.
- **`bot_settings` table**: 
  - Introduce a new setting `unapproved_test_limit_per_plan` (default: 1) which limits how many free tests an *unapproved* user can get per test plan per day.
  - **Remove** the `group_name` setting entirely as it is no longer relevant for the reseller flow.
- **New Table: `refund_requests`**: 
  - Create a new table to track refund requests triggered when a reseller deletes an active subscription.
  - Columns: `id`, `user_id`, `subscription_id` (nullable or reference), `calculated_amount`, `approved_amount`, `status` ('pending', 'approved', 'rejected'), `admin_id`, `created_at`, `updated_at`.

### Core Bot Logic (Middlewares & Handlers)

- **Language Management**:
  - Permanently hardcode the language for end-users to `fa` (Persian) and admin users to `en` (English).
  - Remove any existing UI or commands that allow users to change their language.

- **User Approval Flow**:
  - Unapproved users will have limited access. They can see pricing/plans and get a limited number of test subscriptions.
  - Implement a "Request Access" button on the start/main menu for unapproved users.
  - Update the admin panel to receive "Request Access" notifications and allow approval.
  - When approved, prompt the user to input their `service_name` (if not already set). 
  - **Service Name Constraints**: Validate that the input is 3-32 characters long, containing only letters, numbers, hyphens, or underscores (`^[a-zA-Z0-9_-]{3,32}$`). This ensures safe group creation in 3x-ui. The user cannot access the main reseller features until a valid `service_name` is set.

- **Test Subscriptions Configuration**:
  - For **Unapproved Users**: Enforce the global limit `unapproved_test_limit_per_plan` per plan per day.
  - For **Approved Users**: Enforce the `max_per_day` limit defined on the specific `test_plans` row.

- **3x-ui Client Creation**:
  - Modify the XUI client payload builder in `internal/xui/`.
  - Ensure we do not use the removed global `group_name`. Instead, explicitly use the `service_name` of the reseller as the logical grouping label (the `group_name` or `group` property in the 3x-ui payload).

- **My Services (Reseller Dashboard) & Refund Feature**:
  - Revamp the "My Services" UI for resellers.
  - Display the list of subscriptions owned by the reseller. We will query the bot's local `subscriptions` table (`WHERE user_id = ?`) to avoid heavy loading on 3x-ui.
  - Add inline buttons to each subscription to allow:
    1. **Disable/Enable**: Triggering 3x-ui API to toggle the `enable` flag without changing traffic or expiry.
    2. **Delete**: 
       - Prompt the reseller for confirmation.
       - If the subscription has not yet expired, calculate a suggested refund amount (e.g., based on the remaining days or unused traffic ratio relative to the original price).
       - Create a `refund_requests` record with status `pending`.
       - Send a notification to the admin with the calculated refund amount.
       - The admin can then Approve (with the exact or modified amount) or Reject the refund. Upon approval, the `wallet_balance` of the reseller is credited.
       - Delete the client from 3x-ui and mark the local `subscriptions` record as deleted or remove it.
  - Fetch real-time traffic statistics from 3x-ui when the reseller views the details of a specific client, keeping the initial list load lightweight.

## Verification Plan

### Automated Tests
- Unit tests for testing limit calculations for approved vs. unapproved users.
- Unit tests for the refund calculation logic to ensure proportional accuracy.

### Manual Verification
- Start the bot as a new user (unapproved).
- Verify only test limits and "Request Access" are available.
- Verify test creation respects the global unapproved limit.
- Click "Request Access" and verify the admin receives it.
- Approve the user and verify the `service_name` prompt flow, ensuring invalid characters are rejected.
- Verify approved users can create test plans according to plan-specific limits.
- Create a paid plan and test service, and verify in the 3x-ui panel that the client is assigned the correct group name (the `service_name`).
- Delete an active subscription and verify a refund request is sent to the admin.
- Admin modifies the refund amount and approves it, verifying the reseller's wallet is credited correctly.
