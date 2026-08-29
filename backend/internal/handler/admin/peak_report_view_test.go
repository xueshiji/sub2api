package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newPeakReportViewRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewDashboardHandler(nil, nil)
	r.POST("/api/v1/admin/dashboard/peak-report-views", h.CreatePeakReportView)
	r.GET("/api/v1/admin/dashboard/peak-report-views/:id", h.GetPeakReportView)
	return r
}

func createPeakReportView(t *testing.T, r *gin.Engine, html string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"html": html})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dashboard/peak-report-views", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	viewID := ""
	if w.Code == http.StatusOK {
		var resp struct {
			Data struct {
				ViewID string `json:"view_id"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		viewID = resp.Data.ViewID
	}
	return w.Code, viewID
}

func TestPeakReportViewReturnsHTMLOnceWithReportCSP(t *testing.T) {
	r := newPeakReportViewRouter()
	code, viewID := createPeakReportView(t, r, "<!DOCTYPE html><html><body>report</body></html>")
	require.Equal(t, http.StatusOK, code)
	require.NotEmpty(t, viewID)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard/peak-report-views/"+viewID, nil))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	csp := w.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "script-src 'unsafe-inline' https://cdn.jsdelivr.net")
	require.Contains(t, csp, "connect-src 'none'")
	require.Contains(t, w.Body.String(), "report")

	// 同一 id 只能打开一次
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard/peak-report-views/"+viewID, nil))
	require.Equal(t, http.StatusNotFound, w2.Code)
}

func TestPeakReportViewRejectsInvalidInput(t *testing.T) {
	r := newPeakReportViewRouter()

	code, _ := createPeakReportView(t, r, "  ")
	require.Equal(t, http.StatusBadRequest, code)

	code, _ = createPeakReportView(t, r, strings.Repeat("a", peakReportViewMaxSize+1))
	require.Equal(t, http.StatusBadRequest, code)

	for _, id := range []string{"", "abc", "ZZ" + strings.Repeat("0", 62), "../../etc/passwd"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard/peak-report-views/"+id, nil))
		require.Equal(t, http.StatusNotFound, w.Code, "id=%q", id)
	}
}

func TestPeakReportViewExpiresAfterTTL(t *testing.T) {
	h := NewDashboardHandler(nil, nil)
	now := time.Now()
	id, err := h.peakReportViews.put([]byte("<html>x</html>"), now)
	require.NoError(t, err)

	_, ok := h.peakReportViews.take(id, now.Add(peakReportViewTTL+time.Second))
	require.False(t, ok)
}

// 生产链路上全局 SecurityHeaders 中间件会先设置 nonce CSP（其会拦截报告的内联脚本），
// handler 的报告 CSP 必须最终生效。
func TestPeakReportViewOverridesGlobalNonceCSP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewDashboardHandler(nil, nil)
	r := gin.New()
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Policy: config.DefaultCSPPolicy, Enabled: true}, nil))
	r.GET("/api/v1/admin/dashboard/peak-report-views/:id", h.GetPeakReportView)

	id, err := h.peakReportViews.put([]byte("<html>report</html>"), time.Now())
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard/peak-report-views/"+id, nil))

	require.Equal(t, http.StatusOK, w.Code)
	csp := w.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "script-src 'unsafe-inline' https://cdn.jsdelivr.net")
	require.NotContains(t, csp, "nonce-")
}
