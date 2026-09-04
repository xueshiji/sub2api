package service

import (
	"context"
	"log/slog"
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

// AccountPerformanceStats 是账号在统计窗口内的性能指标。
type AccountPerformanceStats struct {
	AvgTTFTMs    *float64  `json:"avg_ttft_ms"`
	AvgDecodeTps *float64  `json:"avg_decode_tps"`
	SampleCount  int64     `json:"sample_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// accountPerfStatsRepo 只依赖窗口聚合查询，便于测试替身。
type accountPerfStatsRepo interface {
	GetAccountPerformanceWindowStats(ctx context.Context, since time.Time) ([]AccountPerfWindowRow, error)
}

// AccountPerformanceStatsService 维护各账号近 30 分钟性能指标的进程内缓存，
// 由后台定时任务从 usage_logs 聚合刷新；只读不写库，多实例各自刷新即可。
// 调度评分按 (账号, 映射后的上游模型) 维度查询，避免不同模型的请求混在一个
// 账号分里造成模型组合差异被误读为账号性能差异；Snapshot 提供跨模型聚合的
// 账号级视图仅供管理端展示。
type AccountPerformanceStatsService struct {
	repo accountPerfStatsRepo

	mu        sync.RWMutex
	byAccount map[int64]*AccountPerformanceStats
	perModel  map[string]map[int64]*AccountPerformanceStats

	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewAccountPerformanceStatsService(repo accountPerfStatsRepo) *AccountPerformanceStatsService {
	return &AccountPerformanceStatsService{
		repo: repo,
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

// Snapshot 返回跨模型聚合的账号级指标拷贝，供管理端展示。
func (s *AccountPerformanceStatsService) Snapshot() map[int64]*AccountPerformanceStats {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64]*AccountPerformanceStats, len(s.byAccount))
	for id, st := range s.byAccount {
		copied := *st
		out[id] = &copied
	}
	return out
}

// refreshOnce 聚合最近窗口数据并整体替换缓存；失败时保留旧数据。
func (s *AccountPerformanceStatsService) refreshOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), accountPerformanceStatsRefreshTimeout)
	defer cancel()

	rows, err := s.repo.GetAccountPerformanceWindowStats(ctx, time.Now().Add(-accountPerformanceStatsWindow))
	if err != nil {
		slog.Warn("account_performance_stats_refresh_failed", "error", err)
		return
	}

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

	s.mu.Lock()
	s.byAccount = nextAccount
	s.perModel = nextModel
	s.mu.Unlock()
}
