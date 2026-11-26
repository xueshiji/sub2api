-- Group-level cache hit rate adjustment (0 ~ 0.10, i.e. 0% ~ 10%),
-- usage_logs admin audit fields, and Channel Monitor V2 split reduction counters.

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS cache_hit_rate_adjustment NUMERIC(10,4) NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'chk_groups_cache_hit_rate_adjustment'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT chk_groups_cache_hit_rate_adjustment
            CHECK (cache_hit_rate_adjustment >= 0 AND cache_hit_rate_adjustment <= 0.1);
    END IF;
END $$;

COMMENT ON COLUMN groups.cache_hit_rate_adjustment IS
    'Group cache hit rate adjustment ratio in [0, 0.10] (e.g. 0.05 = -5%); applied when real cache hit rate > 10%.';

-- Keep enqueue_group_auth_cache_invalidation() in sync with all cached Group fields
-- (extends migration 240_group_token_limit_auth_cache_invalidation.sql).
CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_group_id BIGINT;
BEGIN
    target_group_id := OLD.id;
    IF TG_OP = 'UPDATE'
       AND OLD.status IS NOT DISTINCT FROM NEW.status
       AND OLD.is_exclusive IS NOT DISTINCT FROM NEW.is_exclusive
       AND OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation
       AND OLD.platform IS NOT DISTINCT FROM NEW.platform
       AND OLD.subscription_type IS NOT DISTINCT FROM NEW.subscription_type
       AND OLD.rate_multiplier IS NOT DISTINCT FROM NEW.rate_multiplier
       AND OLD.daily_limit_tokens IS NOT DISTINCT FROM NEW.daily_limit_tokens
       AND OLD.weekly_limit_tokens IS NOT DISTINCT FROM NEW.weekly_limit_tokens
       AND OLD.monthly_limit_tokens IS NOT DISTINCT FROM NEW.monthly_limit_tokens
       AND OLD.peak_rate_enabled IS NOT DISTINCT FROM NEW.peak_rate_enabled
       AND OLD.peak_start IS NOT DISTINCT FROM NEW.peak_start
       AND OLD.peak_end IS NOT DISTINCT FROM NEW.peak_end
       AND OLD.peak_rate_multiplier IS NOT DISTINCT FROM NEW.peak_rate_multiplier
       AND OLD.off_peak_rate_multiplier IS NOT DISTINCT FROM NEW.off_peak_rate_multiplier
       AND OLD.peak_model_multipliers IS NOT DISTINCT FROM NEW.peak_model_multipliers
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
       AND OLD.cache_hit_rate_adjustment IS NOT DISTINCT FROM NEW.cache_hit_rate_adjustment
       AND OLD.deleted_at IS NOT DISTINCT FROM NEW.deleted_at THEN
        RETURN NEW;
    END IF;

    INSERT INTO auth_cache_invalidation_outbox (cache_key)
    SELECT encode(sha256(convert_to(k.key, 'UTF8')), 'hex')
    FROM api_keys AS k
    WHERE k.group_id = target_group_id
      AND k.deleted_at IS NULL
      AND k.key <> '';
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS cache_hit_rate_adjustment NUMERIC(10,4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_read_tokens_reduction INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN usage_logs.cache_hit_rate_adjustment IS
    'Effective cache hit rate adjustment ratio applied to this request (0 if not applied).';
COMMENT ON COLUMN usage_logs.cache_read_tokens_reduction IS
    'Number of cache_read_tokens shifted to input_tokens by cache hit rate adjustment (admin audit).';

ALTER TABLE channel_monitor_v2_metrics_1m
    ADD COLUMN IF NOT EXISTS cache_read_reduction BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_monitor_v2_metrics_rollup
    ADD COLUMN IF NOT EXISTS cache_read_reduction BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_monitor_v2_user_metrics_1m
    ADD COLUMN IF NOT EXISTS cache_read_reduction BIGINT NOT NULL DEFAULT 0;
ALTER TABLE channel_monitor_v2_user_metrics_rollup
    ADD COLUMN IF NOT EXISTS cache_read_reduction BIGINT NOT NULL DEFAULT 0;
