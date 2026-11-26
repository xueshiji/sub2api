package cachehitadj

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRewriteOpenAIUsageJSON_MatchesBillingUsageLocation(t *testing.T) {
	const chatChoices = `"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]`
	for _, tt := range []struct {
		name       string
		body       string
		usagePaths []string
		unchanged  bool
	}{
		{
			name:       "usage wrapped in data",
			body:       `{"data":{"id":"c1","object":"chat.completion",` + chatChoices + `,"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}}},"success":true}`,
			usagePaths: []string{"data.usage"},
		},
		{
			name:       "usage wrapped in data.response",
			body:       `{"data":{"response":{"id":"r1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"input_tokens_details":{"cached_tokens":800}}}}}`,
			usagePaths: []string{"data.response.usage"},
		},
		{
			name:       "responses event carries only response.usage",
			body:       `{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800},"output_tokens":200}}}`,
			usagePaths: []string{"response.usage"},
		},
		{
			name:       "chat completions carries only usage",
			body:       `{"id":"c1","object":"chat.completion",` + chatChoices + `,"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}}}`,
			usagePaths: []string{"usage"},
		},
		{
			name:       "null usage falls through to response.usage",
			body:       `{"usage":null,"response":{"id":"r1","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}}`,
			usagePaths: []string{"response.usage"},
		},
		{
			name:       "top-level usage and response.usage are both rewritten",
			body:       `{"usage":{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}},"response":{"usage":{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}}}`,
			usagePaths: []string{"usage", "response.usage"},
		},
		{
			name:       "hit rate at threshold leaves the body untouched",
			body:       `{"data":{"usage":{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":100}}},"success":true}`,
			usagePaths: []string{"data.usage"},
			unchanged:  true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := RewriteOpenAIUsageJSON([]byte(tt.body), 0.05)
			if tt.unchanged {
				require.Equal(t, tt.body, string(out))
				return
			}
			for _, usagePath := range tt.usagePaths {
				require.Equal(t, int64(750), gjson.GetBytes(out, usagePath+".input_tokens_details.cached_tokens").Int()+gjson.GetBytes(out, usagePath+".prompt_tokens_details.cached_tokens").Int(), usagePath)
				require.Equal(t, int64(1000), gjson.GetBytes(out, usagePath+".input_tokens").Int()+gjson.GetBytes(out, usagePath+".prompt_tokens").Int(), usagePath)
			}
			require.Equal(t, tt.body, string(RewriteOpenAIUsageJSON([]byte(tt.body), 0)))
		})
	}
}

// cachedUsageRoot returns the path of the usage object holding cachedPath.
func cachedUsageRoot(cachedPath string) string {
	if strings.HasPrefix(cachedPath, "usage.") {
		return "usage"
	}
	return cachedPath[:strings.Index(cachedPath, ".usage")+len(".usage")]
}

