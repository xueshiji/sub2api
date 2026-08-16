package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// 积分报告由前端生成为自包含 HTML（内联脚本 + CDN Chart.js）。srcdoc/blob 等
// 前端内嵌打开方式会继承管理页的 nonce CSP，报告内联脚本会被整体拦截，因此
// 由后端短时托管：管理端 POST 报告内容换取一次性随机 id，浏览器新标签页直接
// GET 打开（顶级导航无法携带 Authorization 头，id 即访问凭证）。
const (
	peakReportViewTTL     = 5 * time.Minute
	peakReportViewMaxSize = 16 << 20
	// JSON 包裹与转义余量
	peakReportViewBodyLimit = peakReportViewMaxSize + (1 << 20)
)

var peakReportViewIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type peakReportViewStore struct {
	mu    sync.Mutex
	views map[string]peakReportView
}

type peakReportView struct {
	html      []byte
	expiresAt time.Time
}

func (s *peakReportViewStore) put(html []byte, now time.Time) (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buffer)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.views == nil {
		s.views = make(map[string]peakReportView)
	}
	s.sweepLocked(now)
	s.views[id] = peakReportView{html: html, expiresAt: now.Add(peakReportViewTTL)}
	return id, nil
}

// take 返回内容并删除条目，报告视图只能被打开一次。
func (s *peakReportViewStore) take(id string, now time.Time) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	view, ok := s.views[id]
	if !ok {
		return nil, false
	}
	delete(s.views, id)
	return view.html, true
}

func (s *peakReportViewStore) sweepLocked(now time.Time) {
	for id, view := range s.views {
		if now.After(view.expiresAt) {
			delete(s.views, id)
		}
	}
}

// CreatePeakReportView 托管前端生成的积分报告 HTML，返回一次性访问 id。
// POST /api/v1/admin/dashboard/peak-report-views
func (h *DashboardHandler) CreatePeakReportView(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, peakReportViewBodyLimit)
	var req struct {
		HTML string `json:"html"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.HTML) == "" {
		response.BadRequest(c, "报告内容无效")
		return
	}
	if len(req.HTML) > peakReportViewMaxSize {
		response.BadRequest(c, "报告内容过大")
		return
	}
	id, err := h.peakReportViews.put([]byte(req.HTML), time.Now())
	if err != nil {
		response.Error(c, 500, "Failed to create report view")
		return
	}
	response.Success(c, gin.H{"view_id": id})
}

// GetPeakReportView 按一次性 id 返回托管报告。
// GET /api/v1/admin/dashboard/peak-report-views/:id
func (h *DashboardHandler) GetPeakReportView(c *gin.Context) {
	id := c.Param("id")
	if !peakReportViewIDPattern.MatchString(id) {
		response.Error(c, 404, "Report view not found")
		return
	}
	html, ok := h.peakReportViews.take(id, time.Now())
	if !ok {
		response.Error(c, 404, "Report view not found")
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	// 覆盖全局 nonce CSP：自包含报告依赖内联脚本与 jsdelivr 的 Chart.js；
	// connect-src 'none' 阻断页面脚本外发数据，frame-ancestors 'none' 禁止被嵌入
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'unsafe-inline'; img-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	c.Data(http.StatusOK, "text/html; charset=utf-8", html)
}
