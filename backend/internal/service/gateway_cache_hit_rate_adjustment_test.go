package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachehitadj"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCacheAdjGatewayServiceForTest(usageRepo UsageLogRepository, billingRepo UsageBillingRepository, rateRepo UserGroupRateRepository) *GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.0
	svc := NewGatewayService(
		nil,
		nil,
		usageRepo,
		nil,
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		rateRepo,
		nil,
		cfg,
		nil,
		nil,
		NewBillingService(cfg, nil),
		nil,
		&BillingCacheService{},
		nil,
		nil,
		&DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	svc.usageBillingRepo = billingRepo
	return svc
}

func TestAnthropicStreaming_PartialUsageMatchesAdjustedBilling(t *testing.T) {
	for _, route := range []string{"gateway", "passthrough", "native"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: 0.05})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			body := "data: " + `{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800}}}` + "\n\n" +
				"data: " + `{"type":"message_delta","usage":{"cache_read_input_tokens":1000,"output_tokens":20}}` + "\n\n" +
				"data: " + `{"type":"message_stop"}` + "\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			defer func() { require.NoError(t, resp.Body.Close()) }()
			svc := &GatewayService{cfg: &config.Config{}}
			var rawUsage ClaudeUsage
			if route == "native" {
				openAISvc := &OpenAIGatewayService{cfg: &config.Config{}}
				result, err := openAISvc.handleNativeAnthropicStreamingResponse(ctx, resp, c, &Account{ID: 1}, "model", "model", "model", nil, time.Now())
				require.NoError(t, err)
				rawUsage = ClaudeUsage{InputTokens: result.Usage.InputTokens - result.Usage.CacheReadInputTokens, CacheReadInputTokens: result.Usage.CacheReadInputTokens, OutputTokens: result.Usage.OutputTokens}
			} else {
				var result *streamingResult
				var err error
				if route == "passthrough" {
					result, err = svc.handleStreamingResponseAnthropicAPIKeyPassthrough(ctx, resp, c, &Account{ID: 1}, time.Now(), "model")
				} else {
					result, err = svc.handleStreamingResponse(ctx, resp, c, &Account{ID: 1}, time.Now(), "model", "model", false)
				}
				require.NoError(t, err)
				rawUsage = *result.usage
			}
			require.Equal(t, ClaudeUsage{InputTokens: 200, CacheReadInputTokens: 1000, OutputTokens: 20}, rawUsage)
			clientUsage := &ClaudeUsage{}
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if data, ok := extractAnthropicSSEDataLine(line); ok {
					svc.parseSSEUsage(data, clientUsage)
				}
			}
			require.Equal(t, ClaudeUsage{InputTokens: 260, CacheReadInputTokens: 940, OutputTokens: 20}, *clientUsage)
		})
	}
}

func TestRecordUsage_ZeroRawCostStoresExplicitAccountCost(t *testing.T) {
	for _, route := range []string{"gateway", "openai"} {
		t.Run(route, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			inputPrice, zeroPrice := 1e-6, 0.0
			groupID := int64(1)
			apiKey := &APIKey{ID: 1, GroupID: &groupID, Group: &Group{
				ID: groupID, CacheHitRateAdjustment: 0.05, RateMultiplier: 1,
				ModelPricing: []ChannelModelPricing{{Models: []string{"custom-free-cache"}, BillingMode: BillingModeToken,
					InputPrice: &inputPrice, OutputPrice: &zeroPrice, CacheReadPrice: &zeroPrice}},
			}}
			account := &Account{ID: 3, Type: AccountTypeAPIKey, Extra: map[string]any{"quota_limit": 100.0}}
			if route == "openai" {
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "zero-raw", Model: "custom-free-cache", Usage: OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 1000}},
					APIKey: apiKey, User: &User{ID: 2}, Account: account,
				})
				require.NoError(t, err)
			} else {
				svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				err := svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{RequestID: "zero-raw", Model: "custom-free-cache", Usage: ClaudeUsage{CacheReadInputTokens: 1000}},
					APIKey: apiKey, User: &User{ID: 2}, Account: account,
				})
				require.NoError(t, err)
			}
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, 50*inputPrice, usageRepo.lastLog.TotalCost, 1e-12)
			require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
			require.Equal(t, 0.0, *usageRepo.lastLog.AccountStatsCost)
			require.NotNil(t, billingRepo.lastCmd)
			require.Equal(t, 0.0, billingRepo.lastCmd.AccountQuotaCost)
		})
	}
}

