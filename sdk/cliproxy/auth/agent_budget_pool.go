package auth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Agent's budget pool belongs to its upstream route, not necessarily the caller's
// account or API key. Preserve the failure without quarantining a healthy key.
func isAgentBudgetPoolResultError(auth *Auth, err *Error) bool {
	if auth == nil || err == nil || err.HTTPStatus != http.StatusPaymentRequired || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	baseURL, errParse := url.Parse(strings.TrimSpace(auth.Attributes["base_url"]))
	if errParse != nil || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || !strings.EqualFold(baseURL.Hostname(), "agentrouter.org") {
		return false
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Message), &body) != nil {
		return false
	}
	return body.Error.Type == "bad_response_status_code" && body.Error.Code == "bad_response_status_code" &&
		strings.HasPrefix(body.Error.Message, "Budget pool quota has been exhausted.")
}

func shouldSkipCredentialCooldownForAuth(auth *Auth, err *Error) bool {
	return shouldSkipCredentialCooldown(err) || isAgentBudgetPoolResultError(auth, err)
}
