<template>
  <div>
    <!-- Loading state -->
    <div v-if="props.loading && !props.stats" class="space-y-0.5">
      <div class="h-3 w-14 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      <div class="h-3 w-16 animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
    </div>

    <!-- Error state -->
    <div v-else-if="props.error && !props.stats" class="text-xs text-red-500">
      {{ props.error }}
    </div>

    <!-- Stats data -->
    <div v-else-if="props.stats" class="space-y-0.5 text-xs">
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.perfScore') }}:</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          props.stats.score != null ? `${Math.round(props.stats.score * 100)}%` : '-'
        }}</span>
        <span
          v-if="props.stats.slow_penalty"
          class="rounded bg-orange-100 px-1 text-[10px] text-orange-700 dark:bg-orange-900/40 dark:text-orange-300"
          :title="slowPenaltyTip"
        >
          {{ t('admin.accounts.stats.slowPenalty') }}
        </span>
      </div>
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">TTFT:</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          props.stats.avg_ttft_ms != null ? formatTtft(props.stats.avg_ttft_ms) : '-'
        }}</span>
      </div>
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stats.decodeSpeed') }}:</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          props.stats.avg_decode_tps != null ? `${props.stats.avg_decode_tps.toFixed(1)} tok/s` : '-'
        }}</span>
      </div>
      <div class="text-[10px] text-gray-400 dark:text-gray-500">
        {{ t('admin.accounts.stats.samples') }}: {{ props.stats.sample_count }}
      </div>
    </div>

    <!-- No data -->
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountPerfStats } from '@/types'

const props = withDefaults(
  defineProps<{
    stats?: AccountPerfStats | null
    loading?: boolean
    error?: string | null
  }>(),
  {
    stats: null,
    loading: false,
    error: null
  }
)

const { t } = useI18n()

const slowPenaltyTip = computed(() => {
  if (!props.stats?.slow_penalty_until) {
    return t('admin.accounts.stats.slowPenaltyTip')
  }
  return t('admin.accounts.stats.slowPenaltyTipUntil', {
    time: new Date(props.stats.slow_penalty_until).toLocaleTimeString()
  })
})

const formatTtft = (ms: number): string => {
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)}s`
  }
  return `${Math.round(ms)}ms`
}
</script>
