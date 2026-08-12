package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeUnsupportedWebSearchError = `{"type":"error","error":{"type":"invalid_request_error","message":"tool type 'web_search_20250305' is not supported for this model"}}`
const claudeUnsupportedWebSearchBedrockError = `{"type":"error","error":{"type":"invalid_request_error","message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1) [trace_id=trace-1] (request id: relay-1)"}}`
const claudeUnsupportedWebSearchBedrockNonStreamError = `{"type":"error","error":{"type":"invalid_request_error","message":"InvokeModel: operation error Bedrock Runtime: InvokeModel, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1) [trace_id=trace-1] (request id: relay-1)"}}`
const claudeUnsupportedWebSearchRelayTraceError = `{"type":"error","error":{"type":"invalid_request_error","message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream-trace [trace_id=relay-trace] (request id: relay-request)"}}`

func TestClaudeUnsupportedServerToolFallbackParserRequiresExactDeclaredType(t *testing.T) {
	requestBody := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "exact declared tool type",
			body: claudeUnsupportedWebSearchError,
			want: true,
		},
		{
			name: "exact bedrock validation wrapper",
			body: claudeUnsupportedWebSearchBedrockError,
			want: true,
		},
		{
			name: "exact bedrock non-stream validation wrapper",
			body: claudeUnsupportedWebSearchBedrockNonStreamError,
			want: true,
		},
		{
			name: "exact bedrock validation wrapper without trace suffix",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: true,
		},
		{
			name: "exact relay trace wrapper",
			body: claudeUnsupportedWebSearchRelayTraceError,
			want: true,
		},
		{
			name: "relay trace wrapper missing first trace id is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model [trace_id=relay-trace] (request id: relay-request)"}}`,
			want: false,
		},
		{
			name: "relay trace wrapper arbitrary suffix is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream-trace retry later [trace_id=relay-trace] (request id: relay-request)"}}`,
			want: false,
		},
		{
			name: "relay trace wrapper missing final request id is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream-trace [trace_id=relay-trace]"}}`,
			want: false,
		},
		{
			name: "relay trace wrapper unicode format control is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream\u200btrace [trace_id=relay-trace] (request id: relay-request)"}}`,
			want: false,
		},
		{
			name: "relay trace wrapper unicode control is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream-trace [trace_id=relay\u0085trace] (request id: relay-request)"}}`,
			want: false,
		},
		{
			name: "relay trace wrapper request id whitespace is rejected",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model, trace_id: upstream-trace [trace_id=relay-trace] (request id: relay request)"}}`,
			want: false,
		},
		{
			name: "generic validation wrapper is rejected",
			body: `{"error":{"message":"ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "generic bedrock marker sequence is rejected",
			body: `{"error":{"message":"Bedrock Runtime: StatusCode: 400, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper missing outer request id is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper outer request id with whitespace is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request id, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper inner request id with whitespace is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream id)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper unicode whitespace in request id is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request\u00a0id, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper unicode control in request id is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request\u0085id, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper unicode format control in request id is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request\u200bid, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper without request id boundary is rejected",
			body: `{"error":{"message":"Bedrock Runtime: StatusCode: 400, ValidationException: tool type 'web_search_20250305' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper with trailing clause text is rejected",
			body: `{"error":{"message":"Bedrock Runtime: StatusCode: 400, ValidationException: tool type 'web_search_20250305' is not supported for this model; retry later (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock marker substring is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error NotBedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock status code prefix is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 4000, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper marker order is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error StatusCode: 400, Bedrock Runtime: InvokeModelWithResponseStream, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "validation marker substring is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, NotValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1)"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper trailing metadata text is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: upstream-1); retry later"}}`,
			want: false,
		},
		{
			name: "bedrock wrapper empty request id is rejected",
			body: `{"error":{"message":"InvokeModelWithResponseStream: operation error Bedrock Runtime: InvokeModelWithResponseStream, https response error StatusCode: 400, RequestID: request-1, ValidationException: tool type 'web_search_20250305' is not supported for this model (request id: )"}}`,
			want: false,
		},
		{
			name: "generic unsupported message",
			body: `{"error":{"message":"tool is not supported for this model"}}`,
			want: false,
		},
		{
			name: "different tool type",
			body: `{"error":{"message":"tool type 'computer_20250124' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "declared type is only a prefix",
			body: `{"error":{"message":"tool type 'web_search_20250305_beta' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "declared type only appears in unrelated text",
			body: `{"error":{"message":"another tool is not supported for this model; request also mentioned web_search_20250305"}}`,
			want: false,
		},
		{
			name: "missing quotes",
			body: `{"error":{"message":"tool type web_search_20250305 is not supported for this model"}}`,
			want: false,
		},
		{
			name: "prefixed message",
			body: `{"error":{"message":"proxy note: tool type 'web_search_20250305' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "leading message whitespace",
			body: `{"error":{"message":" tool type 'web_search_20250305' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "trailing message whitespace",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model "}}`,
			want: false,
		},
		{
			name: "trailing message",
			body: `{"error":{"message":"tool type 'web_search_20250305' is not supported for this model; request rejected"}}`,
			want: false,
		},
		{
			name: "case changed tool type",
			body: `{"error":{"message":"tool type 'Web_Search_20250305' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "tool type contains surrounding whitespace",
			body: `{"error":{"message":"tool type ' web_search_20250305 ' is not supported for this model"}}`,
			want: false,
		},
		{
			name: "server error",
			body: claudeUnsupportedWebSearchError,
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := http.StatusBadRequest
			if test.name == "server error" {
				status = http.StatusInternalServerError
			}
			_, got := claudeUnsupportedServerToolFallbackFromError(status, []byte(test.body), requestBody)
			if got != test.want {
				t.Fatalf("fallback match = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRemoveClaudeUnsupportedServerToolsPreservesCustomToolWithSameName(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"custom","name":"web_search","input_schema":{"type":"object"}}
		]
	}`)
	out, removed := removeClaudeUnsupportedServerTools(body, claudeUnsupportedServerToolFallback{
		toolTypes: map[string]bool{"web_search_20250305": true},
		toolNames: map[string]bool{"web_search": true},
	})
	if !removed {
		t.Fatal("removeClaudeUnsupportedServerTools() removed = false, want true")
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tool count = %d, want 1; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "custom" {
		t.Fatalf("remaining tool type = %q, want custom; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("remaining tool name = %q, want web_search; body=%s", got, out)
	}
}

func TestRemoveClaudeUnsupportedServerToolsPreservesSameNameCustomToolChoice(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"custom","name":"web_search","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"web_search"}
	}`)
	out, removed := removeClaudeUnsupportedServerTools(body, claudeUnsupportedServerToolFallback{
		toolTypes: map[string]bool{"web_search_20250305": true},
		toolNames: map[string]bool{"web_search": true},
	})
	if !removed {
		t.Fatal("removeClaudeUnsupportedServerTools() removed = false, want true")
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tool count = %d, want 1; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "custom" {
		t.Fatalf("remaining tool type = %q, want custom; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Fatalf("tool_choice.name = %q, want web_search; body=%s", got, out)
	}
}

func TestRemoveClaudeUnsupportedServerToolsKeepsUnrelatedToolChoice(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"web_search_20250305","name":"web_search"},
			{"name":"lookup","input_schema":{"type":"object"}}
		],
		"tool_choice":{"type":"tool","name":"lookup"}
	}`)
	out, removed := removeClaudeUnsupportedServerTools(body, claudeUnsupportedServerToolFallback{
		toolTypes: map[string]bool{"web_search_20250305": true},
		toolNames: map[string]bool{"web_search": true},
	})
	if !removed {
		t.Fatal("removeClaudeUnsupportedServerTools() removed = false, want true")
	}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tool count = %d, want 1; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.name = %q, want lookup; body=%s", got, out)
	}
}

func TestClaudeExecutorRetriesOnceWithoutExplicitlyUnsupportedServerTool(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "execute stream"
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var upstreamBodies [][]byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				mu.Lock()
				upstreamBodies = append(upstreamBodies, bytes.Clone(body))
				attempt := len(upstreamBodies)
				mu.Unlock()

				if attempt == 1 {
					errorBody := claudeUnsupportedWebSearchBedrockNonStreamError
					if stream {
						errorBody = claudeUnsupportedWebSearchBedrockError
					}
					return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", errorBody), nil
				}
				if stream {
					return claudeTestHTTPResponse(req, http.StatusOK, "text/event-stream", claudeToolFallbackSuccessSSE()), nil
				}
				return claudeTestHTTPResponse(req, http.StatusOK, "application/json", claudeToolFallbackSuccessJSON()), nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "agent-router-key",
				"base_url": "https://agentrouter.invalid",
			}}
			payload := []byte(`{
				"model":"claude-opus-5",
				"max_tokens":32,
				"messages":[{"role":"user","content":"reply ok"}],
				"tools":[
					{"type":"web_search_20250305","name":"web_search","max_uses":5},
					{"name":"lookup","description":"custom lookup","input_schema":{"type":"object","properties":{}}}
				],
				"tool_choice":{"type":"tool","name":"web_search"}
			}`)
			request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
			options := cliproxyexecutor.Options{
				Stream:         stream,
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			}

			if stream {
				result, errStream := executor.ExecuteStream(ctx, auth, request, options)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				var response bytes.Buffer
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					response.Write(chunk.Payload)
				}
				if !strings.Contains(response.String(), `"text":"ok"`) {
					t.Fatalf("stream response = %q, want ok text", response.String())
				}
			} else {
				response, errExecute := executor.Execute(ctx, auth, request, options)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				if got := gjson.GetBytes(response.Payload, "content.0.text").String(); got != "ok" {
					t.Fatalf("response text = %q, want ok; payload=%s", got, response.Payload)
				}
			}

			mu.Lock()
			bodies := append([][]byte(nil), upstreamBodies...)
			mu.Unlock()
			if len(bodies) != 2 {
				t.Fatalf("upstream attempts = %d, want 2", len(bodies))
			}
			if got := gjson.GetBytes(bodies[0], "tools.0.type").String(); got != "web_search_20250305" {
				t.Fatalf("first request tool type = %q, want web_search_20250305; body=%s", got, bodies[0])
			}
			if got := gjson.GetBytes(bodies[1], "tools.#").Int(); got != 1 {
				t.Fatalf("retry tool count = %d, want 1; body=%s", got, bodies[1])
			}
			if got := gjson.GetBytes(bodies[1], "tools.0.name").String(); got != "lookup" {
				t.Fatalf("retry tool name = %q, want lookup; body=%s", got, bodies[1])
			}
			if got := gjson.GetBytes(bodies[1], "tool_choice"); got.Exists() {
				t.Fatalf("retry tool_choice = %s, want absent after selected tool removal; body=%s", got.Raw, bodies[1])
			}
		})
	}
}

