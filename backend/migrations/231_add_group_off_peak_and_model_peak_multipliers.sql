-- Peak-rate extension: off-peak multiplier (applied outside the peak window and
-- on weekends) and per-model peak multipliers (exact model name or trailing-*
-- pattern; unmatched models fall back to peak_rate_multiplier). Both are part
-- of the API-key auth snapshot and feed the billing hot path and the
-- profit-control gate, so extend the durable invalidation trigger to cover the
-- new columns. Based on the latest function body from
-- 193_group_profit_control_auth_cache_invalidation.sql.

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS off_peak_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1.0,
    ADD COLUMN IF NOT EXISTS peak_model_multipliers JSONB;

COMMENT ON COLUMN groups.off_peak_rate_multiplier IS
    'Multiplier applied outside the peak window and on weekends when peak_rate_enabled is true; 1.0 keeps legacy behavior';
COMMENT ON COLUMN groups.peak_model_multipliers IS
    'Per-model peak multipliers keyed by model name or trailing-* pattern; unmatched models fall back to peak_rate_multiplier';

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
       AND OLD.peak_rate_enabled IS NOT DISTINCT FROM NEW.peak_rate_enabled
       AND OLD.peak_start IS NOT DISTINCT FROM NEW.peak_start
       AND OLD.peak_end IS NOT DISTINCT FROM NEW.peak_end
       AND OLD.peak_rate_multiplier IS NOT DISTINCT FROM NEW.peak_rate_multiplier
       AND OLD.off_peak_rate_multiplier IS NOT DISTINCT FROM NEW.off_peak_rate_multiplier
       AND OLD.peak_model_multipliers IS NOT DISTINCT FROM NEW.peak_model_multipliers
       AND OLD.profit_control_enabled IS NOT DISTINCT FROM NEW.profit_control_enabled
       AND OLD.profit_min_margin IS NOT DISTINCT FROM NEW.profit_min_margin
       AND OLD.profit_safety_buffer IS NOT DISTINCT FROM NEW.profit_safety_buffer
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
