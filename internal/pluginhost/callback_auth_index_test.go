package pluginhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHostHTTPCallbackAuthIndexUsesSelectedCredentialProxy(t *testing.T) {
	global := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("global")) }))
	defer global.Close()
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("selected")) }))
	defer selected.Close()
	host := newHostWithRecords(capabilityRecord{id: "egress-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Executor: &fakeExecutor{identifier: "fixture-provider"}}}})
	host.runtimeConfig = &config.Config{SDKConfig: config.SDKConfig{ProxyURL: global.URL}}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	auth := &coreauth.Auth{ID: "fixture", Provider: "fixture-provider", ProxyURL: selected.URL}
	auth.EnsureIndex()
	if _, err := host.currentAuthManager().Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	instance := &hostCallbackInstance{}
	ctx := withHostCallbackIdentity(context.Background(), "egress-plugin", instance)
	raw, _ := json.Marshal(map[string]string{"method": "GET", "url": "http://upstream.example.test/catalog", "auth_index": auth.Index})
	response, err := host.callFromPlugin(ctx, pluginabi.MethodHostHTTPDo, raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeRPCEnvelope[pluginapi.HTTPResponse](response)
	if err != nil || string(result.Body) != "selected" {
		t.Fatalf("response=%+v error=%v; want selected credential proxy", result, err)
	}
}

func TestHostHTTPAuthIndexIsolationAndBoundPriority(t *testing.T) {
	for _, testCase := range []struct {
		name, provider                                                            string
		missingIdentity, closedInstance, foreignInstance, disabled, bound, stream bool
		wantErr                                                                   bool
	}{
		{name: "owned stream", provider: "fixture-provider", stream: true},
		{name: "foreign provider", provider: "other-provider", wantErr: true},
		{name: "missing plugin identity", provider: "fixture-provider", missingIdentity: true, wantErr: true},
		{name: "closed plugin", provider: "fixture-provider", closedInstance: true, wantErr: true},
		{name: "different instance", provider: "fixture-provider", foreignInstance: true, wantErr: true},
		{name: "disabled credential", provider: "fixture-provider", disabled: true, wantErr: true},
		{name: "bound request wins", provider: "other-provider", bound: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("selected")) }))
			defer proxy.Close()
			host := newHostWithRecords(capabilityRecord{id: "egress-plugin", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Executor: &fakeExecutor{identifier: "fixture-provider"}}}})
			host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
			auth := &coreauth.Auth{ID: "fixture", Provider: testCase.provider, ProxyURL: proxy.URL, Disabled: testCase.disabled}
			auth.EnsureIndex()
			if _, err := host.currentAuthManager().Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			instance := &hostCallbackInstance{}
			host.loaded["egress-plugin"] = &loadedPlugin{callbackInstance: instance}
			callerInstance := instance
			if testCase.foreignInstance {
				callerInstance = &hostCallbackInstance{}
			}
			if testCase.closedInstance {
				instance.closed.Store(true)
			}
			ctx := withHostCallbackIdentity(context.Background(), "egress-plugin", callerInstance)
			if testCase.missingIdentity {
				ctx = context.Background()
			}
			callbackID := ""
			if testCase.bound {
				callbackID, _ = host.openCallbackContextForPluginInstance(withCallbackHTTPClient(context.Background(), host.newHTTPClient(&coreauth.Auth{ProxyURL: proxy.URL})), "egress-plugin", instance)
			}
			raw, _ := json.Marshal(map[string]string{"method": "GET", "url": "http://upstream.example.test/catalog", "auth_index": auth.Index, "host_callback_id": callbackID})
			method := pluginabi.MethodHostHTTPDo
			if testCase.stream {
				method = pluginabi.MethodHostHTTPDoStream
			}
			response, err := host.callFromPlugin(ctx, method, raw)
			if testCase.wantErr {
				if err == nil {
					t.Fatal("invalid auth selection accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if testCase.stream {
				result, err := decodeRPCEnvelope[rpcHostHTTPStreamResponse](response)
				if err != nil || result.StatusCode != 200 || result.StreamID == "" {
					t.Fatalf("stream=%+v err=%v", result, err)
				}
				closeRaw, _ := json.Marshal(rpcHostHTTPStreamCloseRequest{StreamID: result.StreamID})
				_, _ = host.callFromPlugin(ctx, pluginabi.MethodHostHTTPStreamClose, closeRaw)
			} else {
				result, err := decodeRPCEnvelope[pluginapi.HTTPResponse](response)
				if err != nil || string(result.Body) != "selected" {
					t.Fatalf("response=%+v err=%v", result, err)
				}
			}
		})
	}
}
