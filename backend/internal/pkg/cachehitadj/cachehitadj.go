package cachehitadj

import (
	"bytes"
	"fmt"
	"math"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// MinEffectiveHitRate is the real cache hit rate threshold (strictly > 10%)
	// required before cache hit rate adjustment takes effect.
	MinEffectiveHitRate = 0.10
	// MaxAdjustment is the maximum allowed adjustment ratio (10% = 0.10).
	MaxAdjustment = 0.10
)

// Result holds the adjusted text prompt token counts and metadata.
type Result struct {
	InputTokens     int
	CacheReadTokens int
	Reduction       int
	Applied         bool
	Adjustment      float64
}

// ValidateAdjustment validates a group's cache_hit_rate_adjustment ratio in [0, 0.10].
func ValidateAdjustment(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > MaxAdjustment {
		return fmt.Errorf("cache_hit_rate_adjustment 应为 [0, 0.10] 的小数，got %v", v)
	}
	return nil
}

// NormalizeAdjustment clamps and rounds adjustment to 4 decimal places.
func NormalizeAdjustment(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	if v > MaxAdjustment {
		v = MaxAdjustment
	}
	return math.Round(v*10000) / 10000
}

// Adjust shifts a portion of cache_read_tokens into input_tokens when the real
// cache hit rate (cache_read / total_prompt) is strictly greater than 10%.
// Total prompt tokens and cache_creation_tokens remain invariant.
func Adjust(inputTokens, cacheReadTokens, cacheCreationTokens int, adjustment float64) Result {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if cacheCreationTokens < 0 {
		cacheCreationTokens = 0
	}
	if cacheReadTokens < 0 {
		cacheReadTokens = 0
	}

	adj := NormalizeAdjustment(adjustment)
	if adj <= 0 || cacheReadTokens <= 0 {
		return Result{
			InputTokens:     inputTokens,
			CacheReadTokens: cacheReadTokens,
		}
	}

	totalPrompt := inputTokens + cacheReadTokens + cacheCreationTokens
	// Trigger strictly when real hit rate > 10% (exact integer check avoids float boundary drift).
	if totalPrompt <= 0 || cacheReadTokens*10 <= totalPrompt {
		return Result{
			InputTokens:     inputTokens,
			CacheReadTokens: cacheReadTokens,
		}
	}

	delta := int(math.Round(float64(totalPrompt) * adj))
	if delta > cacheReadTokens {
		delta = cacheReadTokens
	}
	if delta <= 0 {
		return Result{
			InputTokens:     inputTokens,
			CacheReadTokens: cacheReadTokens,
		}
	}

	return Result{
		InputTokens:     inputTokens + delta,
		CacheReadTokens: cacheReadTokens - delta,
		Reduction:       delta,
		Applied:         true,
		Adjustment:      adj,
	}
}

type AnthropicStreamRewriter struct {
	usage   []byte
	applied bool
	// exposed lists synthesized usage keys already written to an earlier event of the stream.
	exposed []byte
}

