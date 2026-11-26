package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// total_cost_before_adjustment 紧跟修正审计列之后、created_at 之前；未修正的行为 SQL NULL。
func TestPrepareUsageLogInsert_TotalCostBeforeAdjustmentWiring(t *testing.T) {
	idx := len(usageLogInsertArgTypes) - 2
	require.Equal(t, "numeric", usageLogInsertArgTypes[idx])

	before := 0.05432
	adjusted := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "req-adjusted", Model: "gpt-5",
		CacheHitRateAdjustment: 0.05, CacheReadTokensReduction: 500, TotalCostBeforeAdjustment: &before,
		CreatedAt: time.Now().UTC(),
	})
	require.Len(t, adjusted.args, len(usageLogInsertArgTypes))
	require.Equal(t, &before, adjusted.args[idx])

	notAdjusted := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "req-plain", Model: "gpt-5",
		CreatedAt: time.Now().UTC(),
	})
	value, ok := notAdjusted.args[idx].(*float64)
	require.True(t, ok)
	require.Nil(t, value)
}
