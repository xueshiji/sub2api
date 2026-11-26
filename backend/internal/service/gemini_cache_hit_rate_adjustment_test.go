package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游 usage：promptTokenCount 含 cachedContentTokenCount，输出 5 个 token。
const (
	geminiCacheAdjUpstreamPrompt    = 10000
	geminiCacheAdjUpstreamCacheRead = 8000
	geminiCacheAdjUpstreamOutput    = 5
)

func geminiCacheAdjUpstreamJSON(cacheRead int) string {
	return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":%d,"cachedContentTokenCount":%d,"candidatesTokenCount":%d,"totalTokenCount":%d}}`,
		geminiCacheAdjUpstreamPrompt, cacheRead, geminiCacheAdjUpstreamOutput, geminiCacheAdjUpstreamPrompt+geminiCacheAdjUpstreamOutput)
}

func geminiCacheAdjUpstreamSSE(oauth bool, cacheRead int) string {
	wrap := func(chunk string) string {
		if oauth {
			return `{"response":` + chunk + `}`
		}
		return chunk
	}
	chunks := []string{
		fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"h"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":%d,"cachedContentTokenCount":%d,"candidatesTokenCount":1,"totalTokenCount":%d}}`,
			geminiCacheAdjUpstreamPrompt, cacheRead, geminiCacheAdjUpstreamPrompt+1),
		geminiCacheAdjUpstreamJSON(cacheRead),
	}
	var b strings.Builder
	for _, chunk := range chunks {
		_, _ = b.WriteString("data: " + wrap(chunk) + "\n\n")
	}
	_, _ = b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// geminiCacheAdjUsagePaths 是一种响应形态里 usage 对象及其提示 token / 缓存命中字段的 gjson 路径。
type geminiCacheAdjUsagePaths struct {
	usage  string
	prompt string
	cached string
}

var (
	geminiCacheAdjClaudePaths = geminiCacheAdjUsagePaths{usage: "usage", prompt: "input_tokens", cached: "cache_read_input_tokens"}
	geminiCacheAdjChatPaths   = geminiCacheAdjUsagePaths{usage: "usage", prompt: "prompt_tokens", cached: "prompt_tokens_details.cached_tokens"}
	geminiCacheAdjNativePaths = geminiCacheAdjUsagePaths{usage: "usageMetadata", prompt: "promptTokenCount", cached: "cachedContentTokenCount"}
)

// geminiCacheAdjClientUsage 是客户端响应里最后一个 usage 对象的提示 token 口径与原文。
type geminiCacheAdjClientUsage struct {
	prompt int
	cached int
	raw    string
}

// geminiCacheAdjLastUsage 取响应里最后一个 usage 对象；流式响应逐个 data 载荷查找。
func geminiCacheAdjLastUsage(body string, paths geminiCacheAdjUsagePaths) geminiCacheAdjClientUsage {
	payloads := geminiCacheAdjSSEPayloads(body)
	if len(payloads) == 0 {
		payloads = []string{body}
	}
	var last geminiCacheAdjClientUsage
	for _, payload := range payloads {
		usage := gjson.Get(payload, paths.usage)
		if !usage.IsObject() {
			continue
		}
		last = geminiCacheAdjClientUsage{
			prompt: int(usage.Get(paths.prompt).Int()),
			cached: int(usage.Get(paths.cached).Int()),
			raw:    usage.Raw,
		}
	}
	return last
}

func geminiCacheAdjSSEPayloads(body string) []string {
	var payloads []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

var geminiCacheAdjVolatileFields = regexp.MustCompile(`"(id|created)":(?:"[^"]*"|\d+)`)

// geminiCacheAdjStableBody 抹掉随机 id 与时间戳，供两次响应逐字节比较。
func geminiCacheAdjStableBody(body string) string {
	return geminiCacheAdjVolatileFields.ReplaceAllString(body, `"$1":x`)
}

type geminiCacheAdjForwardFunc func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error)