func TestClaudeExecutorDoesNotRetryOrdinaryBadRequest(t *testing.T) {
	attempts := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", `{"type":"error","error":{"message":"invalid max_tokens"}}`), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx,
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": "https://agentrouter.invalid"}},
		cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: []byte(`{"model":"claude-opus-5","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude},
	)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want upstream bad request")
	}
	var statusCoder interface{ StatusCode() int }
	if !errors.As(errExecute, &statusCoder) || statusCoder.StatusCode() != http.StatusBadRequest {
		t.Fatalf("Execute() error = %T %v, want HTTP 400", errExecute, errExecute)
	}
	if attempts != 1 {
		t.Fatalf("upstream attempts = %d, want 1", attempts)
	}
}

func TestClaudeExecutorUnsupportedServerToolFallbackRetriesAtMostOnce(t *testing.T) {
	attempts := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", claudeUnsupportedWebSearchError), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx,
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": "https://agentrouter.invalid"}},
		cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: []byte(`{"model":"claude-opus-5","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
		},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude},
	)
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want second upstream rejection")
	}
	if attempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", attempts)
	}
}

func claudeTestHTTPResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func claudeToolFallbackSuccessJSON() string {
	return `{"id":"msg_tool_fallback","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
}

func claudeToolFallbackSuccessSSE() string {
	return "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool_fallback\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
}

func TestClaudeExecutorUnsupportedServerToolFallbackResignsCCH(t *testing.T) {
	var bodies [][]byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		bodies = append(bodies, bytes.Clone(body))
		if len(bodies) == 1 {
			return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", claudeUnsupportedWebSearchError), nil
		}
		return claudeTestHTTPResponse(req, http.StatusOK, "application/json", claudeToolFallbackSuccessJSON()), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	payload := []byte(`{
		"model":"claude-opus-5",
		"max_tokens":32,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search"}]
	}`)
	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx,
		&cliproxyauth.Auth{
			Attributes: map[string]string{"api_key": "sk-ant-oat-tool-fallback", "base_url": "https://agentrouter.invalid"},
			Metadata:   claudeOAuthTestMetadata(),
		},
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if len(bodies) != 2 {
		t.Fatalf("upstream attempts = %d, want 2", len(bodies))
	}
	for index, body := range bodies {
		resigned, errResign := finalizeAnthropicMessagesBodyCCH(body, "")
		if errResign != nil {
			t.Fatalf("re-sign request %d: %v", index+1, errResign)
		}
		if !bytes.Equal(resigned, body) {
			t.Fatalf("request %d CCH is stale after body rewrite", index+1)
		}
	}
	if bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("retry body did not change after unsupported tool removal")
	}
	if got := gjson.GetBytes(bodies[1], "tools"); got.Exists() {
		t.Fatalf("retry tools = %s, want absent; body=%s", got.Raw, bodies[1])
	}
}

func TestClaudeExecutorUnsupportedServerToolFallbackWritesSeparateRequestLogAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	transportAttempts := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		transportAttempts++
		if transportAttempts == 1 {
			return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", claudeUnsupportedWebSearchRelayTraceError), nil
		}
		return claudeTestHTTPResponse(req, http.StatusOK, "application/json", claudeToolFallbackSuccessJSON()), nil
	})
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport))
	payload := []byte(`{"model":"claude-opus-5","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)

	_, errExecute := NewClaudeExecutor(&config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}).Execute(ctx,
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": "https://agentrouter.invalid"}},
		cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	requestLog, _ := ginCtx.Get("API_REQUEST")
	requestBytes, _ := requestLog.([]byte)
	requestText := string(requestBytes)
	if !strings.Contains(requestText, "=== API REQUEST 1 ===") || !strings.Contains(requestText, "=== API REQUEST 2 ===") {
		t.Fatalf("request log does not contain two attempts: %q", requestText)
	}
	responseLog, _ := ginCtx.Get("API_RESPONSE")
	responseBytes, _ := responseLog.([]byte)
	responseText := string(responseBytes)
	if !strings.Contains(responseText, "=== API RESPONSE 1 ===") || !strings.Contains(responseText, "Status: 400") ||
		!strings.Contains(responseText, "=== API RESPONSE 2 ===") || !strings.Contains(responseText, "Status: 200") {
		t.Fatalf("response log does not contain separate 400 and 200 attempts: %q", responseText)
	}
}

func TestClaudeExecutorOpenAIResponsesWebSearchFallbackReturnsTranslatedOutput(t *testing.T) {
	var mu sync.Mutex
	var upstreamBodies [][]byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		mu.Lock()
		upstreamBodies = append(upstreamBodies, bytes.Clone(body))
		attempt := len(upstreamBodies)
		mu.Unlock()
		if attempt == 1 {
			return claudeTestHTTPResponse(req, http.StatusBadRequest, "application/json", claudeUnsupportedWebSearchError), nil
		}
		return claudeTestHTTPResponse(req, http.StatusOK, "text/event-stream", claudeToolFallbackSuccessSSE()), nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	payload := []byte(`{
		"model":"opus5",
		"instructions":"answer briefly",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"say ok"}]}],
		"tools":[
			{"type":"web_search","name":"web_search"},
			{"type":"function","name":"lookup","description":"custom lookup","parameters":{"type":"object","properties":{}}}
		]
	}`)
	response, errExecute := NewClaudeExecutor(&config.Config{}).Execute(ctx,
		&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key", "base_url": "https://agentrouter.invalid"}},
		cliproxyexecutor.Request{Model: "opus5", Payload: payload},
		cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAIResponse,
			ResponseFormat:  sdktranslator.FormatOpenAIResponse,
			OriginalRequest: payload,
		},
	)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(response.Payload, "output.0.content.0.text").String(); got != "ok" {
		t.Fatalf("Responses output text = %q, want ok; payload=%s", got, response.Payload)
	}

	mu.Lock()
	bodies := append([][]byte(nil), upstreamBodies...)
	mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("upstream attempts = %d, want 2", len(bodies))
	}
	if got := gjson.GetBytes(bodies[0], "tools.0.type").String(); got != "web_search_20250305" {
		t.Fatalf("translated first tool type = %q, want web_search_20250305; body=%s", got, bodies[0])
	}
	if got := gjson.GetBytes(bodies[1], "tools.#").Int(); got != 1 {
		t.Fatalf("retry tool count = %d, want custom tool only; body=%s", got, bodies[1])
	}
	if got := gjson.GetBytes(bodies[1], "tools.0.name").String(); got != "lookup" {
		t.Fatalf("retry custom tool name = %q, want lookup; body=%s", got, bodies[1])
	}
}
