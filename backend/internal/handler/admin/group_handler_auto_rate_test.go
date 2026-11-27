//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type autoRateGroupAdminServiceStub struct {
	service.AdminService
	preview                 *service.GroupAutoRatePreview
	previewGroupID          int64
	previewMode             *string
	previewValue            *float64
	previewSourceID         *int64
	previewRequireOAuthOnly *bool
	recalculatedGroup       *service.Group
}

func (s *autoRateGroupAdminServiceStub) GetGroupAutoRatePreview(_ context.Context, id int64, mode *string, value *float64, sourceGroupID *int64, requireOAuthOnly *bool) (*service.GroupAutoRatePreview, error) {
	s.previewGroupID = id
	s.previewMode = mode
	s.previewValue = value
	s.previewSourceID = sourceGroupID
	s.previewRequireOAuthOnly = requireOAuthOnly
	return s.preview, nil
}

func (s *autoRateGroupAdminServiceStub) RecalculateGroupAutoRate(_ context.Context, _ int64) (*service.Group, *service.GroupAutoRatePreview, error) {
	return s.recalculatedGroup, s.preview, nil
}

func setupAutoRateGroupRouter(svc service.AdminService, runMode string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	cfg := &config.Config{RunMode: runMode}
	h := NewGroupHandlerWithConfig(svc, nil, nil, cfg)
	router.GET("/api/v1/admin/groups/:id/auto-rate-preview", h.GetAutoRatePreview)
	router.POST("/api/v1/admin/groups/:id/recalculate-auto-rate", h.RecalculateAutoRate)
	return router
}

func TestGroupHandler_GetAutoRatePreview(t *testing.T) {
	maxRate := 0.15
	computed := 0.18
	stub := &autoRateGroupAdminServiceStub{
		preview: &service.GroupAutoRatePreview{
			Supported:            true,
			EligibleAccountCount: 2,
			MaxAccountName:       "acc-high",
			MaxAccountRate:       &maxRate,
			Mode:                 service.GroupAutoRateModePercentage,
			Value:                0.2,
			ComputedRate:         &computed,
			Status:               service.GroupAutoRateStatusReady,
		},
	}
	router := setupAutoRateGroupRouter(stub, config.RunModeStandard)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/0/auto-rate-preview?mode=percentage&value=0.2&source_group_id=9&require_oauth_only=true", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(0), stub.previewGroupID)
	require.NotNil(t, stub.previewMode)
	require.Equal(t, "percentage", *stub.previewMode)
	require.NotNil(t, stub.previewValue)
	require.InDelta(t, 0.2, *stub.previewValue, 1e-9)
	require.NotNil(t, stub.previewSourceID)
	require.Equal(t, int64(9), *stub.previewSourceID)
	require.NotNil(t, stub.previewRequireOAuthOnly)
	require.True(t, *stub.previewRequireOAuthOnly)

	var resp struct {
		Data service.GroupAutoRatePreview `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Data.Supported)
	require.NotNil(t, resp.Data.ComputedRate)
	require.InDelta(t, 0.18, *resp.Data.ComputedRate, 1e-9)

	badReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/0/auto-rate-preview?require_oauth_only=invalid", nil)
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	require.Equal(t, http.StatusBadRequest, badRec.Code)
}

func TestGroupHandler_RecalculateAutoRate(t *testing.T) {
	maxRate := 0.20
	computed := 0.30
	stub := &autoRateGroupAdminServiceStub{
		recalculatedGroup: &service.Group{
			ID:               5,
			Name:             "openai-auto",
			Platform:         service.PlatformOpenAI,
			SubscriptionType: service.SubscriptionTypeStandard,
			RateMultiplier:   0.30,
			AutoRateEnabled:  true,
			AutoRateMode:     service.GroupAutoRateModeFixed,
			AutoRateValue:    0.10,
		},
		preview: &service.GroupAutoRatePreview{
			Supported:            true,
			EligibleAccountCount: 1,
			MaxAccountRate:       &maxRate,
			Mode:                 service.GroupAutoRateModeFixed,
			Value:                0.10,
			ComputedRate:         &computed,
			Status:               service.GroupAutoRateStatusReady,
		},
	}
	router := setupAutoRateGroupRouter(stub, config.RunModeStandard)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/5/recalculate-auto-rate", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data struct {
			Group struct {
				RateMultiplier  float64 `json:"rate_multiplier"`
				AutoRateEnabled bool    `json:"auto_rate_enabled"`
			} `json:"group"`
			Preview service.GroupAutoRatePreview `json:"preview"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Data.Group.AutoRateEnabled)
	require.InDelta(t, 0.30, resp.Data.Group.RateMultiplier, 1e-9)
	require.Equal(t, service.GroupAutoRateStatusReady, resp.Data.Preview.Status)
}

func TestGroupHandler_AutoRateRejectedInSimpleMode(t *testing.T) {
	router := setupAutoRateGroupRouter(&autoRateGroupAdminServiceStub{}, config.RunModeSimple)

	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/groups/1/auto-rate-preview", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusForbidden, rec1.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/1/recalculate-auto-rate", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusForbidden, rec2.Code)
}