func TestGatewayServiceRecordUsage_CacheHitRateAdjustmentIsolatesAccountCost(t *testing.T) {
	t.Parallel()

	accountRate := 1.5
	quotaAccount := &Account{
		ID:             701,
		Type:           AccountTypeAPIKey,
		RateMultiplier: &accountRate,
		Extra:          map[string]any{"quota_limit": 100.0},
	}

	t.Run("balance billing applies adjustment and preserves real account cost", func(t *testing.T) {
		t.Parallel()

		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)

		rawUsage := ClaudeUsage{InputTokens: 2000, OutputTokens: 100, CacheReadInputTokens: 8000}
		adjUsage := ClaudeUsage{InputTokens: 2500, OutputTokens: 100, CacheReadInputTokens: 7500}
		rawCost, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
			InputTokens:     rawUsage.InputTokens,
			OutputTokens:    rawUsage.OutputTokens,
			CacheReadTokens: rawUsage.CacheReadInputTokens,
		}, 1.0)
		require.NoError(t, err)
		adjCost, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
			InputTokens:     adjUsage.InputTokens,
			OutputTokens:    adjUsage.OutputTokens,
			CacheReadTokens: adjUsage.CacheReadInputTokens,
		}, 1.0)
		require.NoError(t, err)
		require.Greater(t, adjCost.TotalCost, rawCost.TotalCost)

		err = svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: &ForwardResult{
				RequestID: "req-adj-balance",
				Usage:     rawUsage,
				Model:     "claude-sonnet-4",
				Duration:  time.Second,
			},
			APIKey: &APIKey{
				ID:      501,
				GroupID: i64p(10),
				Group: &Group{
					ID:                     10,
					SubscriptionType:       SubscriptionTypeStandard,
					RateMultiplier:         1.0,
					CacheHitRateAdjustment: 0.05,
				},
			},
			User:    &User{ID: 601},
			Account: quotaAccount,
		})
		require.NoError(t, err)
		require.NotNil(t, billingRepo.lastCmd)
		require.NotNil(t, usageRepo.lastLog)

		logEntry := usageRepo.lastLog
		require.Equal(t, 2500, logEntry.InputTokens)
		require.Equal(t, 7500, logEntry.CacheReadTokens)
		require.InDelta(t, 0.05, logEntry.CacheHitRateAdjustment, 1e-9)
		require.Equal(t, 500, logEntry.CacheReadTokensReduction)
		require.InDelta(t, adjCost.TotalCost, logEntry.TotalCost, 1e-12)
		require.InDelta(t, adjCost.ActualCost, logEntry.ActualCost, 1e-12)
		require.NotNil(t, logEntry.AccountStatsCost)
		require.InDelta(t, rawCost.TotalCost, *logEntry.AccountStatsCost, 1e-12)
		require.InDelta(t, adjCost.ActualCost, billingRepo.lastCmd.BalanceCost, 1e-12)
		require.InDelta(t, rawCost.TotalCost*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)
	})

	t.Run("subscription USD and token modes use adjusted tokens and keep real account cost", func(t *testing.T) {
		t.Parallel()

		for _, subType := range []string{SubscriptionTypeSubscription, SubscriptionTypeSubscriptionToken} {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)

			rawUsage := ClaudeUsage{InputTokens: 2000, OutputTokens: 100, CacheReadInputTokens: 8000}
			rawCost, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
				InputTokens:     rawUsage.InputTokens,
				OutputTokens:    rawUsage.OutputTokens,
				CacheReadTokens: rawUsage.CacheReadInputTokens,
			}, 1.0)
			require.NoError(t, err)
			adjCost, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
				InputTokens:     2500,
				OutputTokens:    100,
				CacheReadTokens: 7500,
			}, 1.0)
			require.NoError(t, err)

			err = svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: "req-adj-sub-" + subType,
					Usage:     rawUsage,
					Model:     "claude-sonnet-4",
					Duration:  time.Second,
				},
				APIKey: &APIKey{
					ID:      502,
					GroupID: i64p(11),
					Group: &Group{
						ID:                     11,
						SubscriptionType:       subType,
						RateMultiplier:         2.0,
						CacheHitRateAdjustment: 0.05,
					},
				},
				User:         &User{ID: 602},
				Account:      quotaAccount,
				Subscription: &UserSubscription{ID: 901},
			})
			require.NoError(t, err)
			require.NotNil(t, billingRepo.lastCmd)
			require.NotNil(t, usageRepo.lastLog)

			require.Equal(t, 2500, usageRepo.lastLog.InputTokens)
			require.Equal(t, 7500, usageRepo.lastLog.CacheReadTokens)
			require.Equal(t, 500, usageRepo.lastLog.CacheReadTokensReduction)
			require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
			require.InDelta(t, rawCost.TotalCost, *usageRepo.lastLog.AccountStatsCost, 1e-12)
			require.InDelta(t, rawCost.TotalCost*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)

			if subType == SubscriptionTypeSubscription {
				require.InDelta(t, adjCost.TotalCost*2.0, billingRepo.lastCmd.SubscriptionCost, 1e-12)
			} else {
				// (2500 + 100 + 7500) * 2.0 = 20200
				require.Equal(t, int64(20200), billingRepo.lastCmd.SubscriptionTokens)
			}
		}
	})

	t.Run("force cache billing converts first and then applies adjustment", func(t *testing.T) {
		t.Parallel()

		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)

		rawCostAfterForceCache, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
			InputTokens:     0,
			OutputTokens:    100,
			CacheReadTokens: 10000,
		}, 1.0)
		require.NoError(t, err)

		err = svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: &ForwardResult{
				RequestID: "req-adj-force-cache",
				Usage:     ClaudeUsage{InputTokens: 2000, OutputTokens: 100, CacheReadInputTokens: 8000},
				Model:     "claude-sonnet-4",
				Duration:  time.Second,
			},
			APIKey: &APIKey{
				ID:      503,
				GroupID: i64p(12),
				Group: &Group{
					ID:                     12,
					SubscriptionType:       SubscriptionTypeStandard,
					RateMultiplier:         1.0,
					CacheHitRateAdjustment: 0.05,
				},
			},
			User:              &User{ID: 603},
			Account:           quotaAccount,
			ForceCacheBilling: true,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.Equal(t, 500, usageRepo.lastLog.InputTokens)
		require.Equal(t, 9500, usageRepo.lastLog.CacheReadTokens)
		require.Equal(t, 500, usageRepo.lastLog.CacheReadTokensReduction)
		require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
		require.InDelta(t, rawCostAfterForceCache.TotalCost, *usageRepo.lastLog.AccountStatsCost, 1e-12)
		require.InDelta(t, rawCostAfterForceCache.TotalCost*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)
	})

	t.Run("threshold at or below 10 percent does not apply adjustment", func(t *testing.T) {
		t.Parallel()

		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
		svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)

		err := svc.RecordUsage(context.Background(), &RecordUsageInput{
			Result: &ForwardResult{
				RequestID: "req-adj-threshold-10pct",
				Usage:     ClaudeUsage{InputTokens: 9000, OutputTokens: 100, CacheReadInputTokens: 1000},
				Model:     "claude-sonnet-4",
				Duration:  time.Second,
			},
			APIKey: &APIKey{
				ID:      504,
				GroupID: i64p(13),
				Group: &Group{
					ID:                     13,
					SubscriptionType:       SubscriptionTypeStandard,
					RateMultiplier:         1.0,
					CacheHitRateAdjustment: 0.05,
				},
			},
			User:    &User{ID: 604},
			Account: quotaAccount,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.Equal(t, 9000, usageRepo.lastLog.InputTokens)
		require.Equal(t, 1000, usageRepo.lastLog.CacheReadTokens)
		require.Equal(t, 0.0, usageRepo.lastLog.CacheHitRateAdjustment)
		require.Equal(t, 0, usageRepo.lastLog.CacheReadTokensReduction)
		require.Nil(t, usageRepo.lastLog.AccountStatsCost)
	})
}

