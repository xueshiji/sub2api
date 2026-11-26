package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// usageBillingWindowCostChannelService 构造只含一个分组渠道映射的 ChannelService，
// 账号统计定价因此走定价文件价。
func usageBillingWindowCostChannelService(groupID int64, platform string) *ChannelService {
	cache := newEmptyChannelCache()
	cache.channelByGroupID[groupID] = &Channel{ID: 1, Status: StatusActive}
	cache.groupPlatform[groupID] = platform
	cache.loadedAt = time.Now()
	cs := &ChannelService{}
	cs.cache.Store(cache)
	return cs
}

// usageBillingDoubledSellingPriceGroup 返回把 model 售价定为定价文件两倍的分组。
func usageBillingDoubledSellingPriceGroup(t *testing.T, bs *BillingService, groupID int64, adjustment float64, model string) *Group {
	t.Helper()
	pricing, err := bs.GetModelPricing(model)
	require.NoError(t, err)
	double := func(v float64) *float64 { x := 2 * v; return &x }
	return &Group{
		ID: groupID, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, CacheHitRateAdjustment: adjustment,
		ModelPricing: []ChannelModelPricing{{Models: []string{model}, BillingMode: BillingModeToken,
			InputPrice:     double(pricing.InputPricePerToken),
			OutputPrice:    double(pricing.OutputPricePerToken),
			CacheReadPrice: double(pricing.CacheReadPricePerToken),
		}},
	}
}

// 分组售价与定价文件不同时，修正生效的用量行把售价口径的修正前成本写入 total_cost_before_adjustment，
// 账号统计成本仍按定价文件价记；未修正的行不写该列。
func TestRecordUsage_AdjustedRowStoresSellingPriceCostBeforeAdjustment(t *testing.T) {
	const (
		model           = "kimi-k2"
		inputTokens     = 1000
		cacheReadTokens = 90000
		outputTokens    = 6250
	)
	groupID := int64(31)

	// fileCostOf 返回按定价文件价、修正前 token 算出的成本。
	fileCostOf := func(t *testing.T, bs *BillingService) float64 {
		cost, err := bs.CalculateCost(model, UsageTokens{InputTokens: inputTokens, CacheReadTokens: cacheReadTokens, OutputTokens: outputTokens}, 1.0)
		require.NoError(t, err)
		require.Positive(t, cost.TotalCost)
		return cost.TotalCost
	}

	routes := []struct {
		name string
		run  func(t *testing.T, adjustment float64) (*UsageLog, float64)
	}{
		{
			name: "gateway",
			run: func(t *testing.T, adjustment float64) (*UsageLog, float64) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newCacheAdjGatewayServiceForTest(usageRepo, &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				svc.channelService = usageBillingWindowCostChannelService(groupID, PlatformAnthropic)
				err := svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{RequestID: "before-cost-gateway", Model: model,
						Usage: ClaudeUsage{InputTokens: inputTokens, CacheReadInputTokens: cacheReadTokens, OutputTokens: outputTokens}, Duration: time.Second},
					APIKey:  &APIKey{ID: 1, GroupID: &groupID, Group: usageBillingDoubledSellingPriceGroup(t, svc.billingService, groupID, adjustment, model)},
					User:    &User{ID: 2},
					Account: &Account{ID: 3, Type: AccountTypeAPIKey},
				})
				require.NoError(t, err)
				return usageRepo.lastLog, fileCostOf(t, svc.billingService)
			},
		},
		{
			name: "openai",
			run: func(t *testing.T, adjustment float64) (*UsageLog, float64) {
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				svc.channelService = usageBillingWindowCostChannelService(groupID, PlatformAnthropic)
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "before-cost-openai", Model: model, UpstreamModel: model,
						Usage: OpenAIUsage{InputTokens: inputTokens + cacheReadTokens, CacheReadInputTokens: cacheReadTokens, OutputTokens: outputTokens}, Duration: time.Second},
					APIKey:  &APIKey{ID: 1, GroupID: &groupID, Group: usageBillingDoubledSellingPriceGroup(t, svc.billingService, groupID, adjustment, model)},
					User:    &User{ID: 2},
					Account: &Account{ID: 3, Type: AccountTypeAPIKey},
				})
				require.NoError(t, err)
				return usageRepo.lastLog, fileCostOf(t, svc.billingService)
			},
		},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			log, fileCost := route.run(t, 0.05)
			require.Positive(t, log.CacheReadTokensReduction)
			require.Greater(t, log.TotalCost, 2*fileCost, "修正后的用户侧成本高于修正前")
			require.NotNil(t, log.AccountStatsCost)
			require.InDelta(t, fileCost, *log.AccountStatsCost, 1e-12)
			require.NotNil(t, log.TotalCostBeforeAdjustment)
			require.InDelta(t, 2*fileCost, *log.TotalCostBeforeAdjustment, 1e-12)

			log, fileCost = route.run(t, 0)
			require.Zero(t, log.CacheReadTokensReduction)
			require.Nil(t, log.TotalCostBeforeAdjustment)
			require.InDelta(t, 2*fileCost, log.TotalCost, 1e-12)
		})
	}
}