// geminiCacheAdjCase 是一个客户端出口；oauth 为 OAuth 账号带 project_id，非流式请求由上游流式聚合而成。
type geminiCacheAdjCase struct {
	name      string
	oauth     bool
	stream    bool
	body      []byte
	paths     geminiCacheAdjUsagePaths
	forward   geminiCacheAdjForwardFunc
	zeroUsage geminiCacheAdjClientUsage
	adjUsage  geminiCacheAdjClientUsage
}

func geminiCacheAdjCases() []geminiCacheAdjCase {
	forwardMessages := func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
		return svc.Forward(context.Background(), c, account, body)
	}
	forwardChat := func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
		return svc.ForwardAsChatCompletions(context.Background(), c, account, body)
	}
	forwardNative := func(action string, stream bool) geminiCacheAdjForwardFunc {
		return func(svc *GeminiMessagesCompatService, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
			return svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", action, stream, body)
		}
	}

	messages := []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	messagesStream := []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	chat := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`)
	chatStream := []byte(`{"model":"gemini-2.5-flash","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	native := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	// Claude 形态的 usage 只有 input_tokens / output_tokens。
	claudeZero := geminiCacheAdjClientUsage{prompt: 2000, raw: `{"input_tokens":2000,"output_tokens":5}`}
	claudeAdj := geminiCacheAdjClientUsage{prompt: 2500, raw: `{"input_tokens":2500,"output_tokens":5}`}
	// API Key 账号的 Chat Completions 非流式响应不带缓存字段，prompt_tokens 即未缓存输入。
	chatPlainZero := geminiCacheAdjClientUsage{prompt: 2000, raw: `{"prompt_tokens":2000,"completion_tokens":5,"total_tokens":2005}`}
	chatPlainAdj := geminiCacheAdjClientUsage{prompt: 2500, raw: `{"prompt_tokens":2500,"completion_tokens":5,"total_tokens":2505}`}
	// 带缓存字段的 Chat Completions 形态：prompt_tokens 是提示总量，缓存命中在 cached_tokens。
	chatCachedZero := geminiCacheAdjClientUsage{prompt: 10000, cached: 8000, raw: `{"prompt_tokens":10000,"completion_tokens":5,"total_tokens":10005,"prompt_tokens_details":{"cached_tokens":8000}}`}
	chatCachedAdj := geminiCacheAdjClientUsage{prompt: 10000, cached: 7500, raw: `{"prompt_tokens":10000,"completion_tokens":5,"total_tokens":10005,"prompt_tokens_details":{"cached_tokens":7500}}`}
	nativeZero := geminiCacheAdjClientUsage{prompt: 10000, cached: 8000}
	nativeAdj := geminiCacheAdjClientUsage{prompt: 10000, cached: 7500}

	return []geminiCacheAdjCase{
		{name: "messages 非流式 / api-key", body: messages, paths: geminiCacheAdjClaudePaths, forward: forwardMessages, zeroUsage: claudeZero, adjUsage: claudeAdj},
		{name: "messages 非流式 / oauth 聚合", oauth: true, body: messages, paths: geminiCacheAdjClaudePaths, forward: forwardMessages, zeroUsage: claudeZero, adjUsage: claudeAdj},
		{name: "messages 流式 / api-key", stream: true, body: messagesStream, paths: geminiCacheAdjClaudePaths, forward: forwardMessages, zeroUsage: claudeZero, adjUsage: claudeAdj},
		{name: "messages 流式 / oauth", oauth: true, stream: true, body: messagesStream, paths: geminiCacheAdjClaudePaths, forward: forwardMessages, zeroUsage: claudeZero, adjUsage: claudeAdj},
		{name: "chat/completions 非流式 / api-key", body: chat, paths: geminiCacheAdjChatPaths, forward: forwardChat, zeroUsage: chatPlainZero, adjUsage: chatPlainAdj},
		{name: "chat/completions 非流式 / oauth 聚合", oauth: true, body: chat, paths: geminiCacheAdjChatPaths, forward: forwardChat, zeroUsage: chatCachedZero, adjUsage: chatCachedAdj},
		{name: "chat/completions 流式 / api-key", stream: true, body: chatStream, paths: geminiCacheAdjChatPaths, forward: forwardChat, zeroUsage: chatCachedZero, adjUsage: chatCachedAdj},
		{name: "chat/completions 流式 / oauth", oauth: true, stream: true, body: chatStream, paths: geminiCacheAdjChatPaths, forward: forwardChat, zeroUsage: chatCachedZero, adjUsage: chatCachedAdj},
		{name: "generateContent / api-key", body: native, paths: geminiCacheAdjNativePaths, forward: forwardNative("generateContent", false), zeroUsage: nativeZero, adjUsage: nativeAdj},
		{name: "generateContent / oauth 聚合", oauth: true, body: native, paths: geminiCacheAdjNativePaths, forward: forwardNative("generateContent", false), zeroUsage: nativeZero, adjUsage: nativeAdj},
		{name: "streamGenerateContent / api-key", stream: true, body: native, paths: geminiCacheAdjNativePaths, forward: forwardNative("streamGenerateContent", true), zeroUsage: nativeZero, adjUsage: nativeAdj},
		{name: "streamGenerateContent / oauth", oauth: true, stream: true, body: native, paths: geminiCacheAdjNativePaths, forward: forwardNative("streamGenerateContent", true), zeroUsage: nativeZero, adjUsage: nativeAdj},
	}
}

