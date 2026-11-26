//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 账号窗口费用的 standard_cost：缓存命中率修正生效的行取售价口径的修正前成本 total_cost_before_adjustment，
// 该列为空的历史修正行回落 account_stats_cost，未修正的行取 total_cost。
func TestUsageLogRepo_AccountWindowStandardCostStaysOnSellingPriceBasis(t *testing.T) {
	ctx := context.Background()
	filePriceCost, sellingPriceCost := 0.02716, 0.05432

	cases := []struct {
		name string
		rows []service.UsageLog
		want float64
	}{
		{
			name: "分组售价为定价文件两倍时修正行按售价口径计入",
			rows: []service.UsageLog{
				{RequestID: "window-cost-plain", TotalCost: 0.03},
				{RequestID: "window-cost-adjusted", TotalCost: 0.06, CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500,
					TotalCostBeforeAdjustment: &sellingPriceCost, AccountStatsCost: &filePriceCost},
				{RequestID: "window-cost-legacy-adjusted", TotalCost: 0.06, CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500,
					AccountStatsCost: &filePriceCost},
				{RequestID: "window-cost-legacy-adjusted-without-stats-cost", TotalCost: 0.02, CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500},
			},
			want: 0.03 + sellingPriceCost + filePriceCost + 0.02,
		},
		{
			name: "默认售价下修正行计入的成本不变",
			rows: []service.UsageLog{
				{RequestID: "window-cost-plain", TotalCost: 0.03},
				{RequestID: "window-cost-adjusted", TotalCost: 0.031, CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500,
					TotalCostBeforeAdjustment: &filePriceCost, AccountStatsCost: &filePriceCost},
			},
			want: 0.03 + filePriceCost,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := testEntTx(t)
			client := tx.Client()
			repo := newUsageLogRepositoryWithSQL(client, tx)

			user := mustCreateUser(t, client, &service.User{})
			apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "k"})
			account := mustCreateAccount(t, client, &service.Account{Name: "acc-window-cost"})

			now := time.Now().UTC()
			for i := range tc.rows {
				log := &tc.rows[i]
				log.UserID, log.APIKeyID, log.AccountID = user.ID, apiKey.ID, account.ID
				log.Model, log.InputTokens, log.OutputTokens = "kimi-k2", 1000, 100
				log.CreatedAt = now
				inserted, err := repo.Create(ctx, log)
				require.NoError(t, err)
				require.True(t, inserted)

				stored, err := repo.GetByID(ctx, log.ID)
				require.NoError(t, err)
				require.Equal(t, log.TotalCostBeforeAdjustment, stored.TotalCostBeforeAdjustment, log.RequestID)
			}

			windowStart := now.Add(-time.Minute)
			window, err := repo.GetAccountWindowStats(ctx, account.ID, windowStart)
			require.NoError(t, err)
			require.InDelta(t, tc.want, window.StandardCost, 1e-9)

			batch, err := repo.GetAccountWindowStatsBatch(ctx, []int64{account.ID}, windowStart)
			require.NoError(t, err)
			require.InDelta(t, tc.want, batch[account.ID].StandardCost, 1e-9)

			today, err := repo.GetAccountTodayStats(ctx, account.ID)
			require.NoError(t, err)
			require.InDelta(t, tc.want, today.StandardCost, 1e-9)
		})
	}
}

// 单条、批量与 best-effort 批量三条写入路径都原样落库 total_cost_before_adjustment，未修正时为 NULL。
func TestUsageLogRepo_TotalCostBeforeAdjustmentPersistedOnEveryInsertPath(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	batchRepo := newUsageLogRepositoryWithSQL(client, integrationDB)
	tx := testEntTx(t)
	singleRepo := newUsageLogRepositoryWithSQL(tx.Client(), tx)

	user := mustCreateUser(t, client, &service.User{})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-before-cost"})

	before := 0.05432
	newLog := func(name string, before *float64) *service.UsageLog {
		return &service.UsageLog{
			UserID: user.ID, APIKeyID: apiKey.ID, AccountID: account.ID,
			RequestID: "before-cost-" + name, Model: "kimi-k2", InputTokens: 1000, OutputTokens: 100,
			CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500,
			TotalCostBeforeAdjustment: before,
		}
	}
	single, singleNull := newLog("single", &before), newLog("single-null", nil)
	batch, batchNull := newLog("batch", &before), newLog("batch-null", nil)
	bestEffort, bestEffortNull := newLog("best-effort", &before), newLog("best-effort-null", nil)

	for _, log := range []*service.UsageLog{single, singleNull} {
		inserted, err := singleRepo.Create(ctx, log)
		require.NoError(t, err)
		require.True(t, inserted)
	}
	for _, log := range []*service.UsageLog{batch, batchNull} {
		inserted, err := batchRepo.Create(ctx, log)
		require.NoError(t, err)
		require.True(t, inserted)
	}
	for _, log := range []*service.UsageLog{bestEffort, bestEffortNull} {
		require.NoError(t, batchRepo.CreateBestEffort(ctx, log))
	}

	assertStored := func(q sqlExecutor, log *service.UsageLog) {
		t.Helper()
		rows, err := q.QueryContext(ctx,
			"SELECT total_cost_before_adjustment FROM usage_logs WHERE request_id = $1 AND api_key_id = $2",
			log.RequestID, log.APIKeyID)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		require.True(t, rows.Next(), log.RequestID)
		var stored sql.NullFloat64
		require.NoError(t, rows.Scan(&stored))
		if log.TotalCostBeforeAdjustment == nil {
			require.False(t, stored.Valid, log.RequestID)
			return
		}
		require.True(t, stored.Valid, log.RequestID)
		require.InDelta(t, *log.TotalCostBeforeAdjustment, stored.Float64, 1e-12, log.RequestID)
	}
	for _, log := range []*service.UsageLog{single, singleNull} {
		assertStored(tx, log)
	}
	for _, log := range []*service.UsageLog{batch, batchNull, bestEffort, bestEffortNull} {
		assertStored(integrationDB, log)
	}
}
