package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type egressCallbackPluginClient struct {
	host       *Host
	identity   context.Context
	stream     bool
	body       string
	callbackID string
	operation  bool
}

func (c *egressCallbackPluginClient) Shutdown() {}

func (c *egressCallbackPluginClient) Call(_ context.Context, _ string, request []byte) ([]byte, error) {
	var req struct {
		HostCallbackID string `json:"host_callback_id"`
	}
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	c.callbackID = req.HostCallbackID
	if c.callbackID == "" {
		return nil, fmt.Errorf("missing callback scope")
	}
	operationID := ""
	if c.operation {
		rawOpen, _ := json.Marshal(rpcHostHTTPOperationOpenRequest{HostCallbackID: c.callbackID})
		rawOperation, errOpen := c.host.callFromPlugin(c.identity, pluginabi.MethodHostHTTPOperationOpen, rawOpen)
		if errOpen != nil {
			return nil, errOpen
		}
		opened, errDecode := decodeRPCEnvelope[rpcHostHTTPOperationOpenResponse](rawOperation)
		if errDecode != nil {
			return nil, errDecode
		}
		operationID = opened.OperationID
	}
	rawRequest, errMarshal := json.Marshal(rpcHostHTTPRequest{HostCallbackID: c.callbackID, OperationID: operationID, Method: http.MethodGet, URL: "http://upstream.example.test/catalog"})
	if errMarshal != nil {
		return nil, errMarshal
	}
	method := pluginabi.MethodHostHTTPDo
	if c.stream {
		method = pluginabi.MethodHostHTTPDoStream
	}
	rawResponse, errCall := c.host.callFromPlugin(c.identity, method, rawRequest)
	if errCall != nil {
		return nil, errCall
	}
	if !c.stream {
		response, errDecode := decodeRPCEnvelope[pluginapi.HTTPResponse](rawResponse)
		if errDecode != nil {
			return nil, errDecode
		}
		c.body = string(response.Body)
	} else {
		response, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamResponse](rawResponse)
		if errDecode != nil {
			return nil, errDecode
		}
		rawRead, _ := json.Marshal(rpcHostHTTPStreamReadRequest{StreamID: response.StreamID})
		for {
			rawChunk, errRead := c.host.callFromPlugin(c.identity, pluginabi.MethodHostHTTPStreamRead, rawRead)
			if errRead != nil {
				return nil, errRead
			}
			chunk, errDecode := decodeRPCEnvelope[rpcHostHTTPStreamReadResponse](rawChunk)
			if errDecode != nil {
				return nil, errDecode
			}
			c.body += string(chunk.Payload)
			if chunk.Done {
				break
			}
		}
	}
	return marshalRPCResult(rpcExecutorStreamResponse{Chunks: []pluginapi.ExecutorStreamChunk{{Payload: []byte("done")}}})
}

func TestRPCExecuteRetainsCredentialHTTPClient(t *testing.T) {
	globalProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("global")) }))
	defer globalProxy.Close()
	authProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("credential")) }))
	defer authProxy.Close()
	host := New()
	host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalProxy.URL}}
	instance := &hostCallbackInstance{}
	client := &egressCallbackPluginClient{host: host, identity: withHostCallbackIdentity(context.Background(), "egress-plugin", instance)}
	adapter := &rpcPluginAdapter{id: "egress-plugin", instance: instance, host: host, client: client}
	_, errExecute := adapter.Execute(context.Background(), pluginapi.ExecutorRequest{HTTPClient: host.newHTTPClient(&coreauth.Auth{ProxyURL: authProxy.URL})})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if client.body != "credential" {
		t.Fatalf("callback egress = %q, want credential rather than global", client.body)
	}
}

func TestRPCAccountCallbacksRetainSuppliedHTTPClient(t *testing.T) {
	methods := []struct {
		name string
		call func(context.Context, *rpcPluginAdapter, pluginapi.HostHTTPClient) error
	}{
		{"execute", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.Execute(ctx, pluginapi.ExecutorRequest{HTTPClient: client})
			return err
		}},
		{"execute_stream", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			stream, err := a.ExecuteStream(ctx, pluginapi.ExecutorRequest{HTTPClient: client, Stream: true})
			if err == nil {
				for range stream.Chunks {
				}
			}
			return err
		}},
		{"models_for_auth", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.ModelsForAuth(ctx, pluginapi.AuthModelRequest{HTTPClient: client})
			return err
		}},
		{"quota_fetch", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := (rpcQuotaProvider{rpcPluginAdapter: a}).FetchQuota(ctx, pluginapi.QuotaFetchRequest{HTTPClient: client})
			return err
		}},
		{"quota_reset", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := (rpcQuotaProvider{rpcPluginAdapter: a}).ResetQuota(ctx, pluginapi.QuotaResetRequest{HTTPClient: client})
			return err
		}},
		{"count_tokens", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.CountTokens(ctx, pluginapi.ExecutorRequest{HTTPClient: client})
			return err
		}},
		{"http_request", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.HttpRequest(ctx, pluginapi.ExecutorHTTPRequest{HTTPClient: client})
			return err
		}},
		{"auth_refresh", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.RefreshAuth(ctx, pluginapi.AuthRefreshRequest{HTTPClient: client})
			return err
		}},
		{"login_start", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.StartLogin(ctx, pluginapi.AuthLoginStartRequest{HTTPClient: client})
			return err
		}},
		{"login_poll", func(ctx context.Context, a *rpcPluginAdapter, client pluginapi.HostHTTPClient) error {
			_, err := a.PollLogin(ctx, pluginapi.AuthLoginPollRequest{HTTPClient: client})
			return err
		}},
	}
	proxy := func(body string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		t.Cleanup(server.Close)
		return server
	}
	globalProxy, authProxy, requestProxy := proxy("global"), proxy("credential"), proxy("request")
	for _, method := range methods {
		for _, streaming := range []bool{false, true} {
			for _, policy := range []string{"credential", "request", "default"} {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", method.name, streaming, policy), func(t *testing.T) {
					host := New()
					host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{ProxyURL: globalProxy.URL}}
					instance := &hostCallbackInstance{}
					client := &egressCallbackPluginClient{host: host, identity: withHostCallbackIdentity(context.Background(), "egress-plugin", instance), stream: streaming}
					adapter := &rpcPluginAdapter{id: "egress-plugin", instance: instance, host: host, client: client}
					var httpClient pluginapi.HostHTTPClient
					want := policy
					switch policy {
					case "credential":
						httpClient = host.newHTTPClient(&coreauth.Auth{ProxyURL: authProxy.URL})
					case "request":
						httpClient = host.newHTTPClientWithProxy(&coreauth.Auth{ProxyURL: authProxy.URL}, requestProxy.URL)
					case "default":
						want = "global"
					}
					if errCall := method.call(context.Background(), adapter, httpClient); errCall != nil {
						t.Fatal(errCall)
					}
					if client.body != want {
						t.Fatalf("callback egress = %q, want %q", client.body, want)
					}
				})
			}
		}
	}
}

