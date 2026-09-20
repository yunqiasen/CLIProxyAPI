package auth

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ValidateRetrievalRouting uses the same compiled alias/capability snapshot as
// execution. Embedding aliases must identify one upstream model even across
// requests, not just across attempts within a request.
func (m *Manager) ValidateRetrievalRouting(providers []string, req ex.Request, opts ex.Options) error {
	kind := ""
	switch opts.SourceFormat.String() {
	case "openai-embeddings":
		kind = "embeddings"
	case "cohere-rerank":
		kind = "rerank"
	default:
		return nil
	}
	if m.HomeEnabled() {
		return retrievalRoutingError("native retrieval requires local openai-compatibility routing; Home retrieval needs an explicit model capability contract")
	}
	allowed := make(map[string]bool, len(providers))
	for _, p := range providers {
		allowed[p] = true
	}
	routing := m.loadAPIKeyModelRouting()
	registryRef := registry.GetGlobalRegistry()
	upstream := ""
	found := false
	for _, a := range m.List() {
		if a.Disabled || !allowed[a.Provider] {
			continue
		}
		authHasRoute := false
		routeModel := rewriteModelForAuth(req.Model, a)
		_, candidates := modelAliasLookupCandidates(routeModel)
		for _, candidate := range candidates {
			for _, route := range routing.capabilities[a.ID][strings.ToLower(strings.TrimSpace(candidate))] {
				found = true
				authHasRoute = true
				if route.modelInfo == nil || route.modelInfo.Type != kind {
					return retrievalRoutingError("model alias includes a different endpoint type")
				}
				if kind == "embeddings" && upstream != "" && upstream != route.upstreamModel {
					return retrievalRoutingError("embedding alias maps to different upstream models; use a separate alias for each vector space")
				}
				upstream = route.upstreamModel
			}
		}
		// OAuth and other advertised routes may have no configured API-key capability.
		// Use the scheduler's model eligibility before allowing such a candidate.
		if !authHasRoute && m.authSupportsRouteModel(registryRef, a, req.Model) {
			return retrievalRoutingError("model alias includes a different endpoint type")
		}
	}
	if !found {
		return retrievalRoutingError("configure this model with type: " + kind + " under openai-compatibility")
	}
	return nil
}

func retrievalRoutingError(message string) error {
	return &Error{Code: requestScopedErrorCode, Message: message, HTTPStatus: http.StatusBadRequest}
}