func TestGatewayServiceQuoteUsageCost_AppliesCacheHitAdjustment(t *testing.T) {
	t.Parallel()

	svc := newCacheAdjGatewayServiceForTest(nil, nil, nil)
	rawUsage := ClaudeUsage{InputTokens: 2000, OutputTokens: 100, CacheReadInputTokens: 8000}
	apiKey := &APIKey{
		ID: 505,
		Group: &Group{
			ID:                     14,
			RateMultiplier:         1.0,
			CacheHitRateAdjustment: 0.05,
		},
	}

	quoted := svc.QuoteUsageCost(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{Model: "claude-sonnet-4", Usage: rawUsage},
		APIKey: apiKey,
		User:   &User{ID: 605},
	})
	expectedAdj, err := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
		InputTokens:     2500,
		OutputTokens:    100,
		CacheReadTokens: 7500,
	}, 1.0)
	require.NoError(t, err)
	require.InDelta(t, expectedAdj.ActualCost, quoted, 1e-12)
}

func TestOpenAIGatewayServiceRecordUsage_CacheHitRateAdjustmentIsolatesAccountCost(t *testing.T) {
	t.Parallel()

	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc.usageBillingRepo = billingRepo

	// OpenAI input_tokens (10000) includes cached_tokens (8000) -> raw actual input = 2000, cache_read = 8000
	rawCost := expectedOpenAICost(t, svc, "gpt-5.1", OpenAIUsage{
		InputTokens:          10000,
		OutputTokens:         200,
		CacheReadInputTokens: 8000,
	}, 1.0)
	adjCost := expectedOpenAICost(t, svc, "gpt-5.1", OpenAIUsage{
		InputTokens:          10000,
		OutputTokens:         200,
		CacheReadInputTokens: 7500,
	}, 1.0)
	require.Greater(t, adjCost.TotalCost, rawCost.TotalCost)

	accountRate := 1.4
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "req-openai-adj",
			Model:     "gpt-5.1",
			Usage: OpenAIUsage{
				InputTokens:          10000,
				OutputTokens:         200,
				CacheReadInputTokens: 8000,
			},
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:      510,
			GroupID: i64p(20),
			Group: &Group{
				ID:                     20,
				SubscriptionType:       SubscriptionTypeStandard,
				RateMultiplier:         1.0,
				CacheHitRateAdjustment: 0.05,
			},
		},
		User: &User{ID: 610},
		Account: &Account{
			ID:             710,
			Type:           AccountTypeAPIKey,
			RateMultiplier: &accountRate,
			Extra:          map[string]any{"quota_limit": 200.0},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, billingRepo.lastCmd)

	logEntry := usageRepo.lastLog
	require.Equal(t, 2500, logEntry.InputTokens)
	require.Equal(t, 7500, logEntry.CacheReadTokens)
	require.InDelta(t, 0.05, logEntry.CacheHitRateAdjustment, 1e-9)
	require.Equal(t, 500, logEntry.CacheReadTokensReduction)
	require.InDelta(t, adjCost.TotalCost, logEntry.TotalCost, 1e-12)
	require.InDelta(t, adjCost.ActualCost, logEntry.ActualCost, 1e-12)
	require.NotNil(t, logEntry.AccountStatsCost)
	require.InDelta(t, rawCost.TotalCost, *logEntry.AccountStatsCost, 1e-12)
	require.InDelta(t, rawCost.TotalCost*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)
}

