//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupAutoRateRepository_GetEligibleSummary(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)SELECT a\.id, a\.name, a\.rate_multiplier, COUNT\(\*\) OVER\(\) AS eligible_count.*a\.expires_at IS NULL OR a\.expires_at > NOW\(\) OR a\.auto_pause_on_expired = FALSE.*\(NOT \$3 OR a\.type <> \$4\)`).
		WithArgs(int64(7), service.StatusActive, false, service.AccountTypeAPIKey).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "rate_multiplier", "eligible_count"}).
			AddRow(int64(101), "upstream-a", 0.35, int64(3)))

	repo := newGroupAutoRateRepositoryWithSQL(nil, db)
	summary, err := repo.GetGroupAutoRateEligibleSummary(context.Background(), 7, false)
	require.NoError(t, err)
	require.EqualValues(t, 3, summary.EligibleAccountCount)
	require.NotNil(t, summary.MaxAccountID)
	require.EqualValues(t, 101, *summary.MaxAccountID)
	require.Equal(t, "upstream-a", summary.MaxAccountName)
	require.NotNil(t, summary.MaxAccountRate)
	require.InDelta(t, 0.35, *summary.MaxAccountRate, 1e-9)

	mock.ExpectQuery(`(?s)SELECT a\.id, a\.name, a\.rate_multiplier, COUNT\(\*\) OVER\(\) AS eligible_count.*\(NOT \$3 OR a\.type <> \$4\)`).
		WithArgs(int64(7), service.StatusActive, true, service.AccountTypeAPIKey).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "rate_multiplier", "eligible_count"}).
			AddRow(int64(102), "oauth-b", 0.25, int64(1)))

	oauthSummary, err := repo.GetGroupAutoRateEligibleSummary(context.Background(), 7, true)
	require.NoError(t, err)
	require.EqualValues(t, 1, oauthSummary.EligibleAccountCount)
	require.NotNil(t, oauthSummary.MaxAccountID)
	require.EqualValues(t, 102, *oauthSummary.MaxAccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupAutoRateRepository_SyncAutoRateForGroups_UpdatesAndEnqueuesOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	// Fast read-only precheck finds group 7.
	mock.ExpectQuery(`(?s)SELECT id.*FROM groups.*auto_rate_enabled = TRUE`).
		WithArgs(sqlmock.AnyArg(), service.PlatformOpenAI, service.SubscriptionTypeStandard).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, rate_multiplier, auto_rate_mode, auto_rate_value.*FOR UPDATE`).
		WithArgs(sqlmock.AnyArg(), service.PlatformOpenAI, service.SubscriptionTypeStandard).
		WillReturnRows(sqlmock.NewRows([]string{"id", "rate_multiplier", "auto_rate_mode", "auto_rate_value"}).
			AddRow(int64(7), 1.0, service.GroupAutoRateModePercentage, 0.2))
	mock.ExpectQuery(`(?s)SELECT ag\.group_id, MAX\(a\.rate_multiplier\) AS max_rate.*a\.expires_at IS NULL OR a\.expires_at > NOW\(\) OR a\.auto_pause_on_expired = FALSE`).
		WithArgs(sqlmock.AnyArg(), service.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "max_rate"}).
			AddRow(int64(7), 0.15))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE groups")).
		WithArgs(0.18, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventGroupChanged, nil, int64(7), nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	repo := newGroupAutoRateRepositoryWithSQL(client, db)
	changed, err := repo.SyncAutoRateForGroups(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Equal(t, []int64{7}, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupAutoRateRepository_SyncAutoRateForGroups_EmptyPoolLeavesRateUnchanged(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectQuery(`(?s)SELECT id.*FROM groups.*auto_rate_enabled = TRUE`).
		WithArgs(sqlmock.AnyArg(), service.PlatformOpenAI, service.SubscriptionTypeStandard).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id, rate_multiplier, auto_rate_mode, auto_rate_value.*FOR UPDATE`).
		WithArgs(sqlmock.AnyArg(), service.PlatformOpenAI, service.SubscriptionTypeStandard).
		WillReturnRows(sqlmock.NewRows([]string{"id", "rate_multiplier", "auto_rate_mode", "auto_rate_value"}).
			AddRow(int64(7), 1.25, service.GroupAutoRateModeFixed, 0.1))
	mock.ExpectQuery(`(?s)SELECT ag\.group_id, MAX\(a\.rate_multiplier\) AS max_rate`).
		WithArgs(sqlmock.AnyArg(), service.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "max_rate"}))
	mock.ExpectCommit()

	repo := newGroupAutoRateRepositoryWithSQL(client, db)
	changed, err := repo.SyncAutoRateForGroups(context.Background(), []int64{7})
	require.NoError(t, err)
	require.Empty(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupAutoRateRepository_SyncAutoRateForGroups_PrecheckSkipsTx(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)SELECT id.*FROM groups.*auto_rate_enabled = TRUE`).
		WithArgs(sqlmock.AnyArg(), service.PlatformOpenAI, service.SubscriptionTypeStandard).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	repo := newGroupAutoRateRepositoryWithSQL(nil, db)
	changed, err := repo.SyncAutoRateForGroups(context.Background(), []int64{11})
	require.NoError(t, err)
	require.Empty(t, changed)
	require.NoError(t, mock.ExpectationsWereMet())
}
