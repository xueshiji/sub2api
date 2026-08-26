package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	testAPIKeyAccountID      = 42
	testAPIKeyFingerprintUA  = "claude-cli/2.1.22 (external, cli)"
	testAPIKeyFingerprintEnv = "MacOSX"
	testAPIKeyClientID       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newAPIKeyIdentityTestService(t *testing.T) (*GatewayService, *stubIdentityCache) {
	t.Helper()
	cache := &stubIdentityCache{}
	require.NoError(t, cache.SetFingerprint(context.Background(), testAPIKeyAccountID, &Fingerprint{
		ClientID:                testAPIKeyClientID,
		UserAgent:               testAPIKeyFingerprintUA,
		StainlessLang:           "js",
		StainlessPackageVersion: "0.70.0",
		StainlessOS:             testAPIKeyFingerprintEnv,
		StainlessArch:           "arm64",
		StainlessRuntime:        "node",
		StainlessRuntimeVersion: "v24.3.0",
	}))
	return &GatewayService{
		cfg:             &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		identityService: NewIdentityService(cache),
	}, cache
}

func newAPIKeyIdentityTestContext(t *testing.T, ua string, headers map[string]string) (*gin.Context, context.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("User-Agent", ua)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c, req.Context()
}

func testAPIKeyAccount() *Account {
	return &Account{
		ID:          testAPIKeyAccountID,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream-key"},
	}
}

func claudeCodeMetadataUserID(deviceID, sessionID string) string {
	return FormatMetadataUserID(deviceID, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", sessionID, "2.0.0")
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequest_RewritesIdentity(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), origSession)))

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{
		"X-Stainless-OS":           "Linux",
		"X-Claude-Code-Session-Id": origSession,
	})

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	uid := gjson.GetBytes(wireBody, "metadata.user_id").String()
	parsed := ParseMetadataUserID(uid)
	require.NotNil(t, parsed, "rewritten user_id must stay parseable, got: %s", uid)
	require.Equal(t, testAPIKeyClientID, parsed.DeviceID, "device_id must collapse to the account fingerprint ClientID")
	require.Equal(t, derivedAPIKeyAccountUUID(testAPIKeyAccountID), parsed.AccountUUID)
	require.Equal(t, DerivedSessionID(testAPIKeyAccountID, origSession), parsed.SessionID)
	require.NotEqual(t, origSession, parsed.SessionID)

	require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"), "client env header must be overridden by the account fingerprint")
	require.Equal(t, testAPIKeyFingerprintUA, getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "sk-upstream-key", getHeaderRaw(req.Header, "x-api-key"))
	require.Equal(t, parsed.SessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "session header must follow the rewritten body")
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequests_CollapseToSingleIdentity(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()

	build := func(deviceSeed, osHeader, session string) *ParsedUserID {
		t.Helper()
		body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
			claudeCodeMetadataUserID(strings.Repeat(deviceSeed, 64), session)))
		c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{"X-Stainless-OS": osHeader})
		req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
		require.NoError(t, err)
		parsed := ParseMetadataUserID(gjson.GetBytes(wireBody, "metadata.user_id").String())
		require.NotNil(t, parsed)
		require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"))
		require.Equal(t, testAPIKeyFingerprintUA, getHeaderRaw(req.Header, "User-Agent"))
		return parsed
	}

	userA := build("a", "MacOSX", "11111111-2222-4333-8444-555555555555")
	userB := build("c", "Linux", "99999999-8888-4777-8666-555555555555")

	require.Equal(t, userA.DeviceID, userB.DeviceID, "two downstream users must collapse to one upstream device identity")
	require.Equal(t, userA.AccountUUID, userB.AccountUUID)
	require.NotEqual(t, userA.SessionID, userB.SessionID, "sessions must stay distinguishable")
}

