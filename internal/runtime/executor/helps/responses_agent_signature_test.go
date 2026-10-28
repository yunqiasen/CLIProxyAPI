package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAgentMaskedResourceRejectionPreservesPortableHistory(t *testing.T) {
	body := []byte(`{"input":[{"type":"message","id":"msg_foreign","role":"assistant","content":[{"type":"output_text","text":"Keep the earlier answer."}]},{"type":"message","id":"client_owned","role":"user","content":"Continue the same task."}]}`)
	original := bytes.Clone(body)
	rejection := []byte(`{"error":{"message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]","param":"","type":"invalid_request_error"}}`)
	repaired, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses")
	if !retry {
		t.Fatal("Agent's masked resource rejection did not enter same-request portable recovery")
	}
	if gjson.GetBytes(repaired, "input.0.id").Exists() ||
		gjson.GetBytes(repaired, "input.0.content.0.text").String() != "Keep the earlier answer." ||
		gjson.GetBytes(repaired, "input.1.id").String() != "client_owned" ||
		gjson.GetBytes(repaired, "input.1.content").String() != "Continue the same task." {
		t.Fatalf("resource repair changed portable history: %s", repaired)
	}
	if !bytes.Equal(body, original) {
		t.Fatal("resource repair mutated the caller's history")
	}
}

func TestAgentMaskedResourceRejectionGuards(t *testing.T) {
	rejection := []byte(`{"error":{"message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]","param":"","type":"invalid_request_error"}}`)
	testAgentResourceRejectionGuards(t, rejection)
}

func testAgentResourceRejectionGuards(t *testing.T, rejection []byte) {
	t.Helper()
	body := []byte(`{"input":[{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep history."},{"role":"user","content":"Continue."}]}`)
	endpoint := "https://agentrouter.org/v1/responses"
	for _, tc := range []struct{ name, old, replacement string }{
		{"quoted_message", "OpenAI Responses bad request:", "A tool quoted:"},
		{"unrelated_resource", "*** OpenAI", "Unknown OpenAI"},
		{"different_mask", "*** OpenAI", "**** OpenAI"},
		{"rate_limit_type", "invalid_request_error", "rate_limit_error"},
		{"explicit_other_code", `"param":""`, `"param":"","code":"different_error"`},
		{"unrelated_parameter", `"param":""`, `"param":"tools"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := bytes.Replace(rejection, []byte(tc.old), []byte(tc.replacement), 1)
			got, retry := PortableResponsesSignatureRetry(body, invalid, endpoint)
			if retry || !bytes.Equal(got, body) {
				t.Fatal("unrelated rejection changed history")
			}
		})
	}
	for _, route := range []string{"https://example.test/v1/responses", "https://agentrouter.org.example/v1/responses", "https://agentrouter.org/v1/responses/compact"} {
		if got, retry := PortableResponsesSignatureRetry(body, rejection, route); retry || !bytes.Equal(got, body) {
			t.Errorf("unrelated route changed history: %s", route)
		}
	}
	for _, history := range []string{
		`{"previous_response_id":"resp_old","input":[{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep history."},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction","encrypted_content":"only history"},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"item_reference","id":"stored"},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"only history"}]}`,
		`{"input":[{"type":"message","id":"client_owned","role":"user","content":"Continue."}]}`,
	} {
		got, retry := PortableResponsesSignatureRetry([]byte(history), rejection, endpoint)
		if retry || string(got) != history {
			t.Fatal("nonportable or client-owned state changed")
		}
	}
}

func TestAgentRequestIDRejectionPreservesPortableHistory(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"Keep summary."}]},{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep answer."},{"type":"function_call","id":"fc_foreign","name":"lookup","call_id":"call_keep","arguments":"{}"},{"type":"function_call_output","id":"fco_foreign","call_id":"call_keep","output":"Keep result."},{"type":"message","id":"client_owned","role":"user","content":"Continue."}]}`)
	original := bytes.Clone(body)
	for _, tc := range []struct{ name, message string }{
		{"azure", "The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it."},
		{"masked", "The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it."},
		{"encrypted", "The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed."},
	} {
		for _, prefix := range []string{"", "OpenAI Responses bad request: "} {
			for suffixIndex, suffix := range []string{" (request id: 20261021073158346501676MSvAiaYZ)", " (request id: fixture-123_abc) [trace_id=fixture]"} {
				for _, nested := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/prefixed_%t/suffix_%d/nested_%t", tc.name, prefix != "", suffixIndex, nested), func(t *testing.T) {
						envelope := map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": nil, "param": "", "message": prefix + tc.message + suffix}}
						if nested {
							envelope = map[string]any{"type": "response.failed", "response": envelope}
						}
						rejection, err := json.Marshal(envelope)
						if err != nil {
							t.Fatal(err)
						}
						beforeRejection := bytes.Clone(rejection)
						repaired, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses")
						if !retry {
							t.Fatal("Agent request-id metadata prevented portable recovery")
						}
						if gjson.GetBytes(repaired, "model").String() != "gpt-6.1-sol" || gjson.GetBytes(repaired, "input.#").Int() != 5 {
							t.Fatal("model or portable history count changed")
						}
						for _, path := range []string{"input.0.id", "input.0.encrypted_content", "input.1.id", "input.2.id", "input.3.id"} {
							if gjson.GetBytes(repaired, path).Exists() {
								t.Errorf("rejected binding retained: %s", path)
							}
						}
						for path, want := range map[string]string{"input.0.summary.0.text": "Keep summary.", "input.1.content": "Keep answer.", "input.2.call_id": "call_keep", "input.2.arguments": "{}", "input.3.call_id": "call_keep", "input.3.output": "Keep result.", "input.4.id": "client_owned", "input.4.content": "Continue."} {
							if got := gjson.GetBytes(repaired, path).String(); got != want {
								t.Errorf("%s=%q want=%q", path, got, want)
							}
						}
						if !bytes.Equal(body, original) || !bytes.Equal(rejection, beforeRejection) {
							t.Fatal("caller history or original error mutated")
						}
						if _, again := PortableResponsesSignatureRetry(repaired, rejection, "https://agentrouter.org/v1/responses"); again {
							t.Fatal("already-portable history retried again")
						}
					})
				}
			}
		}
	}
}

