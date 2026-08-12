package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResponsesToClaudePreservesCommonTextInputShapes(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		path     string
		want     string
		wantRole string
	}{
		{
			name:     "top-level string input",
			body:     `{"model":"claude-test","input":"阿福"}`,
			path:     "messages.0.content",
			want:     "阿福",
			wantRole: "user",
		},
		{
			name:     "generic text content part",
			body:     `{"model":"claude-test","input":[{"type":"message","role":"user","content":[{"type":"text","text":"阿福"}]}]}`,
			path:     "messages.0.content",
			want:     "阿福",
			wantRole: "user",
		},
		{
			name:     "assistant generic text content part",
			body:     `{"model":"claude-test","input":[{"type":"message","role":"assistant","content":[{"type":"text","text":"历史回复"}]}]}`,
			path:     "messages.0.content",
			want:     "历史回复",
			wantRole: "assistant",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(
				sdktranslator.FormatOpenAIResponse,
				sdktranslator.FormatClaude,
				"claude-test",
				[]byte(tt.body),
				false,
			)
			if got := gjson.GetBytes(out, tt.path).String(); got != tt.want {
				t.Fatalf("%s = %q, want %q. Output: %s", tt.path, got, tt.want, out)
			}
			if got := gjson.GetBytes(out, "messages.0.role").String(); got != tt.wantRole {
				t.Fatalf("messages.0.role = %q, want %q. Output: %s", got, tt.wantRole, out)
			}
		})
	}
}
