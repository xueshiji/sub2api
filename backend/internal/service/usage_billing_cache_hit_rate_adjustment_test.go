package service

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type usageBillingSlogCapture struct {
	records []slog.Record
}

func (h *usageBillingSlogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *usageBillingSlogCapture) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *usageBillingSlogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *usageBillingSlogCapture) WithGroup(string) slog.Handler      { return h }

type usageBillingTokens struct {
	input     int
	cacheRead int
	output    int
}

func (tk usageBillingTokens) shifted(delta int) usageBillingTokens {
	return usageBillingTokens{input: tk.input + delta, cacheRead: tk.cacheRead - delta, output: tk.output}
}

// usageBillingTokenCost 按仓库兜底价表（$/MTok）计价：kimi-k2 input 0.56 / cache_read 0.14 / output 2.24，
// kimi-k2.5 input 0.60 / cache_read 0.098 / output 3.00，jev-latest 仅收 input 0.042。
func usageBillingTokenCost(t *testing.T, bs *BillingService, model string, tk usageBillingTokens) *CostBreakdown {
	t.Helper()
	cost, err := bs.CalculateCost(model, UsageTokens{InputTokens: tk.input, CacheReadTokens: tk.cacheRead, OutputTokens: tk.output}, 1.0)
	require.NoError(t, err)
	return cost
}

// 采纳判定依赖输入与缓存的拆分：同一请求在修正后 token 与原始 token 下可能得出相反结论，
// 账号侧成本必须沿用用户账单（修正后 token）采纳的计费模型。
func TestGatewayServiceRecordUsage_AccountCostFollowsResponseModelAdoptedForAdjustedTokens(t *testing.T) {
	cases := []struct {
		name            string
		baseline        string
		response        string
		raw             usageBillingTokens
		delta           int
		wantModel       string // 修正后 token 的采纳判定给出的计费模型
		wantAppliedLogs int
	}{
		{
			name:            "原始 token 下响应模型更贵而修正后更便宜时账号侧按响应模型计费",
			baseline:        "kimi-k2.5",
			response:        "kimi-k2",
			raw:             usageBillingTokens{input: 10000, cacheRead: 90000, output: 4000},
			delta:           10000,
			wantModel:       "kimi-k2",
			wantAppliedLogs: 1,
		},
		{
			name:            "原始 token 下响应模型更便宜而修正后更贵时账号侧按基线模型计费",
			baseline:        "kimi-k2",
			response:        "kimi-k2.5",
			raw:             usageBillingTokens{input: 10000, cacheRead: 90000, output: 4000},
			delta:           10000,
			wantModel:       "kimi-k2",
			wantAppliedLogs: 0,
		},
		{
			name:            "原始 input 为 0 时响应模型成本为 0 而修正后大于 0 时账号侧按响应模型计费",
			baseline:        "kimi-k2.5",
			response:        "jev-latest",
			raw:             usageBillingTokens{input: 0, cacheRead: 1000, output: 100},
			delta:           100,
			wantModel:       "jev-latest",
			wantAppliedLogs: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
			adjusted := tc.raw.shifted(tc.delta)
			require.True(t, svc.billingService.HasIdentifiedTokenPricing(tc.baseline))
			require.True(t, svc.billingService.HasIdentifiedTokenPricing(tc.response))

			cost := func(model string, tk usageBillingTokens) *CostBreakdown {
				return usageBillingTokenCost(t, svc.billingService, model, tk)
			}
			require.NotEqual(t, cost(tc.baseline, tc.raw).TotalCost, cost(tc.response, tc.raw).TotalCost, "两个模型在原始 token 下必须不同价")
			adoptAt := func(tk usageBillingTokens) bool {
				return responseModelBillingAdoptable(cost(tc.baseline, tk), cost(tc.response, tk), false, false)
			}
			require.Equal(t, tc.wantModel == tc.response, adoptAt(adjusted))
			require.NotEqual(t, tc.wantModel == tc.response, adoptAt(tc.raw), "原始 token 下的采纳结论必须与修正后 token 相反")

			capture := &usageBillingSlogCapture{}
			prev := slog.Default()
			slog.SetDefault(slog.New(capture))
			defer slog.SetDefault(prev)

			accountRate := 1.5
			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID:             "response-model-adjusted-" + tc.response,
					Usage:                 ClaudeUsage{InputTokens: tc.raw.input, CacheReadInputTokens: tc.raw.cacheRead, OutputTokens: tc.raw.output},
					Model:                 tc.baseline,
					UpstreamResponseModel: tc.response,
					Duration:              time.Second,
				},
				APIKey: &APIKey{
					ID:      501,
					GroupID: i64p(10),
					Group: &Group{
						ID:                     10,
						SubscriptionType:       SubscriptionTypeStandard,
						RateMultiplier:         1.0,
						CacheHitRateAdjustment: 0.10,
					},
				},
				User:    &User{ID: 601},
				Account: &Account{ID: 701, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 100.0}},
				ChannelUsageFields: ChannelUsageFields{
					ChannelID:          9,
					OriginalModel:      tc.baseline,
					ChannelMappedModel: tc.baseline,
					BillingModelSource: BillingModelSourceResponse,
				},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.NotNil(t, billingRepo.lastCmd)

			wantUser := cost(tc.wantModel, adjusted).TotalCost
			wantRaw := cost(tc.wantModel, tc.raw).TotalCost
			require.Equal(t, tc.delta, usageRepo.lastLog.CacheReadTokensReduction)
			require.InDelta(t, wantUser, usageRepo.lastLog.TotalCost, 1e-12)
			require.InDelta(t, wantUser, usageRepo.lastLog.ActualCost, 1e-12)
			require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
			require.InDelta(t, wantRaw, *usageRepo.lastLog.AccountStatsCost, 1e-12)
			require.InDelta(t, wantRaw*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)

			applied := 0
			for _, r := range capture.records {
				if r.Message == "billing.response_model_applied" {
					applied++
				}
			}
			require.Equal(t, tc.wantAppliedLogs, applied, "billing.response_model_applied 日志条数")
		})
	}
}