func TestRPCExplicitHTTPOperationRetainsBoundClient(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("credential")) }))
			defer proxy.Close()
			host := New()
			instance := &hostCallbackInstance{}
			client := &egressCallbackPluginClient{host: host, identity: withHostCallbackIdentity(context.Background(), "egress-plugin", instance), operation: true, stream: streaming}
			adapter := &rpcPluginAdapter{id: "egress-plugin", host: host, instance: instance, client: client}
			if _, err := adapter.Execute(context.Background(), pluginapi.ExecutorRequest{HTTPClient: host.newHTTPClient(&coreauth.Auth{ProxyURL: proxy.URL})}); err != nil {
				t.Fatal(err)
			}
			if client.body != "credential" {
				t.Fatalf("explicit operation egress = %q", client.body)
			}
			// RPC completion closes the scope. Its client must not remain available.
			raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: client.callbackID, Method: http.MethodGet, URL: "http://upstream.example.test/catalog"})
			if _, err := host.callFromPlugin(client.identity, pluginabi.MethodHostHTTPDo, raw); err == nil {
				t.Fatal("completed callback retained access to the bound client")
			}
		})
	}
}

func TestBoundHTTPClientRejectsForeignPluginAndInstance(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, foreignInstance := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/foreign_instance=%v", streaming, foreignInstance), func(t *testing.T) {
				requests := make(chan struct{}, 1)
				proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests <- struct{}{}
					_, _ = w.Write([]byte("unexpected"))
				}))
				defer proxy.Close()
				host := New()
				owner := &hostCallbackInstance{}
				ctx := withCallbackHTTPClient(context.Background(), host.newHTTPClient(&coreauth.Auth{ProxyURL: proxy.URL}))
				callbackID, closeCallback := host.openCallbackContextForPluginInstance(ctx, "owner", owner)
				defer closeCallback()
				caller := withHostCallbackIdentity(context.Background(), "other", owner)
				if foreignInstance {
					caller = withHostCallbackIdentity(context.Background(), "owner", &hostCallbackInstance{})
				}
				raw, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: callbackID, Method: http.MethodGet, URL: "http://upstream.example.test/catalog"})
				method := pluginabi.MethodHostHTTPDo
				if streaming {
					method = pluginabi.MethodHostHTTPDoStream
				}
				if _, err := host.callFromPlugin(caller, method, raw); err == nil {
					t.Fatal("foreign callback accessed the bound HTTP client")
				}
				select {
				case <-requests:
					t.Fatal("foreign callback reached credential proxy")
				default:
				}
			})
		}
	}
}

func TestRPCAsyncStreamRetainsHTTPClientUntilClose(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("credential")) }))
	defer proxy.Close()
	host := New()
	instance := &hostCallbackInstance{}
	plugin := newStreamCallbackPluginClient()
	adapter := &rpcPluginAdapter{id: "async-plugin", host: host, instance: instance, client: plugin}
	stream, errStream := adapter.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true, HTTPClient: host.newHTTPClient(&coreauth.Auth{ProxyURL: proxy.URL})})
	if errStream != nil {
		t.Fatal(errStream)
	}
	identity := withHostCallbackIdentity(context.Background(), "async-plugin", instance)
	rawRequest, _ := json.Marshal(rpcHostHTTPRequest{HostCallbackID: plugin.callbackID, Method: http.MethodGet, URL: "http://upstream.example.test/catalog"})
	rawResponse, errCall := host.callFromPlugin(identity, pluginabi.MethodHostHTTPDo, rawRequest)
	if errCall != nil {
		t.Fatal(errCall)
	}
	response, errDecode := decodeRPCEnvelope[pluginapi.HTTPResponse](rawResponse)
	if errDecode != nil || string(response.Body) != "credential" {
		t.Fatalf("async callback response = %+v, error = %v", response, errDecode)
	}
	rawClose, _ := json.Marshal(rpcStreamCloseRequest{StreamID: plugin.streamID})
	if _, errClose := host.callFromPlugin(identity, pluginabi.MethodHostStreamClose, rawClose); errClose != nil {
		t.Fatal(errClose)
	}
	for range stream.Chunks {
	}
	if _, errCall := host.callFromPlugin(identity, pluginabi.MethodHostHTTPDo, rawRequest); errCall == nil {
		t.Fatal("closed stream retained the callback client")
	}
}
