package management

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type apiKeyUsageEntry struct {
	Success        int64                          `json:"success"`
	Failed         int64                          `json:"failed"`
	RecentRequests []coreauth.RecentRequestBucket `json:"recent_requests"`
	SuccessDetails []apiKeyUsageSuccessDetail     `json:"success_details,omitempty"`
	FailureDetails []apiKeyUsageFailureDetail     `json:"failure_details,omitempty"`
}

type apiKeyUsageSuccessDetail struct {
	Model  string `json:"model"`
	Status int    `json:"status"`
	Count  int64  `json:"count"`
}

type apiKeyUsageFailureDetail struct {
	Model  string `json:"model"`
	Status int    `json:"status"`
	Error  string `json:"error"`
	Count  int64  `json:"count"`
}

type apiKeyUsageLookupKey struct {
	Provider  string
	Protocol  string
	BaseURL   string
	Composite string
}

type apiKeyUsageHistoricalIdentity struct {
	Provider    string
	Protocol    string
	UpstreamURL string
}

const (
	apiKeyUsageBucketSeconds int64 = 10 * 60
	apiKeyUsageBucketCount         = 20
	apiKeyUsageDetailLimit         = 10
)

func emptyAPIKeyUsageBuckets(now time.Time) []coreauth.RecentRequestBucket {
	out := make([]coreauth.RecentRequestBucket, 0, apiKeyUsageBucketCount)
	currentBucketID := now.Unix() / apiKeyUsageBucketSeconds
	for i := apiKeyUsageBucketCount - 1; i >= 0; i-- {
		bucketID := currentBucketID - int64(i)
		start := time.Unix(bucketID*apiKeyUsageBucketSeconds, 0).In(time.Local)
		end := start.Add(time.Duration(apiKeyUsageBucketSeconds) * time.Second)
		out = append(out, coreauth.RecentRequestBucket{Time: start.Format("15:04") + "-" + end.Format("15:04")})
	}
	return out
}

func recordAPIKeyUsageBucket(buckets []coreauth.RecentRequestBucket, now time.Time, timestampUnix int64, success bool) {
	if len(buckets) == 0 || timestampUnix <= 0 {
		return
	}
	currentBucketID := now.Unix() / apiKeyUsageBucketSeconds
	firstBucketID := currentBucketID - int64(len(buckets)-1)
	bucketID := timestampUnix / apiKeyUsageBucketSeconds
	idx := int(bucketID - firstBucketID)
	if idx < 0 || idx >= len(buckets) {
		return
	}
	if success {
		buckets[idx].Success++
		return
	}
	buckets[idx].Failed++
}

func requestLogCutoffSQL(cutoff *int64) (string, []any) {
	if cutoff == nil {
		return "", nil
	}
	return " AND timestamp_unix >= ?", []any{*cutoff}
}

