package service

import (
	"bytes"
	"context"
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
	"github.com/tidwall/gjson"
)

const openAICacheHitRateAdjustment = 0.05

// openAIDualUsageBody 的顶层 usage 与 response.usage 各报提示 1000、缓存读取 800。
const openAIDualUsageBody = `{"id":"resp_dual_usage","object":"response","status":"completed","output":[],"created_at":1700000000,"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}},"response":{"id":"resp_dual_usage","object":"response","status":"completed","output":[],"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"input_tokens_details":{"cached_tokens":800}}}}`

// recordOpenAICacheHitUsage 按分组比例把 usage 记成 usage log，返回日志里的未缓存输入与缓存读取。
func recordOpenAICacheHitUsage(t *testing.T, ctx context.Context, result *OpenAIForwardResult, pricing *PricingService) (int, int) {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	if pricing != nil {
		billing.billingService = NewBillingService(&config.Config{}, pricing)
		billing.resolver = NewModelPricingResolver(nil, billing.billingService)
	}
	group, ok := ctx.Value(ctxkey.Group).(*Group)
	require.True(t, ok, "ctxkey.Group must contain a *Group")
	err := billing.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:  result,
		APIKey:  &APIKey{ID: 1, GroupID: i64p(group.ID), Group: group},
		User:    &User{ID: 2},
		Account: &Account{ID: 3, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.Equal(t, openAICacheHitRateAdjustment, usageRepo.lastLog.CacheHitRateAdjustment)
	return usageRepo.lastLog.InputTokens, usageRepo.lastLog.CacheReadTokens
}

func TestOpenAICacheHitRateAdjustment_RewritesEveryUsageCopyOfResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handlers := []struct {
		name string
		call func(ctx context.Context, resp *http.Response, c *gin.Context) (*OpenAIUsage, error)
	}{
		{
			name: "handleNonStreamingResponse",
			call: func(ctx context.Context, resp *http.Response, c *gin.Context) (*OpenAIUsage, error) {
				result, err := (&OpenAIGatewayService{cfg: &config.Config{}}).handleNonStreamingResponse(ctx, resp, c, &Account{ID: 1, Type: AccountTypeAPIKey}, "gpt-5.1", "gpt-5.1")
				if err != nil {
					return nil, err
				}
				return result.OpenAIUsage, nil
			},
		},
		{
			name: "handleNonStreamingResponsePassthrough",
			call: func(ctx context.Context, resp *http.Response, c *gin.Context) (*OpenAIUsage, error) {
				result, err := (&OpenAIGatewayService{cfg: &config.Config{}}).handleNonStreamingResponsePassthrough(ctx, resp, c, &Account{ID: 1, Type: AccountTypeAPIKey}, "gpt-5.1", "gpt-5.1")
				if err != nil {
					return nil, err
				}
				return result.OpenAIUsage, nil
			},
		},
	}
	for _, handler := range handlers {
		t.Run(handler.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, RateMultiplier: 1.0, CacheHitRateAdjustment: openAICacheHitRateAdjustment})
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(openAIDualUsageBody)),
			}

			usage, err := handler.call(ctx, resp, c)

			require.NoError(t, err)
			require.Equal(t, OpenAIUsage{InputTokens: 1000, OutputTokens: 100, CacheReadInputTokens: 800}, *usage)
			clientBody := rec.Body.String()
			require.Equal(t, int64(750), gjson.Get(clientBody, "usage.prompt_tokens_details.cached_tokens").Int())
			require.Equal(t, int64(750), gjson.Get(clientBody, "response.usage.input_tokens_details.cached_tokens").Int())
			require.Equal(t, int64(1000), gjson.Get(clientBody, "usage.prompt_tokens").Int())
			require.Equal(t, int64(1000), gjson.Get(clientBody, "response.usage.input_tokens").Int())

			input, cacheRead := recordOpenAICacheHitUsage(t, ctx, &OpenAIForwardResult{
				RequestID: "dual-usage", Model: "gpt-5.1", BillingModel: "gpt-5.1", Usage: *usage, Duration: time.Second,
			}, nil)
			require.Equal(t, 250, input)
			require.Equal(t, 750, cacheRead)
		})
	}

	t.Run("streaming data line", func(t *testing.T) {
		event := `{"type":"response.completed","response":{"id":"resp_dual_usage","usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800}}},"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}}}`

		out := rewriteSSEDataLine("data: "+event+"\n", openAICacheHitRateAdjustment, cachehitadj.RewriteOpenAIUsageJSON)

		require.Equal(t, int64(750), gjson.Get(out, "usage.prompt_tokens_details.cached_tokens").Int())
		require.Equal(t, int64(750), gjson.Get(out, "response.usage.input_tokens_details.cached_tokens").Int())
		require.Equal(t, "data: "+event+"\n", rewriteSSEDataLine("data: "+event+"\n", 0, cachehitadj.RewriteOpenAIUsageJSON))
	})
}

func TestForwardEmbeddings_CacheHitRateAdjustmentFollowsEmbeddingsBillingParser(t *testing.T) {
	const inputPrice, cacheReadPrice = 0.02e-6, 0.005e-6
	for _, tc := range []struct {
		name  string
		usage string
	}{
		{
			name:  "prompt_tokens differs from input_tokens",
			usage: `{"prompt_tokens":1000,"input_tokens":2000,"total_tokens":2000,"prompt_tokens_details":{"cached_tokens":800}}`,
		},
		{
			name:  "only total_tokens",
			usage: `{"total_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, RateMultiplier: 1.0, CacheHitRateAdjustment: openAICacheHitRateAdjustment})
			reqBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(reqBody)).WithContext(ctx)
			svc := &OpenAIGatewayService{
				cfg: &config.Config{},
				httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small","usage":` + tc.usage + `}`)),
				}},
			}
			account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "sk-test", "base_url": "https://api.openai.com/v1",
			}}

			result, err := svc.ForwardEmbeddings(ctx, c, account, reqBody, "")

			require.NoError(t, err)
			require.Equal(t, OpenAIUsage{InputTokens: 1000, CacheReadInputTokens: 800}, result.Usage)
			clientUsage := gjson.GetBytes(rec.Body.Bytes(), "usage")
			require.Equal(t, int64(750), clientUsage.Get("prompt_tokens_details.cached_tokens").Int())

			input, cacheRead := recordOpenAICacheHitUsage(t, ctx, &OpenAIForwardResult{
				RequestID: "embeddings-dual-usage", Model: result.Model, BillingModel: result.BillingModel,
				UpstreamModel: result.UpstreamModel, Usage: result.Usage, Duration: time.Second,
			}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
				"text-embedding-3-small": {Mode: "embedding", InputCostPerToken: inputPrice, CacheReadInputTokenCost: cacheReadPrice},
			}})
			require.Equal(t, 250, input)
			require.Equal(t, 750, cacheRead)
		})
	}
}
