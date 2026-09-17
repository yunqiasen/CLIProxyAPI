package helps

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeCodexInputItemIDsPreservesReadableReasoning(t *testing.T) {
	for _, field := range []string{"summary", "content"} {
		t.Run(field, func(t *testing.T) {
			kind := "summary_text"
			if field == "content" {
				kind = "reasoning_text"
			}
			item := map[string]any{
				"type": "reasoning", "id": "rs_" + strings.Repeat("a", 62),
				"encrypted_content": "route-bound-state",
				field:               []any{map[string]any{"type": kind, "text": "Keep the user's plan.", "extra": "retained"}},
			}
			body, err := json.Marshal(map[string]any{"input": []any{item,
				map[string]any{"type": "function_call_output", "call_id": "call_keep", "output": "tool result"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			original := string(body)
			got := SanitizeCodexInputItemIDs(body)
			if gjson.GetBytes(got, "input.0."+field+".0.text").String() != "Keep the user's plan." {
				t.Fatalf("readable history lost: %s", got)
			}
			if gjson.GetBytes(got, "input.0."+field+".0.extra").String() != "retained" || gjson.GetBytes(got, "input.1.output").String() != "tool result" {
				t.Fatalf("unrelated history changed: %s", got)
			}
			if gjson.GetBytes(got, "input.0.id").Exists() || gjson.GetBytes(got, "input.0.encrypted_content").Exists() {
				t.Fatalf("invalid route-bound state retained: %s", got)
			}
			if string(body) != original || string(SanitizeCodexInputItemIDs(got)) != string(got) {
				t.Fatal("sanitization mutates input or is not idempotent")
			}
		})
	}
}

func TestSanitizeCodexInputItemIDsReadableBoundaryAndOpaqueState(t *testing.T) {
	for _, length := range []int{64, 65} {
		for _, character := range []string{"a", "界"} {
			id := "rs_" + strings.Repeat(character, length-3)
			body := []byte(`{"input":[{"type":"reasoning","id":"` + id + `","encrypted_content":"state","summary":[{"type":"summary_text","text":"retained"}]},{"type":"compaction","id":"compact_1","encrypted_content":"opaque"},{"type":"item_reference","id":"ref_1"}]}`)
			got := SanitizeCodexInputItemIDs(body)
			if gjson.GetBytes(got, "input.0.summary.0.text").String() != "retained" {
				t.Fatalf("history lost at %d characters: %s", length, got)
			}
			if length == 64 && (gjson.GetBytes(got, "input.0.id").String() != id || gjson.GetBytes(got, "input.0.encrypted_content").String() != "state") {
				t.Fatal("valid-length binding changed")
			}
			if length == 65 && (gjson.GetBytes(got, "input.0.id").Exists() || gjson.GetBytes(got, "input.0.encrypted_content").Exists()) {
				t.Fatal("invalid binding remained")
			}
			if gjson.GetBytes(got, "input.1.encrypted_content").String() != "opaque" || gjson.GetBytes(got, "input.2.id").String() != "ref_1" {
				t.Fatal("opaque compaction or stored reference changed")
			}
		}
	}
}
