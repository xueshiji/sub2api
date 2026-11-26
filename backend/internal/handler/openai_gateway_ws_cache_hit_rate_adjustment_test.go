package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游每一轮固定回同一份用量，缓存命中率足够高，任何一档修正比例都会触发扣减。
const (
	wsCacheHitRateUpstreamInputTokens  = 10000
	wsCacheHitRateUpstreamCachedTokens = 8000
)

func wsCacheHitRateUpstreamCompletedEvent(model string) string {
	return fmt.Sprintf(
		`{"type":"response.completed","response":{"id":"resp_ws_cache_hit_rate","model":%q,"status":"completed","usage":{"input_tokens":%d,"output_tokens":1,"input_tokens_details":{"cached_tokens":%d}}}}`,
		model,
		wsCacheHitRateUpstreamInputTokens,
		wsCacheHitRateUpstreamCachedTokens,
	)
}

// 分组缓存命中率修正比例在两个 turn 之间调整时，每一轮客户端看到的 cached_tokens
// 等于该轮 usage_logs 的修正口径。
func TestOpenAIResponsesWebSocket_CacheHitRateAdjustmentChangeAppliesToNextTurn(t *testing.T) {
	type wsCacheHitRateTurn struct {
		adjustment float64
		input      int
		cacheRead  int
		reduction  int
	}
	for _, mode := range []string{
		service.OpenAIWSIngressModePassthrough,
		service.OpenAIWSIngressModeCtxPool,
		service.OpenAIWSIngressModeHTTPBridge,
		service.OpenAIWSIngressModeShared,
		service.OpenAIWSIngressModeDedicated,
	} {
		for _, tc := range []struct {
			name       string
			secondTurn wsCacheHitRateTurn
		}{
			{
				name:       "raised to 0.10",
				secondTurn: wsCacheHitRateTurn{adjustment: 0.10, input: 3000, cacheRead: 7000, reduction: 1000},
			},
			{
				name:       "set to 0",
				secondTurn: wsCacheHitRateTurn{adjustment: 0, input: 2000, cacheRead: 8000, reduction: 0},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				repo := newWSGroupPricingAPIKeyRepoStub(3.0)
				repo.group.CacheHitRateAdjustment = 0.05
				apiKeyService := newWSGroupPricingAPIKeyService(repo)
				clientEvents, logs := runOpenAIResponsesWebSocketCacheHitRateCase(t, mode, apiKeyService, func() error {
					return repo.adminUpdate(apiKeyService, func(_ *service.APIKey, group *service.Group) {
						group.CacheHitRateAdjustment = tc.secondTurn.adjustment
					})
				})

				require.Len(t, clientEvents, 2)
				require.Len(t, logs, 2)
				want := []wsCacheHitRateTurn{
					{adjustment: 0.05, input: 2500, cacheRead: 7500, reduction: 500},
					tc.secondTurn,
				}
				for i, expected := range want {
					clientUsage := gjson.GetBytes(clientEvents[i], "response.usage")
					require.Equal(t, int64(wsCacheHitRateUpstreamInputTokens), clientUsage.Get("input_tokens").Int(), "turn %d", i+1)
					require.Equal(t, int64(expected.cacheRead), clientUsage.Get("input_tokens_details.cached_tokens").Int(),
						"turn %d: client usage must carry the same adjustment as the usage log", i+1)
					require.InDelta(t, expected.adjustment, logs[i].CacheHitRateAdjustment, 1e-12, "turn %d", i+1)
					require.Equal(t, expected.input, logs[i].InputTokens, "turn %d", i+1)
					require.Equal(t, expected.cacheRead, logs[i].CacheReadTokens, "turn %d", i+1)
					require.Equal(t, expected.reduction, logs[i].CacheReadTokensReduction, "turn %d", i+1)
				}
			})
		}
	}
}

