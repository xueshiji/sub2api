//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type perfStatsRepoStub struct {
	rows    []AccountPerfWindowRow
	poolP95 map[string]float64
	err     error
}

func (r *perfStatsRepoStub) GetAccountPerformanceWindowStats(ctx context.Context, since time.Time) (*AccountPerfWindowStats, error) {
	if r.err != nil {
		return nil, r.err
	}
	p95 := r.poolP95
	if p95 == nil {
		p95 = map[string]float64{}
	}
	return &AccountPerfWindowStats{Rows: r.rows, PoolTTFTP95Ms: p95}, nil
}

func TestAccountPerformanceStatsServiceRefreshSwapsSnapshot(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5, SumOutputTokens: 900, SumDecodeMs: 20000},
		{AccountID: 2, Model: "upstream-a", SampleCount: 3},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()

	got := svc.Get(1, "upstream-a")
	if got == nil {
		t.Fatal("account 1 should have stats after refresh")
	}
	if got.AvgTTFTMs == nil || *got.AvgTTFTMs != 800 {
		t.Fatalf("expected avg ttft 800, got %#v", got.AvgTTFTMs)
	}
	if got.AvgDecodeTps == nil || *got.AvgDecodeTps != 45 { // 900*1000/20000
		t.Fatalf("expected decode tps 45, got %#v", got.AvgDecodeTps)
	}
	if svc.Get(1, "upstream-b") != nil {
		t.Fatal("account 1 has no stats under another upstream model")
	}
	if svc.Get(1, "") != nil {
		t.Fatal("empty upstream model should return nil")
	}
	// 无有效指标的账号不进入缓存
	if svc.Get(2, "upstream-a") != nil {
		t.Fatal("account without ttft/decode metrics should return nil")
	}
}

func TestAccountPerformanceStatsServiceSnapshotAggregatesAcrossModels(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		// upstream-a：TTFT 均值 600ms（2 样本），decode 1000 tokens / 20000ms
		{AccountID: 1, Model: "upstream-a", SampleCount: 2, AvgTTFTMs: perfFloatPtr(600), TtftCount: 2, SumOutputTokens: 1000, SumDecodeMs: 20000},
		// upstream-b：TTFT 均值 1200ms（1 样本），decode 500 tokens / 5000ms
		{AccountID: 1, Model: "upstream-b", SampleCount: 1, AvgTTFTMs: perfFloatPtr(1200), TtftCount: 1, SumOutputTokens: 500, SumDecodeMs: 5000},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()

	snapshot := svc.Snapshot()
	st, ok := snapshot[1]
	if !ok {
		t.Fatal("account-level snapshot should exist")
	}
	if st.AvgTTFTMs == nil || *st.AvgTTFTMs != 800 { // (600*2 + 1200*1) / 3
		t.Fatalf("expected aggregated avg ttft 800, got %#v", st.AvgTTFTMs)
	}
	if st.AvgDecodeTps == nil || *st.AvgDecodeTps != 60 { // 1500*1000/25000
		t.Fatalf("expected aggregated decode tps 60, got %#v", st.AvgDecodeTps)
	}
	if st.SampleCount != 3 {
		t.Fatalf("expected sample count 3, got %d", st.SampleCount)
	}
}

func TestAccountPerformanceStatsServiceSnapshotScore(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		// 账号 1：全站最优（TTFT 500ms、TPS 50），得分 1.0
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(500), TtftCount: 5, SumOutputTokens: 100, SumDecodeMs: 2000},
		// 账号 2：TTFT 慢一倍、TPS 持平，得分 0.5*(500/1000) + 0.5*1 = 0.75
		{AccountID: 2, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(1000), TtftCount: 5, SumOutputTokens: 100, SumDecodeMs: 2000},
		// 账号 3：样本不足，不计分
		{AccountID: 3, Model: "upstream-a", SampleCount: 2, AvgTTFTMs: perfFloatPtr(800), TtftCount: 2, SumOutputTokens: 100, SumDecodeMs: 2000},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()

	snapshot := svc.Snapshot()
	if st := snapshot[1]; st.Score == nil || *st.Score != 1.0 {
		t.Fatalf("expected best account score 1.0, got %#v", st.Score)
	}
	if st := snapshot[2]; st.Score == nil || *st.Score != 0.75 {
		t.Fatalf("expected slower account score 0.75, got %#v", st.Score)
	}
	if st := snapshot[3]; st.Score != nil {
		t.Fatalf("low-sample account should not have a score, got %#v", st.Score)
	}
	// 调度侧的模型维度视图不计分
	if st := svc.Get(1, "upstream-a"); st.Score != nil {
		t.Fatalf("per-model stats should not carry a score, got %#v", st.Score)
	}
}

func TestAccountPerformanceStatsServiceKeepsOldDataOnRefreshFailure(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5, SumOutputTokens: 900, SumDecodeMs: 20000},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()

	repo.err = errors.New("db down")
	svc.refreshOnce()

	if svc.Get(1, "upstream-a") == nil {
		t.Fatal("previous stats should survive a failed refresh")
	}
}