// 同一组 token 走 OpenAI 计费主干时，账号侧成本同样沿用采纳的响应模型。
func TestOpenAIGatewayServiceRecordUsage_AccountCostFollowsResponseModelAdoptedForAdjustedTokens(t *testing.T) {
	const baseline, response = "kimi-k2.5", "kimi-k2"
	raw := usageBillingTokens{input: 10000, cacheRead: 90000, output: 4000}
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)

	accountRate := 1.5
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:             "openai-response-model-adjusted",
			Model:                 baseline,
			UpstreamModel:         baseline,
			UpstreamResponseModel: response,
			Usage:                 OpenAIUsage{InputTokens: 100000, CacheReadInputTokens: 90000, OutputTokens: 4000},
			Duration:              time.Second,
		},
		APIKey: &APIKey{
			ID:      510,
			GroupID: i64p(20),
			Group:   &Group{ID: 20, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1.0, CacheHitRateAdjustment: 0.10},
		},
		User:    &User{ID: 610},
		Account: &Account{ID: 710, Type: AccountTypeAPIKey, RateMultiplier: &accountRate, Extra: map[string]any{"quota_limit": 200.0}},
		ChannelUsageFields: ChannelUsageFields{
			ChannelID:          9,
			OriginalModel:      baseline,
			ChannelMappedModel: baseline,
			BillingModelSource: BillingModelSourceResponse,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, billingRepo.lastCmd)

	wantUser := usageBillingTokenCost(t, svc.billingService, response, raw.shifted(10000)).TotalCost
	wantRaw := usageBillingTokenCost(t, svc.billingService, response, raw).TotalCost
	require.NotEqual(t, wantRaw, usageBillingTokenCost(t, svc.billingService, baseline, raw).TotalCost)
	require.Equal(t, 10000, usageRepo.lastLog.CacheReadTokensReduction)
	require.InDelta(t, wantUser, usageRepo.lastLog.TotalCost, 1e-12)
	require.NotNil(t, usageRepo.lastLog.AccountStatsCost)
	require.InDelta(t, wantRaw, *usageRepo.lastLog.AccountStatsCost, 1e-12)
	require.InDelta(t, wantRaw*accountRate, billingRepo.lastCmd.AccountQuotaCost, 1e-12)
}

