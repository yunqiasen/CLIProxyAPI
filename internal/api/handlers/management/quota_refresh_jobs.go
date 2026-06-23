package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	defaultQuotaRefreshConcurrency = 3
	maxQuotaRefreshConcurrency     = 10
	maxQuotaRefreshErrors          = 50
)

var (
	antigravityQuotaURLs = []string{
		"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
		"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	}
	antigravityQuotaHeaders = map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "antigravity/1.11.5 windows/amd64",
	}
	geminiCLIQuotaURL      = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiCLILoadAssistURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	geminiCLIQuotaHeaders  = map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
	}
	claudeQuotaUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	claudeQuotaProfileURL = "https://api.anthropic.com/api/oauth/profile"
	claudeQuotaHeaders    = map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}
	codexQuotaUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaHeaders  = map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
	}
	kimiQuotaUsageURL = "https://api.kimi.com/coding/v1/usages"
	kimiQuotaHeaders  = map[string]string{
		"Authorization": "Bearer $TOKEN$",
	}
	geminiDefaultProjectID = "bamboo-precept-lgxtn"
	accountProjectPattern  = regexp.MustCompile(`\(([^()]+)\)`)
)

type quotaRefreshJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*quotaRefreshJob
}

type quotaRefreshJob struct {
	ID          string                 `json:"id"`
	Status      string                 `json:"status"`
	Provider    string                 `json:"provider"`
	Total       int                    `json:"total"`
	Done        int                    `json:"done"`
	Success     int                    `json:"success"`
	Failed      int                    `json:"failed"`
	Concurrency int                    `json:"concurrency"`
	StartedAt   time.Time              `json:"started_at"`
	FinishedAt  *time.Time             `json:"finished_at,omitempty"`
	Current     []string               `json:"current,omitempty"`
	Errors      []quotaRefreshJobError `json:"errors,omitempty"`
}

type quotaRefreshJobError struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Error    string `json:"error"`
}

type startQuotaRefreshJobRequest struct {
	Provider    string   `json:"provider"`
	Type        string   `json:"type"`
	Names       []string `json:"names"`
	Concurrency int      `json:"concurrency"`
}

func newQuotaRefreshJobStore() *quotaRefreshJobStore {
	return &quotaRefreshJobStore{jobs: make(map[string]*quotaRefreshJob)}
}

func (s *quotaRefreshJobStore) add(job *quotaRefreshJob) quotaRefreshJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return cloneQuotaRefreshJob(job)
}

func (s *quotaRefreshJobStore) get(id string) (quotaRefreshJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job := s.jobs[id]
	if job == nil {
		return quotaRefreshJob{}, false
	}
	return cloneQuotaRefreshJob(job), true
}

func (s *quotaRefreshJobStore) update(id string, fn func(*quotaRefreshJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[id]; job != nil {
		fn(job)
	}
}

func cloneQuotaRefreshJob(job *quotaRefreshJob) quotaRefreshJob {
	if job == nil {
		return quotaRefreshJob{}
	}
	out := *job
	out.Current = append([]string(nil), job.Current...)
	out.Errors = append([]quotaRefreshJobError(nil), job.Errors...)
	return out
}

// StartQuotaRefreshJob starts a low-concurrency quota refresh job for matching credentials.
func (h *Handler) StartQuotaRefreshJob(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	if h.quotaRefreshJobs == nil {
		h.quotaRefreshJobs = newQuotaRefreshJobStore()
	}

	var req startQuotaRefreshJobRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if errBind := c.ShouldBindJSON(&req); errBind != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	}
	provider := normalizeQuotaProvider(firstNonEmpty(req.Provider, req.Type, c.Query("provider"), c.Query("type")))
	concurrency := clampQuotaRefreshConcurrency(req.Concurrency)
	targets := h.selectQuotaRefreshTargets(provider, req.Names)

	job := &quotaRefreshJob{
		ID:          fmt.Sprintf("qrj-%d", time.Now().UnixNano()),
		Status:      "running",
		Provider:    provider,
		Total:       len(targets),
		Concurrency: concurrency,
		StartedAt:   time.Now().UTC(),
	}
	if len(targets) == 0 {
		now := time.Now().UTC()
		job.Status = "completed"
		job.FinishedAt = &now
	}
	snapshot := h.quotaRefreshJobs.add(job)
	if len(targets) > 0 {
		go h.runQuotaRefreshJob(job.ID, targets, concurrency)
	}
	c.JSON(http.StatusAccepted, snapshot)
}

// GetQuotaRefreshJob returns the current state of a quota refresh job.
func (h *Handler) GetQuotaRefreshJob(c *gin.Context) {
	if h == nil || h.quotaRefreshJobs == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	job, ok := h.quotaRefreshJobs.get(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, job)
}

