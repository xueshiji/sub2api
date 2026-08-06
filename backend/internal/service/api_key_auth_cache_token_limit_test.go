package service

// 认证快照 token 限额字段保真回归：快照构建 → L2 JSON 序列化 → 反序列化
// → 还原 apiKey.Group → 限额判定，全链路不得丢失 daily/weekly/monthly
// limit tokens。任一环节漏列会让 HasDailyTokenLimit() 恒为 false，
// 订阅 token 限额静默失效（2026-08 生产超限事故根因）。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func tokenLimitAuthTestAPIKey() *APIKey {
	groupID := int64(13)
	daily := int64(50_000_000)
	weekly := int64(200_000_000)
	monthly := int64(0)
	return &APIKey{
		ID:      21,
		UserID:  3,
		GroupID: &groupID,
		Name:    "token-limit-roundtrip",
		Status:  StatusActive,
		User: &User{
			ID:          3,
			Email:       "tokenlimit@test.local",
			Status:      StatusActive,
			Concurrency: 5,
		},
		Group: &Group{
			ID:                 groupID,
			Name:               "token-sub-roundtrip",
			Platform:           PlatformOpenAI,
			Status:             StatusActive,
			Hydrated:           true,
			SubscriptionType:   SubscriptionTypeSubscriptionToken,
			DailyLimitTokens:   &daily,
			WeeklyLimitTokens:  &weekly,
			MonthlyLimitTokens: &monthly,
		},
	}
}

// 快照构建 → L2 JSON 往返 → 还原：token 限额字段必须全程保真，
// 还原后的分组必须能通过 token 限额判定入口。
func TestAPIKeyAuthSnapshotTokenLimitRoundtrip(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := tokenLimitAuthTestAPIKey()

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.Group)
	require.NotNil(t, snapshot.Group.DailyLimitTokens)
	require.Equal(t, int64(50_000_000), *snapshot.Group.DailyLimitTokens)
	require.NotNil(t, snapshot.Group.WeeklyLimitTokens)
	require.Equal(t, int64(200_000_000), *snapshot.Group.WeeklyLimitTokens)

	// 模拟 L2 缓存的完整 JSON 往返（与 apiKeyCache.SetAuthCache/GetAuthCache 同构）。
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var restored APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &restored))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &restored)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.IsSubscriptionTokenType())

	// 还原后的分组必须保留 token 限额，否则订阅限额检查（HasDailyTokenLimit
	// 守卫）在缓存命中路径上整体失效。
	require.True(t, materialized.Group.HasDailyTokenLimit(), "日 token 限额在快照往返后丢失，限额检查会被跳过")
	require.True(t, materialized.Group.HasWeeklyTokenLimit(), "周 token 限额在快照往返后丢失，限额检查会被跳过")
	require.NotNil(t, materialized.Group.DailyLimitTokens)
	require.Equal(t, int64(50_000_000), *materialized.Group.DailyLimitTokens)
	require.NotNil(t, materialized.Group.WeeklyLimitTokens)
	require.Equal(t, int64(200_000_000), *materialized.Group.WeeklyLimitTokens)
}
