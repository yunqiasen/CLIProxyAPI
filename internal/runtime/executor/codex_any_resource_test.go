package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const anyHistoricalItemRejection = `{"error":{"message":"bad response status code 400 (request id: 20260920121127464443788CqZ9dieq)","param":"","type":"invalid_request_error"}}`

func TestAnyHistoricalItemRecovery(t *testing.T) {
	for _, reject := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("reject_%t/stream_%t", reject, stream), func(t *testing.T) {
				var bodies [][]byte
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					bodies = append(bodies, b)
					if r.Header.Get("Authorization") != "Bearer selected-key" {
						t.Error("credential changed")
					}
					if reject && gjson.GetBytes(b, "input.0.id").Exists() {
						w.WriteHeader(400)
						_, _ = io.WriteString(w, anyHistoricalItemRejection)
						return
					}
					if gjson.GetBytes(b, "input.0.content.0.text").String() != "Earlier answer" || gjson.GetBytes(b, "input.1.id").String() != "msg_client" {
						t.Error("portable history changed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
				}))
				defer upstream.Close()
				credential := &auth.Auth{ID: "any-selected", Provider: "codex", ProxyURL: upstream.URL, Attributes: map[string]string{"base_url": "http://anyrouter.top/v1", "api_key": "selected-key"}}
				raw := []byte(`{"model":"gpt-6-astra","store":false,"input":[{"type":"message","id":"msg_from_agent","role":"assistant","content":[{"type":"output_text","text":"Earlier answer"}]},{"type":"message","id":"msg_client","role":"user","content":"Continue"}]}`)
				req := ex.Request{Model: "gpt-6-astra", Payload: raw}
				opts := ex.Options{SourceFormat: tr.FormatOpenAIResponse, Stream: stream}
				e := NewCodexExecutor(&config.Config{})
				var output string
				if stream {
					r, err := e.ExecuteStream(context.Background(), credential, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for c := range r.Chunks {
						if c.Err != nil {
							t.Fatal(c.Err)
						}
						output += string(c.Payload)
					}
				} else {
					r, err := e.Execute(context.Background(), credential, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = string(r.Payload)
				}
				wantCalls := 1
				if reject {
					wantCalls = 2
				} else if gjson.GetBytes(bodies[0], "input.0.id").String() != "msg_from_agent" {
					t.Fatal("successful request lost historical ID")
				}
				if len(bodies) != wantCalls || !strings.Contains(output, "OK") {
					t.Fatalf("calls=%d output=%s", len(bodies), output)
				}
				if !gjson.GetBytes(raw, "input.0.id").Exists() {
					t.Fatal("client request mutated")
				}
			})
		}
	}
}
