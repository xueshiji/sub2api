package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

func init() {
	// 测试固定全局时区为 UTC，确保判定可复现。
	_ = timezone.Init("UTC")
}

func newPeakGroup(enabled bool, start, end string, mult float64) *Group {
	return &Group{
		SubscriptionType:   "subscription",
		PeakRateEnabled:    enabled,
		PeakStart:          start,
		PeakEnd:            end,
		PeakRateMultiplier: mult,
		OffPeakMultiplier:  1.0,
	}
}

func at(hour, min int) time.Time {
	return time.Date(2026, 6, 29, hour, min, 0, 0, time.UTC)
}

// atDay 的 2026-06-27 为周六、28 为周日、29 为周一。
func atDay(day, hour, min int) time.Time {
	return time.Date(2026, 6, day, hour, min, 0, 0, time.UTC)
}

func TestPeakMultiplierAt_DisabledOrUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		g    *Group
	}{
		{"disabled", newPeakGroup(false, "14:00", "18:00", 3.0)},
		{"empty start", newPeakGroup(true, "", "18:00", 3.0)},
		{"empty end", newPeakGroup(true, "14:00", "", 3.0)},
		{"invalid start>=end", newPeakGroup(true, "18:00", "14:00", 3.0)},
		{"equal start==end", newPeakGroup(true, "14:00", "14:00", 3.0)},
		{"malformed start", newPeakGroup(true, "99:99", "18:00", 3.0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.g.PeakMultiplierAt("claude-opus-4", at(15, 0)); got != 1.0 {
				t.Fatalf("expect 1.0, got %v", got)
			}
		})
	}
}

func TestPeakMultiplierAt_NilReceiver(t *testing.T) {
	var g *Group
	if got := g.PeakMultiplierAt("claude-opus-4", at(15, 0)); got != 1.0 {
		t.Fatalf("expect 1.0, got %v", got)
	}
}

func TestPeakMultiplierAt_Boundaries(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	cases := []struct {
		t    time.Time
		want float64
	}{
		{at(13, 59), 1.0},
		{at(14, 0), 3.0},
		{at(15, 30), 3.0},
		{at(17, 59), 3.0},
		{at(18, 0), 1.0},
		{at(23, 0), 1.0},
	}
	for _, c := range cases {
		t.Run(c.t.Format("15:04"), func(t *testing.T) {
			if got := g.PeakMultiplierAt("", c.t); got != c.want {
				t.Fatalf("at %s: expect %v, got %v", c.t.Format("15:04"), got, c.want)
			}
		})
	}
}

func TestPeakMultiplierAt_WeekendAppliesOffPeakMultiplier(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	g.OffPeakMultiplier = 0.5
	cases := []struct {
		name string
		at   time.Time
		want float64
	}{
		{"saturday in window applies off-peak", atDay(27, 15, 30), 0.5},
		{"sunday in window applies off-peak", atDay(28, 15, 30), 0.5},
		{"monday in window applies peak", atDay(29, 15, 30), 3.0},
		{"saturday off window applies off-peak", atDay(27, 20, 0), 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := g.PeakMultiplierAt("", c.at); got != c.want {
				t.Fatalf("at %s: expect %v, got %v", c.at.Format("2006-01-02 15:04"), got, c.want)
			}
		})
	}
}

func TestPeakMultiplierAt_WeekendDefaultOffPeakIsOne(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt("", atDay(27, 15, 30)); got != 1.0 {
		t.Fatalf("weekend with default off-peak: expect 1.0, got %v", got)
	}
}

func TestPeakMultiplierAt_OffPeakOutsideWindow(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	g.OffPeakMultiplier = 0.5
	if got := g.PeakMultiplierAt("", at(20, 0)); got != 0.5 {
		t.Fatalf("off-window on weekday: expect 0.5, got %v", got)
	}
	if got := g.PeakMultiplierAt("claude-opus-4", at(20, 0)); got != 0.5 {
		t.Fatalf("off-window with model: expect 0.5, got %v", got)
	}
}

func TestPeakMultiplierAt_NegativeOffPeakDegradesToOne(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	g.OffPeakMultiplier = -1
	if got := g.PeakMultiplierAt("", at(20, 0)); got != 1.0 {
		t.Fatalf("negative off-peak must degrade to 1.0, got %v", got)
	}
	if got := g.PeakMultiplierAt("", atDay(27, 15, 0)); got != 1.0 {
		t.Fatalf("negative off-peak on weekend must degrade to 1.0, got %v", got)
	}
}

