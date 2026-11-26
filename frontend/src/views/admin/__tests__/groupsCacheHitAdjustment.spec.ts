import { describe, expect, it } from "vitest";

import {
  cacheHitAdjustmentDecimalToPercent,
  cacheHitAdjustmentPercentToDecimal,
  formatCacheHitAdjustmentPercent,
  validateCacheHitAdjustmentPercent,
} from "../groupsCacheHitAdjustment";

describe("groupsCacheHitAdjustment", () => {
  it("converts percent values between 0 and 10 into 4-decimal ratios", () => {
    expect(cacheHitAdjustmentPercentToDecimal(0)).toBe(0);
    expect(cacheHitAdjustmentPercentToDecimal(5)).toBe(0.05);
    expect(cacheHitAdjustmentPercentToDecimal(2.55)).toBe(0.0255);
    expect(cacheHitAdjustmentPercentToDecimal(10)).toBe(0.1);
    expect(cacheHitAdjustmentPercentToDecimal("")).toBe(0);
    expect(cacheHitAdjustmentPercentToDecimal(null)).toBe(0);
    expect(cacheHitAdjustmentPercentToDecimal(-1)).toBe(0);
  });

  it("converts backend decimals back to display percentages cleanly", () => {
    expect(cacheHitAdjustmentDecimalToPercent(0)).toBe(0);
    expect(cacheHitAdjustmentDecimalToPercent(0.05)).toBe(5);
    expect(cacheHitAdjustmentDecimalToPercent(0.0255)).toBe(2.55);
    expect(cacheHitAdjustmentDecimalToPercent(0.1)).toBe(10);
    expect(formatCacheHitAdjustmentPercent(0.05)).toBe("5");
    expect(formatCacheHitAdjustmentPercent(0.0255)).toBe("2.55");
  });

  it("validates percent bounds in [0, 10]", () => {
    expect(validateCacheHitAdjustmentPercent(0)).toBeNull();
    expect(validateCacheHitAdjustmentPercent(5)).toBeNull();
    expect(validateCacheHitAdjustmentPercent(10)).toBeNull();
    expect(validateCacheHitAdjustmentPercent("")).toBeNull();
    expect(validateCacheHitAdjustmentPercent(null)).toBeNull();
    expect(validateCacheHitAdjustmentPercent(-0.1)).toBe("rangeError");
    expect(validateCacheHitAdjustmentPercent(10.01)).toBe("rangeError");
    expect(validateCacheHitAdjustmentPercent("abc")).toBe("rangeError");
  });
});
