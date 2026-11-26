package service

import (
	"context"
	"fmt"
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

const genericRouteCacheHitAdjustment = 0.05

const (
	// 标准 Anthropic 用量。
	genericRouteUsageReadByBilling = `{"input_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":1500,"cache_creation":{"ephemeral_5m_input_tokens":300,"ephemeral_1h_input_tokens":0},"output_tokens":1,"server_tool_usage":{"web_search_requests":0}}`
	// 仅 cached_tokens：通用路由的计费把它当缓存读取量，Antigravity 上游透传的计费不认。
	genericRouteUsageCachedTokensOnly = `{"input_tokens":1000,"output_tokens":1,"cached_tokens":800}`
)

type genericRouteUsageShape struct {
	name   string
	usage  string
	billed ClaudeUsage
}

// 改写后的用量经计费解析读回与落账不一致的形态，两条路由都保持上游原样。
var anthropicRouteUsageShapesLeftUntouched = []genericRouteUsageShape{
	{
		name:   "cache_creation details without aggregate",
		usage:  `{"input_tokens":200,"cache_read_input_tokens":800,"cache_creation":{"ephemeral_5m_input_tokens":300},"output_tokens":1}`,
		billed: ClaudeUsage{InputTokens: 200, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "cache_creation 1h detail without aggregate",
		usage:  `{"input_tokens":200,"cache_read_input_tokens":800,"cache_creation":{"ephemeral_1h_input_tokens":300},"output_tokens":1}`,
		billed: ClaudeUsage{InputTokens: 200, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens_details cached_tokens without cache read",
		usage:  `{"input_tokens":1000,"output_tokens":1,"prompt_tokens_details":{"cached_tokens":800}}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1},
	},
	{
		name:   "prompt_tokens below cache buckets",
		usage:  `{"input_tokens":250,"output_tokens":1,"cache_read_input_tokens":800,"cache_creation_input_tokens":500,"prompt_tokens":900}`,
		billed: ClaudeUsage{OutputTokens: 1, CacheCreationInputTokens: 500, CacheReadInputTokens: 800},
	},
}

// 改写后的用量经计费解析读回等于修正后落账的形态，两条路由都照常改写。
var anthropicRouteUsageShapesRewritten = []genericRouteUsageShape{
	{
		name:   "standard",
		usage:  genericRouteUsageReadByBilling,
		billed: ClaudeUsage{InputTokens: 200, OutputTokens: 1, CacheCreationInputTokens: 300, CacheReadInputTokens: 1500},
	},
	{
		name:   "cache_creation aggregate with matching 5m detail",
		usage:  `{"input_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":1500,"cache_creation":{"ephemeral_5m_input_tokens":300},"output_tokens":1}`,
		billed: ClaudeUsage{InputTokens: 200, OutputTokens: 1, CacheCreationInputTokens: 300, CacheReadInputTokens: 1500},
	},
	{
		name:   "cache_creation aggregate with matching 1h detail",
		usage:  `{"input_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":1500,"cache_creation":{"ephemeral_1h_input_tokens":300},"output_tokens":1}`,
		billed: ClaudeUsage{InputTokens: 200, OutputTokens: 1, CacheCreationInputTokens: 300, CacheReadInputTokens: 1500},
	},
	{
		name:   "prompt_tokens",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_tokens":1800}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens zero",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_tokens":0}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_cache_hit_tokens without prompt_tokens or prompt_cache_miss_tokens",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_cache_hit_tokens":800}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_cache_hit_tokens zero without prompt_tokens or prompt_cache_miss_tokens",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_cache_hit_tokens":0}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens zero with prompt_cache_hit_tokens zero",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_tokens":0,"prompt_cache_hit_tokens":0}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens_details cached_tokens",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_tokens_details":{"cached_tokens":800}}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens_details cached_tokens zero",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"prompt_tokens_details":{"cached_tokens":0}}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "prompt_tokens_details cache_write_tokens and cache_creation_tokens",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"cache_creation_input_tokens":100,"prompt_tokens_details":{"cache_write_tokens":100,"cache_creation_tokens":100}}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheCreationInputTokens: 100, CacheReadInputTokens: 800},
	},
	{
		name:   "cached_tokens zero",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"cached_tokens":0}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
	{
		name:   "cached_tokens with standard cache read",
		usage:  `{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":800,"cached_tokens":800}`,
		billed: ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800},
	},
}

// Kimi 流式：message_start 给提示总量，message_delta 给未缓存输入与缓存读取，两处都带 prompt_tokens。
var genericRouteKimiStream = genericRouteSSE(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"prompt_tokens":173306,"cached_tokens":0}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":250,"cache_creation_input_tokens":0,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`,
	`{"type":"message_stop"}`,
)

// Kimi 全量命中：message_start 的 input_tokens 等于 prompt_tokens，message_delta 的 input_tokens 为 0。
var genericRouteKimiFullyCachedStream = genericRouteSSE(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"prompt_tokens":173306}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
	`{"type":"message_delta","usage":{"input_tokens":0,"cache_read_input_tokens":173306,"prompt_tokens":173306,"output_tokens":166},"delta":{"stop_reason":"end_turn"}}`,
	`{"type":"message_stop"}`,
)

type anthropicRouteProviderUsage struct {
	name string
	// 非流式响应里的 usage。
	usage string
	// 流式事件序列：message_start、message_delta、message_stop。
	stream string
	billed ClaudeUsage
}

// Kimi、GLM、DeepSeek 形态：缓存量写在别名字段里，未缓存输入取自别名字段而非 input_tokens。
var anthropicRouteProviderUsages = []anthropicRouteProviderUsage{
	{
		name:   "kimi",
		usage:  `{"input_tokens":173306,"output_tokens":166,"cache_read_input_tokens":173056,"prompt_tokens":173306,"cached_tokens":173056}`,
		stream: genericRouteKimiStream,
		billed: ClaudeUsage{InputTokens: 250, OutputTokens: 166, CacheReadInputTokens: 173056},
	},
	{
		name:   "kimi fully cached",
		usage:  `{"input_tokens":173306,"output_tokens":166,"cache_read_input_tokens":173306,"prompt_tokens":173306,"cached_tokens":173306}`,
		stream: genericRouteKimiFullyCachedStream,
		billed: ClaudeUsage{OutputTokens: 166, CacheReadInputTokens: 173306},
	},
	{
		name:  "kimi cache read in cached_tokens only",
		usage: `{"input_tokens":173306,"output_tokens":49,"prompt_tokens":173306,"cached_tokens":173306}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"prompt_tokens":173306,"output_tokens":1}}}`,
			`{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":49,"prompt_tokens":173306,"cached_tokens":173306},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{OutputTokens: 49, CacheReadInputTokens: 173306},
	},
	{
		name:  "kimi cache read in prompt_tokens_details only",
		usage: `{"input_tokens":173306,"output_tokens":49,"prompt_tokens":173306,"prompt_tokens_details":{"cached_tokens":173306}}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"prompt_tokens":173306,"output_tokens":1}}}`,
			`{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":49,"prompt_tokens":173306,"prompt_tokens_details":{"cached_tokens":173306}},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{OutputTokens: 49, CacheReadInputTokens: 173306},
	},
	{
		name:  "glm",
		usage: `{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":1200,"output_tokens":1,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}}`,
			`{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{InputTokens: 400, OutputTokens: 300, CacheReadInputTokens: 800},
	},
	{
		name:  "deepseek",
		usage: `{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":1200,"output_tokens":1,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}}`,
			`{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{InputTokens: 400, OutputTokens: 300, CacheReadInputTokens: 800},
	},
	{
		name:  "deepseek fully cached",
		usage: `{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":1200,"prompt_cache_miss_tokens":0}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200,"output_tokens":1,"prompt_cache_hit_tokens":1200,"prompt_cache_miss_tokens":0}}}`,
			`{"type":"message_delta","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":1200,"prompt_cache_miss_tokens":0},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{OutputTokens: 30, CacheReadInputTokens: 1200},
	},
	{
		name:  "deepseek with standard buckets",
		usage: `{"input_tokens":400,"cache_creation_input_tokens":0,"cache_read_input_tokens":800,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":400,"cache_creation_input_tokens":0,"cache_read_input_tokens":800,"output_tokens":1,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}}`,
			`{"type":"message_delta","usage":{"input_tokens":400,"cache_creation_input_tokens":0,"cache_read_input_tokens":800,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{InputTokens: 400, OutputTokens: 300, CacheReadInputTokens: 800},
	},
	{
		name:  "deepseek with prompt_tokens and no cache_read_input_tokens",
		usage: `{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":1200,"output_tokens":1,"prompt_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}}`,
			`{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{InputTokens: 400, OutputTokens: 300, CacheReadInputTokens: 800},
	},
	{
		name:  "deepseek usage in message_delta only",
		usage: `{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
		stream: genericRouteSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[]}}`,
			`{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":300,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400},"delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		),
		billed: ClaudeUsage{InputTokens: 400, OutputTokens: 300, CacheReadInputTokens: 800},
	},
}

func genericRouteTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: genericRouteCacheHitAdjustment})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	return c, rec
}

func genericRouteGatewayService() *GatewayService {
	return &GatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
}

func genericRouteUpstreamResponse(payload string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}
}

func genericRouteMessageBody(usage string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"model","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":` + usage + `}`
}

func genericRouteSSE(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", gjson.Get(event, "type").String(), event)
	}
	return b.String()
}

var genericRouteStreamPlacements = []string{"message_start", "message_delta"}

// genericRouteStream 把 usage 放进 message_start（Anthropic 标准）或 message_delta（Kimi 等上游），
// 并返回不带该 usage 的事件块：它没有任何可修正的缓存读取量，应原样到达客户端。
func genericRouteStream(usage, placement string) (stream, plainEvent string) {
	plainStart := `{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`
	outputDelta := `{"type":"message_delta","usage":{"output_tokens":40},"delta":{"stop_reason":"end_turn"}}`
	start, delta := plainStart, outputDelta
	switch placement {
	case "message_start":
		start = `{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":` + usage + `}}`
		plainEvent = genericRouteSSE(outputDelta)
	case "message_delta":
		delta = `{"type":"message_delta","usage":` + usage + `,"delta":{"stop_reason":"end_turn"}}`
		plainEvent = genericRouteSSE(plainStart)
	}
	return genericRouteSSE(start, delta, `{"type":"message_stop"}`), plainEvent
}

// genericRouteStreamBilledUsage 是流式计费解析按事件位置合并后的用量。
func genericRouteStreamBilledUsage(billed ClaudeUsage, placement string) ClaudeUsage {
	if placement == "message_start" {
		billed.OutputTokens = 40
	}
	return billed
}

// genericRouteComparableUsage 取计费解析读出的四个标准计数。
func genericRouteComparableUsage(usage ClaudeUsage) ClaudeUsage {
	return ClaudeUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}
}

// genericRouteAdjustedUsage 对计费解析结果做与 recordUsageCore 相同的修正。
func genericRouteAdjustedUsage(billed ClaudeUsage) ClaudeUsage {
	adj := cachehitadj.Adjust(billed.InputTokens, billed.CacheReadInputTokens, billed.CacheCreationInputTokens, genericRouteCacheHitAdjustment)
	billed.InputTokens = adj.InputTokens
	billed.CacheReadInputTokens = adj.CacheReadTokens
	return billed
}

// anthropicRouteUsageLog 按 recordUsageCore 落账，返回 usage_logs 行记录的用量。
func anthropicRouteUsageLog(t *testing.T, billed ClaudeUsage, stream bool) ClaudeUsage {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
	group := &Group{ID: 1, RateMultiplier: 1, CacheHitRateAdjustment: genericRouteCacheHitAdjustment}

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result:  &ForwardResult{RequestID: "cache-hit-usage-shape", Usage: billed, Model: "claude-sonnet-4", Stream: stream, Duration: time.Second},
		APIKey:  &APIKey{ID: 1, GroupID: &group.ID, Group: group},
		User:    &User{ID: 2},
		Account: &Account{ID: 3, Type: AccountTypeAPIKey},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	return ClaudeUsage{
		InputTokens:              usageRepo.lastLog.InputTokens,
		OutputTokens:             usageRepo.lastLog.OutputTokens,
		CacheCreationInputTokens: usageRepo.lastLog.CacheCreationTokens,
		CacheReadInputTokens:     usageRepo.lastLog.CacheReadTokens,
	}
}

func genericRouteForwardUpstream(t *testing.T, payload string, stream bool) (*AntigravityGatewayService, *httptest.ResponseRecorder, ClaudeUsage) {
	t.Helper()
	svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
	svc.httpUpstream = &httpUpstreamStub{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}}
	c, rec := genericRouteTestContext(t)
	request := fmt.Sprintf(`{"model":"model","max_tokens":16,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
	account := &Account{ID: 7, Type: AccountTypeUpstream, Platform: PlatformAntigravity, Credentials: map[string]any{"base_url": "http://upstream.invalid", "api_key": "k"}}

	result, err := svc.ForwardUpstream(c.Request.Context(), c, account, []byte(request))

	require.NoError(t, err)
	return svc, rec, result.Usage
}

// anthropicRoute 是一条做缓存命中率修正的路由：forward 返回计费解析读出的用量与发给客户端的响应，
// readBack 用该路由的计费解析去读客户端响应里的用量。
type anthropicRoute struct {
	name     string
	forward  func(t *testing.T, payload string, stream bool) (billed ClaudeUsage, client string)
	readBack func(t *testing.T, client string, stream bool) ClaudeUsage
}

func anthropicGenericRouteForward(t *testing.T, payload string, stream bool) (ClaudeUsage, string) {
	t.Helper()
	c, rec := genericRouteTestContext(t)
	svc := genericRouteGatewayService()
	if stream {
		result, err := svc.handleStreamingResponse(c.Request.Context(), genericRouteUpstreamResponse(payload), c, &Account{ID: 1}, time.Now(), "model", "model", false)
		require.NoError(t, err)
		return *result.usage, rec.Body.String()
	}
	usage, err := svc.handleNonStreamingResponse(c.Request.Context(), genericRouteUpstreamResponse(payload), c, &Account{ID: 1}, "model", "model")
	require.NoError(t, err)
	return *usage, rec.Body.String()
}

func anthropicGenericRouteReadBack(t *testing.T, client string, stream bool) ClaudeUsage {
	t.Helper()
	if !stream {
		_, usage, err := readAnthropicResponseUsage([]byte(client))
		require.NoError(t, err)
		return usage
	}
	svc := genericRouteGatewayService()
	usage := &ClaudeUsage{}
	for _, line := range strings.Split(client, "\n") {
		if data, ok := extractAnthropicSSEDataLine(line); ok {
			svc.parseSSEUsage(data, usage)
		}
	}
	return *usage
}

func anthropicAntigravityRouteForward(t *testing.T, payload string, stream bool) (ClaudeUsage, string) {
	t.Helper()
	_, rec, usage := genericRouteForwardUpstream(t, payload, stream)
	return usage, rec.Body.String()
}

func anthropicAntigravityRouteReadBack(t *testing.T, client string, stream bool) ClaudeUsage {
	t.Helper()
	svc := &AntigravityGatewayService{}
	if !stream {
		return *svc.extractClaudeUsage([]byte(client))
	}
	usage := &ClaudeUsage{}
	for _, line := range strings.Split(client, "\n") {
		svc.extractSSEUsage(line, usage)
	}
	return *usage
}

var anthropicRoutes = []anthropicRoute{
	{
		name:     "generic route",
		forward:  anthropicGenericRouteForward,
		readBack: anthropicGenericRouteReadBack,
	},
	{
		name:     "antigravity upstream",
		forward:  anthropicAntigravityRouteForward,
		readBack: anthropicAntigravityRouteReadBack,
	},
}

func requireClientUsageMatchesUsageLog(t *testing.T, route anthropicRoute, payload string, stream bool, billed ClaudeUsage) {
	t.Helper()
	gotBilled, client := route.forward(t, payload, stream)
	require.Equal(t, genericRouteComparableUsage(billed), genericRouteComparableUsage(gotBilled))
	require.NotEqual(t, payload, client, "响应应被改写")
	adjusted := genericRouteComparableUsage(genericRouteAdjustedUsage(gotBilled))
	require.Equal(t, adjusted, genericRouteComparableUsage(anthropicRouteUsageLog(t, gotBilled, stream)))
	require.Equal(t, adjusted, genericRouteComparableUsage(route.readBack(t, client, stream)))
}

func TestAnthropicRoutesProviderUsageAliases_ClientUsageMatchesUsageLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range anthropicRoutes {
		for _, tc := range anthropicRouteProviderUsages {
			t.Run(route.name+" "+tc.name+" non-stream", func(t *testing.T) {
				body := genericRouteMessageBody(tc.usage)

				billed, client := route.forward(t, body, false)

				require.Equal(t, genericRouteComparableUsage(tc.billed), genericRouteComparableUsage(billed))
				adjusted := genericRouteComparableUsage(genericRouteAdjustedUsage(tc.billed))
				require.Equal(t, adjusted, genericRouteComparableUsage(anthropicRouteUsageLog(t, billed, false)))
				require.Equal(t, adjusted, genericRouteComparableUsage(route.readBack(t, client, false)))
			})
			t.Run(route.name+" "+tc.name+" stream", func(t *testing.T) {
				billed, client := route.forward(t, tc.stream, true)

				require.Equal(t, genericRouteComparableUsage(tc.billed), genericRouteComparableUsage(billed))
				adjusted := genericRouteComparableUsage(genericRouteAdjustedUsage(tc.billed))
				require.Equal(t, adjusted, genericRouteComparableUsage(anthropicRouteUsageLog(t, billed, true)))
				require.Equal(t, adjusted, genericRouteComparableUsage(route.readBack(t, client, true)))
			})
		}
	}
}

func TestAnthropicRoutesUsageShapesRewriteWouldMisstate_ResponseUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range anthropicRoutes {
		for _, tc := range anthropicRouteUsageShapesLeftUntouched {
			t.Run(route.name+" "+tc.name+" non-stream", func(t *testing.T) {
				body := genericRouteMessageBody(tc.usage)

				billed, client := route.forward(t, body, false)

				require.Equal(t, body, client)
				require.Equal(t, genericRouteComparableUsage(tc.billed), genericRouteComparableUsage(billed))
			})
			for _, placement := range genericRouteStreamPlacements {
				t.Run(route.name+" "+tc.name+" stream in "+placement, func(t *testing.T) {
					stream, _ := genericRouteStream(tc.usage, placement)

					billed, client := route.forward(t, stream, true)

					require.Equal(t, stream, client)
					billed = genericRouteComparableUsage(billed)
					require.Equal(t, genericRouteComparableUsage(genericRouteStreamBilledUsage(tc.billed, placement)), billed)
				})
			}
		}
	}
}

func TestAnthropicRoutesUsageShapesRewritten_ClientUsageMatchesUsageLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range anthropicRoutes {
		for _, tc := range anthropicRouteUsageShapesRewritten {
			t.Run(route.name+" "+tc.name+" non-stream", func(t *testing.T) {
				body := genericRouteMessageBody(tc.usage)

				billed, client := route.forward(t, body, false)

				require.Equal(t, genericRouteComparableUsage(tc.billed), genericRouteComparableUsage(billed))
				adjusted := genericRouteComparableUsage(genericRouteAdjustedUsage(tc.billed))
				require.Equal(t, adjusted, genericRouteComparableUsage(anthropicRouteUsageLog(t, billed, false)))
				require.Equal(t, adjusted, genericRouteComparableUsage(route.readBack(t, client, false)))
				require.Equal(t, gjson.Get(body, "usage.cache_creation").Raw, gjson.Get(client, "usage.cache_creation").Raw)
				require.Equal(t, gjson.Get(body, "usage.server_tool_usage").Raw, gjson.Get(client, "usage.server_tool_usage").Raw)
			})
			for _, placement := range genericRouteStreamPlacements {
				t.Run(route.name+" "+tc.name+" stream in "+placement, func(t *testing.T) {
					stream, plainEvent := genericRouteStream(tc.usage, placement)
					billed := genericRouteStreamBilledUsage(tc.billed, placement)

					gotBilled, client := route.forward(t, stream, true)

					require.Equal(t, genericRouteComparableUsage(billed), genericRouteComparableUsage(gotBilled))
					adjusted := genericRouteComparableUsage(genericRouteAdjustedUsage(billed))
					require.Equal(t, adjusted, genericRouteComparableUsage(anthropicRouteUsageLog(t, gotBilled, true)))
					require.Equal(t, adjusted, genericRouteComparableUsage(route.readBack(t, client, true)))
					require.Contains(t, client, plainEvent)
				})
			}
		}
	}
}

// 仅 cached_tokens 的用量：通用路由按缓存读取量计费并改写，Antigravity 上游透传不计缓存读取量，响应保持原样。
func TestAnthropicRoutesCachedTokensOnly_BilledAndRewrittenByGenericRouteOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("generic route non-stream", func(t *testing.T) {
		body := genericRouteMessageBody(genericRouteUsageCachedTokensOnly)

		billed, client := anthropicGenericRouteForward(t, body, false)

		require.Equal(t, ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800}, genericRouteComparableUsage(billed))
		usage := gjson.Get(client, "usage")
		require.Equal(t, int64(1090), usage.Get("input_tokens").Int())
		require.Equal(t, int64(710), usage.Get("cache_read_input_tokens").Int())
		require.Equal(t, int64(710), usage.Get("cached_tokens").Int())
	})
	for _, placement := range genericRouteStreamPlacements {
		t.Run("generic route stream in "+placement, func(t *testing.T) {
			stream, _ := genericRouteStream(genericRouteUsageCachedTokensOnly, placement)

			requireClientUsageMatchesUsageLog(t, anthropicRoutes[0], stream, true, genericRouteStreamBilledUsage(ClaudeUsage{InputTokens: 1000, OutputTokens: 1, CacheReadInputTokens: 800}, placement))
		})
	}
	t.Run("antigravity upstream non-stream", func(t *testing.T) {
		body := genericRouteMessageBody(genericRouteUsageCachedTokensOnly)

		billed, client := anthropicAntigravityRouteForward(t, body, false)

		require.Equal(t, body, client)
		require.Equal(t, ClaudeUsage{InputTokens: 1000, OutputTokens: 1}, genericRouteComparableUsage(billed))
	})
	for _, placement := range genericRouteStreamPlacements {
		t.Run("antigravity upstream stream in "+placement, func(t *testing.T) {
			stream, _ := genericRouteStream(genericRouteUsageCachedTokensOnly, placement)

			billed, client := anthropicAntigravityRouteForward(t, stream, true)

			require.Equal(t, stream, client)
			require.Equal(t, genericRouteComparableUsage(genericRouteStreamBilledUsage(ClaudeUsage{InputTokens: 1000, OutputTokens: 1}, placement)), genericRouteComparableUsage(billed))
		})
	}
}

// 同一条流里 message_start 带 prompt_tokens，message_delta 只带标准输入侧字段：改写器会把 message_start 的
// prompt_tokens 带到 message_delta 上，与计费按标准字段的读数不一致。
var anthropicRouteAliasedStartStandardDeltaStream = genericRouteSSE(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"cache_creation_input_tokens":500,"cache_read_input_tokens":0,"output_tokens":1,"prompt_tokens":173306}}}`,
	`{"type":"message_delta","usage":{"input_tokens":250,"cache_read_input_tokens":173056,"output_tokens":166},"delta":{"stop_reason":"end_turn"}}`,
	`{"type":"message_stop"}`,
)

var anthropicRouteAliasedStartStandardDeltaStreamBilled = ClaudeUsage{InputTokens: 250, OutputTokens: 166, CacheCreationInputTokens: 500, CacheReadInputTokens: 173056}

// 同一条流里 message_start 只带标准字段，message_delta 带 prompt_tokens 且小于缓存读取与缓存写入之和。
var anthropicRouteStandardStartAliasedDeltaStream = genericRouteSSE(
	`{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":173306,"cache_creation_input_tokens":500,"cache_read_input_tokens":0,"output_tokens":1}}}`,
	`{"type":"message_delta","usage":{"input_tokens":173306,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306},"delta":{"stop_reason":"end_turn"}}`,
	`{"type":"message_delta","usage":{"input_tokens":250,"cache_read_input_tokens":173056,"output_tokens":166},"delta":{"stop_reason":"end_turn"}}`,
	`{"type":"message_stop"}`,
)

var anthropicRouteStandardStartAliasedDeltaStreamBilled = ClaudeUsage{InputTokens: 250, OutputTokens: 166, CacheCreationInputTokens: 500, CacheReadInputTokens: 173056}

func TestAnthropicRoutesStream_UsageShapeChangingMidStreamLeftUntouched(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		stream string
		billed ClaudeUsage
	}{
		{name: "aliased message_start then standard message_delta", stream: anthropicRouteAliasedStartStandardDeltaStream, billed: anthropicRouteAliasedStartStandardDeltaStreamBilled},
		{name: "standard message_start then aliased message_delta", stream: anthropicRouteStandardStartAliasedDeltaStream, billed: anthropicRouteStandardStartAliasedDeltaStreamBilled},
	} {
		for _, route := range anthropicRoutes {
			t.Run(route.name+" "+tc.name, func(t *testing.T) {
				billed, client := route.forward(t, tc.stream, true)

				require.Equal(t, tc.stream, client)
				require.Equal(t, genericRouteComparableUsage(tc.billed), genericRouteComparableUsage(billed))
			})
		}
	}
}

