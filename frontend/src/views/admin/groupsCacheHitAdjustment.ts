export const MAX_CACHE_HIT_ADJUSTMENT_PERCENT = 10;

export const cacheHitAdjustmentPercentToDecimal = (
  value: number | string | null | undefined,
): number => {
  if (value === null || value === undefined || value === "") {
    return 0;
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 100) / 10000;
};

export const cacheHitAdjustmentDecimalToPercent = (
  value: number | null | undefined,
): number => {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0;
  }
  return Math.round(parsed * 1e6) / 1e4;
};

export const formatCacheHitAdjustmentPercent = (
  decimalValue: number | null | undefined,
): string => {
  const percent = cacheHitAdjustmentDecimalToPercent(decimalValue);
  return String(percent);
};

export const validateCacheHitAdjustmentPercent = (
  value: number | string | null | undefined,
): string | null => {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  const parsed = Number(value);
  if (
    !Number.isFinite(parsed) ||
    parsed < 0 ||
    parsed > MAX_CACHE_HIT_ADJUSTMENT_PERCENT
  ) {
    return "rangeError";
  }
  if (cacheHitAdjustmentPercentToDecimal(parsed) > 0.1) {
    return "rangeError";
  }
  return null;
};
