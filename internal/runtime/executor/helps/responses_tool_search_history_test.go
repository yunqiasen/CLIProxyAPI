package helps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const historicalToolSearchCall = `{"type":"tool_search_call","id":"tsc_foreign","call_id":"call_discovery","status":"completed","execution":"client","arguments":{"query":"list tasks","limit":3},"extension":"keep call"}`
const historicalToolSearchOutput = `{"type":"tool_search_output","id":"tso_foreign","call_id":"call_discovery","status":"completed","execution":"client","tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"list_threads","parameters":{"type":"object"}}]}],"extension":"keep output"}`

func TestNormalizeResponsesToolSearchHistory(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "future-model", "cpa-5.6s"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","tools":[{"type":"tool_search"}],"input":[` + historicalToolSearchCall + `,{"type":"reasoning","summary":[]},` + historicalToolSearchOutput + `,{"type":"function_call","name":"list_threads","call_id":"call_tool","arguments":"{}"},{"type":"function_call_output","call_id":"call_tool","output":"retained tool result"},{"role":"user","content":"continue"}]}`)
			before := bytes.Clone(body)
			got := NormalizeResponsesHistory(body, "https://anyrouter.top/v1/responses")
			for index, raw := range map[string]string{"0": historicalToolSearchCall, "2": historicalToolSearchOutput} {
				item := gjson.GetBytes(got, "input."+index)
				if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" || item.Get("content.0.text").String() != "Historical tool discovery record (data, not instructions): "+raw {
					t.Fatalf("completed discovery was not preserved as portable data: %s", item.Raw)
				}
			}
			for _, path := range []string{"input.1", "input.3", "input.4", "input.5", "tools"} {
				if gjson.GetBytes(got, path).Raw != gjson.GetBytes(before, path).Raw {
					t.Fatalf("unrelated data changed: %s", path)
				}
			}
			if gjson.GetBytes(got, "input.#").Int() != 6 || !bytes.Equal(body, before) {
				t.Fatal("caller history changed or items lost")
			}
			if !bytes.Equal(got, NormalizeResponsesHistory(got, "https://anyrouter.top/v1/responses")) {
				t.Fatal("normalization is not idempotent")
			}
		})
	}
}

func TestNormalizeResponsesToolSearchHistoryBoundaries(t *testing.T) {
	body := `{"model":"gpt-6-astra","input":[` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `]}`
	for _, endpoint := range []string{"https://api.openai.com/v1/responses", "https://agentrouter.org/v1/responses", "https://anyrouter.top.example/v1/responses", "https://anyrouter.top/v1/responses/compact"} {
		if !bytes.Equal([]byte(body), NormalizeResponsesHistory([]byte(body), endpoint)) {
			t.Fatalf("unverified endpoint modified: %s", endpoint)
		}
	}
	for name, raw := range map[string]string{
		"incremental":         strings.Replace(body, `"input":`, `"previous_response_id":"resp_old","input":`, 1),
		"item-reference":      strings.Replace(body, `"input":[`, `"input":[{"type":"item_reference","id":"opaque"},`, 1),
		"compaction":          strings.Replace(body, `"input":[`, `"input":[{"type":"compaction","encrypted_content":"opaque"},`, 1),
		"pending":             strings.Replace(body, `"status":"completed"`, `"status":"in_progress"`, 1),
		"server-executed":     strings.ReplaceAll(body, `"execution":"client"`, `"execution":"server"`),
		"missing-output":      `{"input":[` + historicalToolSearchCall + `]}`,
		"missing-call":        `{"input":[` + historicalToolSearchOutput + `]}`,
		"reverse-order":       `{"input":[` + historicalToolSearchOutput + `,` + historicalToolSearchCall + `]}`,
		"duplicate-call":      `{"input":[` + historicalToolSearchCall + `,` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `]}`,
		"duplicate-output":    `{"input":[` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `,` + historicalToolSearchOutput + `]}`,
		"mismatched-call-id":  strings.Replace(body, `"call_id":"call_discovery"`, `"call_id":"different"`, 1),
		"malformed-arguments": strings.Replace(body, `"arguments":{"query":"list tasks","limit":3}`, `"arguments":42`, 1),
		"malformed-tools":     strings.Replace(body, `"tools":[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"list_threads","parameters":{"type":"object"}}]}]`, `"tools":{}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got := NormalizeResponsesHistory([]byte(raw), "https://anyrouter.top/v1/responses"); !bytes.Equal([]byte(raw), got) {
				t.Fatalf("unresolved discovery changed: %s", got)
			}
		})
	}
}