func geminiCacheAdjUpstream(tc geminiCacheAdjCase, cacheRead int) (string, bool) {
	// OAuth 账号带 project_id 时非流式请求也走上游流式接口。
	if tc.stream || tc.oauth {
		return geminiCacheAdjUpstreamSSE(tc.oauth, cacheRead), true
	}
	return geminiCacheAdjUpstreamJSON(cacheRead), false
}

func geminiCacheAdjService(upstream string, stream bool) *GeminiMessagesCompatService {
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	return &GeminiMessagesCompatService{
		tokenProvider: &GeminiTokenProvider{},
		httpUpstream: &geminiCompatHTTPUpstreamStub{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(strings.NewReader(upstream)),
			},
		},
		cfg: &config.Config{},
	}
}

func geminiCacheAdjAccount(oauth bool) *Account {
	if oauth {
		return &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "ya29.test-token", "project_id": "project-1"}, Concurrency: 1}
	}
	return &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "gemini-api-key"}, Concurrency: 1}
}

// geminiCacheAdjRequest 构造带分组（可为 nil）的请求上下文并转发，返回响应体。
func geminiCacheAdjRequest(t *testing.T, tc geminiCacheAdjCase, group *Group, cacheRead int) (*httptest.ResponseRecorder, *ForwardResult, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream, upstreamStream := geminiCacheAdjUpstream(tc, cacheRead)
	svc := geminiCacheAdjService(upstream, upstreamStream)

	ctx := context.Background()
	if group != nil {
		ctx = context.WithValue(ctx, ctxkey.Group, group)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(tc.body)).WithContext(ctx)

	account := geminiCacheAdjAccount(tc.oauth)
	result, err := tc.forward(svc, c, account, tc.body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	return rec, result, account
}

// geminiCacheAdjBilledUsage 走一次 RecordUsage，返回落账的 token 口径。
func geminiCacheAdjBilledUsage(t *testing.T, result *ForwardResult, account *Account) ClaudeUsage {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	inputPrice, outputPrice, cacheReadPrice := 1.25, 10.0, 0.125
	groupID := int64(1)
	apiKey := &APIKey{ID: 1, GroupID: &groupID, Group: &Group{
		ID: groupID, SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1, CacheHitRateAdjustment: 0.05,
		ModelPricing: []ChannelModelPricing{{Models: []string{"gemini-2.5-flash"}, BillingMode: BillingModeToken,
			InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &cacheReadPrice}},
	}}
	require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: result, APIKey: apiKey, User: &User{ID: 2}, Account: account, PricingAt: time.Now(),
	}))
	require.NotNil(t, usageRepo.lastLog)
	return ClaudeUsage{InputTokens: usageRepo.lastLog.InputTokens, OutputTokens: usageRepo.lastLog.OutputTokens, CacheReadInputTokens: usageRepo.lastLog.CacheReadTokens}
}

