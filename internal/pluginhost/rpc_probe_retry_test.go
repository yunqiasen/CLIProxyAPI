package pluginhost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestRPCProbeStopsRetriesAndPreservesCredentialAvailability(t *testing.T) {
	for _, status := range []int{503, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			record := normalizeTestCapabilityRecord(capabilityRecord{id: "probe-stop"})
			host := newHostWithRecords(record)
			calls := 0
			client := staticEnvelopePluginClient{raw: []byte(fmt.Sprintf(`{"ok":false,"error":{"code":"upstream_probe","message":"raw failure","http_status":%d,"request_stop":true}}`, status))}
			exec := &fakeExecutor{identifier: "probe-provider", execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
				calls++
				return callPlugin[pluginapi.ExecutorResponse](ctx, client, "executor.execute", req)
			}}
			adapter := newExecutorAdapterForRecordForTest(host, record, exec, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
			adapter.provider = "probe-provider"
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(adapter)
			manager.SetRetryConfig(3, time.Millisecond, 0)
			model := "probe-stop-model-" + fmt.Sprint(status)
			for _, id := range []string{"probe-a-" + fmt.Sprint(status), "probe-b-" + fmt.Sprint(status)} {
				if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: adapter.provider, Status: coreauth.StatusActive}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, adapter.provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			_, err := manager.Execute(context.Background(), []string{adapter.provider}, coreexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"ok"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
			stop, ok := err.(interface{ IsRequestStop() bool })
			if !ok || !stop.IsRequestStop() {
				t.Fatalf("RPC lost request stop: %T %v", err, err)
			}
			if calls != 1 {
				t.Fatalf("probe executed %d times", calls)
			}
			for _, auth := range manager.List() {
				if auth.Status != coreauth.StatusActive || auth.Unavailable {
					t.Fatalf("diagnostic poisoned credential: %+v", auth)
				}
			}
		})
	}
}