func (s *requestLogStore) apiKeyUsageIdentityByAuthID(ctx context.Context, cutoff *int64) (map[string]apiKeyUsageHistoricalIdentity, error) {
	cutoffSQL, cutoffArgs := requestLogCutoffSQL(cutoff)
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(auth_id, ''), COALESCE(NULLIF(TRIM(provider_name), ''), NULLIF(TRIM(provider), ''), ''), COALESCE(provider, ''), COALESCE(upstream_url, '') FROM request_log_entries WHERE COALESCE(auth_id, '') != ''`+cutoffSQL+` ORDER BY timestamp_unix DESC`, cutoffArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]apiKeyUsageHistoricalIdentity)
	for rows.Next() {
		var authID string
		var provider string
		var protocol string
		var upstreamURL string
		if err := rows.Scan(&authID, &provider, &protocol, &upstreamURL); err != nil {
			return nil, err
		}
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, exists := out[authID]; !exists {
			out[authID] = apiKeyUsageHistoricalIdentity{
				Provider:    strings.ToLower(strings.TrimSpace(provider)),
				Protocol:    strings.ToLower(strings.TrimSpace(protocol)),
				UpstreamURL: strings.TrimSpace(upstreamURL),
			}
		}
	}
	return out, rows.Err()
}

func (s *requestLogStore) apiKeyUsageByAuthID(ctx context.Context, now time.Time, cutoff *int64) (map[string]apiKeyUsageEntry, error) {
	cutoffSQL, cutoffArgs := requestLogCutoffSQL(cutoff)
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(auth_id, ''), COALESCE(success, 0), COALESCE(timestamp_unix, 0) FROM request_log_entries WHERE COALESCE(auth_id, '') != ''`+cutoffSQL, cutoffArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]apiKeyUsageEntry)
	for rows.Next() {
		var authID string
		var successInt int
		var timestampUnix int64
		if err := rows.Scan(&authID, &successInt, &timestampUnix); err != nil {
			return nil, err
		}
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		entry := out[authID]
		if len(entry.RecentRequests) == 0 {
			entry.RecentRequests = emptyAPIKeyUsageBuckets(now)
		}
		success := successInt != 0
		if success {
			entry.Success++
		} else {
			entry.Failed++
		}
		recordAPIKeyUsageBucket(entry.RecentRequests, now, timestampUnix, success)
		out[authID] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachAPIKeyUsageDetails(ctx, cutoff, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *requestLogStore) attachAPIKeyUsageDetails(ctx context.Context, cutoff *int64, out map[string]apiKeyUsageEntry) error {
	if len(out) == 0 {
		return nil
	}
	if err := s.attachAPIKeyUsageSuccessDetails(ctx, cutoff, out); err != nil {
		return err
	}
	return s.attachAPIKeyUsageFailureDetails(ctx, cutoff, out)
}

func (s *requestLogStore) attachAPIKeyUsageSuccessDetails(ctx context.Context, cutoff *int64, out map[string]apiKeyUsageEntry) error {
	cutoffSQL, cutoffArgs := requestLogCutoffSQL(cutoff)
	rows, err := s.db.QueryContext(ctx, `
SELECT auth_id, model_name, status_code, request_count
FROM (
  SELECT
    COALESCE(auth_id, '') AS auth_id,
    COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(upstream_model), ''), 'unknown') AS model_name,
    COALESCE(status, 0) AS status_code,
    COUNT(1) AS request_count
  FROM request_log_entries
  WHERE COALESCE(auth_id, '') != ''`+cutoffSQL+` AND COALESCE(success, 0) != 0
  GROUP BY auth_id, model_name, status_code
)
ORDER BY auth_id ASC, request_count DESC, model_name ASC, status_code ASC`, cutoffArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen := make(map[string]int)
	for rows.Next() {
		var authID string
		var model string
		var status int
		var count int64
		if err := rows.Scan(&authID, &model, &status, &count); err != nil {
			return err
		}
		authID = strings.TrimSpace(authID)
		if authID == "" || seen[authID] >= apiKeyUsageDetailLimit {
			continue
		}
		entry, ok := out[authID]
		if !ok {
			continue
		}
		entry.SuccessDetails = append(entry.SuccessDetails, apiKeyUsageSuccessDetail{Model: apiKeyUsageText(model, "unknown"), Status: status, Count: count})
		out[authID] = entry
		seen[authID]++
	}
	return rows.Err()
}

func (s *requestLogStore) attachAPIKeyUsageFailureDetails(ctx context.Context, cutoff *int64, out map[string]apiKeyUsageEntry) error {
	cutoffSQL, cutoffArgs := requestLogCutoffSQL(cutoff)
	rows, err := s.db.QueryContext(ctx, `
SELECT auth_id, model_name, status_code, error_text, request_count
FROM (
  SELECT
    COALESCE(auth_id, '') AS auth_id,
    COALESCE(NULLIF(TRIM(model), ''), NULLIF(TRIM(upstream_model), ''), 'unknown') AS model_name,
    COALESCE(status, 0) AS status_code,
    COALESCE(
      NULLIF(TRIM(error_preview), ''),
      NULLIF(TRIM(error), ''),
      CASE WHEN COALESCE(status, 0) > 0 THEN 'HTTP ' || COALESCE(status, 0) ELSE NULL END,
      'unknown'
    ) AS error_text,
    COUNT(1) AS request_count
  FROM request_log_entries
  WHERE COALESCE(auth_id, '') != ''`+cutoffSQL+` AND (COALESCE(success, 0) = 0 OR COALESCE(has_error, 0) != 0)
  GROUP BY auth_id, model_name, status_code, error_text
)
ORDER BY auth_id ASC, request_count DESC, model_name ASC, status_code ASC, error_text ASC`, cutoffArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen := make(map[string]int)
	for rows.Next() {
		var authID string
		var model string
		var status int
		var errorText string
		var count int64
		if err := rows.Scan(&authID, &model, &status, &errorText, &count); err != nil {
			return err
		}
		authID = strings.TrimSpace(authID)
		if authID == "" || seen[authID] >= apiKeyUsageDetailLimit {
			continue
		}
		entry, ok := out[authID]
		if !ok {
			continue
		}
		entry.FailureDetails = append(entry.FailureDetails, apiKeyUsageFailureDetail{Model: apiKeyUsageText(model, "unknown"), Status: status, Error: apiKeyUsageText(errorText, "unknown"), Count: count})
		out[authID] = entry
		seen[authID]++
	}
	return rows.Err()
}

func apiKeyUsageText(value, fallback string) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if text == "" {
		text = fallback
	}
	runes := []rune(text)
	if len(runes) <= 240 {
		return text
	}
	return string(runes[:240]) + "..."
}

func usageProtocolsMatch(historical, current string) bool {
	historical = strings.ToLower(strings.TrimSpace(historical))
	current = strings.ToLower(strings.TrimSpace(current))
	return historical != "" && historical == current
}

func uniqueAPIKeyUsageProtocolBaseMatch(lookup map[string]apiKeyUsageLookupKey, identity apiKeyUsageHistoricalIdentity) (apiKeyUsageLookupKey, bool) {
	matches := make(map[string]apiKeyUsageLookupKey)
	customMatches := make(map[string]apiKeyUsageLookupKey)
	for _, candidate := range lookup {
		if !usageProtocolsMatch(identity.Protocol, candidate.Protocol) {
			continue
		}
		upstreamURL := strings.TrimRight(strings.ToLower(identity.UpstreamURL), "/")
		baseURL := strings.TrimRight(strings.ToLower(candidate.BaseURL), "/")
		if baseURL != "" && upstreamURL != baseURL && !strings.HasPrefix(upstreamURL, baseURL+"/") {
			continue
		}
		groupKey := candidate.Provider + "\x00" + candidate.BaseURL
		matches[groupKey] = candidate
		if candidate.Provider != candidate.Protocol {
			customMatches[groupKey] = candidate
		}
	}
	if len(customMatches) == 1 {
		for _, candidate := range customMatches {
			return candidate, true
		}
	}
	if len(matches) == 1 {
		for _, candidate := range matches {
			return candidate, true
		}
	}
	return apiKeyUsageLookupKey{}, false
}

func (h *Handler) persistedAPIKeyUsage(ctx context.Context, now time.Time, lookup map[string]apiKeyUsageLookupKey) map[apiKeyUsageLookupKey]apiKeyUsageEntry {
	if h == nil || len(lookup) == 0 {
		return nil
	}
	manager, _ := h.requestLogIndexSnapshot()
	if manager == nil {
		return nil
	}
	manager.TriggerSync()
	cutoff := requestLogRetentionCutoff(now, h.requestLogRetentionDays())
	byAuthID, errUsage := manager.APIKeyUsageByAuthID(ctx, now, cutoff)
	if errUsage != nil {
		return nil
	}
	identityByAuthID, errIdentity := manager.APIKeyUsageIdentityByAuthID(ctx, cutoff)
	if errIdentity != nil {
		return nil
	}
	providerFallback := make(map[string]apiKeyUsageLookupKey)
	for _, key := range lookup {
		if key.Provider != "" && key.Composite != "" {
			if _, exists := providerFallback[key.Provider]; !exists {
				providerFallback[key.Provider] = key
			}
		}
	}

	out := make(map[apiKeyUsageLookupKey]apiKeyUsageEntry)
	for authID, usage := range byAuthID {
		identity := identityByAuthID[authID]
		key, ok := lookup[authID]
		if remapped, matched := uniqueAPIKeyUsageProtocolBaseMatch(lookup, identity); matched && remapped.Provider != key.Provider {
			key = remapped
			ok = true
		}
		if !ok {
			key, ok = providerFallback[identity.Provider]
			if !ok {
				key, ok = uniqueAPIKeyUsageProtocolBaseMatch(lookup, identity)
			}
		}
		if !ok || key.Provider == "" || key.Composite == "" {
			continue
		}
		existing := out[key]
		existing.Success += usage.Success
		existing.Failed += usage.Failed
		existing.RecentRequests = mergeRecentRequestBuckets(existing.RecentRequests, usage.RecentRequests)
		existing.SuccessDetails = mergeAPIKeyUsageSuccessDetails(existing.SuccessDetails, usage.SuccessDetails)
		existing.FailureDetails = mergeAPIKeyUsageFailureDetails(existing.FailureDetails, usage.FailureDetails)
		out[key] = existing
	}
	return out
}

func mergeAPIKeyUsageSuccessDetails(dst, src []apiKeyUsageSuccessDetail) []apiKeyUsageSuccessDetail {
	if len(dst) == 0 {
		return limitAPIKeyUsageSuccessDetails(src)
	}
	counts := make(map[string]apiKeyUsageSuccessDetail)
	for _, item := range append(append([]apiKeyUsageSuccessDetail{}, dst...), src...) {
		model := apiKeyUsageText(item.Model, "unknown")
		key := model + "\x00" + strconv.Itoa(item.Status)
		existing := counts[key]
		existing.Model = model
		existing.Status = item.Status
		existing.Count += item.Count
		counts[key] = existing
	}
	merged := make([]apiKeyUsageSuccessDetail, 0, len(counts))
	for _, item := range counts {
		merged = append(merged, item)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Count != merged[j].Count {
			return merged[i].Count > merged[j].Count
		}
		if merged[i].Model != merged[j].Model {
			return merged[i].Model < merged[j].Model
		}
		return merged[i].Status < merged[j].Status
	})
	return limitAPIKeyUsageSuccessDetails(merged)
}

func mergeAPIKeyUsageFailureDetails(dst, src []apiKeyUsageFailureDetail) []apiKeyUsageFailureDetail {
	if len(dst) == 0 {
		return limitAPIKeyUsageFailureDetails(src)
	}
	counts := make(map[string]apiKeyUsageFailureDetail)
	for _, item := range append(append([]apiKeyUsageFailureDetail{}, dst...), src...) {
		model := apiKeyUsageText(item.Model, "unknown")
		errorText := apiKeyUsageText(item.Error, "unknown")
		key := model + "\x00" + strconv.Itoa(item.Status) + "\x00" + errorText
		existing := counts[key]
		existing.Model = model
		existing.Status = item.Status
		existing.Error = errorText
		existing.Count += item.Count
		counts[key] = existing
	}
	merged := make([]apiKeyUsageFailureDetail, 0, len(counts))
	for _, item := range counts {
		merged = append(merged, item)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Count != merged[j].Count {
			return merged[i].Count > merged[j].Count
		}
		if merged[i].Model != merged[j].Model {
			return merged[i].Model < merged[j].Model
		}
		if merged[i].Status != merged[j].Status {
			return merged[i].Status < merged[j].Status
		}
		return merged[i].Error < merged[j].Error
	})
	return limitAPIKeyUsageFailureDetails(merged)
}

