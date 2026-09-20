package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAnyHistoricalItemRecoveryGuards(t *testing.T) {
	body := []byte(`{"model":"gpt-6-astra","input":[{"type":"message","role":"assistant","id":"msg_old","content":"Keep answer"},{"type":"function_call","id":"fc_old","call_id":"keep_call","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fco_old","call_id":"keep_call","output":"Keep result"},{"type":"message","role":"user","id":"msg_client","content":"Continue"}]}`)
	rejection := []byte(`{"error":{"message":"bad response status code 400 (request id: fixture)","type":"invalid_request_error","code":null,"param":""}}`)
	endpoint := "https://anyrouter.top/v1/responses"
	got, ok := PortableResponsesSignatureRetry(body, rejection, endpoint)
	if !ok || gjson.GetBytes(got, "input.0.id").Exists() || gjson.GetBytes(got, "input.1.id").Exists() || gjson.GetBytes(got, "input.2.id").Exists() {
		t.Fatal("old item IDs retained")
	}
	for _, path := range []string{"input.0.content", "input.1.call_id", "input.1.arguments", "input.2.call_id", "input.2.output", "input.3"} {
		if gjson.GetBytes(got, path).Raw != gjson.GetBytes(body, path).Raw {
			t.Errorf("portable field changed: %s", path)
		}
	}
	if _, again := PortableResponsesSignatureRetry(got, rejection, endpoint); again {
		t.Fatal("unchanged request offered another retry")
	}
	for _, route := range []string{"https://example.test/v1/responses", "https://agentrouter.org/v1/responses", "https://anyrouter.top.example/v1/responses", "https://anyrouter.top/v1/responses/compact"} {
		if _, ok := PortableResponsesSignatureRetry(body, rejection, route); ok {
			t.Errorf("unrelated route: %s", route)
		}
	}
	for _, tc := range []struct{ old, new string }{
		{"invalid_request_error", "rate_limit_error"},
		{`"code":null`, `"code":"invalid_responses_request"`},
		{`"param":""`, `"param":"tools"`},
		{"bad response status", "a tool quoted: bad response status"},
		{"code 400", "code 429"},
	} {
		r := bytes.Replace(rejection, []byte(tc.old), []byte(tc.new), 1)
		if _, ok := PortableResponsesSignatureRetry(body, r, endpoint); ok {
			t.Fatalf("unrelated rejection: %s", r)
		}
	}
	for _, raw := range []string{
		`{"model":"other","input":[{"type":"message","role":"assistant","id":"msg_old","content":"Keep"}]}`,
		`{"model":"gpt-6-astra","previous_response_id":"resp_old","input":[{"type":"message","role":"assistant","id":"msg_old","content":"Keep"}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"message","role":"assistant","id":"msg_old","content":"Keep"},{"type":"compaction","encrypted_content":"compact"}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"message","role":"assistant","id":"msg_old","content":"Keep"},{"type":"item_reference","id":"stored"}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","id":"msg_client","content":"Keep"}]}`,
		`{"model":"gpt-6-astra","input":[{"type":"reasoning","id":"rs_old","encrypted_content":"opaque"},{"role":"user","content":"Keep"}]}`,
	} {
		r, ok := PortableResponsesSignatureRetry([]byte(raw), rejection, endpoint)
		if ok || string(r) != raw {
			t.Fatalf("unsupported history changed: %s", raw)
		}
	}
}
