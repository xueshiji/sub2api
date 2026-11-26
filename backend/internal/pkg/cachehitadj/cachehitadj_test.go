package cachehitadj

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestValidateAndNormalizeAdjustment(t *testing.T) {
	require.NoError(t, ValidateAdjustment(0))
	require.NoError(t, ValidateAdjustment(0.05))
	require.NoError(t, ValidateAdjustment(0.10))

	require.Error(t, ValidateAdjustment(-0.001))
	require.Error(t, ValidateAdjustment(0.1001))
	require.Error(t, ValidateAdjustment(math.NaN()))
	require.Error(t, ValidateAdjustment(math.Inf(1)))

	require.Equal(t, 0.0, NormalizeAdjustment(-0.05))
	require.Equal(t, 0.0, NormalizeAdjustment(math.NaN()))
	require.Equal(t, 0.10, NormalizeAdjustment(0.25))
	require.Equal(t, 0.0512, NormalizeAdjustment(0.05124))
}

func TestAdjustThresholdAndFormula(t *testing.T) {
	t.Run("disabled when adjustment is 0", func(t *testing.T) {
		res := Adjust(1000, 8000, 1000, 0)
		require.False(t, res.Applied)
		require.Equal(t, 1000, res.InputTokens)
		require.Equal(t, 8000, res.CacheReadTokens)
		require.Equal(t, 0, res.Reduction)
	})

	t.Run("skipped at exact 10 percent hit rate", func(t *testing.T) {
		// 100 / (800 + 100 + 100) = 10.0%
		res := Adjust(800, 100, 100, 0.05)
		require.False(t, res.Applied)
		require.Equal(t, 800, res.InputTokens)
		require.Equal(t, 100, res.CacheReadTokens)
	})

	t.Run("applied when strictly above 10 percent", func(t *testing.T) {
		// 101 / (799 + 101 + 100) = 10.1%
		res := Adjust(799, 101, 100, 0.05)
		require.True(t, res.Applied)
		require.Equal(t, 50, res.Reduction)
		require.Equal(t, 849, res.InputTokens)
		require.Equal(t, 51, res.CacheReadTokens)
	})

	t.Run("80 percent minus 5 percent with cache_creation in denominator", func(t *testing.T) {
		// totalPrompt = 1000 + 8000 + 1000 = 10000; 5% => delta = 500
		res := Adjust(1000, 8000, 1000, 0.05)
		require.True(t, res.Applied)
		require.Equal(t, 500, res.Reduction)
		require.Equal(t, 1500, res.InputTokens)
		require.Equal(t, 7500, res.CacheReadTokens)
		require.Equal(t, 0.05, res.Adjustment)
	})

	t.Run("clamps delta to cache_read_tokens when hit rate 10.1 percent and adjustment 10 percent", func(t *testing.T) {
		// totalPrompt = 1000, cache_read = 101 -> round(1000*0.10)=100 <= 101
		res := Adjust(899, 101, 0, 0.10)
		require.True(t, res.Applied)
		require.Equal(t, 100, res.Reduction)
		require.Equal(t, 1, res.CacheReadTokens)

		// totalPrompt = 10000, cache_read = 1001, adj = 0.10 -> delta = 1000
		res2 := Adjust(8999, 1001, 0, 0.10)
		require.True(t, res2.Applied)
		require.Equal(t, 1000, res2.Reduction)
		require.Equal(t, 1, res2.CacheReadTokens)

		// Small prompt where delta rounds to 0 is not applied
		res3 := Adjust(1, 2, 0, 0.05) // 3 * 0.05 = 0.15 -> 0
		require.False(t, res3.Applied)
	})
}

func TestRewriteAnthropicSSEAndJSON(t *testing.T) {
	rewriter := &AnthropicStreamRewriter{}
	msgStart := []byte(`{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":8000,"output_tokens":1}}}`)
	rewrittenStart := rewriter.Rewrite(msgStart, 0.05)
	require.Equal(t, int64(1500), gjson.GetBytes(rewrittenStart, "message.usage.input_tokens").Int())
	require.Equal(t, int64(7500), gjson.GetBytes(rewrittenStart, "message.usage.cache_read_input_tokens").Int())
	require.Equal(t, int64(1000), gjson.GetBytes(rewrittenStart, "message.usage.cache_creation_input_tokens").Int())

	// message_delta with only output_tokens stays untouched
	msgDeltaNoCache := []byte(`{"type":"message_delta","usage":{"output_tokens":120}}`)
	require.Equal(t, msgDeltaNoCache, rewriter.Rewrite(msgDeltaNoCache, 0.05))

	// message_delta carrying cumulative prompt usage
	msgDeltaWithCache := []byte(`{"type":"message_delta","usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":8000,"output_tokens":120}}`)
	rewrittenDelta := rewriter.Rewrite(msgDeltaWithCache, 0.05)
	require.Equal(t, int64(1500), gjson.GetBytes(rewrittenDelta, "usage.input_tokens").Int())
	require.Equal(t, int64(7500), gjson.GetBytes(rewrittenDelta, "usage.cache_read_input_tokens").Int())
}

