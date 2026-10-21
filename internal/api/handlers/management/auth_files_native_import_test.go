package management

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// --- helpers ---

func newNativeImportTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	return h, authDir
}

func multipartUploadFile(t *testing.T, h *Handler, field, filename, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err = part.Write(body); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req
	h.UploadAuthFile(ctx)
	return rec
}

func rawBodyUpload(t *testing.T, h *Handler, name string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	url := "/v0/management/auth-files"
	if name != "" {
		url += "?name=" + name
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.UploadAuthFile(ctx)
	return rec
}

// Artificial tokens — never real credentials.
const (
	artificialClaudeAccess  = "claude-at-artificial-0001"
	artificialClaudeRefresh = "claude-rt-artificial-0001"
	artificialCodexAccess   = "codex-at-artificial-0002"
	artificialCodexRefresh  = "codex-rt-artificial-0002"
	artificialCodexAccount  = "codex-acct-artificial-0002"
	artificialCodexIDToken  = "codex-id-artificial-0002"
)

func claudeNativeJSON() []byte {
	return []byte(`{"claudeAiOauth":{"accessToken":"` + artificialClaudeAccess + `","refreshToken":"` + artificialClaudeRefresh + `","expiresAt":1764576000000},"colorTheme":"dark","oauthAccount":{"uuid":"irrelevant"}}`)
}

func codexNativeJSON() []byte {
	return []byte(`{"tokens":{"access_token":"` + artificialCodexAccess + `","refresh_token":"` + artificialCodexRefresh + `","id_token":"` + artificialCodexIDToken + `","account_id":"` + artificialCodexAccount + `"},"OPENAI_API_KEY":null,"last_refresh":"2026-01-01T00:00:00Z"}`)
}

// --- Claude native import ---

func TestNativeImport_ClaudeMultipart(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	name, _ := resp["name"].(string)
	if name == "" || !strings.HasPrefix(name, "native-claude-") || !strings.HasSuffix(name, ".json") {
		t.Fatalf("expected native-claude-*.json name, got %q", name)
	}

	data, err := os.ReadFile(filepath.Join(authDir, name))
	if err != nil {
		t.Fatalf("read converted file: %v", err)
	}
	var saved map[string]any
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if saved["type"] != "claude" {
		t.Fatalf("type = %v, want claude", saved["type"])
	}
	if saved["access_token"] != artificialClaudeAccess {
		t.Fatalf("access_token mismatch")
	}
	if saved["refresh_token"] != artificialClaudeRefresh {
		t.Fatalf("refresh_token mismatch")
	}
	if exp, _ := saved["expired"].(string); exp == "" {
		t.Fatalf("expired should be set (RFC3339)")
	}
	if _, ok := saved["colorTheme"]; ok {
		t.Fatalf("colorTheme should not be in converted file")
	}
	if _, ok := saved["oauthAccount"]; ok {
		t.Fatalf("oauthAccount should not be in converted file")
	}
}

func TestNativeImport_ClaudeRawBody(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := rawBodyUpload(t, h, "auth.json", claudeNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	name, _ := resp["name"].(string)
	if name == "" || !strings.HasPrefix(name, "native-claude-") {
		t.Fatalf("expected native-claude-* name, got %q", name)
	}

	if _, err := os.Stat(filepath.Join(authDir, name)); err != nil {
		t.Fatalf("converted file not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(authDir, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("original auth.json should not exist in auth dir")
	}
}

// --- Codex native import ---

func TestNativeImport_CodexMultipart(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", codexNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	name, _ := resp["name"].(string)
	if name == "" || !strings.HasPrefix(name, "native-codex-") || !strings.HasSuffix(name, ".json") {
		t.Fatalf("expected native-codex-*.json name, got %q", name)
	}

	data, err := os.ReadFile(filepath.Join(authDir, name))
	if err != nil {
		t.Fatalf("read converted file: %v", err)
	}
	var saved map[string]any
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved: %v", err)
	}
	if saved["type"] != "codex" {
		t.Fatalf("type = %v, want codex", saved["type"])
	}
	if saved["access_token"] != artificialCodexAccess {
		t.Fatalf("access_token mismatch")
	}
	if saved["refresh_token"] != artificialCodexRefresh {
		t.Fatalf("refresh_token mismatch")
	}
	if saved["id_token"] != artificialCodexIDToken {
		t.Fatalf("id_token mismatch")
	}
	if saved["account_id"] != artificialCodexAccount {
		t.Fatalf("account_id mismatch")
	}
	// websockets must NOT be forced — the CPA default transport applies.
	if _, hasWS := saved["websockets"]; hasWS {
		t.Fatalf("websockets should not be set on native codex import")
	}
	if _, ok := saved["OPENAI_API_KEY"]; ok {
		t.Fatalf("OPENAI_API_KEY should not be in converted file")
	}
	if lr, ok := saved["last_refresh"]; !ok || lr != "2026-01-01T00:00:00Z" {
		t.Fatalf("last_refresh = %v, want 2026-01-01T00:00:00Z", saved["last_refresh"])
	}
}

func TestNativeImport_CodexRawBody(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := rawBodyUpload(t, h, "auth.json", codexNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	name, _ := resp["name"].(string)
	if name == "" || !strings.HasPrefix(name, "native-codex-") {
		t.Fatalf("expected native-codex-* name, got %q", name)
	}
	if _, err := os.Stat(filepath.Join(authDir, name)); err != nil {
		t.Fatalf("converted file not found: %v", err)
	}
}

// --- Validation failures ---

func TestNativeImport_ClaudeMissingRefreshToken(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	bad := []byte(`{"claudeAiOauth":{"accessToken":"only-access","expiresAt":1764576000000}}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	entries, _ := os.ReadDir(authDir)
	if len(entries) != 0 {
		t.Fatalf("expected no files written, got %d", len(entries))
	}
}

func TestNativeImport_CodexMissingTokens(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	bad := []byte(`{"tokens":{"access_token":"only-access"}}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	entries, _ := os.ReadDir(authDir)
	if len(entries) != 0 {
		t.Fatalf("expected no files written, got %d", len(entries))
	}
}

func TestNativeImport_MalformedClaudeAiOauth(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	bad := []byte(`{"claudeAiOauth":"not-a-map"}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
}

// --- Size limit ---

func TestNativeImport_OversizedNativePayload(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	big := make([]byte, nativeCredentialMaxSize+1)
	for i := range big {
		big[i] = 'x'
	}
	wrapper := append([]byte(`{"claudeAiOauth":{"refreshToken":"`), big...)
	wrapper = append(wrapper, []byte(`"}}`)...)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", wrapper)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized native, got %d body %s", rec.Code, rec.Body.String())
	}
}

// A large normalized CPA upload must pass through without the native size
// limit being applied.
func TestNativeImport_LargeNormalizedCPAUploadAllowed(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	// Build a >1 MiB normalized CPA codex file with explicit type field.
	big := make([]byte, nativeCredentialMaxSize+1)
	for i := range big {
		big[i] = 'x'
	}
	normalCPA := []byte(`{"type":"codex","email":"large@example.com","access_token":"tok","refresh_token":"rt","padding":"`)
	normalCPA = append(normalCPA, big...)
	normalCPA = append(normalCPA, '"', '}')

	rec := multipartUploadFile(t, h, "file", "codex-large@example.com.json", "application/json", normalCPA)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for large normalized CPA upload, got %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(authDir, "codex-large@example.com.json")); err != nil {
		t.Fatalf("expected file at original name: %v", err)
	}
	entries, _ := os.ReadDir(authDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "native-") {
			t.Fatalf("unexpected native import file: %s", e.Name())
		}
	}
}

// --- CPA type field passthrough ---

// A top-level explicit CPA "type" with plugin-specific token fields must
// remain a normal upload, not get reinterpreted as a native format.
func TestNativeImport_NormalizedCPATypeWithTokensPassthrough(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	// This looks like it might contain native tokens but has explicit CPA type.
	body := []byte(`{"type":"codex","tokens":{"access_token":"plugin-at","refresh_token":"plugin-rt"},"OPENAI_API_KEY":"key"}`)
	rec := multipartUploadFile(t, h, "file", "codex-plugin@example.com.json", "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	// File should be at the original name, not a native-* name.
	if _, err := os.Stat(filepath.Join(authDir, "codex-plugin@example.com.json")); err != nil {
		t.Fatalf("expected file at original name: %v", err)
	}
	entries, _ := os.ReadDir(authDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "native-") {
			t.Fatalf("unexpected native import file: %s", e.Name())
		}
	}
}

// --- Ambiguous both wrappers ---

func TestNativeImport_AmbiguousBothWrappers(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	body := []byte(`{"claudeAiOauth":{"refresh_token":"rt-a"},"tokens":{"access_token":"at-b","refresh_token":"rt-b"}}`)
	rec := multipartUploadFile(t, h, "file", "ambiguous.json", "application/json", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ambiguous native import, got %d body %s", rec.Code, rec.Body.String())
	}
}

// --- Collision / O_EXCL behavior ---

func TestNativeImport_CollisionDifferentAccount(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	// Import once to learn the destination name.
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("first import: %d %s", rec.Code, rec.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp1)
	dstName, _ := resp1["name"].(string)
	dstPath := filepath.Join(authDir, dstName)
	existingBefore, _ := os.ReadFile(dstPath)

	// Overwrite with a different account's credential.
	diffCredential := []byte(`{"type":"claude","refresh_token":"different-account-rt","access_token":"diff-at"}`)
	if err := os.WriteFile(dstPath, diffCredential, 0o600); err != nil {
		t.Fatalf("write conflicting file: %v", err)
	}

	// Re-import: must be 409, and the existing different-account bytes preserved.
	rec2 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 collision, got %d body %s", rec2.Code, rec2.Body.String())
	}
	existingAfter, errRead := os.ReadFile(dstPath)
	if errRead != nil {
		t.Fatalf("read preserved file: %v", errRead)
	}
	if string(existingAfter) != string(diffCredential) {
		t.Fatalf("existing file bytes changed on collision:\nbefore=%s\nafter=%s", diffCredential, existingAfter)
	}
	_ = existingBefore
}

// A duplicate upload of the same native credential must return the same
// destination unchanged, with a conflict status rather than replacing the file.
func TestNativeImport_ExactRepeatSameAccount(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec1 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec1.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec1.Code, rec1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	name1, _ := resp1["name"].(string)

	// Capture file bytes after the first import.
	before, errRead := os.ReadFile(filepath.Join(authDir, name1))
	if errRead != nil {
		t.Fatalf("read after first: %v", errRead)
	}

	// Exact repeat.
	rec2 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec2.Code != http.StatusConflict {
		t.Fatalf("repeat: %d %s", rec2.Code, rec2.Body.String())
	}
	after, errRead := os.ReadFile(filepath.Join(authDir, name1))
	if errRead != nil {
		t.Fatalf("read after repeat: %v", errRead)
	}
	if string(before) != string(after) {
		t.Fatalf("exact repeat should produce identical file bytes")
	}
}

// Invalid JSON at a native destination must cause a 409 collision, not
// a silent overwrite.
func TestNativeImport_InvalidJSONOccupiedFile(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	name, _ := resp["name"].(string)
	dstPath := filepath.Join(authDir, name)

	// Replace with invalid JSON.
	invalidJSON := []byte("this is not json")
	if err := os.WriteFile(dstPath, invalidJSON, 0o600); err != nil {
		t.Fatalf("write invalid: %v", err)
	}

	rec2 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for invalid occupied file, got %d body %s", rec2.Code, rec2.Body.String())
	}
	after, errRead := os.ReadFile(dstPath)
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	if string(after) != string(invalidJSON) {
		t.Fatalf("invalid JSON file should be preserved, got: %s", after)
	}
}

// Type mismatch at the destination (e.g. a codex file at the same name
// where a claude native would land) must be a collision.
func TestNativeImport_TypeMismatchCollision(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	name, _ := resp["name"].(string)
	dstPath := filepath.Join(authDir, name)

	// Write a codex-type file at the claude destination.
	if err := os.WriteFile(dstPath, []byte(`{"type":"codex","refresh_token":"`+artificialClaudeRefresh+`"}`), 0o600); err != nil {
		t.Fatalf("write mismatch: %v", err)
	}

	rec2 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for type mismatch, got %d body %s", rec2.Code, rec2.Body.String())
	}
}

