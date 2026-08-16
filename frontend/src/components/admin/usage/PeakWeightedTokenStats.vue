<template>
  <!-- 用量页"加权统计"tab 内容：无卡片外观，依赖父级统一卡片；筛选/时间范围复用页面级筛选栏 -->
  <div>
    <!-- Toolbar -->
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3 dark:border-dark-700/50 sm:px-6">
      <p class="text-xs text-gray-400 dark:text-gray-500">{{ t('admin.usage.peakStats.subtitle') }}</p>
      <div class="flex items-center gap-3">
        <span v-if="!loading && items.length > 0" class="text-xs text-gray-400 dark:text-gray-500">
          {{ t('admin.usage.peakStats.userCount', { count: items.length }) }}
        </span>
        <button type="button" class="btn btn-secondary" :disabled="loading || items.length === 0" @click="exportHtml">
          {{ t('admin.usage.peakStats.exportHtml') }}
        </button>
      </div>
    </div>

    <!-- Loading / Empty -->
    <div v-if="loading" class="py-16 text-center">
      <LoadingSpinner />
    </div>
    <div v-else-if="items.length === 0" class="py-16 text-center text-sm text-gray-400 dark:text-gray-500">
      {{ t('admin.usage.peakStats.empty') }}
    </div>

    <template v-else>
      <!-- KPI Cards -->
      <div class="grid grid-cols-2 gap-4 px-4 py-6 sm:px-6 lg:grid-cols-4">
        <div
          v-for="kpi in kpis"
          :key="kpi.label"
          class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800"
        >
          <p class="text-xs font-medium uppercase tracking-wide text-gray-400 dark:text-gray-500">{{ kpi.label }}</p>
          <p class="mt-1 text-2xl font-bold tabular-nums text-gray-900 dark:text-white">
            {{ kpi.value }}<span class="ml-1 text-sm font-medium text-gray-400">{{ kpi.unit }}</span>
          </p>
          <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">{{ kpi.hint }}</p>
        </div>
      </div>

      <!-- Top 3 Podium -->
      <div class="grid grid-cols-1 gap-4 px-4 pb-6 sm:px-6 md:grid-cols-3">
        <div
          v-for="(d, i) in sorted.slice(0, 3)"
          :key="d.user_id"
          class="rounded-xl border p-4 text-center"
          :class="podiumClasses[i].border"
        >
          <p class="text-xs font-semibold tracking-wide text-gray-400 dark:text-gray-500">TOP {{ i + 1 }}</p>
          <p class="mt-2 truncate text-base font-bold text-gray-900 dark:text-white" :title="d.user_label">{{ d.user_label }}</p>
          <p class="mt-1 text-xl font-bold tabular-nums" :class="podiumClasses[i].text">{{ fmtM(d.weighted_tokens) }}</p>
          <p class="mt-2 border-t border-gray-100 pt-2 text-xs text-gray-400 dark:border-dark-700 dark:text-gray-500">
            {{ t('admin.usage.peakStats.table.requests') }} {{ d.requests.toLocaleString() }} · {{ t('admin.usage.peakStats.table.share') }}
            {{ shareOf(d.weighted_tokens) }}%
          </p>
        </div>
      </div>

      <!-- Charts -->
      <div class="grid grid-cols-1 gap-4 px-4 pb-6 sm:px-6 lg:grid-cols-2">
        <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700 lg:col-span-2">
          <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.usage.peakStats.charts.top15Title') }}</p>
          <p class="mb-3 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.usage.peakStats.charts.top15Subtitle') }}</p>
          <div class="h-[420px]"><Bar :data="top15Data" :options="barOptions" /></div>
        </div>
        <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.usage.peakStats.charts.compareTitle') }}</p>
          <p class="mb-3 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.usage.peakStats.charts.compareSubtitle') }}</p>
          <div class="h-[340px]"><Bar :data="compareData" :options="groupedBarOptions" /></div>
        </div>
        <div class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
          <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.usage.peakStats.charts.pieTitle') }}</p>
          <p class="mb-3 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.usage.peakStats.charts.pieSubtitle') }}</p>
          <div class="h-[340px]"><Doughnut :data="pieData" :options="pieOptions" /></div>
        </div>
      </div>

      <!-- Table -->
      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-4 py-3 dark:border-dark-700/50 sm:px-6">
        <input
          v-model="searchTerm"
          type="text"
          class="input max-w-xs"
          :placeholder="t('admin.usage.peakStats.table.searchPlaceholder')"
        />
        <div class="flex gap-2">
          <button
            v-for="chip in chips"
            :key="chip.key"
            type="button"
            class="rounded-full border px-3 py-1.5 text-xs font-medium transition-colors"
            :class="activeFilter === chip.key
              ? 'border-primary-500 bg-primary-500 text-white'
              : 'border-gray-200 text-gray-500 hover:border-primary-400 hover:text-primary-500 dark:border-dark-600 dark:text-gray-400'"
            @click="activeFilter = chip.key"
          >
            {{ chip.label }}
          </button>
        </div>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full min-w-max divide-y divide-gray-200 dark:divide-dark-700">
          <thead class="bg-gray-50 dark:bg-dark-800">
            <tr>
              <th class="w-16 px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400 sm:px-6">#</th>
              <th class="cursor-pointer select-none px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('user_label')">
                {{ t('admin.usage.peakStats.table.user') }}
                <span v-if="sortKey === 'user_label'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('weighted_tokens')">
                {{ t('admin.usage.peakStats.table.weighted') }}
                <span v-if="sortKey === 'weighted_tokens'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('total_tokens')">
                {{ t('admin.usage.peakStats.table.original') }}
                <span v-if="sortKey === 'total_tokens'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('cache_read_tokens')">
                {{ t('admin.usage.peakStats.table.cacheRead') }}
                <span v-if="sortKey === 'cache_read_tokens'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('input_tokens')">
                {{ t('admin.usage.peakStats.table.inputTokens') }}
                <span v-if="sortKey === 'input_tokens'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('output_tokens')">
                {{ t('admin.usage.peakStats.table.outputTokens') }}
                <span v-if="sortKey === 'output_tokens'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('requests')">
                {{ t('admin.usage.peakStats.table.requests') }}
                <span v-if="sortKey === 'requests'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400" @click="setSort('ctxLen')">
                {{ t('admin.usage.peakStats.table.ctxLen') }}
                <span v-if="sortKey === 'ctxLen'" aria-hidden="true">{{ sortDir === 'asc' ? '↑' : '↓' }}</span>
              </th>
              <th class="w-40 px-4 py-3 text-right text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400 sm:px-6">
                {{ t('admin.usage.peakStats.table.share') }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900">
            <tr v-for="d in tableRows" :key="d.user_id" class="transition-colors hover:bg-gray-50 dark:hover:bg-dark-700/40">
              <td class="px-4 py-3 sm:px-6">
                <span
                  v-if="d.rank <= 3"
                  class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold"
                  :class="RANK_BADGE_CLASSES[d.rank - 1]"
                >{{ d.rank }}</span>
                <span v-else class="inline-block w-6 text-center text-sm tabular-nums text-gray-400">{{ d.rank }}</span>
              </td>
              <td class="max-w-[260px] truncate px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-200" :title="d.user_label">
                {{ d.user_label }}
              </td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm font-semibold tabular-nums text-primary-600 dark:text-primary-400">{{ fmtM(d.weighted_tokens) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400">{{ fmtM(d.total_tokens) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400">{{ fmtM(d.cache_read_tokens) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400">{{ fmtM(d.input_tokens) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400">{{ fmtM(d.output_tokens) }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400">{{ d.requests.toLocaleString() }}</td>
              <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400" :title="Math.round(d.ctxLen).toLocaleString() + ' tokens'">{{ fmtCtx(d.ctxLen) }}</td>
              <td class="px-4 py-3 sm:px-6">
                <div class="flex justify-end text-xs tabular-nums text-gray-400 dark:text-gray-500">{{ shareOf(d.weighted_tokens) }}%</div>
                <div class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                  <div
                    class="h-full rounded-full bg-gradient-to-r from-primary-500 to-violet-500"
                    :style="{ width: barWidthOf(d.weighted_tokens) + '%' }"
                  />
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  BarElement,
  ArcElement,
  Tooltip,
  Legend
} from 'chart.js'
import { Bar, Doughnut } from 'vue-chartjs'
import { getPeakWeightedTokenStats, type PeakWeightedTokenItem } from '@/api/admin/dashboard'
import { buildPeakWeightedReportHtml, peakWeightedReportFilename } from '@/utils/peakWeightedReport'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { useAppStore } from '@/stores/app'
import { formatCompactNumber } from '@/utils/format'

ChartJS.register(CategoryScale, LinearScale, BarElement, ArcElement, Tooltip, Legend)

const props = defineProps<{
  startDate: string
  endDate: string
  filters: Record<string, unknown>
  model?: string
  groupName?: string
}>()

const { t } = useI18n()
const appStore = useAppStore()

// 与导出报告一致的口径：加权 token ≥100M 记为重度用户
const HEAVY_THRESHOLD_M = 100

const items = ref<PeakWeightedTokenItem[]>([])
const timezoneName = ref('')
const loading = ref(false)
let reqSeq = 0

const load = async () => {
  const seq = ++reqSeq
  loading.value = true
  try {
    const params = {
      ...props.filters,
      start_date: props.startDate,
      end_date: props.endDate,
    } as Parameters<typeof getPeakWeightedTokenStats>[0]
    if (props.model) params.model = props.model
    const res = await getPeakWeightedTokenStats(params)
    if (seq !== reqSeq) return
    items.value = res.users || []
    timezoneName.value = res.timezone || ''
  } catch {
    if (seq !== reqSeq) return
    items.value = []
    appStore.showError(t('admin.usage.peakStats.failedToLoad'))
  } finally {
    if (seq === reqSeq) loading.value = false
  }
}

watch(
  () => [props.startDate, props.endDate, props.model, JSON.stringify(props.filters)],
  () => load(),
  { immediate: true }
)

defineExpose({ reload: load })

// ===== Derived stats =====
type Row = PeakWeightedTokenItem & { ctxLen: number; rank: number }

const rows = computed<Row[]>(() => {
  const rankByUser = new Map(sorted.value.map((d, i) => [d.user_id, i + 1]))
  return items.value.map((d) => ({
    ...d,
    ctxLen: d.requests > 0 ? (d.total_tokens / d.requests) : 0,
    rank: rankByUser.get(d.user_id) ?? 0,
  }))
})

const sorted = computed(() => [...items.value].sort((a, b) => b.weighted_tokens - a.weighted_tokens))

const totalWeighted = computed(() => items.value.reduce((s, d) => s + d.weighted_tokens, 0))
const totalOriginal = computed(() => items.value.reduce((s, d) => s + d.total_tokens, 0))
const totalRequests = computed(() => items.value.reduce((s, d) => s + d.requests, 0))
const heavyUsers = computed(() => items.value.filter((d) => d.weighted_tokens >= HEAVY_THRESHOLD_M * 1e6).length)
const peakRatio = computed(() => totalOriginal.value > 0 ? (totalWeighted.value / totalOriginal.value).toFixed(2) : '—')

// 值自带 K/M 后缀且封顶到 M（≥1e9 显示为 1200.0M），调用处不得再拼接单位
const fmtM = (tokens: number) => formatCompactNumber(tokens, { allowBillions: false })
const fmtCtx = (n: number) => {
  if (!isFinite(n) || n <= 0) return '0'
  return formatCompactNumber(Math.round(n))
}
const shareOf = (tokens: number) => totalWeighted.value > 0 ? ((tokens / totalWeighted.value) * 100).toFixed(2) : '0.00'
const barWidthOf = (tokens: number) => {
  const max = Math.max(...items.value.map((d) => d.weighted_tokens), 0)
  return max > 0 ? (tokens / max) * 100 : 0
}

const kpis = computed(() => [
  {
    label: t('admin.usage.peakStats.kpis.totalUsers'),
    value: String(items.value.length),
    unit: t('admin.usage.peakStats.kpis.userUnit'),
    hint: t('admin.usage.peakStats.kpis.totalUsersHint'),
  },
  {
    label: t('admin.usage.peakStats.kpis.weightedTokens'),
    value: fmtM(totalWeighted.value),
    unit: '',
    hint: t('admin.usage.peakStats.kpis.weightedHint', {
      original: fmtM(totalOriginal.value),
      ratio: peakRatio.value,
    }),
  },
  {
    label: t('admin.usage.peakStats.kpis.totalRequests'),
    value: totalRequests.value.toLocaleString(),
    unit: t('admin.usage.peakStats.kpis.requestUnit'),
    hint: t('admin.usage.peakStats.kpis.requestsHint', {
      avg: items.value.length > 0 ? Math.round(totalRequests.value / items.value.length).toLocaleString() : '0',
    }),
  },
  {
    label: t('admin.usage.peakStats.kpis.heavyUsers'),
    value: String(heavyUsers.value),
    unit: t('admin.usage.peakStats.kpis.userUnit'),
    hint: t('admin.usage.peakStats.kpis.heavyHint', { threshold: HEAVY_THRESHOLD_M }),
  },
])

const podiumClasses = [
  { border: 'border-amber-300/70 dark:border-amber-500/30', text: 'text-amber-600 dark:text-amber-400' },
  { border: 'border-gray-300/70 dark:border-gray-500/30', text: 'text-gray-500 dark:text-gray-300' },
  { border: 'border-orange-300/70 dark:border-orange-500/30', text: 'text-orange-600 dark:text-orange-400' },
]

const RANK_BADGE_CLASSES = [
  'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400',
  'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300',
  'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-400',
]

// ===== Charts =====
const isDark = () => document.documentElement.classList.contains('dark')
const chartTheme = computed(() => ({
  text: isDark() ? '#9ca3af' : '#6b7280',
  grid: isDark() ? '#37415166' : '#f1f5f9',
}))

const top15 = computed(() => sorted.value.slice(0, 15))
const top15Data = computed(() => ({
  labels: top15.value.map((d) => d.user_label),
  datasets: [
    {
      label: t('admin.usage.peakStats.charts.top15Title'),
      data: top15.value.map((d) => +(d.weighted_tokens / 1e6).toFixed(2)),
      backgroundColor: (ctx: { chart: { ctx: CanvasRenderingContext2D } }) => {
        const g = ctx.chart.ctx.createLinearGradient(0, 0, 600, 0)
        g.addColorStop(0, '#2563eb')
        g.addColorStop(1, '#8b5cf6')
        return g
      },
      borderRadius: 6,
      barThickness: 18,
    },
  ],
}))

const top10 = computed(() => sorted.value.slice(0, 10))
const compareData = computed(() => ({
  labels: top10.value.map((d) => d.user_label),
  datasets: [
    {
      label: t('admin.usage.peakStats.table.weighted'),
      data: top10.value.map((d) => +(d.weighted_tokens / 1e6).toFixed(2)),
      backgroundColor: '#2563eb',
      borderRadius: 5,
      barPercentage: 0.7,
    },
    {
      label: t('admin.usage.peakStats.table.original'),
      data: top10.value.map((d) => +(d.total_tokens / 1e6).toFixed(2)),
      backgroundColor: '#c7d2fe',
      borderRadius: 5,
      barPercentage: 0.7,
    },
  ],
}))

const PIE_COLORS = ['#2563eb', '#3b82f6', '#60a5fa', '#8b5cf6', '#a78bfa', '#f59e0b', '#10b981', '#ef4444', '#cbd5e1']
const pieData = computed(() => {
  const top8 = sorted.value.slice(0, 8)
  const others = sorted.value.slice(8).reduce((s, d) => s + d.weighted_tokens, 0)
  return {
    labels: [...top8.map((d) => d.user_label), t('admin.usage.peakStats.charts.others')],
    datasets: [
      {
        data: [...top8.map((d) => d.weighted_tokens), others],
        backgroundColor: PIE_COLORS,
        borderColor: isDark() ? '#111827' : '#ffffff',
        borderWidth: 3,
        hoverOffset: 8,
      },
    ],
  }
})

const baseScales = computed(() => ({
  x: { grid: { color: chartTheme.value.grid }, ticks: { color: chartTheme.value.text, font: { size: 11 } } },
  y: { grid: { display: false }, ticks: { color: chartTheme.value.text, font: { size: 11 } } },
}))

const barOptions = computed(() => ({
  indexAxis: 'y' as const,
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      backgroundColor: '#1e293b',
      padding: 12,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      callbacks: { label: (c: any) => ` ${Number(c.parsed.x).toFixed(2)} M` },
    },
  },
  scales: baseScales.value,
}))

const groupedBarOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'bottom' as const,
      labels: { usePointStyle: true, pointStyle: 'circle' as const, padding: 16, color: chartTheme.value.text, font: { size: 12 } },
    },
    tooltip: { backgroundColor: '#1e293b', padding: 12 },
  },
  scales: {
    x: { grid: { display: false }, ticks: { color: chartTheme.value.text, font: { size: 11 } } },
    y: { grid: { color: chartTheme.value.grid }, ticks: { color: chartTheme.value.text, font: { size: 11 } } },
  },
}))

const pieOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  cutout: '58%',
  plugins: {
    legend: {
      position: 'right' as const,
      labels: { usePointStyle: true, pointStyle: 'circle' as const, padding: 12, color: chartTheme.value.text, font: { size: 12 }, boxWidth: 8 },
    },
    tooltip: {
      backgroundColor: '#1e293b',
      padding: 12,
      callbacks: {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        label: (c: any) =>
          ` ${c.label}: ${(Number(c.parsed) / 1e6).toFixed(2)}M (${totalWeighted.value > 0 ? ((Number(c.parsed) / totalWeighted.value) * 100).toFixed(1) : '0.0'}%)`,
      },
    },
  },
}))

// ===== Table =====
type SortKey = 'user_label' | 'weighted_tokens' | 'total_tokens' | 'cache_read_tokens' | 'input_tokens' | 'output_tokens' | 'requests' | 'ctxLen'
const sortKey = ref<SortKey>('weighted_tokens')
const sortDir = ref<'asc' | 'desc'>('desc')
const activeFilter = ref<'all' | 'top10' | 'heavy'>('all')
const searchTerm = ref('')

const chips = computed(() => [
  { key: 'all' as const, label: t('admin.usage.peakStats.table.all') },
  { key: 'top10' as const, label: 'Top 10' },
  { key: 'heavy' as const, label: t('admin.usage.peakStats.table.heavy', { threshold: HEAVY_THRESHOLD_M }) },
])

