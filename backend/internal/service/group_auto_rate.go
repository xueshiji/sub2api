package service

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	GroupAutoRateModePercentage = "percentage"
	GroupAutoRateModeFixed      = "fixed"

	// GroupAutoRateMaxMultiplier bounds automatic group rate write-backs to match
	// upstream billing rate sync safety limits.
	GroupAutoRateMaxMultiplier = 100.0

	GroupAutoRateStatusReady              = "ready"
	GroupAutoRateStatusNoEligibleAccounts = "no_eligible_accounts"
	GroupAutoRateStatusOutOfRange         = "out_of_range"

	groupAutoRateScale       = 10000
	groupAutoRateCentsStep   = 100
	groupAutoRatePercentUnit = 1000000
	groupAutoRateEpsilon     = 1e-9
)

var (
	ErrGroupAutoRateConflict = infraerrors.Conflict(
		"GROUP_AUTO_RATE_CONFLICT",
		"group rate multiplier cannot be changed manually while auto rate is enabled",
	)
)

// GroupAutoRateConfig represents the normalized auto-rate configuration on a group.
type GroupAutoRateConfig struct {
	Platform         string
	SubscriptionType string
	Enabled          bool
	Mode             string
	Value            float64
}

// GroupAutoRateEligibleSummary summarizes the highest-rate eligible account in a group.
type GroupAutoRateEligibleSummary struct {
	EligibleAccountCount int64
	MaxAccountID         *int64
	MaxAccountName       string
	MaxAccountRate       *float64
}

// GroupAutoRatePreview describes the live preview result for auto-rate configuration.
type GroupAutoRatePreview struct {
	Supported            bool     `json:"supported"`
	EligibleAccountCount int64    `json:"eligible_account_count"`
	MaxAccountID         *int64   `json:"max_account_id,omitempty"`
	MaxAccountName       string   `json:"max_account_name,omitempty"`
	MaxAccountRate       *float64 `json:"max_account_rate,omitempty"`
	Mode                 string   `json:"mode"`
	Value                float64  `json:"value"`
	ComputedRate         *float64 `json:"computed_rate,omitempty"`
	Status               string   `json:"status"`
}

// GroupAutoRateRepository defines persistence operations for automatic group rate calculation.
type GroupAutoRateRepository interface {
	GetGroupAutoRateEligibleSummary(ctx context.Context, groupID int64, requireOAuthOnly bool) (*GroupAutoRateEligibleSummary, error)
	SyncAutoRateForGroups(ctx context.Context, groupIDs []int64) ([]int64, error)
	SyncAutoRateForAccounts(ctx context.Context, accountIDs []int64) ([]int64, error)
}

// GroupAutoRateSyncer triggers best-effort automatic group rate synchronization.
type GroupAutoRateSyncer interface {
	SyncAutoRateForGroups(ctx context.Context, groupIDs ...int64) error
	SyncAutoRateForAccounts(ctx context.Context, accountIDs ...int64) error
}

