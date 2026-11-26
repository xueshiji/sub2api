package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWS_CacheAdjustmentPreservesRawUsage(t *testing.T) {
	for _, route := range []string{"json", "sse", "websocket"} {
		for _, adjustment := range []float64{0, 0.05} {
			t.Run(fmt.Sprintf("%s/adjustment=%g", route, adjustment), func(t *testing.T) {
				cfg := newOpenAIWSExecutionScopeTestConfig()
				capture := &openAIWSCaptureConn{events: [][]byte{
					[]byte(`{"type":"response.completed","response":{"id":"resp_cache","model":"gpt-5.1","status":"completed","usage":{"input_tokens":1000,"output_tokens":20,"input_tokens_details":{"cached_tokens":800}}}}`),
				}}
				pool := newOpenAIWSConnPool(cfg)
				pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: capture})
				defer pool.Close()
				svc := &OpenAIGatewayService{
					cfg: cfg, cache: &stubGatewayCache{}, openaiWSPool: pool,
					openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(),
				}
				account := &Account{
					ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true},
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: adjustment})
				var result *OpenAIForwardResult
				var clientUsage gjson.Result
				if route == "websocket" {
					results := make(chan *OpenAIForwardResult, 1)
					errors := make(chan error, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := coderws.Accept(w, r, nil)
						if err != nil {
							errors <- err
							return
						}
						defer func() { _ = conn.CloseNow() }()
						c, _ := gin.CreateTestContext(w)
						c.Request = r.WithContext(ctx)
						hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, turn *OpenAIForwardResult, _ error) { results <- turn }}
						errors <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "sk-test", []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`), hooks)
					}))
					defer server.Close()
					client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
					require.NoError(t, err)
					defer func() { _ = client.CloseNow() }()
					_, message, err := client.Read(ctx)
					require.NoError(t, err)
					clientUsage = gjson.GetBytes(message, "response.usage")
					select {
					case result = <-results:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
					select {
					case err = <-errors:
						require.NoError(t, err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				} else {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")
					body := []byte(fmt.Sprintf(`{"model":"gpt-5.1","stream":%t,"input":"hello"}`, route == "sse"))
					var err error
					result, err = svc.Forward(ctx, c, account, body)
					require.NoError(t, err)
					require.True(t, result.OpenAIWSMode)
					clientUsage = gjson.GetBytes(rec.Body.Bytes(), "usage")
					if route == "sse" {
						for _, line := range strings.Split(rec.Body.String(), "\n") {
							if data, ok := extractAnthropicSSEDataLine(line); ok {
								clientUsage = gjson.Get(data, "response.usage")
							}
						}
					}
				}
				require.NotNil(t, result)
				require.Equal(t, OpenAIUsage{InputTokens: 1000, OutputTokens: 20, CacheReadInputTokens: 800}, result.Usage)
				require.Equal(t, int64(1000), clientUsage.Get("input_tokens").Int())
				require.Equal(t, int64(800-int(1000*adjustment)), clientUsage.Get("input_tokens_details.cached_tokens").Int())
			})
		}
	}
}