// runOpenAIResponsesWebSocketCacheHitRateCase 在指定 ingress 模式下用同一把 Key 跑两个
// turn，第一轮请求到达上游后执行 afterFirstUpstreamRequest。返回客户端收到的
// response.completed 事件与两轮的 usage_logs（按轮次排序）。
func runOpenAIResponsesWebSocketCacheHitRateCase(
	t *testing.T,
	ingressMode string,
	apiKeyService *service.APIKeyService,
	afterFirstUpstreamRequest func() error,
) ([][]byte, []*service.UsageLog) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	const turnCount = 2
	upstreamPayloadCh := make(chan []byte, turnCount)
	upstreamErrCh := make(chan error, 1)
	// http_bridge 的每个 turn 是一次独立的 HTTP 请求，其余模式共用一条上游 WebSocket。
	var httpBridgeTurn atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				upstreamErrCh <- err
				return
			}
			upstreamPayloadCh <- payload
			if int(httpBridgeTurn.Add(1)) == 1 {
				if err := afterFirstUpstreamRequest(); err != nil {
					upstreamErrCh <- err
					return
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", wsCacheHitRateUpstreamCompletedEvent(gjson.GetBytes(payload, "model").String()))
			return
		}
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			upstreamErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for turn := 1; turn <= turnCount; turn++ {
			readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
			msgType, payload, readErr := conn.Read(readCtx)
			cancelRead()
			if readErr != nil {
				upstreamErrCh <- readErr
				return
			}
			if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
				upstreamErrCh <- errors.New("unexpected upstream websocket message type")
				return
			}
			upstreamPayloadCh <- payload
			if turn == 1 {
				if err := afterFirstUpstreamRequest(); err != nil {
					upstreamErrCh <- err
					return
				}
			}
			writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
			writeErr := conn.Write(writeCtx, coderws.MessageText, []byte(wsCacheHitRateUpstreamCompletedEvent(gjson.GetBytes(payload, "model").String())))
			cancelWrite()
			if writeErr != nil {
				upstreamErrCh <- writeErr
				return
			}
		}
	}))
	defer upstreamServer.Close()

	account := service.Account{
		ID:          9905,
		Name:        "openai-ws-cache-hit-rate",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": upstreamServer.URL,
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"openai_apikey_responses_websockets_v2_mode":    ingressMode,
		},
	}

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	accountRepo := &openAIWSUsageHandlerAccountRepoStub{account: account}
	usageRepo := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, turnCount)}

	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		&compositeWSHTTPUpstream{},
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}
	h := &OpenAIGatewayHandler{
		cfg:                 cfg,
		gatewayService:      gatewaySvc,
		billingCacheService: billingCacheSvc,
		apiKeyService:       apiKeyService,
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}

	apiKey, err := apiKeyService.GetByKey(context.Background(), wsGroupPricingKey)
	require.NoError(t, err)
	require.NotNil(t, apiKey.Group)

	handlerDone := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.User.ID, Concurrency: 1})
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, apiKey.Group))
		c.Next()
	})
	router.GET("/openai/v1/responses", func(c *gin.Context) {
		h.ResponsesWebSocket(c)
		close(handlerDone)
	})
	handlerServer := httptest.NewServer(router)
	defer handlerServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(handlerServer.URL, "http")+"/openai/v1/responses",
		&coderws.DialOptions{CompressionMode: coderws.CompressionContextTakeover},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientEvents := make([][]byte, 0, turnCount)
	for turn := 1; turn <= turnCount; turn++ {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(wsGroupPricingFirst)))
		cancelWrite()

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
		clientEvents = append(clientEvents, append([]byte(nil), event...))
	}
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not finish after the client closed")
	}
	select {
	case err := <-upstreamErrCh:
		require.NoError(t, err)
	default:
	}

	logs := make([]*service.UsageLog, 0, turnCount)
	for len(logs) < turnCount {
		select {
		case usageLog := <-usageRepo.created:
			require.NotNil(t, usageLog)
			logs = append(logs, usageLog)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for websocket usage logs")
		}
	}
	for range turnCount {
		select {
		case <-upstreamPayloadCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for the upstream request frame")
		}
	}
	return clientEvents, logs
}
