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
	rows    []service.AccountPerfWindowRow
	poolP95 map[string]float64
}

func (r *perfStatsHandlerRepoStub) GetAccountPerformanceWindowStats(context.Context, time.Time) (*service.AccountPerfWindowStats, error) {
	return &service.AccountPerfWindowStats{Rows: r.rows, PoolTTFTP95Ms: r.poolP95}, nil
}

func newPerfStatsHandlerTestService(t *testing.T, rows []service.AccountPerfWindowRow) *service.AccountPerformanceStatsService {
	t.Helper()
	svc := service.NewAccountPerformanceStatsService(&perfStatsHandlerRepoStub{rows: rows}, service.SlowPenaltyConfig{})
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

func TestGetBatchAccountPerfStatsIncludesSlowPenalty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ttft := 800.0
	svc := service.NewAccountPerformanceStatsService(
		&perfStatsHandlerRepoStub{
			rows:    []service.AccountPerfWindowRow{{AccountID: 82101, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: &ttft, TtftCount: 5}},
			poolP95: map[string]float64{"upstream-a": 1000},
		},
		service.SlowPenaltyConfig{Enabled: true, Consecutive: 2, ThresholdFactor: 1.0, Factor: 0.5, Duration: 10 * time.Minute},
	)
	svc.Start()
	t.Cleanup(svc.Stop)

	// 首次刷新完成前 poolP95 尚未就绪，ObserveTTFT 会被跳过，反复重试到触发为止
	require.Eventually(t, func() bool {
		svc.ObserveTTFT(82101, "upstream-a", 5000)
		svc.ObserveTTFT(82101, "upstream-a", 5000)
		st := svc.Snapshot()[82101]
		return st != nil && st.SlowPenalty
	}, 2*time.Second, 10*time.Millisecond)

	handler := &AccountHandler{accountPerfStats: svc}
	recorder := servePerfStatsBatch(handler, `{"account_ids":[82101]}`, "")
	require.Equal(t, http.StatusOK, recorder.Code)

	stats := decodePerfStatsPayload(t, recorder)
	require.Contains(t, stats, "82101")
	require.True(t, stats["82101"].SlowPenalty)
	require.NotNil(t, stats["82101"].SlowPenaltyUntil)
}

type perfScoreSortAdminServiceStub struct {
	service.AdminService
	accounts []service.Account
}

func (s *perfScoreSortAdminServiceStub) ListAccountsForSchedulerScoreFilter(context.Context, string, string, string, string, int64, string) ([]service.Account, error) {
	return s.accounts, nil
}

func perfScoreSortTestRows() []service.AccountPerfWindowRow {
	return []service.AccountPerfWindowRow{
		// 账号 1 得分 1.0（TTFT 500ms 最优、TPS 50 最优）
		{AccountID: 1, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfStatsTestFloatPtr(500), TtftCount: 5, SumOutputTokens: 100, SumDecodeMs: 2000},
		// 账号 2 得分 0.75（TTFT 1000ms，TPS 持平）
		{AccountID: 2, Model: "upstream-a", SampleCount: 5, AvgTTFTMs: perfStatsTestFloatPtr(1000), TtftCount: 5, SumOutputTokens: 100, SumDecodeMs: 2000},
		// 账号 3 样本不足，无分；账号 4 无窗口数据
		{AccountID: 3, Model: "upstream-a", SampleCount: 2, AvgTTFTMs: perfStatsTestFloatPtr(800), TtftCount: 2, SumOutputTokens: 100, SumDecodeMs: 2000},
	}
}

func perfStatsTestFloatPtr(v float64) *float64 { return &v }

func TestListAccountsSortedByPerfScore(t *testing.T) {
	svc := newPerfStatsHandlerTestService(t, perfScoreSortTestRows())
	require.Eventually(t, func() bool { return svc.Snapshot()[1] != nil }, 2*time.Second, 10*time.Millisecond)

	handler := &AccountHandler{
		adminService:     &perfScoreSortAdminServiceStub{accounts: []service.Account{{ID: 4}, {ID: 2}, {ID: 3}, {ID: 1}}},
		accountPerfStats: svc,
	}

	t.Run("desc 时有分账号按分降序、无分账号排最后", func(t *testing.T) {
		got, total, err := handler.listAccountsSortedByPerfScore(context.Background(), "", "", "", "", 0, "", "desc", 1, 2)
		require.NoError(t, err)
		require.EqualValues(t, 4, total)
		require.Equal(t, []int64{1, 2}, accountIDsOf(got))
	})

	t.Run("asc 时有分账号按分升序", func(t *testing.T) {
		got, _, err := handler.listAccountsSortedByPerfScore(context.Background(), "", "", "", "", 0, "", "asc", 1, 2)
		require.NoError(t, err)
		require.Equal(t, []int64{2, 1}, accountIDsOf(got))
	})

	t.Run("无分账号按 ID 升序排在末页", func(t *testing.T) {
		got, _, err := handler.listAccountsSortedByPerfScore(context.Background(), "", "", "", "", 0, "", "desc", 2, 2)
		require.NoError(t, err)
		require.Equal(t, []int64{3, 4}, accountIDsOf(got))
	})

	t.Run("页码越界时返回空列表", func(t *testing.T) {
		got, total, err := handler.listAccountsSortedByPerfScore(context.Background(), "", "", "", "", 0, "", "desc", 9, 2)
		require.NoError(t, err)
		require.EqualValues(t, 4, total)
		require.Empty(t, got)
	})
}

func TestListAccountsSortedByPerfScoreWithoutStatsFallsBackToIDOrder(t *testing.T) {
	handler := &AccountHandler{
		adminService: &perfScoreSortAdminServiceStub{accounts: []service.Account{{ID: 4}, {ID: 2}, {ID: 1}}},
	}

	got, total, err := handler.listAccountsSortedByPerfScore(context.Background(), "", "", "", "", 0, "", "desc", 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Equal(t, []int64{1, 2, 4}, accountIDsOf(got))
}

func accountIDsOf(accounts []service.Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, acc := range accounts {
		ids = append(ids, acc.ID)
	}
	return ids
}
