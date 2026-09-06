package util

import "testing"

func TestSSEChunkNeedsLineBreak(t *testing.T) {
	for _, tc := range []struct {
		name, pending, chunk string
		want                 bool
	}{
		{"legacy event then data", "event: response.completed", `data: {"type":"response.completed"}`, true},
		{"legacy multiline JSON", `data: {"type":"response.completed",`, `data: "response":{}}`, true},
		{"legacy comment after JSON", `data: {"type":"response.created"}`, ": keep-alive", true},
		{"existing newline", "event: response.completed\n", "data: {}", false},
		{"incoming newline", "event: response.completed", "\r\ndata: {}", false},
		{"partial field", "data", ": {}", false},
		{"JSON key separator", `data: {"type"`, `:"response.completed"}`, false},
		{"data inside string", `data: {"delta":"literal `, `data: value"}`, false},
		{"event inside string", `data: {"delta":"literal `, `event: value"}`, false},
		{"escaped quote inside string", `data: {"delta":"quote \" `, `event: value"}`, false},
		{"escaped backslash closes string", `data: {"delta":"slash\\",`, `data: "type":"response.output_text.delta"}`, true},
		{"empty field", "data:", `{"type":"response.completed"}`, false},
		{"empty chunks", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SSEChunkNeedsLineBreak([]byte(tc.pending), []byte(tc.chunk)); got != tc.want {
				t.Fatalf("SSEChunkNeedsLineBreak(%q, %q)=%t want=%t", tc.pending, tc.chunk, got, tc.want)
			}
		})
	}
}
