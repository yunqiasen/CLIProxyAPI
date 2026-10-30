package helps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesActiveToolSearchKeepsLoadedToolsCallable(t *testing.T) {
	for _, endpoint := range []string{"https://anyrouter.top/v1/responses", "https://agentrouter.org/v1/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"tool_search","execution":"client"}],"input":[{"role":"user","content":"Find and use the task tool."},` + historicalToolSearchCall + `,` + strings.Replace(historicalToolSearchOutput, `"parameters":`, `"defer_loading":true,"parameters":`, 1) + `]}`)
			before := bytes.Clone(body)
			got := NormalizeResponsesHistory(body, endpoint)
			tool := gjson.GetBytes(got, "tools.1")
			if tool.Get("type").String() != "namespace" || tool.Get("name").String() != "codex_app" || tool.Get("tools.0.name").String() != "list_threads" || tool.Get("tools.0.defer_loading").Bool() {
				t.Fatalf("discovered tool is only prose, not callable: %s", got)
			}
			if !bytes.Equal(body, before) {
				t.Fatal("caller payload mutated")
			}
			if !bytes.Equal(got, NormalizeResponsesHistory(got, endpoint)) {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestResponsesActiveToolSearchDoesNotReviveHistoricalCatalog(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"tool_search"}],"input":[` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `,{"role":"user","content":"Start a different task."}]}`)
	got := NormalizeResponsesHistory(body, "https://agentrouter.org/v1/responses")
	if gjson.GetBytes(got, "tools").Raw != gjson.GetBytes(body, "tools").Raw {
		t.Fatal("historical tools activated")
	}
}

func TestResponsesActiveToolSearchRetryKeepsCallableCatalog(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"Find a tool."},{"type":"reasoning","id":"rs_foreign","encrypted_content":"foreign"},` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `]}`)
	rejection := []byte(`{"error":{"code":"invalid_encrypted_content","message":"bad state"}}`)
	got, retry := PortableResponsesSignatureRetry(body, rejection, "https://example.test/v1/responses")
	if !retry || gjson.GetBytes(got, "tools.0.tools.0.name").String() != "list_threads" {
		t.Fatalf("retry lost callable discovery: %s", got)
	}
}

func TestResponsesActiveToolSearchKeepsCurrentDefinitions(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","tools":[{"type":"namespace","name":"codex_app","description":"current namespace","tools":[{"type":"function","name":"list_threads","description":"current definition","defer_loading":true,"parameters":{"type":"object","required":["current"]}},{"type":"function","name":"keep_me"}]}],"input":[{"role":"user","content":"Search."},` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `]}`)
	got := NormalizeResponsesHistory(body, "https://anyrouter.top/v1/responses")
	if gjson.GetBytes(got, "tools.#").Int() != 1 || gjson.GetBytes(got, "tools.0.tools.#").Int() != 2 || gjson.GetBytes(got, "tools.0.tools.0.description").String() != "current definition" || gjson.GetBytes(got, "tools.0.tools.0.defer_loading").Bool() {
		t.Fatalf("current definitions lost or left deferred: %s", got)
	}
}

func TestResponsesActiveToolSearchBoundaries(t *testing.T) {
	base := `{"model":"gpt-6-astra","input":[{"role":"user","content":"Search."},` + historicalToolSearchCall + `,` + historicalToolSearchOutput + `]}`
	for name, body := range map[string]string{
		"unpaired":        strings.Replace(base, `"call_id":"call_discovery"`, `"call_id":"other"`, 1),
		"server":          strings.ReplaceAll(base, `"execution":"client"`, `"execution":"server"`),
		"pending":         strings.Replace(base, `"status":"completed"`, `"status":"in_progress"`, 1),
		"cross_turn":      strings.Replace(base, `,`+historicalToolSearchOutput, `,{"role":"user","content":"new task"},`+historicalToolSearchOutput, 1),
		"reference":       strings.Replace(base, `"input":[`, `"input":[{"type":"item_reference","id":"required"},`, 1),
		"compaction":      strings.Replace(base, `"input":[`, `"input":[{"type":"compaction","encrypted_content":"required"},`, 1),
		"hosted_metadata": strings.Replace(base, `"type":"namespace"`, `"type":"mcp"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			got := NormalizeResponsesHistory([]byte(body), "https://agentrouter.org/v1/responses")
			if gjson.GetBytes(got, "tools.#").Int() != 0 {
				t.Fatalf("unverified catalog activated: %s", got)
			}
		})
	}
}

func TestResponsesActiveToolSearchUsesLatestDiscovery(t *testing.T) {
	output := strings.Replace(historicalToolSearchOutput, `"type":"object"`, `"type":"object","description":"old"`, 1)
	newerCall := strings.ReplaceAll(historicalToolSearchCall, "call_discovery", "call_new")
	newerOutput := strings.ReplaceAll(strings.ReplaceAll(output, "call_discovery", "call_new"), `"description":"old"`, `"description":"new"`)
	body := []byte(`{"model":"gpt-6-astra","input":[` + historicalToolSearchCall + `,` + output + `,` + newerCall + `,` + newerOutput + `]}`)
	got := NormalizeResponsesHistory(body, "https://anyrouter.top/v1/responses")
	if gjson.GetBytes(got, "tools.0.tools.0.parameters.description").String() != "new" || gjson.GetBytes(got, "tools.0.tools.#").Int() != 1 {
		t.Fatalf("old definition won: %s", got)
	}
}
