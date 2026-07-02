CREATE TABLE IF NOT EXISTS bot_users (
    id BIGSERIAL PRIMARY KEY,
    telegram_id BIGINT UNIQUE NOT NULL,
    username TEXT DEFAULT '',
    first_name TEXT DEFAULT '',
    last_name TEXT DEFAULT '',
    language TEXT DEFAULT 'en',
    status TEXT NOT NULL DEFAULT 'pending',
    service_name TEXT UNIQUE,
    wallet_balance BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS bot_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS test_plans (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    usage_description TEXT NOT NULL DEFAULT '',
    inbound_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    expire_seconds BIGINT NOT NULL DEFAULT 3600,
    max_data_bytes BIGINT NOT NULL DEFAULT 0,
    flow TEXT NOT NULL DEFAULT '',
    max_per_day INT NOT NULL DEFAULT 1,
    is_global BOOLEAN NOT NULL DEFAULT TRUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sync_subs BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS paid_plans (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    usage_description TEXT NOT NULL DEFAULT '',
    inbound_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    base_price NUMERIC(14, 2) NOT NULL DEFAULT 0,
    base_ip_limit INT NOT NULL DEFAULT 1,
    max_ip_limit INT NOT NULL DEFAULT 1,
    price_per_extra_ip NUMERIC(14, 2) NOT NULL DEFAULT 0,
    flow TEXT NOT NULL DEFAULT '',
    discount_tiers JSONB NOT NULL DEFAULT '[]'::jsonb,
    is_global BOOLEAN NOT NULL DEFAULT TRUE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sync_subs BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS plan_user_access (
    id BIGSERIAL PRIMARY KEY,
    plan_type TEXT NOT NULL,
    plan_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    UNIQUE (plan_type, plan_id, user_id)
);

CREATE TABLE IF NOT EXISTS test_usage (
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    plan_id BIGINT NOT NULL REFERENCES test_plans(id) ON DELETE CASCADE,
    used_count INT NOT NULL DEFAULT 0,
    reset_date DATE NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, plan_id, reset_date)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    plan_id BIGINT,
    plan_type TEXT NOT NULL,
    client_email TEXT UNIQUE NOT NULL,
    client_uuid TEXT NOT NULL DEFAULT '',
    sub_id TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    ip_limit INT NOT NULL DEFAULT 1,
    expire_time BIGINT,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    status TEXT NOT NULL DEFAULT 'active',
    start_date TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    end_date TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS topup_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    telegram_file_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    amount BIGINT,
    admin_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS transactions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT REFERENCES bot_users(id) ON DELETE SET NULL,
    amount BIGINT NOT NULL,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'completed',
    description TEXT NOT NULL DEFAULT '',
    reference_type TEXT NOT NULL DEFAULT '',
    reference_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$
BEGIN
    -- Drop default for service_name if it exists
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'bot_users' AND column_name = 'service_name' AND column_default IS NOT NULL
    ) THEN
        ALTER TABLE bot_users ALTER COLUMN service_name DROP DEFAULT;
    END IF;

    -- Alter wallet_balance to bigint if it exists and is not bigint
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'bot_users' AND column_name = 'wallet_balance' AND data_type <> 'bigint'
    ) THEN
        ALTER TABLE bot_users ALTER COLUMN wallet_balance TYPE BIGINT USING ROUND(wallet_balance::NUMERIC)::BIGINT;
    END IF;
END $$;

ALTER TABLE IF EXISTS bot_users
    ADD COLUMN IF NOT EXISTS language TEXT DEFAULT 'en',
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS wallet_balance BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'bot_users' AND column_name = 'service_name'
    ) THEN
        UPDATE bot_users SET service_name = NULL WHERE service_name = '';
    END IF;
END $$;

ALTER TABLE IF EXISTS subscriptions
    ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ip_limit INT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS expire_time BIGINT,
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS start_date TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS end_date TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE IF EXISTS bot_settings
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE IF EXISTS subscriptions
    ADD COLUMN IF NOT EXISTS client_uuid TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS topup_requests
    ADD COLUMN IF NOT EXISTS admin_id BIGINT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE IF EXISTS transactions
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'completed',
    ADD COLUMN IF NOT EXISTS reference_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reference_id BIGINT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE IF EXISTS test_usage
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- Ensure test_usage_plan_id_fkey has ON DELETE CASCADE
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 
        FROM information_schema.table_constraints 
        WHERE constraint_name = 'test_usage_plan_id_fkey' AND table_name = 'test_usage'
    ) THEN
        ALTER TABLE test_usage DROP CONSTRAINT test_usage_plan_id_fkey;
    END IF;
    
    IF NOT EXISTS (
        SELECT 1 
        FROM information_schema.table_constraints 
        WHERE constraint_name = 'test_usage_plan_id_fkey' AND table_name = 'test_usage'
    ) THEN
        ALTER TABLE test_usage 
            ADD CONSTRAINT test_usage_plan_id_fkey 
            FOREIGN KEY (plan_id) REFERENCES test_plans(id) ON DELETE CASCADE;
    END IF;
END $$;

INSERT INTO bot_settings (key, value) VALUES
    ('auto_approve_users', 'false'),
    ('test_reset_days', '30'),
    ('support_username', ''),
    ('test_global_description', ''),
    ('card_number', ''),
    ('card_owner', ''),
    ('topup_description', ''),
    ('min_topup_amount', '0'),
    ('currency_name', 'IRR'),
    ('currency_symbol', ''),
    ('expiry_notify_days', '3,1'),
    ('ip_limit_factor', ''),
    ('unapproved_test_limit_per_plan', '1')
ON CONFLICT (key) DO NOTHING;

DELETE FROM bot_settings WHERE key = 'group_name';

ALTER TABLE IF EXISTS test_plans
    ADD COLUMN IF NOT EXISTS sync_subs BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE IF EXISTS paid_plans
    ADD COLUMN IF NOT EXISTS sync_subs BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE IF EXISTS paid_plans
    ADD COLUMN IF NOT EXISTS is_limited BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS price_per_gb NUMERIC(14, 2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS min_data_gb BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS price_per_extra_month NUMERIC(14, 2) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS test_plans
    ADD COLUMN IF NOT EXISTS usage_description TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS paid_plans
    ADD COLUMN IF NOT EXISTS usage_description TEXT NOT NULL DEFAULT '';

ALTER TABLE IF EXISTS subscriptions
    ADD COLUMN IF NOT EXISTS traffic_limit_bytes BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS purchase_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    type TEXT NOT NULL, -- 'buy', 'extend', 'upgrade_ip'
    plan_id BIGINT,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE SET NULL,
    price NUMERIC(14, 2) NOT NULL,
    months INT NOT NULL DEFAULT 0,
    ip_limit INT NOT NULL DEFAULT 0,
    data_gb INT NOT NULL DEFAULT 0,
    custom_name TEXT NOT NULL DEFAULT '',
    client_email TEXT NOT NULL DEFAULT '',
    telegram_file_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'approved', 'rejected'
    admin_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS refund_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES bot_users(id) ON DELETE CASCADE,
    subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE SET NULL,
    calculated_amount BIGINT NOT NULL DEFAULT 0,
    approved_amount BIGINT,
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'approved', 'rejected'
    admin_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
