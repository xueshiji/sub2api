package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/cachehitadj"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// cacheHitRewriteReadsAsBilled reports whether rewritten carries the input and cache read counts
// the adjustment bills for, and billed's cache creation count.
func cacheHitRewriteReadsAsBilled(rewritten, billed ClaudeUsage, adjustment float64) bool {
	adj := cachehitadj.Adjust(billed.InputTokens, billed.CacheReadInputTokens, billed.CacheCreationInputTokens, adjustment)
	return rewritten.InputTokens == adj.InputTokens &&
		rewritten.CacheReadInputTokens == adj.CacheReadTokens &&
		rewritten.CacheCreationInputTokens == billed.CacheCreationInputTokens
}

// rewriteAnthropicUsageAsBilled returns body with its usage rewritten when that usage reads
// back through parse as the adjusted billed usage, and body untouched otherwise.
func rewriteAnthropicUsageAsBilled(body []byte, adjustment float64, billed ClaudeUsage, parse func([]byte) ClaudeUsage) []byte {
	rewritten := cachehitadj.RewriteAnthropicUsageJSON(body, "usage", adjustment)
	if bytes.Equal(rewritten, body) || cacheHitRewriteReadsAsBilled(parse(rewritten), billed, adjustment) {
		return rewritten
	}
	return body
}

// cacheHitStreamRewriter forwards an event rewritten only when its usage reads back, on top of
// the usage the client received before it, as the adjusted billed usage.
type cacheHitStreamRewriter struct {
	rewriter cachehitadj.AnthropicStreamRewriter
	// parse reads an event into a copy of the usage forwarded so far.
	parse func(data []byte, usage ClaudeUsage) ClaudeUsage
	// forwarded is the usage as parsed from the events forwarded so far.
	forwarded ClaudeUsage
}

// Rewrite takes the usage as billed just before data's event.
func (r *cacheHitStreamRewriter) Rewrite(data []byte, adjustment float64, usage ClaudeUsage) []byte {
	switch gjson.GetBytes(data, "type").String() {
	case "message_start", "message_delta":
	default:
		return data
	}
	rewritten := r.rewriter.Rewrite(data, adjustment)
	if !bytes.Equal(rewritten, data) &&
		!cacheHitRewriteReadsAsBilled(r.parse(rewritten, r.forwarded), r.parse(data, usage), adjustment) {
		rewritten = data
	}
	r.forwarded = r.parse(rewritten, r.forwarded)
	return rewritten
}

// CacheHitRateAdjustmentFromGroup returns the normalized cache hit rate
// adjustment ratio in [0, 0.10] for group (0 when nil or disabled).
func CacheHitRateAdjustmentFromGroup(group *Group) float64 {
	if group == nil {
		return 0
	}
	return cachehitadj.NormalizeAdjustment(group.CacheHitRateAdjustment)
}

func cacheHitRateAdjustmentFromContext(ctx context.Context) float64 {
	if ctx == nil {
		return 0
	}
	// Prefer the auth-time billing group frozen by WithGatewayTokenRequestPricing
	// so fallback/composite scheduling overrides on ctxkey.Group cannot drift from RecordUsage.
	if group := gatewayTokenRequestBillingGroupFromContext(ctx); group != nil {
		return CacheHitRateAdjustmentFromGroup(group)
	}
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && group != nil {
		return CacheHitRateAdjustmentFromGroup(group)
	}
	return 0
}

func cacheHitRateAdjustmentFromGin(c *gin.Context) float64 {
	if c == nil || c.Request == nil {
		return 0
	}
	return cacheHitRateAdjustmentFromContext(c.Request.Context())
}

// rewriteSSEDataLine rewrites a single SSE "data: ..." line (preserving any
// trailing \r\n or \n) using fn when adj > 0.
func rewriteSSEDataLine(line string, adj float64, fn func([]byte, float64) []byte) string {
	if adj <= 0 || fn == nil || !strings.Contains(line, "data:") {
		return line
	}
	trimmedEnd := strings.TrimRight(line, "\r\n")
	suffix := line[len(trimmedEnd):]
	if !strings.HasPrefix(trimmedEnd, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmedEnd, "data:"))
	if payload == "" || payload == "[DONE]" {
		return line
	}
	rawBytes := []byte(payload)
	rewritten := fn(rawBytes, adj)
	if bytes.Equal(rewritten, rawBytes) {
		return line
	}
	return "data: " + string(rewritten) + suffix
}

func rewriteSSEBlock(block string, adj float64, fn func([]byte, float64) []byte) string {
	if adj <= 0 || fn == nil || !strings.Contains(block, "usage") {
		return block
	}
	lines := strings.Split(block, "\n")
	changed := false
	for i, line := range lines {
		rewritten := rewriteSSEDataLine(line, adj, fn)
		if rewritten != line {
			lines[i] = rewritten
			changed = true
		}
	}
	if !changed {
		return block
	}
	return strings.Join(lines, "\n")
}

func writeJSONWithUsageRewrite(c *gin.Context, status int, body any, adj float64, fn func([]byte, float64) []byte) {
	if adj > 0 && fn != nil {
		if b, err := json.Marshal(body); err == nil {
			c.Data(status, "application/json; charset=utf-8", fn(b, adj))
			return
		}
	}
	c.JSON(status, body)
}
