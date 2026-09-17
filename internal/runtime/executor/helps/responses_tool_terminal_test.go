package helps

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/tidwall/gjson"
)

const agentForcedToolRequest = `{"model":"gpt-6-astra","store":false,"tools":[{"type":"function","name":"echo"}],"tool_choice":{"type":"function","name":"echo"}}`

func agentToolFrames(index int, id, callID string) []map[string]any {
	return []map[string]any{
		{"type": "response.output_item.added", "output_index": index, "item": map[string]any{"id": id, "call_id": callID, "type": "function_call", "name": "echo", "arguments": "", "status": "in_progress"}},
		{"type": "response.function_call_arguments.delta", "output_index": index, "item_id": id, "delta": `{"text":`},
		{"type": "response.function_call_arguments.delta", "output_index": index, "item_id": id, "delta": `"OK"}`},
		{"type": "response.function_call_arguments.done", "output_index": index, "item_id": id, "arguments": `{"text":"OK"}`},
		{"type": "response.output_item.done", "output_index": index, "item": map[string]any{"id": id, "call_id": callID, "type": "function_call", "name": "echo", "arguments": `{"text":"OK"}`, "status": "completed"}},
	}
}

func encodeAgentToolFrames(t *testing.T, frames []map[string]any) string {
	t.Helper()
	var out strings.Builder
	for i, frame := range frames {
		frame["sequence_number"] = i
		data, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString("data: " + string(data) + "\n\n")
	}
	return out.String()
}

func collectAgentToolFrames(t *testing.T, reader *CodexResponsesSSEReader) ([]gjson.Result, error) {
	t.Helper()
	var events []gjson.Result
	for {
		frame, err := reader.Next()
		if err != nil {
			return events, err
		}
		if json.Valid(frame.Data) {
			events = append(events, gjson.ParseBytes(frame.Data))
		}
	}
}

func completedAgentEvents(events []gjson.Result) []gjson.Result {
	var completed []gjson.Result
	for _, event := range events {
		if event.Get("type").String() == "response.completed" {
			completed = append(completed, event)
		}
	}
	return completed
}

func TestAgentToolTerminalPreservesParallelCallsAndLifecycle(t *testing.T) {
	for _, nativeLifecycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "generated_identity", true: "native_identity"}[nativeLifecycle], func(t *testing.T) {
			a, b := agentToolFrames(0, "fc_a", "call_a"), agentToolFrames(1, "fc_b", "call_b")
			var frames []map[string]any
			if nativeLifecycle {
				frames = append(frames, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_native", "model": "gpt-6-astra", "created_at": 42, "status": "in_progress", "usage": nil, "output": []any{}}})
			}
			for i := range a {
				frames = append(frames, a[i], b[i])
			}
			raw := encodeAgentToolFrames(t, frames)
			reader := NewCodexResponsesSSEReader(context.Background(), iotest.OneByteReader(strings.NewReader(raw)), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest))
			var rawLog strings.Builder
			var events []gjson.Result
			for {
				frame, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				rawLog.Write(frame.Raw)
				events = append(events, gjson.ParseBytes(frame.Data))
			}
			if rawLog.String() != raw {
				t.Fatal("compatibility events contaminated upstream raw logs")
			}
			complete := completedAgentEvents(events)
			if len(complete) != 1 || complete[0].Get("response.output.#").Int() != 2 {
				t.Fatalf("completed calls lost: %v", events)
			}
			id := events[0].Get("response.id").String()
			if id == "" || complete[0].Get("response.id").String() != id || (nativeLifecycle && id != "resp_native") {
				t.Fatal("response identity changed")
			}
			if complete[0].Get("response.output.0.call_id").String() != "call_a" || complete[0].Get("response.output.1.call_id").String() != "call_b" || complete[0].Get("response.usage").Type != gjson.Null {
				t.Fatal("output ordering or usage changed")
			}
			last := int64(-1)
			for _, event := range events {
				seq := event.Get("sequence_number").Int()
				if seq <= last {
					t.Fatal("sequence numbers are not increasing")
				}
				last = seq
			}
		})
	}
}