func TestAnthropicAPIKeyPassthrough_NonClaudeCodeRequest_StaysPassthrough(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origUserID := "pi-session-metadata"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, origUserID))

	c, ctx := newAPIKeyIdentityTestContext(t, "python-sdk/0.30.0", map[string]string{"X-Stainless-OS": "Linux"})

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origUserID, gjson.GetBytes(wireBody, "metadata.user_id").String(), "non-Claude-Code traffic must keep the client user_id untouched")
	require.Equal(t, "python-sdk/0.30.0", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "Linux", getHeaderRaw(req.Header, "X-Stainless-OS"))
	require.Equal(t, "sk-upstream-key", getHeaderRaw(req.Header, "x-api-key"))
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequest_SyncsBillingHeaderVersion(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"system":[{"type":"text","text":"x-anthropic-billing-header plan=pro cc_version=1.0.0 entrypoint=cli"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), "11111111-2222-4333-8444-555555555555")))

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, nil)

	_, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(), "cc_version=2.1.22", "billing cc_version must follow the fingerprint User-Agent version")
}

func TestBuildUpstreamRequest_AnthropicAPIKey_ClaudeCodeRequest_AppliesIdentity(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), origSession)))

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{"X-Stainless-OS": "Linux"})

	req, wireBody, err := svc.buildUpstreamRequest(ctx, c, account, body, "sk-upstream-key", "apikey", "claude-sonnet-4-5", true, false)
	require.NoError(t, err)

	parsed := ParseMetadataUserID(gjson.GetBytes(wireBody, "metadata.user_id").String())
	require.NotNil(t, parsed)
	require.Equal(t, testAPIKeyClientID, parsed.DeviceID)
	require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"))
	require.Equal(t, "sk-upstream-key", getHeaderRaw(req.Header, "x-api-key"))
}

func TestBuildUpstreamRequest_APIKeyNonAnthropicPlatform_ClaudeCodeRequest_AppliesIdentity(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	account.Platform = PlatformAntigravity
	clientUA := "claude-cli/2.0.5 (external, cli)"
	origSession := "11111111-2222-4333-8444-555555555555"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), origSession)))

	c, ctx := newAPIKeyIdentityTestContext(t, clientUA, map[string]string{"X-Stainless-OS": "Linux"})

	req, wireBody, err := svc.buildUpstreamRequest(ctx, c, account, body, "sk-upstream-key", "apikey", "claude-sonnet-4-5", true, false)
	require.NoError(t, err)

	parsed := ParseMetadataUserID(gjson.GetBytes(wireBody, "metadata.user_id").String())
	require.NotNil(t, parsed)
	require.Equal(t, testAPIKeyClientID, parsed.DeviceID, "非 anthropic 平台的 api_key 账号同样要重写为账号级 device_id")
	require.Equal(t, derivedAPIKeyAccountUUID(testAPIKeyAccountID), parsed.AccountUUID, "非 anthropic 平台的 api_key 账号同样要重写为账号级 account_uuid")
	require.Equal(t, testAPIKeyFingerprintUA, getHeaderRaw(req.Header, "User-Agent"), "非 anthropic 平台的 api_key 账号同样要覆写为指纹 UA")
	require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"), "非 anthropic 平台的 api_key 账号同样要覆写出站环境头")
}

func TestAnthropicAPIKeyPassthrough_FingerprintCacheError_DegradesToPassthrough(t *testing.T) {
	svc, cache := newAPIKeyIdentityTestService(t)
	cache.getFingerprintErr = errors.New("redis unavailable")
	account := testAPIKeyAccount()
	clientUA := "claude-cli/2.0.5 (external, cli)"
	origUserID := claudeCodeMetadataUserID(strings.Repeat("a", 64), "11111111-2222-4333-8444-555555555555")
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, origUserID))

	c, ctx := newAPIKeyIdentityTestContext(t, clientUA, map[string]string{"X-Stainless-OS": "Linux"})

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origUserID, gjson.GetBytes(wireBody, "metadata.user_id").String(), "cache failure must pass the original body through")
	require.Equal(t, clientUA, getHeaderRaw(req.Header, "User-Agent"), "cache failure must keep the client User-Agent")
	require.Equal(t, "Linux", getHeaderRaw(req.Header, "X-Stainless-OS"), "cache failure must keep the client environment headers")
}