func TestRewriteOpenAIUsageJSONWithRule_FollowsBillingParserOfEachRoute(t *testing.T) {
	rules := []struct {
		name string
		rule OpenAIUsageRule
	}{
		{name: "default", rule: OpenAIUsageRuleDefault},
		{name: "ws passthrough", rule: OpenAIUsageRuleWSPassthrough},
		{name: "embeddings", rule: OpenAIUsageRuleEmbeddings},
	}
	for _, tc := range []struct {
		name string
		body string
		// want maps a cached_tokens path to the value left under the default, WS passthrough and embeddings rules.
		want map[string][3]int64
	}{
		{
			name: "usage and response.usage carry the same counts",
			body: `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}},"response":{"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"input_tokens_details":{"cached_tokens":800}}}}`,
			want: map[string][3]int64{
				"usage.prompt_tokens_details.cached_tokens":         {750, 750, 750},
				"response.usage.input_tokens_details.cached_tokens": {750, 750, 800},
			},
		},
		{
			name: "usage and response.usage carry different counts",
			body: `{"usage":{"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}},"response":{"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1600}}}}`,
			want: map[string][3]int64{
				"usage.prompt_tokens_details.cached_tokens":         {750, 750, 750},
				"response.usage.input_tokens_details.cached_tokens": {1500, 1500, 1600},
			},
		},
		{
			name: "only response.usage",
			body: `{"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}}`,
			want: map[string][3]int64{"response.usage.input_tokens_details.cached_tokens": {750, 750, 800}},
		},
		{
			name: "only usage",
			body: `{"id":"r1","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}`,
			want: map[string][3]int64{"usage.input_tokens_details.cached_tokens": {750, 750, 750}},
		},
		{
			name: "usage wrapped in data",
			body: `{"data":{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}},"success":true}`,
			want: map[string][3]int64{"data.usage.input_tokens_details.cached_tokens": {750, 800, 800}},
		},
		{
			name: "usage wrapped in data.response",
			body: `{"data":{"response":{"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}}}`,
			want: map[string][3]int64{"data.response.usage.input_tokens_details.cached_tokens": {750, 800, 800}},
		},
		{
			name: "input_tokens is 0 beside prompt_tokens",
			body: `{"usage":{"input_tokens":0,"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}}`,
			want: map[string][3]int64{"usage.prompt_tokens_details.cached_tokens": {750, 800, 750}},
		},
		{
			name: "input_tokens is 0 beside prompt_tokens in response.usage",
			body: `{"response":{"usage":{"input_tokens":0,"prompt_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}}}`,
			want: map[string][3]int64{"response.usage.prompt_tokens_details.cached_tokens": {750, 800, 800}},
		},
		{
			name: "prompt_tokens is 0 beside input_tokens",
			body: `{"usage":{"prompt_tokens":0,"input_tokens":1000,"input_tokens_details":{"cached_tokens":800}}}`,
			want: map[string][3]int64{"usage.input_tokens_details.cached_tokens": {750, 750, 750}},
		},
		{
			name: "only the cached_tokens alias",
			body: `{"usage":{"input_tokens":1000,"cached_tokens":800}}`,
			want: map[string][3]int64{"usage.cached_tokens": {750, 800, 750}},
		},
		{
			name: "only the cached_tokens alias in response.usage",
			body: `{"response":{"usage":{"input_tokens":1000,"cached_tokens":800}}}`,
			want: map[string][3]int64{"response.usage.cached_tokens": {750, 800, 800}},
		},
		{
			name: "prompt_tokens differs from input_tokens",
			body: `{"usage":{"prompt_tokens":1000,"input_tokens":2000,"prompt_tokens_details":{"cached_tokens":800}}}`,
			want: map[string][3]int64{"usage.prompt_tokens_details.cached_tokens": {700, 700, 750}},
		},
		{
			name: "only total_tokens",
			body: `{"usage":{"total_tokens":1000,"prompt_tokens_details":{"cached_tokens":800}}}`,
			want: map[string][3]int64{"usage.prompt_tokens_details.cached_tokens": {800, 800, 750}},
		},
	} {
		for i, rule := range rules {
			t.Run(tc.name+" under "+rule.name, func(t *testing.T) {
				out := string(RewriteOpenAIUsageJSONWithRule([]byte(tc.body), rule.rule, 0.05))
				untouched := true
				for cachedPath, want := range tc.want {
					require.Equal(t, want[i], gjson.Get(out, cachedPath).Int(), cachedPath)
					untouched = untouched && want[i] == gjson.Get(tc.body, cachedPath).Int()
					root := cachedUsageRoot(cachedPath)
					for _, total := range []string{"input_tokens", "prompt_tokens", "total_tokens"} {
						require.Equal(t, gjson.Get(tc.body, root+"."+total).Raw, gjson.Get(out, root+"."+total).Raw, root+"."+total)
					}
				}
				if untouched {
					require.Equal(t, tc.body, out)
				}
			})
		}
	}
}

func TestRewriteOpenAIUsageJSON_ZeroInputTokensFallsBackToPromptTokens(t *testing.T) {
	for _, tt := range []struct {
		name  string
		usage string
	}{
		{name: "input_tokens is 0", usage: `"input_tokens":0,`},
		{name: "input_tokens is null", usage: `"input_tokens":null,`},
		{name: "input_tokens is absent", usage: ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"id":"c1","usage":{` + tt.usage + `"prompt_tokens":1000,"completion_tokens":100,"total_tokens":1100,"prompt_tokens_details":{"cached_tokens":800}}}`
			out := RewriteOpenAIUsageJSON([]byte(body), 0.05)
			require.Equal(t, int64(750), gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int())
			require.Equal(t, int64(1000), gjson.GetBytes(out, "usage.prompt_tokens").Int())
			require.Equal(t, int64(100), gjson.GetBytes(out, "usage.completion_tokens").Int())
		})
	}
}
