-- Auto-rate fields are mapped into the API-key auth Group snapshot in api_key_repo.go;
-- extend enqueue_group_auth_cache_invalidation() to keep the trigger field list in sync
-- with migration 252_group_cache_hit_rate_adjustment.sql. Separate migration because 254
-- may already be applied and its checksum must remain stable.
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
       AND OLD.auto_rate_enabled IS NOT DISTINCT FROM NEW.auto_rate_enabled
       AND OLD.auto_rate_mode IS NOT DISTINCT FROM NEW.auto_rate_mode
       AND OLD.auto_rate_value IS NOT DISTINCT FROM NEW.auto_rate_value
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
