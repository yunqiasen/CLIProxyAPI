package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
)

type mediaAPIKeyEntryWithAuthIndex struct {
	config.MediaAPIKeyEntry
	AuthIndex string `json:"auth-index,omitempty"`
}

type mediaProviderWithAuthIndex struct {
	config.MediaProvider
	APIKeyEntries []mediaAPIKeyEntryWithAuthIndex `json:"api-key-entries,omitempty"`
	AuthIndex     string                          `json:"auth-index,omitempty"`
}

// GetMediaProviders returns configured media providers with runtime auth indexes.
func (h *Handler) GetMediaProviders(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"media-providers": h.mediaProvidersWithAuthIndex()})
}

func (h *Handler) mediaProvidersWithAuthIndex() []mediaProviderWithAuthIndex {
	if h == nil {
		return nil
	}
	live := h.liveAuthIndexByID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return nil
	}
	providers := synthesizer.MaterializeMediaProviderAuthIDs(h.cfg.MediaProviders)
	out := make([]mediaProviderWithAuthIndex, 0, len(providers))
	for i := range providers {
		provider := providers[i]
		response := mediaProviderWithAuthIndex{MediaProvider: provider}
		if len(provider.APIKeyEntries) == 0 {
			response.AuthIndex = live[provider.AuthID]
		} else {
			response.APIKeyEntries = make([]mediaAPIKeyEntryWithAuthIndex, 0, len(provider.APIKeyEntries))
			for _, entry := range provider.APIKeyEntries {
				response.APIKeyEntries = append(response.APIKeyEntries, mediaAPIKeyEntryWithAuthIndex{
					MediaAPIKeyEntry: entry,
					AuthIndex:        live[entry.AuthID],
				})
			}
		}
		out = append(out, response)
	}
	return out
}

// PutMediaProviders replaces the media provider list after validation and normalization.
func (h *Handler) PutMediaProviders(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var providers []config.MediaProvider
	if err = json.Unmarshal(data, &providers); err != nil {
		var wrapper struct {
			Items []config.MediaProvider `json:"items"`
		}
		if errWrap := json.Unmarshal(data, &wrapper); errWrap != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		providers = wrapper.Items
	}
	h.decodeMediaProviderAuthIndexes(providers)
	if errValidate := validateMediaProviders(providers); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	candidate := &config.Config{MediaProviders: providers}
	candidate.SanitizeMediaProviders()
	if len(providers) != 0 && len(candidate.MediaProviders) != len(providers) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media-providers contains an invalid provider"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		h.cfg = &config.Config{}
	}
	h.cfg.MediaProviders = prepareMediaProviderAuthIDs(h.cfg.MediaProviders, candidate.MediaProviders)
	if !h.persistLocked(c) {
		return
	}
}