// Concurrent duplicate native imports should not corrupt or overwrite
// each other's data. Exactly one creates the file; every other request conflicts.
func TestNativeImport_ConcurrentDuplicate(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
			codes[idx] = rec.Code
		}(i)
	}
	wg.Wait()
	successes := 0
	for i, code := range codes {
		if code == http.StatusOK {
			successes++
		}
		if code != http.StatusOK && code != http.StatusConflict {
			t.Fatalf("goroutine %d: unexpected status %d", i, code)
		}
	}
	if successes != 1 {
		t.Fatalf("successful imports=%d, expected exactly one", successes)
	}
}

// Reimporting the same native identity preserves refreshed tokens and policy.
func TestNativeImport_ExistingPolicyPreserved(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)
	first := rawBodyUpload(t, h, "auth.json", codexNativeJSON())
	if first.Code != http.StatusOK {
		t.Fatalf("first upload: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(authDir, result.Name)
	current := []byte(`{"type":"codex","account_id":"` + artificialCodexAccount + `","refresh_token":"newer-refresh","access_token":"newer-access","priority":9,"excluded-models":["model-a"],"proxy_url":"http://proxy:8080","websockets":true,"disabled":true}`)
	if err := os.WriteFile(path, current, 0o600); err != nil {
		t.Fatal(err)
	}
	repeat := rawBodyUpload(t, h, "auth.json", codexNativeJSON())
	if repeat.Code != http.StatusConflict {
		t.Fatalf("same account reimport should preserve current credential: status=%d", repeat.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, current) {
		t.Fatal("native import replaced refreshed tokens or policy")
	}
}

// --- Normal CPA upload unaffected ---

func TestNativeImport_NormalCPAUploadUnchanged(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	normalCPA := []byte(`{"type":"codex","email":"user@example.com","access_token":"tok-123","refresh_token":"rt-456"}`)
	rec := multipartUploadFile(t, h, "file", "codex-user@example.com.json", "application/json", normalCPA)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(authDir, "codex-user@example.com.json")); err != nil {
		t.Fatalf("expected file at original name: %v", err)
	}
	entries, _ := os.ReadDir(authDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "native-") {
			t.Fatalf("unexpected native import file: %s", e.Name())
		}
	}
}

