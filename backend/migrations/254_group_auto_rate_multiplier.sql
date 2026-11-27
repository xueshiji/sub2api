-- Auto group rate multiplier configuration for OpenAI standard groups.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS auto_rate_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS auto_rate_mode VARCHAR(20) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auto_rate_value DECIMAL(10,4) NOT NULL DEFAULT 0;

COMMENT ON COLUMN groups.auto_rate_enabled IS
    'Whether OpenAI standard group automatically sets rate_multiplier from the highest active schedulable account rate_multiplier.';
COMMENT ON COLUMN groups.auto_rate_mode IS
    'Auto rate markup mode: percentage or fixed.';
COMMENT ON COLUMN groups.auto_rate_value IS
    'Auto rate markup value (decimal ratio for percentage, additive rate for fixed; >= 0).';
