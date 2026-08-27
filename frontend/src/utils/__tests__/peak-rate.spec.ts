import { describe, expect, it, vi } from 'vitest'

import { formatPeakRateWindow, hasOffPeakRate, hasPeakModelMultipliers, hasPeakRate } from '../peak-rate'

vi.mock('@/i18n', () => ({
  i18n: {
    global: {
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'common.peakRateWeekdaysOnly') return '周一至周五'
        if (key === 'common.peakRateOffPeak') return '非高峰'
        if (key === 'common.peakRateModelRules') return `${params?.count} 条分模型倍率`
        return key
      },
    },
  },
}))

describe('formatPeakRateWindow', () => {
  it('returns an empty string when peak rate is not configured', () => {
    expect(formatPeakRateWindow(null)).toBe('')
    expect(
      formatPeakRateWindow({ peak_rate_enabled: false, peak_start: '14:00', peak_end: '18:00' })
    ).toBe('')
  })

  it('appends the weekday-only label after the multiplier', () => {
    expect(
      formatPeakRateWindow(
        { peak_rate_enabled: true, peak_start: '14:00', peak_end: '18:00', peak_rate_multiplier: 2 },
        'UTC+08:00'
      )
    ).toBe('14:00-18:00 ×2 周一至周五 (UTC+08:00)')
  })

  it('omits the timezone suffix when tzLabel is empty', () => {
    expect(
      formatPeakRateWindow({ peak_rate_enabled: true, peak_start: '14:00', peak_end: '18:00' })
    ).toBe('14:00-18:00 ×1 周一至周五')
  })

  it('appends the off-peak segment only when it differs from 1', () => {
    expect(
      formatPeakRateWindow({
        peak_rate_enabled: true,
        peak_start: '14:00',
        peak_end: '18:00',
        peak_rate_multiplier: 2,
        off_peak_rate_multiplier: 1,
      })
    ).toBe('14:00-18:00 ×2 周一至周五')
    expect(
      formatPeakRateWindow({
        peak_rate_enabled: true,
        peak_start: '14:00',
        peak_end: '18:00',
        peak_rate_multiplier: 2,
        off_peak_rate_multiplier: 0.5,
      })
    ).toBe('14:00-18:00 ×2 非高峰 ×0.5 周一至周五')
  })

  it('appends a per-model rule summary when model multipliers are configured', () => {
    expect(
      formatPeakRateWindow({
        peak_rate_enabled: true,
        peak_start: '14:00',
        peak_end: '18:00',
        peak_rate_multiplier: 2,
        peak_model_multipliers: { 'claude-opus-*': { peak: 3, off_peak: 1 }, 'claude-sonnet-*': { peak: 1.5, off_peak: 0.5 } },
      })
    ).toBe('14:00-18:00 ×2 2 条分模型倍率 周一至周五')
    // 空 map 等价于未配置
    expect(
      formatPeakRateWindow({
        peak_rate_enabled: true,
        peak_start: '14:00',
        peak_end: '18:00',
        peak_rate_multiplier: 2,
        peak_model_multipliers: {},
      })
    ).toBe('14:00-18:00 ×2 周一至周五')
  })
})

describe('hasPeakRate', () => {
  it('requires the enabled flag and a non-empty window', () => {
    expect(hasPeakRate(null)).toBe(false)
    expect(hasPeakRate({ peak_rate_enabled: true, peak_start: '', peak_end: '18:00' })).toBe(false)
    expect(hasPeakRate({ peak_rate_enabled: true, peak_start: '14:00', peak_end: '18:00' })).toBe(true)
  })
})

describe('hasOffPeakRate', () => {
  it('treats 1 and missing as no extra off-peak factor', () => {
    expect(hasOffPeakRate(null)).toBe(false)
    expect(hasOffPeakRate({ off_peak_rate_multiplier: 1 })).toBe(false)
    expect(hasOffPeakRate({ off_peak_rate_multiplier: 0.5 })).toBe(true)
  })
})

describe('hasPeakModelMultipliers', () => {
  it('requires a non-empty model multiplier map', () => {
    expect(hasPeakModelMultipliers(null)).toBe(false)
    expect(hasPeakModelMultipliers({ peak_model_multipliers: {} })).toBe(false)
    expect(hasPeakModelMultipliers({ peak_model_multipliers: { 'claude-*': { peak: 2, off_peak: 1 } } })).toBe(true)
  })
})
