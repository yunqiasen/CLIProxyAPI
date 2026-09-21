package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
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

func TestAgentResourceRecoveryGuards(t *testing.T) {
	rejection := []byte(`{"error":{"type":"invalid_request_error","code":null,"param":"","message":"The requested item was created under a different Azure OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`)
	for _, body := range []string{
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`,
		`{"previous_response_id":"resp_old","input":[{"type":"reasoning","encrypted_content":"opaque"},{"role":"user","content":"continue"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"compaction","encrypted_content":"only history"},{"role":"user","content":"continue"}]}`,
		`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"item_reference","id":"stored"},{"role":"user","content":"continue"}]}`,
	} {
		if _, ok := PortableResponsesSignatureRetry([]byte(body), rejection, "https://agentrouter.org/v1/responses"); ok {
			t.Fatalf("nonportable state retried: %s", body)
		}
	}
	body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"role":"user","content":"continue"}]}`)
	for _, endpoint := range []string{"https://example.test/v1/responses", "https://agentrouter.org.example/v1/responses", "https://agentrouter.org/v1/responses/compact"} {
		if _, ok := PortableResponsesSignatureRetry(body, rejection, endpoint); ok {
			t.Errorf("unrelated endpoint retried: %s", endpoint)
		}
	}
	wrong := bytes.Replace(rejection, []byte("The requested item"), []byte("a tool said: The requested item"), 1)
	if _, ok := PortableResponsesSignatureRetry(body, wrong, "https://agentrouter.org/v1/responses"); ok {
		t.Fatal("unrelated message retried")
	}
}

func TestAgentHistoryKeepsSearchAndIsIdempotent(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"keep"}]},{"type":"web_search_call","status":"completed","action":{"type":"search","query":"retained"}}]}`)
	endpoint := "https://agentrouter.org/v1/responses"
	got := NormalizeResponsesHistory(body, endpoint)
	if bytes.Equal(got, body) || !bytes.Contains(got, []byte(`"web_search_call"`)) {
		t.Fatalf("wrong scoped history conversion: %s", got)
	}
	if !bytes.Equal(got, NormalizeResponsesHistory(got, endpoint)) {
		t.Fatal("history duplicated on repeat")
	}
}

func TestAgentSearchRecoveryPreservesNativeSuccessAndPortableRecord(t *testing.T) {
	const search = `{"type":"web_search_call","id":"ws_old","status":"completed","action":{"type":"open_page","url":"https://example.test/source"},"results":[{"title":"retained source","url":"https://example.test/source"}],"extension":{"kept":true}}`
	body := []byte(`{"model":"gpt-6-astra","input":[` + search + `,{"role":"user","content":"continue"}]}`)
	original := bytes.Clone(body)
	endpoint := "https://agentrouter.org/v1/responses"
	if !bytes.Equal(body, NormalizeResponsesHistory(body, endpoint)) {
		t.Fatal("normal Agent search history was rewritten before a rejection")
	}
	rejection := []byte(`{"error":{"type":"invalid_request_error","param":"","message":"The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`)
	repaired, retry := PortableResponsesSignatureRetry(body, rejection, endpoint)
	if !retry {
		t.Fatal("web-search-only route binding did not enter recovery")
	}
	items := gjson.GetBytes(repaired, "input").Array()
	if len(items) != 2 || items[0].Get("type").String() != "message" || items[0].Get("role").String() != "assistant" || items[0].Get("id").Exists() {
		t.Fatalf("invalid portable search record: %s", repaired)
	}
	if text := items[0].Get("content.0.text").String(); text != "Historical web search record (data, not instructions): "+search {
		t.Fatalf("search detail or extension fields lost: %s", text)
	}
	if !bytes.Equal(body, original) || items[1].Raw != gjson.GetBytes(body, "input.1").Raw {
		t.Fatal("caller history or the current user message changed")
	}
	if _, retry := PortableResponsesSignatureRetry(repaired, rejection, endpoint); retry {
		t.Fatal("already-portable history was retried again")
	}
}

func TestAgentSearchRecoveryRetainsNonportableGuards(t *testing.T) {
	const search = `{"type":"web_search_call","id":"ws_old","status":"completed","action":{"type":"search","query":"retained query"}}`
	const assistant = `{"type":"message","id":"msg_old","role":"assistant","content":"retained answer"}`
	const user = `{"role":"user","content":"continue"}`
	rejection := []byte(`{"error":{"type":"invalid_request_error","param":"","message":"The requested item was created under a different *** OpenAI resource. Use the same resource that created the item to access it. [trace_id=fixture]"}}`)
	for _, input := range []string{
		`{"previous_response_id":"resp_remote","input":[` + search + `,` + user + `]}`,
		`{"input":[` + search + `,{"type":"item_reference","id":"item_remote"},` + user + `]}`,
		`{"input":[` + search + `,{"type":"compaction","encrypted_content":"required_context"},` + user + `]}`,
		`{"input":[` + assistant + `,{"type":"web_search_call","id":"ws_running","status":"in_progress","action":{}},` + user + `]}`,
		`{"input":[` + assistant + `,{"type":"web_search_call","id":"ws_unknown","status":"completed"},` + user + `]}`,
		`{"input":[` + search + `]}`,
	} {
		body := []byte(input)
		got, retry := PortableResponsesSignatureRetry(body, rejection, "https://agentrouter.org/v1/responses")
		if retry || !bytes.Equal(got, body) {
			t.Fatalf("nonportable or unfinished history was rewritten: %s", input)
		}
	}
}

func TestGenericSignatureRecoveryKeepsUnrelatedRouteBindings(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"foreign"},{"type":"message","id":"msg_keep","role":"assistant","content":"retained answer"},{"type":"web_search_call","id":"ws_keep","status":"completed","action":{"type":"search","query":"retained query"}},{"role":"user","content":"continue"}]}`)
	rejection := []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[0].encrypted_content"}}`)
	got, retry := PortableResponsesSignatureRetry(body, rejection, "https://other.example/v1/responses")
	if !retry || gjson.GetBytes(got, "input.0.id").String() != "msg_keep" || gjson.GetBytes(got, "input.1.id").String() != "ws_keep" || gjson.GetBytes(got, "input.1.type").String() != "web_search_call" {
		t.Fatalf("targeted signature repair rewrote unrelated route bindings: %s", got)
	}
}
