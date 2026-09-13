package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexRecoveryHTTPUnchangedGroupedDraft(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name          string
		excluded      []string
		draftExcluded any
		draftHeaders  map[string]string
		draftModels   any
		recover       bool
	}{
		{name: "nil_excluded", recover: true},
		{name: "empty_excluded", excluded: []string{}, recover: true},
		{name: "same_excluded", excluded: []string{"other-model"}, draftExcluded: []string{"other-model"}, recover: true},
		{name: "removed_exclusion", excluded: []string{"other-model"}, recover: false},
		{name: "added_exclusion", draftExcluded: []string{"other-model"}, recover: false},
		{name: "changed_headers", draftHeaders: map[string]string{"X-Changed": "yes"}, recover: false},
		{name: "cleared_models", draftModels: []map[string]string{}, recover: false},
		{name: "changed_model_mapping", draftModels: []map[string]string{{"name": "different-upstream", "alias": "public"}}, recover: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer selected-key" {
					t.Error("probe used unselected credential")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"probe\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer upstream.Close()
			entry := config.CodexKey{BaseURL: upstream.URL, APIKeyEntries: []config.NativeAPIKeyEntry{{APIKey: "selected-key"}, {APIKey: "other-key"}}, Models: []config.CodexModel{{Name: "upstream", Alias: "public"}}}
			entry.ExcludedModels = tc.excluded
			cfg := &config.Config{CodexKey: []config.CodexKey{entry}}
			m := coreauth.NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			synth := &synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()}
			var selected, other *coreauth.Auth
			for i, key := range entry.GetEffectiveAPIKeys() {
				a := synthesizer.SynthesizeCodexAuth(synth, entry, key, 0)
				if _, err := m.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				m.MarkResult(context.Background(), coreauth.Result{AuthID: a.ID, Model: "public", Error: &coreauth.Error{HTTPStatus: 402, Message: "budget exhausted"}})
				if i == 0 {
					selected = a
				} else {
					other = a
				}
			}
			h := &Handler{cfg: cfg, authManager: m}
			draftModels := tc.draftModels
			if draftModels == nil {
				draftModels = []map[string]string{{"name": "upstream", "alias": "public"}}
			}
			payload := map[string]any{"provider": "codex", "auth_index": selected.EnsureIndex(), "model": "public", "header": tc.draftHeaders, "codex_config": map[string]any{"base-url": upstream.URL, "models": draftModels, "excluded-models": tc.draftExcluded}}
			data, errMarshal := json.Marshal(payload)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/provider-connectivity-test", bytes.NewReader(data))
			h.ProviderConnectivityTest(c)
			var result providerConnectivityTestResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || result.StatusCode != 200 {
				t.Fatalf("probe failed: management=%d upstream=%d", w.Code, result.StatusCode)
			}
			got, _ := m.GetByID(selected.ID)
			if recovered := !got.ModelStates["public"].Unavailable; recovered != tc.recover {
				t.Fatalf("recovered=%v want=%v", recovered, tc.recover)
			}
			gotOther, _ := m.GetByID(other.ID)
			if !gotOther.ModelStates["public"].Unavailable {
				t.Fatal("other key was resumed")
			}
		})
	}
}
