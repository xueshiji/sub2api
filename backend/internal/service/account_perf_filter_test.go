//go:build unit

package service

import (
	"testing"
)

func perfFilterCandidates(ids ...int64) []accountWithLoad {
	out := make([]accountWithLoad, 0, len(ids))
	for _, id := range ids {
		out = append(out, accountWithLoad{
			account:  &Account{ID: id},
			loadInfo: &AccountLoadInfo{AccountID: id},
		})
	}
	return out
}

func perfFilterIDs(accounts []accountWithLoad) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, acc := range accounts {
		ids = append(ids, acc.account.ID)
	}
	return ids
}

func perfFloatPtr(v float64) *float64 { return &v }

func perfFilterLookup(stats map[int64]*AccountPerformanceStats) func(*Account) *AccountPerformanceStats {
	return func(acc *Account) *AccountPerformanceStats { return stats[acc.ID] }
}

func TestFilterByBestPerformanceEmptyOrSingleCandidatePassesThrough(t *testing.T) {
	lookup := func(*Account) *AccountPerformanceStats { return nil }

	if got := filterByBestPerformance(nil, lookup); len(got) != 0 {
		t.Fatalf("empty input should stay empty, got %v", got)
	}

	in := perfFilterCandidates(1)
	got := filterByBestPerformance(in, lookup)
	if len(got) != 1 || got[0].account.ID != 1 {
		t.Fatalf("single candidate should pass through, got %v", perfFilterIDs(got))
	}
}

func TestFilterByBestPerformanceAllWithoutDataPassesThrough(t *testing.T) {
	lookup := func(*Account) *AccountPerformanceStats { return nil }
	in := perfFilterCandidates(1, 2, 3)

	got := filterByBestPerformance(in, lookup)
	if len(got) != 3 {
		t.Fatalf("candidates without window data should all stay, got %v", perfFilterIDs(got))
	}
}

func TestFilterByBestPerformanceKeepsNoDataAndDropsOutlier(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(500), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(10), SampleCount: 10},
	}
	in := perfFilterCandidates(1, 2, 3)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("no-data account should stay and poor performer should drop, got %v", got)
	}
}

func TestFilterByBestPerformanceKeepsCandidatesWithinTolerance(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(500), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(540), AvgDecodeTps: perfFloatPtr(47), SampleCount: 10},
		4: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(10), SampleCount: 10},
	}
	in := perfFilterCandidates(2, 3, 4)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("candidates within tolerance should stay, got %v", got)
	}
}

func TestFilterByBestPerformanceNearIdenticalMetricsKeepAll(t *testing.T) {
	// 801ms 相对 800ms 差距不足 0.2%：与最优等价，不应被放大成满分差距而剔除。
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(801), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
	}
	in := perfFilterCandidates(2, 3)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 {
		t.Fatalf("candidates with near-identical metrics should all stay, got %v", got)
	}
}

func TestFilterByBestPerformanceScoreIndependentOfCandidateSpread(t *testing.T) {
	// 账号 3 相对最优的差距固定：加入更差的账号 4 不应稀释该差距而让 3 躲过剔除。
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(500), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(1000), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
		4: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(10), SampleCount: 10},
	}

	gotTwo := perfFilterIDs(filterByBestPerformance(perfFilterCandidates(2, 3), perfFilterLookup(stats)))
	if len(gotTwo) != 1 || gotTwo[0] != 2 {
		t.Fatalf("mid candidate should drop without the worst candidate present, got %v", gotTwo)
	}

	gotThree := perfFilterIDs(filterByBestPerformance(perfFilterCandidates(2, 3, 4), perfFilterLookup(stats)))
	if len(gotThree) != 1 || gotThree[0] != 2 {
		t.Fatalf("mid candidate should drop regardless of a worse candidate joining, got %v", gotThree)
	}
}

func TestFilterByBestPerformanceTreatsLowSampleCountAsNeutral(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(500), AvgDecodeTps: perfFloatPtr(50), SampleCount: 2},
		3: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(10), SampleCount: 10},
	}
	in := perfFilterCandidates(2, 3)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 {
		t.Fatalf("low-sample account should be treated as neutral and stay, got %v", got)
	}
}

func TestFilterByBestPerformanceMissingMetricDoesNotPenalize(t *testing.T) {
	// 账号 2 缺 TPS 指标：TTFT 与最优持平，TPS 缺失按该维度满分计，
	// 不因缺数据被拖入与最优的分差。
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(500), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(500), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
		4: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
	}
	in := perfFilterCandidates(2, 3, 4)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("missing metric should count as best in that dimension, got %v", got)
	}
}

func TestFilterByBestPerformanceEqualMetricsKeepAll(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
	}
	in := perfFilterCandidates(2, 3)

	got := perfFilterIDs(filterByBestPerformance(in, perfFilterLookup(stats)))
	if len(got) != 2 {
		t.Fatalf("candidates with identical metrics should all stay, got %v", got)
	}
}

func perfJointCandidates(loadRates map[int64]int, ids ...int64) []accountWithLoad {
	out := make([]accountWithLoad, 0, len(ids))
	for _, id := range ids {
		out = append(out, accountWithLoad{
			account:  &Account{ID: id},
			loadInfo: &AccountLoadInfo{AccountID: id, LoadRate: loadRates[id]},
		})
	}
	return out
}