func TestAccountPerformanceStatsServiceDroppedAccountsDisappearAfterSwap(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800)},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()
	if svc.Get(1, "upstream-a") == nil {
		t.Fatal("account 1 should have stats after first refresh")
	}

	// 窗口滑出：聚合结果中不再包含账号 1
	repo.rows = nil
	svc.refreshOnce()
	if svc.Get(1, "upstream-a") != nil {
		t.Fatal("account 1 should disappear after window slides out")
	}
}

func TestAccountPerformanceStatsServiceSnapshotIsACopy(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5},
	}}
	svc := NewAccountPerformanceStatsService(repo, SlowPenaltyConfig{})
	svc.refreshOnce()

	snapshot := svc.Snapshot()
	snapshot[1].AvgTTFTMs = perfFloatPtr(1)
	delete(snapshot, 1)

	if got := svc.Get(1, "upstream-a"); got == nil || *got.AvgTTFTMs != 800 {
		t.Fatalf("mutating snapshot must not affect service state, got %#v", got)
	}
}

func slowPenaltyTestService(t *testing.T, repo *perfStatsRepoStub, cfg SlowPenaltyConfig) (*AccountPerformanceStatsService, *time.Time) {
	t.Helper()
	now := time.Now()
	cfg.now = func() time.Time { return now }
	svc := NewAccountPerformanceStatsService(repo, cfg)
	svc.refreshOnce()
	return svc, &now
}

func slowPenaltyBaseCfg() SlowPenaltyConfig {
	return SlowPenaltyConfig{
		Enabled:         true,
		Consecutive:     3,
		ThresholdFactor: 1.0,
		MinThresholdMs:  0,
		Factor:          0.5,
		Duration:        10 * time.Minute,
	}
}

func TestAccountSlowPenaltyTriggersAfterConsecutiveSlowRequests(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows:    []AccountPerfWindowRow{},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	svc, _ := slowPenaltyTestService(t, repo, slowPenaltyBaseCfg())

	svc.ObserveTTFT(1, "upstream-a", 1200)
	svc.ObserveTTFT(1, "upstream-a", 1200)
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("two slow requests should not trigger penalty, got %v", got)
	}

	svc.ObserveTTFT(1, "upstream-a", 1200)
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 0.5 {
		t.Fatalf("third consecutive slow request should trigger penalty 0.5, got %v", got)
	}
	// 惩罚按 (账号, 模型) 维度隔离
	if got := svc.PenaltyFactor(1, "upstream-b"); got != 1.0 {
		t.Fatalf("other model dimension should stay unpenalized, got %v", got)
	}
	if got := svc.PenaltyFactor(2, "upstream-a"); got != 1.0 {
		t.Fatalf("other account should stay unpenalized, got %v", got)
	}
}

func TestAccountSlowPenaltyStreakResetsOnFastRequest(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows:    []AccountPerfWindowRow{},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	svc, _ := slowPenaltyTestService(t, repo, slowPenaltyBaseCfg())

	svc.ObserveTTFT(1, "upstream-a", 1200)
	svc.ObserveTTFT(1, "upstream-a", 1200)
	svc.ObserveTTFT(1, "upstream-a", 800) // 快请求中断连击
	svc.ObserveTTFT(1, "upstream-a", 1200)
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("interrupted streak should not trigger penalty, got %v", got)
	}
}

func TestAccountSlowPenaltyExpiresAfterDuration(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows:    []AccountPerfWindowRow{},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	cfg := slowPenaltyBaseCfg()
	svc, now := slowPenaltyTestService(t, repo, cfg)

	svc.ObserveTTFT(1, "upstream-a", 1200)
	svc.ObserveTTFT(1, "upstream-a", 1200)
	svc.ObserveTTFT(1, "upstream-a", 1200)
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 0.5 {
		t.Fatalf("penalty should be active, got %v", got)
	}

	*now = now.Add(cfg.Duration + time.Second)
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("penalty should expire after duration, got %v", got)
	}
}

func TestAccountSlowPenaltyRequiresSelfAverageWhenConfigured(t *testing.T) {
	// 账号自身窗口均值 1000ms：ttft 1500 超池基线 1000，但不足自身 2 倍，
	// SelfFactor=2 时不算慢——长上下文画像的账号不应被池基线误伤。
	repo := &perfStatsRepoStub{
		rows: []AccountPerfWindowRow{
			{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(1000), TtftCount: 5},
		},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	cfg := slowPenaltyBaseCfg()
	cfg.SelfFactor = 2.0
	svc, _ := slowPenaltyTestService(t, repo, cfg)

	for i := 0; i < 5; i++ {
		svc.ObserveTTFT(1, "upstream-a", 1500)
	}
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("requests within self-average bound should not trigger penalty, got %v", got)
	}
}

func TestAccountSlowPenaltyMinThreshold(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows:    []AccountPerfWindowRow{},
		poolP95: map[string]float64{"upstream-a": 100},
	}
	cfg := slowPenaltyBaseCfg()
	cfg.MinThresholdMs = 500
	svc, _ := slowPenaltyTestService(t, repo, cfg)

	for i := 0; i < 5; i++ {
		svc.ObserveTTFT(1, "upstream-a", 400) // 超池 P95 但低于绝对下限
	}
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("below absolute floor should not trigger penalty, got %v", got)
	}
}