func (r *AnthropicStreamRewriter) Rewrite(data []byte, adjustment float64) []byte {
	if NormalizeAdjustment(adjustment) <= 0 || !bytes.Contains(data, []byte("usage")) {
		return data
	}
	usagePath := "usage"
	switch gjson.GetBytes(data, "type").String() {
	case "message_start":
		usagePath = "message.usage"
		r.usage = []byte(`{}`)
		r.applied = false
		r.exposed = []byte(`{}`)
	case "message_delta":
	default:
		return data
	}
	usage := gjson.GetBytes(data, usagePath)
	if !usage.IsObject() {
		return data
	}
	if len(r.usage) == 0 {
		r.usage = []byte(`{}`)
	}
	if len(r.exposed) == 0 {
		r.exposed = []byte(`{}`)
	}
	// Kimi's message_delta reports the uncached count in input_tokens (0 when fully cached),
	// replacing a cumulative input_tokens that only mirrors the prompt total.
	promptTotal := gjson.GetBytes(r.usage, "prompt_tokens")
	if !promptTotal.Exists() {
		promptTotal = usage.Get("prompt_tokens")
	}
	deltaInputReplacesMirror := usagePath == "usage" &&
		promptTotal.Int() > 0 &&
		gjson.GetBytes(r.usage, "input_tokens").Int() == promptTotal.Int() &&
		usage.Get("input_tokens").Exists() && usage.Get("prompt_tokens").Exists() &&
		usage.Get("input_tokens").Int() != usage.Get("prompt_tokens").Int()
	fields := []string{
		"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
		"cached_tokens", "prompt_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens",
		"prompt_tokens_details.cached_tokens",
		"cache_creation.ephemeral_5m_input_tokens", "cache_creation.ephemeral_1h_input_tokens",
	}
	updated := false
	for _, field := range fields {
		value := usage.Get(field)
		if !value.Exists() {
			continue
		}
		updated = true
		// Delta prompt counters use the same positive-value merge as billing.
		if usagePath == "usage" && value.Int() <= 0 && !strings.HasPrefix(field, "cache_creation.") &&
			(field != "input_tokens" || !deltaInputReplacesMirror) {
			continue
		}
		if next, err := sjson.SetRawBytes(r.usage, field, []byte(value.Raw)); err == nil {
			r.usage = next
		}
	}
	if !updated {
		return data
	}
	snapshot := append(append([]byte(`{"usage":`), r.usage...), '}')
	// Keys absent from the event and the cumulative usage only feed Adjust.
	synthesized := map[string]bool{}
	for _, field := range []string{"input_tokens", "cache_read_input_tokens"} {
		if gjson.GetBytes(snapshot, "usage."+field).Exists() {
			continue
		}
		if value := usage.Get(field); value.Exists() {
			// A count reported by the event is never synthesized, even when the positive-value merge skipped it.
			snapshot, _ = sjson.SetRawBytes(snapshot, "usage."+field, []byte(value.Raw))
			continue
		}
		synthesized[field] = true
		snapshot, _ = sjson.SetBytes(snapshot, "usage."+field, 0)
	}
	rewritten := RewriteAnthropicUsageJSON(snapshot, "usage", adjustment)
	applied := !bytes.Equal(snapshot, rewritten)
	if !applied && !r.applied {
		return data
	}
	r.applied = applied
	uncachedInput := gjson.GetBytes(rewritten, "usage.input_tokens").Int() != gjson.GetBytes(snapshot, "usage.input_tokens").Int()
	for _, field := range fields {
		value := gjson.GetBytes(rewritten, "usage."+field)
		if !value.Exists() {
			continue
		}
		if synthesized[field] && !uncachedInput && (applied || !gjson.GetBytes(r.exposed, field).Exists()) {
			continue
		}
		if next, err := sjson.SetRawBytes(data, usagePath+"."+field, []byte(value.Raw)); err == nil {
			data = next
		}
		if synthesized[field] {
			r.exposed, _ = sjson.SetBytes(r.exposed, field, true)
		}
	}
	return data
}

