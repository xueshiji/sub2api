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