func TestAgentRequestIDRejectionGuards(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque"},{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep history."},{"role":"user","content":"Continue."}]}`)
	for _, message := range []string{
		"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it.",
		"The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed.",
	} {
		for _, suffix := range []string{
			" (request id: )", " (request id: fixture) extra text", " (request id: fixture other)",
			" (request id: fixture) (request id: another)", " [trace_id=fixture] (request id: fixture)",
			" (request id: fixture\nother)", " (request id: fixture); retry", " (request id: fixture) [trace_id=]",
		} {
			rejection, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message + suffix}})
			if got, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses"); retry || !bytes.Equal(got, body) {
				t.Errorf("unexpected metadata accepted: %q", suffix)
			}
		}
	}
	// Reuse the full existing route, envelope and nonportable-history guards.
	t.Run("resource_with_request_id", func(t *testing.T) {
		rejection := []byte(`{"error":{"message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. (request id: fixture) [trace_id=fixture]","param":"","type":"invalid_request_error"}}`)
		testAgentResourceRejectionGuards(t, rejection)
	})
	for _, itemID := range []string{"rs_other", "rs_foreign_quoted"} {
		message := "The encrypted content for item " + itemID + " could not be verified. Reason: Encrypted content could not be decrypted or parsed. (request id: fixture) [trace_id=fixture]"
		rejection, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message}})
		if got, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses"); retry || !bytes.Equal(got, body) {
			t.Error("unmatched named encrypted item triggered recovery")
		}
	}
	for _, typ := range []string{"compaction", "compaction_summary", "item_reference"} {
		input := bytes.Replace(body, []byte(`"input":[`), []byte(`"input":[{"type":"`+typ+`","id":"retained_remote","encrypted_content":"required_history"},`), 1)
		message := "The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. (request id: fixture) [trace_id=fixture]"
		rejection, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message}})
		if got, retry := PortableResponsesSignatureRetry(input, rejection, "https://agentrouter.org/v1/responses"); retry || !bytes.Equal(got, input) {
			t.Errorf("required remote history changed: %s", typ)
		}
	}
}

func TestAgentEncryptedRecoveryAcceptsCapturedRelayTraceSuffix(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque"},{"role":"user","content":"continue"}]}`)
	rejection := []byte(`{"error":{"type":"invalid_request_error","code":null,"param":"","message":"The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed. trace_id: upstream-trace [trace_id=relay-trace]"}}`)
	got, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses")
	if !retry || gjson.GetBytes(got, "input.#").Int() != 1 || gjson.GetBytes(got, "input.0.content").String() != "continue" {
		t.Fatalf("captured encrypted rejection not repaired: retry=%t body=%s", retry, got)
	}
}