func TestPeakMultiplierAt_ModelMultipliers(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 2.0)
	g.OffPeakMultiplier = 0.6
	g.PeakModelMultipliers = map[string]PeakModelMultiplierRule{
		"claude-opus-4-20250514": {Peak: 3.5, OffPeak: 0.4},
		"claude-sonnet-*":        {Peak: 1.5, OffPeak: 0.7},
		"claude-*":               {Peak: 1.2, OffPeak: 0.9},
	}
	cases := []struct {
		name  string
		model string
		want  float64
	}{
		{"exact match wins", "claude-opus-4-20250514", 3.5},
		{"longest prefix wins over shorter", "claude-sonnet-4-20250514", 1.5},
		{"shorter prefix fallback", "claude-haiku-4", 1.2},
		{"unmatched model falls back to default", "gpt-4o", 2.0},
		{"empty model uses default", "", 2.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := g.PeakMultiplierAt(c.model, at(15, 30)); got != c.want {
				t.Fatalf("model %q in peak window: expect %v, got %v", c.model, c.want, got)
			}
		})
	}
}

func TestPeakMultiplierAt_ModelRuleOffPeakOutsideWindowAndWeekend(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 2.0)
	g.OffPeakMultiplier = 0.8
	g.PeakModelMultipliers = map[string]PeakModelMultiplierRule{"claude-opus-4": {Peak: 3.5, OffPeak: 0.3}}
	if got := g.PeakMultiplierAt("claude-opus-4", at(20, 0)); got != 0.3 {
		t.Fatalf("off-window on weekday uses rule off-peak: got %v, want 0.3", got)
	}
	if got := g.PeakMultiplierAt("claude-opus-4", atDay(27, 15, 0)); got != 0.3 {
		t.Fatalf("weekend uses rule off-peak: got %v, want 0.3", got)
	}
	if got := g.PeakMultiplierAt("gpt-4o", at(20, 0)); got != 0.8 {
		t.Fatalf("off-window unmatched model falls back to group default: got %v, want 0.8", got)
	}
	if got := g.PeakMultiplierAt("", at(20, 0)); got != 0.8 {
		t.Fatalf("off-window empty model uses group default: got %v, want 0.8", got)
	}
}

func TestPeakMultiplierAt_InvalidModelMultiplierFallsBack(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 2.0)
	g.OffPeakMultiplier = 0.8
	g.PeakModelMultipliers = map[string]PeakModelMultiplierRule{"claude-opus-4": {Peak: 3.5, OffPeak: -1}}
	if got := g.PeakMultiplierAt("claude-opus-4", at(15, 30)); got != 2.0 {
		t.Fatalf("rule with invalid field must fall back to default peak: got %v, want 2.0", got)
	}
	if got := g.PeakMultiplierAt("claude-opus-4", at(20, 0)); got != 0.8 {
		t.Fatalf("rule with invalid field must fall back to default off-peak: got %v, want 0.8", got)
	}
}

func TestPeakMultiplierAt_RespectsTimezoneLocation(t *testing.T) {
	// 全局时区为 UTC。北京 15:00 = UTC 07:00，不在 [14:00,18:00)。
	nonUTC := time.Date(2026, 6, 29, 15, 0, 0, 0, mustLoad("Asia/Shanghai"))
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	if got := g.PeakMultiplierAt("", nonUTC); got != 1.0 {
		t.Fatalf("expect 1.0 (converted to UTC 07:00), got %v", got)
	}
}

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func TestValidatePeakRateConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     PeakRateConfig
		wantErr bool
	}{
		{"disabled passes through", PeakRateConfig{SubscriptionType: "subscription"}, false},
		{"subscription enabled valid", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, false},
		{"subscription_token enabled valid", PeakRateConfig{SubscriptionType: "subscription_token", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, false},
		{"standard enabled rejected", PeakRateConfig{SubscriptionType: "standard", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, true},
		{"empty type treated as standard", PeakRateConfig{SubscriptionType: "", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, true},
		{"standard disabled passes", PeakRateConfig{SubscriptionType: "standard"}, false},
		{"enabled empty start", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "", End: "18:00", Multiplier: 1.0}, true},
		{"enabled empty end", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "", Multiplier: 1.0}, true},
		{"enabled malformed start", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "99:99", End: "18:00", Multiplier: 1.0}, true},
		{"enabled malformed end", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "25:00", Multiplier: 1.0}, true},
		{"enabled equal start==end", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "14:00", Multiplier: 1.0}, true},
		{"enabled cross-day rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "22:00", End: "02:00", Multiplier: 1.0}, true},
		{"enabled negative multiplier", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: -0.5}, true},
		{"enabled zero multiplier allowed", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 0}, false},
		{"negative off-peak rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, OffPeakMultiplier: -0.1}, true},
		{"zero off-peak allowed", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, OffPeakMultiplier: 0}, false},
		{"trailing star pattern allowed", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: 2.0, OffPeak: 1.0}}}, false},
		{"exact model allowed", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"claude-opus-4": {Peak: 2.0, OffPeak: 1.0}}}, false},
		{"bare star rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"*": {Peak: 2.0, OffPeak: 1.0}}}, true},
		{"mid-string star rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"claude-*-4": {Peak: 2.0, OffPeak: 1.0}}}, true},
		{"empty model key rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"": {Peak: 2.0, OffPeak: 1.0}}}, true},
		{"negative model multiplier rejected", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 1.0, ModelMultipliers: map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: -1, OffPeak: 1.0}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePeakRateConfig(c.cfg)
			if c.wantErr && err == nil {
				t.Fatalf("expect error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expect no error, got %v", err)
			}
		})
	}
}