// RewriteAnthropicUsageJSON rewrites Anthropic-compatible usage JSON at usagePath
// (e.g. "usage" or "message.usage") when adjustment applies.
func RewriteAnthropicUsageJSON(body []byte, usagePath string, adjustment float64) []byte {
	if NormalizeAdjustment(adjustment) <= 0 || len(body) == 0 || usagePath == "" || !bytes.Contains(body, []byte("usage")) {
		return body
	}
	usageNode := gjson.GetBytes(body, usagePath)
	if !usageNode.Exists() || !usageNode.IsObject() {
		return body
	}

	promptTokens := usageNode.Get("prompt_tokens")
	promptCacheHitTokens := usageNode.Get("prompt_cache_hit_tokens")
	promptCacheMissTokens := usageNode.Get("prompt_cache_miss_tokens")
	hasCompatPrompt := (promptTokens.Exists() && promptTokens.Int() > 0) ||
		promptCacheHitTokens.Exists() || promptCacheMissTokens.Exists()

	cacheCreation := max(int(usageNode.Get("cache_creation_input_tokens").Int()), 0)
	if cacheCreation == 0 {
		cc5m := max(int(usageNode.Get("cache_creation.ephemeral_5m_input_tokens").Int()), 0)
		cc1h := max(int(usageNode.Get("cache_creation.ephemeral_1h_input_tokens").Int()), 0)
		cacheCreation = cc5m + cc1h
	}

	cacheRead := 0
	if v := usageNode.Get("cache_read_input_tokens"); v.Exists() {
		cacheRead = max(int(v.Int()), 0)
	}
	if cacheRead == 0 {
		if v := usageNode.Get("cached_tokens"); v.Exists() {
			cacheRead = max(int(v.Int()), 0)
		}
	}
	if cacheRead == 0 {
		if v := usageNode.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			cacheRead = max(int(v.Int()), 0)
		}
	}
	if cacheRead == 0 && promptCacheHitTokens.Exists() {
		cacheRead = max(int(promptCacheHitTokens.Int()), 0)
	}

	var rawInput int
	if promptCacheMissTokens.Exists() {
		rawInput = max(int(promptCacheMissTokens.Int()), 0)
	} else if promptTokens.Exists() && promptTokens.Int() > 0 {
		rawInput = max(int(promptTokens.Int())-cacheRead-cacheCreation, 0)
	} else {
		rawInput = max(int(usageNode.Get("input_tokens").Int()), 0)
	}

	res := Adjust(rawInput, cacheRead, cacheCreation, adjustment)
	if !res.Applied {
		return body
	}

	// OpenAI-compat upstreams (e.g. Kimi message_start) report the prompt total in input_tokens; a mirror stays unchanged.
	inputNode := usageNode.Get("input_tokens")
	inputMirrorsPrompt := hasCompatPrompt && promptTokens.Exists() && promptTokens.Int() > 0 &&
		inputNode.Int() == promptTokens.Int()

	out := body
	// Beside a mirrored input_tokens a cache read of 0 or less is kept: the adjusted value would grow the prompt total.
	if cacheReadNode := usageNode.Get("cache_read_input_tokens"); cacheReadNode.Exists() && (!inputMirrorsPrompt || cacheReadNode.Int() > 0) {
		if updated, err := sjson.SetBytes(out, usagePath+".cache_read_input_tokens", res.CacheReadTokens); err == nil {
			out = updated
		}
	}
	if usageNode.Get("cached_tokens").Exists() {
		if updated, err := sjson.SetBytes(out, usagePath+".cached_tokens", res.CacheReadTokens); err == nil {
			out = updated
		}
	}
	if usageNode.Get("prompt_tokens_details.cached_tokens").Exists() {
		if updated, err := sjson.SetBytes(out, usagePath+".prompt_tokens_details.cached_tokens", res.CacheReadTokens); err == nil {
			out = updated
		}
	}
	if promptCacheHitTokens.Exists() {
		if updated, err := sjson.SetBytes(out, usagePath+".prompt_cache_hit_tokens", res.CacheReadTokens); err == nil {
			out = updated
		}
	}
	if promptCacheMissTokens.Exists() {
		if updated, err := sjson.SetBytes(out, usagePath+".prompt_cache_miss_tokens", res.InputTokens); err == nil {
			out = updated
		}
	}
	if inputNode.Exists() && !inputMirrorsPrompt {
		if updated, err := sjson.SetBytes(out, usagePath+".input_tokens", res.InputTokens); err == nil {
			out = updated
		}
	}
	return out
}

// OpenAIUsageRule selects the billing parser whose usage locations and token fields the rewrite mirrors.
type OpenAIUsageRule int