// --- Batch partial success ---

func TestNativeImport_BatchPartialSuccess(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	claudeBody := claudeNativeJSON()
	badCodex := []byte(`{"tokens":{"access_token":"only"}}`)
	normalCPA := []byte(`{"type":"claude","email":"normal@example.com","access_token":"n-at","refresh_token":"n-rt"}`)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, item := range []struct {
		field, filename string
		body            []byte
	}{
		{"file1", "auth.json", claudeBody},
		{"file2", ".credentials.json", badCodex},
		{"file3", "claude-normal@example.com.json", normalCPA},
	} {
		part, _ := writer.CreateFormFile(item.field, item.filename)
		part.Write(item.body)
	}
	writer.Close()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req
	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected 207, got %d body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "partial" {
		t.Fatalf("status = %v, want partial", resp["status"])
	}
	uploaded, _ := resp["uploaded"].(float64)
	if uploaded != 2 {
		t.Fatalf("uploaded = %v, want 2", uploaded)
	}
	failed, _ := resp["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("failed count = %d, want 1", len(failed))
	}
	entries, _ := os.ReadDir(authDir)
	fileNames := make(map[string]bool)
	for _, e := range entries {
		fileNames[e.Name()] = true
	}
	if !fileNames["claude-normal@example.com.json"] {
		t.Fatalf("normal CPA file missing")
	}
	foundNative := false
	for n := range fileNames {
		if strings.HasPrefix(n, "native-claude-") {
			foundNative = true
			break
		}
	}
	if !foundNative {
		t.Fatalf("native claude import file missing")
	}
}

