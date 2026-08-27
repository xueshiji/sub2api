<template>
  <div
    v-if="entries.length > 0"
    class="flex flex-wrap items-center gap-x-1 gap-y-0.5 text-[10px] text-amber-600 dark:text-amber-400"
  >
    <span>{{ t('common.peakRateModelRulesDetail') }}</span>
    <code
      v-for="[pattern, rule] in entries"
      :key="pattern"
      class="rounded bg-amber-50 px-1 py-0.5 font-mono dark:bg-amber-900/20"
    >
      {{ pattern }} ×{{ rule.peak }} / ×{{ rule.off_peak }}
    </code>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  rules?: Record<string, { peak: number; off_peak: number }> | null
}>()

const { t } = useI18n()

const entries = computed(() => Object.entries(props.rules ?? {}))
</script>