// PatchMediaProviders replaces one media provider. PUT remains the preferred full-list operation.
func (h *Handler) PatchMediaProviders(c *gin.Context) {
	var body struct {
		Index *int                  `json:"index"`
		Name  *string               `json:"name"`
		Kind  *string               `json:"kind"`
		Value *config.MediaProvider `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	liveAuthIDs := h.liveAuthIDByIndex()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	index := -1
	if body.Index != nil && *body.Index >= 0 && *body.Index < len(h.cfg.MediaProviders) {
		index = *body.Index
	}
	if index < 0 && body.Name != nil {
		for i := range h.cfg.MediaProviders {
			if strings.EqualFold(strings.TrimSpace(h.cfg.MediaProviders[i].Name), strings.TrimSpace(*body.Name)) {
				if body.Kind == nil || strings.EqualFold(strings.TrimSpace(h.cfg.MediaProviders[i].Kind), strings.TrimSpace(*body.Kind)) {
					index = i
					break
				}
			}
		}
	}
	if index < 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	h.decodeMediaProviderAuthIndex(&body.Value.AuthID, body.Value.APIKeyEntries, liveAuthIDs)
	if errValidate := validateMediaProviders([]config.MediaProvider{*body.Value}); errValidate != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errValidate.Error()})
		return
	}
	candidate := &config.Config{MediaProviders: []config.MediaProvider{*body.Value}}
	candidate.SanitizeMediaProviders()
	if len(candidate.MediaProviders) != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media provider"})
		return
	}
	nextProviders := append([]config.MediaProvider(nil), h.cfg.MediaProviders...)
	nextProviders[index] = candidate.MediaProviders[0]
	h.cfg.MediaProviders = prepareMediaProviderAuthIDs(h.cfg.MediaProviders, nextProviders)
	h.persistLocked(c)
}

func (h *Handler) decodeMediaProviderAuthIndexes(providers []config.MediaProvider) {
	if h == nil {
		return
	}
	liveIDs := h.liveAuthIDByIndex()
	for i := range providers {
		h.decodeMediaProviderAuthIndex(&providers[i].AuthID, providers[i].APIKeyEntries, liveIDs)
	}
}

func (h *Handler) decodeMediaProviderAuthIndex(providerID *string, entries []config.MediaAPIKeyEntry, provided ...map[string]string) {
	if h == nil {
		return
	}
	liveIDs := map[string]string(nil)
	if len(provided) > 0 && provided[0] != nil {
		liveIDs = provided[0]
	} else {
		liveIDs = h.liveAuthIDByIndex()
	}
	if providerID != nil {
		if stableID := liveIDs[strings.TrimSpace(*providerID)]; stableID != "" {
			*providerID = stableID
		}
	}
	for i := range entries {
		if stableID := liveIDs[strings.TrimSpace(entries[i].AuthID)]; stableID != "" {
			entries[i].AuthID = stableID
		}
	}
}

func prepareMediaProviderAuthIDs(previous, incoming []config.MediaProvider) []config.MediaProvider {
	materializedPrevious := synthesizer.MaterializeMediaProviderAuthIDs(previous)
	prepared := append([]config.MediaProvider(nil), incoming...)
	usedPrevious := make(map[int]struct{}, len(materializedPrevious))
	previousByAuthID := mediaProviderAuthIDOwners(materializedPrevious)
	for i := range prepared {
		match := -1
		// The management API sends the opaque auth-index back with edits. It is
		// the only identity that remains valid when both name and URL change or
		// when the provider list is reordered.
		for _, authID := range mediaProviderAuthIDs(prepared[i]) {
			if candidate, ok := previousByAuthID[authID]; ok {
				if _, used := usedPrevious[candidate]; !used && strings.EqualFold(strings.TrimSpace(materializedPrevious[candidate].Kind), strings.TrimSpace(prepared[i].Kind)) {
					match = candidate
					break
				}
			}
		}
		if match < 0 {
			for j := range materializedPrevious {
				if _, used := usedPrevious[j]; used {
					continue
				}
				if mediaProviderIdentityMatches(materializedPrevious[j], prepared[i]) {
					match = j
					break
				}
			}
		}
		if match < 0 && i < len(materializedPrevious) {
			if _, used := usedPrevious[i]; !used && strings.EqualFold(materializedPrevious[i].Kind, prepared[i].Kind) {
				match = i
			}
		}
		if match >= 0 {
			inheritMediaProviderAuthIDs(previous[match], materializedPrevious[match], &prepared[i])
			usedPrevious[match] = struct{}{}
		} else {
			clearMediaProviderAuthIDs(&prepared[i])
		}
	}
	assignFreshMediaProviderAuthIDs(prepared)
	return synthesizer.MaterializeMediaProviderAuthIDs(prepared)
}

func mediaProviderAuthIDs(provider config.MediaProvider) []string {
	ids := make([]string, 0, len(provider.APIKeyEntries)+1)
	if id := strings.TrimSpace(provider.AuthID); id != "" {
		ids = append(ids, id)
	}
	for i := range provider.APIKeyEntries {
		if id := strings.TrimSpace(provider.APIKeyEntries[i].AuthID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func mediaProviderAuthIDOwners(providers []config.MediaProvider) map[string]int {
	owners := make(map[string]int)
	ambiguous := make(map[string]struct{})
	for i := range providers {
		for _, authID := range mediaProviderAuthIDs(providers[i]) {
			if _, isAmbiguous := ambiguous[authID]; isAmbiguous {
				continue
			}
			if previous, exists := owners[authID]; exists && previous != i {
				delete(owners, authID)
				ambiguous[authID] = struct{}{}
				continue
			}
			owners[authID] = i
		}
	}
	return owners
}

func mediaProviderIdentityMatches(left, right config.MediaProvider) bool {
	return strings.EqualFold(strings.TrimSpace(left.Kind), strings.TrimSpace(right.Kind)) &&
		strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name)) &&
		strings.EqualFold(strings.TrimRight(strings.TrimSpace(left.BaseURL), "/"), strings.TrimRight(strings.TrimSpace(right.BaseURL), "/"))
}

func mediaProviderHasPersistedAuthID(provider config.MediaProvider) bool {
	if strings.TrimSpace(provider.AuthID) != "" {
		return true
	}
	for i := range provider.APIKeyEntries {
		if strings.TrimSpace(provider.APIKeyEntries[i].AuthID) != "" {
			return true
		}
	}
	return false
}

func mediaProviderAuthIDSlots(provider config.MediaProvider) map[string]int {
	out := make(map[string]int, len(provider.APIKeyEntries)+1)
	if id := strings.TrimSpace(provider.AuthID); id != "" {
		out[id] = -1
	}
	for i := range provider.APIKeyEntries {
		if id := strings.TrimSpace(provider.APIKeyEntries[i].AuthID); id != "" {
			out[id] = i
		}
	}
	return out
}

func inheritMediaProviderAuthIDs(rawPrevious, previous config.MediaProvider, incoming *config.MediaProvider) {
	if incoming == nil {
		return
	}
	previousSlots := mediaProviderAuthIDSlots(previous)
	usedPrevious := make(map[int]struct{}, len(previousSlots))
	legacyPrevious := !mediaProviderHasPersistedAuthID(rawPrevious)

	if len(incoming.APIKeyEntries) == 0 {
		incomingID := strings.TrimSpace(incoming.AuthID)
		if slot, ok := previousSlots[incomingID]; incomingID != "" && ok {
			usedPrevious[slot] = struct{}{}
			incoming.AuthID = incomingID
			return
		}
		incoming.AuthID = ""
		if legacyPrevious {
			switch len(previous.APIKeyEntries) {
			case 0:
				incoming.AuthID = previous.AuthID
			case 1:
				incoming.AuthID = previous.APIKeyEntries[0].AuthID
			}
		}
		return
	}

	incoming.AuthID = ""
	for i := range incoming.APIKeyEntries {
		incomingID := strings.TrimSpace(incoming.APIKeyEntries[i].AuthID)
		if incomingID == "" {
			continue
		}
		slot, ok := previousSlots[incomingID]
		if !ok {
			incoming.APIKeyEntries[i].AuthID = ""
			continue
		}
		if _, used := usedPrevious[slot]; used {
			incoming.APIKeyEntries[i].AuthID = ""
			continue
		}
		incoming.APIKeyEntries[i].AuthID = incomingID
		usedPrevious[slot] = struct{}{}
	}
	if !legacyPrevious {
		return
	}

	for i := range incoming.APIKeyEntries {
		if strings.TrimSpace(incoming.APIKeyEntries[i].AuthID) != "" {
			continue
		}
		for j := range previous.APIKeyEntries {
			if _, used := usedPrevious[j]; used {
				continue
			}
			if strings.TrimSpace(previous.APIKeyEntries[j].APIKey) == strings.TrimSpace(incoming.APIKeyEntries[i].APIKey) &&
				strings.TrimSpace(previous.APIKeyEntries[j].ProxyURL) == strings.TrimSpace(incoming.APIKeyEntries[i].ProxyURL) {
				incoming.APIKeyEntries[i].AuthID = previous.APIKeyEntries[j].AuthID
				usedPrevious[j] = struct{}{}
				break
			}
		}
	}
	if len(incoming.APIKeyEntries) < len(previous.APIKeyEntries) {
		return
	}
	for i := range incoming.APIKeyEntries {
		if strings.TrimSpace(incoming.APIKeyEntries[i].AuthID) != "" {
			continue
		}
		for j := range previous.APIKeyEntries {
			if _, used := usedPrevious[j]; used {
				continue
			}
			incoming.APIKeyEntries[i].AuthID = previous.APIKeyEntries[j].AuthID
			usedPrevious[j] = struct{}{}
			break
		}
	}
}

func clearMediaProviderAuthIDs(provider *config.MediaProvider) {
	if provider == nil {
		return
	}
	provider.AuthID = ""
	for i := range provider.APIKeyEntries {
		provider.APIKeyEntries[i].AuthID = ""
	}
}

func assignFreshMediaProviderAuthIDs(providers []config.MediaProvider) {
	used := make(map[string]struct{})
	for i := range providers {
		provider := &providers[i]
		if len(provider.APIKeyEntries) == 0 {
			if id := strings.TrimSpace(provider.AuthID); id != "" {
				used[id] = struct{}{}
			}
			continue
		}
		provider.AuthID = ""
		for j := range provider.APIKeyEntries {
			if id := strings.TrimSpace(provider.APIKeyEntries[j].AuthID); id != "" {
				used[id] = struct{}{}
			}
		}
	}
	newID := func(kind string) string {
		for {
			candidate := "media-provider:" + strings.ToLower(strings.TrimSpace(kind)) + ":" + uuid.NewString()
			if _, exists := used[candidate]; exists {
				continue
			}
			used[candidate] = struct{}{}
			return candidate
		}
	}
	for i := range providers {
		provider := &providers[i]
		if len(provider.APIKeyEntries) == 0 {
			if strings.TrimSpace(provider.AuthID) == "" {
				provider.AuthID = newID(provider.Kind)
			}
			continue
		}
		for j := range provider.APIKeyEntries {
			if strings.TrimSpace(provider.APIKeyEntries[j].AuthID) == "" {
				provider.APIKeyEntries[j].AuthID = newID(provider.Kind)
			}
		}
	}
}

// DeleteMediaProviders deletes a provider by index or name/kind.
func (h *Handler) DeleteMediaProviders(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing index or name"})
		return
	}
	index := -1
	if raw := strings.TrimSpace(c.Query("index")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed >= 0 && parsed < len(h.cfg.MediaProviders) {
			index = parsed
		}
	}
	if index < 0 && strings.TrimSpace(c.Query("name")) != "" {
		name := strings.TrimSpace(c.Query("name"))
		kind := strings.TrimSpace(c.Query("kind"))
		for i := range h.cfg.MediaProviders {
			if strings.EqualFold(strings.TrimSpace(h.cfg.MediaProviders[i].Name), name) && (kind == "" || strings.EqualFold(strings.TrimSpace(h.cfg.MediaProviders[i].Kind), kind)) {
				index = i
				break
			}
		}
	}
	if index < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing index or name"})
		return
	}
	h.cfg.MediaProviders = append(h.cfg.MediaProviders[:index], h.cfg.MediaProviders[index+1:]...)
	h.persistLocked(c)
}

func validateMediaProviders(providers []config.MediaProvider) error {
	seenProviders := make(map[string]int, len(providers))
	for i := range providers {
		provider := providers[i]
		kind := strings.ToLower(strings.TrimSpace(provider.Kind))
		if kind != config.MediaKindImage && kind != config.MediaKindVideo && kind != config.MediaKindAudio {
			return fmt.Errorf("media-providers[%d].kind is invalid", i)
		}
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			return fmt.Errorf("media-providers[%d].name is required", i)
		}
		if strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf("media-providers[%d].base-url is required", i)
		}
		providerIdentity := kind + "|" + strings.ToLower(name)
		if previous, exists := seenProviders[providerIdentity]; exists {
			return fmt.Errorf("media-providers[%d] duplicates provider at index %d", i, previous)
		}
		seenProviders[providerIdentity] = i

		seenKeys := make(map[string]int, len(provider.APIKeyEntries))
		for j := range provider.APIKeyEntries {
			entry := provider.APIKeyEntries[j]
			key := strings.TrimSpace(entry.APIKey)
			if key == "" {
				return fmt.Errorf("media-providers[%d].api-key-entries[%d].api-key is required", i, j)
			}
			if previous, exists := seenKeys[key]; exists {
				return fmt.Errorf("media-providers[%d].api-key-entries[%d].api-key duplicates entry %d", i, j, previous)
			}
			seenKeys[key] = j
		}

		seenModels := make(map[string]int, len(provider.Models))
		seenModelAliases := make(map[string]int, len(provider.Models))
		for j := range provider.Models {
			model := provider.Models[j]
			modelName := strings.TrimSpace(model.Name)
			if modelName == "" {
				return fmt.Errorf("media-providers[%d].models[%d].name is required", i, j)
			}
			modelKey := strings.ToLower(modelName)
			if previous, exists := seenModels[modelKey]; exists {
				return fmt.Errorf("media-providers[%d].models[%d].name duplicates model %d", i, j, previous)
			}
			seenModels[modelKey] = j
			if alias := strings.TrimSpace(model.Alias); alias != "" {
				aliasKey := strings.ToLower(alias)
				if previous, exists := seenModelAliases[aliasKey]; exists {
					return fmt.Errorf("media-providers[%d].models[%d].alias duplicates model %d", i, j, previous)
				}
				if previous, exists := seenModels[aliasKey]; exists && previous != j {
					return fmt.Errorf("media-providers[%d].models[%d].alias conflicts with model %d", i, j, previous)
				}
				seenModelAliases[aliasKey] = j
			}
			for k, rawCapability := range model.Capabilities {
				capability := strings.ToLower(strings.TrimSpace(rawCapability))
				if !validMediaCapabilityForKind(kind, capability) {
					return fmt.Errorf("media-providers[%d].models[%d].capabilities[%d] is invalid", i, j, k)
				}
			}
		}

		seenOperations := make(map[string]int, len(provider.Operations))
		for j := range provider.Operations {
			op := provider.Operations[j]
			opName := strings.TrimSpace(op.Name)
			if opName == "" {
				return fmt.Errorf("media-providers[%d].operations[%d].name is required", i, j)
			}
			operationKey := strings.ToLower(opName)
			if previous, exists := seenOperations[operationKey]; exists {
				return fmt.Errorf("media-providers[%d].operations[%d].name is duplicated (entry %d)", i, j, previous)
			}
			seenOperations[operationKey] = j
			method := strings.ToUpper(strings.TrimSpace(op.Method))
			if _, ok := map[string]struct{}{"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}}[method]; !ok {
				return fmt.Errorf("media-providers[%d].operations[%d].method is invalid", i, j)
			}
			if strings.TrimSpace(op.Path) == "" {
				return fmt.Errorf("media-providers[%d].operations[%d].path is required", i, j)
			}
			requestFormat := strings.ToLower(strings.TrimSpace(op.RequestFormat))
			if requestFormat != config.MediaRequestJSON && requestFormat != config.MediaRequestMultipart && requestFormat != config.MediaRequestBinary {
				return fmt.Errorf("media-providers[%d].operations[%d].request-format is invalid", i, j)
			}
			modelMode := strings.ToLower(strings.TrimSpace(op.ModelMode))
			if modelMode != config.MediaModelRequired && modelMode != config.MediaModelOptional && modelMode != config.MediaModelNone {
				return fmt.Errorf("media-providers[%d].operations[%d].model-mode is invalid", i, j)
			}
			if modelMode == config.MediaModelNone && strings.TrimSpace(op.Model) != "" {
				return fmt.Errorf("media-providers[%d].operations[%d].model must be empty when model-mode is none", i, j)
			}
			responseFormat := strings.ToLower(strings.TrimSpace(op.ResponseFormat))
			if responseFormat != config.MediaResponsePassthrough && responseFormat != config.MediaResponseJSONURL && responseFormat != config.MediaResponseJSONBase64 && responseFormat != config.MediaResponseBinary {
				return fmt.Errorf("media-providers[%d].operations[%d].response-format is invalid", i, j)
			}
			if (responseFormat == config.MediaResponseJSONURL || responseFormat == config.MediaResponseJSONBase64) && strings.TrimSpace(op.ResultPath) == "" && (op.Async == nil || strings.TrimSpace(op.Async.ResultPath) == "") {
				return fmt.Errorf("media-providers[%d].operations[%d].result-path is required for JSON response normalization", i, j)
			}
			if capability := strings.ToLower(strings.TrimSpace(op.Capability)); capability != "" && !validMediaCapabilityForKind(kind, capability) {
				return fmt.Errorf("media-providers[%d].operations[%d].capability is invalid", i, j)
			}
			if op.TestRequest != nil {
				testJSON := strings.TrimSpace(op.TestRequest.JSON)
				if testJSON != "" && (requestFormat != config.MediaRequestJSON || !json.Valid([]byte(testJSON))) {
					return fmt.Errorf("media-providers[%d].operations[%d].test-request.json is invalid", i, j)
				}
				if len(op.TestRequest.MultipartFields) > 0 && requestFormat != config.MediaRequestMultipart {
					return fmt.Errorf("media-providers[%d].operations[%d].test-request.multipart-fields requires multipart", i, j)
				}
				if testJSON == "" && len(op.TestRequest.MultipartFields) == 0 {
					return fmt.Errorf("media-providers[%d].operations[%d].test-request is empty", i, j)
				}
				for field := range op.TestRequest.MultipartFields {
					if strings.TrimSpace(field) == "" {
						return fmt.Errorf("media-providers[%d].operations[%d].test-request.multipart-fields contains an empty field", i, j)
					}
				}
			}
			if op.Async != nil {
				async := op.Async
				if strings.TrimSpace(async.TaskIDPath) == "" || strings.TrimSpace(async.PollPath) == "" || strings.TrimSpace(async.StatusPath) == "" || len(nonEmptyMediaValues(async.SuccessValues)) == 0 {
					return fmt.Errorf("media-providers[%d].operations[%d].async is incomplete", i, j)
				}
				pollMethod := strings.ToUpper(strings.TrimSpace(async.PollMethod))
				if pollMethod == "" {
					pollMethod = http.MethodGet
				}
				if _, ok := map[string]struct{}{"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {}}[pollMethod]; !ok {
					return fmt.Errorf("media-providers[%d].operations[%d].async.poll-method is invalid", i, j)
				}
				if interval := strings.TrimSpace(async.PollInterval); interval != "" {
					parsed, errParse := time.ParseDuration(interval)
					if errParse != nil || parsed <= 0 {
						return fmt.Errorf("media-providers[%d].operations[%d].async.poll-interval is invalid", i, j)
					}
				}
			}
		}
	}
	return nil
}

func nonEmptyMediaValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func validMediaCapabilityForKind(kind, capability string) bool {
	capability = strings.ToLower(strings.TrimSpace(capability))
	if capability == "" {
		return true
	}
	switch kind {
	case config.MediaKindImage:
		switch capability {
		case config.MediaCapabilityGenerate, config.MediaCapabilityEdit, config.MediaCapabilityUpscale, config.MediaCapabilitySuperResolution, config.MediaCapabilityRemoveBackground:
			return true
		}
	case config.MediaKindVideo:
		switch capability {
		case config.MediaCapabilityGenerate, config.MediaCapabilityTextToVideo, config.MediaCapabilityImageToVideo, config.MediaCapabilityRemoveWatermark:
			return true
		}
	case config.MediaKindAudio:
		switch capability {
		case config.MediaCapabilityGenerate, config.MediaCapabilitySpeech, config.MediaCapabilityMusic, config.MediaCapabilityClone, config.MediaCapabilityVoiceConvert, config.MediaCapabilityTranscribe:
			return true
		}
	}
	return false
}