// usageBillingImageBody 构造 Images API 响应体；流式直调按 completed 事件里是否带 type 识别图片产出。
func usageBillingImageBody(usage string) []byte {
	return []byte(`{"created":1710000000,"data":[{"b64_json":"aGVsbG8=","output_format":"png","size":"1024x1024"}],"type":"image_generation.completed","model":"gpt-image-2",` + usage + `}`)
}

// usageBillingImageCatalogPricing 与仓库价目文件中 gpt-image-2 的价目一致。
func usageBillingImageCatalogPricing() *PricingService {
	return &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-image-2": {
			Mode:                         "image_generation",
			InputCostPerToken:            5e-6,
			OutputCostPerToken:           10e-6,
			InputCostPerImageToken:       8e-6,
			OutputCostPerImageToken:      30e-6,
			CacheReadInputTokenCost:      1.25e-6,
			CacheReadInputImageTokenCost: 2e-6,
		},
	}}
}

const usageBillingImageGroupID = 77

// usageBillingChannelServiceForStats 构造只含一个分组渠道映射的 ChannelService（账号统计定价用）。
func usageBillingChannelServiceForStats(channel *Channel, groupID int64, platform string) *ChannelService {
	cache := newEmptyChannelCache()
	cache.channelByGroupID[groupID] = channel
	cache.groupPlatform[groupID] = platform
	cache.loadedAt = time.Now()
	cs := &ChannelService{}
	cs.cache.Store(cache)
	return cs
}

func usageBillingRecordImageUsage(t *testing.T, usage OpenAIUsage, adjustment float64, tokenCard bool, channelService *ChannelService) *UsageLog {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.billingService = NewBillingService(&config.Config{}, usageBillingImageCatalogPricing())
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	svc.channelService = channelService

	group := &Group{ID: usageBillingImageGroupID, RateMultiplier: 1.0, CacheHitRateAdjustment: adjustment, ImagePrice2K: f64p(0.04)}
	if tokenCard {
		// 管理员为 gpt-image-2 配了 token 模式价卡、未覆盖任何单价
		group.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-image-2"}, BillingMode: BillingModeToken}}
	}
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "image-cache-adj", Model: "gpt-image-2", Usage: usage,
			ImageCount: 1, ImageEndpoint: true, ImageSize: "1024x1024", Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1, GroupID: i64p(usageBillingImageGroupID), Group: group},
		User:    &User{ID: 2},
		Account: &Account{ID: 3, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	return usageRepo.lastLog
}

