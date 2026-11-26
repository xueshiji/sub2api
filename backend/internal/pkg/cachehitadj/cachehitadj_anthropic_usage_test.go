package cachehitadj

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// rewriteStream feeds raw events through one rewriter, as the gateway does for a single stream.
func rewriteStream(adjustment float64, events ...string) []string {
	r := &AnthropicStreamRewriter{}
	outs := make([]string, 0, len(events))
	for _, event := range events {
		outs = append(outs, string(r.Rewrite([]byte(event), adjustment)))
	}
	return outs
}

const (
	kimiFullyCachedStart   = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","usage":{"input_tokens":173306,"prompt_tokens":173306}}}`
	kimiZeroBucketsStart   = `{"type":"message_start","message":{"usage":{"input_tokens":173306,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0,"prompt_tokens":173306,"cached_tokens":0}}}`
	kimiFullyCachedDelta   = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":0,"cache_read_input_tokens":173306,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306}}`
	kimiPartialDelta       = `{"type":"message_delta","usage":{"input_tokens":250,"cache_creation_input_tokens":0,"cache_read_input_tokens":173056,"output_tokens":166,"prompt_tokens":173306,"cached_tokens":173056}}`
	glmStart               = `{"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":1,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}}`
	glmDelta               = `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`
	deepSeekStart          = `{"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":1,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}}`
	deepSeekDelta          = `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`
	kimiNoPromptTotalStart = `{"type":"message_start","message":{"usage":{"input_tokens":173306,"cache_read_input_tokens":0,"output_tokens":1}}}`
)

func requireUsage(t *testing.T, event, path string, want map[string]int64) {
	t.Helper()
	for key, value := range want {
		require.Equal(t, value, gjson.Get(event, path+"."+key).Int(), key)
	}
}

func TestAnthropicStreamRewriter_FullyCachedKimiDeltaReportsBilledInput(t *testing.T) {
	wantDelta := map[string]int64{
		"input_tokens":            8665,
		"cache_read_input_tokens": 164641,
		"cached_tokens":           164641,
		"prompt_tokens":           173306,
		"output_tokens":           8,
	}
	for _, tt := range []struct {
		name  string
		start string
	}{
		{name: "start mirrors the prompt total", start: kimiFullyCachedStart},
		{name: "start mirrors the prompt total with explicit zero buckets", start: kimiZeroBucketsStart},
		{name: "start carries no prompt_tokens", start: kimiNoPromptTotalStart},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outs := rewriteStream(0.05, tt.start, kimiFullyCachedDelta)
			require.Equal(t, tt.start, outs[0], "hit rate at message_start is 0, nothing to adjust")
			requireUsage(t, outs[1], "usage", wantDelta)
			require.Equal(t, "end_turn", gjson.Get(outs[1], "delta.stop_reason").String())
		})
	}

	t.Run("delta without message_start", func(t *testing.T) {
		outs := rewriteStream(0.05, kimiFullyCachedDelta)
		requireUsage(t, outs[0], "usage", wantDelta)
	})

	t.Run("delta with one uncached token", func(t *testing.T) {
		oneUncached := `{"type":"message_delta","usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":173305,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173305}}`
		outs := rewriteStream(0.05, kimiZeroBucketsStart, oneUncached)
		requireUsage(t, outs[1], "usage", map[string]int64{"input_tokens": 8666, "cache_read_input_tokens": 164640})
	})
}

func TestAnthropicStreamRewriter_PartiallyCachedKimiDeltaReportsBilledInput(t *testing.T) {
	outs := rewriteStream(0.05, kimiZeroBucketsStart, kimiPartialDelta)

	require.Equal(t, kimiZeroBucketsStart, outs[0])
	requireUsage(t, outs[1], "usage", map[string]int64{
		"input_tokens":            8915,
		"cache_read_input_tokens": 164391,
		"cached_tokens":           164391,
		"prompt_tokens":           173306,
	})
}

func TestAnthropicStreamRewriter_MirroredPromptTotalDeltaKeepsTotalAndOmitsCacheRead(t *testing.T) {
	outs := rewriteStream(0.05, glmStart, glmDelta)

	requireUsage(t, outs[0], "message.usage", map[string]int64{
		"input_tokens":                        1200,
		"prompt_tokens":                       1200,
		"prompt_tokens_details.cached_tokens": 740,
		"output_tokens":                       1,
	})
	requireUsage(t, outs[1], "usage", map[string]int64{
		"input_tokens":                        1200,
		"prompt_tokens":                       1200,
		"prompt_tokens_details.cached_tokens": 740,
		"output_tokens":                       30,
	})
	for _, out := range outs {
		require.False(t, gjson.Get(out, "message.usage.cache_read_input_tokens").Exists())
		require.False(t, gjson.Get(out, "usage.cache_read_input_tokens").Exists())
	}
}

