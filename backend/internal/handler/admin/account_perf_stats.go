package admin

import (
	"net/http"
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