func TestAgentToolTerminalRejectsIncompleteOrInvalidCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]map[string]any) []map[string]any
	}{
		{"only_added", func(f []map[string]any) []map[string]any { return f[:1] }},
		{"missing_argument_done", func(f []map[string]any) []map[string]any { return append(f[:3], f[4]) }},
		{"missing_item_done", func(f []map[string]any) []map[string]any { return f[:4] }},
		{"missing_delta", func(f []map[string]any) []map[string]any { return append(f[:1], f[2:]...) }},
		{"invalid_json_arguments", func(f []map[string]any) []map[string]any { f[3]["arguments"] = "{"; return f }},
		{"wrong_item_id", func(f []map[string]any) []map[string]any { f[3]["item_id"] = "fc_other"; return f }},
		{"wrong_call_id", func(f []map[string]any) []map[string]any {
			f[4]["item"].(map[string]any)["call_id"] = "call_other"
			return f
		}},
		{"wrong_function", func(f []map[string]any) []map[string]any { f[4]["item"].(map[string]any)["name"] = "other"; return f }},
		{"arguments_disagree", func(f []map[string]any) []map[string]any {
			f[4]["item"].(map[string]any)["arguments"] = `{"text":"different"}`
			return f
		}},
		{"incomplete_status", func(f []map[string]any) []map[string]any {
			f[4]["item"].(map[string]any)["status"] = "incomplete"
			return f
		}},
		{"status_missing", func(f []map[string]any) []map[string]any { delete(f[4]["item"].(map[string]any), "status"); return f }},
		{"index_missing", func(f []map[string]any) []map[string]any { delete(f[4], "output_index"); return f }},
		{"index_fraction", func(f []map[string]any) []map[string]any { f[4]["output_index"] = .5; return f }},
		{"index_gap", func(f []map[string]any) []map[string]any { return agentToolFrames(1, "fc_a", "call_a") }},
		{"dangling_parallel_call", func(f []map[string]any) []map[string]any { return append(f, agentToolFrames(1, "fc_b", "call_b")[0]) }},
		{"duplicate_call", func(f []map[string]any) []map[string]any { return append(f, agentToolFrames(1, "fc_b", "call_a")...) }},
		{"extra_delta_after_done", func(f []map[string]any) []map[string]any { return append(f, f[2]) }},
		{"text_output", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"type": "response.output_text.delta", "delta": "unfinished"})
		}},
		{"opaque_compaction", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "compaction", "encrypted_content": "opaque"}})
		}},
		{"stream_error", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"type": "error", "error": map[string]any{"message": "rate limit"}})
		}},
		{"untyped_error", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"error": map[string]any{"message": "rate limit"}})
		}},
		{"response_failed", func(f []map[string]any) []map[string]any { return append(f, map[string]any{"type": "response.failed"}) }},
		{"response_incomplete", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"type": "response.incomplete"})
		}},
		{"response_cancelled", func(f []map[string]any) []map[string]any {
			return append(f, map[string]any{"type": "response.cancelled"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := encodeAgentToolFrames(t, tc.edit(agentToolFrames(0, "fc_a", "call_a")))
			r := NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest))
			events, err := collectAgentToolFrames(t, r)
			if err != io.EOF || len(completedAgentEvents(events)) != 0 {
				t.Fatalf("invalid stream became success: err=%v events=%v", err, events)
			}
		})
	}
}

func TestAgentToolTerminalPreservesRealTerminal(t *testing.T) {
	for _, nativeLifecycle := range []bool{false, true} {
		for _, status := range []string{"completed", "failed", "incomplete"} {
			name := map[bool]string{false: "generated_identity", true: "native_identity"}[nativeLifecycle]
			t.Run(name+"/"+status, func(t *testing.T) {
				frames := agentToolFrames(0, "fc_a", "call_a")
				if nativeLifecycle {
					frames = append([]map[string]any{{"type": "response.created", "response": map[string]any{"id": "resp_native", "status": "in_progress", "output": []any{}}}}, frames...)
				}
				response := map[string]any{"id": "resp_native", "status": status, "output": []any{frames[len(frames)-1]["item"]}, "usage": map[string]any{"input_tokens": 17, "output_tokens": 9}}
				if status == "failed" {
					response["error"] = map[string]any{"code": "rate_limit_exceeded", "message": "native error"}
				}
				if status == "incomplete" {
					response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				}
				frames = append(frames, map[string]any{"type": "response." + status, "response": response})
				raw := encodeAgentToolFrames(t, frames)
				reader := NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest))
				var events []gjson.Result
				var rawLog strings.Builder
				for {
					frame, err := reader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					events = append(events, gjson.ParseBytes(frame.Data))
					rawLog.Write(frame.Raw)
				}
				if rawLog.String() != raw {
					t.Fatal("native response identity or bytes lost in raw log")
				}
				id := events[0].Get("response.id").String()
				if nativeLifecycle && id != "resp_native" || !nativeLifecycle && !strings.HasPrefix(id, "resp_cpa_") {
					t.Fatal("unexpected response identity")
				}
				if events[0].Get("response.status").String() != "in_progress" {
					t.Fatal("provisional lifecycle claimed a completed response")
				}
				lastSequence := int64(-1)
				for _, event := range events {
					if event.Get("sequence_number").Int() <= lastSequence {
						t.Fatal("non-increasing client sequence")
					}
					lastSequence = event.Get("sequence_number").Int()
					if responseID := event.Get("response.id"); responseID.Exists() && responseID.String() != id {
						t.Fatal("client response identity changed")
					}
				}
				terminal := events[len(events)-1]
				if terminal.Get("type").String() != "response."+status || terminal.Get("response.status").String() != status || terminal.Get("response.usage.input_tokens").Int() != 17 || terminal.Get("response.output.0.arguments").String() != `{"text":"OK"}` {
					t.Fatalf("native outcome, output or usage changed: %v", terminal)
				}
				if status == "failed" && terminal.Get("response.error.message").String() != "native error" || status == "incomplete" && terminal.Get("response.incomplete_details.reason").String() != "max_output_tokens" {
					t.Fatal("native failure details changed")
				}
				completed := completedAgentEvents(events)
				if status == "completed" && len(completed) != 1 || status != "completed" && len(completed) != 0 {
					t.Fatal("native terminal duplicated or changed to success")
				}
			})
		}
	}
}

