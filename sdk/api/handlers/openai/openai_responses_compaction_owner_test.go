package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cfg "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type compactOwnerFixture struct {
	decoded   chan []byte
	afterAuth bool
}

func (h *compactOwnerFixture) InterceptRequestBeforeAuth(_ context.Context, r pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if h.afterAuth {
		return pluginapi.RequestInterceptResponse{}
	}
	return h.decode(r)
}
func (h *compactOwnerFixture) decode(r pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if gjson.GetBytes(r.Body, `input.#(type=="compaction").encrypted_content`).String() == "verified-fixture" {
		h.decoded <- r.Body
		return pluginapi.RequestInterceptResponse{Body: []byte(`{"model":"compact-fixture-model","input":[{"role":"user","content":"decoded summary"}],"stream":true}`)}
	}
	return pluginapi.RequestInterceptResponse{}
}
func (h *compactOwnerFixture) InterceptRequestAfterAuth(_ context.Context, r pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if h.afterAuth {
		return h.decode(r)
	}
	return pluginapi.RequestInterceptResponse{}
}
func (*compactOwnerFixture) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}
func (*compactOwnerFixture) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

type compactOwnerExecutor struct {
	homeResponsesWebsocketExecutor
	seen chan []byte
}

func (*compactOwnerExecutor) Identifier() string { return "claude" }
func (e *compactOwnerExecutor) ExecuteStream(_ context.Context, _ *auth.Auth, r ex.Request, _ ex.Options) (*ex.StreamResult, error) {
	e.seen <- r.Payload
	chunks := make(chan ex.StreamChunk, 1)
	chunks <- ex.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"fixture_response","status":"completed","output":[]}}`)}
	close(chunks)
	return &ex.StreamResult{Chunks: chunks}, nil
}
func TestWebsocketCompactionReachesRegisteredOwnerBeforeFallback(t *testing.T) {
	t.Run("before-auth", func(t *testing.T) { testWebsocketCompactionOwner(t, false) })
	t.Run("after-auth", func(t *testing.T) { testWebsocketCompactionOwner(t, true) })
}
func testWebsocketCompactionOwner(t *testing.T, afterAuth bool) {
	manager := auth.NewManager(nil, nil, nil)
	executor := &compactOwnerExecutor{seen: make(chan []byte, 2)}
	manager.RegisterExecutor(executor)
	credential := &auth.Auth{ID: "compact-fixture-auth", Provider: "claude"}
	if _, err := manager.Register(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, "claude", []*registry.ModelInfo{{ID: "compact-fixture-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	owner := &compactOwnerFixture{decoded: make(chan []byte, 1), afterAuth: afterAuth}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig{}, manager)
	base.PluginHost = owner
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, raw := range []string{`{"type":"response.create","model":"compact-fixture-model","input":[{"role":"user","content":"OLD_HISTORY"}]}`, `{"type":"response.create","model":"compact-fixture-model","input":[{"type":"compaction","encrypted_content":"verified-fixture"},{"role":"user","content":"latest"}]}`} {
		if err = conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			t.Fatal(err)
		}
		for {
			_, reply, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatal(errRead)
			}
			kind := gjson.GetBytes(reply, "type").String()
			if kind == "error" {
				t.Fatalf("request error: %s", reply)
			}
			if kind == "response.completed" {
				break
			}
		}
	}
	select {
	case raw := <-owner.decoded:
		if strings.Contains(string(raw), "OLD_HISTORY") {
			t.Error("stale transcript restored before owner")
		}
	default:
		t.Fatal("compaction stripped before owner")
	}
	<-executor.seen
	if raw := <-executor.seen; !strings.Contains(string(raw), "decoded summary") {
		t.Errorf("decoded history not forwarded: %s", raw)
	}
	// A registered unrelated interceptor is not proof of ownership. An unknown
	// opaque item must not silently disappear in a function-only translator.
	if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"compact-fixture-model","input":[{"type":"compaction","id":"cmp_local_untrusted","encrypted_content":"unknown-fixture"}]}`)); err != nil {
		t.Fatal(err)
	}
	for {
		_, reply, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		kind := gjson.GetBytes(reply, "type").String()
		if kind == "response.completed" {
			t.Fatal("unhandled compaction accepted by unsupported executor")
		}
		if kind == "error" {
			break
		}
	}
	select {
	case <-executor.seen:
		t.Fatal("unhandled compaction reached unsupported executor")
	default:
	}

}
