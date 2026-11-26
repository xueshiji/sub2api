package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheHitRateAdjustment_ValidateAndNormalize(t *testing.T) {
	t.Parallel()

	for _, valid := range []float64{0, 0.0001, 0.05, 0.10} {
		require.NoError(t, ValidateCacheHitRateAdjustment(valid), "value=%v", valid)
	}
	for _, invalid := range []float64{-0.0001, -0.5, 0.1001, 1.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		require.Error(t, ValidateCacheHitRateAdjustment(invalid), "value=%v", invalid)
	}

	require.Equal(t, 0.0, NormalizeCacheHitRateAdjustment(-0.02))
	require.Equal(t, 0.0, NormalizeCacheHitRateAdjustment(math.NaN()))
	require.Equal(t, 0.05, NormalizeCacheHitRateAdjustment(0.05))
	require.Equal(t, 0.0255, NormalizeCacheHitRateAdjustment(0.02554))
	require.Equal(t, 0.10, NormalizeCacheHitRateAdjustment(0.15))
}

func TestCloneGroupForDuplicate_PreservesCacheHitRateAdjustment(t *testing.T) {
	t.Parallel()

	src := &Group{
		ID:                     42,
		Name:                   "prod",
		Platform:               PlatformAnthropic,
		RateMultiplier:         1.5,
		CacheHitRateAdjustment: 0.05,
	}
	cloned := cloneGroupForDuplicate(src, "op-1")
	require.Equal(t, 0.05, cloned.CacheHitRateAdjustment)
}

func TestAPIKeyAuthSnapshot_RoundtripsCacheHitRateAdjustment(t *testing.T) {
	t.Parallel()

	svc := &APIKeyService{}
	apiKey := &APIKey{
		ID:     11,
		UserID: 22,
		User:   &User{ID: 22, Role: RoleUser, Status: StatusActive},
		Group: &Group{
			ID:                     33,
			Name:                   "adj-group",
			Platform:               PlatformOpenAI,
			Status:                 StatusActive,
			RateMultiplier:         1.2,
			CacheHitRateAdjustment: 0.075,
		},
	}

	snap := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NotNil(t, snap)
	require.NotNil(t, snap.Group)
	require.Equal(t, 0.075, snap.Group.CacheHitRateAdjustment)

	raw, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snap})
	require.NoError(t, err)

	var decoded APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(raw, &decoded))

	restored, ok, err := svc.applyAuthCacheEntry("sk-test", &decoded)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, restored)
	require.NotNil(t, restored.Group)
	require.Equal(t, 0.075, restored.Group.CacheHitRateAdjustment)
}