func TestAnthropicStreamRewriter_HitMissDeltaReportsUncachedInputWithCacheRead(t *testing.T) {
	outs := rewriteStream(0.05, deepSeekStart, deepSeekDelta)

	want := map[string]int64{
		"input_tokens":             460,
		"prompt_cache_hit_tokens":  740,
		"prompt_cache_miss_tokens": 460,
		"cache_read_input_tokens":  740,
	}
	requireUsage(t, outs[0], "message.usage", want)
	requireUsage(t, outs[1], "usage", want)
}

func TestAnthropicStreamRewriter_DeltaInputTokensByEventShape(t *testing.T) {
	for _, tt := range []struct {
		name      string
		events    []string
		wantInput int64
		wantCache int64
	}{
		{
			name: "delta without input_tokens key takes the cumulative count",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":900,"cache_read_input_tokens":100,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"cache_read_input_tokens":1000,"output_tokens":50}}`,
			},
			wantInput: 995,
			wantCache: 905,
		},
		{
			name: "native buckets",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":120}}`,
			},
			wantInput: 250,
			wantCache: 750,
		},
		{
			name:      "explicit zero cache read without message_start",
			events:    []string{`{"type":"message_delta","usage":{"input_tokens":200,"cache_read_input_tokens":0,"output_tokens":10}}`},
			wantInput: 200,
			wantCache: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outs := rewriteStream(0.05, tt.events...)
			last := outs[len(outs)-1]
			require.Equal(t, tt.wantInput, gjson.Get(last, "usage.input_tokens").Int())
			require.Equal(t, tt.wantCache, gjson.Get(last, "usage.cache_read_input_tokens").Int())
		})
	}
}

func TestAnthropicStreamRewriter_SynthesizedKeysFollowInputTokensSense(t *testing.T) {
	t.Run("message_start with cache read only", func(t *testing.T) {
		outs := rewriteStream(0.05, `{"type":"message_start","message":{"usage":{"cache_read_input_tokens":800,"output_tokens":1}}}`)
		requireUsage(t, outs[0], "message.usage", map[string]int64{"input_tokens": 40, "cache_read_input_tokens": 760})
	})

	t.Run("message_delta with cache read only and no message_start", func(t *testing.T) {
		outs := rewriteStream(0.05, `{"type":"message_delta","usage":{"cache_read_input_tokens":800,"output_tokens":10}}`)
		requireUsage(t, outs[0], "usage", map[string]int64{"input_tokens": 40, "cache_read_input_tokens": 760})
	})

	t.Run("cached_tokens alias only", func(t *testing.T) {
		outs := rewriteStream(0.05,
			`{"type":"message_start","message":{"usage":{"input_tokens":200,"cached_tokens":800,"output_tokens":1}}}`,
			`{"type":"message_delta","usage":{"input_tokens":200,"cached_tokens":800,"output_tokens":120}}`)
		requireUsage(t, outs[0], "message.usage", map[string]int64{"input_tokens": 250, "cached_tokens": 750, "cache_read_input_tokens": 750})
		requireUsage(t, outs[1], "usage", map[string]int64{"input_tokens": 250, "cached_tokens": 750, "cache_read_input_tokens": 750})
	})

	t.Run("mirrored prompt total keeps the synthesized cache read out", func(t *testing.T) {
		start := `{"type":"message_start","message":{"usage":{"input_tokens":1000,"prompt_tokens":1000,"cached_tokens":800,"cache_creation_input_tokens":0,"output_tokens":1}}}`
		outs := rewriteStream(0.05, start)
		requireUsage(t, outs[0], "message.usage", map[string]int64{"input_tokens": 1000, "cached_tokens": 750})
		require.False(t, gjson.Get(outs[0], "message.usage.cache_read_input_tokens").Exists())
	})

	t.Run("below threshold after an adjusted start keeps the synthesized cache read out", func(t *testing.T) {
		lowDelta := `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":50}}}`
		outs := rewriteStream(0.05, glmStart, lowDelta)
		requireUsage(t, outs[1], "usage", map[string]int64{"input_tokens": 1200, "prompt_tokens_details.cached_tokens": 50})
		require.False(t, gjson.Get(outs[1], "usage.cache_read_input_tokens").Exists())
	})

	t.Run("below threshold after an exposed synthesized cache read resets it", func(t *testing.T) {
		lowDelta := `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":50,"prompt_cache_miss_tokens":1150}}`
		outs := rewriteStream(0.05, deepSeekStart, lowDelta)
		requireUsage(t, outs[1], "usage", map[string]int64{
			"input_tokens":             1200,
			"prompt_cache_hit_tokens":  50,
			"prompt_cache_miss_tokens": 1150,
			"cache_read_input_tokens":  0,
		})
	})

	t.Run("keys reported upstream are still written back", func(t *testing.T) {
		outs := rewriteStream(0.05,
			`{"type":"message_start","message":{"usage":{"input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}`,
			`{"type":"message_delta","usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":10}}`)
		requireUsage(t, outs[1], "usage", map[string]int64{"input_tokens": 250, "cache_read_input_tokens": 750})
	})
}

