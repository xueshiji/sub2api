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
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatResponses_CacheHitAdjustmentPreservesUpstreamUsage(t *testing.T) {
	for _, route := range []string{"responses_to_messages", "chat_to_messages", "chat_to_responses"} {
		for _, stream := range []bool{false, true} {
			for _, tt := range []struct {
				name          string
				adjustment    float64
				cacheRead     int
				wantInput     int
				wantCacheRead int
			}{
				{name: "enabled", adjustment: 0.05, cacheRead: 800, wantInput: 150, wantCacheRead: 750},
				{name: "disabled", cacheRead: 800, wantInput: 100, wantCacheRead: 800},
				{name: "at_threshold", adjustment: 0.05, cacheRead: 100, wantInput: 800, wantCacheRead: 100},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", route, stream, tt.name), func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: tt.adjustment})
					c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
					startTime := time.Now()
					const model = "gpt-5.1"

					chatUsage := fmt.Sprintf(`{"prompt_tokens":1000,"completion_tokens":20,"total_tokens":1020,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":100}}`, tt.cacheRead)
					body := `{"id":"chat_1","object":"chat.completion","model":"gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":` + chatUsage + `}`
					contentType := "application/json"
					if stream {
						contentType = "text/event-stream"
						body = "data: " + `{"id":"chat_1","model":"gpt-5.1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n" +
							"data: " + `{"id":"chat_1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
							"data: " + `{"id":"chat_1","choices":[],"usage":` + chatUsage + `}` + "\n\ndata: [DONE]\n\n"
					}
					if route == "responses_to_messages" {
						contentType = "text/event-stream"
						body = "data: " + `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.1","status":"in_progress","output":[]}}` + "\n\n" +
							"data: " + `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n" +
							fmt.Sprintf("data: "+`{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020,"cache_creation_input_tokens":100,"input_tokens_details":{"cached_tokens":%d}}}}`+"\n\n", tt.cacheRead)
					}
					resp := &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{contentType}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}
					defer func() { require.NoError(t, resp.Body.Close()) }()

					var result *OpenAIForwardResult
					var err error
					switch route {
					case "responses_to_messages":
						if stream {
							result, err = svc.handleAnthropicStreamingResponse(resp, c, account, model, model, model, startTime)
						} else {
							result, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, model, model, model, startTime)
						}
					case "chat_to_messages":
						if stream {
							result, err = svc.streamChatCompletionsAsAnthropic(c, resp, model, model, model, nil, nil, startTime)
						} else {
							result, err = svc.bufferChatCompletionsAsAnthropic(c, resp, model, model, model, nil, nil, startTime)
						}
					case "chat_to_responses":
						if stream {
							result, err = svc.streamChatCompletionsAsResponses(c, resp, model, nil, nil, false, nil, model, model, nil, nil, startTime)
						} else {
							result, err = svc.bufferChatCompletionsAsResponses(c, resp, model, nil, nil, false, nil, model, model, nil, nil, startTime)
						}
					}
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, OpenAIUsage{InputTokens: 1000, OutputTokens: 20, CacheCreationInputTokens: 100, CacheReadInputTokens: tt.cacheRead}, result.Usage)
					require.Equal(t, stream, result.Stream)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Contains(t, rec.Body.String(), "hello")

					usage := gjson.GetBytes(rec.Body.Bytes(), "usage")
					if stream {
						require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if !strings.HasPrefix(line, "data:") {
								continue
							}
							payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
							switch gjson.Get(payload, "type").String() {
							case "message_delta":
								usage = gjson.Get(payload, "usage")
							case "response.completed":
								usage = gjson.Get(payload, "response.usage")
							}
						}
					} else {
						require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
					}
					require.True(t, usage.IsObject(), rec.Body.String())
					require.Equal(t, int64(20), usage.Get("output_tokens").Int())
					if route == "chat_to_responses" {
						require.Equal(t, int64(1000), usage.Get("input_tokens").Int())
						require.Equal(t, int64(tt.wantCacheRead), usage.Get("input_tokens_details.cached_tokens").Int())
					} else {
						require.Equal(t, int64(tt.wantInput), usage.Get("input_tokens").Int())
						require.Equal(t, int64(tt.wantCacheRead), usage.Get("cache_read_input_tokens").Int())
					}
					require.Equal(t, int64(100), usage.Get("cache_creation_input_tokens").Int())
				})
			}
		}
	}
}
