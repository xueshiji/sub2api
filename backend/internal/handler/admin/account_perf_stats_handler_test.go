package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type perfStatsHandlerRepoStub struct {
	rows []service.AccountPerfWindowRow
}

func (r *perfStatsHandlerRepoStub) GetAccountPerformanceWindowStats(context.Context, time.Time) ([]service.AccountPerfWindowRow, error) {
	return r.rows, nil
}

func newPerfStatsHandlerTestService(t *testing.T, rows []service.AccountPerfWindowRow) *service.AccountPerformanceStatsService {
	t.Helper()
	svc := service.NewAccountPerformanceStatsService(&perfStatsHandlerRepoStub{rows: rows})
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}

// 走 router.ServeHTTP 而非直接调用 handler 方法：304 无响应体，需要 engine 的
// WriteHeaderNow 收尾才会写入 recorder。
func servePerfStatsBatch(handler *AccountHandler, body, ifNoneMatch string) *httptest.ResponseRecorder {
	router := gin.New()
	router.POST("/admin/accounts/perf-stats/batch", handler.GetBatchAccountPerfStats)
	request := httptest.NewRequest(http.MethodPost, "/admin/accounts/perf-stats/batch", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodePerfStatsPayload(t *testing.T, recorder *httptest.ResponseRecorder) map[string]service.AccountPerformanceStats {
	t.Helper()
	var payload struct {
		Data struct {
			Stats map[string]service.AccountPerformanceStats `json:"stats"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload.Data.Stats
}

func TestGetBatchAccountPerfStatsValidatesRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &AccountHandler{}

	t.Run("缺少 account_ids 时返回 400", func(t *testing.T) {
		recorder := servePerfStatsBatch(handler, `{}`, "")
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	})

	t.Run("account_ids 为空数组时返回空 stats", func(t *testing.T) {
		recorder := servePerfStatsBatch(handler, `{"account_ids":[]}`, "")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Empty(t, decodePerfStatsPayload(t, recorder))
	})

	t.Run("id 全部非法时返回空 stats", func(t *testing.T) {
		recorder := servePerfStatsBatch(handler, `{"account_ids":[0,-1]}`, "")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Empty(t, decodePerfStatsPayload(t, recorder))
	})
}

func TestGetBatchAccountPerfStatsServesSnapshotWithETagAnd304(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ttft := 800.0
	svc := newPerfStatsHandlerTestService(t, []service.AccountPerfWindowRow{
		{AccountID: 82001, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: &ttft, TtftCount: 5, SumOutputTokens: 900, SumDecodeMs: 20000},
	})
	handler := &AccountHandler{accountPerfStats: svc}
	body := `{"account_ids":[82001,82002]}`

	// 后台首次刷新完成后数据才可见
	require.Eventually(t, func() bool {
		return svc.Snapshot()[82001] != nil
	}, 2*time.Second, 10*time.Millisecond)

	recorderMiss := servePerfStatsBatch(handler, body, "")
	require.Equal(t, http.StatusOK, recorderMiss.Code)
	require.Equal(t, "miss", recorderMiss.Header().Get("X-Snapshot-Cache"))
	etag := recorderMiss.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, "If-None-Match", recorderMiss.Header().Get("Vary"))

	stats := decodePerfStatsPayload(t, recorderMiss)
	require.Contains(t, stats, "82001")
	require.NotContains(t, stats, "82002", "accounts without window data should be absent")

	recorderHit := servePerfStatsBatch(handler, body, "")
	require.Equal(t, http.StatusOK, recorderHit.Code)
	require.Equal(t, "hit", recorderHit.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, etag, recorderHit.Header().Get("ETag"))

	recorderNotModified := servePerfStatsBatch(handler, body, etag)
	require.Equal(t, http.StatusNotModified, recorderNotModified.Code)
	require.Empty(t, recorderNotModified.Body.Bytes())
}

func TestGetBatchAccountPerfStatsWithoutServiceReturnsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &AccountHandler{}
	body := `{"account_ids":[83999]}`

	recorder := servePerfStatsBatch(handler, body, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, decodePerfStatsPayload(t, recorder))
}