// sdkUsage accumulates a stream the way the official Anthropic SDK does: message_start resets
// the usage snapshot and every usage key of message_delta overrides it, including zeros.
func sdkUsage(events ...string) map[string]int64 {
	usage := map[string]int64{}
	for _, event := range events {
		path := ""
		switch gjson.Get(event, "type").String() {
		case "message_start":
			usage = map[string]int64{}
			path = "message.usage"
		case "message_delta":
			path = "usage"
		default:
			continue
		}
		gjson.Get(event, path).ForEach(func(key, value gjson.Result) bool {
			usage[key.String()] = value.Int()
			return true
		})
	}
	return usage
}

func sdkTotalPrompt(usage map[string]int64) int64 {
	return usage["input_tokens"] + usage["cache_read_input_tokens"] + usage["cache_creation_input_tokens"]
}

func TestAnthropicStreamRewriter_SDKTotalPromptTokensMatchUpstream(t *testing.T) {
	for _, tt := range []struct {
		name      string
		events    []string
		wantTotal int64
	}{
		{name: "Kimi fully cached", events: []string{kimiFullyCachedStart, kimiFullyCachedDelta}, wantTotal: 173306},
		{name: "Kimi fully cached with explicit zero buckets", events: []string{kimiZeroBucketsStart, kimiFullyCachedDelta}, wantTotal: 173306},
		{name: "Kimi fully cached with a start lacking prompt_tokens", events: []string{kimiNoPromptTotalStart, kimiFullyCachedDelta}, wantTotal: 173306},
		{name: "Kimi partially cached", events: []string{kimiZeroBucketsStart, kimiPartialDelta}, wantTotal: 173306},
		{name: "GLM", events: []string{glmStart, glmDelta}, wantTotal: 1200},
		{name: "DeepSeek", events: []string{deepSeekStart, deepSeekDelta}, wantTotal: 1200},
		{
			name:      "DeepSeek hit rate drops below threshold",
			events:    []string{deepSeekStart, `{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_cache_hit_tokens":50,"prompt_cache_miss_tokens":1150}}`},
			wantTotal: 1200,
		},
		{
			name: "DeepSeek events also carrying a prompt_tokens equal to input_tokens",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":1200,"output_tokens":1,"prompt_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}}`,
				`{"type":"message_delta","usage":{"input_tokens":1200,"output_tokens":30,"prompt_tokens":1200,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}}`,
			},
			wantTotal: 1200,
		},
		{
			name: "native Anthropic output-only delta",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"output_tokens":120}}`,
			},
			wantTotal: 1000,
		},
		{
			name: "native Anthropic delta repeats cumulative buckets",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"input_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":800,"output_tokens":120}}`,
			},
			wantTotal: 1000,
		},
		{
			name: "native Anthropic with cache creation",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_creation_input_tokens":20000,"cache_read_input_tokens":8000,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"input_tokens":200,"cache_read_input_tokens":8000,"output_tokens":500}}`,
			},
			wantTotal: 28200,
		},
		{
			name: "native Anthropic hit rate drops below threshold",
			events: []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":{"input_tokens":9000,"output_tokens":100}}`,
			},
			wantTotal: 9800,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			outs := rewriteStream(0.05, tt.events...)
			for i := range tt.events {
				require.Equal(t, sdkTotalPrompt(sdkUsage(tt.events[:i+1]...)), sdkTotalPrompt(sdkUsage(outs[:i+1]...)), "after event %d", i)
			}
			require.Equal(t, tt.wantTotal, sdkTotalPrompt(sdkUsage(outs...)))
		})
	}
}

