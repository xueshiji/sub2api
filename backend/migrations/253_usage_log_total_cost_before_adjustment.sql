ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS total_cost_before_adjustment NUMERIC(20,10);

COMMENT ON COLUMN usage_logs.total_cost_before_adjustment IS
    'total_cost before cache hit rate adjustment, computed from pre-adjustment tokens at selling price; NULL unless cache_hit_rate_adjustment > 0.';
