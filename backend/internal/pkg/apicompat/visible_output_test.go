package apicompat

import (
	"testing"
	"time"
)

func TestAnthropicSSEEventCarriesVisibleOutput(t *testing.T) {
	cases := []struct {
		name      string
		eventName string
		data      string
		want      bool
	}{
		{"非空文本增量时返回 true", "", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"你"}}`, true},
		{"空文本增量时返回 false", "", `{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`, false},
		{"增量事件缺 delta 负载时返回 false", "", `{"type":"content_block_delta"}`, false},
		{"非空思考增量时返回 true", "", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"嗯"}}`, true},
		{"非空工具调用增量时返回 true", "", `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}`, true},
		{"携带初始文本的块开始事件返回 true", "", `{"type":"content_block_start","content_block":{"type":"text","text":"hi"}}`, true},
		{"空文本的块开始事件返回 false", "", `{"type":"content_block_start","content_block":{"type":"text","text":""}}`, false},
		{"受理即回的 message_start 返回 false", "", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`, false},
		{"ping 事件返回 false", "ping", `{"type":"ping"}`, false},
		{"event 行为 ping 而 data 缺 type 时返回 false", "ping", `{}`, false},
		{"data 缺 type 时回落 event 行事件名", "content_block_delta", `{"index":0,"delta":{"type":"text_delta","text":"你"}}`, true},
		{"event 行为增量而 data 为空时返回 false", "content_block_delta", ``, false},
		{"event 行为增量而 data 为 DONE 时返回 false", "content_block_delta", `[DONE]`, false},
		{"event 行与 data type 矛盾时以 data 为准", "content_block_delta", `{"type":"message_start"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnthropicSSEEventCarriesVisibleOutput(tc.eventName, tc.data); got != tc.want {
				t.Fatalf("AnthropicSSEEventCarriesVisibleOutput(%q, %q) = %v, want %v", tc.eventName, tc.data, got, tc.want)
			}
		})
	}
}

func TestAnthropicStreamEventCarriesVisibleOutput(t *testing.T) {
	cases := []struct {
		name  string
		event *AnthropicStreamEvent
		want  bool
	}{
		{"非空文本增量时返回 true", &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Text: "你"}}, true},
		{"空文本增量时返回 false", &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Text: ""}}, false},
		{"非空思考增量时返回 true", &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Thinking: "嗯"}}, true},
		{"非空工具调用增量时返回 true", &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{PartialJSON: "{"}}, true},
		{"非空签名增量时返回 true", &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Signature: "sig"}}, true},
		{"增量事件缺 delta 负载时返回 false", &AnthropicStreamEvent{Type: "content_block_delta"}, false},
		{"携带初始文本的块开始事件返回 true", &AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text", Text: "hi"}}, true},
		{"空文本的块开始事件返回 false", &AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text"}}, false},
		{"message_start 返回 false", &AnthropicStreamEvent{Type: "message_start"}, false},
		{"nil 事件返回 false", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.event.CarriesVisibleOutput(); got != tc.want {
				t.Fatalf("CarriesVisibleOutput() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkFirstVisibleOutputSSE(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)

	var got *int
	got = MarkFirstVisibleOutputSSE(got, start, "", `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`)
	if got != nil {
		t.Fatalf("message_start 不应消耗首 token，got %d", *got)
	}
	got = MarkFirstVisibleOutputSSE(got, start, "", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"你"}}`)
	if got == nil || *got < 40 {
		t.Fatalf("首个可见输出应记录 ≥40ms，got %v", got)
	}
	first := got
	got = MarkFirstVisibleOutputSSE(got, start, "", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"好"}}`)
	if got != first {
		t.Fatal("已记录后再次标记应保持原值")
	}
}

func TestMarkFirstVisibleOutputEvent(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)

	var got *int
	got = MarkFirstVisibleOutputEvent(got, start, &AnthropicStreamEvent{Type: "ping"})
	if got != nil {
		t.Fatalf("ping 不应消耗首 token，got %d", *got)
	}
	got = MarkFirstVisibleOutputEvent(got, start, &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Text: "你"}})
	if got == nil {
		t.Fatal("首个可见输出应记录首 token")
	}
	first := got
	got = MarkFirstVisibleOutputEvent(got, start, &AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Text: "好"}})
	if got != first {
		t.Fatal("已记录后再次标记应保持原值")
	}
}
