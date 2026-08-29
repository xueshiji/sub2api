//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGetPeakWeightedTokenStats(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(">= lpad(g.peak_start, 5, '0')")).
		WithArgs(start, end, "Local").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "user_label", "weighted_tokens", "total_tokens", "cache_read_tokens", "input_tokens", "output_tokens",
			"weighted_input_tokens", "weighted_cache_read_tokens", "weighted_output_tokens",
			"peak_input_tokens", "peak_cache_read_tokens", "peak_output_tokens",
			"discounted_weighted_input_tokens", "discounted_weighted_cache_read_tokens", "discounted_weighted_output_tokens",
			"discounted_offpeak_input_tokens", "discounted_offpeak_cache_read_tokens", "discounted_offpeak_output_tokens",
			"requests",
		}).
			AddRow(int64(1), "zhangsan", "458590000.5000", int64(416170000), int64(160000000), int64(140000000), int64(116170000),
				"154000000.5000", "176000000.2500", "127787000.7500", int64(14000000), int64(16000000), int64(11617000),
				"50000000", "40000000", "30000000", int64(10000000), int64(8000000), int64(6000000), int64(2831)).
			AddRow(int64(2), "user#2", "0", int64(0), int64(0), int64(0), int64(0),
				"0", "0", "0", int64(0), int64(0), int64(0),
				"0", "0", "0", int64(0), int64(0), int64(0), int64(0)))

	rows, err := repo.GetPeakWeightedTokenStats(context.Background(), start, end, usagestats.UserBreakdownDimension{})

	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, usagestats.PeakWeightedTokenItem{
		UserID:                            1,
		UserLabel:                         "zhangsan",
		WeightedTokens:                    458590000.5,
		TotalTokens:                       416170000,
		CacheReadTokens:                   160000000,
		InputTokens:                       140000000,
		OutputTokens:                      116170000,
		WeightedInputTokens:               154000000.5,
		WeightedCacheReadTokens:           176000000.25,
		WeightedOutputTokens:              127787000.75,
		PeakInputTokens:                   14000000,
		PeakCacheReadTokens:               16000000,
		PeakOutputTokens:                  11617000,
		DiscountedWeightedInputTokens:     50000000,
		DiscountedWeightedCacheReadTokens: 40000000,
		DiscountedWeightedOutputTokens:    30000000,
		DiscountedOffpeakInputTokens:      10000000,
		DiscountedOffpeakCacheReadTokens:  8000000,
		DiscountedOffpeakOutputTokens:     6000000,
		Requests:                          2831,
	}, rows[0])
	require.Equal(t, "user#2", rows[1].UserLabel)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPeakWeightedTokenStatsMatchesDiscountedModelCaseInsensitively(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("LOWER(COALESCE(NULLIF(TRIM(ul.upstream_model), ''), ul.model)) IN ('glm-5.3-flash')")).
		WithArgs(start, end, "Local").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "user_label", "weighted_tokens", "total_tokens", "cache_read_tokens", "input_tokens", "output_tokens",
			"weighted_input_tokens", "weighted_cache_read_tokens", "weighted_output_tokens",
			"peak_input_tokens", "peak_cache_read_tokens", "peak_output_tokens",
			"discounted_weighted_input_tokens", "discounted_weighted_cache_read_tokens", "discounted_weighted_output_tokens",
			"discounted_offpeak_input_tokens", "discounted_offpeak_cache_read_tokens", "discounted_offpeak_output_tokens",
			"requests",
		}))

	rows, err := repo.GetPeakWeightedTokenStats(context.Background(), start, end, usagestats.UserBreakdownDimension{})

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPeakWeightedModelBreakdown(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta("LOWER(COALESCE(NULLIF(TRIM(ul.upstream_model), ''), ul.model)) AS model")).
		WithArgs(start, end, "Local").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "model", "in_peak", "cache_read_tokens", "input_tokens", "output_tokens",
		}).
			AddRow(int64(1), "glm-5.3-flash", true, int64(16000000), int64(14000000), int64(11617000)).
			AddRow(int64(1), "glm-5.3-flash", false, int64(8000000), int64(10000000), int64(6000000)).
			AddRow(int64(1), "claude-sonnet-5", false, int64(136000000), int64(116000000), int64(98553000)))

	details, err := repo.GetPeakWeightedModelBreakdown(context.Background(), start, end, usagestats.UserBreakdownDimension{})

	require.NoError(t, err)
	require.Len(t, details, 3)
	require.Equal(t, usagestats.PeakWeightedModelDetail{
		UserID:          1,
		Model:           "glm-5.3-flash",
		InPeak:          true,
		CacheReadTokens: 16000000,
		InputTokens:     14000000,
		OutputTokens:    11617000,
	}, details[0])
	require.Equal(t, usagestats.PeakWeightedModelDetail{
		UserID:          1,
		Model:           "glm-5.3-flash",
		InPeak:          false,
		CacheReadTokens: 8000000,
		InputTokens:     10000000,
		OutputTokens:    6000000,
	}, details[1])
	require.Equal(t, "claude-sonnet-5", details[2].Model)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPeakWeightedTokenStatsLimitsPeakToSubscriptionGroups(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("AND g.subscription_type = 'subscription'")).
		WithArgs(start, end, "Local").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "user_label", "weighted_tokens", "total_tokens", "cache_read_tokens", "input_tokens", "output_tokens", "requests",
		}))

	rows, err := repo.GetPeakWeightedTokenStats(context.Background(), start, end, usagestats.UserBreakdownDimension{})

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPeakWeightedTokenStatsExcludesWeekendFromPeakWindow(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("AND EXTRACT(ISODOW FROM ul.created_at AT TIME ZONE $3) < 6")).
		WithArgs(start, end, "Local").
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "user_label", "weighted_tokens", "total_tokens", "cache_read_tokens", "input_tokens", "output_tokens", "requests",
		}))

	rows, err := repo.GetPeakWeightedTokenStats(context.Background(), start, end, usagestats.UserBreakdownDimension{})

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPeakWeightedTokenStatsWithGroupFilter(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("AND ul.group_id = $4")).
		WithArgs(start, end, "Local", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "user_label", "weighted_tokens", "total_tokens", "cache_read_tokens", "input_tokens", "output_tokens", "requests",
		}))

	rows, err := repo.GetPeakWeightedTokenStats(context.Background(), start, end, usagestats.UserBreakdownDimension{GroupID: 7})

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}