func limitAPIKeyUsageSuccessDetails(items []apiKeyUsageSuccessDetail) []apiKeyUsageSuccessDetail {
	if len(items) > apiKeyUsageDetailLimit {
		return items[:apiKeyUsageDetailLimit]
	}
	return items
}

func limitAPIKeyUsageFailureDetails(items []apiKeyUsageFailureDetail) []apiKeyUsageFailureDetail {
	if len(items) > apiKeyUsageDetailLimit {
		return items[:apiKeyUsageDetailLimit]
	}
	return items
}

func mergeRecentRequestBuckets(dst, src []coreauth.RecentRequestBucket) []coreauth.RecentRequestBucket {
	if len(dst) == 0 {
		return src
	}
	if len(src) == 0 {
		return dst
	}
	if len(dst) != len(src) {
		n := len(dst)
		if len(src) < n {
			n = len(src)
		}
		for i := 0; i < n; i++ {
			dst[i].Success += src[i].Success
			dst[i].Failed += src[i].Failed
		}
		return dst
	}
	for i := range dst {
		dst[i].Success += src[i].Success
		dst[i].Failed += src[i].Failed
	}
	return dst
}

func apiKeyUsageProviderKey(auth *coreauth.Auth) string {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if auth.Attributes != nil {
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			provider = strings.ToLower(compatName)
		} else if providerName := strings.TrimSpace(auth.Attributes["provider_name"]); providerName != "" {
			provider = strings.ToLower(providerName)
		}
	}
	if provider == "" {
		return "unknown"
	}
	return provider
}

