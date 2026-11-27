//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

type summaryCall struct {
	groupID          int64
	requireOAuthOnly bool
}

type stubGroupAutoRateRepo struct {
	summaryByGroup     map[int64]*GroupAutoRateEligibleSummary
	summaryOAuthGroup  map[int64]*GroupAutoRateEligibleSummary
	summaryCalls       []summaryCall
	syncGroupCalls     [][]int64
	syncGroupTxPresent []bool
	syncAcctCalls      [][]int64
	syncAcctTxPresent  []bool
	changedGroups      []int64
	syncErr            error
}

func (r *stubGroupAutoRateRepo) GetGroupAutoRateEligibleSummary(_ context.Context, groupID int64, requireOAuthOnly bool) (*GroupAutoRateEligibleSummary, error) {
	r.summaryCalls = append(r.summaryCalls, summaryCall{groupID: groupID, requireOAuthOnly: requireOAuthOnly})
	if requireOAuthOnly && r.summaryOAuthGroup != nil {
		if s, ok := r.summaryOAuthGroup[groupID]; ok && s != nil {
			return s, nil
		}
	}
	if s, ok := r.summaryByGroup[groupID]; ok && s != nil {
		return s, nil
	}
	return &GroupAutoRateEligibleSummary{}, nil
}

func (r *stubGroupAutoRateRepo) SyncAutoRateForGroups(ctx context.Context, groupIDs []int64) ([]int64, error) {
	r.syncGroupCalls = append(r.syncGroupCalls, append([]int64(nil), groupIDs...))
	r.syncGroupTxPresent = append(r.syncGroupTxPresent, dbent.TxFromContext(ctx) != nil)
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	return append([]int64(nil), r.changedGroups...), nil
}

func (r *stubGroupAutoRateRepo) SyncAutoRateForAccounts(ctx context.Context, accountIDs []int64) ([]int64, error) {
	r.syncAcctCalls = append(r.syncAcctCalls, append([]int64(nil), accountIDs...))
	r.syncAcctTxPresent = append(r.syncAcctTxPresent, dbent.TxFromContext(ctx) != nil)
	if r.syncErr != nil {
		return nil, r.syncErr
	}
	return append([]int64(nil), r.changedGroups...), nil
}

type stubAutoRateAuthCacheInvalidator struct {
	groupIDs []int64
}

func (i *stubAutoRateAuthCacheInvalidator) InvalidateAuthCacheByUserID(context.Context, int64) {}
func (i *stubAutoRateAuthCacheInvalidator) InvalidateAuthCacheByKey(context.Context, string)   {}
func (i *stubAutoRateAuthCacheInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, groupID int64) {
	i.groupIDs = append(i.groupIDs, groupID)
}

type stubGroupAutoRateSyncer struct {
	groupCalls   [][]int64
	accountCalls [][]int64
}

func (s *stubGroupAutoRateSyncer) SyncAutoRateForGroups(_ context.Context, groupIDs ...int64) error {
	s.groupCalls = append(s.groupCalls, append([]int64(nil), groupIDs...))
	return nil
}

func (s *stubGroupAutoRateSyncer) SyncAutoRateForAccounts(_ context.Context, accountIDs ...int64) error {
	s.accountCalls = append(s.accountCalls, append([]int64(nil), accountIDs...))
	return nil
}

func TestComputeGroupAutoRate(t *testing.T) {
	tests := []struct {
		name    string
		maxRate float64
		mode    string
		value   float64
		want    float64
		wantOK  bool
	}{
		{name: "exact percentage no float drift", maxRate: 0.15, mode: GroupAutoRateModePercentage, value: 0.20, want: 0.18, wantOK: true},
		{name: "percentage ceil 0.13*1.15=0.1495", maxRate: 0.13, mode: GroupAutoRateModePercentage, value: 0.15, want: 0.15, wantOK: true},
		{name: "percentage ceil 0.3333*1.15=0.383295", maxRate: 0.3333, mode: GroupAutoRateModePercentage, value: 0.15, want: 0.39, wantOK: true},
		{name: "percentage zero markup small rate", maxRate: 0.0001, mode: GroupAutoRateModePercentage, value: 0, want: 0.01, wantOK: true},
		{name: "fixed additive exact", maxRate: 0.25, mode: GroupAutoRateModeFixed, value: 0.10, want: 0.35, wantOK: true},
		{name: "fixed additive from zero", maxRate: 0, mode: GroupAutoRateModeFixed, value: 0.20, want: 0.20, wantOK: true},
		{name: "fixed ceil sub-cent", maxRate: 0.1001, mode: GroupAutoRateModeFixed, value: 0.10, want: 0.21, wantOK: true},
		{name: "zero result rejected", maxRate: 0, mode: GroupAutoRateModePercentage, value: 0.20, want: 0, wantOK: false},
		{name: "zero fixed rejected", maxRate: 0, mode: GroupAutoRateModeFixed, value: 0, want: 0, wantOK: false},
		{name: "above max multiplier rejected", maxRate: 90, mode: GroupAutoRateModePercentage, value: 0.20, want: 0, wantOK: false},
		{name: "exact max multiplier allowed", maxRate: 100, mode: GroupAutoRateModeFixed, value: 0, want: 100, wantOK: true},
		{name: "negative max rate rejected", maxRate: -0.1, mode: GroupAutoRateModeFixed, value: 0.2, want: 0, wantOK: false},
		{name: "negative value rejected", maxRate: 1.0, mode: GroupAutoRateModeFixed, value: -0.1, want: 0, wantOK: false},
		{name: "unknown mode rejected", maxRate: 1.0, mode: "multiply", value: 0.1, want: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ComputeGroupAutoRate(tt.maxRate, tt.mode, tt.value)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.InDelta(t, tt.want, got, 1e-9)
			}
		})
	}
}

