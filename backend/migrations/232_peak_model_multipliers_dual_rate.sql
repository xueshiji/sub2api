-- Per-model multiplier rules now carry both rates: peak (inside the weekday
-- window) and off_peak (outside the window and on weekends). Convert legacy
-- plain-number values (peak-only rules) to rule objects, seeding off_peak with
-- the group's current default so billed behavior is unchanged after migration.
-- Idempotent: object-shaped values pass through untouched.

UPDATE groups AS g
SET peak_model_multipliers = (
    SELECT jsonb_object_agg(
        key,
        CASE WHEN jsonb_typeof(value) = 'number'
             THEN jsonb_build_object('peak', value, 'off_peak', g.off_peak_rate_multiplier)
             ELSE value
        END
    )
    FROM jsonb_each(g.peak_model_multipliers)
)
WHERE g.peak_model_multipliers IS NOT NULL
  AND EXISTS (
      SELECT 1
      FROM jsonb_each(g.peak_model_multipliers)
      WHERE jsonb_typeof(value) = 'number'
  );
