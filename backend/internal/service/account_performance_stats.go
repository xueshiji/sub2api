package service

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"
)

const (
	// accountPerformanceStatsWindow 性能统计窗口：最近 30 分钟。
	accountPerformanceStatsWindow = 30 * time.Minute
	// accountPerformanceStatsRefreshInterval 缓存刷新间隔。
	accountPerformanceStatsRefreshInterval = 60 * time.Second
	accountPerformanceStatsRefreshTimeout  = 10 * time.Second

	// accountPerfMinSamples 样本数不足时视为无数据（中性），防止单次请求主导排序。
	accountPerfMinSamples = 3
	// accountPerfScoreTolerance 与最优分的容差：容差内候选保留，交由 LRU 打散。
	// 各维度按相对候选内最优账号的比值计分，故 0.15 等价于「加权平均相对性能
	// 不低于最优账号的 85%」，含义不随候选构成变化。
	accountPerfScoreTolerance = 0.15
	// accountPerfTTFTWeight / accountPerfDecodeTPSWeight 性能总分中两个指标的权重。
	accountPerfTTFTWeight      = 0.5
	accountPerfDecodeTPSWeight = 0.5
)

// accountPerfScore 按相对最优基准的比值计算加权性能分：TTFT 取 基准/自身（越低
// 越好），decode tps 取 自身/基准（越高越好）。单项缺失或基准无效（≤ 0）时该维度
// 记满分——缺失不构成差的证据，不应挤占另一维度的容差。
func accountPerfScore(ttft, tps *float64, ttftBest, tpsBest float64) float64 {
	normTTFT := 1.0
	if ttft != nil && ttftBest > 0 {
		normTTFT = ttftBest / *ttft
	}
	normTPS := 1.0
	if tps != nil && tpsBest > 0 {
		normTPS = *tps / tpsBest
	}
	return accountPerfTTFTWeight*normTTFT + accountPerfDecodeTPSWeight*normTPS
}

// AccountPerfWindowRow 是 usage_logs 窗口聚合的原始行（repository 层扫描用），
// 每个 (账号, 映射后的上游模型) 一行。
type AccountPerfWindowRow struct {
	AccountID       int64
	Model           string
	SampleCount     int64
	AvgTTFTMs       *float64
	TtftCount       int64
	SumOutputTokens int64
	SumDecodeMs     float64
}

// AccountPerfWindowStats 是一次窗口聚合的完整结果：各 (账号, 模型) 行 +
// 池内每模型请求级 TTFT P95（慢惩罚判定基线）。
type AccountPerfWindowStats struct {
	Rows          []AccountPerfWindowRow
	PoolTTFTP95Ms map[string]float64
}