// message_start 已改写后，改写结果读回与落账不一致的 message_delta 原样到达客户端。
func TestAnthropicRoutesStream_EventRewriteReadingBackDifferentlyForwardedAsIs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rewrittenStart := `{"type":"message_start","message":{"id":"msg_1","type":"message","model":"model","role":"assistant","content":[],"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`
	asIsDelta := `{"type":"message_delta","usage":{"input_tokens":1000,"cache_read_input_tokens":800,"cache_creation":{"ephemeral_5m_input_tokens":300},"output_tokens":10},"delta":{"stop_reason":"end_turn"}}`
	stream := genericRouteSSE(rewrittenStart, asIsDelta, `{"type":"message_stop"}`)

	for _, route := range anthropicRoutes {
		t.Run(route.name, func(t *testing.T) {
			billed, client := route.forward(t, stream, true)

			blocks := strings.Split(client, "\n\n")
			require.Len(t, blocks, 4)
			startUsage := gjson.Get(strings.TrimPrefix(blocks[0], "event: message_start\ndata: "), "message.usage")
			require.Equal(t, int64(250), startUsage.Get("input_tokens").Int())
			require.Equal(t, int64(750), startUsage.Get("cache_read_input_tokens").Int())
			require.Equal(t, genericRouteSSE(asIsDelta), blocks[1]+"\n\n")
			require.Equal(t, genericRouteComparableUsage(ClaudeUsage{InputTokens: 1000, OutputTokens: 10, CacheReadInputTokens: 800}), genericRouteComparableUsage(billed))
		})
	}
}
