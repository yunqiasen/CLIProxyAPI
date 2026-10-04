package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeRequestLogPreservesProviderName(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, claudeRefusalTestSSE("end_turn"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, claudeToolFallbackSuccessJSON())
			}))
			defer server.Close()
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			ctx := context.WithValue(t.Context(), "gin", ginCtx)
			executor := NewClaudeExecutor(&config.Config{SDKConfig: config.SDKConfig{RequestLog: true}})
			auth := &cliproxyauth.Auth{ID: "named-claude", Attributes: map[string]string{"api_key": "fixture", "base_url": server.URL, "provider_name": "Named-Claude"}}
			req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: stream}
			if stream {
				result, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else {
				if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			value, _ := ginCtx.Get("API_REQUEST")
			data, _ := value.([]byte)
			if !strings.Contains(string(data), "provider_name=Named-Claude") {
				t.Fatal("named provider lost from actual request log")
			}
		})
	}
}
