package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var autoRateJSONFields = []string{
	"auto_rate_enabled",
	"auto_rate_mode",
	"auto_rate_value",
}

func autoRateServiceGroup() *service.Group {
	return &service.Group{
		ID:               12,
		Name:             "openai-auto",
		Platform:         service.PlatformOpenAI,
		SubscriptionType: service.SubscriptionTypeStandard,
		RateMultiplier:   0.24,
		Status:           service.StatusActive,
		AutoRateEnabled:  true,
		AutoRateMode:     service.GroupAutoRateModePercentage,
		AutoRateValue:    0.2,
	}
}

func TestGroupFromServiceOmitsAutoRateConfig(t *testing.T) {
	for name, got := range map[string]any{
		"GroupFromService":        GroupFromService(autoRateServiceGroup()),
		"GroupFromServiceShallow": GroupFromServiceShallow(autoRateServiceGroup()),
	} {
		fields := marshalToMap(t, got)
		for _, f := range autoRateJSONFields {
			if _, ok := fields[f]; ok {
				t.Errorf("%s: public group DTO must not include %q", name, f)
			}
		}
		if _, ok := fields["rate_multiplier"]; !ok {
			t.Errorf("%s: should still include rate_multiplier", name)
		}
	}
}

func TestGroupFromServiceAdminIncludesAutoRateConfig(t *testing.T) {
	admin := GroupFromServiceAdmin(autoRateServiceGroup())
	if !admin.AutoRateEnabled || admin.AutoRateMode != service.GroupAutoRateModePercentage || admin.AutoRateValue != 0.2 {
		t.Fatalf("admin DTO missing auto rate config: %+v", admin)
	}
	fields := marshalToMap(t, admin)
	for _, f := range autoRateJSONFields {
		if _, ok := fields[f]; !ok {
			t.Errorf("admin DTO must include %q", f)
		}
	}
}