// GroupAutoRateService coordinates automatic group rate calculation and cache invalidation.
type GroupAutoRateService struct {
	repo                 GroupAutoRateRepository
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

// NewGroupAutoRateService creates a GroupAutoRateService instance.
func NewGroupAutoRateService(repo GroupAutoRateRepository, authCacheInvalidator APIKeyAuthCacheInvalidator) *GroupAutoRateService {
	return &GroupAutoRateService{
		repo:                 repo,
		authCacheInvalidator: authCacheInvalidator,
	}
}

// GroupSupportsAutoRate reports whether a group may enable automatic rate calculation.
func GroupSupportsAutoRate(platform, subscriptionType string) bool {
	return NormalizeGroupPlatform(platform) == PlatformOpenAI && subscriptionType == SubscriptionTypeStandard
}

// NormalizeGroupAutoRateConfig normalizes auto-rate configuration before validation/persistence.
// Unsupported platforms or subscription types silently reset auto-rate configuration to disabled.
func NormalizeGroupAutoRateConfig(cfg GroupAutoRateConfig) GroupAutoRateConfig {
	cfg.Platform = NormalizeGroupPlatform(cfg.Platform)
	if cfg.SubscriptionType == "" {
		cfg.SubscriptionType = SubscriptionTypeStandard
	}
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if !GroupSupportsAutoRate(cfg.Platform, cfg.SubscriptionType) || !cfg.Enabled {
		return GroupAutoRateConfig{
			Platform:         cfg.Platform,
			SubscriptionType: cfg.SubscriptionType,
			Enabled:          false,
			Mode:             "",
			Value:            0,
		}
	}
	if !math.IsNaN(cfg.Value) && !math.IsInf(cfg.Value, 0) && cfg.Value >= 0 && cfg.Value <= GroupAutoRateMaxMultiplier {
		cfg.Value = math.Round(cfg.Value*groupAutoRateScale) / groupAutoRateScale
	}
	return cfg
}

// ValidateGroupAutoRateConfig validates normalized auto-rate configuration.
func ValidateGroupAutoRateConfig(cfg GroupAutoRateConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if !GroupSupportsAutoRate(cfg.Platform, cfg.SubscriptionType) {
		return fmt.Errorf("auto rate is only supported for openai standard groups")
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode != GroupAutoRateModePercentage && mode != GroupAutoRateModeFixed {
		return fmt.Errorf("auto_rate_mode must be %q or %q", GroupAutoRateModePercentage, GroupAutoRateModeFixed)
	}
	if math.IsNaN(cfg.Value) || math.IsInf(cfg.Value, 0) || cfg.Value < 0 || cfg.Value > GroupAutoRateMaxMultiplier {
		return fmt.Errorf("auto_rate_value must be between 0 and %v", GroupAutoRateMaxMultiplier)
	}
	return nil
}

// ComputeGroupAutoRate calculates the target group rate rounded UP to 2 decimal places (step 0.01)
// using fixed-point integer arithmetic to eliminate binary floating-point rounding drift.
// Returns (0, false) when inputs are invalid or the computed rate is <= 0 or > GroupAutoRateMaxMultiplier.
func ComputeGroupAutoRate(maxAccountRate float64, mode string, value float64) (float64, bool) {
	if math.IsNaN(maxAccountRate) || math.IsInf(maxAccountRate, 0) || maxAccountRate < 0 || maxAccountRate > GroupAutoRateMaxMultiplier {
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > GroupAutoRateMaxMultiplier {
		return 0, false
	}
	maxScaled := int64(math.Round(maxAccountRate * groupAutoRateScale))
	valScaled := int64(math.Round(value * groupAutoRateScale))

	var cents int64
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case GroupAutoRateModeFixed:
		totalScaled := maxScaled + valScaled
		if totalScaled <= 0 {
			return 0, false
		}
		cents = (totalScaled + groupAutoRateCentsStep - 1) / groupAutoRateCentsStep
	case GroupAutoRateModePercentage:
		product := maxScaled * (groupAutoRateScale + valScaled)
		if product <= 0 {
			return 0, false
		}
		cents = (product + groupAutoRatePercentUnit - 1) / groupAutoRatePercentUnit
	default:
		return 0, false
	}

	result := float64(cents) / 100.0
	if result <= 0 || result > GroupAutoRateMaxMultiplier {
		return 0, false
	}
	return result, true
}

// BuildGroupAutoRatePreview computes a preview snapshot for the given summary and markup rule.
func BuildGroupAutoRatePreview(
	supported bool,
	summary *GroupAutoRateEligibleSummary,
	mode string,
	value float64,
) *GroupAutoRatePreview {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode != GroupAutoRateModePercentage && normalizedMode != GroupAutoRateModeFixed {
		normalizedMode = GroupAutoRateModePercentage
	}
	preview := &GroupAutoRatePreview{
		Supported: supported,
		Mode:      normalizedMode,
		Value:     value,
		Status:    GroupAutoRateStatusNoEligibleAccounts,
	}
	if summary != nil {
		preview.EligibleAccountCount = summary.EligibleAccountCount
		preview.MaxAccountID = summary.MaxAccountID
		preview.MaxAccountName = summary.MaxAccountName
		preview.MaxAccountRate = summary.MaxAccountRate
	}
	if !supported || summary == nil || summary.EligibleAccountCount <= 0 || summary.MaxAccountRate == nil {
		return preview
	}
	computed, ok := ComputeGroupAutoRate(*summary.MaxAccountRate, normalizedMode, value)
	if !ok {
		preview.Status = GroupAutoRateStatusOutOfRange
		return preview
	}
	preview.ComputedRate = &computed
	preview.Status = GroupAutoRateStatusReady
	return preview
}

// GetEligibleSummary returns the highest-rate active schedulable account summary for a group.
func (s *GroupAutoRateService) GetEligibleSummary(ctx context.Context, groupID int64, requireOAuthOnly bool) (*GroupAutoRateEligibleSummary, error) {
	if s == nil || s.repo == nil || groupID <= 0 {
		return &GroupAutoRateEligibleSummary{}, nil
	}
	return s.repo.GetGroupAutoRateEligibleSummary(ctx, groupID, requireOAuthOnly)
}

// SyncAutoRateForGroups recalculates rate_multiplier for the given groups and invalidates auth cache on changes.
func (s *GroupAutoRateService) SyncAutoRateForGroups(ctx context.Context, groupIDs ...int64) error {
	if s == nil || s.repo == nil || len(groupIDs) == 0 {
		return nil
	}
	changed, err := s.repo.SyncAutoRateForGroups(ctx, groupIDs)
	if err != nil {
		return err
	}
	s.invalidateChangedGroups(ctx, changed)
	return nil
}

// SyncAutoRateForAccounts recalculates rate_multiplier for all auto-rate groups bound to the given accounts.
func (s *GroupAutoRateService) SyncAutoRateForAccounts(ctx context.Context, accountIDs ...int64) error {
	if s == nil || s.repo == nil || len(accountIDs) == 0 {
		return nil
	}
	changed, err := s.repo.SyncAutoRateForAccounts(ctx, accountIDs)
	if err != nil {
		return err
	}
	s.invalidateChangedGroups(ctx, changed)
	return nil
}

func (s *GroupAutoRateService) invalidateChangedGroups(ctx context.Context, changed []int64) {
	if s.authCacheInvalidator == nil || len(changed) == 0 {
		return
	}
	for _, groupID := range changed {
		if groupID > 0 {
			s.authCacheInvalidator.InvalidateAuthCacheByGroupID(ctx, groupID)
		}
	}
}

func syncAutoRateBestEffortGroups(ctx context.Context, syncer GroupAutoRateSyncer, source string, groupIDs ...int64) {
	if syncer == nil || len(groupIDs) == 0 {
		return
	}
	if err := syncer.SyncAutoRateForGroups(ctx, groupIDs...); err != nil {
		slog.Warn("group_auto_rate_sync_failed", "source", source, "group_ids", groupIDs, "error", err)
	}
}

func syncAutoRateBestEffortAccounts(ctx context.Context, syncer GroupAutoRateSyncer, source string, accountIDs ...int64) {
	if syncer == nil || len(accountIDs) == 0 {
		return
	}
	if err := syncer.SyncAutoRateForAccounts(ctx, accountIDs...); err != nil {
		slog.Warn("group_auto_rate_sync_failed", "source", source, "account_ids", accountIDs, "error", err)
	}
}
