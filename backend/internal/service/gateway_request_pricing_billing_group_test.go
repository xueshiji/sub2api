package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/cachehitadj"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestWithGatewayTokenRequestBillingGroup_ReplacesFrozenGroupAndKeepsOtherValues(t *testing.T) {
	orig := &Group{ID: 1, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true, CacheHitRateAdjustment: 0.05}
	fallback := &Group{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}

	ctx, pricingAt := WithGatewayTokenRequestPricing(context.WithValue(context.Background(), ctxkey.Group, orig))
	require.Same(t, orig, gatewayTokenRequestBillingGroupFromContext(ctx))

	ctx = WithGatewayTokenRequestBillingGroup(ctx, fallback)

	require.Same(t, fallback, gatewayTokenRequestBillingGroupFromContext(ctx))
	require.Same(t, orig, ctx.Value(ctxkey.Group), "ctxkey.Group stays untouched")
	require.Equal(t, pricingAt, GatewayTokenRequestPricingAtFromContext(ctx), "pricing instant stays frozen")
}

func TestWithGatewayTokenRequestBillingGroup_KeepsFrozenGroupWhenNewGroupInvalid(t *testing.T) {
	orig := &Group{ID: 1, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true, CacheHitRateAdjustment: 0.05}
	for name, group := range map[string]*Group{
		"nil":          nil,
		"zero id":      {ID: 0, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		"not hydrated": {ID: 2, Platform: PlatformAnthropic, Status: StatusActive},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := WithGatewayTokenRequestPricing(context.WithValue(context.Background(), ctxkey.Group, orig))

			ctx = WithGatewayTokenRequestBillingGroup(ctx, group)

			require.Same(t, orig, gatewayTokenRequestBillingGroupFromContext(ctx))
			require.InDelta(t, 0.05, cacheHitRateAdjustmentFromContext(ctx), 1e-12)
		})
	}
}

func TestWithGatewayTokenRequestBillingGroup_NilContextDoesNotPanic(t *testing.T) {
	//nolint:staticcheck // Exercises the defensive nil-context fallback.
	ctx := WithGatewayTokenRequestBillingGroup(nil, nil)

	require.NotNil(t, ctx)
	require.Nil(t, gatewayTokenRequestBillingGroupFromContext(ctx))
}

// prompt too long 回落后计费分组换成兜底分组时，按上下文改写的客户端用量与兜底分组 RecordUsage 落库的主字段一致。
func TestGatewayFallbackBillingGroup_ClientUsageRewriteMatchesRecordUsage(t *testing.T) {
	raw := ClaudeUsage{InputTokens: 3000, OutputTokens: 500, CacheReadInputTokens: 150000, CacheCreationInputTokens: 10000}
	for _, tt := range []struct {
		name               string
		origAdjustment     float64
		fallbackAdjustment float64
		wantInput          int
		wantCacheRead      int
		wantReduction      int
	}{
		{
			name:           "orig group adjusted and fallback group not adjusted",
			origAdjustment: 0.05, fallbackAdjustment: 0,
			wantInput: 3000, wantCacheRead: 150000,
		},
		{
			name:           "orig group not adjusted and fallback group adjusted",
			origAdjustment: 0, fallbackAdjustment: 0.05,
			wantInput: 11150, wantCacheRead: 141850, wantReduction: 8150,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			orig := &Group{ID: 1, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true, CacheHitRateAdjustment: tt.origAdjustment}
			fallback := &Group{
				ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true,
				SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, CacheHitRateAdjustment: tt.fallbackAdjustment,
			}

			pricingCtx, pricingAt := WithGatewayTokenRequestPricing(context.WithValue(context.Background(), ctxkey.Group, orig))
			fallbackCtx := WithGatewayTokenRequestBillingGroup(context.WithValue(pricingCtx, ctxkey.ForcePlatform, ""), fallback)

			clientBody := cachehitadj.RewriteAnthropicUsageJSON(
				[]byte(`{"usage":{"input_tokens":3000,"cache_creation_input_tokens":10000,"cache_read_input_tokens":150000,"output_tokens":500}}`),
				"usage", cacheHitRateAdjustmentFromContext(fallbackCtx),
			)
			clientInput := gjson.GetBytes(clientBody, "usage.input_tokens").Int()
			clientCacheRead := gjson.GetBytes(clientBody, "usage.cache_read_input_tokens").Int()
			require.Equal(t, int64(tt.wantInput), clientInput)
			require.Equal(t, int64(tt.wantCacheRead), clientCacheRead)

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
			accountRate := 1.5
			require.NoError(t, svc.RecordUsage(fallbackCtx, &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: "req-fallback-billing-group", Usage: raw, Model: "claude-sonnet-4", Duration: time.Second,
				},
				APIKey:    &APIKey{ID: 9, GroupID: i64p(fallback.ID), Group: fallback},
				User:      &User{ID: 601},
				Account:   &Account{ID: 701, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 100.0}},
				PricingAt: pricingAt,
			}))

			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, clientInput, int64(usageRepo.lastLog.InputTokens))
			require.Equal(t, clientCacheRead, int64(usageRepo.lastLog.CacheReadTokens))
			require.Equal(t, tt.wantReduction, usageRepo.lastLog.CacheReadTokensReduction)
			require.InDelta(t, tt.fallbackAdjustment, usageRepo.lastLog.CacheHitRateAdjustment, 1e-12)
		})
	}
}