func TestAgentNumericCodeAndDecodeEnvelopeRecovery(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"Keep summary."}]},{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep answer."},{"role":"user","content":"Continue."}]}`)
	message := "The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it."
	for _, tc := range []struct {
		name    string
		code    any
		suffix  string
		wrapped bool
	}{
		{name: "numeric_code", code: 0},
		{name: "colon_trace", suffix: " trace_id: 3a1ac94610531a8eb4c492e638839a77"},
		{name: "decode_envelope", code: 0, wrapped: true},
		{name: "captured_combination", code: 0, suffix: " trace_id: 3a1ac94610531a8eb4c492e638839a77", wrapped: true},
		{name: "double_trace_inner", code: 0, suffix: " trace_id: 3a1ac94610531a8eb4c492e638839a77 [trace_id=inner-relay]", wrapped: true},
	} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sse=%t", tc.name, nested), func(t *testing.T) {
				envelope := map[string]any{"error": map[string]any{"code": tc.code, "message": message + tc.suffix, "type": "invalid_request_error"}}
				if tc.wrapped {
					inner, _ := json.Marshal(envelope)
					envelope = map[string]any{"error": map[string]any{"code": nil, "param": "", "type": "invalid_request_error", "message": "failed to decode error response: json: cannot unmarshal number into Go struct field ResponseError.error.code of type string, raw: " + string(inner) + " [trace_id=f87913149f33c5d348f6aa95e2ee36b5]"}}
				}
				if nested {
					envelope = map[string]any{"type": "response.failed", "response": envelope}
				}
				rejection, _ := json.Marshal(envelope)
				originalBody, originalRejection := bytes.Clone(body), bytes.Clone(rejection)
				repaired, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses")
				if !retry {
					t.Fatal("Agent state rejection did not enter portable recovery")
				}
				if gjson.GetBytes(repaired, "input.#").Int() != 3 || gjson.GetBytes(repaired, "input.0.summary.0.text").String() != "Keep summary." || gjson.GetBytes(repaired, "input.1.content").String() != "Keep answer." || gjson.GetBytes(repaired, "input.2.content").String() != "Continue." {
					t.Fatal("readable history changed")
				}
				if gjson.GetBytes(repaired, "input.0.encrypted_content").Exists() || gjson.GetBytes(repaired, "input.0.id").Exists() || gjson.GetBytes(repaired, "input.1.id").Exists() {
					t.Fatal("rejected route binding retained")
				}
				if !bytes.Equal(body, originalBody) || !bytes.Equal(rejection, originalRejection) {
					t.Fatal("caller history or original error mutated")
				}
				if _, again := PortableResponsesSignatureRetry(repaired, rejection, "https://agentrouter.org/v1/responses"); again {
					t.Fatal("repaired history retried again")
				}
			})
		}
	}
}