func (h *Handler) selectQuotaRefreshTargets(provider string, names []string) []*coreauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		if n := strings.TrimSpace(name); n != "" {
			nameSet[n] = struct{}{}
		}
	}
	auths := h.authManager.List()
	out := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled {
			continue
		}
		authProvider := normalizeQuotaProvider(auth.Provider)
		if !quotaProviderSupported(authProvider) {
			continue
		}
		if provider != "" && provider != "all" && authProvider != provider {
			continue
		}
		if len(nameSet) > 0 {
			if _, ok := nameSet[auth.ID]; !ok {
				if _, okName := nameSet[auth.FileName]; !okName {
					continue
				}
			}
		}
		auth.EnsureIndex()
		out = append(out, auth)
	}
	sort.Slice(out, func(i, j int) bool {
		return quotaAuthDisplayName(out[i]) < quotaAuthDisplayName(out[j])
	})
	return out
}

func (h *Handler) runQuotaRefreshJob(id string, targets []*coreauth.Auth, concurrency int) {
	if h == nil || h.quotaRefreshJobs == nil {
		return
	}
	ctx := context.Background()
	jobs := make(chan *coreauth.Auth)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for auth := range jobs {
				name := quotaAuthDisplayName(auth)
				h.quotaRefreshJobs.update(id, func(job *quotaRefreshJob) {
					job.Current = append(job.Current, name)
				})
				err := h.refreshQuotaForAuth(ctx, auth)
				h.quotaRefreshJobs.update(id, func(job *quotaRefreshJob) {
					job.Done++
					job.Current = removeString(job.Current, name)
					if err != nil {
						job.Failed++
						if len(job.Errors) < maxQuotaRefreshErrors {
							job.Errors = append(job.Errors, quotaRefreshJobError{ID: auth.ID, Name: name, Provider: normalizeQuotaProvider(auth.Provider), Error: truncateQuotaError(err.Error())})
						}
						return
					}
					job.Success++
				})
			}
		}()
	}
	for _, target := range targets {
		jobs <- target
	}
	close(jobs)
	wg.Wait()
	h.quotaRefreshJobs.update(id, func(job *quotaRefreshJob) {
		now := time.Now().UTC()
		job.Status = "completed"
		job.FinishedAt = &now
		job.Current = nil
	})
}

func (h *Handler) refreshQuotaForAuth(ctx context.Context, auth *coreauth.Auth) error {
	provider := normalizeQuotaProvider(auth.Provider)
	switch provider {
	case "codex":
		return h.refreshCodexQuota(ctx, auth)
	case "claude":
		return h.refreshClaudeQuota(ctx, auth)
	case "antigravity":
		return h.refreshAntigravityQuota(ctx, auth)
	case "gemini-cli":
		return h.refreshGeminiCLIQuota(ctx, auth)
	case "kimi":
		return h.refreshKimiQuota(ctx, auth)
	default:
		return fmt.Errorf("unsupported provider %q", provider)
	}
}

func (h *Handler) refreshCodexQuota(ctx context.Context, auth *coreauth.Auth) error {
	headers := cloneStringMap(codexQuotaHeaders)
	if accountID := codexAccountID(auth); accountID != "" {
		headers["Chatgpt-Account-Id"] = accountID
	}
	return h.callQuotaAPI(ctx, auth, http.MethodGet, codexQuotaUsageURL, headers, "")
}

func (h *Handler) refreshClaudeQuota(ctx context.Context, auth *coreauth.Auth) error {
	if err := h.callQuotaAPI(ctx, auth, http.MethodGet, claudeQuotaUsageURL, cloneStringMap(claudeQuotaHeaders), ""); err != nil {
		return err
	}
	_ = h.callQuotaAPI(ctx, auth, http.MethodGet, claudeQuotaProfileURL, cloneStringMap(claudeQuotaHeaders), "")
	return nil
}

func (h *Handler) refreshAntigravityQuota(ctx context.Context, auth *coreauth.Auth) error {
	body := fmt.Sprintf(`{"project":%q}`, h.quotaProjectID(auth))
	var lastErr error
	for _, quotaURL := range antigravityQuotaURLs {
		if err := h.callQuotaAPI(ctx, auth, http.MethodPost, quotaURL, cloneStringMap(antigravityQuotaHeaders), body); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("quota endpoint unavailable")
}

func (h *Handler) refreshGeminiCLIQuota(ctx context.Context, auth *coreauth.Auth) error {
	project := h.quotaProjectID(auth)
	body := fmt.Sprintf(`{"project":%q}`, project)
	if err := h.callQuotaAPI(ctx, auth, http.MethodPost, geminiCLIQuotaURL, cloneStringMap(geminiCLIQuotaHeaders), body); err != nil {
		return err
	}
	assistBody := fmt.Sprintf(`{"cloudaicompanionProject":%q,"metadata":{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI","duetProject":%q}}`, project, project)
	_ = h.callQuotaAPI(ctx, auth, http.MethodPost, geminiCLILoadAssistURL, cloneStringMap(geminiCLIQuotaHeaders), assistBody)
	return nil
}

func (h *Handler) refreshKimiQuota(ctx context.Context, auth *coreauth.Auth) error {
	return h.callQuotaAPI(ctx, auth, http.MethodGet, kimiQuotaUsageURL, cloneStringMap(kimiQuotaHeaders), "")
}

func (h *Handler) callQuotaAPI(ctx context.Context, auth *coreauth.Auth, method, url string, headers map[string]string, data string) error {
	idx := ""
	if auth != nil {
		idx = auth.EnsureIndex()
	}
	body := apiCallRequest{AuthIndexCamel: &idx, Method: method, URL: url, Header: headers, Data: data}
	resp, _, err := h.performAPICall(ctx, auth, body)
	if err != nil {
		return err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, quotaBodySummary(resp.Body))
	}
	return nil
}

