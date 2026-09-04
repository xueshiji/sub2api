//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type perfStatsRepoStub struct {
	rows []AccountPerfWindowRow
	err  error
}

func (r *perfStatsRepoStub) GetAccountPerformanceWindowStats(ctx context.Context, since time.Time) ([]AccountPerfWindowRow, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.rows, nil
}

func TestAccountPerformanceStatsServiceRefreshSwapsSnapshot(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5, SumOutputTokens: 900, SumDecodeMs: 20000},
		{AccountID: 2, Model: "upstream-a", SampleCount: 3},
	}}
	svc := NewAccountPerformanceStatsService(repo)
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
	svc := NewAccountPerformanceStatsService(repo)
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

func TestAccountPerformanceStatsServiceKeepsOldDataOnRefreshFailure(t *testing.T) {
	repo := &perfStatsRepoStub{rows: []AccountPerfWindowRow{
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfFloatPtr(800), TtftCount: 5, SumOutputTokens: 900, SumDecodeMs: 20000},
	}}
	svc := NewAccountPerformanceStatsService(repo)
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
	svc := NewAccountPerformanceStatsService(repo)
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
	svc := NewAccountPerformanceStatsService(repo)
	svc.refreshOnce()

	snapshot := svc.Snapshot()
	snapshot[1].AvgTTFTMs = perfFloatPtr(1)
	delete(snapshot, 1)

	if got := svc.Get(1, "upstream-a"); got == nil || *got.AvgTTFTMs != 800 {
		t.Fatalf("mutating snapshot must not affect service state, got %#v", got)
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