func TestNormalizeAndValidateGroupAutoRateConfig(t *testing.T) {
	// Unsupported platform or subscription type resets silently.
	res := NormalizeGroupAutoRateConfig(GroupAutoRateConfig{
		Platform:         PlatformAnthropic,
		SubscriptionType: SubscriptionTypeStandard,
		Enabled:          true,
		Mode:             GroupAutoRateModePercentage,
		Value:            0.2,
	})
	require.False(t, res.Enabled)
	require.Equal(t, "", res.Mode)
	require.Equal(t, 0.0, res.Value)
	require.NoError(t, ValidateGroupAutoRateConfig(res))

	subRes := NormalizeGroupAutoRateConfig(GroupAutoRateConfig{
		Platform:         PlatformOpenAI,
		SubscriptionType: SubscriptionTypeSubscription,
		Enabled:          true,
		Mode:             GroupAutoRateModeFixed,
		Value:            0.1,
	})
	require.False(t, subRes.Enabled)

	// Direct validation rejects unsupported platform when enabled=true.
	err := ValidateGroupAutoRateConfig(GroupAutoRateConfig{
		Platform:         PlatformAnthropic,
		SubscriptionType: SubscriptionTypeStandard,
		Enabled:          true,
		Mode:             GroupAutoRateModePercentage,
		Value:            0.2,
	})
	require.Error(t, err)

	// Rejects negative value.
	err = ValidateGroupAutoRateConfig(GroupAutoRateConfig{
		Platform:         PlatformOpenAI,
		SubscriptionType: SubscriptionTypeStandard,
		Enabled:          true,
		Mode:             GroupAutoRateModeFixed,
		Value:            -0.01,
	})
	require.Error(t, err)
}

func TestGroupAutoRateService_InvalidatesAuthCacheForChangedGroups(t *testing.T) {
	repo := &stubGroupAutoRateRepo{changedGroups: []int64{10, 20}}
	invalidator := &stubAutoRateAuthCacheInvalidator{}
	svc := NewGroupAutoRateService(repo, invalidator)

	require.NoError(t, svc.SyncAutoRateForGroups(context.Background(), 10, 20))
	require.Equal(t, [][]int64{{10, 20}}, repo.syncGroupCalls)
	require.Equal(t, []int64{10, 20}, invalidator.groupIDs)

	require.NoError(t, svc.SyncAutoRateForAccounts(context.Background(), 5))
	require.Equal(t, [][]int64{{5}}, repo.syncAcctCalls)
	require.Equal(t, []int64{10, 20, 10, 20}, invalidator.groupIDs)
}

func TestAdminService_UpdateGroup_AutoRateLocksManualRateChange(t *testing.T) {
	existing := &Group{
		ID:               9,
		Name:             "openai-auto",
		Platform:         PlatformOpenAI,
		SubscriptionType: SubscriptionTypeStandard,
		Status:           StatusActive,
		RateMultiplier:   1.2,
		AutoRateEnabled:  true,
		AutoRateMode:     GroupAutoRateModePercentage,
		AutoRateValue:    0.2,
	}
	repo := &groupRepoStubForAdmin{getByID: existing}
	svc := &adminServiceImpl{groupRepo: repo}

	newRate := 1.5
	_, err := svc.UpdateGroup(context.Background(), 9, &UpdateGroupInput{
		RateMultiplier: &newRate,
	})
	require.ErrorIs(t, err, ErrGroupAutoRateConflict)

	// Disabling auto rate in the same request allows updating rate_multiplier.
	disabled := false
	updated, err := svc.UpdateGroup(context.Background(), 9, &UpdateGroupInput{
		AutoRateEnabled: &disabled,
		RateMultiplier:  &newRate,
	})
	require.NoError(t, err)
	require.False(t, updated.AutoRateEnabled)
	require.Equal(t, 1.5, updated.RateMultiplier)
}

