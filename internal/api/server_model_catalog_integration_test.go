package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

// registerCatalogFixtures registers deterministic models and cleans up.
func registerCatalogFixtures(t *testing.T, clientID, provider string, models ...*registry.ModelInfo) {
	t.Helper()
	registry.GetGlobalRegistry().RegisterClient(clientID, provider, models)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })
}

// catalogRequest sends an HTTP request through the test server engine.
func catalogRequest(t *testing.T, server *Server, method, path, body, key string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	return rec
}

// extractIDs returns the "id" (or "slug"/"name") field values from a JSON array.
func extractIDs(t *testing.T, body []byte, arrayKey string, field string) []string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal catalog: %v\n%s", err, body)
	}
	raw, ok := doc[arrayKey]
	if !ok {
		t.Fatalf("missing key %q in catalog: %s", arrayKey, body)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal %s array: %v\n%s", arrayKey, err, body)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		var id string
		if json.Unmarshal(row[field], &id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// containsID checks if id appears in ids.
func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// --- Focus 1: Global policy across two keys and all catalog formats ---

func TestCatalogGlobalPolicyHidesAcrossFormats(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key-a, native-key-b]
client:
  model-catalog:
    hidden: [claude-native-hidden]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)

	// Use claude- prefix to avoid Claude cloaking rewrites.
	registerCatalogFixtures(t, "native-catalog-fixture", "openai",
		&registry.ModelInfo{ID: "claude-native-visible", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-native-hidden", Object: "model", OwnedBy: "test"},
	)

	for _, tc := range []struct {
		name      string
		key       string
		path      string
		headers   []string
		keyName   string
		field     string
		hiddenID  string
		visibleID string
	}{
		{"openai-key-a", "native-key-a", "/v1/models", nil, "data", "id", "claude-native-hidden", "claude-native-visible"},
		{"openai-key-b", "native-key-b", "/v1/models", nil, "data", "id", "claude-native-hidden", "claude-native-visible"},
		{"claude", "native-key-a", "/v1/models", []string{"Anthropic-Version", "2023-06-01"}, "data", "id", "claude-native-hidden", "claude-native-visible"},
		{"gemini", "native-key-a", "/v1beta/models", nil, "models", "name", "models/claude-native-hidden", "models/claude-native-visible"},
		{"codex", "native-key-a", "/v1/models?client_version=cpa", nil, "models", "slug", "claude-native-hidden", "claude-native-visible"},
		{"grok", "native-key-a", "/v1/models", []string{"User-Agent", "grok-shell/0.2.119"}, "data", "id", "claude-native-hidden", "claude-native-visible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := catalogRequest(t, server, "GET", tc.path, "", tc.key, tc.headers...)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			ids := extractIDs(t, rec.Body.Bytes(), tc.keyName, tc.field)
			if containsID(ids, tc.hiddenID) {
				t.Errorf("hidden model appeared in %s catalog: %v", tc.name, ids)
			}
			if !containsID(ids, tc.visibleID) {
				t.Errorf("visible model missing from %s catalog: %v", tc.name, ids)
			}
		})
	}
}

