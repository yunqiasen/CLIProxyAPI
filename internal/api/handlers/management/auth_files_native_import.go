package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// nativeCredentialMaxSize caps the payload size accepted for native
// credential import only. Normal CPA uploads are not subject to this limit.
const nativeCredentialMaxSize = 1 << 20 // 1 MiB

// Sentinel errors for native credential import failures. These are mapped
// to HTTP status codes in the upload handler.
var (
	errNativeImportTooLarge     = errors.New("native credential file exceeds maximum size limit")
	errNativeImportMalformed    = errors.New("native credential file is malformed")
	errNativeImportMissingToken = errors.New("native credential is missing required token fields")
	errNativeImportCollision    = errors.New("native credential destination already exists; existing credential preserved")
	errNativeImportAmbiguous    = errors.New("credential file contains both native formats")
)

// nativeImportResult holds the converted credential data for a recognized
// native import.
type nativeImportResult struct {
	Name string
	Data []byte
}

// tryNativeImport inspects uploaded credential data for native Claude Code
// .credentials.json (claudeAiOauth) or Codex auth.json (tokens) structure.
// If a native format is recognized, it converts and validates the data to
// CPA-normalized JSON and returns the result. If the data is not a recognized
// native format (including CPA-normalized files with an explicit "type"
// field), it returns nil to indicate passthrough.
func tryNativeImport(data []byte) (*nativeImportResult, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var content map[string]any
	if err := json.Unmarshal(data, &content); err != nil {
		// Not valid JSON — let the normal upload path handle it.
		return nil, nil
	}
	if content == nil {
		return nil, nil
	}

	// An explicit CPA "type" field means this is a CPA-normalized file,
	// not a native format. Pass it through unchanged so that files with
	// both "type" and plugin-specific token fields are not reinterpreted.
	if _, hasType := content["type"]; hasType {
		return nil, nil
	}

	_, hasClaude := content["claudeAiOauth"]
	_, hasCodex := content["tokens"]

	// Reject ambiguous files that contain both native wrappers.
	if hasClaude && hasCodex {
		return nil, errNativeImportAmbiguous
	}

	if !hasClaude && !hasCodex {
		return nil, nil
	}

	provider, native := "claude", content["claudeAiOauth"]
	normalize := normalizeClaudeNative
	if hasCodex {
		provider, native = "codex", content["tokens"]
		normalize = normalizeCodexNative
	}
	tokens, ok := native.(map[string]any)
	if !ok {
		return nil, errNativeImportMalformed
	}
	if err := validateNativeSize(data); err != nil {
		return nil, err
	}
	normalized, err := normalize(tokens, content)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal normalized credential: %w", err)
	}
	return &nativeImportResult{Name: nativeCredentialName(provider, normalized), Data: out}, nil
}

// validateNativeSize checks the size limit only for recognized native
// payloads. Normalized CPA uploads are not subject to this limit.
func validateNativeSize(data []byte) error {
	if len(data) > nativeCredentialMaxSize {
		return errNativeImportTooLarge
	}
	return nil
}

// normalizeClaudeNative converts a native Claude Code claudeAiOauth object
// (from .credentials.json) to CPA-normalized credential metadata. Only
// credential fields are extracted; CLI/MCP settings from the wrapper are
// excluded. The websockets transport flag is never set — the CPA default
// transport applies.
func normalizeClaudeNative(content map[string]any, wrapper map[string]any) (map[string]any, error) {
	out := map[string]any{"type": "claude"}

	out["access_token"] = firstNativeString(content, "access_token", "accessToken")
	out["refresh_token"] = firstNativeString(content, "refresh_token", "refreshToken")

	if out["refresh_token"] == "" {
		return nil, errNativeImportMissingToken
	}

	// expiresAt can be epoch milliseconds (number) or an RFC 3339 string.
	// Malformed, fractional, or wrong-type values are rejected.
	if v, ok := content["expiresAt"]; ok {
		switch val := v.(type) {
		case float64:
			// Reject fractional epoch values.
			if val != float64(int64(val)) {
				return nil, errNativeImportMalformed
			}
			if val < 0 || val > 253402300799000 {
				return nil, errNativeImportMalformed
			}
			out["expired"] = time.UnixMilli(int64(val)).UTC().Format(time.RFC3339)
		case string:
			s := strings.TrimSpace(val)
			if s != "" {
				if _, err := time.Parse(time.RFC3339, s); err != nil {
					return nil, errNativeImportMalformed
				}
				out["expired"] = s
			}
		default:
			return nil, errNativeImportMalformed
		}
	}
	if err := copyNativeTimestamp(content, out, "expired"); err != nil {
		return nil, err
	}

	if err := copyNativeTimestamp(wrapper, out, "last_refresh"); err != nil {
		return nil, err
	}

	// Optional identity fields from the native object.
	if email := firstNativeString(content, "email"); email != "" {
		out["email"] = email
	}
	if uuid := firstNativeString(content, "account_uuid", "accountUuid"); uuid != "" {
		out["account_uuid"] = uuid
	}
	if orgUUID := firstNativeString(content, "organization_uuid", "organizationUuid"); orgUUID != "" {
		out["organization_uuid"] = orgUUID
	}
	if orgName := firstNativeString(content, "organization_name", "organizationName"); orgName != "" {
		out["organization_name"] = orgName
	}

	return out, nil
}

