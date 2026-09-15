package helps

import (
	"context"
	"fmt"
	"sync"
	"testing"

	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestRouteStateOwnershipBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                                                                        string
		failed, canceled, otherCaller, unknown, otherModel, otherEndpoint, volatile bool
		wantStrip                                                                   bool
	}{
		{name: "key change", wantStrip: true}, {name: "model change", otherModel: true, wantStrip: true}, {name: "endpoint change", otherEndpoint: true, wantStrip: true},
		{name: "failed", failed: true}, {name: "canceled", canceled: true}, {name: "tenant isolation", otherCaller: true}, {name: "unknown", unknown: true}, {name: "metadata refresh", volatile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &auth.Auth{ID: "a", Provider: "codex", Attributes: map[string]string{"base_url": "https://a.example/v1", "api_key": "a"}, Metadata: map[string]any{"last_refresh": "yesterday"}}
			scope := t.Name()
			body := []byte(`{"previous_response_id":"resp_keep","input":[{"type":"reasoning","id":"rs","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"keep"}]},{"type":"compaction","encrypted_content":"compact_keep"},{"type":"function_call","call_id":"call","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"call","output":"ok"}]}`)
			_, state := PrepareCodexRouteState(a, "model-a", scope, body)
			status := "completed"
			if tc.failed {
				status = "failed"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			if !tc.unknown {
				state.Completed(ctx, []byte(fmt.Sprintf(`{"type":"response.%s","response":{"status":"%s","output":[{"type":"reasoning","encrypted_content":"opaque"}]}}`, status, status)))
			}
			b := a.Clone()
			model := "model-a"
			switch {
			case tc.otherModel:
				model = "model-b"
			case tc.otherEndpoint:
				b.Attributes["base_url"] = "https://b.example/v1"
			case tc.volatile:
				b.Metadata["last_refresh"] = "today"
			default:
				b.ID = "b"
				b.Attributes["api_key"] = "b"
			}
			if tc.otherCaller {
				scope += "-another-caller"
			}
			got, _ := PrepareCodexRouteState(b, model, scope, body)
			stripped := !gjson.GetBytes(got, "input.0.encrypted_content").Exists()
			if stripped != tc.wantStrip {
				t.Fatalf("stripped=%v want=%v: %s", stripped, tc.wantStrip, got)
			}
			for _, path := range []string{"previous_response_id", "input.0.summary", "input.1", "input.2", "input.3"} {
				if gjson.GetBytes(got, path).Raw != gjson.GetBytes(body, path).Raw {
					t.Errorf("changed portable field %s", path)
				}
			}
		})
	}
}

func TestRouteStateConcurrentCompletionsRetainIndividualOwners(t *testing.T) {
	scope := t.Name()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := &auth.Auth{ID: fmt.Sprint(i)}
			_, s := PrepareCodexRouteState(a, "model", scope, nil)
			s.Completed(context.Background(), []byte(fmt.Sprintf(`{"type":"response.completed","response":{"output":[{"type":"reasoning","encrypted_content":"state-%d"}]}}`, i)))
		}(i)
	}
	wg.Wait()
	for i := 0; i < 32; i++ {
		a := &auth.Auth{ID: fmt.Sprint(i)}
		b := []byte(fmt.Sprintf(`{"input":[{"type":"reasoning","encrypted_content":"state-%d"}]}`, i))
		got, _ := PrepareCodexRouteState(a, "model", scope, b)
		if string(got) != string(b) {
			t.Fatalf("concurrent owner displaced: %s", got)
		}
	}
}
