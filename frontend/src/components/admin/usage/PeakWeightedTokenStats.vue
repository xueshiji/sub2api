<template>
  <!-- 用量页"积分统计"tab 内容：报告为自包含 HTML，在新浏览器标签页打开（srcdoc/blob 等
       内嵌方式会继承管理页的 nonce CSP 被拦截）；筛选/时间范围复用页面级筛选栏 -->
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

    <div v-else class="flex flex-col items-center justify-center gap-5 px-6 py-16">
      <p class="max-w-xl text-center text-sm text-gray-400 dark:text-gray-500">
        {{ t('admin.usage.peakStats.openReportHint') }}
      </p>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="opening"
        @click="openReport"
      >
        {{ opening ? t('admin.usage.peakStats.openingReport') : t('admin.usage.peakStats.openReport') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import {
  createPeakReportView,
  getPeakWeightedTokenStats,
  type PeakWeightedModelDetail,
  type PeakWeightedTokenItem
} from '@/api/admin/dashboard'
import { buildApiUrl } from '@/api/client'
import { buildPeakWeightedReportHtml, peakWeightedReportFilename } from '@/utils/peakWeightedReport'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { useAppStore } from '@/stores/app'

const props = defineProps<{
  startDate: string
  endDate: string
  filters: Record<string, unknown>
  model?: string
  groupName?: string
}>()

const { t } = useI18n()
const appStore = useAppStore()

const items = ref<PeakWeightedTokenItem[]>([])
const modelDetails = ref<PeakWeightedModelDetail[]>([])
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
    modelDetails.value = res.model_details || []
    timezoneName.value = res.timezone || ''
  } catch {
    if (seq !== reqSeq) return
    items.value = []
    modelDetails.value = []
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

const reportHtml = computed(() => buildPeakWeightedReportHtml(items.value, modelDetails.value, {
  startDate: props.startDate,
  endDate: props.endDate,
  timezone: timezoneName.value,
  groupName: props.groupName,
}))

const exportHtml = () => {
  saveAs(
    new Blob([reportHtml.value], { type: 'text/html;charset=utf-8' }),
    peakWeightedReportFilename(props.startDate, props.endDate, props.groupName)
  )
}

const opening = ref(false)
const openReport = async () => {
  // window.open 必须在用户手势的同步调用栈内执行，先开占位窗口，拿到 view id 后再重定向
  const win = window.open('', '_blank')
  if (!win) {
    appStore.showError(t('admin.usage.peakStats.popupBlocked'))
    return
  }
  opening.value = true
  try {
    const viewId = await createPeakReportView(reportHtml.value)
    win.location.href = buildApiUrl(`/admin/dashboard/peak-report-views/${viewId}`)
  } catch {
    win.close()
    appStore.showError(t('admin.usage.peakStats.failedToOpen'))
  } finally {
    opening.value = false
  }
}
</script>
