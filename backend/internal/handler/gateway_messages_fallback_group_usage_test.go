//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// fallbackGroupRepo 按 ID 返回分组，原分组调度与兜底分组解析共用。
type fallbackGroupRepo struct {
	*fakeGroupRepo
	groups map[int64]*service.Group
}

func (r *fallbackGroupRepo) GetByID(_ context.Context, id int64) (*service.Group, error) {
	return r.groups[id], nil
}

func (r *fallbackGroupRepo) GetByIDLite(_ context.Context, id int64) (*service.Group, error) {
	return r.groups[id], nil
}

// fallbackSchedulerCache 读取快照时只返回平台与快照桶一致的账号。
type fallbackSchedulerCache struct {
	*fakeSchedulerCache
}

func (c *fallbackSchedulerCache) GetSnapshot(_ context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	accounts := make([]*service.Account, 0, len(c.accounts))
	for _, account := range c.accounts {
		if account.Platform == bucket.Platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, true, nil
}

// fallbackUpstream 每次调用返回同一份预置响应。
type fallbackUpstream struct {
	service.HTTPUpstream
	status      int
	contentType string
	body        string
	calls       int
}

func (u *fallbackUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{"Content-Type": []string{u.contentType}},
		Body:       io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func (u *fallbackUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type fallbackSettingRepo struct{ service.SettingRepository }

func (fallbackSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

type fallbackUsageLogRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *fallbackUsageLogRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return true, nil
}

const fallbackUsageNonStreamBody = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5",` +
	`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":3000,"cache_creation_input_tokens":10000,"cache_read_input_tokens":150000,"output_tokens":500}}`

const fallbackUsageStreamBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","usage":{"input_tokens":3000,"cache_creation_input_tokens":10000,"cache_read_input_tokens":150000,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":500}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type fallbackUsageRun struct {
	rec             *httptest.ResponseRecorder
	fallbackGroupID int64
	antigravity     *fallbackUpstream
	anthropic       *fallbackUpstream
	usageLogRepo    *fallbackUsageLogRepo
}

// runPromptTooLongFallbackMessages 经 GatewayHandler.Messages 走完一次 prompt too long 回落：
// 原分组的 antigravity 账号返回 400，兜底分组的 anthropic 账号返回带 usage 的成功响应。
func runPromptTooLongFallbackMessages(t *testing.T, stream bool, origAdjustment, fallbackAdjustment float64) fallbackUsageRun {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple, Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}

	fallbackGroupID := int64(9512)
	origGroup := &service.Group{
		ID: 9511, Hydrated: true, Platform: service.PlatformAntigravity, Status: service.StatusActive,
		RateMultiplier: 1, CacheHitRateAdjustment: origAdjustment, FallbackGroupIDOnInvalidRequest: &fallbackGroupID,
	}
	fallbackGroup := &service.Group{
		ID: fallbackGroupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard, RateMultiplier: 1, CacheHitRateAdjustment: fallbackAdjustment,
	}
	antigravityAccount := &service.Account{
		ID: 9513, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"access_token": "token", "project_id": "proj"},
		AccountGroups: []service.AccountGroup{{AccountID: 9513, GroupID: origGroup.ID}},
	}
	anthropicAccount := &service.Account{
		ID: 9514, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials:   map[string]any{"api_key": "test-key"},
		AccountGroups: []service.AccountGroup{{AccountID: 9514, GroupID: fallbackGroupID}},
	}

	snapshot := service.NewSchedulerSnapshotService(&fallbackSchedulerCache{
		fakeSchedulerCache: &fakeSchedulerCache{accounts: []*service.Account{antigravityAccount, anthropicAccount}},
	}, nil, nil, nil, nil)
	run := fallbackUsageRun{
		rec:             httptest.NewRecorder(),
		fallbackGroupID: fallbackGroupID,
		antigravity: &fallbackUpstream{
			status: http.StatusBadRequest, contentType: "application/json",
			body: `{"error":{"message":"Prompt is too long"}}`,
		},
		anthropic:    &fallbackUpstream{status: http.StatusOK, contentType: "application/json", body: fallbackUsageNonStreamBody},
		usageLogRepo: &fallbackUsageLogRepo{},
	}
	if stream {
		run.anthropic.contentType = "text/event-stream"
		run.anthropic.body = fallbackUsageStreamBody
	}
	gw := service.NewGatewayService(
		nil, &fallbackGroupRepo{fakeGroupRepo: &fakeGroupRepo{}, groups: map[int64]*service.Group{
			origGroup.ID: origGroup, fallbackGroupID: fallbackGroup,
		}}, run.usageLogRepo, nil, nil, nil, nil, nil, cfg,
		snapshot, nil, service.NewBillingService(cfg, nil), nil, nil, nil, run.anthropic,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	antigravity := service.NewAntigravityGatewayService(
		nil, nil, nil, &service.AntigravityTokenProvider{}, nil, run.antigravity,
		service.NewSettingService(fallbackSettingRepo{}, cfg), nil,
	)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	h := &GatewayHandler{
		gatewayService: gw, antigravityGatewayService: antigravity, billingCacheService: billingCache,
		cfg: cfg, maxAccountSwitches: 1,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
	}

	requestBody := `{"model":"claude-sonnet-4-5","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"fallback usage"}]}`
	if stream {
		requestBody = strings.Replace(requestBody, `"stream":false`, `"stream":true`, 1)
	}
	c, _ := gin.CreateTestContext(run.rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fallback-usage-test")
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, origGroup))
	apiKey := &service.APIKey{
		ID: 9515, UserID: 9516, GroupID: &origGroup.ID, Group: origGroup, Status: service.StatusActive,
		User: &service.User{ID: 9516, Balance: 100, Concurrency: 10},
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})

	h.Messages(c)
	return run
}

