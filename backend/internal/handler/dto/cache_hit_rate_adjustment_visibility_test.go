package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupAndUsageLogDTO_CacheHitAdjustmentAdminOnly(t *testing.T) {
	t.Parallel()

	grp := &service.Group{
		ID:                     9,
		Name:                   "adj-group",
		Platform:               service.PlatformAnthropic,
		RateMultiplier:         1.5,
		Status:                 service.StatusActive,
		CacheHitRateAdjustment: 0.05,
	}

	for name, userGroup := range map[string]any{
		"GroupFromService":        GroupFromService(grp),
		"GroupFromServiceShallow": GroupFromServiceShallow(grp),
	} {
		fields := marshalToMap(t, userGroup)
		_, leaked := fields["cache_hit_rate_adjustment"]
		require.False(t, leaked, "%s must omit cache_hit_rate_adjustment", name)
	}

	adminGroup := GroupFromServiceAdmin(grp)
	require.Equal(t, 0.05, adminGroup.CacheHitRateAdjustment)
	adminGroupFields := marshalToMap(t, adminGroup)
	require.Equal(t, 0.05, adminGroupFields["cache_hit_rate_adjustment"])

	logEntry := &service.UsageLog{
		ID:                       101,
		RequestID:                "req-audit-1",
		Model:                    "claude-sonnet-4",
		InputTokens:              2500,
		CacheReadTokens:          7500,
		CacheHitRateAdjustment:   0.05,
		CacheReadTokensReduction: 500,
	}

	userUsageFields := marshalToMap(t, UsageLogFromService(logEntry))
	_, leakedAdj := userUsageFields["cache_hit_rate_adjustment"]
	_, leakedReduction := userUsageFields["cache_read_tokens_reduction"]
	require.False(t, leakedAdj, "user usage DTO must omit cache_hit_rate_adjustment")
	require.False(t, leakedReduction, "user usage DTO must omit cache_read_tokens_reduction")

	adminUsage := UsageLogFromServiceAdmin(logEntry)
	require.Equal(t, 0.05, adminUsage.CacheHitRateAdjustment)
	require.Equal(t, 500, adminUsage.CacheReadTokensReduction)
	adminUsageFields := marshalToMap(t, adminUsage)
	require.Equal(t, 0.05, adminUsageFields["cache_hit_rate_adjustment"])
	require.Equal(t, float64(500), adminUsageFields["cache_read_tokens_reduction"])
}
