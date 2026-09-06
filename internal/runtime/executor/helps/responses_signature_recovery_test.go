package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPortableResponsesSignatureRetry(t *testing.T) {
	rejection := []byte(`{"error":{"code":"invalid_encrypted_content","param":"input[1].encrypted_content"}}`)
	body := []byte(`{"input":[{"type":"reasoning","encrypted_content":"keep"},{"type":"reasoning","id":"drop","encrypted_content":"foreign"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"fixture"}]}]}`)
	got, ok := PortableResponsesSignatureRetry(body, rejection)
	if !ok || len(gjson.GetBytes(got, "input").Array()) != 2 || gjson.GetBytes(got, "input.0.encrypted_content").String() != "keep" || gjson.GetBytes(got, "input.1.content.0.type").String() != "input_image" {
		t.Fatal("targeted repair changed unrelated history")
	}
	for _, raw := range []string{`{"input":[{"type":"compaction","encrypted_content":"opaque"}]}`, `{"previous_response_id":"remote","input":[{"type":"reasoning","encrypted_content":"opaque"}]}`, `{"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`} {
		if _, ok := PortableResponsesSignatureRetry([]byte(raw), []byte(`{"error":{"code":"invalid_encrypted_content"}}`)); ok {
			t.Fatal("recovery erased the only context or replayed an incremental request")
		}
	}
	if _, ok := PortableResponsesSignatureRetry(body, []byte(`{"error":{"message":"a tool mentioned invalid_encrypted_content"}}`)); ok {
		t.Fatal("free text falsely triggered recovery")
	}
}

func TestPortableResponsesSignatureRetryPreservesHistory(t *testing.T) {
	body := []byte(`{"model":"fixture","input":[{"type":"reasoning","encrypted_content":"foreign"},{"type":"message","role":"user","content":[{"type":"input_text","text":"marker"},{"type":"input_image","image_url":"data:image/png;base64,fixture"}]},{"type":"custom_tool_call","call_id":"call_fixture","name":"fixture","input":"input"},{"type":"custom_tool_call_output","call_id":"call_fixture","output":"output"}],"reasoning":{"effort":"high"}}`)
	original := string(body)
	repaired, ok := PortableResponsesSignatureRetry(body, []byte(`{"response":{"error":{"code":"thinking_signature_invalid"}}}`))
	if !ok {
		t.Fatal("explicit nested signature failure not repaired")
	}
	old := gjson.GetBytes(body, "input").Array()
	got := gjson.GetBytes(repaired, "input").Array()
	if len(got) != len(old)-1 {
		t.Fatal("wrong removal count")
	}
	for i := range got {
		if got[i].Raw != old[i+1].Raw {
			t.Fatal("portable history changed")
		}
	}
	if string(body) != original || gjson.GetBytes(repaired, "reasoning.effort").String() != "high" {
		t.Fatal("request parameters mutated")
	}
	for _, suffix := range []string{`{"type":"compaction","encrypted_content":"compact"}`, `{"type":"compaction_summary","encrypted_content":"compact"}`} {
		raw := []byte(`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","role":"user","content":"marker"},` + suffix + `]}`)
		if _, ok := PortableResponsesSignatureRetry(raw, []byte(`{"error":{"code":"invalid_encrypted_content"}}`)); ok {
			t.Fatal("compaction history silently changed")
		}
	}
	for _, rejection := range []string{`{"error":{"code":"no_capacity"}}`, `{"error":{"code":"invalid_encrypted_content","param":"model"}}`, `{"error":{"code":"invalid_encrypted_content","param":"input[8]"}}`} {
		if _, ok := PortableResponsesSignatureRetry(body, []byte(rejection)); ok {
			t.Fatal("unrelated rejection changed history")
		}
	}
}

type signatureRecoveryTransport func(*http.Request) (*http.Response, error)

func (f signatureRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestResponsesSignatureRecoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	client := &http.Client{Transport: signatureRecoveryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_encrypted_content"}}`)), Header: make(http.Header), Request: r}, nil
	})}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/responses", strings.NewReader(`{"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","role":"user","content":"marker"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = DoWithResponsesSignatureRecovery(client, req, nil)
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attempt replayed: calls=%d err=%v", calls, err)
	}
}