func TestAgentToolTerminalScopeAndTransport(t *testing.T) {
	raw := encodeAgentToolFrames(t, agentToolFrames(0, "fc_a", "call_a"))
	for _, tc := range []struct{ name, endpoint, request string }{
		{"other_provider", "https://anyrouter.top/v1/responses", agentForcedToolRequest},
		{"lookalike_host", "https://agentrouter.org.example/v1/responses", agentForcedToolRequest},
		{"compact_endpoint", "https://agentrouter.org/v1/responses/compact", agentForcedToolRequest},
		{"other_model", "https://agentrouter.org/v1/responses", strings.ReplaceAll(agentForcedToolRequest, "gpt-6-astra", "gpt-5.6-sol")},
		{"stored_response", "https://agentrouter.org/v1/responses", strings.ReplaceAll(agentForcedToolRequest, `"store":false`, `"store":true`)},
		{"auto_tool", "https://agentrouter.org/v1/responses", strings.ReplaceAll(agentForcedToolRequest, `"tool_choice":{"type":"function","name":"echo"}`, `"tool_choice":"auto"`)},
		{"compact_input", "https://agentrouter.org/v1/responses", strings.TrimSuffix(agentForcedToolRequest, "}") + `,"input":[{"type":"compaction_trigger"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, err := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, tc.endpoint, []byte(tc.request)))
			if err != io.EOF || len(completedAgentEvents(events)) != 0 || len(events) != 5 {
				t.Fatal("compatibility escaped the affected scope")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		reader io.Reader
		limit  int
	}{
		{"transport_error", io.MultiReader(strings.NewReader(raw), iotest.ErrReader(io.ErrUnexpectedEOF)), 1 << 20},
		{"malformed_tail", strings.NewReader(raw + "data: {broken\n\n"), 1 << 20},
		{"empty_done", strings.NewReader("data: [DONE]\n\n"), 1 << 20},
		{"bounded_history", strings.NewReader(raw), len(raw) - 150},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, _ := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), tc.reader, tc.limit, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest)))
			if len(completedAgentEvents(events)) != 0 {
				t.Fatal("failed transport became success")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := NewCodexResponsesSSEReader(ctx, strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest))
	for i := 0; i < 6; i++ {
		if _, err := reader.Next(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if frame, err := reader.Next(); err != io.EOF || gjson.GetBytes(frame.Data, "type").String() == "response.completed" {
		t.Fatal("cancellation became success")
	}
}

func TestAgentToolTerminalRejectsContradictoryLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response map[string]any
	}{
		{"error_details", map[string]any{"id": "resp_native", "status": "in_progress", "output": []any{}, "error": map[string]any{"message": "failed"}}},
		{"incomplete_details", map[string]any{"id": "resp_native", "status": "in_progress", "output": []any{}, "incomplete_details": map[string]any{"reason": "max_output_tokens"}}},
		{"malformed_output", map[string]any{"id": "resp_native", "status": "in_progress", "output": "missing history"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := append([]map[string]any{{"type": "response.created", "response": tc.response}}, agentToolFrames(0, "fc_a", "call_a")...)
			raw := encodeAgentToolFrames(t, frames)
			events, _ := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest)))
			if len(completedAgentEvents(events)) != 0 {
				t.Fatal("contradictory lifecycle became success")
			}
		})
	}
	raw := encodeAgentToolFrames(t, agentToolFrames(0, "fc_a", "call_a")) + "event: response.failed\n\n"
	events, _ := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest)))
	if len(completedAgentEvents(events)) != 0 {
		t.Fatal("empty failure frame became success")
	}
}

func TestAgentToolTerminalRejectsConflictingSSEEvent(t *testing.T) {
	for _, failure := range []string{"response.failed", "response.incomplete", "error"} {
		t.Run(failure, func(t *testing.T) {
			raw := encodeAgentToolFrames(t, agentToolFrames(0, "fc_a", "call_a"))
			at := strings.LastIndex(raw, "data: ")
			raw = raw[:at] + "event: " + failure + "\n" + raw[at:]
			events, _ := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest)))
			if len(completedAgentEvents(events)) != 0 {
				t.Fatal("conflicting failure event became success")
			}
		})
		t.Run(failure+"_done_sentinel", func(t *testing.T) {
			raw := encodeAgentToolFrames(t, agentToolFrames(0, "fc_a", "call_a")) + "event: " + failure + "\ndata: [DONE]\n\n"
			events, _ := collectAgentToolFrames(t, NewCodexResponsesSSEReader(context.Background(), strings.NewReader(raw), 1<<20, "https://agentrouter.org/v1/responses", []byte(agentForcedToolRequest)))
			if len(completedAgentEvents(events)) != 0 {
				t.Fatal("failure event with done sentinel became success")
			}
		})
	}
}