func TestAnthropicAPIKeyPassthrough_UnparseableUserIDWithBillingBlock_StaysPassthrough(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origUserID := "proxy-gateway-opaque-user-id"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"system":[{"type":"text","text":"x-anthropic-billing-header plan=pro cc_version=1.0.0 cc_entrypoint=cli"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, origUserID))

	c, ctx := newAPIKeyIdentityTestContext(t, "Go-http-client/2.0", map[string]string{"X-Stainless-OS": "Linux"})

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origUserID, gjson.GetBytes(wireBody, "metadata.user_id").String(), "unparseable user_id must keep the request fully passthrough")
	require.Equal(t, "Go-http-client/2.0", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "Linux", getHeaderRaw(req.Header, "X-Stainless-OS"))
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeClientWithUnparseableUserID_KeepsUserIDAndAppliesFingerprint(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origUserID := "opaque-client-user-id"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, origUserID))

	c, ctx := newAPIKeyIdentityTestContext(t, "claude-cli/2.0.5 (external, cli)", nil)
	ctx = SetClaudeCodeClient(ctx, true)

	req, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origUserID, gjson.GetBytes(wireBody, "metadata.user_id").String(), "unparseable user_id must stay untouched while the rest of the normalization applies")
	require.Equal(t, testAPIKeyFingerprintUA, getHeaderRaw(req.Header, "User-Agent"), "fingerprint User-Agent must still be applied")
	require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"))
}

func claudeCodeAPIKeyPassthroughBody(t *testing.T, system string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"system":[{"type":"text","text":%q}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), "11111111-2222-4333-8444-555555555555"), system))
}

func TestAnthropicAPIKeyPassthrough_RebuildFromSameBody_KeepsStableSessionHash(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origBody := claudeCodeAPIKeyPassthroughBody(t, "You are Claude Code.")

	build := func(input []byte) (string, []byte) {
		t.Helper()
		c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, nil)
		_, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, input, "sk-upstream-key")
		require.NoError(t, err)
		parsed := ParseMetadataUserID(gjson.GetBytes(wireBody, "metadata.user_id").String())
		require.NotNil(t, parsed)
		return parsed.SessionID, wireBody
	}

	sessionFirst, firstBody := build(origBody)
	sessionSecond, _ := build(origBody)
	require.Equal(t, sessionFirst, sessionSecond, "independent builds from the same body must derive the same session")

	sessionRebuilt, _ := build(firstBody)
	require.Equal(t, sessionFirst, sessionRebuilt, "rebuilding from the already-normalized body must not re-hash the session")
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequestWithDatelineVariant_CleansSystemDateline(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, nil)
	_, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		ctx, c, account, claudeCodeAPIKeyPassthroughBody(t, "<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"), "sk-upstream-key")
	require.NoError(t, err)

	sys := gjson.GetBytes(wireBody, "system.0.text").String()
	require.Contains(t, sys, "Today's date is 2026-07-01.")
	require.NotContains(t, sys, "2026/07/01")
}

func TestAnthropicAPIKeyPassthrough_NonClaudeCodeRequestWithDatelineVariant_KeepsSystemDateline(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	dirty := "<system-reminder>\nToday’s date is 2026/07/01.\n</system-reminder>"
	body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"pi-session-metadata"},"system":[{"type":"text","text":%q}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, dirty))

	c, ctx := newAPIKeyIdentityTestContext(t, "python-sdk/0.30.0", nil)
	_, wireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, dirty, gjson.GetBytes(wireBody, "system.0.text").String())
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequestWithoutXApp_FillsDefaultXApp(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, nil)
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeAPIKeyPassthroughBody(t, "You are Claude Code."), "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, "cli", getHeaderRaw(req.Header, "X-App"))
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequestWithXApp_KeepsClientXApp(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{"X-App": "custom"})
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeAPIKeyPassthroughBody(t, "You are Claude Code."), "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, "custom", getHeaderRaw(req.Header, "X-App"))
}

func TestAnthropicAPIKeyPassthrough_NonClaudeCodeRequestWithoutXApp_AddsNoXApp(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"pi-session-metadata"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	c, ctx := newAPIKeyIdentityTestContext(t, "python-sdk/0.30.0", nil)
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Empty(t, getHeaderRaw(req.Header, "X-App"))
}

func claudeCodeCountTokensBody() []byte {
	return []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
}

