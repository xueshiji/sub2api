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

const openAIWSTurnCacheHitConnectAdjustment = 0.05

func openAIWSTurnCacheHitCompletedEvent(responseID string) string {
	return fmt.Sprintf(
		`{"type":"response.completed","response":{"id":%q,"model":"gpt-5.1","status":"completed","usage":{"input_tokens":10000,"output_tokens":1,"input_tokens_details":{"cached_tokens":8000}}}}`,
		responseID,
	)
}

func openAIWSTurnCacheHitClientCachedTokens(event []byte) int64 {
	return gjson.GetBytes(event, "response.usage.input_tokens_details.cached_tokens").Int()
}

// runOpenAIWSTurnCacheHitRateCase 在指定 ingress 模式下以建连分组比例 5% 跑两个 turn，
// 返回客户端收到的两个 terminal 事件；转发返回后调用方才可以读取 hooks 记录的状态。
func runOpenAIWSTurnCacheHitRateCase(t *testing.T, ctx context.Context, ingressMode string, hooks *OpenAIWSIngressHooks) [][]byte {
	t.Helper()
	if ingressMode == OpenAIWSIngressModePassthrough {
		return runOpenAIWSTurnCacheHitRatePassthroughCase(t, ctx, hooks)
	}
	return runOpenAIWSTurnCacheHitRatePoolCase(t, ctx, hooks)
}

func runOpenAIWSTurnCacheHitRatePassthroughCase(t *testing.T, ctx context.Context, hooks *OpenAIWSIngressHooks) [][]byte {
	t.Helper()
	gin.SetMode(gin.TestMode)

	upstream := newStagedPassthroughConn()
	upstream.Send(openAIWSTurnCacheHitCompletedEvent("resp_turn_1"))
	server, serverErr := startPassthroughHookRecordingServer(
		t,
		ctx,
		newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream),
		passthroughLifecycleAccount(),
		hooks,
	)
	defer server.Close()
	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)

	events := make([][]byte, 0, 2)
	first, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	events = append(events, first)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1"}`)))
	cancelWrite()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(openAIWSTurnCacheHitCompletedEvent("resp_turn_2"))
	second, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	events = append(events, second)

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-serverErr:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return events
}

func runOpenAIWSTurnCacheHitRatePoolCase(t *testing.T, ctx context.Context, hooks *OpenAIWSIngressHooks) [][]byte {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := newOpenAIWSExecutionScopeTestConfig()
	capture := &openAIWSCaptureConn{events: [][]byte{
		[]byte(openAIWSTurnCacheHitCompletedEvent("resp_turn_1")),
		[]byte(openAIWSTurnCacheHitCompletedEvent("resp_turn_2")),
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
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		c, _ := gin.CreateTestContext(w)
		c.Request = r.WithContext(ctx)
		serverErr <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "sk-test", []byte(`{"type":"response.create","model":"gpt-5.1","input":"hello"}`), hooks)
	}))
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()

	events := make([][]byte, 0, 2)
	for turn := 1; turn <= 2; turn++ {
		if turn == 2 {
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"again"}`)))
		}
		_, event, err := client.Read(ctx)
		require.NoError(t, err)
		events = append(events, event)
	}
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-serverErr:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return events
}

// 转发层在每个 turn 的 BeforeTurn 之后读取该 turn 计费分组的修正比例：第二轮客户端
// 用量按新比例改写，首轮不受影响，转发给计费的原始用量保持不变。
func TestOpenAIWS_ClientUsageFollowsTurnCacheHitRateAdjustment(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		for _, tc := range []struct {
			name                 string
			secondTurnAdjustment float64
			wantSecondTurnCached int64
		}{
			{name: "raised", secondTurnAdjustment: 0.10, wantSecondTurnCached: 7000},
			{name: "disabled", secondTurnAdjustment: 0, wantSecondTurnCached: 8000},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: openAIWSTurnCacheHitConnectAdjustment})
				results := make(chan *OpenAIForwardResult, 2)
				hooks := &OpenAIWSIngressHooks{
					BeforeTurn: func(int) error { return nil },
					TurnCacheHitRateAdjustment: func(turn int) float64 {
						if turn >= 2 {
							return tc.secondTurnAdjustment
						}
						return openAIWSTurnCacheHitConnectAdjustment
					},
					AfterTurn: func(_ int, turn *OpenAIForwardResult, _ error) {
						if turn != nil {
							results <- turn
						}
					},
				}

				events := runOpenAIWSTurnCacheHitRateCase(t, ctx, mode, hooks)

				require.Len(t, events, 2)
				require.Equal(t, int64(7500), openAIWSTurnCacheHitClientCachedTokens(events[0]), "first turn keeps the connect-time adjustment")
				require.Equal(t, tc.wantSecondTurnCached, openAIWSTurnCacheHitClientCachedTokens(events[1]))
				for range 2 {
					select {
					case result := <-results:
						require.Equal(t, OpenAIUsage{InputTokens: 10000, OutputTokens: 1, CacheReadInputTokens: 8000}, result.Usage)
					case <-ctx.Done():
						t.Fatal("turn result was not reported")
					}
				}
			})
		}
	}
}

// 未提供按轮比例的调用方：整条连接沿用建连时的比例。
func TestOpenAIWS_WithoutTurnCacheHitRateAdjustmentKeepsConnectTimeRatio(t *testing.T) {
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 1, CacheHitRateAdjustment: openAIWSTurnCacheHitConnectAdjustment})
			hooks := &OpenAIWSIngressHooks{BeforeTurn: func(int) error { return nil }}

			events := runOpenAIWSTurnCacheHitRateCase(t, ctx, mode, hooks)

			require.Len(t, events, 2)
			for i, event := range events {
				require.Equal(t, int64(7500), openAIWSTurnCacheHitClientCachedTokens(event), "turn %d", i+1)
			}
		})
	}
}
