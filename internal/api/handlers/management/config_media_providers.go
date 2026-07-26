package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
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
	idGen := synthesizer.NewStableIDGenerator()
	out := make([]mediaProviderWithAuthIndex, 0, len(h.cfg.MediaProviders))
	for i := range h.cfg.MediaProviders {
		provider := h.cfg.MediaProviders[i]
		kind := strings.ToLower(strings.TrimSpace(provider.Kind))
		name := strings.TrimSpace(provider.Name)
		idKind := fmt.Sprintf("media-provider:%s:%s", kind, strings.ToLower(name))
		response := mediaProviderWithAuthIndex{MediaProvider: provider}
		if len(provider.APIKeyEntries) == 0 {
			id, _ := idGen.Next(idKind, provider.BaseURL)
			response.AuthIndex = live[id]
		} else {
			response.APIKeyEntries = make([]mediaAPIKeyEntryWithAuthIndex, 0, len(provider.APIKeyEntries))
			for _, entry := range provider.APIKeyEntries {
				id, _ := idGen.Next(idKind, entry.APIKey, provider.BaseURL, entry.ProxyURL)
				response.APIKeyEntries = append(response.APIKeyEntries, mediaAPIKeyEntryWithAuthIndex{
					MediaAPIKeyEntry: entry,
					AuthIndex:        live[id],
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
	h.cfg.MediaProviders = candidate.MediaProviders
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
	h.cfg.MediaProviders[index] = candidate.MediaProviders[0]
	h.persistLocked(c)
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
	for i := range providers {
		provider := providers[i]
		kind := strings.ToLower(strings.TrimSpace(provider.Kind))
		if kind != config.MediaKindImage && kind != config.MediaKindVideo && kind != config.MediaKindAudio {
			return fmt.Errorf("media-providers[%d].kind is invalid", i)
		}
		if strings.TrimSpace(provider.Name) == "" {
			return fmt.Errorf("media-providers[%d].name is required", i)
		}
		if strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf("media-providers[%d].base-url is required", i)
		}
		seenOps := map[string]struct{}{}
		for j := range provider.Operations {
			op := provider.Operations[j]
			key := strings.ToLower(strings.TrimSpace(op.Name))
			if key == "" {
				return fmt.Errorf("media-providers[%d].operations[%d].name is required", i, j)
			}
			if _, ok := seenOps[key]; ok {
				return fmt.Errorf("media-providers[%d].operations[%d].name is duplicated", i, j)
			}
			seenOps[key] = struct{}{}
			requestFormat := strings.ToLower(strings.TrimSpace(op.RequestFormat))
			if requestFormat != config.MediaRequestJSON && requestFormat != config.MediaRequestMultipart && requestFormat != config.MediaRequestBinary {
				return fmt.Errorf("media-providers[%d].operations[%d].request-format is invalid", i, j)
			}
			modelMode := strings.ToLower(strings.TrimSpace(op.ModelMode))
			if modelMode != config.MediaModelRequired && modelMode != config.MediaModelOptional && modelMode != config.MediaModelNone {
				return fmt.Errorf("media-providers[%d].operations[%d].model-mode is invalid", i, j)
			}
			responseFormat := strings.ToLower(strings.TrimSpace(op.ResponseFormat))
			if responseFormat != config.MediaResponsePassthrough && responseFormat != config.MediaResponseJSONURL && responseFormat != config.MediaResponseJSONBase64 && responseFormat != config.MediaResponseBinary {
				return fmt.Errorf("media-providers[%d].operations[%d].response-format is invalid", i, j)
			}
		}
	}
	return nil
}
