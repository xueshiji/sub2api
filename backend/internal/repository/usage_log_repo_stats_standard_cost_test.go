package repository

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// adjustedRowStandardCostPattern 匹配账号侧标准费用列：缓存命中率修正生效的行 total_cost 是修正后的用户侧口径，
// 须取售价口径的修正前成本 total_cost_before_adjustment，该列为空的历史行回落 account_stats_cost；
// 未修正的行仍取 total_cost，且整列不乘账号倍率。
const adjustedRowStandardCostPattern = `COALESCE\(SUM\(CASE WHEN cache_hit_rate_adjustment > 0 THEN COALESCE\(total_cost_before_adjustment, account_stats_cost, total_cost\) ELSE total_cost END\), 0\) as standard_cost,`

func TestUsageLogRepositoryAccountStandardCostReadsRealCostForHitRateAdjustedRows(t *testing.T) {
	accountID := int64(7)
	windowStart := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)
	statsColumns := []string{"requests", "tokens", "cost", "standard_cost", "user_cost"}
	// cost 与 standard_cost 取不同值，保证断言落在标准费用列而不是账号口径列。
	statsRow := []driver.Value{int64(2), int64(30000), 0.62175, 0.72175, 1.5}

	cases := []struct {
		name    string
		pattern string
		columns []string
		row     []driver.Value
		args    []driver.Value
		run     func(ctx context.Context, repo *usageLogRepository) (float64, error)
	}{
		{
			name: "GetAccountTodayStats",
			pattern: `(?s)SELECT COUNT\(\*\) as requests,.*` + adjustedRowStandardCostPattern +
				`.*FROM usage_logs WHERE account_id = \$1 AND created_at >= \$2`,
			columns: statsColumns,
			row:     statsRow,
			args:    []driver.Value{accountID, sqlmock.AnyArg()},
			run: func(ctx context.Context, repo *usageLogRepository) (float64, error) {
				stats, err := repo.GetAccountTodayStats(ctx, accountID)
				if err != nil {
					return 0, err
				}
				return stats.StandardCost, nil
			},
		},
		{
			name: "GetAccountWindowStats",
			pattern: `(?s)SELECT COUNT\(\*\) as requests,.*` + adjustedRowStandardCostPattern +
				`.*FROM usage_logs WHERE account_id = \$1 AND created_at >= \$2`,
			columns: statsColumns,
			row:     statsRow,
			args:    []driver.Value{accountID, windowStart},
			run: func(ctx context.Context, repo *usageLogRepository) (float64, error) {
				stats, err := repo.GetAccountWindowStats(ctx, accountID, windowStart)
				if err != nil {
					return 0, err
				}
				return stats.StandardCost, nil
			},
		},
		{
			name: "GetAccountWindowStatsBatch",
			pattern: `(?s)SELECT account_id, COUNT\(\*\) as requests,.*` + adjustedRowStandardCostPattern +
				`.*FROM usage_logs WHERE account_id = ANY\(\$1\) AND created_at >= \$2 GROUP BY account_id`,
			columns: append([]string{"account_id"}, statsColumns...),
			row:     append([]driver.Value{accountID}, statsRow...),
			args:    []driver.Value{sqlmock.AnyArg(), windowStart},
			run: func(ctx context.Context, repo *usageLogRepository) (float64, error) {
				statsByAccount, err := repo.GetAccountWindowStatsBatch(ctx, []int64{accountID}, windowStart)
				if err != nil {
					return 0, err
				}
				return statsByAccount[accountID].StandardCost, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := newUsageLogRepositoryWithSQL(nil, db)

			mock.ExpectQuery(tc.pattern).
				WithArgs(tc.args...).
				WillReturnRows(sqlmock.NewRows(tc.columns).AddRow(tc.row...))

			standardCost, err := tc.run(context.Background(), repo)
			require.NoError(t, err)
			require.InDelta(t, 0.72175, standardCost, 0.0000001)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// GetAccountUsageStats 的按天 cost 取 total_cost，与模型、端点明细的 cost 同口径。
func TestUsageLogRepositoryAccountUsageStatsStandardCostSumsDailyTotalCost(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)

	accountID := int64(7)
	startTime := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)
	endTime := startTime.Add(24 * time.Hour)

	mock.ExpectQuery(`(?s)TO_CHAR\(created_at, 'YYYY-MM-DD'\) as date,.*`+
		`COALESCE\(SUM\(total_cost\), 0\) as cost,`+
		`.*FROM usage_logs WHERE account_id = \$1 AND created_at >= \$2 AND created_at < \$3 GROUP BY date`).
		WithArgs(accountID, startTime, endTime).
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "tokens", "cost", "actual_cost", "user_cost"}).
			AddRow("2026-03-09", int64(2), int64(30000), 0.72175, 0.62175, 1.5))
	mock.ExpectQuery(`(?s)SELECT COALESCE\(AVG\(duration_ms\), 0\) as avg_duration_ms FROM usage_logs`).
		WithArgs(accountID, startTime, endTime).
		WillReturnRows(sqlmock.NewRows([]string{"avg_duration_ms"}).AddRow(0.0))
	// 模型与端点分布不在本测试范围内，返回空结果集即可。
	mock.ExpectQuery(`ORDER BY total_tokens DESC`).
		WillReturnRows(sqlmock.NewRows([]string{"model"}))
	mock.ExpectQuery(`GROUP BY endpoint ORDER BY requests DESC`).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint"}))
	mock.ExpectQuery(`GROUP BY endpoint ORDER BY requests DESC`).
		WillReturnRows(sqlmock.NewRows([]string{"endpoint"}))

	resp, err := repo.GetAccountUsageStats(context.Background(), accountID, startTime, endTime)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.InDelta(t, 0.72175, resp.Summary.TotalStandardCost, 0.0000001)
	require.InDelta(t, 0.62175, resp.Summary.TotalCost, 0.0000001)
	require.NoError(t, mock.ExpectationsWereMet())
}