// clientVisibleUsage 返回非流式响应体或流式 message_start 事件里客户端可见的 usage。
func clientVisibleUsage(t *testing.T, body string, stream bool) gjson.Result {
	t.Helper()
	if !stream {
		return gjson.Get(body, "usage")
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if gjson.Get(payload, "type").String() == "message_start" {
			return gjson.Get(payload, "message.usage")
		}
	}
	t.Fatalf("message_start event not found in streamed response:\n%s", body)
	return gjson.Result{}
}

// prompt too long 回落到兜底分组后，客户端可见用量与 usage_logs 主字段都按兜底分组的修正比例计算。
func TestGatewayMessages_PromptTooLongFallbackClientUsageMatchesUsageLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		mode := "non_stream"
		if stream {
			mode = "stream"
		}
		for _, tt := range []struct {
			name               string
			origAdjustment     float64
			fallbackAdjustment float64
			wantInput          int
			wantCacheRead      int
			wantReduction      int
			wantAdjustment     float64
		}{
			{
				name:           "orig group adjusted and fallback group not adjusted",
				origAdjustment: 0.05, fallbackAdjustment: 0,
				wantInput: 3000, wantCacheRead: 150000,
			},
			{
				name:           "orig group not adjusted and fallback group adjusted",
				origAdjustment: 0, fallbackAdjustment: 0.05,
				wantInput: 11150, wantCacheRead: 141850, wantReduction: 8150, wantAdjustment: 0.05,
			},
		} {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				run := runPromptTooLongFallbackMessages(t, stream, tt.origAdjustment, tt.fallbackAdjustment)

				require.Equal(t, http.StatusOK, run.rec.Code, run.rec.Body.String())
				require.Equal(t, 1, run.antigravity.calls, "orig group antigravity account is tried once")
				require.Equal(t, 1, run.anthropic.calls, "fallback group account is tried once")

				clientUsage := clientVisibleUsage(t, run.rec.Body.String(), stream)
				require.Equal(t, int64(tt.wantInput), clientUsage.Get("input_tokens").Int())
				require.Equal(t, int64(tt.wantCacheRead), clientUsage.Get("cache_read_input_tokens").Int())

				run.usageLogRepo.mu.Lock()
				defer run.usageLogRepo.mu.Unlock()
				require.Len(t, run.usageLogRepo.logs, 1)
				usageLog := run.usageLogRepo.logs[0]
				require.NotNil(t, usageLog.GroupID)
				require.Equal(t, run.fallbackGroupID, *usageLog.GroupID, "usage is logged under the fallback group")
				require.Equal(t, tt.wantInput, usageLog.InputTokens)
				require.Equal(t, tt.wantCacheRead, usageLog.CacheReadTokens)
				require.Equal(t, tt.wantReduction, usageLog.CacheReadTokensReduction)
				require.InDelta(t, tt.wantAdjustment, usageLog.CacheHitRateAdjustment, 1e-12)
			})
		}
	}
}