func TestAgentDecodeEnvelopeRecoveryGuards(t *testing.T) {
	const prefix = "failed to decode error response: json: cannot unmarshal number into Go struct field ResponseError.error.code of type string, raw: "
	const inner = `{"error":{"code":0,"message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. trace_id: inner-123","type":"invalid_request_error"}}`
	const suffix = " [trace_id=outer-456]"
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque"},{"type":"message","id":"msg_old","role":"assistant","content":"Keep history."},{"role":"user","content":"Continue."}]}`)
	endpoint := "https://agentrouter.org/v1/responses"
	wrap := func(message string) []byte {
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": nil, "type": "invalid_request_error", "param": "", "message": message}})
		return b
	}
	assertUnchanged := func(t *testing.T, request, rejection []byte, route string) {
		t.Helper()
		if got, retry := PortableResponsesSignatureRetry(request, rejection, route); retry || !bytes.Equal(got, request) {
			t.Fatal("unrelated error or nonportable history entered recovery")
		}
	}
	for _, tc := range []struct{ name, raw string }{
		{"nonzero_code", strings.Replace(inner, `"code":0`, `"code":400`, 1)},
		{"string_code", strings.Replace(inner, `"code":0`, `"code":"0"`, 1)},
		{"null_code", strings.Replace(inner, `"code":0`, `"code":null`, 1)},
		{"fractional_code", strings.Replace(inner, `"code":0`, `"code":0.5`, 1)},
		{"different_type", strings.Replace(inner, "invalid_request_error", "rate_limit_error", 1)},
		{"different_parameter", strings.Replace(inner, `"code":0`, `"code":0,"param":"tools"`, 1)},
		{"quoted_message", strings.Replace(inner, "The requested item", "Tool reported: The requested item", 1)},
		{"unrelated_message", strings.Replace(inner, "different Azure OpenAI resource", "different project", 1)},
		{"malformed_json", strings.TrimSuffix(inner, "}")},
		{"second_json_value", inner + `{}`},
		{"array", "[" + inner + "]"},
		{"recursive_wrapper", string(wrap(prefix + inner + suffix))},
	} {
		t.Run(tc.name, func(t *testing.T) { assertUnchanged(t, body, wrap(prefix+tc.raw+suffix), endpoint) })
	}
	for _, tc := range []struct{ name, message string }{
		{"quoted_wrapper", "Tool reported: " + prefix + inner + suffix},
		{"different_decoder", strings.Replace(prefix, "ResponseError.error.code", "ResponseError.status", 1) + inner + suffix},
		{"trailing_prose", prefix + inner + suffix + " retry anyway"},
		{"empty_trace", prefix + inner + " [trace_id=]"},
		{"repeated_trace", prefix + inner + suffix + suffix},
		{"newline_trace", prefix + inner + "\n" + suffix},
		{"truncated_wrapper", prefix},
	} {
		t.Run(tc.name, func(t *testing.T) { assertUnchanged(t, body, wrap(tc.message), endpoint) })
	}
	rejection := wrap(prefix + inner + suffix)
	for _, field := range []string{`"type":"rate_limit_error"`, `"code":400`, `"param":"tools"`} {
		var envelope map[string]any
		_ = json.Unmarshal(rejection, &envelope)
		var changed map[string]any
		_ = json.Unmarshal([]byte("{"+field+"}"), &changed)
		for k, v := range changed {
			envelope["error"].(map[string]any)[k] = v
		}
		invalid, _ := json.Marshal(envelope)
		assertUnchanged(t, body, invalid, endpoint)
	}
	for _, route := range []string{"https://example.test/v1/responses", "https://agentrouter.org.example/v1/responses", "https://agentrouter.org/v1/responses/compact"} {
		assertUnchanged(t, body, rejection, route)
	}
	for _, history := range []string{
		`{"previous_response_id":"resp_stored","input":[{"type":"message","id":"msg_old","role":"assistant","content":"History."},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction","encrypted_content":"required"},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction_summary","encrypted_content":"required"},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"item_reference","id":"stored"},{"role":"user","content":"Continue."}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"only history"}]}`,
		`{"input":[{"type":"message","id":"client_owned","role":"user","content":"Continue."}]}`,
	} {
		assertUnchanged(t, []byte(history), rejection, endpoint)
	}
	for _, code := range []any{1, -1, 0.5, "0", false, "invalid_api_key"} {
		var envelope map[string]any
		_ = json.Unmarshal([]byte(inner), &envelope)
		envelope["error"].(map[string]any)["code"] = code
		invalid, _ := json.Marshal(envelope)
		assertUnchanged(t, body, invalid, endpoint)
	}
	for _, trace := range []string{" trace_id: ", " trace_id: valid extra", " trace_id: inner [trace_id=outer] [trace_id=third]", " trace_id: a\nb"} {
		invalid := strings.Replace(inner, " trace_id: inner-123", trace, 1)
		assertUnchanged(t, body, []byte(invalid), endpoint)
	}
}
