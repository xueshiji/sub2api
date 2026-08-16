import { describe, it, expect } from 'vitest'
import { buildPeakWeightedReportHtml, peakWeightedReportFilename } from '../peakWeightedReport'

const items = [
  {
    user_id: 1, user_label: 'zhangsan', weighted_tokens: 458590000, total_tokens: 416170000,
    cache_read_tokens: 160000000, input_tokens: 140000000, output_tokens: 116170000, requests: 2831,
    peak_input_tokens: 14000000, peak_cache_read_tokens: 16000000, peak_output_tokens: 11617000,
    weighted_input_tokens: 154000000, weighted_cache_read_tokens: 176000000, weighted_output_tokens: 127787000,
    discounted_weighted_input_tokens: 50000000, discounted_weighted_cache_read_tokens: 40000000, discounted_weighted_output_tokens: 30000000,
    discounted_offpeak_input_tokens: 10000000, discounted_offpeak_cache_read_tokens: 8000000, discounted_offpeak_output_tokens: 6000000,
  },
  {
    user_id: 2, user_label: '李四 <x@q.com>', weighted_tokens: 500000000, total_tokens: 77340000,
    cache_read_tokens: 30000000, input_tokens: 25340000, output_tokens: 22000000, requests: 569,
    peak_input_tokens: 10000000, peak_cache_read_tokens: 2000000, peak_output_tokens: 4000000,
    weighted_input_tokens: 30340000, weighted_cache_read_tokens: 34000000, weighted_output_tokens: 30000000,
    discounted_weighted_input_tokens: 0, discounted_weighted_cache_read_tokens: 0, discounted_weighted_output_tokens: 0,
    discounted_offpeak_input_tokens: 0, discounted_offpeak_cache_read_tokens: 0, discounted_offpeak_output_tokens: 0,
  },
]

const modelDetails = [
  { user_id: 1, model: 'glm-5.3-flash', in_peak: true, cache_read_tokens: 16000000, input_tokens: 14000000, output_tokens: 11617000 },
  { user_id: 1, model: 'glm-5.3-flash', in_peak: false, cache_read_tokens: 8000000, input_tokens: 10000000, output_tokens: 6000000 },
  { user_id: 1, model: 'claude-sonnet-5', in_peak: false, cache_read_tokens: 136000000, input_tokens: 116000000, output_tokens: 98553000 },
  { user_id: 2, model: 'glm-4.7', in_peak: true, cache_read_tokens: 2000000, input_tokens: 10000000, output_tokens: 4000000 },
  { user_id: 2, model: 'glm-4.7', in_peak: false, cache_read_tokens: 28000000, input_tokens: 15340000, output_tokens: 18000000 },
]