// 不得给本用例加 t.Parallel()：gin.SetMode 改写进程级状态。
func TestGatewayCacheHitAdjustment_LiveResponseRewrites(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("Anthropic JSON and SSE", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		writeJSONWithUsageRewrite(c, 200, map[string]any{
			"id":    "msg_1",
			"usage": map[string]any{"input_tokens": 200, "cache_read_input_tokens": 800, "output_tokens": 50},
		}, 0.05, func(b []byte, adj float64) []byte {
			return cachehitadj.RewriteAnthropicUsageJSON(b, "usage", adj)
		})
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
		usage, ok := decoded["usage"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, float64(250), usage["input_tokens"])
		require.Equal(t, float64(750), usage["cache_read_input_tokens"])

		sseLine := `data: {"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":0}}}` + "\n"
		rewriter := &cachehitadj.AnthropicStreamRewriter{}
		rewrittenLine := rewriteSSEDataLine(sseLine, 0.05, rewriter.Rewrite)
		require.Contains(t, rewrittenLine, `"input_tokens":250`)
		require.Contains(t, rewrittenLine, `"cache_read_input_tokens":750`)
	})

	t.Run("OpenAI JSON and SSE block", func(t *testing.T) {
		t.Parallel()
		sseBlock := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":20,\"input_tokens_details\":{\"cached_tokens\":800}}}}\n\n"
		rewritten := rewriteSSEBlock(sseBlock, 0.05, cachehitadj.RewriteOpenAIUsageJSON)
		require.Contains(t, rewritten, `"input_tokens":1000`)
		require.Contains(t, rewritten, `"cached_tokens":750`)

		ccLine := `data: {"id":"chatcmpl-1","usage":{"prompt_tokens":1000,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":800}}}` + "\n"
		rewrittenCC := rewriteSSEDataLine(ccLine, 0.05, cachehitadj.RewriteOpenAIUsageJSON)
		require.Contains(t, rewrittenCC, `"prompt_tokens":1000`)
		require.Contains(t, rewrittenCC, `"cached_tokens":750`)
	})

	t.Run("Gemini JSON and SSE", func(t *testing.T) {
		t.Parallel()
		geminiJSON := []byte(`{"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":50}}`)
		rewritten := cachehitadj.RewriteGeminiUsageJSON(geminiJSON, 0.05)
		require.Contains(t, string(rewritten), `"promptTokenCount":1000`)
		require.Contains(t, string(rewritten), `"cachedContentTokenCount":750`)
	})
}