func TestPeakMultiplierAt_StandardTypeDegradesToOne(t *testing.T) {
	g := newPeakGroup(true, "14:00", "18:00", 3.0)
	g.SubscriptionType = "standard"
	if got := g.PeakMultiplierAt("", at(15, 30)); got != 1.0 {
		t.Fatalf("standard group must degrade to 1.0, got %v", got)
	}

	sub := newPeakGroup(true, "14:00", "18:00", 3.0)
	sub.SubscriptionType = "subscription"
	if got := sub.PeakMultiplierAt("", at(15, 30)); got != 3.0 {
		t.Fatalf("subscription group peak multiplier: got %v, want 3.0", got)
	}
}

// TestPeakMultiplier_GatewayBillingSequence 调用 gateway_service.recordUsageCore 与
// openai_gateway_service.RecordUsage 共用的 computePeakAwareMultipliers，验证计费叠加顺序：
// 图片按次倍率基于基础倍率算出且不受高峰影响，高峰因子只乘入 token 倍率。
// 若有人调换叠加顺序或把高峰并入 imageMultiplier，此测试会失败。
func TestPeakMultiplier_GatewayBillingSequence(t *testing.T) {
	const baseMultiplier = 0.8
	apiKey := &APIKey{Group: newPeakGroup(true, "14:00", "18:00", 3.0)}
	approxEq := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	t.Run("peak hour amplifies token multiplier only", func(t *testing.T) {
		now := at(15, 30) // 处于 [14:00, 18:00)
		tokenMultiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, "claude-sonnet-4", baseMultiplier, now)
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("image multiplier must not be affected by peak: got %v, want %v", imageMultiplier, baseMultiplier)
		}
		if want := baseMultiplier * 3.0; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier should include peak factor: got %v, want %v", tokenMultiplier, want)
		}
	})

	t.Run("off-peak leaves both multipliers at base", func(t *testing.T) {
		now := at(20, 0)
		tokenMultiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, "claude-sonnet-4", baseMultiplier, now)
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("image multiplier: got %v, want %v", imageMultiplier, baseMultiplier)
		}
		if !approxEq(tokenMultiplier, baseMultiplier) {
			t.Fatalf("token multiplier should equal base off-peak: got %v, want %v", tokenMultiplier, baseMultiplier)
		}
	})

	t.Run("per-model peak multiplier applies to matching model", func(t *testing.T) {
		group := newPeakGroup(true, "14:00", "18:00", 3.0)
		group.PeakModelMultipliers = map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: 5.0, OffPeak: 1.0}}
		key := &APIKey{Group: group}
		now := at(15, 30)
		tokenMultiplier, _ := computePeakAwareMultipliers(key, "claude-opus-4-20250514", baseMultiplier, now)
		if want := baseMultiplier * 5.0; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier with per-model rule: got %v, want %v", tokenMultiplier, want)
		}
		tokenMultiplier, _ = computePeakAwareMultipliers(key, "claude-sonnet-4", baseMultiplier, now)
		if want := baseMultiplier * 3.0; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier for unmatched model: got %v, want %v", tokenMultiplier, want)
		}
	})

	t.Run("configured off-peak multiplier scales token multiplier", func(t *testing.T) {
		group := newPeakGroup(true, "14:00", "18:00", 3.0)
		group.OffPeakMultiplier = 0.5
		key := &APIKey{Group: group}
		now := at(20, 0)
		tokenMultiplier, imageMultiplier := computePeakAwareMultipliers(key, "claude-sonnet-4", baseMultiplier, now)
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("image multiplier: got %v, want %v", imageMultiplier, baseMultiplier)
		}
		if want := baseMultiplier * 0.5; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier with off-peak factor: got %v, want %v", tokenMultiplier, want)
		}
	})

	t.Run("image independent mode decoupled from peak", func(t *testing.T) {
		indGroup := newPeakGroup(true, "14:00", "18:00", 3.0)
		indGroup.ImageRateIndependent = true
		indGroup.ImageRateMultiplier = 0.5
		indKey := &APIKey{Group: indGroup}
		now := at(15, 30)
		tokenMultiplier, imageMultiplier := computePeakAwareMultipliers(indKey, "claude-sonnet-4", baseMultiplier, now)
		if !approxEq(imageMultiplier, 0.5) {
			t.Fatalf("independent image multiplier: got %v, want 0.5", imageMultiplier)
		}
		if want := baseMultiplier * 3.0; !approxEq(tokenMultiplier, want) {
			t.Fatalf("token multiplier should include peak factor: got %v, want %v", tokenMultiplier, want)
		}
	})

	t.Run("nil api key degrades to base multipliers", func(t *testing.T) {
		now := at(15, 30)
		tokenMultiplier, imageMultiplier := computePeakAwareMultipliers(nil, "claude-sonnet-4", baseMultiplier, now)
		if !approxEq(tokenMultiplier, baseMultiplier) {
			t.Fatalf("nil group token multiplier: got %v, want %v", tokenMultiplier, baseMultiplier)
		}
		if !approxEq(imageMultiplier, baseMultiplier) {
			t.Fatalf("nil group image multiplier: got %v, want %v", imageMultiplier, baseMultiplier)
		}
	})
}

