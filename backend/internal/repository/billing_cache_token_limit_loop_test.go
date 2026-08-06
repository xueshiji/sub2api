//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tokenSubscriptionLoopHarness 按 gateway 生产调用序列模拟 token 型订阅的请求生命周期。
// 序列来源：
//   - preflight 检查: billing_cache_service.go GetSubscriptionStatus（miss → 读 DB 快照 →
//     异步入队 set 重建，快照与 set 之间存在 worker 延迟窗口）
//   - checkSubscriptionEligibility（usage >= limit 拒绝）
//   - 计费完成: gateway_usage_billing.go finalizePostUsageBilling（DB 累加 + 同步 UpdateSubscriptionUsageTokens）
//   - 窗口重置: subscription_service.go EnsureWindowMaintenance（DB 清零 + InvalidateSubscriptionCache DEL）
type tokenSubscriptionLoopHarness struct {
	cache        *billingCache
	db           *tokenUsageDB
	dailyLimit   int64
	weeklyLimit  int64
	tokensPerReq int64
}

// allowedAt 复刻 checkSubscriptionEligibility 的 token 分支。
func (h *tokenSubscriptionLoopHarness) allowedAt(daily, weekly int64) bool {
	dailyOK := h.dailyLimit <= 0 || daily < h.dailyLimit
	weeklyOK := h.weeklyLimit <= 0 || weekly < h.weeklyLimit
	return dailyOK && weeklyOK
}

// tokenUsageDB 模拟 user_subscriptions 表的 token 用量与窗口（权威数据）。
type tokenUsageDB struct {
	dailyUsage  int64
	weeklyUsage int64
}

const (
	loopUserID  = int64(101)
	loopGroupID = int64(202)
)

func newTokenLoopHarness(t *testing.T) *tokenSubscriptionLoopHarness {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &tokenSubscriptionLoopHarness{
		cache: &billingCache{rdb: rdb},
		db:    &tokenUsageDB{},
	}
}

// snapshotDB 复刻 getSubscriptionFromDB：读 DB 快照。
func (h *tokenSubscriptionLoopHarness) snapshotDB() *service.SubscriptionCacheData {
	return &service.SubscriptionCacheData{
		Status:            "active",
		ExpiresAt:         time.Now().Add(24 * time.Hour),
		DailyUsageTokens:  h.db.dailyUsage,
		WeeklyUsageTokens: h.db.weeklyUsage,
	}
}

// rebuild 复刻 cacheWriteWorker 执行 set 任务：用快照覆盖重建缓存。
func (h *tokenSubscriptionLoopHarness) rebuild(ctx context.Context, snapshot *service.SubscriptionCacheData) {
	_ = h.cache.SetSubscriptionCache(ctx, loopUserID, loopGroupID, snapshot)
}

// preflight 复刻 GetSubscriptionStatus + checkSubscriptionEligibility 的 token 分支。
// 返回 (daily, weekly, allowed)。
func (h *tokenSubscriptionLoopHarness) preflight(ctx context.Context) (daily, weekly int64, allowed bool) {
	data, err := h.cache.GetSubscriptionCache(ctx, loopUserID, loopGroupID)
	if err == nil && data != nil {
		return data.DailyUsageTokens, data.WeeklyUsageTokens,
			h.allowedAt(data.DailyUsageTokens, data.WeeklyUsageTokens)
	}
	// miss：读 DB 快照并重建（生产中快照与 set 之间有 worker 延迟）
	snapshot := h.snapshotDB()
	h.rebuild(ctx, snapshot)
	return snapshot.DailyUsageTokens, snapshot.WeeklyUsageTokens,
		h.allowedAt(snapshot.DailyUsageTokens, snapshot.WeeklyUsageTokens)
}

// completeRequest 复刻计费完成：DB 累加（权威）+ 同步缓存累加（key 不存在时 Lua 静默丢弃）。
func (h *tokenSubscriptionLoopHarness) completeRequest(ctx context.Context, tokens int64) {
	h.db.dailyUsage += tokens
	h.db.weeklyUsage += tokens
	_ = h.cache.UpdateSubscriptionUsageTokens(ctx, loopUserID, loopGroupID, tokens)
}

// resetWindows 复刻 EnsureWindowMaintenance 的窗口重置：DB 清零 + DEL 缓存。
func (h *tokenSubscriptionLoopHarness) resetWindows(ctx context.Context) {
	h.db.dailyUsage = 0
	h.db.weeklyUsage = 0
	_ = h.cache.InvalidateSubscriptionCache(ctx, loopUserID, loopGroupID)
}

