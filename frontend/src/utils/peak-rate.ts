/**
 * 高峰时段倍率的共享展示逻辑。
 *
 * 高峰窗口由后端按服务器全局时区判定（Group.PeakMultiplierAt），
 * 前端展示必须带上服务器时区标注（来自公共设置 server_utc_offset），
 * 避免用户按浏览器本地时间误读计费窗口。
 * 高峰倍率仅周一至周五生效（周末按非高峰倍率计费），展示统一附带生效日标注。
 * 分模型倍率（peak_model_multipliers）只展示概要标记，完整清单在分组详情（模型广场）。
 */

import { i18n } from '@/i18n'

export interface PeakRateFields {
  peak_rate_enabled?: boolean
  peak_start?: string
  peak_end?: string
  peak_rate_multiplier?: number
  off_peak_rate_multiplier?: number
  peak_model_multipliers?: Record<string, { peak: number; off_peak: number }> | null
}

export function hasPeakRate(fields?: PeakRateFields | null): boolean {
  return Boolean(fields?.peak_rate_enabled && fields?.peak_start && fields?.peak_end)
}

/** "+08:00" → "UTC+08:00"；旧缓存无该字段时返回空串，调用方降级为不带时区标注 */
export function serverTimezoneLabel(utcOffset?: string | null): string {
  return utcOffset ? `UTC${utcOffset}` : ''
}

/** 配置了非 1 的非高峰倍率时返回 true（1 与缺省等价于"非高峰不叠加"的既有行为） */
export function hasOffPeakRate(fields?: PeakRateFields | null): boolean {
  return typeof fields?.off_peak_rate_multiplier === 'number' && fields.off_peak_rate_multiplier !== 1
}

export function hasPeakModelMultipliers(fields?: PeakRateFields | null): boolean {
  return Boolean(fields?.peak_model_multipliers && Object.keys(fields.peak_model_multipliers).length > 0)
}

/** "14:00-18:00 ×2 非高峰 ×0.5 ·3 条分模型规则 周一至周五 (UTC+08:00)"，各段按配置省略；tzLabel 为空时省略括号部分 */
export function formatPeakRateWindow(
  fields: PeakRateFields | null | undefined,
  tzLabel?: string
): string {
  if (!hasPeakRate(fields) || !fields) return ''
  const parts: string[] = [`${fields.peak_start}-${fields.peak_end} ×${fields.peak_rate_multiplier ?? 1}`]
  if (hasOffPeakRate(fields)) {
    parts.push(`${i18n.global.t('common.peakRateOffPeak')} ×${fields.off_peak_rate_multiplier}`)
  }
  if (hasPeakModelMultipliers(fields) && fields.peak_model_multipliers) {
    parts.push(i18n.global.t('common.peakRateModelRules', { count: Object.keys(fields.peak_model_multipliers).length }))
  }
  parts.push(i18n.global.t('common.peakRateWeekdaysOnly'))
  const base = parts.join(' ')
  return tzLabel ? `${base} (${tzLabel})` : base
}
