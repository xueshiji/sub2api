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
	insert(accountA.ID, now, "upstream-model-a", false, intPtr(6000), nil, 300)          // 非流式：decode 6000ms, tokens 300
	insert(accountA.ID, now, "upstream-model-a", false, intPtr(1000), nil, 0)            // output 为 0：decode 无效且无 TTFT
	// 账号 A（窗口内，upstream-model-b）：
	insert(accountA.ID, now, "upstream-model-b", true, intPtr(11000), intPtr(1000), 200) // decode 10000ms, tokens 200
	insert(accountA.ID, now, "upstream-model-b", false, intPtr(2000), nil, 10)           // 非流式无 TTFT：decode 2000ms, tokens 10
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
	require.Equal(t, int64(3), rowA.SampleCount) // 4 行中 output=0 的行无任何有效指标
	require.NotNil(t, rowA.AvgTTFTMs)
	require.InDelta(t, float64(2000+800)/2, *rowA.AvgTTFTMs, 0.01)
	require.Equal(t, int64(2), rowA.TtftCount)
	require.Equal(t, int64(400), rowA.SumOutputTokens)
	require.InDelta(t, float64(8000+6000), rowA.SumDecodeMs, 0.01)

	rowAB, ok := byKey[fmt.Sprintf("%d:upstream-model-b", accountA.ID)]
	require.True(t, ok, "account A model-b should be aggregated separately")
	require.Equal(t, int64(2), rowAB.SampleCount)
	require.NotNil(t, rowAB.AvgTTFTMs)
	require.InDelta(t, float64(1000), *rowAB.AvgTTFTMs, 0.01)
	require.Equal(t, int64(210), rowAB.SumOutputTokens)
	require.InDelta(t, float64(10000+2000), rowAB.SumDecodeMs, 0.01)

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

	// 池内每模型请求级 TTFT P95（线性插值）：model-a 窗口内样本 [400, 800, 2000]，
	// 位置 (3-1)*0.95=1.9 → 800 + 0.9*(2000-800) = 1880；单样本模型 P95 即该值。
	require.InDelta(t, float64(1880), window.PoolTTFTP95Ms["upstream-model-a"], 0.01)
	require.InDelta(t, float64(1000), window.PoolTTFTP95Ms["upstream-model-b"], 0.01)
	require.InDelta(t, float64(500), window.PoolTTFTP95Ms["claude-test"], 0.01)
}