// TestPeakMultiplier_SnapshotRoundTrip 防回归：认证缓存快照（APIKeyAuthGroupSnapshot）
// 必须携带高峰倍率全部字段，否则扣费路径拿到的 apiKey.Group 会缺字段、PeakMultiplierAt 恒降级为 1.0。
// 调用真实链路 snapshotFromAPIKey → snapshotToAPIKey，验证 peak 配置经快照往返后仍生效。
func TestPeakMultiplier_SnapshotRoundTrip(t *testing.T) {
	group := newPeakGroup(true, "14:00", "18:00", 3.0)
	group.OffPeakMultiplier = 0.5
	group.PeakModelMultipliers = map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: 5.0, OffPeak: 0.5}}
	apiKey := &APIKey{
		User:  &User{ID: 1, Status: StatusActive, Role: RoleUser},
		Group: group,
	}
	svc := &APIKeyService{}

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	if snapshot == nil || snapshot.Group == nil {
		t.Fatalf("snapshot or snapshot.Group must not be nil")
	}
	restored := svc.snapshotToAPIKey("k", snapshot)
	if restored.Group == nil {
		t.Fatalf("restored.Group must not be nil")
	}

	if !restored.Group.PeakRateEnabled ||
		restored.Group.PeakStart != "14:00" ||
		restored.Group.PeakEnd != "18:00" ||
		restored.Group.PeakRateMultiplier != 3.0 ||
		restored.Group.OffPeakMultiplier != 0.5 ||
		restored.Group.PeakModelMultipliers["claude-opus-*"].Peak != 5.0 || restored.Group.PeakModelMultipliers["claude-opus-*"].OffPeak != 0.5 {
		t.Fatalf("peak fields lost in snapshot round-trip: %+v", restored.Group)
	}
	if got := restored.Group.PeakMultiplierAt("", at(15, 30)); got != 3.0 {
		t.Fatalf("peak hour multiplier after round-trip: got %v, want 3.0", got)
	}
	if got := restored.Group.PeakMultiplierAt("claude-opus-4", at(15, 30)); got != 5.0 {
		t.Fatalf("per-model peak multiplier after round-trip: got %v, want 5.0", got)
	}
	if got := restored.Group.PeakMultiplierAt("", at(20, 0)); got != 0.5 {
		t.Fatalf("off-peak multiplier after round-trip: got %v, want 0.5", got)
	}
}