func (h *Handler) quotaProjectID(auth *coreauth.Auth) string {
	for _, source := range []map[string]any{authAnyMap(auth, "metadata"), authAnyMap(auth, "attributes")} {
		if project := stringFromAnyMap(source, "project_id", "projectId", "project"); project != "" {
			return project
		}
	}
	if project := projectFromAccountString(auth); project != "" {
		return project
	}
	if project := h.projectIDFromAuthFile(auth); project != "" {
		return project
	}
	return geminiDefaultProjectID
}

func (h *Handler) projectIDFromAuthFile(auth *coreauth.Auth) string {
	if h == nil || h.cfg == nil || auth == nil || isUnsafeAuthFileName(auth.FileName) {
		return ""
	}
	full := filepath.Join(h.cfg.AuthDir, auth.FileName)
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	if project := stringFromAnyMap(payload, "project_id", "projectId", "project"); project != "" {
		return project
	}
	for _, key := range []string{"installed", "web"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if project := stringFromAnyMap(nested, "project_id", "projectId", "project"); project != "" {
				return project
			}
		}
	}
	return ""
}

func codexAccountID(auth *coreauth.Auth) string {
	for _, raw := range []string{
		stringFromAnyMap(authAnyMap(auth, "metadata"), "id_token"),
		stringFromAnyMap(authAnyMap(auth, "attributes"), "id_token"),
	} {
		if accountID := accountIDFromJWT(raw); accountID != "" {
			return accountID
		}
	}
	return ""
}

func accountIDFromJWT(raw string) string {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return ""
	}
	return stringFromAnyMap(payload, "chatgpt_account_id", "chatgptAccountId")
}

func projectFromAccountString(auth *coreauth.Auth) string {
	for _, raw := range []string{
		stringFromAnyMap(authAnyMap(auth, "metadata"), "account"),
		stringFromAnyMap(authAnyMap(auth, "attributes"), "account"),
	} {
		matches := accountProjectPattern.FindAllStringSubmatch(raw, -1)
		if len(matches) > 0 {
			project := strings.TrimSpace(matches[len(matches)-1][1])
			if project != "" {
				return project
			}
		}
	}
	return ""
}

func authAnyMap(auth *coreauth.Auth, field string) map[string]any {
	if auth == nil {
		return nil
	}
	switch field {
	case "metadata":
		return auth.Metadata
	case "attributes":
		out := make(map[string]any, len(auth.Attributes))
		for k, v := range auth.Attributes {
			out[k] = v
		}
		return out
	default:
		return nil
	}
}

func stringFromAnyMap(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch typed := value.(type) {
			case string:
				if out := strings.TrimSpace(typed); out != "" {
					return out
				}
			case fmt.Stringer:
				if out := strings.TrimSpace(typed.String()); out != "" {
					return out
				}
			}
		}
	}
	return ""
}

func normalizeQuotaProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.ReplaceAll(provider, "_", "-")
	if provider == "anthropic" {
		return "claude"
	}
	if provider == "geminicli" || provider == "gemini-cli" {
		return "gemini-cli"
	}
	return provider
}

func quotaProviderSupported(provider string) bool {
	switch normalizeQuotaProvider(provider) {
	case "codex", "claude", "antigravity", "gemini-cli", "kimi":
		return true
	default:
		return false
	}
}

func clampQuotaRefreshConcurrency(value int) int {
	if value <= 0 {
		return defaultQuotaRefreshConcurrency
	}
	if value > maxQuotaRefreshConcurrency {
		return maxQuotaRefreshConcurrency
	}
	return value
}

func quotaAuthDisplayName(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return strings.TrimSpace(auth.ID)
}

func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func quotaBodySummary(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return "empty response"
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		if msg := stringFromAnyMap(payload, "message", "error", "detail"); msg != "" {
			return truncateQuotaError(msg)
		}
		if nested, ok := payload["error"].(map[string]any); ok {
			if msg := stringFromAnyMap(nested, "message", "error", "detail"); msg != "" {
				return truncateQuotaError(msg)
			}
		}
	}
	return truncateQuotaError(body)
}

func truncateQuotaError(message string) string {
	message = strings.Join(strings.Fields(strings.TrimSpace(message)), " ")
	if len(message) <= 300 {
		return message
	}
	return message[:300] + "..."
}
