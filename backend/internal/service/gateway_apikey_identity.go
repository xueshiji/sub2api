package service

// Anthropic api_key 账号的 Claude Code 身份归一化：将 OAuth 路径已有的统一指纹
// 与 metadata.user_id 重写能力，以"请求判定为 Claude Code 流量"为条件应用到
// api_key 转发路径；非 Claude Code 流量保持透传。固定启用，不随 OAuth 路径的
// 全局转发设置开关变化。指纹获取失败时整体降级透传；metadata.user_id 无法解析
// 时跳过该项重写，其余归一化保持生效。
// 归一化范围另含 dateline 隐写清洗（与 OAuth 路径同一管理开关）、身份头兜底
// （x-app 等，仅补缺失、不覆盖已有，由各构建函数应用）与 CC 会话头收敛
// （X-Claude-Code-Session-Id 收敛为账号级确定性派生，body 有可解析 user_id 时以其为准）。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/anthropicfp"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// isClaudeCodeIdentityRequest 判定出站请求是否为 Claude Code 流量，
// 采用 strict 回退判定（见 isClaudeCodeTraffic）：归一化会改写出站请求，误判代价高。
func isClaudeCodeIdentityRequest(ctx context.Context, c *gin.Context, body []byte) bool {
	var ua string
	if c != nil && c.Request != nil {
		ua = c.GetHeader("User-Agent")
	}
	metadataUserID := ""
	if len(body) > 0 {
		metadataUserID = gjson.GetBytes(body, "metadata.user_id").String()
	}
	return isClaudeCodeTraffic(ctx, ua, metadataUserID, body, true)
}

// derivedAPIKeyAccountUUID 为缺少 extra.account_uuid 的 api_key 账号派生稳定的
// account UUID。确定性派生（而非请求路径写库）保证多实例部署下幂等一致。
func derivedAPIKeyAccountUUID(accountID int64) string {
	return generateUUIDFromSeed(fmt.Sprintf("anthropic-apikey-account-uuid::%d", accountID))
}

// normalizeAPIKeyClaudeCodeIdentity 对 Anthropic api_key 账号应用身份归一化：
// 账号级统一指纹 + metadata.user_id 重写（device_id / account_uuid 收敛为账号级
// 单值，session_id 重算为确定性哈希）+ dateline 隐写清洗 + billing header 版本
// 同步。仅 Claude Code 流量生效。指纹获取失败时记 Warn 并整体降级为透传原始
// 请求；metadata.user_id 无法解析时记 Warn 并跳过该项重写，其余归一化保持生效。
func (s *GatewayService) normalizeAPIKeyClaudeCodeIdentity(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) ([]byte, *Fingerprint) {
	if s == nil || s.identityService == nil || account == nil || account.Type != AccountTypeAPIKey {
		return body, nil
	}
	if !isClaudeCodeIdentityRequest(ctx, c, body) {
		return body, nil
	}
	logDegraded := func(step string, err error) {
		slog.Warn("API Key 账号 Claude Code 身份归一化失败，已降级为透传（上游将看到各客户端真实设备身份）",
			"component", "gateway.apikey_identity",
			"account_id", account.ID,
			"platform", account.Platform,
			"step", step,
			"error", err.Error(),
		)
	}
	fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, clientHeadersOf(c))
	if err != nil {
		logDegraded("get_fingerprint", err)
		return body, nil
	}
	if fp.ClientID != "" {
		accountUUID := account.GetExtraString("account_uuid")
		if accountUUID == "" {
			accountUUID = derivedAPIKeyAccountUUID(account.ID)
		}
		// 会话伪装有意不适用于 api_key 账号（IsSessionIDMaskingEnabled 仅对
		// OAuth/SetupToken 生效）：多用户共享 key 需保留会话区分度。
		if uid := gjson.GetBytes(body, "metadata.user_id").String(); uid != "" && ParseMetadataUserID(uid) == nil {
			slog.Warn("API Key 账号 metadata.user_id 无法解析，跳过 metadata.user_id 重写，其余归一化保持生效",
				"component", "gateway.apikey_identity",
				"account_id", account.ID,
				"platform", account.Platform,
				"step", "parse_metadata_user_id",
			)
		} else if newBody, err := s.identityService.RewriteUserID(body, account.ID, accountUUID, fp.ClientID, fp.UserAgent); err == nil && len(newBody) > 0 {
			body = newBody
		}
	}
	// 与 OAuth 路径同一开关、同一清洗，消除 system 中的客户端日期隐写指纹。
	if s.settingService == nil || s.settingService.IsClientDatelineNormalizationEnabled(ctx) {
		if next, _, changed := anthropicfp.NormalizeDateline(body); changed {
			body = next
		}
	}
	return syncBillingHeaderVersion(body, fp.UserAgent), fp
}

// syncClaudeCodeSessionHeader 将 X-Claude-Code-Session-Id 头覆写为 body 当前 metadata.user_id 的
// session 部分，保持会话头与（可能已被重写的）body 一致；头缺失或 user_id 不可解析时不改动。
func syncClaudeCodeSessionHeader(header http.Header, body []byte) {
	if getHeaderRaw(header, "X-Claude-Code-Session-Id") == "" {
		return
	}
	uid := gjson.GetBytes(body, "metadata.user_id").String()
	if uid == "" {
		return
	}
	parsed := ParseMetadataUserID(uid)
	if parsed == nil {
		return
	}
	setHeaderRaw(header, "X-Claude-Code-Session-Id", parsed.SessionID)
}

// normalizeClaudeCodeSessionHeader 归一化 CC 会话头：先将客户端原始 session 头收敛为账号级确定性派生
// （与 messages 路径 body 重写同一派生），再由 body 有可解析 user_id 时以其为准同步。
// 仅在身份归一化已生效（api_key 指纹非 nil）时调用；count_tokens 等 body 无 user_id 的请求
// 依赖第一步完成会话头收敛。
func normalizeClaudeCodeSessionHeader(header http.Header, body []byte, accountID int64) {
	if orig := getHeaderRaw(header, "X-Claude-Code-Session-Id"); orig != "" {
		setHeaderRaw(header, "X-Claude-Code-Session-Id", DerivedSessionID(accountID, orig))
	}
	syncClaudeCodeSessionHeader(header, body)
}
