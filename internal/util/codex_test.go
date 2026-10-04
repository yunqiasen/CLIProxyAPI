package util

import (
	"net/http"
	"testing"
)

func TestCodexLiteHeaderCasing(t *testing.T) {
	for _, key := range []string{"X-OpenAI-Internal-Codex-Responses-Lite", "X-Openai-Internal-Codex-Responses-Lite", "x-openai-internal-codex-responses-lite"} {
		if !IsCodexResponsesLiteRequest(nil, http.Header{key: {"true"}}) {
			t.Errorf("missed header %q", key)
		}
	}
}
