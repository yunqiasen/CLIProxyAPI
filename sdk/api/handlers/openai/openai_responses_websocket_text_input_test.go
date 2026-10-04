package openai

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestWebsocketTextInputCompatibility(t *testing.T) {
	for _, kind := range []string{"response.create", "response.append"} {
		raw := []byte(`{"type":"` + kind + `","input":"hello\nworld","model":"fixture"}`)
		got := normalizeResponsesWebsocketTextInput(raw)
		if gjson.GetBytes(got, "input.0.role").String() != "user" || gjson.GetBytes(got, "input.0.content.0.text").String() != "hello\nworld" || gjson.GetBytes(got, "type").String() != kind {
			t.Fatalf("text input lost: %s", got)
		}
	}
	for _, raw := range []string{`{"input":[]}`, `{"input":{}}`, `{"input":null}`, `{"input":2}`, `{"type":"response.create"}`} {
		if got := string(normalizeResponsesWebsocketTextInput([]byte(raw))); got != raw {
			t.Fatalf("non-string input changed: %s", got)
		}
	}
}
