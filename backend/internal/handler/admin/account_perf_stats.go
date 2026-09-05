package admin

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var accountPerfStatsBatchCache = newSnapshotCache(30 * time.Second)

func buildAccountPerfStatsBatchCacheKey(accountIDs []int64) string {
	if len(accountIDs) == 0 {
		return "accounts_perf_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(accountIDs) * 6)
	_, _ = b.WriteString("accounts_perf_stats:")
	for i, id := range accountIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

// BatchPerfStatsRequest 批量账号近 30 分钟性能统计请求体。
type BatchPerfStatsRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

// accountPerfScoreSortKey 性能得分排序字段：得分来自内存缓存而非 DB，排序在内存完成。
const accountPerfScoreSortKey = "perf_score"

// listAccountsSortedByPerfScore 按性能得分排序的账号列表：全量取出过滤后账号，
// 按缓存中的账号级得分排序（无分账号排最后，不随方向变化）后内存分页。
func (h *AccountHandler) listAccountsSortedByPerfScore(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode, sortOrder string, page, pageSize int) ([]service.Account, int64, error) {
	accounts, err := h.adminService.ListAccountsForSchedulerScoreFilter(ctx, platform, accountType, status, search, groupID, privacyMode)
	if err != nil {
		return nil, 0, err
	}

	var snapshot map[int64]*service.AccountPerformanceStats
	if h.accountPerfStats != nil {
		snapshot = h.accountPerfStats.Snapshot()
	}
	desc := strings.EqualFold(strings.TrimSpace(sortOrder), "desc")
	scoreOf := func(id int64) (*float64, bool) {
		st, ok := snapshot[id]
		if !ok || st.Score == nil {
			return nil, false
		}
		return st.Score, true
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		si, iOK := scoreOf(accounts[i].ID)
		sj, jOK := scoreOf(accounts[j].ID)
		switch {
		case !iOK && !jOK:
			return accounts[i].ID < accounts[j].ID
		case !iOK:
			return false
		case !jOK:
			return true
		}
		if *si != *sj {
			if desc {
				return *si > *sj
			}
			return *si < *sj
		}
		return accounts[i].ID < accounts[j].ID
	})

	start := (page - 1) * pageSize
	if start >= len(accounts) || start < 0 {
		return []service.Account{}, int64(len(accounts)), nil
	}
	end := start + pageSize
	if end > len(accounts) {
		end = len(accounts)
	}
	return accounts[start:end], int64(len(accounts)), nil
}

// GetBatchAccountPerfStats 批量获取账号近 30 分钟性能指标（平均 TTFT、decode 吞吐）。
// 数据来自内存缓存服务，无数据库查询。
// POST /api/v1/admin/accounts/perf-stats/batch
func (h *AccountHandler) GetBatchAccountPerfStats(c *gin.Context) {
	var req BatchPerfStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	accountIDs := normalizeInt64IDList(req.AccountIDs)
	if len(accountIDs) == 0 {
		response.Success(c, gin.H{"stats": map[string]any{}})
		return
	}

	cacheKey := buildAccountPerfStatsBatchCacheKey(accountIDs)
	if cached, ok := accountPerfStatsBatchCache.Get(cacheKey); ok {
		if cached.ETag != "" {
			c.Header("ETag", cached.ETag)
			c.Header("Vary", "If-None-Match")
			if ifNoneMatchMatched(c.GetHeader("If-None-Match"), cached.ETag) {
				c.Status(http.StatusNotModified)
				return
			}
		}
		c.Header("X-Snapshot-Cache", "hit")
		response.Success(c, cached.Payload)
		return
	}

	stats := make(map[int64]*service.AccountPerformanceStats, len(accountIDs))
	if h.accountPerfStats != nil {
		snapshot := h.accountPerfStats.Snapshot()
		for _, id := range accountIDs {
			if st, ok := snapshot[id]; ok {
				stats[id] = st
			}
		}
	}

	payload := gin.H{"stats": stats}
	cached := accountPerfStatsBatchCache.Set(cacheKey, payload)
	if cached.ETag != "" {
		c.Header("ETag", cached.ETag)
		c.Header("Vary", "If-None-Match")
	}
	c.Header("X-Snapshot-Cache", "miss")
	response.Success(c, payload)
}
