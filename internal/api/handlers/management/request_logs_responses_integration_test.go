package management

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRequestLogResponsesMultiKeyWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		firstHTTP429 bool
		partial      bool
		wantCalls    []string
		wantSuccess  bool
		wantAuth     string
		wantOutput   string
	}{
		{"HTTP quota then second key succeeds", true, false, []string{"primary", "secondary"}, true, "2", "second key answer"},
		{"SSE quota before output then second key succeeds", false, false, []string{"primary", "secondary"}, true, "2", "second key answer"},
		{"SSE quota after output does not replay", false, true, []string{"primary"}, false, "1", "partial answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			calls := make(chan string, 8)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fixture-")
				calls <- key
				if key == "primary" {
					if tc.firstHTTP429 {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"fixture quota exhausted"}}`))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if tc.partial {
						_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial answer\"}\n\n"))
					}
					_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"fixture quota exhausted\"}}\n\n"))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"second key answer\"}\n\n"))
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-fixture\",\"status\":\"completed\",\"output\":[]}}\n\n"))
			}))
			defer upstream.Close()

			cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(executor.NewCodexExecutor(cfg))
			prefix := strings.ReplaceAll(t.Name(), "/", "-")
			model := prefix + "-model"
			for i, key := range []string{"primary", "secondary"} {
				id := fmt.Sprintf("%s-%d", prefix, i+1)
				auth := &coreauth.Auth{
					ID: id, Provider: "codex", Status: coreauth.StatusActive,
					Attributes: map[string]string{"api_key": "fixture-" + key, "base_url": upstream.URL, "provider_name": "FixtureRelay"},
				}
				if _, err := manager.Register(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			logsDir := t.TempDir()
			router := gin.New()
			router.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(true, logsDir, logsDir, 0)))
			h := openai.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router.POST("/v1/responses", h.Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, model)))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var gotCalls []string
			for len(calls) > 0 {
				gotCalls = append(gotCalls, <-calls)
			}
			if !reflect.DeepEqual(gotCalls, tc.wantCalls) {
				t.Fatalf("credential attempts=%v want=%v body=%q", gotCalls, tc.wantCalls, recorder.Body.String())
			}
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), tc.wantOutput) {
				t.Fatalf("downstream response status=%d body=%q", recorder.Code, recorder.Body.String())
			}
			wantFailures := 0
			if !tc.wantSuccess {
				wantFailures = 1
			}
			if count := strings.Count(recorder.Body.String(), `"type":"response.failed"`); count != wantFailures {
				t.Fatalf("terminal failure count=%d want=%d body=%q", count, wantFailures, recorder.Body.String())
			}

			candidates, err := collectRequestLogCandidates(logsDir)
			if err != nil || len(candidates) != 1 {
				t.Fatalf("request logs=%d error=%v", len(candidates), err)
			}
			candidate := candidates[0]
			rawBefore, err := os.ReadFile(candidate.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(rawBefore, []byte("fixture quota exhausted")) {
				t.Fatal("raw log lost the failed attempt")
			}
			parsed, err := parseRequestLogFile(candidate)
			if err != nil {
				t.Fatal(err)
			}
			wantAuth := prefix + "-" + tc.wantAuth
			if parsed.Success != tc.wantSuccess || parsed.HasError == tc.wantSuccess || parsed.AuthID != wantAuth || parsed.Provider != "FixtureRelay" || parsed.Model != model {
				t.Fatalf("log outcome success=%t has_error=%t auth=%q provider=%q model=%q error=%q", parsed.Success, parsed.HasError, parsed.AuthID, parsed.Provider, parsed.Model, parsed.error)
			}
			if !strings.Contains(parsed.output, tc.wantOutput) || (tc.wantSuccess && parsed.error != "") || (!tc.wantSuccess && !strings.Contains(parsed.error, "fixture quota exhausted")) {
				t.Fatalf("log output=%q error=%q", parsed.output, parsed.error)
			}
			store, err := openRequestLogStore(logsDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			if err := syncRequestLogStore(context.Background(), store, logsDir); err != nil {
				t.Fatal(err)
			}
			usage, err := store.apiKeyUsageByAuthID(context.Background(), time.Now(), nil)
			if err != nil {
				t.Fatal(err)
			}
			finalUsage := usage[wantAuth]
			if len(usage) != 1 || finalUsage.Failed != int64(wantFailures) || finalUsage.Success != int64(1-wantFailures) {
				t.Fatalf("request usage=%#v want only final credential with failures=%d", usage, wantFailures)
			}
			rawAfter, err := os.ReadFile(candidate.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rawBefore, rawAfter) {
				t.Fatal("indexing changed the raw log")
			}
		})
	}
}
