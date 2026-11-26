package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIWSPassthroughCacheHitAdjustment = 0.05

// 透传转发按 WS 透传计费解析的口径改写客户端用量：每个存在的用量对象都按自身数值改写，
// 计费解析读不到的提示总量或缓存读取不改写；转发给计费的用量始终是上游原始值。
func TestOpenAIWSPassthrough_ClientUsageRewriteFollowsRelayBillingParser(t *testing.T) {
	for _, tc := range []struct {
		name       string
		completed  string
		wantBilled OpenAIUsage
		// wantClient maps a cached_tokens path of the client event to its value.
		wantClient map[string]int64
	}{
		{
			name:       "usage and response.usage are both rewritten",
			completed:  `{"type":"response.completed","response":{"id":"resp_adj_1","usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":800}}},"usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":800}}}`,
			wantBilled: OpenAIUsage{InputTokens: 1000, OutputTokens: 50, CacheReadInputTokens: 800},
			wantClient: map[string]int64{
				"response.usage.input_tokens_details.cached_tokens": 750,
				"usage.input_tokens_details.cached_tokens":          750,
			},
		},
		{
			name:       "input_tokens of 0 beside prompt_tokens is not rewritten",
			completed:  `{"type":"response.completed","response":{"id":"resp_adj_2","usage":{"input_tokens":0,"output_tokens":50,"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}}}`,
			wantBilled: OpenAIUsage{OutputTokens: 50, CacheReadInputTokens: 800},
			wantClient: map[string]int64{"response.usage.prompt_tokens_details.cached_tokens": 800},
		},
		{
			name:       "only the cached_tokens alias is not rewritten",
			completed:  `{"type":"response.completed","response":{"id":"resp_adj_3","usage":{"input_tokens":1000,"output_tokens":50,"cached_tokens":800}}}`,
			wantBilled: OpenAIUsage{InputTokens: 1000, OutputTokens: 50},
			wantClient: map[string]int64{"response.usage.cached_tokens": 800},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: openAIWSPassthroughCacheHitAdjustment})

			upstream := newStagedPassthroughConn()
			upstream.Send(tc.completed)
			var turnResult *OpenAIForwardResult
			hooks := &OpenAIWSIngressHooks{
				BeforeTurn: func(int) error { return nil },
				AfterTurn:  func(_ int, turn *OpenAIForwardResult, _ error) { turnResult = turn },
			}
			server, serverErr := startPassthroughHookRecordingServer(t, ctx, newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream), passthroughLifecycleAccount(), hooks)
			defer server.Close()
			clientConn := dialPassthroughLifecycleClient(t, server)
			defer func() { _ = clientConn.CloseNow() }()
			requirePassthroughUpstreamWrite(t, upstream, time.Second)

			event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
			require.NoError(t, err)
			require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
			select {
			case err := <-serverErr:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			for cachedPath, want := range tc.wantClient {
				require.Equal(t, want, gjson.GetBytes(event, cachedPath).Int(), cachedPath)
			}
			require.NotNil(t, turnResult)
			require.Equal(t, tc.wantBilled, turnResult.Usage)
		})
	}
}