func TestAdminService_CreateAndUpdateGroup_TriggersAutoRateSync(t *testing.T) {
	autoRepo := &stubGroupAutoRateRepo{changedGroups: []int64{55}}
	invalidator := &stubAutoRateAuthCacheInvalidator{}
	autoSvc := NewGroupAutoRateService(autoRepo, invalidator)

	synced := &Group{
		ID:               55,
		Name:             "openai-auto",
		Platform:         PlatformOpenAI,
		SubscriptionType: SubscriptionTypeStandard,
		Status:           StatusActive,
		RateMultiplier:   0.24,
		AutoRateEnabled:  true,
		AutoRateMode:     GroupAutoRateModePercentage,
		AutoRateValue:    0.2,
	}
	repo := &groupRepoStubForAdmin{
		createID: 55,
		getByID:  synced,
	}
	svc := &adminServiceImpl{
		groupRepo:       repo,
		autoRateService: autoSvc,
	}

	val := 0.2
	created, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name:             "openai-auto",
		Platform:         PlatformOpenAI,
		SubscriptionType: SubscriptionTypeStandard,
		RateMultiplier:   1.0,
		AutoRateEnabled:  true,
		AutoRateMode:     GroupAutoRateModePercentage,
		AutoRateValue:    &val,
	})
	require.NoError(t, err)
	require.Equal(t, 0.24, created.RateMultiplier)
	require.Equal(t, [][]int64{{55}}, autoRepo.syncGroupCalls)
	require.Equal(t, []int64{55}, invalidator.groupIDs)
}

func TestUpstreamBillingProbeService_TriggersGroupAutoRateSyncOnRateWriteBack(t *testing.T) {
	account := &Account{
		ID:       31,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-upstream",
			"base_url": "https://relay.example.com",
		},
		Extra: map[string]any{
			UpstreamBillingProbeEnabledExtraKey:    true,
			UpstreamBillingRateSyncEnabledExtraKey: true,
		},
	}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{31: account}}
	probeSvc := newUpstreamBillingProbeTestService(repo, &upstreamBillingProbeHTTPStub{}, &upstreamBillingProbeSettingRepo{})
	syncer := &stubGroupAutoRateSyncer{}
	probeSvc.SetGroupAutoRateSyncer(syncer)

	snap, err := probeSvc.probeLoadedAccount(context.Background(), account, 30)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.Equal(t, UpstreamBillingProbeStatusOK, snap.Status)
	require.Equal(t, [][]int64{{31}}, syncer.accountCalls)
}

type autoRateSchedulerAccountRepo struct {
	*bulkEventAccountRepo
}

func (r *autoRateSchedulerAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, acc := range r.accounts {
		if acc != nil && acc.ID == id {
			return acc, nil
		}
	}
	return nil, ErrAccountNotFound
}

func TestSchedulerSnapshotService_OutboxAccountEventsTriggerAutoRateSync(t *testing.T) {
	syncer := &stubGroupAutoRateSyncer{}
	account := &Account{
		ID:       88,
		Platform: PlatformOpenAI,
		Status:   StatusError,
		GroupIDs: []int64{12, 19},
	}
	cache := newBulkEventSnapshotCache()
	accounts := &autoRateSchedulerAccountRepo{bulkEventAccountRepo: newBulkEventAccountRepo(account)}
	svc := NewSchedulerSnapshotService(cache, nil, accounts, nil, nil)
	svc.SetGroupAutoRateSyncer(syncer)

	acctID := int64(88)
	require.NoError(t, svc.handleAccountEvent(context.Background(), &acctID, nil, nil))
	require.Equal(t, [][]int64{{12, 19}}, syncer.groupCalls)
}

func TestSchedulerSnapshotService_OutboxBulkAccountDefaultBranchTriggersAutoRateSync(t *testing.T) {
	syncer := &stubGroupAutoRateSyncer{}
	account := &Account{
		ID:       91,
		Platform: "custom-unknown",
		Status:   StatusActive,
		GroupIDs: []int64{25, 26},
	}
	cache := newBulkEventSnapshotCache()
	accounts := &autoRateSchedulerAccountRepo{bulkEventAccountRepo: newBulkEventAccountRepo(account)}
	svc := newBulkEventTestService(cache, accounts)
	svc.SetGroupAutoRateSyncer(syncer)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{91}, nil), make(map[batchSeenKey]struct{}))
	require.NoError(t, err)
	require.Len(t, syncer.groupCalls, 1)
	require.ElementsMatch(t, []int64{25, 26}, syncer.groupCalls[0])
}

