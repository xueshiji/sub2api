//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLog_GetAccountPerformanceWindowStats(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)

	user := mustCreateUser(t, client, &service.User{Email: "perf-stats@test.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-perf-stats", Name: "perf-stats"})
	accountA := mustCreateAccount(t, client, &service.Account{Name: "perf-account-a"})
	accountB := mustCreateAccount(t, client, &service.Account{Name: "perf-account-b"})

	intPtr := func(v int) *int { return &v }
	strPtr := func(v string) *string { return &v }
	now := time.Now().UTC()

	insert := func(accountID int64, createdAt time.Time, upstreamModel string, stream bool, durationMs, firstTokenMs *int, outputTokens int) {
		_, err := repo.Create(ctx, &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: accountID,
			Model: "claude-test", RequestedModel: "claude-test", UpstreamModel: strPtr(upstreamModel),
			Stream:     stream,
			DurationMs: durationMs, FirstTokenMs: firstTokenMs,
			InputTokens: 1, OutputTokens: outputTokens,
			CreatedAt: createdAt,
		})
		require.NoError(t, err)
	}

	// 账号 A（窗口内，upstream-model-a）：
	insert(accountA.ID, now, "upstream-model-a", true, intPtr(10000), intPtr(2000), 100) // decode 8000ms, tokens 100
	insert(accountA.ID, now, "upstream-model-a", true, intPtr(500), intPtr(800), 50)     // duration <= first_token：decode 无效，TTFT 仍有效
	insert(accountA.ID, now, "upstream-model-a", false, intPtr(6000), intPtr(900), 300)  // 非流式：TTFT 有效，decode 不计（口径只统计流式）
	insert(accountA.ID, now, "upstream-model-a", false, intPtr(1000), nil, 0)            // 非流式无 TTFT：decode 不计，无任何有效指标
	// 账号 A（窗口内，upstream-model-b）：
	insert(accountA.ID, now, "upstream-model-b", true, intPtr(11000), intPtr(1000), 200) // decode 10000ms, tokens 200
	insert(accountA.ID, now, "upstream-model-b", false, intPtr(2000), nil, 10)           // 非流式无 TTFT：不参与任何聚合
	// 窗口外：
	insert(accountA.ID, now.Add(-2*time.Hour), "upstream-model-a", true, intPtr(9000), intPtr(1000), 999)
	// upstream_model 为空的历史行：回落 requested_model 桶
	_, err := repo.Create(ctx, &service.UsageLog{
		UserID: user.ID, APIKeyID: apiKey.ID, AccountID: accountA.ID,
		Model: "claude-test", RequestedModel: "claude-test", UpstreamModel: nil,
		Stream: true, DurationMs: intPtr(3000), FirstTokenMs: intPtr(500), InputTokens: 1, OutputTokens: 60,
		CreatedAt: now,
	})
	require.NoError(t, err)
	// 账号 B（窗口内，upstream-model-a）：
	insert(accountB.ID, now, "upstream-model-a", true, intPtr(4000), intPtr(400), 80) // decode 3600ms, tokens 80

	window, err := repo.GetAccountPerformanceWindowStats(ctx, now.Add(-30*time.Minute))
	require.NoError(t, err)
	rows := window.Rows

	byKey := make(map[string]service.AccountPerfWindowRow, len(rows))
	for _, row := range rows {
		byKey[fmt.Sprintf("%d:%s", row.AccountID, row.Model)] = row
	}

	rowA, ok := byKey[fmt.Sprintf("%d:upstream-model-a", accountA.ID)]
	require.True(t, ok, "account A model-a should be aggregated")
	require.Equal(t, int64(3), rowA.SampleCount) // 非流式带 TTFT 的行计入样本；无 TTFT 且无 decode 的行不计
	require.NotNil(t, rowA.AvgTTFTMs)
	require.InDelta(t, float64(2000+800+900)/3, *rowA.AvgTTFTMs, 0.01)
	require.Equal(t, int64(3), rowA.TtftCount)
	require.Equal(t, int64(100), rowA.SumOutputTokens) // 非流式行的 output 不进 decode 分子
	require.InDelta(t, float64(8000), rowA.SumDecodeMs, 0.01)

	rowAB, ok := byKey[fmt.Sprintf("%d:upstream-model-b", accountA.ID)]
	require.True(t, ok, "account A model-b should be aggregated separately")
	require.Equal(t, int64(1), rowAB.SampleCount)
	require.NotNil(t, rowAB.AvgTTFTMs)
	require.InDelta(t, float64(1000), *rowAB.AvgTTFTMs, 0.01)
	require.Equal(t, int64(200), rowAB.SumOutputTokens)
	require.InDelta(t, float64(10000), rowAB.SumDecodeMs, 0.01)

	rowB, ok := byKey[fmt.Sprintf("%d:upstream-model-a", accountB.ID)]
	require.True(t, ok, "account B should be aggregated")
	require.Equal(t, int64(1), rowB.SampleCount)
	require.NotNil(t, rowB.AvgTTFTMs)
	require.InDelta(t, float64(400), *rowB.AvgTTFTMs, 0.01)
	require.Equal(t, int64(80), rowB.SumOutputTokens)
	require.InDelta(t, float64(3600), rowB.SumDecodeMs, 0.01)

	// upstream_model 为空的行回落 requested_model 桶
	rowFallback, ok := byKey[fmt.Sprintf("%d:claude-test", accountA.ID)]
	require.True(t, ok, "rows without upstream_model should fall back to requested_model")
	require.Equal(t, int64(1), rowFallback.SampleCount)
	require.Equal(t, int64(60), rowFallback.SumOutputTokens)
	require.InDelta(t, float64(2500), rowFallback.SumDecodeMs, 0.01)

	// 池内每模型请求级 TTFT P50/P95（线性插值）：model-a 窗口内样本 [400, 800, 900, 2000]，
	// P95 位置 (4-1)*0.95=2.85 → 900 + 0.85*(2000-900) = 1835；
	// P50 位置 1.5 → 800 + 0.5*(900-800) = 850；单样本模型分位即该值。
	require.InDelta(t, float64(850), window.PoolTTFTP50Ms["upstream-model-a"], 0.01)
	require.InDelta(t, float64(1835), window.PoolTTFTP95Ms["upstream-model-a"], 0.01)
	require.InDelta(t, float64(1000), window.PoolTTFTP50Ms["upstream-model-b"], 0.01)
	require.InDelta(t, float64(1000), window.PoolTTFTP95Ms["upstream-model-b"], 0.01)
	require.InDelta(t, float64(500), window.PoolTTFTP50Ms["claude-test"], 0.01)
	require.InDelta(t, float64(500), window.PoolTTFTP95Ms["claude-test"], 0.01)
}