func TestAccountSlowPenaltyDisabledNeverPenalizes(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows:    []AccountPerfWindowRow{},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	cfg := slowPenaltyBaseCfg()
	cfg.Enabled = false
	svc, _ := slowPenaltyTestService(t, repo, cfg)

	for i := 0; i < 5; i++ {
		svc.ObserveTTFT(1, "upstream-a", 5000)
	}
	if got := svc.PenaltyFactor(1, "upstream-a"); got != 1.0 {
		t.Fatalf("disabled penalty should never trigger, got %v", got)
	}
}

func TestSnapshotCarriesSlowPenaltyState(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows: []AccountPerfWindowRow{
			{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5},
			{AccountID: 2, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5},
		},
		poolP95: map[string]float64{"upstream-a": 1000},
	}
	cfg := slowPenaltyBaseCfg()
	svc, now := slowPenaltyTestService(t, repo, cfg)

	for i := 0; i < 3; i++ {
		svc.ObserveTTFT(1, "upstream-a", 1200)
	}
	st := svc.Snapshot()[1]
	if st == nil || !st.SlowPenalty {
		t.Fatalf("account under penalty should be flagged in snapshot, got %#v", st)
	}
	want := now.Add(cfg.Duration)
	if st.SlowPenaltyUntil == nil || !st.SlowPenaltyUntil.Equal(want) {
		t.Fatalf("expected penalty until %v, got %#v", want, st.SlowPenaltyUntil)
	}
	if other := svc.Snapshot()[2]; other.SlowPenalty {
		t.Fatal("unpenalized account should not be flagged")
	}

	*now = now.Add(cfg.Duration + time.Second)
	st = svc.Snapshot()[1]
	if st.SlowPenalty || st.SlowPenaltyUntil != nil {
		t.Fatalf("expired penalty should not be flagged, got %#v", st)
	}
}

func TestSnapshotSlowPenaltyTakesLatestAcrossModels(t *testing.T) {
	repo := &perfStatsRepoStub{
		rows: []AccountPerfWindowRow{
			{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5},
			{AccountID: 1, Model: "upstream-b", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5},
		},
		poolP95: map[string]float64{"upstream-a": 1000, "upstream-b": 1000},
	}
	cfg := slowPenaltyBaseCfg()
	svc, now := slowPenaltyTestService(t, repo, cfg)

	for i := 0; i < 3; i++ {
		svc.ObserveTTFT(1, "upstream-a", 1200)
	}
	*now = now.Add(time.Minute)
	for i := 0; i < 3; i++ {
		svc.ObserveTTFT(1, "upstream-b", 1200)
	}
	st := svc.Snapshot()[1]
	want := now.Add(cfg.Duration)
	if st == nil || !st.SlowPenalty {
		t.Fatalf("account penalized on one model should be flagged, got %#v", st)
	}
	if st.SlowPenaltyUntil == nil || !st.SlowPenaltyUntil.Equal(want) {
		t.Fatalf("expected latest penalty until %v, got %#v", want, st.SlowPenaltyUntil)
	}
}

func TestResolveAccountUpstreamModel(t *testing.T) {
	s := &GatewayService{}

	t.Run("空请求模型时返回空串", func(t *testing.T) {
		if got := s.resolveAccountUpstreamModel(context.Background(), &Account{Platform: PlatformAnthropic}, "  "); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})

	t.Run("APIKey 账号命中账号级映射时返回映射值", func(t *testing.T) {
		acc := &Account{
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Credentials: map[string]any{"model_mapping": map[string]any{"gpt-x": "gpt-y"}},
		}
		if got := s.resolveAccountUpstreamModel(context.Background(), acc, "gpt-x"); got != "gpt-y" {
			t.Fatalf("expected mapped model gpt-y, got %q", got)
		}
	})

	t.Run("APIKey 账号未配置映射时透传原名", func(t *testing.T) {
		acc := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{}}
		if got := s.resolveAccountUpstreamModel(context.Background(), acc, "gpt-x"); got != "gpt-x" {
			t.Fatalf("expected passthrough model gpt-x, got %q", got)
		}
	})

	t.Run("Anthropic OAuth 账号归一为长 ID", func(t *testing.T) {
		acc := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{}}
		if got := s.resolveAccountUpstreamModel(context.Background(), acc, "claude-sonnet-4-5"); got != "claude-sonnet-4-5-20250929" {
			t.Fatalf("expected normalized long ID, got %q", got)
		}
	})

	t.Run("Antigravity 账号按默认映射解析", func(t *testing.T) {
		acc := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
		if got := s.resolveAccountUpstreamModel(context.Background(), acc, "gemini-3-flash"); got != "gemini-3-flash" {
			t.Fatalf("expected gemini-3-flash, got %q", got)
		}
		if got := s.resolveAccountUpstreamModel(context.Background(), acc, "zz-unknown-model"); got != "" {
			t.Fatalf("expected empty for unmapped model, got %q", got)
		}
	})
}
