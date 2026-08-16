import { describe, it, expect } from 'vitest'
import { buildPeakWeightedReportHtml, peakWeightedReportFilename } from '../peakWeightedReport'

const items = [
  {
    user_id: 1, user_label: 'zhangsan', weighted_tokens: 458590000, total_tokens: 416170000,
    cache_read_tokens: 160000000, input_tokens: 140000000, output_tokens: 116170000, requests: 2831,
    peak_input_tokens: 14000000, peak_cache_read_tokens: 16000000, peak_output_tokens: 11617000,
    weighted_input_tokens: 154000000, weighted_cache_read_tokens: 176000000, weighted_output_tokens: 127787000,
  },
  {
    user_id: 2, user_label: '李四 <x@q.com>', weighted_tokens: 500000000, total_tokens: 77340000,
    cache_read_tokens: 30000000, input_tokens: 25340000, output_tokens: 22000000, requests: 569,
    peak_input_tokens: 10000000, peak_cache_read_tokens: 2000000, peak_output_tokens: 4000000,
    weighted_input_tokens: 30340000, weighted_cache_read_tokens: 34000000, weighted_output_tokens: 30000000,
  },
]

describe('buildPeakWeightedReportHtml', () => {
  it('内嵌可解析的数据载荷且包含主要区块', () => {
    const html = buildPeakWeightedReportHtml(items, { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' })
    const m = html.match(/var REPORT = (\{[\s\S]*?\});\n/)
    expect(m).not.toBeNull()
    const payload = JSON.parse(m![1])
    expect(payload.rows).toHaveLength(2)
    // token 字段按原始精度内嵌（M 化与积分计算由报告内脚本完成）
    expect(payload.rows[0]).toEqual({
      label: 'zhangsan', weighted: 458590000, original: 416170000,
      cache_read: 160000000, input_tokens: 140000000, output_tokens: 116170000,
      peak_input: 14000000, peak_cache_read: 16000000, peak_output: 11617000,
      weighted_input: 154000000, weighted_cache_read: 176000000, weighted_output: 127787000,
      request_count: 2831,
    })
    expect(payload.range).toEqual({ start: '2026-08-01', end: '2026-08-07' })
    expect(payload.timezone).toBe('Asia/Shanghai')
    for (const id of ['kpiGrid', 'insightGrid', 'podium', 'chartTop15', 'chartCompare', 'chartPie', 'chartCtx', 'tableBody']) {
      expect(html).toContain(`id="${id}"`)
    }
  })

  it('明细表包含高峰/非高峰拆分列与周额度列，且积分口径常量注入', () => {
    const html = buildPeakWeightedReportHtml(items, { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' })
    for (const key of ['offpeak_input', 'offpeak_cache_read', 'offpeak_output', 'peak_input', 'peak_cache_read', 'peak_output', 'quota_pct']) {
      expect(html).toContain(`data-key="${key}"`)
    }
    // 明细默认按周额度消耗降序，加权列不再持有默认排序标记
    expect(html).toContain('<th rowspan="2" data-key="quota_pct" class="sorted">周额度 <span class="sort-icon">▼</span></th>')
    expect(html).toContain('<th rowspan="2" data-key="weighted">加权 Tokens <span class="sort-icon"></span></th>')
    // 高峰/非高峰按分组表头收纳，无 # 序号列
    expect(html).toContain('<th colspan="3" class="group group-offpeak">非高峰</th>')
    expect(html).toContain('<th colspan="3" class="group group-peak">高峰</th>')
    expect(html).not.toContain('<th style="width:60px">#</th>')
    expect(html).toContain("var sortKey = 'quota_pct'")
    // Top 10 筛选按周额度消耗积分取前 10 名
    expect(html).toContain('creditsSorted.slice(0, 10)')
    // rawData 映射漏字段会让内嵌脚本渲染表格时抛 TypeError，此处锁定每个字段都被消费
    expect(html).toContain('offpeak_input: (r.input_tokens - r.peak_input) / 1e6')
    expect(html).toContain('offpeak_cache_read: (r.cache_read - r.peak_cache_read) / 1e6')
    expect(html).toContain('offpeak_output: (r.output_tokens - r.peak_output) / 1e6')
    expect(html).toContain('credits: credits')
    expect(html).toContain('quota_pct: WEEKLY_QUOTA > 0 ? (credits / WEEKLY_QUOTA) * 100 : 0')
    // 单价与周额度常量
    expect(html).toContain('var PRICE = { input: 345, cacheRead: 85, output: 1200 }')
    expect(html).toContain('var WEEKLY_QUOTA = 155000')
    // KPI 总计
    expect(html).toContain('非高峰 Token 总计')
    // 领奖台与饼图区块按周额度消耗口径命名
    expect(html).toContain('周额度消耗排行 · TOP 3')
    expect(html).toContain('周额度消耗占比分布')
    expect(html).toContain('高峰 Token 总计')
    expect(html).toContain('周额度消耗总计')
  })

  it('在 DOM 中执行内嵌脚本后渲染明细行与周额度计算结果', () => {
    const html = buildPeakWeightedReportHtml(items, { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' })
    document.body.innerHTML = html.match(/<body>\n([\s\S]*?)\n<script>/)![1]
    const script = html.match(/<script>\n([\s\S]*?)<\/script>/)![1]
    // Chart.js 走 CDN，测试环境不加载，脚本内有 typeof Chart 守卫
    eval(script)

    const rows = document.querySelectorAll('#tableBody tr')
    expect(rows).toHaveLength(2)
    // 李四加权 Token 更高（500M > 458.59M），首行仍是 zhangsan 才能证明默认排序是周额度消耗：
    // zhangsan：154×345 + 176×85 + 127.787×1200 = 221434.4 积分，÷155000 = 142.9%
    expect(rows[0].innerHTML).toContain('zhangsan')
    expect(rows[0].innerHTML).toContain('142.9%')
    // 用量占比按消耗积分口径：221434.4 / (221434.4 + 49357.3) = 81.77%
    expect(rows[0].innerHTML).toContain('81.77%')
    // KPI 总计：221434.4 + 49357.3 = 270791.7 积分，折合 1.75 个周额度
    expect(document.getElementById('kpiGrid')!.innerHTML).toContain('周额度消耗总计')
    // 领奖台按周额度消耗排名：zhangsan 以 221,434 积分居首，占比 = 221434.4 / 270791.7 = 81.8%
    const podium = document.getElementById('podium')!.innerHTML
    expect(podium).toContain('221,434')
    expect(podium).toContain('81.8%')
    expect(document.getElementById('kpiGrid')!.innerHTML).toContain('1.75')
    // 洞察卡按周额度与高峰占比口径生成（2 位用户走合计分支）
    const insight = document.getElementById('insightGrid')!.innerHTML
    expect(insight).toContain('合计消耗 <strong>270,792</strong> 积分')
    expect(insight).toContain('消耗周额度的 <strong>142.9%</strong>')
    expect(insight).toContain('高峰时段占比与放大')
  })

  it('用户标签中的 HTML 字符被转义', () => {
    const html = buildPeakWeightedReportHtml(items, { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' })
    expect(html).not.toContain('<x@q.com>')
    expect(html).toContain('&lt;x@q.com&gt;')
  })

  it('传入分组名时标题带分组后缀，分组名转义；未传时标题不含后缀', () => {
    const opts = { startDate: '2026-08-01', endDate: '2026-08-07', timezone: 'Asia/Shanghai' }
    const withGroup = buildPeakWeightedReportHtml(items, { ...opts, groupName: '旗舰<组>' })
    expect(withGroup).toContain('<title>Token 使用统计报告 · 旗舰&lt;组&gt;</title>')
    expect(withGroup).toContain('<h1>Token 使用统计报告 · 旗舰&lt;组&gt;</h1>')
    const withoutGroup = buildPeakWeightedReportHtml(items, opts)
    expect(withoutGroup).toContain('<title>Token 使用统计报告</title>')
    expect(withoutGroup).toContain('<h1>Token 使用统计报告</h1>')
  })

  it('带小时的时间范围原样透传且展示时去掉分隔符 T', () => {
    const html = buildPeakWeightedReportHtml(items, { startDate: '2026-08-21T10:00', endDate: '2026-08-27T23:00', timezone: 'Asia/Shanghai' })
    const m = html.match(/var REPORT = (\{[\s\S]*?\});\n/)
    expect(m).not.toBeNull()
    expect(JSON.parse(m![1]).range).toEqual({ start: '2026-08-21T10:00', end: '2026-08-27T23:00' })
    expect(html).toContain("String(REPORT.range.start).replace('T', ' ') + ' — ' + String(REPORT.range.end).replace('T', ' ')")
  })
})

describe('peakWeightedReportFilename', () => {
  it('文件名包含起止日期', () => {
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07')).toBe('token加权统计_2026-08-01_to_2026-08-07.html')
  })

  it('带小时的文件名不含冒号和分隔符 T', () => {
    expect(peakWeightedReportFilename('2026-08-21T10:00', '2026-08-27T23:00')).toBe('token加权统计_2026-08-21_1000_to_2026-08-27_2300.html')
  })

  it('传入分组名时文件名包含分组名，非法字符替换为下划线', () => {
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07', '旗舰组')).toBe('token加权统计_旗舰组_2026-08-01_to_2026-08-07.html')
    expect(peakWeightedReportFilename('2026-08-01', '2026-08-07', 'a/b:c')).toBe('token加权统计_a_b_c_2026-08-01_to_2026-08-07.html')
  })
})
