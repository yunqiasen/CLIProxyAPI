package auth

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

type credentialCooldownCause struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
	Status   int    `json:"upstream_status"`
	Count    int    `json:"count"`
}

type credentialCooldownError struct {
	model   string
	resetIn time.Duration
	causes  []credentialCooldownCause
}

func (e *credentialCooldownError) Error() string {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type": "server_error", "code": "upstream_credentials_cooling_down",
		"message": "All matching upstream credentials are cooling down after upstream payment, quota or authentication failures",
		"model":   e.model, "reset_seconds": int(math.Ceil(e.resetIn.Seconds())), "causes": e.causes,
	}})
	return string(body)
}

// CredentialCooldownDiagnostic returns only CPA-generated structured details.
// Raw upstream errors with a matching code are intentionally not trusted.
func CredentialCooldownDiagnostic(err error) []byte {
	var cooling *credentialCooldownError
	if !errors.As(err, &cooling) || cooling == nil {
		return nil
	}
	return []byte(cooling.Error())
}

func (e *credentialCooldownError) StatusCode() int { return http.StatusServiceUnavailable }
func (e *credentialCooldownError) Headers() http.Header {
	h := make(http.Header)
	h.Set("Retry-After", strconv.Itoa(int(math.Ceil(e.resetIn.Seconds()))))
	return h
}

// knownCredentialCooldownError preserves known failure causes without exposing keys
// or replaying arbitrary upstream error messages. It does not alter selection state.
func knownCredentialCooldownError(auths []*Auth, provider, model string, now time.Time) error {
	return credentialCooldownErrorForModels(auths, provider, model, now, func(*Auth) string { return model })
}

func credentialCooldownErrorForModels(auths []*Auth, provider, model string, now time.Time, selectionModel func(*Auth) string) error {
	if len(auths) == 0 {
		return nil
	}
	counts := make(map[credentialCooldownCause]int)
	var earliest time.Time
	for _, a := range auths {
		checkModel := selectionModel(a)
		blocked, reason, next := isAuthBlockedForModel(a, checkModel, now)
		if !blocked || reason == blockReasonDisabled || !next.After(now) {
			return nil
		}
		last := a.LastError
		if checkModel != "" {
			var latest time.Time
			for key, state := range a.ModelStates {
				if state != nil && canonicalModelKey(key) == canonicalModelKey(checkModel) && !state.UpdatedAt.Before(latest) {
					last = state.LastError
					latest = state.UpdatedAt
				}
			}
		}
		if last == nil {
			return nil
		}
		code := ""
		status := statusCodeFromResult(last)
		switch status {
		case 401:
			code = "upstream_authentication_failed"
		case 402:
			code = "upstream_payment_required"
			if strings.HasPrefix(gjson.Get(last.Message, "error.message").String(), "Budget pool quota has been exhausted.") {
				code = "upstream_budget_pool_exhausted"
			}
		case 403:
			code = "upstream_access_denied"
			if gjson.Get(last.Message, "error.code").String() == "insufficient_user_quota" {
				code = "upstream_account_quota_exhausted"
			}
		default:
			return nil
		}
		label := strings.TrimSpace(a.Attributes["provider_name"])
		if label == "" {
			label = provider
		}
		if label == "" || label == "mixed" {
			label = a.Provider
		}
		counts[credentialCooldownCause{Provider: label, Code: code, Status: status}]++
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	causes := make([]credentialCooldownCause, 0, len(counts))
	for cause, count := range counts {
		cause.Count = count
		causes = append(causes, cause)
	}
	sort.Slice(causes, func(i, j int) bool {
		if causes[i].Provider != causes[j].Provider {
			return causes[i].Provider < causes[j].Provider
		}
		return causes[i].Code < causes[j].Code
	})
	return &credentialCooldownError{model: model, resetIn: earliest.Sub(now), causes: causes}
}

func (m *modelScheduler) matchingAuths(predicate func(*scheduledAuth) bool) []*Auth {
	var auths []*Auth
	for _, entry := range m.entries {
		if entry != nil && (predicate == nil || predicate(entry)) {
			auths = append(auths, entry.auth)
		}
	}
	return auths
}
