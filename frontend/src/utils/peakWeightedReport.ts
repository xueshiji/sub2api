import type { PeakWeightedModelDetail, PeakWeightedTokenItem } from '@/api/admin/dashboard'

/**
 * 生成"积分使用统计报告"自包含 HTML 报告。
 * 视觉与交互对齐仓库根目录的《token统计智能电网.html》参考稿：
 * Chart.js 走 CDN（与参考稿一致），数据直接内嵌，表格/搜索/排序纯本地。
 */

// user_label / model 来自用户名/邮箱/上游模型名，会拼进 innerHTML，先做 HTML 转义
const escapeHtml = (s: string): string =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;')

// 积分口径：非高峰单价按每 M tokens 计，高峰时段按所属分组高峰倍率放大，
// 即消耗积分 = 加权输入×345 + 加权缓存命中×85 + 加权输出×1200（每 M）
const CREDIT_PRICES_PER_M = { input: 345, cacheRead: 85, output: 1200 } as const
const WEEKLY_CREDIT_QUOTA = 155000
// 上游命中折扣表的模型按统一乘数折减积分；乘数须全表统一，后端 discounted_* 拆分列
// 按单一乘数设计，变更模型集合或乘数时须同步后端 GetPeakWeightedTokenStats 的 SQL
const CREDIT_DISCOUNT_MODELS = ['glm-5.3-flash'] as const
const CREDIT_DISCOUNT_FACTOR = 1 / 3

// 乘数展示为 1/N 形式（1/3 经 IEEE754 往返不整，用容差取整）
const discountFactorLabel = (() => {
  const reciprocal = 1 / CREDIT_DISCOUNT_FACTOR
  const rounded = Math.round(reciprocal)
  return Math.abs(reciprocal - rounded) < 1e-9 ? `1/${rounded}` : String(CREDIT_DISCOUNT_FACTOR)
})()
const discountBadgeText = CREDIT_DISCOUNT_MODELS
  .map((m) => `上游 ${m.toUpperCase()} 积分 ×${discountFactorLabel}`)
  .join(' · ')

export interface PeakWeightedReportOptions {
  startDate: string
  endDate: string
  timezone: string
  groupName?: string
}