const (
	// OpenAIUsageRuleDefault mirrors the HTTP Responses / Chat Completions billing parser (extractOpenAIUsageFromJSONBytes).
	OpenAIUsageRuleDefault OpenAIUsageRule = iota
	// OpenAIUsageRuleWSPassthrough mirrors the WS v2 passthrough relay billing parser (parseUsageAndAccumulate).
	OpenAIUsageRuleWSPassthrough
	// OpenAIUsageRuleEmbeddings mirrors the embeddings billing parser (extractOpenAIEmbeddingsUsage).
	OpenAIUsageRuleEmbeddings
)

// RewriteOpenAIUsageJSON rewrites cached token counts inside every usage object
// OpenAIUsageRuleDefault reads while keeping total prompt tokens invariant.
func RewriteOpenAIUsageJSON(body []byte, adjustment float64) []byte {
	return RewriteOpenAIUsageJSONWithRule(body, OpenAIUsageRuleDefault, adjustment)
}

// RewriteOpenAIUsageJSONWithRule rewrites every usage object at the rule's locations, each from its own counts.
func RewriteOpenAIUsageJSONWithRule(body []byte, rule OpenAIUsageRule, adjustment float64) []byte {
	if NormalizeAdjustment(adjustment) <= 0 || len(body) == 0 || !bytes.Contains(body, []byte("usage")) {
		return body
	}
	out := body
	for _, usagePath := range rule.usagePaths() {
		if usageNode := gjson.GetBytes(out, usagePath); usageNode.IsObject() {
			out = rewriteOpenAIUsage(out, usagePath, rule, usageNode, adjustment)
		}
	}
	return out
}

func rewriteOpenAIUsage(out []byte, usagePath string, rule OpenAIUsageRule, usageNode gjson.Result, adjustment float64) []byte {
	totalPrompt := rule.promptTotal(usageNode)
	cacheRead := rule.cacheRead(usageNode)
	if cacheRead <= 0 || totalPrompt <= 0 {
		return out
	}

	cacheCreation := openAICacheCreationTokens(usageNode)
	uncached := max(totalPrompt-cacheRead-cacheCreation, 0)
	res := Adjust(uncached, cacheRead, cacheCreation, adjustment)
	if !res.Applied {
		return out
	}

	for _, field := range []string{
		"input_tokens_details.cached_tokens",
		"prompt_tokens_details.cached_tokens",
		"cache_read_input_tokens",
		"cache_read_tokens",
		"cached_tokens",
		"prompt_cache_hit_tokens",
	} {
		if usageNode.Get(field).Exists() {
			if updated, err := sjson.SetBytes(out, usagePath+"."+field, res.CacheReadTokens); err == nil {
				out = updated
			}
		}
	}
	if missNode := usageNode.Get("prompt_cache_miss_tokens"); missNode.Exists() {
		miss := max(int(missNode.Int()), 0)
		if updated, err := sjson.SetBytes(out, usagePath+".prompt_cache_miss_tokens", miss+res.Reduction); err == nil {
			out = updated
		}
	}
	return out
}

func (r OpenAIUsageRule) usagePaths() []string {
	switch r {
	case OpenAIUsageRuleWSPassthrough:
		return []string{"response.usage", "usage"}
	case OpenAIUsageRuleEmbeddings:
		return []string{"usage"}
	default:
		return []string{"usage", "response.usage", "data.usage", "data.response.usage"}
	}
}

// promptTotal returns the usage object's total prompt tokens as its billing parser reads them.
func (r OpenAIUsageRule) promptTotal(usage gjson.Result) int {
	switch r {
	case OpenAIUsageRuleWSPassthrough:
		return relayUsageNumber(usage, "input_tokens", "prompt_tokens")
	case OpenAIUsageRuleEmbeddings:
		return firstPositiveInt(usage.Get("prompt_tokens"), usage.Get("input_tokens"), usage.Get("total_tokens"))
	default:
		totalPrompt := int(usage.Get("input_tokens").Int())
		if totalPrompt == 0 {
			totalPrompt = int(usage.Get("prompt_tokens").Int())
		}
		return totalPrompt
	}
}