func TestAdminService_GetGroupAutoRatePreview_AppliesRequireOAuthOnlyOnSourceCopy(t *testing.T) {
	apiKeyMax := 0.50
	oauthMax := 0.20
	autoRepo := &stubGroupAutoRateRepo{
		summaryByGroup: map[int64]*GroupAutoRateEligibleSummary{
			10: {EligibleAccountCount: 2, MaxAccountRate: &apiKeyMax},
			20: {EligibleAccountCount: 3, MaxAccountRate: &apiKeyMax},
		},
		summaryOAuthGroup: map[int64]*GroupAutoRateEligibleSummary{
			10: {EligibleAccountCount: 1, MaxAccountRate: &oauthMax},
		},
	}
	autoSvc := NewGroupAutoRateService(autoRepo, nil)
	repo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			10: {ID: 10, Platform: PlatformOpenAI, SubscriptionType: SubscriptionTypeStandard},
			20: {
				ID:               20,
				Platform:         PlatformOpenAI,
				SubscriptionType: SubscriptionTypeStandard,
				RequireOAuthOnly: true,
				AutoRateEnabled:  true,
				AutoRateMode:     GroupAutoRateModePercentage,
				AutoRateValue:    0.1,
			},
		},
	}
	svc := &adminServiceImpl{groupRepo: repo, autoRateService: autoSvc}

	srcID := int64(10)
	oauthOnly := true
	createPreview, err := svc.GetGroupAutoRatePreview(context.Background(), 0, nil, nil, &srcID, &oauthOnly)
	require.NoError(t, err)
	require.Equal(t, int64(1), createPreview.EligibleAccountCount)
	require.NotNil(t, createPreview.ComputedRate)
	require.InDelta(t, 0.20, *createPreview.ComputedRate, 1e-9)

	// Existing group with RequireOAuthOnly=true previewing source group inherits filter when param omitted.
	editCopyPreview, err := svc.GetGroupAutoRatePreview(context.Background(), 20, nil, nil, &srcID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), editCopyPreview.EligibleAccountCount)
	require.NotNil(t, editCopyPreview.ComputedRate)
	require.InDelta(t, 0.22, *editCopyPreview.ComputedRate, 1e-9)

	// Previewing own group (no sourceGroupID) does not filter existing bound accounts.
	ownPreview, err := svc.GetGroupAutoRatePreview(context.Background(), 20, nil, nil, nil, &oauthOnly)
	require.NoError(t, err)
	require.Equal(t, int64(3), ownPreview.EligibleAccountCount)
	require.Equal(t, []summaryCall{
		{groupID: 10, requireOAuthOnly: true},
		{groupID: 10, requireOAuthOnly: true},
		{groupID: 20, requireOAuthOnly: false},
	}, autoRepo.summaryCalls)
}

type autoRateAccountTxTestRepo struct {
	*bulkEventAccountRepo
	account *Account
}

func (r *autoRateAccountTxTestRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *autoRateAccountTxTestRepo) SetSchedulable(_ context.Context, _ int64, schedulable bool) error {
	r.account.Schedulable = schedulable
	return nil
}

func TestAdminService_AccountAutoRateSyncRunsAfterTxCommitWithoutTxContext(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))

	autoRepo := &stubGroupAutoRateRepo{syncErr: errors.New("sync failed")}
	autoSvc := NewGroupAutoRateService(autoRepo, nil)
	acctRepo := &autoRateAccountTxTestRepo{
		bulkEventAccountRepo: newBulkEventAccountRepo(),
		account:              &Account{ID: 7, GroupIDs: []int64{15}},
	}
	svc := &adminServiceImpl{accountRepo: acctRepo, autoRateService: autoSvc}

	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(context.Background(), tx)

	updated, err := svc.SetAccountSchedulable(txCtx, 7, true)
	require.NoError(t, err)
	require.True(t, updated.Schedulable)
	require.Empty(t, autoRepo.syncGroupCalls, "sync must wait until outer tx commits")

	mock.ExpectCommit()
	require.NoError(t, tx.Commit())
	require.Equal(t, [][]int64{{15}}, autoRepo.syncGroupCalls)
	require.Equal(t, []bool{false}, autoRepo.syncGroupTxPresent, "sync must run outside tx context")

	// Rolled back outer tx must not trigger sync.
	autoRepo.syncGroupCalls = nil
	autoRepo.syncGroupTxPresent = nil
	mock.ExpectBegin()
	tx2, err := client.Tx(context.Background())
	require.NoError(t, err)
	_, err = svc.SetAccountSchedulable(dbent.NewTxContext(context.Background(), tx2), 7, false)
	require.NoError(t, err)
	mock.ExpectRollback()
	require.NoError(t, tx2.Rollback())
	require.Empty(t, autoRepo.syncGroupCalls)
	require.NoError(t, mock.ExpectationsWereMet())
}