export function buildPeakWeightedReportHtml(items: PeakWeightedTokenItem[], details: PeakWeightedModelDetail[], opts: PeakWeightedReportOptions): string {
  const data = items.map((d) => ({
    user_id: d.user_id,
    label: escapeHtml(d.user_label),
    original: d.total_tokens,
    input_tokens: d.input_tokens,
    cache_read: d.cache_read_tokens,
    output_tokens: d.output_tokens,
    peak_input: d.peak_input_tokens,
    peak_cache_read: d.peak_cache_read_tokens,
    peak_output: d.peak_output_tokens,
    weighted_input: d.weighted_input_tokens,
    weighted_cache_read: d.weighted_cache_read_tokens,
    weighted_output: d.weighted_output_tokens,
    disc_weighted_input: d.discounted_weighted_input_tokens,
    disc_weighted_cache_read: d.discounted_weighted_cache_read_tokens,
    disc_weighted_output: d.discounted_weighted_output_tokens,
    disc_offpeak_input: d.discounted_offpeak_input_tokens,
    disc_offpeak_cache_read: d.discounted_offpeak_cache_read_tokens,
    disc_offpeak_output: d.discounted_offpeak_output_tokens,
    request_count: d.requests,
  }))
  const modelDetails = details.map((m) => ({
    user_id: m.user_id,
    model: escapeHtml(m.model),
    in_peak: m.in_peak,
    cache_read: m.cache_read_tokens,
    input: m.input_tokens,
    output: m.output_tokens,
  }))
  const payload = JSON.stringify({
    range: { start: opts.startDate, end: opts.endDate },
    timezone: opts.timezone,
    rows: data,
    model_details: modelDetails,
  })
  const titleSuffix = opts.groupName ? ` · ${escapeHtml(opts.groupName)}` : ''
  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>积分使用统计报告${titleSuffix}</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.5.0/dist/chart.umd.js" integrity="sha384-iU8HYtnGQ8Cy4zl7gbNMOhsDTTKX02BTXptVP/vqAWIaTfM7isw76iyZCsjL2eVi" crossorigin="anonymous"><\/script>
<style>
  :root {
    color-scheme: light;
    --bg: #f1f5f9;
    --bg-gradient: linear-gradient(135deg, #eef2ff 0%, #f1f5f9 50%, #f0f9ff 100%);
    --card-bg: #ffffff;
    --primary: #2563eb;
    --primary-light: #3b82f6;
    --primary-dark: #1d4ed8;
    --accent: #8b5cf6;
    --accent-light: #a78bfa;
    --text: #0f172a;
    --text-secondary: #475569;
    --text-muted: #94a3b8;
    --border: #e2e8f0;
    --success: #10b981;
    --warning: #f59e0b;
    --danger: #ef4444;
    --shadow-sm: 0 1px 2px rgba(15,23,42,0.04);
    --shadow: 0 4px 12px rgba(15,23,42,0.06);
    --shadow-lg: 0 12px 32px rgba(15,23,42,0.08);
    --radius: 14px;
    --radius-sm: 10px;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "PingFang SC", "Microsoft YaHei", "Helvetica Neue", "Segoe UI", sans-serif;
    background: var(--bg-gradient);
    color: var(--text);
    line-height: 1.6;
    min-height: 100vh;
    -webkit-font-smoothing: antialiased;
    -moz-osx-font-smoothing: grayscale;
  }
  .container { max-width: 1280px; margin: 0 auto; padding: 0 24px 64px; }
  .header {
    background: linear-gradient(135deg, #1e293b 0%, #1e3a5f 50%, #2563eb 100%);
    color: white; padding: 48px 0 40px; border-radius: 0 0 28px 28px;
    box-shadow: var(--shadow-lg); position: relative; overflow: hidden;
  }
  .header::before {
    content: ''; position: absolute; top: -50%; right: -10%; width: 500px; height: 500px;
    background: radial-gradient(circle, rgba(96,165,250,0.25) 0%, transparent 70%); pointer-events: none;
  }
  .header::after {
    content: ''; position: absolute; bottom: -30%; left: -5%; width: 400px; height: 400px;
    background: radial-gradient(circle, rgba(139,92,246,0.18) 0%, transparent 70%); pointer-events: none;
  }
  .header-inner { max-width: 1280px; margin: 0 auto; padding: 0 24px; position: relative; z-index: 1; }
  .header h1 { font-size: 30px; font-weight: 700; letter-spacing: -0.02em; }
  .header .subtitle { margin-top: 10px; font-size: 15px; color: rgba(255,255,255,0.75); font-weight: 400; }
  .header .meta-row { margin-top: 18px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .badge {
    display: inline-flex; align-items: center; gap: 6px; padding: 6px 14px;
    background: rgba(255,255,255,0.12); backdrop-filter: blur(8px);
    border: 1px solid rgba(255,255,255,0.2); border-radius: 999px;
    font-size: 13px; color: rgba(255,255,255,0.9);
  }
  .badge .dot {
    width: 7px; height: 7px; background: #34d399; border-radius: 50%;
    box-shadow: 0 0 0 3px rgba(52,211,153,0.3); animation: pulse 2s infinite;
  }
  @keyframes pulse {
    0%, 100% { box-shadow: 0 0 0 3px rgba(52,211,153,0.3); }
    50% { box-shadow: 0 0 0 6px rgba(52,211,153,0.1); }
  }
  .kpi-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 20px; margin-top: -32px; position: relative; z-index: 2; }
  .kpi-grid.ctx-kpi-grid { margin-top: 0; z-index: 1; }
  .kpi-card {
    background: var(--card-bg); border-radius: var(--radius); padding: 24px;
    box-shadow: var(--shadow-lg); border: 1px solid var(--border);
    transition: transform 0.2s ease, box-shadow 0.2s ease; position: relative; overflow: hidden;
  }
  .kpi-card:hover { transform: translateY(-3px); box-shadow: 0 16px 40px rgba(15,23,42,0.12); }
  .kpi-card::after {
    content: ''; position: absolute; top: 0; left: 0; right: 0; height: 3px;
    background: linear-gradient(90deg, var(--primary), var(--accent)); opacity: 0; transition: opacity 0.2s;
  }
  .kpi-card:hover::after { opacity: 1; }
  .kpi-label { font-size: 13px; color: var(--text-muted); font-weight: 500; text-transform: uppercase; letter-spacing: 0.04em; }
  .kpi-value { font-size: 32px; font-weight: 700; color: var(--text); margin-top: 8px; letter-spacing: -0.02em; font-variant-numeric: tabular-nums; }
  .kpi-unit { font-size: 15px; font-weight: 500; color: var(--text-muted); margin-left: 4px; }
  .kpi-hint { font-size: 12px; color: var(--text-secondary); margin-top: 6px; }
  .kpi-icon { position: absolute; top: 20px; right: 20px; width: 40px; height: 40px; border-radius: 12px; display: flex; align-items: center; justify-content: center; font-size: 20px; }
  .section { margin-top: 36px; }
  .section-title { font-size: 20px; font-weight: 700; color: var(--text); margin-bottom: 16px; display: flex; align-items: center; gap: 10px; }
  .section-title .bar { width: 4px; height: 22px; background: linear-gradient(180deg, var(--primary), var(--accent)); border-radius: 3px; }
  .section-desc { font-size: 14px; color: var(--text-muted); margin-bottom: 20px; }
  .charts-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; }
  @media (max-width: 900px) { .charts-grid { grid-template-columns: 1fr; } }
  .chart-card { background: var(--card-bg); border-radius: var(--radius); padding: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); }
  .chart-card.full { grid-column: 1 / -1; }
  .chart-title { font-size: 16px; font-weight: 600; color: var(--text); margin-bottom: 4px; }
  .chart-subtitle { font-size: 13px; color: var(--text-muted); margin-bottom: 20px; }
  .chart-wrapper { position: relative; height: 380px; }
  .chart-wrapper.tall { height: 460px; }
  .podium { display: grid; grid-template-columns: repeat(3, 1fr); gap: 16px; }
  @media (max-width: 700px) { .podium { grid-template-columns: 1fr; } }
  .podium-card {
    background: var(--card-bg); border-radius: var(--radius); padding: 24px; text-align: center;
    box-shadow: var(--shadow); border: 1px solid var(--border); position: relative; overflow: hidden;
  }
  .podium-card .rank { font-size: 13px; font-weight: 600; color: var(--text-muted); letter-spacing: 0.05em; }
  .podium-card .medal {
    width: 56px; height: 56px; margin: 12px auto; border-radius: 50%; display: flex;
    align-items: center; justify-content: center; font-size: 26px; font-weight: 700; color: white;
    background: linear-gradient(135deg, var(--primary), var(--primary-dark)); box-shadow: 0 6px 20px rgba(37,99,235,0.35);
  }
  .podium-card.gold .medal { background: linear-gradient(135deg, #fbbf24, #f59e0b); box-shadow: 0 6px 20px rgba(245,158,11,0.4); }
  .podium-card.silver .medal { background: linear-gradient(135deg, #cbd5e1, #94a3b8); box-shadow: 0 6px 20px rgba(148,163,184,0.35); }
  .podium-card.bronze .medal { background: linear-gradient(135deg, #f97316, #c2410c); box-shadow: 0 6px 20px rgba(194,65,12,0.35); }
  .podium-card .name { font-size: 18px; font-weight: 700; color: var(--text); margin-bottom: 4px; }
  .podium-card .tokens { font-size: 24px; font-weight: 700; color: var(--primary); font-variant-numeric: tabular-nums; }
  .podium-card .tokens-unit { font-size: 13px; font-weight: 500; color: var(--text-muted); }
  .podium-card .meta { font-size: 13px; color: var(--text-secondary); margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border); }
  .table-card { background: var(--card-bg); border-radius: var(--radius); padding: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); }
  .table-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 16px; margin-bottom: 18px; flex-wrap: wrap; }
  .search-box { position: relative; flex: 1; min-width: 220px; max-width: 360px; }
  .search-box input {
    width: 100%; padding: 10px 14px 10px 38px; border: 1px solid var(--border); border-radius: 10px;
    font-size: 14px; color: var(--text); background: #f8fafc;
    transition: border-color 0.15s, background 0.15s, box-shadow 0.15s; font-family: inherit;
  }
  .search-box input:focus { outline: none; border-color: var(--primary); background: white; box-shadow: 0 0 0 3px rgba(37,99,235,0.12); }
  .search-box::before {
    content: ''; position: absolute; left: 13px; top: 50%; transform: translateY(-50%); width: 16px; height: 16px;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='16' viewBox='0 0 24 24' fill='none' stroke='%2394a3b8' stroke-width='2.5' stroke-linecap='round' stroke-linejoin='round'%3E%3Ccircle cx='11' cy='11' r='8'%3E%3C/circle%3E%3Cline x1='21' y1='21' x2='16.65' y2='16.65'%3E%3C/line%3E%3C/svg%3E");
    background-repeat: no-repeat; background-position: center; pointer-events: none;
  }
  .filter-chips { display: flex; gap: 8px; }
  .chip {
    padding: 8px 14px; border-radius: 999px; border: 1px solid var(--border); background: white;
    font-size: 13px; font-weight: 500; color: var(--text-secondary); cursor: pointer;
    transition: all 0.15s; font-family: inherit;
  }
  .chip:hover { border-color: var(--primary); color: var(--primary); }
  .chip.active { background: var(--primary); border-color: var(--primary); color: white; }
  .table-scroll { overflow-x: auto; border-radius: var(--radius-sm); border: 1px solid var(--border); }
  table { width: 100%; border-collapse: collapse; font-size: 14px; }
  thead { background: #f8fafc; }
  th {
    text-align: left; padding: 10px 10px; font-weight: 600; color: var(--text-secondary);
    font-size: 12px; text-transform: uppercase; letter-spacing: 0.05em;
    border-bottom: 1px solid var(--border); white-space: nowrap; cursor: pointer; user-select: none;
  }
  th:hover { color: var(--primary); }
  th .sort-icon { display: inline-block; margin-left: 4px; opacity: 0.35; font-size: 11px; }
  th.sorted .sort-icon { opacity: 1; color: var(--primary); }
  th.group { text-align: center; border-bottom: none; cursor: default; padding-bottom: 2px; }
  th.group.group-model { background: #ede9fe; color: #6d28d9; }
  th.group.group-offpeak { background: #e0f2fe; color: #0369a1; }
  th.group.group-peak { background: #fef3c7; color: #b45309; }
  th.sub { padding-top: 2px; }
  th.sub.sub-offpeak { background: #f0f9ff; }
  th.sub.sub-peak { background: #fffbeb; }
  td { padding: 9px 10px; border-bottom: 1px solid #f1f5f9; color: var(--text); white-space: nowrap; }
  tbody tr:last-child td { border-bottom: none; }
  tbody tr:hover { background: #f8fafc; }
  .user-name {
    font-weight: 600; color: var(--text); display: inline-block; max-width: 170px;
    overflow: hidden; text-overflow: ellipsis; vertical-align: bottom;
  }
  .num { font-variant-numeric: tabular-nums; font-weight: 600; }
  .num-muted { font-variant-numeric: tabular-nums; color: var(--text-secondary); }
  .bar-cell { width: 110px; }
  .mini-bar { height: 6px; background: #f1f5f9; border-radius: 3px; overflow: hidden; margin-top: 4px; }
  .mini-bar-fill { height: 100%; background: linear-gradient(90deg, var(--primary), var(--accent)); border-radius: 3px; }
  .table-footer { margin-top: 16px; display: flex; justify-content: space-between; align-items: center; font-size: 13px; color: var(--text-muted); flex-wrap: wrap; gap: 8px; }
  .insight-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; }
  .insight-card {
    background: var(--card-bg); border-radius: var(--radius); padding: 20px;
    border: 1px solid var(--border); box-shadow: var(--shadow-sm); display: flex; gap: 14px; align-items: flex-start;
  }
  .insight-card .ic-icon { width: 42px; height: 42px; border-radius: 11px; display: flex; align-items: center; justify-content: center; font-size: 20px; flex-shrink: 0; }
  .ic-icon.blue { background: #dbeafe; color: #2563eb; }
  .ic-icon.green { background: #d1fae5; color: #059669; }
  .ic-icon.amber { background: #fef3c7; color: #d97706; }
  .ic-icon.violet { background: #ede9fe; color: #7c3aed; }
  .insight-card .ic-title { font-size: 14px; font-weight: 600; color: var(--text); }
  .insight-card .ic-text { font-size: 13px; color: var(--text-secondary); margin-top: 4px; line-height: 1.5; }
  .insight-card .ic-text strong { color: var(--text); font-weight: 700; }
  .footer { text-align: center; margin-top: 48px; font-size: 13px; color: var(--text-muted); }
  .footer .divider { width: 40px; height: 3px; background: linear-gradient(90deg, var(--primary), var(--accent)); border-radius: 2px; margin: 0 auto 16px; }
  @media print {
    .header { border-radius: 0; }
    .chart-card, .kpi-card, .table-card, .podium-card, .insight-card { box-shadow: none; break-inside: avoid; }
  }
</style>
</head>
<body>

<div class="header">
  <div class="header-inner">
    <h1>积分使用统计报告${titleSuffix}</h1>
    <p class="subtitle">用户调用量、积分消耗与请求频次分析 · 积分按订阅分组配置的高峰时段倍率放大</p>
    <div class="meta-row">
      <span class="badge"><span class="dot"></span> 统计周期：<span id="reportRange">—</span></span>
      <span class="badge">时区：<span id="reportTz">—</span></span>
      <span class="badge">积分单价（每M）：输入 ${CREDIT_PRICES_PER_M.input} · 缓存命中 ${CREDIT_PRICES_PER_M.cacheRead} · 输出 ${CREDIT_PRICES_PER_M.output}，高峰按分组倍率放大</span>
      <span class="badge">积分折扣：${discountBadgeText}</span>
      <span class="badge">周积分额度：${WEEKLY_CREDIT_QUOTA.toLocaleString('en-US')} /席位</span>
    </div>
  </div>
</div>

<div class="container">
  <div class="kpi-grid" id="kpiGrid"></div>
  <div class="section">
    <h2 class="section-title"><span class="bar"></span>关键洞察</h2>
    <div class="insight-grid" id="insightGrid"></div>
  </div>
  <div class="section">
    <h2 class="section-title"><span class="bar"></span>周额度消耗排行 · TOP 3</h2>
    <p class="section-desc">按周额度消耗积分排名的前三位用户</p>
    <div class="podium" id="podium"></div>
  </div>
  <div class="section">
    <h2 class="section-title"><span class="bar"></span>数据可视化</h2>
    <div class="charts-grid">
      <div class="chart-card full">
        <div class="chart-title">Top 15 用户 · 消耗积分</div>
        <div class="chart-subtitle">按周额度消耗积分横向对比</div>
        <div class="chart-wrapper tall"><canvas id="chartTop15"></canvas></div>
      </div>
      <div class="chart-card">
        <div class="chart-title">高峰 vs 非高峰 积分对比</div>
        <div class="chart-subtitle">Top 10 用户高峰 / 非高峰消耗积分对比</div>
        <div class="chart-wrapper"><canvas id="chartCompare"></canvas></div>
      </div>
      <div class="chart-card">
        <div class="chart-title">周额度消耗占比分布</div>
        <div class="chart-subtitle">各用户消耗积分占总消耗的比例</div>
        <div class="chart-wrapper"><canvas id="chartPie"></canvas></div>
      </div>
    </div>
  </div>
  <div class="section">
    <h2 class="section-title"><span class="bar"></span>平均请求上下文长度分析</h2>
    <p class="section-desc">基于「原始 Token ÷ 请求数」衡量每位用户单次请求的平均上下文规模。口径采用原始 Token，未含高峰期加权放大。</p>
    <div class="kpi-grid ctx-kpi-grid" id="ctxKpiGrid"></div>
    <div class="charts-grid" style="margin-top:20px">
      <div class="chart-card full">
        <div class="chart-title">Top 15 用户 · 单次平均上下文长度</div>
        <div class="chart-subtitle">按「原始 Token / 请求数」降序排列（单位：K tokens）</div>
        <div class="chart-wrapper tall"><canvas id="chartCtx"></canvas></div>
      </div>
    </div>
  </div>
  <div class="section">
    <h2 class="section-title"><span class="bar"></span>完整数据明细</h2>
    <p class="section-desc">非高峰 / 高峰按所属订阅分组的高峰窗口划分（仅周一至周五生效，周末全部计入非高峰），模型列取上游模型（模型映射后的最终上游请求模型），token 为原始未加权量。周额度消耗 = 消耗积分 ÷ ${WEEKLY_CREDIT_QUOTA.toLocaleString('en-US')} × 100%，其中消耗积分 = 加权输入×${CREDIT_PRICES_PER_M.input} + 加权缓存命中×${CREDIT_PRICES_PER_M.cacheRead} + 加权输出×${CREDIT_PRICES_PER_M.output}（每 M tokens，高峰量已乘分组倍率），${discountBadgeText}。表中 token 各列单位均为 M（百万 tokens），平均 Token 为单次请求的原始 token 数。</p>
    <div class="table-card">
      <div class="table-toolbar">
        <div class="search-box">
          <input type="text" id="searchInput" placeholder="搜索用户名 / 邮箱...">
        </div>
        <div class="filter-chips">
          <button class="chip active" data-filter="all">全部</button>
          <button class="chip" data-filter="top10">Top 10</button>
          <button class="chip" data-filter="heavy" id="chipHeavy">重度用户</button>
        </div>
      </div>
      <div class="table-scroll">
        <table>
          <thead id="tableHead"></thead>
          <tbody id="tableBody"></tbody>
        </table>
      </div>
      <div class="table-footer">
        <span id="tableCount"></span>
        <span>点击表头可排序</span>
      </div>
    </div>
  </div>
  <div class="footer">
    <div class="divider"></div>
    积分使用统计报告 · 数据仅供内部参考
  </div>
</div>

<script>
var REPORT = ${payload};
var PRICE = { input: ${CREDIT_PRICES_PER_M.input}, cacheRead: ${CREDIT_PRICES_PER_M.cacheRead}, output: ${CREDIT_PRICES_PER_M.output} };
var WEEKLY_QUOTA = ${WEEKLY_CREDIT_QUOTA};
var DISCOUNT = ${CREDIT_DISCOUNT_FACTOR};
var HEAVY_QUOTA_PCT = 25;
// 明细列的度量与顺序，须与表头/单元格渲染一致
var METRICS = [['cache_read', '缓存'], ['input', '输入'], ['output', '输出']];
var rawData = REPORT.rows.map(function (r) {
  var fullCredits = (r.weighted_input * PRICE.input + r.weighted_cache_read * PRICE.cacheRead + r.weighted_output * PRICE.output) / 1e6;
  var discBase = (r.disc_weighted_input * PRICE.input + r.disc_weighted_cache_read * PRICE.cacheRead + r.disc_weighted_output * PRICE.output) / 1e6;
  var credits = fullCredits - discBase * (1 - DISCOUNT);
  // 非高峰积分：非折扣模型按非高峰原始量 × 单价，折扣模型部分再乘折扣；高峰积分 = 总积分 - 非高峰积分
  var offIn = (r.input_tokens - r.peak_input) - r.disc_offpeak_input;
  var offCache = (r.cache_read - r.peak_cache_read) - r.disc_offpeak_cache_read;
  var offOut = (r.output_tokens - r.peak_output) - r.disc_offpeak_output;
  var offFull = (offIn * PRICE.input + offCache * PRICE.cacheRead + offOut * PRICE.output) / 1e6;
  var offDiscBase = (r.disc_offpeak_input * PRICE.input + r.disc_offpeak_cache_read * PRICE.cacheRead + r.disc_offpeak_output * PRICE.output) / 1e6;
  var offpeakCredits = offFull + offDiscBase * DISCOUNT;
  return {
    user_label: r.label,
    original: r.original / 1e6,
    credits: credits,
    offpeak_credits: offpeakCredits,
    peak_credits: credits - offpeakCredits,
    quota_pct: WEEKLY_QUOTA > 0 ? (credits / WEEKLY_QUOTA) * 100 : 0,
    request_count: r.request_count,
    ctxLen: r.request_count > 0 ? r.original / r.request_count : 0,
    cells: {},
  };
});

// 模型分组明细：模型列按全量 token 降序展开（宽表全量 + 横向滚动）
var modelVolume = {};
var detailsByUser = {};
REPORT.model_details.forEach(function (m) {
  if (!detailsByUser[m.user_id]) detailsByUser[m.user_id] = {};
  var byModel = detailsByUser[m.user_id];
  if (!byModel[m.model]) {
    byModel[m.model] = { offpeak: { cache_read: 0, input: 0, output: 0 }, peak: { cache_read: 0, input: 0, output: 0 } };
    modelVolume[m.model] = 0;
  }
  var slot = m.in_peak ? 'peak' : 'offpeak';
  byModel[m.model][slot].cache_read += m.cache_read;
  byModel[m.model][slot].input += m.input;
  byModel[m.model][slot].output += m.output;
  modelVolume[m.model] += m.cache_read + m.input + m.output;
});
var models = Object.keys(modelVolume).sort(function (a, b) { return modelVolume[b] - modelVolume[a]; });
rawData.forEach(function (d, idx) {
  var byModel = detailsByUser[REPORT.rows[idx].user_id] || {};
  models.forEach(function (model, mi) {
    ['offpeak', 'peak'].forEach(function (slot) {
      METRICS.forEach(function (met) {
        d.cells['mk_' + mi + '_' + slot + '_' + met[0]] = byModel[model] ? byModel[model][slot][met[0]] / 1e6 : 0;
      });
    });
  });
});

document.getElementById('reportRange').textContent = String(REPORT.range.start).replace('T', ' ') + ' — ' + String(REPORT.range.end).replace('T', ' ');
document.getElementById('reportTz').textContent = REPORT.timezone;
document.getElementById('chipHeavy').textContent = '重度用户 (≥' + HEAVY_QUOTA_PCT + '% 周额度)';

var totalOriginal = rawData.reduce(function (s, d) { return s + d.original; }, 0);
var totalRequests = rawData.reduce(function (s, d) { return s + d.request_count; }, 0);
var totalUsers = rawData.length;
var totalCredits = rawData.reduce(function (s, d) { return s + d.credits; }, 0);
var totalOffpeakCredits = rawData.reduce(function (s, d) { return s + d.offpeak_credits; }, 0);
var totalPeakCredits = rawData.reduce(function (s, d) { return s + d.peak_credits; }, 0);
var avgRequests = totalUsers > 0 ? Math.round(totalRequests / totalUsers) : 0;
var heavyUsers = rawData.filter(function (d) { return d.quota_pct >= HEAVY_QUOTA_PCT; }).length;
var creditsSorted = rawData.slice().sort(function (a, b) { return b.credits - a.credits; });
var top3Credits = creditsSorted.slice(0, 3).reduce(function (s, d) { return s + d.credits; }, 0);
var top3CreditsShare = totalCredits > 0 ? (top3Credits / totalCredits * 100).toFixed(1) : '0.0';
var quotaTop = creditsSorted[0] || { user_label: '—', quota_pct: 0, credits: 0 };
var peakCreditsShare = totalCredits > 0 ? (totalPeakCredits / totalCredits * 100).toFixed(1) : '0.0';
var reqSorted = rawData.slice().sort(function (a, b) { return b.request_count - a.request_count; });
var reqTop = reqSorted[0] || { user_label: '—', request_count: 0 };
var reqTopCreditsRank = creditsSorted.findIndex(function (d) { return d.user_label === reqTop.user_label; }) + 1;

var overallAvgCtx = totalRequests > 0 ? (totalOriginal * 1e6) / totalRequests : 0;
var ctxByUser = rawData.filter(function (d) { return d.ctxLen > 0; }).map(function (d) { return d.ctxLen; }).sort(function (a, b) { return a - b; });
var ctxUserCount = ctxByUser.length;
var ctxMean = ctxUserCount > 0 ? ctxByUser.reduce(function (s, v) { return s + v; }, 0) / ctxUserCount : 0;
var ctxMedian = ctxUserCount === 0 ? 0 : (ctxUserCount % 2 === 0
  ? (ctxByUser[ctxUserCount / 2 - 1] + ctxByUser[ctxUserCount / 2]) / 2
  : ctxByUser[Math.floor(ctxUserCount / 2)]);
var ctxMax = ctxUserCount > 0 ? Math.max.apply(null, ctxByUser) : 0;
var ctxMin = ctxUserCount > 0 ? Math.min.apply(null, ctxByUser) : 0;
var ctxSorted = rawData.filter(function (d) { return d.ctxLen > 0; }).sort(function (a, b) { return b.ctxLen - a.ctxLen; });
var ctxTopUser = ctxSorted[0] || reqTop;
var heavyReqUsers = rawData.filter(function (d) { return d.request_count >= 100; });
var ctxShortHighFreq = heavyReqUsers.length
  ? heavyReqUsers.reduce(function (min, d) { return d.ctxLen < min.ctxLen ? d : min; }, heavyReqUsers[0])
  : reqTop;

var fmt = function (n) { return n.toLocaleString('en-US'); };
var fmt1 = function (n) { return n.toLocaleString('en-US', { maximumFractionDigits: 1 }); };
var fmtCtx = function (n) {
  if (!isFinite(n) || n <= 0) return '0';
  if (n >= 1000) return (n / 1000).toLocaleString('en-US', { maximumFractionDigits: 1 }) + 'K';
  return Math.round(n).toLocaleString('en-US');
};

var kpis = [
  { label: '总用户数', value: totalUsers, unit: '人', hint: '活跃调用账户', icon: '👥', bg: '#dbeafe', color: '#2563eb' },
  { label: '总请求次数', value: fmt(totalRequests), unit: '次', hint: '人均 ' + fmt(avgRequests) + ' 次', icon: '🔄', bg: '#d1fae5', color: '#059669' },
  { label: '周额度消耗总计', value: fmt(Math.round(totalCredits)), unit: '积分', hint: '折合 ' + (WEEKLY_QUOTA > 0 ? (totalCredits / WEEKLY_QUOTA).toFixed(2) : '—') + ' 个周额度（' + fmt(WEEKLY_QUOTA) + ' 积分/席位/周）', icon: '💳', bg: '#d1fae5', color: '#059669' },
  { label: '非高峰积分总计', value: fmt(Math.round(totalOffpeakCredits)), unit: '积分', hint: '未落入高峰窗口的消耗，周末全部计入非高峰', icon: '🌙', bg: '#e0f2fe', color: '#0284c7' },
  { label: '高峰积分总计', value: fmt(Math.round(totalPeakCredits)), unit: '积分', hint: '高峰窗口消耗，已按分组倍率放大', icon: '☀️', bg: '#fef3c7', color: '#d97706' },
  { label: '重度用户', value: heavyUsers, unit: '人', hint: '周额度消耗 ≥ ' + HEAVY_QUOTA_PCT + '%', icon: '📊', bg: '#fef3c7', color: '#d97706' },
];
document.getElementById('kpiGrid').innerHTML = kpis.map(function (k) {
  return '<div class="kpi-card"><div class="kpi-icon" style="background:' + k.bg + ';color:' + k.color + '">' + k.icon + '</div>' +
    '<div class="kpi-label">' + k.label + '</div>' +
    '<div class="kpi-value">' + k.value + '<span class="kpi-unit">' + k.unit + '</span></div>' +
    '<div class="kpi-hint">' + k.hint + '</div></div>';
}).join('');

var insights = [
  { icon: '🏆', cls: 'amber', title: '头部集中度', text: creditsSorted.length >= 3
    ? '周额度消耗前 3 名（<strong>' + creditsSorted[0].user_label + '</strong>、<strong>' + creditsSorted[1].user_label + '</strong>、<strong>' + creditsSorted[2].user_label + '</strong>）合计消耗 <strong>' + fmt(Math.round(top3Credits)) + '</strong> 积分，占总消耗的 <strong>' + top3CreditsShare + '%</strong>。'
    : '共 ' + totalUsers + ' 位用户，合计消耗 <strong>' + fmt(Math.round(totalCredits)) + '</strong> 积分。' },
  { icon: '💳', cls: 'green', title: '周额度消耗', text: '消耗榜首 <strong>' + quotaTop.user_label + '</strong> 消耗周额度的 <strong>' + quotaTop.quota_pct.toFixed(1) + '%</strong>（' + fmt(Math.round(quotaTop.credits)) + ' 积分）。' },
  { icon: '📈', cls: 'blue', title: '高峰积分占比', text: '高峰时段积分占总消耗的 <strong>' + peakCreditsShare + '%</strong>（高峰 <strong>' + fmt(Math.round(totalPeakCredits)) + '</strong> 积分 · 非高峰 <strong>' + fmt(Math.round(totalOffpeakCredits)) + '</strong> 积分）。' },
  { icon: '🚀', cls: 'violet', title: '最高频调用', text: '<strong>' + reqTop.user_label + '</strong> 以 <strong>' + fmt(reqTop.request_count) + '</strong> 次请求位居请求频次榜首，周额度消耗排名第 <strong>' + (reqTopCreditsRank || '—') + '</strong> 位。' },
  { icon: '📐', cls: 'blue', title: '上下文长度特征', text: '全量请求平均上下文约 <strong>' + fmtCtx(overallAvgCtx) + ' tokens</strong>（用户中位数 <strong>' + fmtCtx(ctxMedian) + '</strong>）。<strong>' + ctxTopUser.user_label + '</strong> 单次最长（<strong>' + fmtCtx(ctxMax) + '</strong>）；高频用户（≥100 次）中 <strong>' + ctxShortHighFreq.user_label + '</strong> 单次最短（<strong>' + fmtCtx(ctxShortHighFreq.ctxLen) + '</strong>）。' },
];
document.getElementById('insightGrid').innerHTML = insights.map(function (i) {
  return '<div class="insight-card"><div class="ic-icon ' + i.cls + '">' + i.icon + '</div>' +
    '<div><div class="ic-title">' + i.title + '</div><div class="ic-text">' + i.text + '</div></div></div>';
}).join('');

var medals = ['gold', 'silver', 'bronze'];
var ranks = ['TOP 1', 'TOP 2', 'TOP 3'];
document.getElementById('podium').innerHTML = creditsSorted.slice(0, 3).map(function (d, i) {
  return '<div class="podium-card ' + medals[i] + '">' +
    '<div class="rank">' + ranks[i] + '</div>' +
    '<div class="medal">' + (i + 1) + '</div>' +
    '<div class="name">' + d.user_label + '</div>' +
    '<div class="tokens">' + fmt(Math.round(d.credits)) + '<span class="tokens-unit"> 积分</span></div>' +
    '<div class="meta">请求 ' + fmt(d.request_count) + ' 次 · 占总消耗 ' + (totalCredits > 0 ? (d.credits / totalCredits * 100).toFixed(1) : '0.0') + '%</div>' +
    '</div>';
}).join('');

if (typeof Chart !== 'undefined') {
  Chart.defaults.font.family = "-apple-system, 'PingFang SC', 'Microsoft YaHei', sans-serif";
  Chart.defaults.color = '#64748b';

  var top15 = creditsSorted.slice(0, 15);
  new Chart(document.getElementById('chartTop15'), {
    type: 'bar',
    data: {
      labels: top15.map(function (d) { return d.user_label; }),
      datasets: [{
        label: '消耗积分',
        data: top15.map(function (d) { return Math.round(d.credits); }),
        backgroundColor: function (ctx) {
          var g = ctx.chart.ctx.createLinearGradient(0, 0, 600, 0);
          g.addColorStop(0, '#2563eb'); g.addColorStop(1, '#8b5cf6');
          return g;
        },
        borderRadius: 6,
        barThickness: 18,
      }],
    },
    options: {
      indexAxis: 'y',
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: { backgroundColor: '#1e293b', padding: 12, callbacks: { label: function (c) { return ' ' + fmt(c.parsed.x) + ' 积分'; } } },
      },
      scales: {
        x: { grid: { color: '#f1f5f9' }, ticks: { font: { size: 11 } } },
        y: { grid: { display: false }, ticks: { font: { size: 12, weight: '500' } } },
      },
    },
  });

  var top10 = creditsSorted.slice(0, 10);
  new Chart(document.getElementById('chartCompare'), {
    type: 'bar',
    data: {
      labels: top10.map(function (d) { return d.user_label; }),
      datasets: [
        { label: '非高峰积分', data: top10.map(function (d) { return Math.round(d.offpeak_credits); }), backgroundColor: '#3b82f6', borderRadius: 5, barPercentage: 0.7 },
        { label: '高峰积分', data: top10.map(function (d) { return Math.round(d.peak_credits); }), backgroundColor: '#f59e0b', borderRadius: 5, barPercentage: 0.7 },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { position: 'bottom', labels: { usePointStyle: true, pointStyle: 'circle', padding: 16, font: { size: 12 } } },
        tooltip: { backgroundColor: '#1e293b', padding: 12, callbacks: { label: function (c) { return ' ' + fmt(c.parsed.y) + ' 积分'; } } },
      },
      scales: {
        x: { grid: { display: false }, ticks: { font: { size: 11 } } },
        y: { grid: { color: '#f1f5f9' }, ticks: { font: { size: 11 } } },
      },
    },
  });

  var top8 = creditsSorted.slice(0, 8);
  var othersCredits = creditsSorted.slice(8).reduce(function (s, d) { return s + d.credits; }, 0);
  var pieColors = ['#2563eb', '#3b82f6', '#60a5fa', '#8b5cf6', '#a78bfa', '#f59e0b', '#10b981', '#ef4444', '#cbd5e1'];
  new Chart(document.getElementById('chartPie'), {
    type: 'doughnut',
    data: {
      labels: top8.map(function (d) { return d.user_label; }).concat(['其他']),
      datasets: [{
        data: top8.map(function (d) { return Math.round(d.credits); }).concat([Math.round(othersCredits)]),
        backgroundColor: pieColors,
        borderColor: '#fff',
        borderWidth: 3,
        hoverOffset: 8,
      }],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      cutout: '58%',
      plugins: {
        legend: { position: 'right', labels: { usePointStyle: true, pointStyle: 'circle', padding: 12, font: { size: 12 }, boxWidth: 8 } },
        tooltip: {
          backgroundColor: '#1e293b', padding: 12,
          callbacks: { label: function (c) { return ' ' + c.label + ': ' + fmt(Math.round(c.parsed)) + ' 积分 (' + (totalCredits > 0 ? (c.parsed / totalCredits * 100).toFixed(1) : '0.0') + '%)'; } },
        },
      },
    },
  });

  var ctxTop15 = ctxSorted.slice(0, 15);
  new Chart(document.getElementById('chartCtx'), {
    type: 'bar',
    data: {
      labels: ctxTop15.map(function (d) { return d.user_label; }),
      datasets: [{
        label: '单次平均上下文长度',
        data: ctxTop15.map(function (d) { return d.ctxLen / 1000; }),
        backgroundColor: function (c) {
          var g = c.chart.ctx.createLinearGradient(0, 0, 600, 0);
          g.addColorStop(0, '#059669'); g.addColorStop(1, '#8b5cf6');
          return g;
        },
        borderRadius: 6,
        barThickness: 18,
      }],
    },
    options: {
      indexAxis: 'y',
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: '#1e293b', padding: 12,
          callbacks: {
            label: function (c) { return ' ' + c.parsed.x.toFixed(1) + 'K tokens'; },
            afterLabel: function (c) { var d = ctxTop15[c.dataIndex]; return ' 共 ' + fmt(d.request_count) + ' 次请求 · 原始 ' + fmt1(d.original) + 'M'; },
          },
        },
      },
      scales: {
        x: { grid: { color: '#f1f5f9' }, ticks: { font: { size: 11 }, callback: function (v) { return v + 'K'; } } },
        y: { grid: { display: false }, ticks: { font: { size: 12, weight: '500' } } },
      },
    },
  });
}

var ctxKpis = [
  { label: '总体平均上下文', value: fmtCtx(overallAvgCtx), unit: 'tokens', hint: '全量请求加权均值 · ' + fmt(totalRequests) + ' 次请求', icon: '📐', bg: '#dbeafe', color: '#2563eb' },
  { label: '用户均值', value: fmtCtx(ctxMean), unit: 'tokens', hint: '跨 ' + ctxUserCount + ' 位用户的算术平均', icon: '📊', bg: '#ede9fe', color: '#7c3aed' },
  { label: '用户中位数', value: fmtCtx(ctxMedian), unit: 'tokens', hint: '半数用户单次上下文低于此值', icon: '📏', bg: '#d1fae5', color: '#059669' },
  { label: '峰值', value: fmtCtx(ctxMax), unit: 'tokens', hint: ctxTopUser.user_label + ' · 单次最长；最短 ' + fmtCtx(ctxMin), icon: '🏔️', bg: '#fef3c7', color: '#d97706' },
];
document.getElementById('ctxKpiGrid').innerHTML = ctxKpis.map(function (k) {
  return '<div class="kpi-card"><div class="kpi-icon" style="background:' + k.bg + ';color:' + k.color + '">' + k.icon + '</div>' +
    '<div class="kpi-label">' + k.label + '</div>' +
    '<div class="kpi-value">' + k.value + '<span class="kpi-unit">' + k.unit + '</span></div>' +
    '<div class="kpi-hint">' + k.hint + '</div></div>';
}).join('');

// 三行复合表头：固定列 + 每模型（非高峰/高峰 × 缓存/输入/输出）动态列组
var headRow1 = '<tr>' +
  '<th rowspan="3" data-key="label">用户 <span class="sort-icon"></span></th>' +
  '<th rowspan="3" data-key="original">总 Tokens <span class="sort-icon"></span></th>';
var headRow2 = '<tr>';
var headRow3 = '<tr>';
models.forEach(function (model, mi) {
  headRow1 += '<th colspan="6" class="group group-model">' + model + '</th>';
  headRow2 += '<th colspan="3" class="group group-offpeak">非高峰</th><th colspan="3" class="group group-peak">高峰</th>';
  ['offpeak', 'peak'].forEach(function (slot) {
    METRICS.forEach(function (met) {
      headRow3 += '<th data-key="mk_' + mi + '_' + slot + '_' + met[0] + '" class="sub sub-' + slot + '">' + met[1] + ' <span class="sort-icon"></span></th>';
    });
  });
});
headRow1 += '<th rowspan="3" data-key="request_count">请求次数 <span class="sort-icon"></span></th>' +
  '<th rowspan="3" data-key="ctxLen">平均 Token <span class="sort-icon"></span></th>' +
  '<th rowspan="3" data-key="quota_pct" class="sorted">周额度 <span class="sort-icon">▼</span></th>' +
  '<th rowspan="3">用量占比</th></tr>';
document.getElementById('tableHead').innerHTML = headRow1 + headRow2 + '</tr>' + headRow3 + '</tr>';

var sortKey = 'quota_pct';
var sortDir = 'desc';
var activeFilter = 'all';
var searchTerm = '';

function valueOf(d, key) { return key.indexOf('mk_') === 0 ? (d.cells[key] || 0) : d[key]; }

function renderTable() {
  var rows = rawData.slice();
  if (searchTerm) {
    var q = searchTerm.toLowerCase();
    rows = rows.filter(function (d) { return d.user_label.toLowerCase().indexOf(q) !== -1; });
  }
  if (activeFilter === 'top10') {
    var topSet = {};
    creditsSorted.slice(0, 10).forEach(function (d) { topSet[d.user_label] = true; });
    rows = rows.filter(function (d) { return topSet[d.user_label]; });
  } else if (activeFilter === 'heavy') {
    rows = rows.filter(function (d) { return d.quota_pct >= HEAVY_QUOTA_PCT; });
  }
  rows.sort(function (a, b) {
    var va = valueOf(a, sortKey), vb = valueOf(b, sortKey);
    if (typeof va === 'string') { return sortDir === 'asc' ? va.localeCompare(vb) : vb.localeCompare(va); }
    return sortDir === 'asc' ? va - vb : vb - va;
  });

  var maxCredits = rawData.length ? Math.max.apply(null, rawData.map(function (d) { return d.credits; })) : 0;
  document.getElementById('tableBody').innerHTML = rows.map(function (d) {
    var share = totalCredits > 0 ? (d.credits / totalCredits * 100).toFixed(2) : '0.00';
    var barW = maxCredits > 0 ? (d.credits / maxCredits * 100).toFixed(1) : '0.0';
    var modelCells = '';
    models.forEach(function (model, mi) {
      ['offpeak', 'peak'].forEach(function (slot) {
        METRICS.forEach(function (met) {
          var v = d.cells['mk_' + mi + '_' + slot + '_' + met[0]];
          modelCells += '<td><span class="num-muted">' + (v > 0 ? fmt1(v) : '—') + '</span></td>';
        });
      });
    });
    return '<tr>' +
      '<td><span class="user-name" title="' + d.user_label + '">' + d.user_label + '</span></td>' +
      '<td><span class="num">' + fmt1(d.original) + '</span></td>' +
      modelCells +
      '<td><span class="num-muted">' + fmt(d.request_count) + '</span></td>' +
      '<td><span class="num-muted" title="' + fmt(Math.round(d.ctxLen)) + ' tokens">' + fmtCtx(d.ctxLen) + '</span></td>' +
      '<td><span class="num" title="消耗 ' + fmt(Math.round(d.credits)) + ' 积分 / ' + fmt(WEEKLY_QUOTA) + '">' + d.quota_pct.toFixed(1) + '%</span></td>' +
      '<td class="bar-cell"><div style="display:flex;justify-content:space-between;font-size:12px;color:#64748b"><span>' + share + '%</span></div>' +
      '<div class="mini-bar"><div class="mini-bar-fill" style="width:' + barW + '%"></div></div></td>' +
      '</tr>';
  }).join('');
  document.getElementById('tableCount').textContent = '共 ' + rows.length + ' 条记录 · 总计 ' + rawData.length + ' 位用户';
}

document.querySelectorAll('th[data-key]').forEach(function (th) {
  th.addEventListener('click', function () {
    var key = th.dataset.key;
    if (sortKey === key) { sortDir = sortDir === 'asc' ? 'desc' : 'asc'; }
    else { sortKey = key; sortDir = 'desc'; }
    document.querySelectorAll('th').forEach(function (t) {
      t.classList.remove('sorted');
      var ic = t.querySelector('.sort-icon');
      if (ic) ic.textContent = '';
    });
    th.classList.add('sorted');
    th.querySelector('.sort-icon').textContent = sortDir === 'asc' ? '▲' : '▼';
    renderTable();
  });
});
document.getElementById('searchInput').addEventListener('input', function (e) { searchTerm = e.target.value; renderTable(); });
document.querySelectorAll('.chip').forEach(function (c) {
  c.addEventListener('click', function () {
    document.querySelectorAll('.chip').forEach(function (x) { x.classList.remove('active'); });
    c.classList.add('active');
    activeFilter = c.dataset.filter;
    renderTable();
  });
});
renderTable();
<\/script>
</body>
</html>`
}

export function peakWeightedReportFilename(startDate: string, endDate: string, groupName?: string): string {
  // 时间值含 T 和冒号（冒号在 Windows 文件名非法），统一替换为紧凑格式
  const sanitize = (s: string) => s.replace(/T/g, '_').replace(/:/g, '')
  // 分组名可能含 Windows 文件名非法字符
  const groupPart = groupName ? `_${groupName.replace(/[\\/:*?"<>|]/g, '_')}` : ''
  return `积分使用统计报告${groupPart}_${sanitize(startDate)}_to_${sanitize(endDate)}.html`
}