// cacheRead returns the usage object's cached prompt tokens as its billing parser reads them.
func (r OpenAIUsageRule) cacheRead(usage gjson.Result) int {
	if r == OpenAIUsageRuleWSPassthrough {
		return relayUsageNumber(usage, "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens")
	}
	return openAICacheReadTokens(usage)
}

// relayUsageNumber reads the first existing of two fields the way parseUsageAndAccumulate does:
// even at 0, and only when it is a JSON number.
func relayUsageNumber(usage gjson.Result, first, second string) int {
	value := usage.Get(first)
	if !value.Exists() {
		value = usage.Get(second)
	}
	if value.Type != gjson.Number {
		return 0
	}
	return int(value.Int())
}

func firstPositiveInt(values ...gjson.Result) int {
	for _, value := range values {
		if n := int(value.Int()); value.Exists() && n > 0 {
			return n
		}
	}
	return 0
}

func openAICacheReadTokens(usageNode gjson.Result) int {
	for _, field := range []string{
		"input_tokens_details.cached_tokens",
		"prompt_tokens_details.cached_tokens",
	} {
		if v := usageNode.Get(field); v.Exists() {
			return max(int(v.Int()), 0)
		}
	}
	for _, field := range []string{
		"cache_read_input_tokens",
		"cache_read_tokens",
		"cached_tokens",
		"prompt_cache_hit_tokens",
	} {
		if v := usageNode.Get(field); v.Exists() && v.Int() > 0 {
			return int(v.Int())
		}
	}
	return 0
}

func openAICacheCreationTokens(usageNode gjson.Result) int {
	for _, field := range []string{
		"input_tokens_details.cache_write_tokens",
		"prompt_tokens_details.cache_write_tokens",
		"input_tokens_details.cache_creation_tokens",
		"prompt_tokens_details.cache_creation_tokens",
	} {
		if v := usageNode.Get(field); v.Exists() {
			return max(int(v.Int()), 0)
		}
	}
	for _, field := range []string{
		"cache_write_tokens",
		"cache_creation_input_tokens",
		"cache_write_input_tokens",
		"cache_creation_tokens",
	} {
		if v := usageNode.Get(field); v.Exists() && v.Int() > 0 {
			return int(v.Int())
		}
	}
	return 0
}

// RewriteGeminiUsageJSON rewrites cachedContentTokenCount inside native Gemini or
// Antigravity-wrapped JSON payloads while keeping promptTokenCount invariant.
func RewriteGeminiUsageJSON(body []byte, adjustment float64) []byte {
	if NormalizeAdjustment(adjustment) <= 0 || len(body) == 0 || !bytes.Contains(body, []byte("cachedContentTokenCount")) {
		return body
	}
	usagePath := ""
	var usageNode gjson.Result
	if res := gjson.GetBytes(body, "response.usageMetadata"); res.Exists() && res.IsObject() {
		usagePath = "response.usageMetadata"
		usageNode = res
	} else if res := gjson.GetBytes(body, "usageMetadata"); res.Exists() && res.IsObject() {
		usagePath = "usageMetadata"
		usageNode = res
	} else {
		return body
	}

	cachedNode := usageNode.Get("cachedContentTokenCount")
	if !cachedNode.Exists() {
		return body
	}
	prompt := max(int(usageNode.Get("promptTokenCount").Int()), 0)
	cached := max(int(cachedNode.Int()), 0)
	uncached := max(prompt-cached, 0)
	res := Adjust(uncached, cached, 0, adjustment)
	if !res.Applied {
		return body
	}
	if updated, err := sjson.SetBytes(body, usagePath+".cachedContentTokenCount", res.CacheReadTokens); err == nil {
		return updated
	}
	return body
}