// 图片端点的响应 usage 原样透传，用量行与成本不随分组缓存命中率修正变化。
func TestOpenAIGatewayServiceRecordUsage_ImageEndpointSkipsCacheHitRateAdjustment(t *testing.T) {
	cases := []struct {
		name   string
		usage  string // 上游 usage JSON
		stream bool
		apiKey bool // false：Codex 直调路径；true：API Key 路径
	}{
		{
			name:  "直调_仅 cached_tokens_details",
			usage: `"usage":{"input_tokens":1100,"input_tokens_details":{"text_tokens":200,"image_tokens":900,"cached_tokens_details":{"image_tokens":900}},"output_tokens":1056,"output_tokens_details":{"image_tokens":1056}}`,
		},
		{
			name:  "直调_cached_tokens 与 cached_tokens_details",
			usage: `"usage":{"input_tokens":100,"input_tokens_details":{"text_tokens":20,"image_tokens":80,"cached_tokens":50,"cached_tokens_details":{"text_tokens":10,"image_tokens":40}},"output_tokens":200}`,
		},
		{
			name:   "APIKey_cached_tokens",
			usage:  `"usage":{"input_tokens":1100,"input_tokens_details":{"text_tokens":200,"image_tokens":900,"cached_tokens":400},"output_tokens":1056,"output_tokens_details":{"image_tokens":1056}}`,
			apiKey: true,
		},
		{
			name:   "直调流式_仅 cached_tokens_details",
			usage:  `"usage":{"input_tokens":1100,"input_tokens_details":{"text_tokens":200,"image_tokens":900,"cached_tokens_details":{"image_tokens":900}},"output_tokens":1056,"output_tokens_details":{"image_tokens":1056}}`,
			stream: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := usageBillingImageBody(tc.usage)
			upstreamBody := payload
			contentType := "application/json"
			if tc.stream {
				upstreamBody = []byte("event: image_generation.completed\ndata: " + string(payload) + "\n\n")
				contentType = "text/event-stream"
			}
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
			svc := newOpenAIImagesTestService(nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
			}
			parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, Model: "gpt-image-2", ResponseFormat: "b64_json", Size: "1024x1024"}
			var (
				usage OpenAIUsage
				count int
				err   error
			)
			if tc.stream {
				usage, count, _, _, err = svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), parsed)
			} else if tc.apiKey {
				usage, count, _, err = svc.handleOpenAIImagesNonStreamingResponse(context.Background(), resp, c, newOpenAIImagesAPIKeyAccount(), parsed)
			} else {
				usage, count, _, err = svc.handleCodexDirectImagesNonStreamingResponse(resp, c, parsed)
			}
			require.NoError(t, err)
			require.Equal(t, 1, count)
			require.Positive(t, usage.CacheReadInputTokens)
			// 客户端拿到的 usage 与上游一致
			upstreamUsage := gjson.GetBytes(payload, "usage").Raw
			require.NotEmpty(t, upstreamUsage)
			if tc.stream {
				require.Contains(t, rec.Body.String(), upstreamUsage)
			} else {
				require.Equal(t, upstreamUsage, gjson.GetBytes(rec.Body.Bytes(), "usage").Raw)
			}

			for _, tokenCard := range []bool{true, false} {
				baseline := usageBillingRecordImageUsage(t, usage, 0, tokenCard, nil)
				wantMode := string(BillingModeImage)
				if tokenCard {
					wantMode = string(BillingModeToken)
				}
				require.NotNil(t, baseline.BillingMode)
				require.Equal(t, wantMode, *baseline.BillingMode)
				require.Positive(t, baseline.TotalCost)
				require.Zero(t, baseline.CacheReadTokensReduction)
				for _, adjustment := range []float64{0.05, 0.10} {
					got := usageBillingRecordImageUsage(t, usage, adjustment, tokenCard, nil)
					require.Equal(t, baseline.InputTokens, got.InputTokens)
					require.Equal(t, baseline.CacheReadTokens, got.CacheReadTokens)
					require.Zero(t, got.CacheHitRateAdjustment)
					require.Zero(t, got.CacheReadTokensReduction)
					require.Nil(t, got.AccountStatsCost)
					require.Equal(t, baseline.BillingMode, got.BillingMode)
					require.InDelta(t, baseline.TotalCost, got.TotalCost, 1e-12)
					require.InDelta(t, baseline.ActualCost, got.ActualCost, 1e-12)
				}
			}
		})
	}

	t.Run("同一用量不带图片端点标记时按比例修正", func(t *testing.T) {
		usage := OpenAIUsage{InputTokens: 1100, ImageInputTokens: 900, ImageCacheReadTokens: 900, CacheReadInputTokens: 900, OutputTokens: 1056, ImageOutputTokens: 1056}
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		svc.billingService = NewBillingService(&config.Config{}, usageBillingImageCatalogPricing())
		svc.resolver = NewModelPricingResolver(nil, svc.billingService)
		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{
				RequestID: "mixed-image-cache-adj", Model: "gpt-image-2", Usage: usage,
				ImageCount: 1, ImageSize: "1024x1024", Duration: time.Second,
			},
			APIKey: &APIKey{ID: 1, GroupID: i64p(usageBillingImageGroupID), Group: &Group{
				ID: usageBillingImageGroupID, RateMultiplier: 1.0, CacheHitRateAdjustment: 0.05,
				ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-image-2"}, BillingMode: BillingModeToken}},
			}},
			User:    &User{ID: 2},
			Account: &Account{ID: 3, Type: AccountTypeAPIKey},
		})
		require.NoError(t, err)
		require.Equal(t, 55, usageRepo.lastLog.CacheReadTokensReduction)
	})

	t.Run("自定义账号统计定价照常按原始 token 计算", func(t *testing.T) {
		inputPrice, outputPrice, cacheReadPrice := 1e-6, 2e-6, 0.5e-6
		channelService := usageBillingChannelServiceForStats(&Channel{
			ID: 1, Status: StatusActive,
			AccountStatsPricingRules: []AccountStatsPricingRule{{
				GroupIDs: []int64{usageBillingImageGroupID},
				Pricing:  []ChannelModelPricing{{Models: []string{"gpt-image-2"}, InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &cacheReadPrice}},
			}},
		}, usageBillingImageGroupID, PlatformOpenAI)
		usage := OpenAIUsage{InputTokens: 1100, ImageInputTokens: 900, ImageCacheReadTokens: 900, CacheReadInputTokens: 900, OutputTokens: 1056, ImageOutputTokens: 1056}
		want := float64(200)*inputPrice + float64(1056)*outputPrice + float64(900)*cacheReadPrice

		baseline := usageBillingRecordImageUsage(t, usage, 0, true, channelService)
		adjusted := usageBillingRecordImageUsage(t, usage, 0.05, true, channelService)
		for _, log := range []*UsageLog{baseline, adjusted} {
			require.Equal(t, 200, log.InputTokens)
			require.Equal(t, 900, log.CacheReadTokens)
			require.Zero(t, log.CacheReadTokensReduction)
			require.NotNil(t, log.AccountStatsCost)
			require.InDelta(t, want, *log.AccountStatsCost, 1e-12)
		}
	})
}