const setSort = (key: SortKey) => {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortKey.value = key
    sortDir.value = 'desc'
  }
}

const tableRows = computed(() => {
  let list = rows.value
  if (searchTerm.value.trim()) {
    const q = searchTerm.value.trim().toLowerCase()
    list = list.filter((d) => d.user_label.toLowerCase().includes(q))
  }
  if (activeFilter.value === 'top10') {
    const topSet = new Set(sorted.value.slice(0, 10).map((d) => d.user_id))
    list = list.filter((d) => topSet.has(d.user_id))
  } else if (activeFilter.value === 'heavy') {
    list = list.filter((d) => d.weighted_tokens >= HEAVY_THRESHOLD_M * 1e6)
  }
  return [...list].sort((a, b) => {
    const va = a[sortKey.value]
    const vb = b[sortKey.value]
    if (typeof va === 'string' || typeof vb === 'string') {
      return sortDir.value === 'asc'
        ? String(va).localeCompare(String(vb))
        : String(vb).localeCompare(String(va))
    }
    return sortDir.value === 'asc' ? va - vb : vb - va
  })
})

// ===== Export =====
const exportHtml = () => {
  const html = buildPeakWeightedReportHtml(items.value, {
    startDate: props.startDate,
    endDate: props.endDate,
    timezone: timezoneName.value,
    groupName: props.groupName,
  })
  saveAs(
    new Blob([html], { type: 'text/html;charset=utf-8' }),
    peakWeightedReportFilename(props.startDate, props.endDate, props.groupName)
  )
}
</script>
