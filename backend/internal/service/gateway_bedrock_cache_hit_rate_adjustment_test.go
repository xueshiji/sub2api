package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游真实用量 input=2000 / cache_creation=1000 / cache_read=8000，命中率 8000/11000 高于 10% 门槛。
const (
	bedrockCacheAdjInputTokens     = 2000
	bedrockCacheAdjCacheReadTokens = 8000
)

const bedrockCacheAdjInvocationMetrics = `"amazon-bedrock-invocationMetrics":{"inputTokenCount":11000,"outputTokenCount":100}`

// 提示总量 11000：修正 5% 时 delta = round(11000 × 5%) = 550；修正 0 时客户端看到的即上游原值。
var bedrockCacheAdjCases = []struct {
	name              string
	adjustment        float64
	clientInputTokens int
	clientCacheRead   int
	reduction         int
}{
	{name: "adjustment 0.05", adjustment: 0.05, clientInputTokens: 2550, clientCacheRead: 7450, reduction: 550},
	{name: "adjustment 0", adjustment: 0, clientInputTokens: 2000, clientCacheRead: 8000, reduction: 0},
}

// bedrockCacheAdjMessageBody 是 Bedrock InvokeModel 的响应体；withMetrics 为 true 时带上游原包里的 invocationMetrics。
func bedrockCacheAdjMessageBody(inputTokens, cacheReadTokens int, withMetrics bool) string {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"hi"}],` +
		`"stop_reason":"end_turn","stop_sequence":null,` +
		`"usage":{"input_tokens":` + strconv.Itoa(inputTokens) + `,"cache_creation_input_tokens":1000,"cache_read_input_tokens":` +
		strconv.Itoa(cacheReadTokens) + `,"output_tokens":100}`
	if withMetrics {
		body += `,` + bedrockCacheAdjInvocationMetrics
	}
	return body + `}`
}

// bedrockCacheAdjStreamEvents 是一次完整响应的 Claude SSE 事件 JSON 序列，事件类型取 JSON 的 type 字段。
func bedrockCacheAdjStreamEvents(inputTokens, cacheReadTokens int, withMetrics bool) []string {
	messageDelta := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":100}`
	if withMetrics {
		messageDelta += `,` + bedrockCacheAdjInvocationMetrics
	}
	return []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-20250514",` +
			`"content":[],"stop_reason":null,"stop_sequence":null,` +
			`"usage":{"input_tokens":` + strconv.Itoa(inputTokens) + `,"cache_creation_input_tokens":1000,"cache_read_input_tokens":` +
			strconv.Itoa(cacheReadTokens) + `,"output_tokens":1}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		messageDelta + `}`,
		`{"type":"message_stop"}`,
	}
}

// bedrockCacheAdjClientSSE 是网关写给客户端的 SSE 文本：每个事件一行 event 加一行 data。
func bedrockCacheAdjClientSSE(events []string) string {
	var builder strings.Builder
	for _, event := range events {
		_, _ = builder.WriteString("event: " + gjson.Get(event, "type").String() + "\ndata: " + event + "\n\n")
	}
	return builder.String()
}

// bedrockCacheAdjChunkFrame 把一个 Claude SSE 事件 JSON 封成 Bedrock EventStream 的 chunk 帧。
func bedrockCacheAdjChunkFrame(event string) []byte {
	payload := []byte(`{"bytes":"` + base64.StdEncoding.EncodeToString([]byte(event)) + `"}`)
	var headers bytes.Buffer
	_ = headers.WriteByte(byte(len(":event-type")))
	_, _ = headers.WriteString(":event-type")
	_ = headers.WriteByte(7) // string
	_ = binary.Write(&headers, binary.BigEndian, uint16(len("chunk")))
	_, _ = headers.WriteString("chunk")

	var prelude bytes.Buffer
	_ = binary.Write(&prelude, binary.BigEndian, uint32(12+headers.Len()+len(payload)+4))
	_ = binary.Write(&prelude, binary.BigEndian, uint32(headers.Len()))

	var frame bytes.Buffer
	_, _ = frame.Write(prelude.Bytes())
	_ = binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(prelude.Bytes()))
	_, _ = frame.Write(headers.Bytes())
	_, _ = frame.Write(payload)
	_ = binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes()))
	return frame.Bytes()
}

// bedrockCacheAdjBilledTokens 以 Bedrock 账号落库计费，返回 usage_logs 里的计费 input / cache_read token 数。
func bedrockCacheAdjBilledTokens(t *testing.T, group *Group, usage ClaudeUsage) (inputTokens, cacheReadTokens, reduction int) {
	t.Helper()
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newCacheAdjGatewayServiceForTest(usageRepo, billingRepo, nil)
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "bedrock-cache-hit-adjustment",
			Model:         "claude-sonnet-4",
			UpstreamModel: "claude-sonnet-4",
			Duration:      time.Second,
			Usage:         usage,
		},
		APIKey:  &APIKey{ID: 1, GroupID: &group.ID, Group: group},
		User:    &User{ID: 2},
		Account: &Account{ID: 3, Platform: PlatformAnthropic, Type: AccountTypeBedrock},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	return usageRepo.lastLog.InputTokens, usageRepo.lastLog.CacheReadTokens, usageRepo.lastLog.CacheReadTokensReduction
}

func TestBedrockNonStreaming_CacheHitRateAdjustedUsageMatchesBilledUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeBedrock}

	for _, tc := range bedrockCacheAdjCases {
		t.Run(tc.name, func(t *testing.T) {
			group := &Group{
				ID:                     1,
				Platform:               PlatformAnthropic,
				SubscriptionType:       SubscriptionTypeStandard,
				RateMultiplier:         1.0,
				CacheHitRateAdjustment: tc.adjustment,
			}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(bedrockCacheAdjMessageBody(bedrockCacheAdjInputTokens, bedrockCacheAdjCacheReadTokens, true))),
			}
			svc := &GatewayService{cfg: &config.Config{}}

			usage, err := svc.handleBedrockNonStreamingResponse(ctx, resp, c, account)

			require.NoError(t, err)
			require.Equal(t, bedrockCacheAdjMessageBody(tc.clientInputTokens, tc.clientCacheRead, false), rec.Body.String())
			require.Equal(t, ClaudeUsage{
				InputTokens:              bedrockCacheAdjInputTokens,
				OutputTokens:             100,
				CacheCreationInputTokens: 1000,
				CacheReadInputTokens:     bedrockCacheAdjCacheReadTokens,
			}, *usage)
			inputTokens, cacheReadTokens, reduction := bedrockCacheAdjBilledTokens(t, group, *usage)
			require.Equal(t, tc.clientInputTokens, inputTokens)
			require.Equal(t, tc.clientCacheRead, cacheReadTokens)
			require.Equal(t, tc.reduction, reduction)
		})
	}
}

func TestBedrockStreaming_CacheHitRateAdjustedUsageMatchesBilledUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeBedrock}

	for _, tc := range bedrockCacheAdjCases {
		t.Run(tc.name, func(t *testing.T) {
			group := &Group{
				ID:                     1,
				Platform:               PlatformAnthropic,
				SubscriptionType:       SubscriptionTypeStandard,
				RateMultiplier:         1.0,
				CacheHitRateAdjustment: tc.adjustment,
			}
			ctx := context.WithValue(context.Background(), ctxkey.Group, group)
			var frames bytes.Buffer
			for _, event := range bedrockCacheAdjStreamEvents(bedrockCacheAdjInputTokens, bedrockCacheAdjCacheReadTokens, true) {
				_, _ = frames.Write(bedrockCacheAdjChunkFrame(event))
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(bytes.NewReader(frames.Bytes())),
			}
			svc := &GatewayService{cfg: &config.Config{}}

			result, err := svc.handleBedrockStreamingResponse(ctx, resp, c, account, time.Now(), "claude-sonnet-4")

			require.NoError(t, err)
			require.Equal(t, bedrockCacheAdjClientSSE(bedrockCacheAdjStreamEvents(tc.clientInputTokens, tc.clientCacheRead, false)), rec.Body.String())
			require.Equal(t, ClaudeUsage{
				InputTokens:              bedrockCacheAdjInputTokens,
				OutputTokens:             100,
				CacheCreationInputTokens: 1000,
				CacheReadInputTokens:     bedrockCacheAdjCacheReadTokens,
			}, *result.usage)
			inputTokens, cacheReadTokens, reduction := bedrockCacheAdjBilledTokens(t, group, *result.usage)
			require.Equal(t, tc.clientInputTokens, inputTokens)
			require.Equal(t, tc.clientCacheRead, cacheReadTokens)
			require.Equal(t, tc.reduction, reduction)
		})
	}
}