// driveLoop 串行执行“检查→计费”直到 preflight 拒绝或达到 maxRequests。
// 返回放行的请求数。
func (h *tokenSubscriptionLoopHarness) driveLoop(ctx context.Context, maxRequests int) int {
	granted := 0
	for i := 0; i < maxRequests; i++ {
		_, _, allowed := h.preflight(ctx)
		if !allowed {
			break
		}
		h.completeRequest(ctx, h.tokensPerReq)
		granted++
	}
	return granted
}

// 场景 1：串行正常流量（无窗口重置、无交错）。检查层应准确拦截在限额边界。
func TestTokenLimitLoop_SerialTrafficStopsAtLimit(t *testing.T) {
	h := newTokenLoopHarness(t)
	h.dailyLimit = 100_000
	h.tokensPerReq = 10_000
	ctx := context.Background()

	granted := h.driveLoop(ctx, 100)

	// 允许最后一个请求跨界：consumed <= limit + tokensPerReq
	if granted != 10 {
		t.Fatalf("serial traffic granted=%d requests, want 10 (exact limit boundary)", granted)
	}
	if h.db.dailyUsage != 100_000 {
		t.Fatalf("db usage %d != consumed %d", h.db.dailyUsage, int64(granted)*h.tokensPerReq)
	}
}

// 场景 2（生产交错·日窗口）：跨天重置瞬间，飞行中请求的计费落在
// “preflight DB 快照读取之后、缓存重建 set 之前”的间隙里：
//
//	t1 重置: DB 清零 + DEL 缓存
//	t2 请求 Y preflight miss → 读 DB 快照(0) → [worker 延迟窗口]
//	t3 飞行中请求 X 完成: DB += 5万（落入新窗口，权威正确）; HINCRBY 丢弃（key 不存在）
//	t4 set(快照 0) 覆盖重建 → 缓存 0，真值 5万 → 偏差固化
//	t5 D2 后续流量按缓存放行到 10万 → DB 终值 15万，超过日限额 50%
func TestTokenLimitLoop_DailyWindowResetOrphanedInflightIncrement(t *testing.T) {
	h := newTokenLoopHarness(t)
	h.dailyLimit = 100_000
	h.tokensPerReq = 10_000
	ctx := context.Background()

	// 跨天：窗口重置
	h.resetWindows(ctx)

	// 请求 Y preflight miss：读 DB 快照（0）。X 尚未提交。
	snapshot := h.snapshotDB()

	// 飞行中请求 X 完成（DB 落账新窗口；HINCRBY 因 key 不存在被丢弃）
	h.completeRequest(ctx, 50_000)

	// worker 执行 set（旧快照 0）→ 缓存固化偏差
	h.rebuild(ctx, snapshot)

	// D2 流量：检查层只看到缓存，从 0 放行到限额
	granted := h.driveLoop(ctx, 100)

	// 断言：D2 窗口实际 DB 用量不应显著超过限额（单请求跨界可容忍）
	d2Actual := h.db.dailyUsage
	if d2Actual > h.dailyLimit+h.tokensPerReq {
		t.Fatalf("D2 actual usage=%d exceeds daily limit=%d (cache lost inflight increment; granted=%d)",
			d2Actual, h.dailyLimit, granted)
	}
}

// 场景 3（生产交错·周窗口）：同场景 2，但发生在 7 天周期重置时刻。
// 周窗口 DEL 后的间隙丢失会让整周用量持续偏低（HINCRBY 命中后每次 EXPIRE 续期，
// 偏差在持续流量下不会随 TTL 过期修正）。
func TestTokenLimitLoop_WeeklyWindowResetOrphanedInflightIncrement(t *testing.T) {
	h := newTokenLoopHarness(t)
	h.weeklyLimit = 100_000
	h.tokensPerReq = 10_000
	ctx := context.Background()

	// 周窗口重置
	h.resetWindows(ctx)

	// preflight miss 读快照（weekly=0）
	snapshot := h.snapshotDB()

	// 飞行中请求完成 5 万（HINCRBY 丢弃）
	h.completeRequest(ctx, 50_000)

	// 旧快照覆盖重建
	h.rebuild(ctx, snapshot)

	// 本周流量按缓存放行
	granted := h.driveLoop(ctx, 100)

	weeklyActual := h.db.weeklyUsage
	if weeklyActual > h.weeklyLimit+h.tokensPerReq {
		t.Fatalf("weekly actual usage=%d exceeds weekly limit=%d (granted=%d)",
			weeklyActual, h.weeklyLimit, granted)
	}
}