func TestRewriteOpenAIAndGeminiUsageJSON(t *testing.T) {
	responsesPayload := []byte(`{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":8000},"output_tokens":200}}}`)
	rewrittenResp := RewriteOpenAIUsageJSON(responsesPayload, 0.05)
	require.Equal(t, int64(10000), gjson.GetBytes(rewrittenResp, "response.usage.input_tokens").Int())
	require.Equal(t, int64(7500), gjson.GetBytes(rewrittenResp, "response.usage.input_tokens_details.cached_tokens").Int())
	require.Equal(t, int64(200), gjson.GetBytes(rewrittenResp, "response.usage.output_tokens").Int())

	chatDeepSeek := []byte(`{"id":"c1","usage":{"prompt_tokens":1000,"prompt_cache_hit_tokens":800,"prompt_cache_miss_tokens":200,"completion_tokens":50}}`)
	rewrittenChat := RewriteOpenAIUsageJSON(chatDeepSeek, 0.05)
	require.Equal(t, int64(1000), gjson.GetBytes(rewrittenChat, "usage.prompt_tokens").Int())
	require.Equal(t, int64(750), gjson.GetBytes(rewrittenChat, "usage.prompt_cache_hit_tokens").Int())
	require.Equal(t, int64(250), gjson.GetBytes(rewrittenChat, "usage.prompt_cache_miss_tokens").Int())

	geminiPayload := []byte(`{"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":800,"candidatesTokenCount":50}}`)
	rewrittenGemini := RewriteGeminiUsageJSON(geminiPayload, 0.05)
	require.Equal(t, int64(1000), gjson.GetBytes(rewrittenGemini, "usageMetadata.promptTokenCount").Int())
	require.Equal(t, int64(750), gjson.GetBytes(rewrittenGemini, "usageMetadata.cachedContentTokenCount").Int())
}

func TestAnthropicStreamRewriter_PartialUsageKeepsCumulativePromptCounts(t *testing.T) {
	for _, tt := range []struct {
		name      string
		delta     string
		wantInput int64
		wantCache int64
	}{
		{"cache read update", `{"cache_read_input_tokens":1000}`, 260, 940},
		{"input update", `{"input_tokens":400}`, 460, 740},
		{"cache creation update", `{"cache_creation_input_tokens":200}`, 260, 740},
		{"zero input retains start count", `{"input_tokens":0,"cache_read_input_tokens":1000}`, 260, 940},
		{"zero prompt retains start counts", `{"input_tokens":0,"cache_read_input_tokens":0}`, 250, 750},
		{"hit rate falls below threshold", `{"input_tokens":9000}`, 9000, 800},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &AnthropicStreamRewriter{}
			start := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800}}}`)
			r.Rewrite(start, 0.05)
			delta := []byte(`{"type":"message_delta","usage":` + tt.delta + `,"delta":{"stop_reason":"end_turn"}}`)
			out := r.Rewrite(delta, 0.05)
			require.Equal(t, tt.wantInput, gjson.GetBytes(out, "usage.input_tokens").Int())
			require.Equal(t, tt.wantCache, gjson.GetBytes(out, "usage.cache_read_input_tokens").Int())
			require.Equal(t, "end_turn", gjson.GetBytes(out, "delta.stop_reason").String())
		})
	}
}

func TestAnthropicStreamRewriter_OutputOnlyDeltaPreservesBytes(t *testing.T) {
	r := &AnthropicStreamRewriter{}
	r.Rewrite([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800}}}`), 0.05)
	delta := []byte(`{"type":"message_delta", "usage":{"output_tokens":20}}`)
	require.Equal(t, delta, r.Rewrite(delta, 0.05))
}

func TestAnthropicStreamRewriter_MessageStartResetsPromptCounts(t *testing.T) {
	r := &AnthropicStreamRewriter{}
	r.Rewrite([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":200,"cache_read_input_tokens":800}}}`), 0.05)
	start := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":900,"cache_read_input_tokens":100}}}`)
	require.Equal(t, start, r.Rewrite(start, 0.05))
	delta := []byte(`{"type":"message_delta","usage":{"cache_read_input_tokens":300}}`)
	out := r.Rewrite(delta, 0.05)
	require.Equal(t, int64(960), gjson.GetBytes(out, "usage.input_tokens").Int())
	require.Equal(t, int64(240), gjson.GetBytes(out, "usage.cache_read_input_tokens").Int())
}