// GetAPIKeyUsage returns recent request buckets for all in-memory api_key auths,
// grouped by provider and keyed by "base_url|api_key".
func (h *Handler) GetAPIKeyUsage(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	now := time.Now()
	out := make(map[string]map[string]apiKeyUsageEntry)
	lookup := make(map[string]apiKeyUsageLookupKey)
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		kind, apiKey := auth.AccountInfo()
		if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
			continue
		}
		apiKey = strings.TrimSpace(apiKey)
		isMediaAuth := false
		if auth.Attributes != nil {
			isMediaAuth = strings.TrimSpace(auth.Attributes["media_kind"]) != ""
		}
		if apiKey == "" && !isMediaAuth {
			continue
		}
		baseURL := ""
		if auth.Attributes != nil {
			baseURL = strings.TrimSpace(auth.Attributes["base_url"])
			if baseURL == "" {
				baseURL = strings.TrimSpace(auth.Attributes["base-url"])
			}
		}
		compositeKey := baseURL + "|" + apiKey
		provider := apiKeyUsageProviderKey(auth)
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			lookup[authID] = apiKeyUsageLookupKey{Provider: provider, Protocol: strings.ToLower(strings.TrimSpace(auth.Provider)), BaseURL: baseURL, Composite: compositeKey}
		}

		recent := auth.RecentRequestsSnapshot(now)
		providerBucket, ok := out[provider]
		if !ok {
			providerBucket = make(map[string]apiKeyUsageEntry)
			out[provider] = providerBucket
		}
		if existing, exists := providerBucket[compositeKey]; exists {
			existing.Success += auth.Success
			existing.Failed += auth.Failed
			existing.RecentRequests = mergeRecentRequestBuckets(existing.RecentRequests, recent)
			providerBucket[compositeKey] = existing
			continue
		}
		providerBucket[compositeKey] = apiKeyUsageEntry{
			Success:        auth.Success,
			Failed:         auth.Failed,
			RecentRequests: recent,
		}
	}

	for key, persisted := range h.persistedAPIKeyUsage(c.Request.Context(), now, lookup) {
		providerBucket, ok := out[key.Provider]
		if !ok {
			providerBucket = make(map[string]apiKeyUsageEntry)
			out[key.Provider] = providerBucket
		}
		existing := providerBucket[key.Composite]
		if persisted.Success+persisted.Failed > existing.Success+existing.Failed {
			providerBucket[key.Composite] = persisted
			continue
		}
		existing.SuccessDetails = mergeAPIKeyUsageSuccessDetails(existing.SuccessDetails, persisted.SuccessDetails)
		existing.FailureDetails = mergeAPIKeyUsageFailureDetails(existing.FailureDetails, persisted.FailureDetails)
		providerBucket[key.Composite] = existing
	}

	h.setRequestLogSyncHeaders(c)
	c.JSON(http.StatusOK, out)
}