// --- Excludes unrelated settings ---

func TestNativeImport_ClaudeExcludesMCESettings(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	native := []byte(`{"claudeAiOauth":{"accessToken":"` + artificialClaudeAccess + `","refreshToken":"` + artificialClaudeRefresh + `","expiresAt":1764576000000},"mcpServers":{"foo":{"command":"bar"}},"customApiKeyResponses":[]}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", native)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	name, _ := resp["name"].(string)

	data, err := os.ReadFile(filepath.Join(authDir, name))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var saved map[string]any
	_ = json.Unmarshal(data, &saved)
	if _, ok := saved["mcpServers"]; ok {
		t.Fatalf("mcpServers must not be in converted file")
	}
	if _, ok := saved["customApiKeyResponses"]; ok {
		t.Fatalf("customApiKeyResponses must not be in converted file")
	}
}

// --- Deterministic name ---

func TestNativeImport_DeterministicName(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	rec1 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec1.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rec1.Code, rec1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	name1, _ := resp1["name"].(string)

	// Remove the file to test determinism without collision.
	os.Remove(filepath.Join(authDir, name1))

	rec2 := multipartUploadFile(t, h, "file", "auth.json", "application/json", claudeNativeJSON())
	if rec2.Code != http.StatusOK {
		t.Fatalf("second: %d %s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	name2, _ := resp2["name"].(string)

	if name1 != name2 {
		t.Fatalf("names differ: %q vs %q", name1, name2)
	}
}

// --- No secrets in error responses ---

func TestNativeImport_NoSecretsInError(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	secretAccess := "super-secret-access-xyz"
	bad := []byte(`{"claudeAiOauth":{"accessToken":"` + secretAccess + `"}}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", bad)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected error, got 200")
	}
	if strings.Contains(rec.Body.String(), secretAccess) {
		t.Fatalf("error response leaked secret: %s", rec.Body.String())
	}
}