// AccountPerformanceStats 是账号在统计窗口内的性能指标。
type AccountPerformanceStats struct {
	AvgTTFTMs    *float64 `json:"avg_ttft_ms"`
	AvgDecodeTps *float64 `json:"avg_decode_tps"`
	SampleCount  int64    `json:"sample_count"`
	// Score 相对窗口内全站最优账号的加权性能分，与调度联合评分中性能分成分
	// 同公式（联合评分另乘负载折扣与慢惩罚两个瞬态因子），样本不足不计分，
	// 仅账号级聚合视图填充，供管理端展示与排序。
	Score *float64 `json:"score,omitempty"`
	// SlowPenalty 账号任一模型维度当前处于慢惩罚降权期；SlowPenaltyUntil 为
	// 其中最晚的自动解除时间。Snapshot 拷贝时实时查询填充，供管理端展示。
	SlowPenalty      bool       `json:"slow_penalty,omitempty"`
	SlowPenaltyUntil *time.Time `json:"slow_penalty_until,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// accountPerfStatsRepo 只依赖窗口聚合查询，便于测试替身。
type accountPerfStatsRepo interface {
	GetAccountPerformanceWindowStats(ctx context.Context, since time.Time) (*AccountPerfWindowStats, error)
}

// SlowPenaltyConfig 慢请求快速降权配置。30 分钟统计窗口对账号突发劣化存在
// 滞后（差数据要稀释旧好数据后才反映），本机制在请求完成时实时判定：
// 连续 N 条 TTFT 超过阈值即对该账号性能分乘 Factor，持续 Duration 后自动解除。
type SlowPenaltyConfig struct {
	Enabled         bool
	Consecutive     int
	ThresholdFactor float64 // 阈值 = 模型池内请求级 P95 × Factor
	MinThresholdMs  int     // 阈值绝对下限
	SelfFactor      float64 // 额外要求超过账号自身窗口均值 × 此值（消除请求画像偏差）
	Factor          float64 // 惩罚期性能分乘子
	Duration        time.Duration
	now             func() time.Time // 测试注入
}

// AccountPerformanceStatsService 维护各账号近 30 分钟性能指标的进程内缓存，
// 由后台定时任务从 usage_logs 聚合刷新；只读不写库，多实例各自刷新即可。
// 调度评分按 (账号, 映射后的上游模型) 维度查询，避免不同模型的请求混在一个
// 账号分里造成模型组合差异被误读为账号性能差异；Snapshot 提供跨模型聚合的
// 账号级视图仅供管理端展示。
type AccountPerformanceStatsService struct {
	repo      accountPerfStatsRepo
	slowCfg   SlowPenaltyConfig
	timeNow   func() time.Time
	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	mu        sync.RWMutex
	byAccount map[int64]*AccountPerformanceStats
	perModel  map[string]map[int64]*AccountPerformanceStats

	// penMu 保护慢惩罚状态；与 mu 独立，避免调度热路径读惩罚时阻塞统计刷新。
	penMu        sync.Mutex
	poolP95      map[string]float64
	slowStreak   map[accountSlowKey]int
	penaltyUntil map[accountSlowKey]time.Time
}

type accountSlowKey struct {
	AccountID int64
	Model     string
}

func NewAccountPerformanceStatsService(repo accountPerfStatsRepo, slowCfg SlowPenaltyConfig) *AccountPerformanceStatsService {
	timeNow := slowCfg.now
	if timeNow == nil {
		timeNow = time.Now
	}
	return &AccountPerformanceStatsService{
		repo:         repo,
		slowCfg:      slowCfg,
		timeNow:      timeNow,
		poolP95:      make(map[string]float64),
		slowStreak:   make(map[accountSlowKey]int),
		penaltyUntil: make(map[accountSlowKey]time.Time),
	}
}

func (s *AccountPerformanceStatsService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		if s.stopCh == nil {
			s.stopCh = make(chan struct{})
		}
		go s.run()
	})
}

func (s *AccountPerformanceStatsService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
}

func (s *AccountPerformanceStatsService) run() {
	s.refreshOnce()

	ticker := time.NewTicker(accountPerformanceStatsRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.refreshOnce()
		case <-s.stopCh:
			return
		}
	}
}

// Get 返回账号在指定上游模型维度下的窗口性能指标；无该模型数据（含
// upstreamModel 为空、窗口内样本滑出）时返回 nil，调度侧按中性处理。
func (s *AccountPerformanceStatsService) Get(accountID int64, upstreamModel string) *AccountPerformanceStats {
	if s == nil || upstreamModel == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.perModel[upstreamModel][accountID]
}

// Snapshot 返回跨模型聚合的账号级指标拷贝，供管理端展示。慢惩罚状态随请求
// 实时变化、不随窗口刷新周期，拷贝时实时查询填充。
func (s *AccountPerformanceStatsService) Snapshot() map[int64]*AccountPerformanceStats {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	out := make(map[int64]*AccountPerformanceStats, len(s.byAccount))
	for id, st := range s.byAccount {
		copied := *st
		out[id] = &copied
	}
	s.mu.RUnlock()

	if !s.slowCfg.Enabled {
		return out
	}
	now := s.timeNow()
	s.penMu.Lock()
	for key, until := range s.penaltyUntil {
		if !now.Before(until) {
			delete(s.penaltyUntil, key)
			continue
		}
		if st := out[key.AccountID]; st != nil && (st.SlowPenaltyUntil == nil || until.After(*st.SlowPenaltyUntil)) {
			st.SlowPenalty = true
			st.SlowPenaltyUntil = &until
		}
	}
	s.penMu.Unlock()
	return out
}

// ObserveTTFT 在请求完成计费时上报 TTFT，驱动慢惩罚判定。判定维度与调度评分
// 一致：(账号, 映射后的上游模型)。线程安全；可在计费 worker 中直接调用。
func (s *AccountPerformanceStatsService) ObserveTTFT(accountID int64, model string, ttftMs int64) {
	if s == nil || !s.slowCfg.Enabled || model == "" || ttftMs <= 0 {
		return
	}

	s.penMu.Lock()
	p95 := s.poolP95[model]
	s.penMu.Unlock()
	if p95 <= 0 {
		return
	}

	// 自身窗口均值条件：池基线对长上下文等画像天然偏高，叠加自身相对条件
	// 才能把「账号劣化」与「请求天然慢」区分开；无自身数据时仅按池基线判定。
	var selfAvg *float64
	if stats := s.Get(accountID, model); stats != nil {
		selfAvg = stats.AvgTTFTMs
	}
	threshold := p95 * s.slowCfg.ThresholdFactor
	if min := float64(s.slowCfg.MinThresholdMs); threshold < min {
		threshold = min
	}
	slow := float64(ttftMs) > threshold
	if slow && selfAvg != nil && *selfAvg > 0 && s.slowCfg.SelfFactor > 1 {
		slow = float64(ttftMs) > *selfAvg*s.slowCfg.SelfFactor
	}

	key := accountSlowKey{AccountID: accountID, Model: model}
	now := s.timeNow()
	s.penMu.Lock()
	defer s.penMu.Unlock()
	if !slow {
		delete(s.slowStreak, key)
		return
	}
	s.slowStreak[key]++
	if s.slowStreak[key] >= s.slowCfg.Consecutive {
		s.penaltyUntil[key] = now.Add(s.slowCfg.Duration)
		delete(s.slowStreak, key)
	}
}

// PenaltyFactor 返回 (账号, 模型) 当前的慢惩罚乘子：惩罚期内为配置的 Factor，
// 否则 1.0。调度联合评分将它乘到性能分上。
func (s *AccountPerformanceStatsService) PenaltyFactor(accountID int64, model string) float64 {
	if s == nil || !s.slowCfg.Enabled || model == "" {
		return 1.0
	}
	key := accountSlowKey{AccountID: accountID, Model: model}
	s.penMu.Lock()
	defer s.penMu.Unlock()
	until, ok := s.penaltyUntil[key]
	if !ok {
		return 1.0
	}
	if !s.timeNow().Before(until) {
		delete(s.penaltyUntil, key)
		return 1.0
	}
	return s.slowCfg.Factor
}

// refreshOnce 聚合最近窗口数据并整体替换缓存；失败时保留旧数据。
func (s *AccountPerformanceStatsService) refreshOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), accountPerformanceStatsRefreshTimeout)
	defer cancel()

	window, err := s.repo.GetAccountPerformanceWindowStats(ctx, time.Now().Add(-accountPerformanceStatsWindow))
	if err != nil {
		slog.Warn("account_performance_stats_refresh_failed", "error", err)
		return
	}
	rows := window.Rows

	now := time.Now()
	nextModel := make(map[string]map[int64]*AccountPerformanceStats)
	type accountAgg struct {
		sampleCount, ttftCount int64
		ttftSum                float64
		sumTokens              int64
		sumDecodeMs            float64
	}
	aggs := make(map[int64]*accountAgg, len(rows))

	for _, row := range rows {
		stats := &AccountPerformanceStats{
			SampleCount: row.SampleCount,
		}
		if row.AvgTTFTMs != nil {
			ttft := *row.AvgTTFTMs
			stats.AvgTTFTMs = &ttft
		}
		if row.SumDecodeMs > 0 && row.SumOutputTokens > 0 {
			tps := float64(row.SumOutputTokens) * 1000.0 / row.SumDecodeMs
			stats.AvgDecodeTps = &tps
		}
		if stats.AvgTTFTMs != nil || stats.AvgDecodeTps != nil {
			stats.UpdatedAt = now
			if nextModel[row.Model] == nil {
				nextModel[row.Model] = make(map[int64]*AccountPerformanceStats)
			}
			nextModel[row.Model][row.AccountID] = stats
		}

		agg := aggs[row.AccountID]
		if agg == nil {
			agg = &accountAgg{}
			aggs[row.AccountID] = agg
		}
		agg.sampleCount += row.SampleCount
		agg.sumTokens += row.SumOutputTokens
		agg.sumDecodeMs += row.SumDecodeMs
		if row.AvgTTFTMs != nil {
			agg.ttftSum += *row.AvgTTFTMs * float64(row.TtftCount)
			agg.ttftCount += row.TtftCount
		}
	}

	nextAccount := make(map[int64]*AccountPerformanceStats, len(aggs))
	for id, agg := range aggs {
		stats := &AccountPerformanceStats{
			SampleCount: agg.sampleCount,
		}
		if agg.ttftCount > 0 {
			ttft := agg.ttftSum / float64(agg.ttftCount)
			stats.AvgTTFTMs = &ttft
		}
		if agg.sumDecodeMs > 0 && agg.sumTokens > 0 {
			tps := float64(agg.sumTokens) * 1000.0 / agg.sumDecodeMs
			stats.AvgDecodeTps = &tps
		}
		if stats.AvgTTFTMs != nil || stats.AvgDecodeTps != nil {
			stats.UpdatedAt = now
			nextAccount[id] = stats
		}
	}

	// 账号级得分：公式与调度联合评分的性能分成分相同（联合评分另乘负载折扣与
	// 慢惩罚两个瞬态因子），基准取窗口内全站最优（跨模型聚合指标），样本不足时
	// 不计分。调度侧实际比较范围是当前请求映射后模型维度的候选内，此分仅供
	// 管理端排序参考，不参与调度。
	ttftBest, tpsBest := math.Inf(1), 0.0
	for _, stats := range nextAccount {
		if stats.SampleCount < accountPerfMinSamples {
			continue
		}
		if stats.AvgTTFTMs != nil && *stats.AvgTTFTMs < ttftBest {
			ttftBest = *stats.AvgTTFTMs
		}
		if stats.AvgDecodeTps != nil && *stats.AvgDecodeTps > tpsBest {
			tpsBest = *stats.AvgDecodeTps
		}
	}
	for _, stats := range nextAccount {
		if stats.SampleCount < accountPerfMinSamples {
			continue
		}
		score := accountPerfScore(stats.AvgTTFTMs, stats.AvgDecodeTps, ttftBest, tpsBest)
		stats.Score = &score
	}

	s.mu.Lock()
	s.byAccount = nextAccount
	s.perModel = nextModel
	s.mu.Unlock()

	s.penMu.Lock()
	s.poolP95 = window.PoolTTFTP95Ms
	s.penMu.Unlock()
}