func noPenalty(*Account) float64 { return 1.0 }

func TestFilterByJointPerformanceSlopeNonPositivePassesThrough(t *testing.T) {
	in := perfJointCandidates(map[int64]int{1: 0, 2: 50}, 1, 2)
	if got := filterByJointPerformance(in, 0, perfFilterLookup(nil), noPenalty); len(got) != 2 {
		t.Fatalf("slope<=0 should pass through, got %v", perfFilterIDs(got))
	}
	if got := filterByJointPerformance(nil, 0.5, perfFilterLookup(nil), noPenalty); len(got) != 0 {
		t.Fatalf("empty input should stay empty, got %v", perfFilterIDs(got))
	}
	if got := filterByJointPerformance(in[:1], 0.5, perfFilterLookup(nil), noPenalty); len(got) != 1 {
		t.Fatalf("single candidate should pass through, got %v", perfFilterIDs(got))
	}
}

func TestFilterByJointPerformanceFastLoadedAccountBeatsSlowIdleAccount(t *testing.T) {
	// 联合评分的核心语义：高负载但性能好的账号可以胜过低负载但性能差的账号，
	// 不再被「最低负载率并列集」硬过滤截断。
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(400), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},  // 快，负载 67%
		3: {AvgTTFTMs: perfFloatPtr(5000), AvgDecodeTps: perfFloatPtr(10), SampleCount: 10}, // 慢，负载 0%
	}
	in := perfJointCandidates(map[int64]int{2: 67, 3: 0}, 2, 3)

	got := perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(stats), noPenalty))
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("fast loaded account should beat slow idle account, got %v", got)
	}
}

func TestFilterByJointPerformanceSamePerformancePrefersLowerLoad(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
	}
	// slope 0.4：负载差 33pp → 分差 0.132 < 容差 0.15，两者都保留交 LRU；
	// 负载差 67pp → 分差 0.268 > 容差，仅低负载者保留。
	in := perfJointCandidates(map[int64]int{2: 33, 3: 0}, 2, 3)
	got := perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(stats), noPenalty))
	if len(got) != 2 {
		t.Fatalf("load gap within tolerance should keep both for LRU, got %v", got)
	}

	in = perfJointCandidates(map[int64]int{2: 67, 3: 0}, 2, 3)
	got = perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(stats), noPenalty))
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("large load gap should keep only the idle account, got %v", got)
	}
}

func TestFilterByJointPerformanceNoDataIdleAccountStaysNeutral(t *testing.T) {
	// 无数据 + 零负载视为中性满分：空闲账号不应被挤掉，由 LRU 复探。
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(400), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
	}
	in := perfJointCandidates(map[int64]int{2: 0, 3: 0}, 2, 3)

	got := perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(stats), noPenalty))
	if len(got) != 2 {
		t.Fatalf("no-data idle account should stay neutral, got %v", got)
	}
}

func TestFilterByJointPerformancePenaltyPushesAccountOutOfTolerance(t *testing.T) {
	stats := map[int64]*AccountPerformanceStats{
		2: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
		3: {AvgTTFTMs: perfFloatPtr(800), AvgDecodeTps: perfFloatPtr(40), SampleCount: 10},
	}
	in := perfJointCandidates(map[int64]int{2: 0, 3: 0}, 2, 3)
	penalty := func(acc *Account) float64 {
		if acc.ID == 2 {
			return 0.5
		}
		return 1.0
	}

	got := perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(stats), penalty))
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("penalized account should drop out of tolerance, got %v", got)
	}
}

func TestFilterByJointPerformanceAllWithoutDataFallsBackToLoadDiscount(t *testing.T) {
	// 全部无数据：联合分退化为负载折扣，低负载者胜出（与旧 min-load 方向一致）。
	in := perfJointCandidates(map[int64]int{2: 67, 3: 0}, 2, 3)

	got := perfFilterIDs(filterByJointPerformance(in, 0.4, perfFilterLookup(nil), noPenalty))
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("all-no-data should prefer idle account via load discount, got %v", got)
	}
}

func TestFilterByJointPerformanceNoDataAccountDoesNotRaiseAnchor(t *testing.T) {
	// 账号 3 是最优数据账号（perf 0.95），账号 4 是中游数据账号（perf 0.833，
	// 距 3 为 0.117 在容差内）。无数据账号 2 共存时锚点仍由 3 定义，4 被保留——
	// 无数据的满分表示「未知」，不抬高及格线挤掉有证据的中游账号。
	stats := map[int64]*AccountPerformanceStats{
		3: {AvgTTFTMs: perfFloatPtr(400), AvgDecodeTps: perfFloatPtr(45), SampleCount: 10},
		4: {AvgTTFTMs: perfFloatPtr(600), AvgDecodeTps: perfFloatPtr(50), SampleCount: 10},
	}
	in := perfJointCandidates(map[int64]int{2: 0, 3: 0, 4: 0}, 2, 3, 4)

	got := perfFilterIDs(filterByJointPerformance(in, 0.5, perfFilterLookup(stats), noPenalty))
	if len(got) != 3 {
		t.Fatalf("no-data account should not push mid-tier data account out of tolerance, got %v", got)
	}
}
