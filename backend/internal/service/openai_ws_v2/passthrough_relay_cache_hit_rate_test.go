package openai_ws_v2

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/cachehitadj"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游 terminal 事件同时带 response.usage 与顶层 usage，各报提示 1000、缓存读取 800。
const relayDualUsageCompleted = `{"type":"response.completed","response":{"id":"resp_adj_2","usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":800}}},"usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":800}}}`

func TestRelay_PassthroughRuleRewritesEveryUsageCopyAndBillsRawUsage(t *testing.T) {
	t.Parallel()

	clientConn := newPassthroughTestFrameConn(nil, false)
	upstreamConn := newPassthroughTestFrameConn([]passthroughTestFrame{
		{msgType: coderws.MessageText, payload: []byte(relayDualUsageCompleted)},
	}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var observedTurnUsage *RelayTurnResult
	result, relayExit := Relay(ctx, clientConn, upstreamConn, []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[{"type":"input_text","text":"hello"}]}`), RelayOptions{
		OnTurnComplete: func(turn RelayTurnResult) {
			cp := turn
			observedTurnUsage = &cp
		},
		RewriteClientPayload: func(msgType coderws.MessageType, payload []byte) []byte {
			if msgType != coderws.MessageText {
				return payload
			}
			return cachehitadj.RewriteOpenAIUsageJSONWithRule(payload, cachehitadj.OpenAIUsageRuleWSPassthrough, 0.05)
		},
	})
	require.Nil(t, relayExit)
	require.Equal(t, 1000, result.Usage.InputTokens)
	require.Equal(t, 800, result.Usage.CacheReadInputTokens)
	require.NotNil(t, observedTurnUsage)
	require.Equal(t, 800, observedTurnUsage.Usage.CacheReadInputTokens)

	writes := clientConn.Writes()
	require.Len(t, writes, 1)
	clientPayload := string(writes[0].payload)
	require.Equal(t, int64(750), gjson.Get(clientPayload, "usage.input_tokens_details.cached_tokens").Int())
	require.Equal(t, int64(750), gjson.Get(clientPayload, "response.usage.input_tokens_details.cached_tokens").Int())
	require.Equal(t, int64(1000), gjson.Get(clientPayload, "response.usage.input_tokens").Int())
}