func TestRewriteAnthropicUsageJSON_MirroredInputTokensPreserved(t *testing.T) {
	for _, tt := range []struct {
		name          string
		body          string
		wantInput     int64
		wantCacheRead int64
	}{
		{
			name:          "mirrored total with cache read aliases",
			body:          `{"id":"msg_1","usage":{"input_tokens":173306,"cache_read_input_tokens":173306,"output_tokens":8,"prompt_tokens":173306,"cached_tokens":173306}}`,
			wantInput:     173306,
			wantCacheRead: 164641,
		},
		{
			name:          "mirrored total with nested cached_tokens",
			body:          `{"id":"msg_1","usage":{"input_tokens":1200,"output_tokens":300,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`,
			wantInput:     1200,
			wantCacheRead: 740,
		},
		{
			name:          "exclusive input and cache read buckets",
			body:          `{"id":"msg_1","usage":{"input_tokens":200,"cache_read_input_tokens":800,"output_tokens":50}}`,
			wantInput:     250,
			wantCacheRead: 750,
		},
		{
			name:          "hit alias without prompt total",
			body:          `{"id":"msg_1","usage":{"input_tokens":1000,"prompt_cache_hit_tokens":800,"output_tokens":50}}`,
			wantInput:     1090,
			wantCacheRead: 710,
		},
		{
			name:          "hit alias with zero prompt total",
			body:          `{"id":"msg_1","usage":{"input_tokens":1000,"prompt_tokens":0,"prompt_cache_hit_tokens":800,"output_tokens":50}}`,
			wantInput:     1090,
			wantCacheRead: 710,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := RewriteAnthropicUsageJSON([]byte(tt.body), "usage", 0.05)
			require.Equal(t, tt.wantInput, gjson.GetBytes(out, "usage.input_tokens").Int())
			if gjson.GetBytes(out, "usage.cache_read_input_tokens").Exists() {
				require.Equal(t, tt.wantCacheRead, gjson.GetBytes(out, "usage.cache_read_input_tokens").Int())
			}
			if gjson.GetBytes(out, "usage.cached_tokens").Exists() {
				require.Equal(t, tt.wantCacheRead, gjson.GetBytes(out, "usage.cached_tokens").Int())
			}
			if gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Exists() {
				require.Equal(t, tt.wantCacheRead, gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int())
			}
			if gjson.GetBytes(out, "usage.prompt_cache_hit_tokens").Exists() {
				require.Equal(t, tt.wantCacheRead, gjson.GetBytes(out, "usage.prompt_cache_hit_tokens").Int())
			}
		})
	}

	t.Run("nested cached_tokens adds no cache_read_input_tokens", func(t *testing.T) {
		out := RewriteAnthropicUsageJSON([]byte(`{"usage":{"input_tokens":1200,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}}`), "usage", 0.05)
		require.False(t, gjson.GetBytes(out, "usage.cache_read_input_tokens").Exists())
	})
}

func TestRewriteAnthropicUsageJSON_ZeroCacheReadBesideMirroredInputTokens(t *testing.T) {
	for _, tt := range []struct {
		name string
		// usage omits output_tokens; the stream events below add their own.
		usage string
		want  map[string]int64
		// keepSDKTotal expects the SDK prompt total of the rewritten stream to equal the upstream one.
		keepSDKTotal bool
	}{
		{
			name:         "mirrored total with a zero cache read keeps input and cache read",
			usage:        `{"input_tokens":1200,"cache_read_input_tokens":0,"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":800}}`,
			want:         map[string]int64{"input_tokens": 1200, "cache_read_input_tokens": 0, "prompt_tokens_details.cached_tokens": 740},
			keepSDKTotal: true,
		},
		{
			name:         "hit and miss buckets with a zero cache read take the adjusted cache read",
			usage:        `{"input_tokens":1200,"cache_read_input_tokens":0,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":400}`,
			want:         map[string]int64{"input_tokens": 460, "cache_read_input_tokens": 740, "prompt_cache_hit_tokens": 740, "prompt_cache_miss_tokens": 460},
			keepSDKTotal: true,
		},
		{
			name:  "mirrored total with a positive cache read keeps adjusting the cache read",
			usage: `{"input_tokens":1200,"cache_read_input_tokens":800,"prompt_tokens":1200,"cached_tokens":800}`,
			want:  map[string]int64{"input_tokens": 1200, "cache_read_input_tokens": 740, "cached_tokens": 740},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requireUsage(t, string(RewriteAnthropicUsageJSON([]byte(`{"id":"msg_1","usage":`+tt.usage+`}`), "usage", 0.05)), "usage", tt.want)

			prompt := strings.TrimSuffix(tt.usage, "}")
			events := []string{
				`{"type":"message_start","message":{"usage":` + prompt + `,"output_tokens":1}}}`,
				`{"type":"message_delta","usage":` + prompt + `,"output_tokens":166},"delta":{"stop_reason":"end_turn"}}`,
			}
			outs := rewriteStream(0.05, events...)
			wantStart := maps.Clone(tt.want)
			wantStart["output_tokens"] = 1
			wantDelta := maps.Clone(tt.want)
			wantDelta["output_tokens"] = 166
			requireUsage(t, outs[0], "message.usage", wantStart)
			requireUsage(t, outs[1], "usage", wantDelta)
			if tt.keepSDKTotal {
				require.Equal(t, sdkTotalPrompt(sdkUsage(events...)), sdkTotalPrompt(sdkUsage(outs...)))
			}
		})
	}
}