// normalizeCodexNative converts a native Codex tokens object (from
// auth.json) to CPA-normalized credential metadata. Only the OAuth token
// fields are extracted. The websockets transport flag is not set — the CPA
// default transport (HTTP) applies. Users who need websockets can set it via
// the management UI after import.
func normalizeCodexNative(content map[string]any, wrapper map[string]any) (map[string]any, error) {
	out := map[string]any{"type": "codex"}

	for _, k := range []string{"access_token", "refresh_token", "id_token", "account_id"} {
		if v := firstNativeString(content, k); v != "" {
			out[k] = v
		}
	}

	if out["access_token"] == nil || out["refresh_token"] == nil {
		return nil, errNativeImportMissingToken
	}

	// Do not force websockets=true. The native Codex auth.json does not
	// carry a websockets field, and the CPA default transport (HTTP) applies.
	// Forcing websockets would silently opt users into a different transport.

	if err := copyNativeTimestamp(wrapper, out, "last_refresh"); err != nil {
		return nil, err
	}

	// Optional fields.
	if email := firstNativeString(content, "email"); email != "" {
		out["email"] = email
	}
	if err := copyNativeTimestamp(content, out, "expired"); err != nil {
		return nil, err
	}

	return out, nil
}

// copyNativeTimestamp validates supplied timestamps without carrying CLI settings.
func copyNativeTimestamp(source, out map[string]any, key string) error {
	value, exists := source[key]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return errNativeImportMalformed
	}
	text = strings.TrimSpace(text)
	if _, err := time.Parse(time.RFC3339, text); err != nil {
		return errNativeImportMalformed
	}
	out[key] = text
	return nil
}

// nativeCredentialName generates a deterministic file name for a normalized
// native credential. The name is based on the provider type and a stable
// account identity (account_id for Codex, refresh_token for Claude).
func nativeCredentialName(provider string, m map[string]any) string {
	identity := nativeIdentity(provider, m)
	h := sha256.Sum256([]byte(provider + ":" + identity))
	return "native-" + provider + "-" + hex.EncodeToString(h[:12]) + ".json"
}

// nativeIdentity returns the stable account identity for a given provider.
// For Claude the identity is refresh_token; for Codex it is account_id with
// refresh_token fallback.
func nativeIdentity(provider string, m map[string]any) string {
	switch provider {
	case "claude":
		return firstNativeString(m, "refresh_token")
	case "codex":
		return firstNativeString(m, "account_id", "refresh_token")
	default:
		return ""
	}
}

// firstNativeString returns the first non-empty trimmed string value found
// under any of the given keys in the map.
func firstNativeString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// writeNativeAuthFile publishes a complete native file without replacing any
// existing credential, including a refreshed credential for the same account.
func (h *Handler) writeNativeAuthFile(ctx context.Context, name string, data []byte) error {
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		absolute, err := filepath.Abs(dst)
		if err != nil {
			return fmt.Errorf("resolve native credential path: %w", err)
		}
		dst = absolute
	}
	auth, err := h.buildAuthFromFileData(dst, data)
	if err != nil {
		return err
	}
	// A staging file is not a JSON credential and is ignored by the auth watcher.
	// Linking atomically publishes it and fails if the destination already exists.
	f, err := os.CreateTemp(filepath.Dir(dst), ".native-import-*")
	if err != nil {
		return fmt.Errorf("stage native credential: %w", err)
	}
	defer func() {
		if errRemove := os.Remove(f.Name()); errRemove != nil && !os.IsNotExist(errRemove) {
			log.Warnf("remove native credential staging file: %v", errRemove)
		}
	}()
	_, errWrite := f.Write(data)
	if errWrite == nil {
		errWrite = f.Sync()
	}
	errClose := f.Close()
	if errWrite != nil {
		return fmt.Errorf("write native credential: %w", errWrite)
	}
	if errClose != nil {
		return fmt.Errorf("close native credential: %w", errClose)
	}
	if errLink := os.Link(f.Name(), dst); errLink != nil {
		if os.IsExist(errLink) {
			return errNativeImportCollision
		}
		return fmt.Errorf("publish native credential: %w", errLink)
	}
	if err := h.upsertAuthRecord(ctx, auth); err != nil {
		return err
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(ctx, auth); errHook != nil {
			return fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	return nil
}

// nativeImportErrorStatus maps native import sentinel errors to HTTP status
// codes. Returns (0, false) if the error is not a native import error.
func nativeImportErrorStatus(err error) (int, bool) {
	switch {
	case errors.Is(err, errNativeImportTooLarge),
		errors.Is(err, errNativeImportMalformed),
		errors.Is(err, errNativeImportMissingToken),
		errors.Is(err, errNativeImportAmbiguous):
		return http.StatusBadRequest, true
	case errors.Is(err, errNativeImportCollision):
		return http.StatusConflict, true
	default:
		return 0, false
	}
}
