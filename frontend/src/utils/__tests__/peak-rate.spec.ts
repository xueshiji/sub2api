import { describe, expect, it, vi } from 'vitest'

import { formatPeakRateWindow, hasPeakRate } from '../peak-rate'

vi.mock('@/i18n', () => ({
  i18n: {
    global: {
      t: (key: string) => (key === 'common.peakRateWeekdaysOnly' ? '周一至周五' : key),
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
})

describe('hasPeakRate', () => {
  it('requires the enabled flag and a non-empty window', () => {
    expect(hasPeakRate(null)).toBe(false)
    expect(hasPeakRate({ peak_rate_enabled: true, peak_start: '', peak_end: '18:00' })).toBe(false)
    expect(hasPeakRate({ peak_rate_enabled: true, peak_start: '14:00', peak_end: '18:00' })).toBe(true)
  })
})