func TestNormalizePeakRateConfig(t *testing.T) {
	cases := []struct {
		name        string
		in          PeakRateConfig
		wantEnabled bool
		wantStart   string
		wantEnd     string
		wantMult    float64
		wantOffPeak float64
	}{
		{"subscription enabled keeps config", PeakRateConfig{SubscriptionType: "subscription", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, true, "14:00", "18:00", 3.0, 0},
		{"subscription_token enabled keeps config", PeakRateConfig{SubscriptionType: "subscription_token", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, true, "14:00", "18:00", 3.0, 0},
		{"standard clears config", PeakRateConfig{SubscriptionType: "standard", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, false, "", "", 1.0, 1.0},
		{"empty type clears config", PeakRateConfig{SubscriptionType: "", Enabled: true, Start: "14:00", End: "18:00", Multiplier: 3.0}, false, "", "", 1.0, 1.0},
		{"subscription disabled keeps valid window", PeakRateConfig{SubscriptionType: "subscription", Start: "14:00", End: "18:00", Multiplier: 1.0}, false, "14:00", "18:00", 1.0, 0},
		{"subscription_token disabled cleans dirty window", PeakRateConfig{SubscriptionType: "subscription_token", Start: "99:99", End: "18:00", Multiplier: -1.0}, false, "", "18:00", 1.0, 0},
		{"disabled cleans negative off-peak", PeakRateConfig{SubscriptionType: "subscription", Start: "14:00", End: "18:00", OffPeakMultiplier: -0.5}, false, "14:00", "18:00", 0, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizePeakRateConfig(c.in)
			if got.Enabled != c.wantEnabled || got.Start != c.wantStart || got.End != c.wantEnd || got.Multiplier != c.wantMult || got.OffPeakMultiplier != c.wantOffPeak {
				t.Fatalf("NormalizePeakRateConfig(%+v): got (%v,%q,%q,%v,%v), want (%v,%q,%q,%v,%v)",
					c.in, got.Enabled, got.Start, got.End, got.Multiplier, got.OffPeakMultiplier,
					c.wantEnabled, c.wantStart, c.wantEnd, c.wantMult, c.wantOffPeak)
			}
		})
	}
}

func TestNormalizePeakRateConfig_ModelMultipliers(t *testing.T) {
	t.Run("non-subscription clears model multipliers", func(t *testing.T) {
		got := NormalizePeakRateConfig(PeakRateConfig{
			SubscriptionType: "standard",
			Enabled:          true,
			Start:            "14:00",
			End:              "18:00",
			ModelMultipliers: map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: 2.0, OffPeak: 1.0}},
		})
		if got.ModelMultipliers != nil {
			t.Fatalf("standard group model multipliers must be cleared, got %v", got.ModelMultipliers)
		}
	})
	t.Run("invalid entries dropped and valid kept", func(t *testing.T) {
		got := NormalizePeakRateConfig(PeakRateConfig{
			SubscriptionType: "subscription",
			Enabled:          true,
			Start:            "14:00",
			End:              "18:00",
			ModelMultipliers: map[string]PeakModelMultiplierRule{
				"  claude-opus-* ": {Peak: 2.0, OffPeak: 1.0},
				"*":                {Peak: 9.0, OffPeak: 9.0},
				"mid*star":         {Peak: 1.0, OffPeak: 1.0},
				"":                 {Peak: 1.0, OffPeak: 1.0},
				"claude-sonnet-*":  {Peak: -1, OffPeak: 1.0},
				"claude-haiku-*":   {Peak: 0, OffPeak: 0},
			},
		})
		want := map[string]PeakModelMultiplierRule{"claude-opus-*": {Peak: 2.0, OffPeak: 1.0}, "claude-haiku-*": {Peak: 0, OffPeak: 0}}
		if len(got.ModelMultipliers) != len(want) {
			t.Fatalf("expect %v, got %v", want, got.ModelMultipliers)
		}
		for k, v := range want {
			if got.ModelMultipliers[k] != v {
				t.Fatalf("key %q: expect %v, got %v", k, v, got.ModelMultipliers[k])
			}
		}
	})
	t.Run("all invalid entries yield nil map", func(t *testing.T) {
		got := NormalizePeakRateConfig(PeakRateConfig{
			SubscriptionType: "subscription",
			Enabled:          true,
			Start:            "14:00",
			End:              "18:00",
			ModelMultipliers: map[string]PeakModelMultiplierRule{"*": {Peak: 9.0, OffPeak: 9.0}},
		})
		if got.ModelMultipliers != nil {
			t.Fatalf("expect nil, got %v", got.ModelMultipliers)
		}
	})
}
