package helps

import (
	"bytes"
	"testing"
)

func TestNormalizeResponsesHistoryScopeAndIdempotence(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"keep"}]},{"type":"web_search_call","status":"completed","action":{"type":"search","query":"kept"}}]}`)
	for _, endpoint := range []string{"https://example.test/v1/responses", "https://anyrouter.top.example/v1/responses", "https://anyrouter.top/v1/responses/compact", "https://anyrouter.top/v1/images/generations"} {
		if !bytes.Equal(body, NormalizeResponsesHistory(body, endpoint)) {
			t.Errorf("unrelated route changed: %s", endpoint)
		}
	}
	endpoint := "https://anyrouter.top/v1/responses"
	normalized := NormalizeResponsesHistory(body, endpoint)
	if bytes.Equal(normalized, body) || !bytes.Equal(normalized, NormalizeResponsesHistory(normalized, endpoint)) {
		t.Fatal("normalization missing or not idempotent")
	}
	for _, input := range []string{
		`{"model":"gpt-other","input":[{"type":"reasoning","content":[{"type":"reasoning_text","text":"keep"}]}]}`,
		`{"model":"gpt-6-astra","previous_response_id":"remote","input":[{"type":"web_search_call","status":"completed","action":{}}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"compaction"},{"type":"web_search_call","status":"completed","action":{}}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"reasoning","content":[{"type":"unknown","text":"keep"}]},{"type":"web_search_call","status":"in_progress","action":{}}]}`,
	} {
		if got := NormalizeResponsesHistory([]byte(input), endpoint); !bytes.Equal(got, []byte(input)) {
			t.Fatal("unrecognized/incremental history changed")
		}
	}
}

func TestAgentSignatureMessageRequiresExactEnvelopeAndReferencedItem(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","encrypted_content":"opaque"},{"role":"user","content":"keep"}]}`)
	rejection := []byte(`{"error":{"type":"invalid_request_error","code":null,"param":"","message":"OpenAI Responses bad request: The encrypted content for item rs_foreign could not be verified. Reason: Encrypted content could not be decrypted or parsed. [trace_id=fixture]"}}`)
	for _, endpoint := range []string{"https://example.test/v1/responses", "https://agentrouter.org.example/v1/responses", "https://agentrouter.org/v1/responses/compact"} {
		if _, retry := PortableResponsesSignatureRetry(body, rejection, endpoint); retry {
			t.Errorf("unrelated endpoint repaired: %s", endpoint)
		}
	}
	endpoint := "https://agentrouter.org/v1/responses"
	for _, invalid := range [][]byte{
		bytes.Replace(rejection, []byte("rs_foreign"), []byte("rs_other"), 1),
		bytes.Replace(rejection, []byte(`"code":null`), []byte(`"code":"different_error"`), 1),
		bytes.Replace(rejection, []byte(`"param":""`), []byte(`"param":"tools"`), 1),
		bytes.Replace(rejection, []byte("OpenAI Responses bad request: "), []byte("a tool said: "), 1),
	} {
		if _, retry := PortableResponsesSignatureRetry(body, invalid, endpoint); retry {
			t.Fatal("unrelated envelope repaired")
		}
	}
	if _, retry := PortableResponsesSignatureRetry(body, rejection, endpoint); !retry {
		t.Fatal("captured envelope not repaired")
	}
}