func TestCountTokensAPIKeyPassthrough_ClaudeCodeRequest_SessionHeaderCollapsesToDerivedValue(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{
		"X-Claude-Code-Session-Id": origSession,
	})
	ctx = SetClaudeCodeClient(ctx, true)

	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeCountTokensBody(), "sk-upstream-key")
	require.NoError(t, err)

	derived := DerivedSessionID(testAPIKeyAccountID, origSession)
	require.NotEqual(t, origSession, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "count_tokens 会话头不得透传客户端真实 session UUID")
	require.Equal(t, derived, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))

	msgBody := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-5","metadata":{"user_id":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		claudeCodeMetadataUserID(strings.Repeat("a", 64), origSession)))
	msgC, msgCtx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, nil)
	_, msgWireBody, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(msgCtx, msgC, account, msgBody, "sk-upstream-key")
	require.NoError(t, err)
	msgParsed := ParseMetadataUserID(gjson.GetBytes(msgWireBody, "metadata.user_id").String())
	require.NotNil(t, msgParsed)
	require.Equal(t, msgParsed.SessionID, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "count_tokens 会话头须与同 session 的 messages 请求重写后的 session 一致")
}

func TestCountTokensAPIKeyPassthrough_ClaudeCodeRequest_AppliesFingerprintIdentity(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	clientUA := "claude-cli/2.0.5 (external, cli)"

	c, ctx := newAPIKeyIdentityTestContext(t, clientUA, map[string]string{"X-Stainless-OS": "Linux"})
	ctx = SetClaudeCodeClient(ctx, true)

	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeCountTokensBody(), "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, testAPIKeyFingerprintUA, getHeaderRaw(req.Header, "User-Agent"), "count_tokens 出站 UA 必须覆写为账号指纹 UA")
	require.Equal(t, testAPIKeyFingerprintEnv, getHeaderRaw(req.Header, "X-Stainless-OS"), "count_tokens 出站环境头必须覆写为指纹值")
	require.Equal(t, "cli", getHeaderRaw(req.Header, "X-App"), "x-app 缺失时必须补默认值 cli")
}

func TestBuildCountTokensRequest_APIKeyClaudeCodeRequest_SessionHeaderCollapsesToDerivedValue(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{
		"X-Claude-Code-Session-Id": origSession,
	})
	ctx = SetClaudeCodeClient(ctx, true)

	req, _, err := svc.buildCountTokensRequest(ctx, c, account, claudeCodeCountTokensBody(), "sk-upstream-key", "apikey", "claude-sonnet-4-5", false)
	require.NoError(t, err)

	require.Equal(t, DerivedSessionID(testAPIKeyAccountID, origSession), getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))
}

func TestAnthropicAPIKeyPassthrough_NonClaudeCodeRequest_KeepsSessionHeader(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"pi-session-metadata"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	c, ctx := newAPIKeyIdentityTestContext(t, "python-sdk/0.30.0", map[string]string{
		"X-Claude-Code-Session-Id": origSession,
	})

	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origSession, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "非 Claude Code 流量会话头保持原值")
}

func TestCountTokensAPIKeyPassthrough_NonClaudeCodeRequest_KeepsSessionHeader(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"

	c, ctx := newAPIKeyIdentityTestContext(t, "python-sdk/0.30.0", map[string]string{
		"X-Claude-Code-Session-Id": origSession,
	})

	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeCountTokensBody(), "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, origSession, getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "非 Claude Code 流量会话头保持原值")
}

func TestAnthropicAPIKeyPassthrough_ClaudeCodeRequestWithoutMetadataUserID_SessionHeaderCollapsesToDerivedValue(t *testing.T) {
	svc, _ := newAPIKeyIdentityTestService(t)
	account := testAPIKeyAccount()
	origSession := "11111111-2222-4333-8444-555555555555"

	c, ctx := newAPIKeyIdentityTestContext(t, testAPIKeyFingerprintUA, map[string]string{
		"X-Claude-Code-Session-Id": origSession,
	})
	ctx = SetClaudeCodeClient(ctx, true)

	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(ctx, c, account, claudeCodeCountTokensBody(), "sk-upstream-key")
	require.NoError(t, err)

	require.Equal(t, DerivedSessionID(testAPIKeyAccountID, origSession), getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"), "body 无可解析 user_id 时会话头收敛为账号级派生")
}
