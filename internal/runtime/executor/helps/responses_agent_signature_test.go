package helps

import (
	"bytes"
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
	body := []byte(`{"input":[{"type":"message","id":"msg_foreign","role":"assistant","content":"Keep history."},{"role":"user","content":"Continue."}]}`)
	rejection := []byte(`{"error":{"message":"OpenAI Responses bad request: The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]","param":"","type":"invalid_request_error"}}`)
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