func TestCatalogClaudeBoundaryFieldsRecomputed(t *testing.T) {
	// Hide the second model; first_id/last_id must reflect the survivor.
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
client:
  model-catalog:
    hidden: [claude-boundary-b]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	registerCatalogFixtures(t, "native-claude-boundary", "claude",
		&registry.ModelInfo{ID: "claude-boundary-a", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-boundary-b", Object: "model", OwnedBy: "test"},
	)

	rec := catalogRequest(t, server, "GET", "/v1/models", "", "native-key", "Anthropic-Version", "2023-06-01")
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var doc struct {
		Data    []struct{ ID string `json:"id"` } `json:"data"`
		FirstID string                              `json:"first_id"`
		LastID  string                              `json:"last_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Data) != 1 || doc.Data[0].ID != "claude-boundary-a" {
		t.Fatalf("expected only claude-boundary-a, got: %+v", doc.Data)
	}
	if doc.FirstID != "claude-boundary-a" || doc.LastID != "claude-boundary-a" {
		t.Fatalf("boundary IDs wrong: first=%q last=%q", doc.FirstID, doc.LastID)
	}
}

func TestCatalogAllHiddenProducesEmptyArrays(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
client:
  model-catalog:
    hidden: ["*"]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	registerCatalogFixtures(t, "native-all-hidden", "openai",
		&registry.ModelInfo{ID: "claude-all-1", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-all-2", Object: "model", OwnedBy: "test"},
	)

	for _, tc := range []struct {
		name    string
		path    string
		headers []string
		keyName string
	}{
		{"openai", "/v1/models", nil, "data"},
		{"claude", "/v1/models", []string{"Anthropic-Version", "2023-06-01"}, "data"},
		{"gemini", "/v1beta/models", nil, "models"},
		{"grok", "/v1/models", []string{"User-Agent", "grok-shell/0.2.119"}, "data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := catalogRequest(t, server, "GET", tc.path, "", "native-key", tc.headers...)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			raw, ok := doc[tc.keyName]
			if !ok {
				t.Fatalf("missing key %q: %s", tc.keyName, rec.Body.String())
			}
			// Must be an array, not null.
			if string(raw) == "null" {
				t.Fatalf("%s array is null, expected []", tc.keyName)
			}
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.keyName, err)
			}
			if len(arr) != 0 {
				t.Fatalf("%s array not empty: %d items", tc.keyName, len(arr))
			}
		})
	}

	// Claude boundary fields must be empty strings when all hidden.
	rec := catalogRequest(t, server, "GET", "/v1/models", "", "native-key", "Anthropic-Version", "2023-06-01")
	var claudeDoc struct {
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &claudeDoc); err != nil {
		t.Fatal(err)
	}
	if claudeDoc.FirstID != "" || claudeDoc.LastID != "" {
		t.Fatalf("empty catalog boundary IDs should be empty: first=%q last=%q", claudeDoc.FirstID, claudeDoc.LastID)
	}
}

func TestCatalogHiddenNamesRemainInManagementInventory(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
remote-management: {secret-key: native-mgmt}
client:
  model-catalog:
    hidden: [claude-inv-hidden]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))
	registerCatalogFixtures(t, "native-inventory", "openai",
		&registry.ModelInfo{ID: "claude-inv-visible", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-inv-hidden", Object: "model", OwnedBy: "test"},
	)

	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("inventory status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view struct {
		Entries []struct {
			ID     string `json:"id"`
			Hidden bool   `json:"hidden"`
		} `json:"entries"`
		Counts struct {
			Total  int `json:"total"`
			Hidden int `json:"hidden"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	foundHidden := false
	for _, e := range view.Entries {
		if e.ID == "claude-inv-hidden" {
			if !e.Hidden {
				t.Errorf("hidden model %q not marked hidden in inventory", e.ID)
			}
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Errorf("hidden model not in inventory entries")
	}
	if view.Counts.Total < 2 || view.Counts.Hidden < 1 {
		t.Fatalf("counts wrong: %+v", view.Counts)
	}
}

// --- Focus 2: Direct requests to hidden models succeed against mock upstream ---

func TestCatalogHiddenModelDirectRequestsSucceed(t *testing.T) {
	// Concurrency-safe capture of upstream requests.
	var mu sync.Mutex
	var receivedModel string
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedPath = r.URL.Path
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		receivedModel = payload.Model
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/embeddings") {
			_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/rerank") {
			_, _ = io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.9}]}`)
			return
		}
		// Default: chat completions response (used by /v1/responses translation).
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	yamlContent := fmt.Sprintf(`openai-compatibility:
- name: native-direct-fixture
  base-url: %s/v1
  api-key-entries: [{api-key: upstream-key}]
  models:
  - {name: embed-model, alias: embed-alias, type: embeddings}
  - {name: rerank-model, alias: rerank-alias, type: rerank}
  - {name: chat-model, alias: chat-alias}
`, upstream.URL)

	// --- Before hiding: direct requests succeed ---
	cfgBefore, err := yamlToConfig(yamlContent)
	if err != nil {
		t.Fatal(err)
	}
	serverBefore := newRetrievalServer(t, cfgBefore)

	// Embeddings
	rec := catalogRequest(t, serverBefore, "POST", "/v1/embeddings", `{"model":"embed-alias","input":"test"}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("embeddings before hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var embedResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &embedResp); err != nil {
		t.Fatalf("embeddings before hide parse: %v body=%s", err, rec.Body.String())
	}
	if len(embedResp.Data) == 0 || len(embedResp.Data[0].Embedding) == 0 {
		t.Fatalf("embeddings before hide no vector: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel := receivedModel
	gotPath := receivedPath
	mu.Unlock()
	if gotModel != "embed-model" {
		t.Errorf("embeddings upstream model: got %q want embed-model", gotModel)
	}

	// Rerank
	rec = catalogRequest(t, serverBefore, "POST", "/v1/rerank", `{"model":"rerank-alias","query":"q","documents":["d"],"top_n":1}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("rerank before hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var rerankResp struct {
		Results []struct {
			Index         int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rerankResp); err != nil {
		t.Fatalf("rerank before hide parse: %v body=%s", err, rec.Body.String())
	}
	if len(rerankResp.Results) == 0 || rerankResp.Results[0].RelevanceScore == 0 {
		t.Fatalf("rerank before hide no result: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel = receivedModel
	mu.Unlock()
	if gotModel != "rerank-model" {
		t.Errorf("rerank upstream model: got %q want rerank-model", gotModel)
	}

	// Responses (non-streaming)
	rec = catalogRequest(t, serverBefore, "POST", "/v1/responses", `{"model":"chat-alias","input":"hello"}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("responses before hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("responses before hide no completed output: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel = receivedModel
	mu.Unlock()
	if gotModel != "chat-model" {
		t.Errorf("responses upstream model: got %q want chat-model", gotModel)
	}

	// --- After hiding: same requests still succeed ---
	cfgAfter, err := yamlToConfig(yamlContent + "client:\n  model-catalog:\n    hidden: [embed-alias, rerank-alias, chat-alias]\n")
	if err != nil {
		t.Fatal(err)
	}
	serverAfter := newRetrievalServer(t, cfgAfter)

	// Verify models are hidden from discovery.
	rec = catalogRequest(t, serverAfter, "GET", "/v1/models", "", "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("models list after hide: status=%d", rec.Code)
	}
	for _, hidden := range []string{"embed-alias", "rerank-alias", "chat-alias"} {
		if strings.Contains(rec.Body.String(), hidden) {
			t.Errorf("hidden model %q appeared in discovery: %s", hidden, rec.Body.String())
		}
	}

	// Embeddings to hidden model still succeed.
	rec = catalogRequest(t, serverAfter, "POST", "/v1/embeddings", `{"model":"embed-alias","input":"test"}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("embeddings after hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &embedResp); err != nil {
		t.Fatalf("embeddings after hide parse: %v", err)
	}
	if len(embedResp.Data) == 0 || len(embedResp.Data[0].Embedding) == 0 {
		t.Fatalf("embeddings after hide no vector: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel = receivedModel
	gotPath = receivedPath
	mu.Unlock()
	if gotModel != "embed-model" {
		t.Errorf("embeddings after hide upstream model: got %q want embed-model", gotModel)
	}
	if gotPath != "/v1/embeddings" {
		t.Errorf("embeddings after hide upstream path: got %q want /v1/embeddings", gotPath)
	}

	// Rerank to hidden model still succeeds.
	rec = catalogRequest(t, serverAfter, "POST", "/v1/rerank", `{"model":"rerank-alias","query":"q","documents":["d"],"top_n":1}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("rerank after hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rerankResp); err != nil {
		t.Fatalf("rerank after hide parse: %v", err)
	}
	if len(rerankResp.Results) == 0 || rerankResp.Results[0].RelevanceScore == 0 {
		t.Fatalf("rerank after hide no result: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel = receivedModel
	mu.Unlock()
	if gotModel != "rerank-model" {
		t.Errorf("rerank after hide upstream model: got %q want rerank-model", gotModel)
	}

	// Responses to hidden model still succeeds.
	rec = catalogRequest(t, serverAfter, "POST", "/v1/responses", `{"model":"chat-alias","input":"hello"}`, "retrieval-client")
	if rec.Code != 200 {
		t.Fatalf("responses after hide: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Errorf("responses after hide no completed output: %s", rec.Body.String())
	}
	mu.Lock()
	gotModel = receivedModel
	mu.Unlock()
	if gotModel != "chat-model" {
		t.Errorf("responses after hide upstream model: got %q want chat-model", gotModel)
	}

	// Unknown model is blocked.
	rec = catalogRequest(t, serverAfter, "POST", "/v1/embeddings", `{"model":"nonexistent-model","input":"test"}`, "retrieval-client")
	if rec.Code == 200 {
		t.Errorf("unknown model should not succeed")
	}
}

// yamlToConfig parses YAML into a Config.
func yamlToConfig(yamlContent string) (*config.Config, error) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(yamlContent), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// --- Focus 3: Management endpoints ---

func TestCatalogManagementAuthRequired(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [native-key]\nremote-management: {secret-key: native-mgmt}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))

	// No auth -> 401.
	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "")
	if rec.Code != 401 {
		t.Fatalf("GET inventory without auth: status=%d", rec.Code)
	}
	// Wrong key -> 401.
	rec = catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "wrong-password")
	if rec.Code != 401 {
		t.Fatalf("GET inventory wrong auth: status=%d", rec.Code)
	}
	// POST preview without auth -> 401.
	rec = catalogRequest(t, server, "POST", "/v8/management/models/catalog/preview", `{"format":"openai","policy":{"hidden":[],"order":"preserve","pinned":[]}}`, "")
	if rec.Code != 401 {
		t.Fatalf("POST preview without auth: status=%d", rec.Code)
	}
}

func TestCatalogManagementInventoryWireView(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
remote-management: {secret-key: native-mgmt}
client:
  model-catalog:
    hidden: [claude-wire-hidden]
    pinned: [claude-wire-visible]
    order: preserve
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))
	registerCatalogFixtures(t, "native-wire", "openai",
		&registry.ModelInfo{ID: "claude-wire-visible", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-wire-hidden", Object: "model", OwnedBy: "test"},
	)

	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("inventory: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view struct {
		Format  string `json:"format"`
		Policy  struct {
			Order  string   `json:"order"`
			Pinned []string `json:"pinned"`
			Hidden []string `json:"hidden"`
		} `json:"policy"`
		Entries []struct {
			ID          string   `json:"id"`
			Label       string   `json:"label"`
			Format      string   `json:"format"`
			Hidden      bool     `json:"hidden"`
			HiddenRules []string `json:"hidden_rules"`
			PinnedRules []string `json:"pinned_rules"`
			Position    int      `json:"position"`
		} `json:"entries"`
		VisibleIDs       []string `json:"visible_ids"`
		Counts           struct {
			Total   int `json:"total"`
			Visible int `json:"visible"`
			Hidden  int `json:"hidden"`
		} `json:"counts"`
		ModelSortEnabled bool `json:"model_sort_enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Format != "openai" {
		t.Errorf("format: got %q want openai", view.Format)
	}
	if view.Policy.Order != "preserve" {
		t.Errorf("policy.order: got %q want preserve", view.Policy.Order)
	}
	if view.Counts.Total < 2 || view.Counts.Hidden < 1 || view.Counts.Visible < 1 {
		t.Errorf("counts wrong: %+v", view.Counts)
	}
	if view.ModelSortEnabled {
		t.Error("model_sort_enabled should be false without plugin")
	}
	// Visible IDs should contain the visible model.
	foundVisible := false
	for _, id := range view.VisibleIDs {
		if id == "claude-wire-visible" {
			foundVisible = true
		}
	}
	if !foundVisible {
		t.Errorf("visible IDs missing claude-wire-visible: %v", view.VisibleIDs)
	}
	// Inventory default format is openai.
	for _, e := range view.Entries {
		if e.Format != "openai" {
			t.Errorf("entry format: got %q want openai", e.Format)
		}
	}
}

func TestCatalogManagementPreviewDoesNotMutateFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initialYAML := "api-keys: [native-key]\nremote-management: {secret-key: native-mgmt}\n"
	if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 0
	cfg.AuthDir = filepath.Join(dir, "auth")
	os.MkdirAll(cfg.AuthDir, 0o700)
	cfg.Debug = true
	cfg.LoggingToFile = false
	cfg.UsageStatisticsEnabled = false
	server := NewServer(cfg, auth.NewManager(nil, nil, nil), access.NewManager(), configPath, WithLocalManagementPassword("native-mgmt"))
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	registerCatalogFixtures(t, "native-preview-noop", "openai",
		&registry.ModelInfo{ID: "claude-preview-model", Object: "model", OwnedBy: "test"},
	)

	// Capture post-load file state (LoadConfig hashes+persists the secret key).
	baseline, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Preview with a draft policy that hides everything.
	previewBody := `{"format":"openai","policy":{"hidden":["*"],"order":"preserve","pinned":[]}}`
	rec := catalogRequest(t, server, "POST", "/v8/management/models/catalog/preview", previewBody, "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("preview: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var view struct {
		Counts struct {
			Hidden int `json:"hidden"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Counts.Hidden == 0 {
		t.Errorf("preview should show all hidden: %+v", view.Counts)
	}

	// Config file must be unchanged from post-load baseline.
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(saved)) != strings.TrimSpace(string(baseline)) {
		t.Errorf("config file mutated by preview:\nbefore:\n%s\nafter:\n%s", baseline, saved)
	}

	// Public catalog must still show all models.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "claude-preview-model") {
		t.Errorf("public catalog affected by preview: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCatalogManagementInvalidFormat(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [native-key]\nremote-management: {secret-key: native-mgmt}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))

	rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog?format=invalid", "", "native-mgmt")
	if rec.Code != 400 {
		t.Fatalf("invalid format: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = catalogRequest(t, server, "POST", "/v8/management/models/catalog/preview", `{"format":"xml","policy":{"hidden":[],"order":"preserve","pinned":[]}}`, "native-mgmt")
	if rec.Code != 400 {
		t.Fatalf("invalid preview format: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCatalogManagementPreviewInvalidPolicy(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [native-key]\nremote-management: {secret-key: native-mgmt}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))

	// Invalid order enum.
	rec := catalogRequest(t, server, "POST", "/v8/management/models/catalog/preview", `{"format":"openai","policy":{"hidden":[],"order":"invalid","pinned":[]}}`, "native-mgmt")
	if rec.Code != 422 {
		t.Fatalf("invalid order enum: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Missing policy.
	rec = catalogRequest(t, server, "POST", "/v8/management/models/catalog/preview", `{"format":"openai"}`, "native-mgmt")
	if rec.Code != 400 {
		t.Fatalf("missing policy: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCatalogManagementFormatQuery(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte("api-keys: [native-key]\nremote-management: {secret-key: native-mgmt}\n"))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg, WithLocalManagementPassword("native-mgmt"))
	registerCatalogFixtures(t, "native-format-query", "openai",
		&registry.ModelInfo{ID: "claude-fmt-model", Object: "model", OwnedBy: "test"},
	)

	for _, format := range []string{"openai", "claude", "gemini", "codex", "grok"} {
		t.Run(format, func(t *testing.T) {
			rec := catalogRequest(t, server, "GET", "/v8/management/models/catalog?format="+format, "", "native-mgmt")
			if rec.Code != 200 {
				t.Fatalf("format=%s: status=%d body=%s", format, rec.Code, rec.Body.String())
			}
			var view struct {
				Format string `json:"format"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.Format != format {
				t.Errorf("format: got %q want %q", view.Format, format)
			}
		})
	}
}

// --- Focus 3 continued: Config persistence and reload ---

// newCatalogPersistenceServer creates a server backed by a real config file
// with a deterministic reload channel for synchronization.
func newCatalogPersistenceServer(t *testing.T, yamlContent string) (*Server, chan struct{}) {
	t.Helper()
	// Ensure management auth is available. Append only if no remote-management group exists.
	if !strings.Contains(yamlContent, "remote-management:") {
		yamlContent += "\nremote-management:\n  secret-key: native-mgmt\n"
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 0
	cfg.AuthDir = filepath.Join(dir, "auth")
	os.MkdirAll(cfg.AuthDir, 0o700)
	cfg.Debug = true
	cfg.LoggingToFile = false
	cfg.UsageStatisticsEnabled = false
	server := NewServer(cfg, auth.NewManager(nil, nil, nil), access.NewManager(), configPath, WithLocalManagementPassword("native-mgmt"))
	reloadDone := make(chan struct{}, 1)
	server.mgmt.SetConfigReloadHook(func(ctx context.Context, next *config.Config) bool {
		applied := server.UpdateClientsContext(ctx, next)
		select {
		case reloadDone <- struct{}{}:
		default:
		}
		return applied
	})
	t.Cleanup(func() { _ = server.Stop(context.Background()) })
	return server, reloadDone
}

// waitForReload waits for the reload channel or fails the test.
func waitForReload(t *testing.T, reloadDone <-chan struct{}) {
	t.Helper()
	select {
	case <-reloadDone:
	case <-time.After(10 * time.Second):
		t.Fatal("config reload timed out")
	}
}

func TestCatalogConfigPutPersistenceAndReload(t *testing.T) {
	server, reloadDone := newCatalogPersistenceServer(t, "api-keys: [native-key]\n")
	registerCatalogFixtures(t, "native-persistence", "openai",
		&registry.ModelInfo{ID: "claude-persist-a", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-persist-b", Object: "model", OwnedBy: "test"},
	)

	// Initial: both models visible.
	rec := catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if !strings.Contains(rec.Body.String(), "claude-persist-a") || !strings.Contains(rec.Body.String(), "claude-persist-b") {
		t.Fatalf("initial catalog missing models: %s", rec.Body.String())
	}

	// PUT hidden policy.
	putBody := `{"hidden":["claude-persist-b"],"order":"preserve","pinned":[]}`
	rec = catalogRequest(t, server, "PUT", "/v8/management/config/client/model-catalog", putBody, "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("PUT config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitForReload(t, reloadDone)

	// Verify hiding applied to runtime catalog.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if rec.Code != 200 {
		t.Fatalf("catalog after hide: status=%d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "claude-persist-b") {
		t.Errorf("hidden model appeared in catalog after PUT: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "claude-persist-a") {
		t.Errorf("visible model missing from catalog after PUT: %s", rec.Body.String())
	}

	// Verify persisted to file.
	saved, err := os.ReadFile(server.configFilePath)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	if !strings.Contains(string(saved), "claude-persist-b") {
		t.Errorf("config file does not contain hidden model: %s", saved)
	}

	// Read back via V8 GET.
	rec = catalogRequest(t, server, "GET", "/v8/management/config/client/model-catalog", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("GET config readback: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var readback struct {
		Hidden []string `json:"hidden"`
		Order  string   `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if !containsID(readback.Hidden, "claude-persist-b") {
		t.Errorf("readback hidden missing claude-persist-b: %+v", readback)
	}
}

func TestCatalogConfigPatchAndDelete(t *testing.T) {
	server, reloadDone := newCatalogPersistenceServer(t, "api-keys: [native-key]\nclient:\n  model-catalog:\n    hidden: [claude-patch-1]\n")
	registerCatalogFixtures(t, "native-patch", "openai",
		&registry.ModelInfo{ID: "claude-patch-1", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-patch-2", Object: "model", OwnedBy: "test"},
	)

	// Verify initial hiding.
	rec := catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if strings.Contains(rec.Body.String(), "claude-patch-1") {
		t.Errorf("initial hidden model visible: %s", rec.Body.String())
	}

	// PATCH to add another hidden model.
	rec = catalogRequest(t, server, "PATCH", "/v8/management/config/client/model-catalog", `{"hidden":["claude-patch-1","claude-patch-2"]}`, "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("PATCH config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitForReload(t, reloadDone)

	// Both models should now be hidden.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if strings.Contains(rec.Body.String(), "claude-patch-1") || strings.Contains(rec.Body.String(), "claude-patch-2") {
		t.Errorf("both should be hidden after PATCH: %s", rec.Body.String())
	}

	// DELETE the entire model-catalog group.
	rec = catalogRequest(t, server, "DELETE", "/v8/management/config/client/model-catalog", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("DELETE config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitForReload(t, reloadDone)

	// All models should be visible again.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if !strings.Contains(rec.Body.String(), "claude-patch-1") || !strings.Contains(rec.Body.String(), "claude-patch-2") {
		t.Errorf("models should reappear after DELETE: %s", rec.Body.String())
	}
}

func TestCatalogConfigExplicitClears(t *testing.T) {
	server, reloadDone := newCatalogPersistenceServer(t, "api-keys: [native-key]\nclient:\n  model-catalog:\n    hidden: [claude-clear-1]\n")
	registerCatalogFixtures(t, "native-clear", "openai",
		&registry.ModelInfo{ID: "claude-clear-1", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-clear-2", Object: "model", OwnedBy: "test"},
	)

	// PUT with empty hidden list (explicit clear).
	rec := catalogRequest(t, server, "PUT", "/v8/management/config/client/model-catalog", `{"hidden":[],"order":"preserve","pinned":[]}`, "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("PUT clear: status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitForReload(t, reloadDone)

	// Model should reappear.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if !strings.Contains(rec.Body.String(), "claude-clear-1") {
		t.Errorf("model should reappear after clear: %s", rec.Body.String())
	}

	// Readback should have empty hidden.
	rec = catalogRequest(t, server, "GET", "/v8/management/config/client/model-catalog", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("readback: status=%d", rec.Code)
	}
	var readback struct {
		Hidden []string `json:"hidden"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &readback); err != nil {
		t.Fatal(err)
	}
	if len(readback.Hidden) != 0 {
		t.Errorf("hidden should be empty after clear: %+v", readback)
	}
}

func TestCatalogConfigInvalidEnumLeavesOldBehavior(t *testing.T) {
	server, reloadDone := newCatalogPersistenceServer(t, "api-keys: [native-key]\nclient:\n  model-catalog:\n    hidden: [claude-invalid-1]\n")
	registerCatalogFixtures(t, "native-invalid-enum", "openai",
		&registry.ModelInfo{ID: "claude-invalid-1", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "claude-invalid-2", Object: "model", OwnedBy: "test"},
	)

	// PUT with invalid order enum.
	rec := catalogRequest(t, server, "PUT", "/v8/management/config/client/model-catalog", `{"hidden":["claude-invalid-2"],"order":"bogus","pinned":[]}`, "native-mgmt")
	if rec.Code < 400 {
		t.Fatalf("invalid order should fail: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Old behavior retained: claude-invalid-1 still hidden, claude-invalid-2 still visible.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if strings.Contains(rec.Body.String(), "claude-invalid-1") {
		t.Errorf("old hidden model appeared: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "claude-invalid-2") {
		t.Errorf("old visible model disappeared: %s", rec.Body.String())
	}

	// No reload should have fired for the invalid attempt.
	select {
	case <-reloadDone:
		t.Error("reload fired for invalid config")
	default:
	}
}

func TestCatalogConfigInvalidTypeLeavesOldBehavior(t *testing.T) {
	server, _ := newCatalogPersistenceServer(t, "api-keys: [native-key]\nclient:\n  model-catalog:\n    hidden: [claude-type-1]\n")
	registerCatalogFixtures(t, "native-invalid-type", "openai",
		&registry.ModelInfo{ID: "claude-type-1", Object: "model", OwnedBy: "test"},
	)

	// PUT with hidden as a string instead of array.
	rec := catalogRequest(t, server, "PUT", "/v8/management/config/client/model-catalog", `{"hidden":"not-an-array","order":"preserve","pinned":[]}`, "native-mgmt")
	if rec.Code < 400 {
		t.Fatalf("invalid type should fail: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Old behavior retained: claude-type-1 still hidden.
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if strings.Contains(rec.Body.String(), "claude-type-1") {
		t.Errorf("old hidden model appeared after invalid type: %s", rec.Body.String())
	}
}

func TestCatalogConfigUnrelatedFieldsPreserved(t *testing.T) {
	server, reloadDone := newCatalogPersistenceServer(t, "api-keys: [native-key]\nremote-management:\n  allow-remote: false\n  secret-key: native-mgmt\n")
	registerCatalogFixtures(t, "native-unrelated", "openai",
		&registry.ModelInfo{ID: "claude-unrelated", Object: "model", OwnedBy: "test"},
	)

	// PUT model-catalog.
	rec := catalogRequest(t, server, "PUT", "/v8/management/config/client/model-catalog", `{"hidden":["claude-unrelated"],"order":"preserve","pinned":[]}`, "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("PUT config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	waitForReload(t, reloadDone)

	// Verify unrelated config survived.
	rec = catalogRequest(t, server, "GET", "/v8/management/config/management", "", "native-mgmt")
	if rec.Code != 200 {
		t.Fatalf("GET unrelated config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var mgmt struct {
		AllowRemote bool `json:"allow-remote"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &mgmt); err != nil {
		t.Fatal(err)
	}
	if mgmt.AllowRemote {
		t.Errorf("unrelated field changed: allow-remote=%v", mgmt.AllowRemote)
	}
}

func TestCatalogConfigGetAbsentGroupReturns404(t *testing.T) {
	server, _ := newCatalogPersistenceServer(t, "api-keys: [native-key]\n")

	// Absent model-catalog group -> 404 (unconfigured, not error).
	rec := catalogRequest(t, server, "GET", "/v8/management/config/client/model-catalog", "", "native-mgmt")
	if rec.Code != 404 {
		t.Fatalf("absent group: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog should have default behavior (no hiding).
	registerCatalogFixtures(t, "native-absent", "openai",
		&registry.ModelInfo{ID: "claude-absent-model", Object: "model", OwnedBy: "test"},
	)
	rec = catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if !strings.Contains(rec.Body.String(), "claude-absent-model") {
		t.Errorf("absent policy should not hide models: %s", rec.Body.String())
	}
}

func TestCatalogConfigSortingAndPinning(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
client:
  model-catalog:
    order: asc
    pinned: [native-pin-1]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	registerCatalogFixtures(t, "native-sort", "openai",
		&registry.ModelInfo{ID: "native-pin-1", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "native-pin-2", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "native-sort-a", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "native-sort-b", Object: "model", OwnedBy: "test"},
	)

	rec := catalogRequest(t, server, "GET", "/v1/models", "", "native-key")
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	ids := extractIDs(t, rec.Body.Bytes(), "data", "id")
	if len(ids) < 4 {
		t.Fatalf("expected >= 4 models, got %d: %v", len(ids), ids)
	}
	// Pinned model should be first.
	if ids[0] != "native-pin-1" {
		t.Errorf("pinned model should be first: got %q, want native-pin-1", ids[0])
	}
	// Remaining should be sorted ascending.
	rest := ids[1:]
	for i := 1; i < len(rest); i++ {
		if rest[i-1] > rest[i] {
			t.Errorf("not ascending at %d: %v", i, rest)
		}
	}
}

func TestCatalogGeminiBareAndResourceFormMatching(t *testing.T) {
	cfg, err := config.ParseConfigBytes([]byte(`
api-keys: [native-key]
client:
  model-catalog:
    hidden: [gemini-bare-id]
`))
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithConfig(t, cfg)
	registerCatalogFixtures(t, "native-gemini-match", "gemini",
		&registry.ModelInfo{ID: "gemini-bare-id", Name: "gemini-bare-id", Object: "model", OwnedBy: "test"},
		&registry.ModelInfo{ID: "gemini-other", Name: "gemini-other", Object: "model", OwnedBy: "test"},
	)

	rec := catalogRequest(t, server, "GET", "/v1beta/models", "", "native-key")
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The Gemini handler adds models/ prefix, so name becomes "models/gemini-bare-id".
	// Hiding rule "gemini-bare-id" (bare) should match both forms.
	if strings.Contains(rec.Body.String(), "gemini-bare-id") {
		t.Errorf("bare-form hidden rule should match resource-form name: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gemini-other") {
		t.Errorf("unmatched model should remain: %s", rec.Body.String())
	}
}