// embeddings 响应里的 cached_tokens 按分组修正改写，计费解析仍取上游原始值，两者扣减量一致。
func TestForwardEmbeddings_CacheHitRateAdjustmentRewritesClientUsage(t *testing.T) {
	upstreamBody := []byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1000,"total_tokens":1000,"prompt_tokens_details":{"cached_tokens":600}}}`)
	const inputPrice, cacheReadPrice = 0.02e-6, 0.005e-6

	for _, tc := range []struct {
		name       string
		adjustment float64
		wantCached int
	}{
		{name: "分组未配置修正时原样透传", adjustment: 0, wantCached: 600},
		{name: "分组配置 5% 修正时客户端 cached_tokens 减少 50", adjustment: 0.05, wantCached: 550},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			groupID := int64(78)
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: groupID, CacheHitRateAdjustment: tc.adjustment})
			reqBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")

			svc := &OpenAIGatewayService{
				cfg: &config.Config{},
				httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
				}},
			}
			account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "sk-test", "base_url": "https://api.openai.com/v1",
			}}
			result, err := svc.ForwardEmbeddings(ctx, c, account, reqBody, "")
			require.NoError(t, err)
			require.Equal(t, OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 600}, result.Usage)

			clientUsage := gjson.GetBytes(rec.Body.Bytes(), "usage")
			require.Equal(t, int64(1000), clientUsage.Get("prompt_tokens").Int())
			require.Equal(t, int64(tc.wantCached), clientUsage.Get("prompt_tokens_details.cached_tokens").Int())

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			billing.billingService = NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				"text-embedding-3-small": {Mode: "embedding", InputCostPerToken: inputPrice, CacheReadInputTokenCost: cacheReadPrice},
			}})
			billing.resolver = NewModelPricingResolver(nil, billing.billingService)
			err = billing.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "embeddings-cache-adj", Model: result.Model, BillingModel: result.BillingModel,
					UpstreamModel: result.UpstreamModel, Usage: result.Usage, Duration: time.Second,
				},
				APIKey:  &APIKey{ID: 1, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1.0, CacheHitRateAdjustment: tc.adjustment}},
				User:    &User{ID: 2},
				Account: &Account{ID: 3, Type: AccountTypeAPIKey},
			})
			require.NoError(t, err)
			require.Equal(t, 1000-tc.wantCached, usageRepo.lastLog.InputTokens)
			require.Equal(t, tc.wantCached, usageRepo.lastLog.CacheReadTokens)
			require.InDelta(t, float64(1000-tc.wantCached)*inputPrice+float64(tc.wantCached)*cacheReadPrice, usageRepo.lastLog.TotalCost, 1e-12)
		})
	}
}
