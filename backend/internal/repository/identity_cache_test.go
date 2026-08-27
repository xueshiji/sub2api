//go:build unit

package repository

import (
	"context"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFingerprintKey(t *testing.T) {
	tests := []struct {
		name      string
		accountID int64
		expected  string
	}{
		{
			name:      "normal_account_id",
			accountID: 123,
			expected:  "fingerprint:123",
		},
		{
			name:      "zero_account_id",
			accountID: 0,
			expected:  "fingerprint:0",
		},
		{
			name:      "negative_account_id",
			accountID: -1,
			expected:  "fingerprint:-1",
		},
		{
			name:      "max_int64",
			accountID: math.MaxInt64,
			expected:  "fingerprint:9223372036854775807",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fingerprintKey(tc.accountID)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestIdentityCache_GetFingerprint_MissingKey_ReturnsNilWithoutError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewIdentityCache(rdb)
	ctx := context.Background()

	fp, err := cache.GetFingerprint(ctx, 123)
	require.NoError(t, err, "missing key is a miss, not a cache failure")
	require.Nil(t, fp)

	require.NoError(t, cache.SetFingerprint(ctx, 123, &service.Fingerprint{ClientID: "c1", UserAgent: "ua"}))
	got, err := cache.GetFingerprint(ctx, 123)
	require.NoError(t, err)
	require.Equal(t, "c1", got.ClientID)
	require.Equal(t, "ua", got.UserAgent)
}

func TestIdentityCache_GetFingerprint_CorruptedValue_ReturnsNilWithoutError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewIdentityCache(rdb)
	ctx := context.Background()

	require.NoError(t, rdb.Set(ctx, fingerprintKey(123), "invalid-json-data", 0).Err())

	fp, err := cache.GetFingerprint(ctx, 123)
	require.NoError(t, err, "corrupted value is a miss so the create branch can overwrite it")
	require.Nil(t, fp)
}