const opts = { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' }

describe('buildPeakWeightedReportHtml', () => {
  it('内嵌可解析的数据载荷且包含主要区块', () => {
    const html = buildPeakWeightedReportHtml(items, modelDetails, opts)
    const m = html.match(/var REPORT = (\{[\s\S]*?\});\n/)
    expect(m).not.toBeNull()
    const payload = JSON.parse(m![1])
    expect(payload.rows).toHaveLength(2)
    // token 字段按原始精度内嵌（M 化与积分计算由报告内脚本完成）
    expect(payload.rows[0]).toEqual({
      user_id: 1, label: 'zhangsan', original: 416170000,
      input_tokens: 140000000, cache_read: 160000000, output_tokens: 116170000,
      peak_input: 14000000, peak_cache_read: 16000000, peak_output: 11617000,
      weighted_input: 154000000, weighted_cache_read: 176000000, weighted_output: 127787000,
      disc_weighted_input: 50000000, disc_weighted_cache_read: 40000000, disc_weighted_output: 30000000,
      disc_offpeak_input: 10000000, disc_offpeak_cache_read: 8000000, disc_offpeak_output: 6000000,
      request_count: 2831,
    })
    expect(payload.model_details).toHaveLength(5)
    expect(payload.model_details[0]).toEqual({
      user_id: 1, model: 'glm-5.3-flash', in_peak: true,
      cache_read: 16000000, input: 14000000, output: 11617000,
    })
    expect(payload.range).toEqual({ start: '2026-08-01', end: '2026-08-07' })
    expect(payload.timezone).toBe('Asia/Shanghai')
    for (const id of ['kpiGrid', 'insightGrid', 'podium', 'chartTop15', 'chartCompare', 'chartPie', 'chartCtx', 'ctxKpiGrid', 'tableHead', 'tableBody']) {
      expect(html).toContain(`id="${id}"`)
    }
  })

  it('积分口径常量注入且头部标注折扣规则', () => {
    const html = buildPeakWeightedReportHtml(items, modelDetails, opts)
    expect(html).toContain('var PRICE = { input: 345, cacheRead: 85, output: 1200 }')
    expect(html).toContain('var WEEKLY_QUOTA = 155000')
    expect(html).toContain('积分折扣：上游 GLM-5.3-FLASH 积分 ×1/3')
    // 明细默认按周额度消耗降序
    expect(html).toContain("var sortKey = 'quota_pct'")
    // 图表与 KPI 按积分口径命名，不再出现加权 token 口径
    expect(html).toContain('Top 15 用户 · 消耗积分')
    expect(html).toContain('高峰 vs 非高峰 积分对比')
    expect(html).toContain('周额度消耗占比分布')
    expect(html).toContain('非高峰积分总计')
    expect(html).toContain('高峰积分总计')
    expect(html).not.toContain('加权 Tokens')
    expect(html).not.toContain('加权 Token')
  })

  it('在 DOM 中执行内嵌脚本后渲染积分明细与模型分组表头', () => {
    const html = buildPeakWeightedReportHtml(items, modelDetails, opts)
    document.body.innerHTML = html.match(/<body>\n([\s\S]*?)\n<script>/)![1]
    const script = html.match(/<script>\n([\s\S]*?)<\/script>/)![1]
    // Chart.js 走 CDN，测试环境不加载，脚本内有 typeof Chart 守卫
    eval(script)

    // 三行复合表头：模型列按总量降序展开（claude-sonnet-5 350.6M > glm-4.7 77.3M > glm-5.3-flash 65.6M）
    const head = document.getElementById('tableHead')!.innerHTML
    expect(head).toContain('<th colspan="6" class="group group-model">claude-sonnet-5</th>')
    expect(head).toContain('<th colspan="3" class="group group-offpeak">非高峰</th>')
    expect(head).toContain('<th colspan="3" class="group group-peak">高峰</th>')
    expect(head).toContain('<th data-key="mk_2_peak_cache_read" class="sub sub-peak">缓存 <span class="sort-icon"></span></th>')
    expect(head).toContain('<th rowspan="3" data-key="quota_pct" class="sorted">周额度 <span class="sort-icon">▼</span></th>')

    const rows = document.querySelectorAll('#tableBody tr')
    expect(rows).toHaveLength(2)
    // 李四加权 Token 更高（500M > 458.59M），首行仍是 zhangsan 才能证明默认排序是周额度消耗：
    // zhangsan 折后积分 = 221434.4 - 56650×2/3 = 183667.73，÷155000 = 118.5%
    expect(rows[0].innerHTML).toContain('zhangsan')
    expect(rows[0].innerHTML).toContain('118.5%')
    // 用量占比按折后积分口径：183667.73 / (183667.73 + 49357.3) = 78.82%
    expect(rows[0].innerHTML).toContain('78.82%')
    // 李四无 claude-sonnet-5 用量，该模型列显示占位符
    expect(rows[1].innerHTML).toContain('—')
    // chip 文案按积分阈值生成
    expect(document.getElementById('chipHeavy')!.textContent).toBe('重度用户 (≥25% 周额度)')

    // KPI：总积分 233025（折合 1.50 个周额度）；非高峰 = 非折扣 169843.6 + 折扣 11330×1/3 + 李四 29272.3 = 202892.57，高峰 = 233025.03 − 202892.57
    const kpiHtml = document.getElementById('kpiGrid')!.innerHTML
    expect(kpiHtml).toContain('周额度消耗总计')
    expect(kpiHtml).toContain('233,025')
    expect(kpiHtml).toContain('1.50')
    expect(kpiHtml).toContain('30,132')
    expect(kpiHtml).toContain('202,893')
    // 领奖台按折后积分排名：zhangsan 183,668 积分居首，占比 78.8%
    const podium = document.getElementById('podium')!.innerHTML
    expect(podium).toContain('183,668')
    expect(podium).toContain('78.8%')
    // 洞察卡含高峰积分占比口径：30132.47 / 233025.03 = 12.9%
    const insight = document.getElementById('insightGrid')!.innerHTML
    expect(insight).toContain('高峰积分占比')
    expect(insight).toContain('12.9%')
  })

  it('用户标签中的 HTML 字符被转义', () => {
    const html = buildPeakWeightedReportHtml(items, modelDetails, opts)
    expect(html).not.toContain('<x@q.com>')
    expect(html).toContain('&lt;x@q.com&gt;')
  })

  it('传入分组名时标题带分组后缀，分组名转义；未传时标题不含后缀', () => {
    const withGroup = buildPeakWeightedReportHtml(items, modelDetails, { ...opts, groupName: '旗舰<组>' })
    expect(withGroup).toContain('<title>积分使用统计报告 · 旗舰&lt;组&gt;</title>')
    expect(withGroup).toContain('<h1>积分使用统计报告 · 旗舰&lt;组&gt;</h1>')
    const withoutGroup = buildPeakWeightedReportHtml(items, modelDetails, opts)
    expect(withoutGroup).toContain('<title>积分使用统计报告</title>')
    expect(withoutGroup).toContain('<h1>积分使用统计报告</h1>')
  })

  it('带小时的时间范围原样透传且展示时去掉分隔符 T', () => {
    const html = buildPeakWeightedReportHtml(items, modelDetails, { startDate: '2026-08-21T10:00', endDate: '2026-08-27T23:00', timezone: 'Asia/Shanghai' })
    const m = html.match(/var REPORT = (\{[\s\S]*?\});\n/)
    expect(m).not.toBeNull()
    expect(JSON.parse(m![1]).range).toEqual({ start: '2026-08-21T10:00', end: '2026-08-27T23:00' })
    expect(html).toContain("String(REPORT.range.start).replace('T', ' ') + ' — ' + String(REPORT.range.end).replace('T', ' ')")
  })
})

describe('peakWeightedReportFilename', () => {
  it('文件名包含起止日期', () => {
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07')).toBe('积分使用统计报告_2026-08-01_to_2026-08-07.html')
  })

  it('带小时的文件名不含冒号和分隔符 T', () => {
    expect(peakWeightedReportFilename('2026-08-21T10:00', '2026-08-27T23:00')).toBe('积分使用统计报告_2026-08-21_1000_to_2026-08-27_2300.html')
  })

  it('传入分组名时文件名包含分组名，非法字符替换为下划线', () => {
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07', '旗舰组')).toBe('积分使用统计报告_旗舰组_2026-08-01_to_2026-08-07.html')
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07', 'a/b:c')).toBe('积分使用统计报告_a_b_c_2026-08-01_to_2026-08-07.html')
  })
})
