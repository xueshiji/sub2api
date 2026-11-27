import type { GroupAutoRateMode } from '@/types'

export const GROUP_AUTO_RATE_MAX_MULTIPLIER = 100

export const isAutoRateGroupSupported = (
  platform: string,
  subscriptionType: string
): boolean => platform === 'openai' && (subscriptionType === '' || subscriptionType === 'standard')

export const autoRatePercentToDecimal = (
  value: number | string | null | undefined
): number => {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0
  }
  return Math.round(parsed * 100) / 10000
}

export const autoRateDecimalToPercent = (
  value: number | null | undefined
): number => {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0
  }
  return Math.round(parsed * 1e6) / 1e4
}

export const normalizeAutoRateFixedValue = (
  value: number | string | null | undefined
): number => {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return 0
  }
  return Math.round(parsed * 10000) / 10000
}

export const computeGroupAutoRate = (
  maxAccountRate: number | null | undefined,
  mode: GroupAutoRateMode | string,
  value: number
): number | null => {
  if (
    maxAccountRate === null ||
    maxAccountRate === undefined ||
    !Number.isFinite(maxAccountRate) ||
    maxAccountRate < 0 ||
    maxAccountRate > GROUP_AUTO_RATE_MAX_MULTIPLIER
  ) {
    return null
  }
  if (!Number.isFinite(value) || value < 0 || value > GROUP_AUTO_RATE_MAX_MULTIPLIER) {
    return null
  }

  const maxScaled = Math.round(maxAccountRate * 10000)
  const valScaled = Math.round(value * 10000)

  let cents = 0
  if (mode === 'fixed') {
    const totalScaled = maxScaled + valScaled
    if (totalScaled <= 0) {
      return null
    }
    cents = Math.floor((totalScaled + 99) / 100)
  } else if (mode === 'percentage') {
    const product = maxScaled * (10000 + valScaled)
    if (product <= 0) {
      return null
    }
    cents = Math.floor((product + 999999) / 1000000)
  } else {
    return null
  }

  const result = cents / 100
  if (result <= 0 || result > GROUP_AUTO_RATE_MAX_MULTIPLIER) {
    return null
  }
  return result
}

export const formatAutoRateRuleBadge = (
  mode: GroupAutoRateMode | string | undefined,
  value: number | undefined
): string => {
  const parsed = Number(value ?? 0)
  if (!Number.isFinite(parsed) || parsed < 0) {
    return '+0%'
  }
  if (mode === 'fixed') {
    const normalized = normalizeAutoRateFixedValue(parsed)
    return `+${Number(normalized.toFixed(4))}`
  }
  const percent = autoRateDecimalToPercent(parsed)
  return `+${Number(percent.toFixed(2))}%`
}

export const validateAutoRateFormValue = (
  enabled: boolean,
  platform: string,
  subscriptionType: string,
  mode: GroupAutoRateMode | string,
  rawInputValue: number | string | null | undefined
): string | null => {
  if (!enabled || !isAutoRateGroupSupported(platform, subscriptionType)) {
    return null
  }
  if (mode !== 'percentage' && mode !== 'fixed') {
    return 'invalidMode'
  }
  const parsed = Number(rawInputValue ?? 0)
  if (!Number.isFinite(parsed) || parsed < 0) {
    return 'valueRangeError'
  }
  const backendValue =
    mode === 'percentage'
      ? autoRatePercentToDecimal(parsed)
      : normalizeAutoRateFixedValue(parsed)
  if (backendValue < 0 || backendValue > GROUP_AUTO_RATE_MAX_MULTIPLIER) {
    return 'valueRangeError'
  }
  return null
}
