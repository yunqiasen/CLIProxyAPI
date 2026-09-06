package test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketReportsExhaustedBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		keys     []string
		finalKey string
	}{
		{"single credential", []string{"primary"}, "primary"},
		{"all credentials exhausted", []string{"primary", "secondary"}, "secondary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				_, _ = w.Write([]byte(`{"error":{"message":"Budget pool quota has been exhausted.","type":"bad_response_status_code","code":"bad_response_status_code"}}`))
			}), tc.keys)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
				t.Fatal(err)
			}
			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("quota exhaustion must be an explicit error, not a bare disconnect: %v", err)
			}
			var event struct {
				Type   string `json:"type"`
				Status int    `json:"status"`
				Error  struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err = json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "error" || event.Status != 402 || event.Error.Message != "Budget pool quota has been exhausted." {
				t.Fatalf("quota error contract lost: %s", raw)
			}
			if _, raw, err = conn.ReadMessage(); err == nil {
				t.Fatalf("unexpected event after terminal quota error: %s", raw)
			}
			item := fixture.waitForLog(t)
			if item.Get("success").Bool() || !item.Get("has_error").Bool() || item.Get("status").Int() != 402 || item.Get("provider").String() != "FixtureRelay" || item.Get("auth_id").String() != t.Name()+"-"+tc.finalKey || !strings.Contains(item.Get("error_preview").String(), "Budget pool quota has been exhausted.") {
				t.Fatalf("quota log contract lost: %s", item.Raw)
			}
		})
	}
}

type responsesQuotaFixture struct {
	server *httptest.Server
	model  string
}

func newResponsesQuotaFixture(t *testing.T, upstreamHandler http.Handler, keys []string) *responsesQuotaFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}, RequestLogRetentionDays: 7}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor.NewCodexExecutor(cfg))
	model := t.Name() + "-model"
	for _, key := range keys {
		auth := &coreauth.Auth{ID: t.Name() + "-" + key, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "fixture-" + key, "base_url": upstream.URL, "provider_name": "FixtureRelay"}}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	logsDir := t.TempDir()
	logHandler := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	logHandler.SetLogDirectory(logsDir)
	if err := logHandler.StartRequestLogIndex(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logHandler.Close(); err != nil {
			t.Error(err)
		}
	})
	router := gin.New()
	router.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(true, logsDir, logsDir, 0)))
	router.GET("/v0/management/request-logs", logHandler.GetRequestLogs)
	router.GET("/v1/responses", h.ResponsesWebsocket)
	router.POST("/v1/responses", h.Responses)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &responsesQuotaFixture{server: server, model: model}
}

func TestResponsesHTTPKeepsBudgetFailover(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, budgetFailoverUpstream(), []string{"primary", "secondary"})
	payload, _ := json.Marshal(map[string]any{"model": fixture.model, "input": "hello", "stream": true})
	resp, err := fixture.server.Client().Post(fixture.server.URL+"/v1/responses", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(body), "second key answer") || strings.Count(string(body), `"type":"response.completed"`) != 1 || strings.Contains(string(body), `"error"`) {
		t.Fatalf("successful credential fallback was interrupted: status=%d body=%s", resp.StatusCode, body)
	}
	item := fixture.waitForLog(t)
	if !item.Get("success").Bool() || item.Get("has_error").Bool() || item.Get("auth_id").String() != t.Name()+"-secondary" || item.Get("provider").String() != "FixtureRelay" || item.Get("error_preview").String() != "" || !strings.Contains(item.Get("output_preview").String(), "second key answer") {
		t.Fatalf("recovered request log describes the rejected key: %s", item.Raw)
	}

}

func (f *responsesQuotaFixture) waitForLog(t *testing.T) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastBody []byte
	for time.Now().Before(deadline) {
		resp, err := f.server.Client().Get(f.server.URL + "/v0/management/request-logs?limit=10")
		if err != nil {
			t.Fatal(err)
		}
		body, errRead := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if errRead != nil {
			t.Fatal(errRead)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("request log API status=%d body=%s", resp.StatusCode, body)
		}
		lastBody = body
		for _, item := range gjson.GetBytes(body, "items").Array() {
			if item.Get("model").String() == f.model {
				return item
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("completed request missing from public request-log API: %s", lastBody)
	return gjson.Result{}
}

func TestResponsesWebsocketKeepsPartialOutputOnBudgetFailure(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.Header.Get("Authorization") == "Bearer fixture-primary" {
			_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\ndata: {\"type\":\"error\",\"error\":{\"status_code\":402,\"message\":\"Budget pool quota has been exhausted.\",\"code\":\"bad_response_status_code\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"wrong-replay\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}), []string{"primary", "secondary"})
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(first, "delta").String() != "partial answer" {
		t.Fatalf("partial output lost: %s", first)
	}
	_, terminal, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("partial stream lost its quota reason: %v", err)
	}
	if gjson.GetBytes(terminal, "type").String() != "error" || gjson.GetBytes(terminal, "status").Int() != 402 {
		t.Fatalf("partial generation was replayed or mislabeled: %s", terminal)
	}
	if _, extra, err := conn.ReadMessage(); err == nil {
		t.Fatalf("extra generation after failure: %s", extra)
	}
	item := fixture.waitForLog(t)
	if item.Get("success").Bool() || item.Get("status").Int() != 402 || item.Get("auth_id").String() != t.Name()+"-primary" || !strings.Contains(item.Get("output_preview").String(), "partial answer") || !strings.Contains(item.Get("error_preview").String(), "Budget pool quota") {
		t.Fatalf("partial failure log is inaccurate: %s", item.Raw)
	}
}

func budgetFailoverUpstream() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fixture-primary" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"Budget pool quota has been exhausted.","code":"bad_response_status_code"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"second key answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"second key answer\"}]}]}}\n\n"))
	})
}

func TestResponsesWebsocketKeepsBudgetFailover(t *testing.T) {
	fixture := newResponsesQuotaFixture(t, budgetFailoverUpstream(), []string{"primary", "secondary"})
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(fixture.server.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if err = conn.WriteJSON(map[string]any{"type": "response.create", "model": fixture.model, "input": "hello"}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("recoverable budget error reached client: %v", err)
		}
		if gjson.GetBytes(raw, "type").String() == "error" {
			t.Fatalf("quota exposed before healthy credential tried: %s", raw)
		}
		output.WriteString(gjson.GetBytes(raw, "delta").String())
		if gjson.GetBytes(raw, "type").String() == "response.completed" {
			break
		}
	}
	if output.String() != "second key answer" {
		t.Fatalf("wrong recovered output: %q", output.String())
	}
	if err = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, raw, err := conn.ReadMessage(); err == nil {
		t.Fatalf("unexpected output after closing completed turn: %s", raw)
	}
	item := fixture.waitForLog(t)
	if !item.Get("success").Bool() || item.Get("has_error").Bool() || item.Get("status").Int() != 200 || item.Get("auth_id").String() != t.Name()+"-secondary" || item.Get("error_preview").String() != "" || !strings.Contains(item.Get("output_preview").String(), "second key answer") {
		t.Fatalf("completed websocket log blamed rejected credential: %s", item.Raw)
	}
}