func TestGeminiCompat_ClientUsageMatchesBilledUsageWhenCacheHitRateAdjusted(t *testing.T) {
	const adjustment = 0.05

	for _, tc := range geminiCacheAdjCases() {
		t.Run(tc.name, func(t *testing.T) {
			rec, result, account := geminiCacheAdjRequest(t, tc, &Group{ID: 1, CacheHitRateAdjustment: adjustment}, geminiCacheAdjUpstreamCacheRead)

			// 转发结果携带上游原始用量，供账号侧按真实值计费。
			require.Equal(t, ClaudeUsage{InputTokens: 2000, OutputTokens: geminiCacheAdjUpstreamOutput, CacheReadInputTokens: geminiCacheAdjUpstreamCacheRead}, result.Usage)

			billed := geminiCacheAdjBilledUsage(t, result, account)
			require.Equal(t, ClaudeUsage{InputTokens: 2500, OutputTokens: geminiCacheAdjUpstreamOutput, CacheReadInputTokens: 7500}, billed)

			client := geminiCacheAdjLastUsage(rec.Body.String(), tc.paths)
			require.Equal(t, tc.adjUsage.prompt, client.prompt)
			require.Equal(t, tc.adjUsage.cached, client.cached)
			if tc.adjUsage.raw != "" {
				require.Equal(t, tc.adjUsage.raw, client.raw)
			}
			// 未缓存输入与计费口径一致；带缓存字段的形态其缓存命中同样与计费一致。
			require.Equal(t, billed.InputTokens, client.prompt-client.cached)
			if client.cached > 0 {
				require.Equal(t, billed.CacheReadInputTokens, client.cached)
			}
		})
	}
}

func TestGeminiCompat_ResponseUnchangedWhenCacheHitRateAdjustmentIsZero(t *testing.T) {
	for _, tc := range geminiCacheAdjCases() {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, _ := geminiCacheAdjRequest(t, tc, &Group{ID: 1, CacheHitRateAdjustment: 0}, geminiCacheAdjUpstreamCacheRead)
			plain, _, _ := geminiCacheAdjRequest(t, tc, nil, geminiCacheAdjUpstreamCacheRead)

			require.Equal(t, geminiCacheAdjStableBody(plain.Body.String()), geminiCacheAdjStableBody(rec.Body.String()))

			client := geminiCacheAdjLastUsage(rec.Body.String(), tc.paths)
			require.Equal(t, tc.zeroUsage.prompt, client.prompt)
			require.Equal(t, tc.zeroUsage.cached, client.cached)
			if tc.zeroUsage.raw != "" {
				require.Equal(t, tc.zeroUsage.raw, client.raw)
			}
		})
	}
}

func TestGeminiCompat_ResponseUnchangedWhenRealCacheHitRateNotAboveThreshold(t *testing.T) {
	for _, tc := range geminiCacheAdjCases() {
		t.Run(tc.name, func(t *testing.T) {
			// 命中率恰为 10%（1000 / 10000），未严格超过阈值。
			const cacheRead = 1000
			rec, result, account := geminiCacheAdjRequest(t, tc, &Group{ID: 1, CacheHitRateAdjustment: 0.05}, cacheRead)
			zero, _, _ := geminiCacheAdjRequest(t, tc, &Group{ID: 1, CacheHitRateAdjustment: 0}, cacheRead)

			require.Equal(t, geminiCacheAdjStableBody(zero.Body.String()), geminiCacheAdjStableBody(rec.Body.String()))

			billed := geminiCacheAdjBilledUsage(t, result, account)
			client := geminiCacheAdjLastUsage(rec.Body.String(), tc.paths)
			require.Equal(t, billed.InputTokens, client.prompt-client.cached)
		})
	}
}