// --- Expiry validation ---

func TestNativeImport_ExpiredRFC3339(t *testing.T) {
	h, authDir := newNativeImportTestHandler(t)

	native := []byte(`{"claudeAiOauth":{"accessToken":"` + artificialClaudeAccess + `","refreshToken":"` + artificialClaudeRefresh + `","expiresAt":"2026-12-31T23:59:59Z"}}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", native)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	name, _ := resp["name"].(string)
	data, err := os.ReadFile(filepath.Join(authDir, name))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var saved map[string]any
	_ = json.Unmarshal(data, &saved)
	if saved["expired"] != "2026-12-31T23:59:59Z" {
		t.Fatalf("expired = %v, want 2026-12-31T23:59:59Z", saved["expired"])
	}
}

func TestNativeImport_ExpiredMalformedRejected(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"fractional_epoch", `{"claudeAiOauth":{"refreshToken":"rt-frac","expiresAt":1764576000000.5}}`},
		{"malformed_string", `{"claudeAiOauth":{"refreshToken":"rt-bad","expiresAt":"not-a-date"}}`},
		{"wrong_type", `{"claudeAiOauth":{"refreshToken":"rt-wrong","expiresAt":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s, got %d body %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNativeImport_ExpiredFractionalRejected(t *testing.T) {
	h, _ := newNativeImportTestHandler(t)

	body := []byte(`{"claudeAiOauth":{"refreshToken":"rt-frac","expiresAt":1764576000000.5}}`)
	rec := multipartUploadFile(t, h, "file", "auth.json", "application/json", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for fractional epoch, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestNativeImport_NullWrappersAndWrongExpiryTypes(t *testing.T) {
	for _, body := range []string{
		`{"tokens":null}`,
		`{"claudeAiOauth":null}`,
		`{"tokens":{},"claudeAiOauth":null}`,
		`{"tokens":{"access_token":"access","refresh_token":"refresh","expired":true}}`,
		`{"claudeAiOauth":{"refreshToken":"refresh","expired":42}}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, authDir := newNativeImportTestHandler(t)
			r := rawBodyUpload(t, h, "auth.json", []byte(body))
			if r.Code != http.StatusBadRequest {
				t.Fatalf("malformed native input status=%d", r.Code)
			}
			files, err := os.ReadDir(authDir)
			if err != nil || len(files) != 0 {
				t.Fatalf("malformed input wrote files: %v %v", files, err)
			}
		})
	}
}
