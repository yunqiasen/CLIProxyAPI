package test

import (
	"bytes"
	"context"
	"encoding/json"
	claude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	gemini "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/openai/responses"
	chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestApplyPatchBridgeDefinitionsAndRoundTrip(t *testing.T) {
	raw := []byte(`{"model":"fixture","input":[{"type":"custom_tool_call","name":"apply_patch","call_id":"call_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","call_id":"call_patch","output":"ok"}],"tools":[{"type":"custom","name":"apply_patch","description":"Edit files"}]}`)
	t.Run("claude", func(t *testing.T) {
		body := claude.ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		if !gjson.GetBytes(body, `tools.#(name=="apply_patch")`).Exists() {
			t.Fatalf("patch declaration missing: %s", body)
		}
	})
	t.Run("gemini", func(t *testing.T) {
		body := gemini.ConvertOpenAIResponsesRequestToGemini("gemini-test", raw, false)
		if gjson.GetBytes(body, "tools.0.functionDeclarations.0.name").String() != "apply_patch" {
			t.Fatalf("patch declaration missing: %s", body)
		}
		response := gemini.ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-test", raw, body, []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"apply_patch","args":{"input":"*** Begin Patch\n*** End Patch"}}}]},"finishReason":"STOP"}]}`), nil)
		if gjson.GetBytes(response, `output.#(type=="custom_tool_call").input`).String() != "*** Begin Patch\n*** End Patch" {
			t.Fatalf("patch response not restored: %s", response)
		}
	})
}

func TestApplyPatchStreamingExchange(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: 文件.txt\n+\"quoted\" \\ end\n*** End Patch"
	for _, backend := range []string{"claude", "gemini", "chat"} {
		for _, namespace := range []string{"", "functions"} {
			t.Run(backend+"/"+namespace, func(t *testing.T) {
				tool := map[string]any{"type": "custom", "name": "apply_patch"}
				var declaration any = tool
				if namespace != "" {
					declaration = map[string]any{"type": "namespace", "name": namespace, "tools": []any{tool}}
				}
				raw, _ := json.Marshal(map[string]any{"model": "fixture", "tools": []any{declaration}})
				args, _ := json.Marshal(map[string]string{"input": patch})
				name := "apply_patch"
				if namespace != "" {
					name = namespace + "__apply_patch"
				}
				var convert func(context.Context, string, []byte, []byte, []byte, *any) [][]byte
				var request func(string, []byte, bool) []byte
				var chunks [][]byte
				switch backend {
				case "claude":
					convert = claude.ConvertClaudeResponseToOpenAIResponses
					request = claude.ConvertOpenAIResponsesRequestToClaude
					delta, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": string(args)}})
					chunks = [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_patch","usage":{"input_tokens":1}}}`), []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_patch","name":"` + name + `","input":{}}}`), append([]byte("data: "), delta...), []byte(`data: {"type":"content_block_stop","index":0}`), []byte(`data: {"type":"message_stop"}`)}
				case "gemini":
					convert = gemini.ConvertGeminiResponseToOpenAIResponses
					request = gemini.ConvertOpenAIResponsesRequestToGemini
					if namespace != "" {
						name = namespace + ".apply_patch"
					}
					chunks = [][]byte{[]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + name + `","args":` + string(args) + `}}]},"finishReason":"STOP"}]}`)}
				case "chat":
					convert = chat.ConvertOpenAIChatCompletionsResponseToOpenAIResponses
					request = chat.ConvertOpenAIResponsesRequestToOpenAIChatCompletions
					if namespace != "" {
						name = namespace + "__apply_patch"
					}
					chunk, _ := json.Marshal(map[string]any{"id": "chat-patch", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_patch", "type": "function", "function": map[string]string{"name": name, "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
					chunks = [][]byte{append([]byte("data: "), chunk...), []byte("data: [DONE]")}
				}
				body := request("fixture", raw, true)
				if !strings.Contains(string(body), "apply_patch") {
					t.Fatalf("missing definition: %s", body)
				}
				var param any
				var completed gjson.Result
				deltas := map[string]string{}
				done := map[string]string{}
				lastSequence := int64(-1)
				for _, chunk := range chunks {
					for _, event := range convert(context.Background(), "fixture", raw, body, chunk, &param) {
						i := bytes.Index(event, []byte("data: "))
						if i < 0 {
							continue
						}
						d := gjson.ParseBytes(bytes.TrimSpace(event[i+6:]))
						kind := d.Get("type").String()
						if seq := d.Get("sequence_number"); seq.Exists() {
							if seq.Int() <= lastSequence {
								t.Fatalf("sequence not increasing: %s", event)
							}
							lastSequence = seq.Int()
						}
						switch kind {
						case "response.function_call_arguments.delta", "response.function_call_arguments.done":
							t.Fatalf("leaked function event: %s", event)
						case "response.custom_tool_call_input.delta":
							deltas[d.Get("item_id").String()] += d.Get("delta").String()
						case "response.custom_tool_call_input.done":
							done[d.Get("item_id").String()] = d.Get("input").String()
						case "response.completed":
							completed = d.Get("response")
						}
					}
				}
				item := completed.Get(`output.#(type=="custom_tool_call")`)
				if item.Get("input").String() != patch || item.Get("name").String() != "apply_patch" || item.Get("namespace").String() != namespace {
					t.Fatalf("bad completed: %s", completed.Raw)
				}
				id := item.Get("id").String()
				if deltas[id] != patch || done[id] != patch {
					t.Fatalf("delta/done mismatch: %q / %q", deltas[id], done[id])
				}
				replay, _ := json.Marshal(map[string]any{"model": "fixture", "tools": []any{declaration}, "input": []any{json.RawMessage(item.Raw), map[string]any{"type": "custom_tool_call_output", "call_id": item.Get("call_id").String(), "output": "PATCH_OK"}}})
				forwarded := request("fixture", replay, false)
				if !strings.Contains(string(forwarded), "PATCH_OK") || !strings.Contains(string(forwarded), "Begin Patch") {
					t.Fatalf("tool exchange lost: %s", forwarded)
				}
			})
		}
	}
}

func TestGeminiParallelPatchAndFunctionCalls(t *testing.T) {
	raw := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	upstream := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"apply_patch","args":{"input":"PATCH_ONE"}}},{"functionCall":{"name":"lookup","args":{"query":"hello"}}},{"functionCall":{"name":"apply_patch","args":{"input":"PATCH_TWO"}}}]},"finishReason":"STOP"}]}`)
	var state any
	var outputs gjson.Result
	ids := map[string]bool{}
	var deltaCount, functionCount int
	for _, event := range gemini.ConvertGeminiResponseToOpenAIResponses(context.Background(), "fixture", raw, nil, upstream, &state) {
		i := bytes.Index(event, []byte("data: "))
		if i < 0 {
			continue
		}
		d := gjson.ParseBytes(bytes.TrimSpace(event[i+6:]))
		switch d.Get("type").String() {
		case "response.completed":
			outputs = d.Get("response.output")
		case "response.custom_tool_call_input.delta":
			deltaCount++
		case "response.function_call_arguments.delta":
			functionCount++
		}
	}
	if len(outputs.Array()) != 3 || deltaCount != 2 || functionCount != 1 {
		t.Fatalf("parallel events lost: %s delta=%d function=%d", outputs.Raw, deltaCount, functionCount)
	}
	for _, item := range outputs.Array() {
		id := item.Get("call_id").String()
		if id == "" || ids[id] {
			t.Fatalf("call identity collision: %s", outputs.Raw)
		}
		ids[id] = true
	}
	if outputs.Array()[0].Get("input").String() != "PATCH_ONE" || outputs.Array()[2].Get("input").String() != "PATCH_TWO" {
		t.Fatalf("patch calls crossed: %s", outputs.Raw)
	}
}
