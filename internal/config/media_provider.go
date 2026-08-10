package config

import (
	"encoding/json"
	"strings"
)

const (
	MediaKindImage = "image"
	MediaKindVideo = "video"
	MediaKindAudio = "audio"

	MediaCapabilityGenerate         = "generate"
	MediaCapabilityEdit             = "edit"
	MediaCapabilityUpscale          = "upscale"
	MediaCapabilitySuperResolution  = "super-resolution"
	MediaCapabilityRemoveBackground = "remove-background"
	MediaCapabilityTextToVideo      = "text-to-video"
	MediaCapabilityImageToVideo     = "image-to-video"
	MediaCapabilityRemoveWatermark  = "remove-watermark"
	MediaCapabilitySpeech           = "speech"
	MediaCapabilityMusic            = "music"
	MediaCapabilityClone            = "clone"
	MediaCapabilityVoiceConvert     = "voice-convert"
	MediaCapabilityTranscribe       = "transcribe"

	MediaRequestJSON      = "json"
	MediaRequestMultipart = "multipart"
	MediaRequestBinary    = "binary"

	MediaModelRequired = "required"
	MediaModelOptional = "optional"
	MediaModelNone     = "none"

	MediaResponsePassthrough = "passthrough"
	MediaResponseJSONURL     = "json-url"
	MediaResponseJSONBase64  = "json-base64"
	MediaResponseBinary      = "binary"
)

// MediaProvider configures an image, video, or audio upstream.
type MediaProvider struct {
	// AuthID is an internal stable runtime identity retained across management edits.
	AuthID         string `yaml:"auth-id,omitempty" json:"auth-index,omitempty"`
	Name           string `yaml:"name" json:"name"`
	Kind           string `yaml:"kind" json:"kind"`
	BaseURL        string `yaml:"base-url" json:"base-url"`
	Priority       int    `yaml:"priority,omitempty" json:"priority,omitempty"`
	Disabled       bool   `yaml:"disabled,omitempty" json:"disabled,omitempty"`
	DisableCooling bool   `yaml:"disable-cooling,omitempty" json:"disable-cooling,omitempty"`
	Prefix         string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	// APIKeyHeader overrides the header used to send the credential. Empty means Authorization.
	APIKeyHeader string `yaml:"api-key-header,omitempty" json:"api-key-header,omitempty"`
	// APIKeyPrefix overrides the credential value prefix. Use "-" for a bare value; empty means "Bearer ".
	APIKeyPrefix  string             `yaml:"api-key-prefix,omitempty" json:"api-key-prefix,omitempty"`
	APIKeyEntries []MediaAPIKeyEntry `yaml:"api-key-entries,omitempty" json:"api-key-entries,omitempty"`
	Headers       map[string]string  `yaml:"headers,omitempty" json:"headers,omitempty"`
	Models        []MediaModel       `yaml:"models,omitempty" json:"models,omitempty"`
	Operations    []MediaOperation   `yaml:"operations,omitempty" json:"operations,omitempty"`
}

// MediaAPIKeyEntry configures one provider credential.
type MediaAPIKeyEntry struct {
	// AuthID is an internal stable runtime identity retained across key edits.
	AuthID   string `yaml:"auth-id,omitempty" json:"auth-index,omitempty"`
	APIKey   string `yaml:"api-key" json:"api-key"`
	Priority *int   `yaml:"priority,omitempty" json:"priority,omitempty"`
	ProxyURL string `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
}

// MediaModel maps a public alias to an upstream media model.
type MediaModel struct {
	Name         string   `yaml:"name" json:"name"`
	Alias        string   `yaml:"alias,omitempty" json:"alias,omitempty"`
	DisplayName  string   `yaml:"display-name,omitempty" json:"display-name,omitempty"`
	ForceMapping bool     `yaml:"force-mapping,omitempty" json:"force-mapping,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

func (m MediaModel) GetName() string        { return m.Name }
func (m MediaModel) GetAlias() string       { return m.Alias }
func (m MediaModel) GetDisplayName() string { return m.DisplayName }
func (m MediaModel) GetForceMapping() bool  { return m.ForceMapping }

// MediaOperation maps a public operation name to an upstream HTTP endpoint.
type MediaOperation struct {
	Name           string               `yaml:"name" json:"name"`
	Capability     string               `yaml:"capability,omitempty" json:"capability,omitempty"`
	Method         string               `yaml:"method" json:"method"`
	Path           string               `yaml:"path" json:"path"`
	RequestFormat  string               `yaml:"request-format" json:"request-format"`
	ModelMode      string               `yaml:"model-mode" json:"model-mode"`
	Model          string               `yaml:"model,omitempty" json:"model,omitempty"`
	ResponseFormat string               `yaml:"response-format" json:"response-format"`
	ResultPath     string               `yaml:"result-path,omitempty" json:"result-path,omitempty"`
	TestRequest    *MediaTestRequest    `yaml:"test-request,omitempty" json:"test-request,omitempty"`
	Async          *MediaAsyncOperation `yaml:"async,omitempty" json:"async,omitempty"`
}

// MediaTestRequest overrides the synthetic connectivity payload for providers
// whose test contract differs from the normal operation payload.
type MediaTestRequest struct {
	JSON            string            `yaml:"json,omitempty" json:"json,omitempty"`
	MultipartFields map[string]string `yaml:"multipart-fields,omitempty" json:"multipart-fields,omitempty"`
}

// MediaAsyncOperation configures submit-and-poll operation behavior.
type MediaAsyncOperation struct {
	TaskIDPath    string   `yaml:"task-id-path" json:"task-id-path"`
	PollMethod    string   `yaml:"poll-method,omitempty" json:"poll-method,omitempty"`
	PollPath      string   `yaml:"poll-path" json:"poll-path"`
	StatusPath    string   `yaml:"status-path" json:"status-path"`
	SuccessValues []string `yaml:"success-values" json:"success-values"`
	FailureValues []string `yaml:"failure-values,omitempty" json:"failure-values,omitempty"`
	ResultPath    string   `yaml:"result-path,omitempty" json:"result-path,omitempty"`
	PollInterval  string   `yaml:"poll-interval,omitempty" json:"poll-interval,omitempty"`
}

var mediaKinds = map[string]struct{}{
	MediaKindImage: {}, MediaKindVideo: {}, MediaKindAudio: {},
}

var mediaCapabilities = map[string]struct{}{
	MediaCapabilityGenerate: {}, MediaCapabilityEdit: {}, MediaCapabilityUpscale: {},
	MediaCapabilitySuperResolution: {}, MediaCapabilityRemoveBackground: {},
	MediaCapabilityTextToVideo: {}, MediaCapabilityImageToVideo: {}, MediaCapabilityRemoveWatermark: {},
	MediaCapabilitySpeech: {}, MediaCapabilityMusic: {}, MediaCapabilityClone: {}, MediaCapabilityVoiceConvert: {},
	MediaCapabilityTranscribe: {},
}

var mediaRequestFormats = map[string]struct{}{
	MediaRequestJSON: {}, MediaRequestMultipart: {}, MediaRequestBinary: {},
}

var mediaModelModes = map[string]struct{}{
	MediaModelRequired: {}, MediaModelOptional: {}, MediaModelNone: {},
}

var mediaResponseFormats = map[string]struct{}{
	MediaResponsePassthrough: {}, MediaResponseJSONURL: {}, MediaResponseJSONBase64: {}, MediaResponseBinary: {},
}

var mediaMethods = map[string]struct{}{
	"GET": {}, "POST": {}, "PUT": {}, "PATCH": {}, "DELETE": {},
}

func normalizeMediaPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return value
}

func normalizeMediaValues(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

// SanitizeMediaProviders normalizes media provider config and drops malformed entries.
func (cfg *Config) SanitizeMediaProviders() {
	if cfg == nil || len(cfg.MediaProviders) == 0 {
		return
	}
	providers := make([]MediaProvider, 0, len(cfg.MediaProviders))
	for i := range cfg.MediaProviders {
		provider := cfg.MediaProviders[i]
		provider.AuthID = strings.TrimSpace(provider.AuthID)
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Kind = strings.ToLower(strings.TrimSpace(provider.Kind))
		provider.BaseURL = strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/")
		provider.Prefix = normalizeModelPrefix(provider.Prefix)
		provider.APIKeyHeader = strings.TrimSpace(provider.APIKeyHeader)
		provider.APIKeyPrefix = strings.TrimSpace(provider.APIKeyPrefix)
		provider.Headers = NormalizeHeaders(provider.Headers)
		if provider.Name == "" || provider.BaseURL == "" {
			continue
		}
		if _, ok := mediaKinds[provider.Kind]; !ok {
			continue
		}

		keys := make([]MediaAPIKeyEntry, 0, len(provider.APIKeyEntries))
		seenKeys := make(map[string]struct{}, len(provider.APIKeyEntries))
		for j := range provider.APIKeyEntries {
			entry := provider.APIKeyEntries[j]
			entry.AuthID = strings.TrimSpace(entry.AuthID)
			entry.APIKey = strings.TrimSpace(entry.APIKey)
			entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
			if entry.APIKey == "" {
				continue
			}
			if _, exists := seenKeys[entry.APIKey]; exists {
				continue
			}
			seenKeys[entry.APIKey] = struct{}{}
			keys = append(keys, entry)
		}
		provider.APIKeyEntries = keys

		models := make([]MediaModel, 0, len(provider.Models))
		for j := range provider.Models {
			model := provider.Models[j]
			model.Name = strings.TrimSpace(model.Name)
			model.Alias = strings.TrimSpace(model.Alias)
			model.DisplayName = strings.TrimSpace(model.DisplayName)
			if model.Name == "" {
				continue
			}
			caps := make([]string, 0, len(model.Capabilities))
			seenCaps := make(map[string]struct{}, len(model.Capabilities))
			for _, raw := range model.Capabilities {
				capability := strings.ToLower(strings.TrimSpace(raw))
				if _, ok := mediaCapabilities[capability]; !ok {
					continue
				}
				if _, exists := seenCaps[capability]; exists {
					continue
				}
				seenCaps[capability] = struct{}{}
				caps = append(caps, capability)
			}
			model.Capabilities = caps
			models = append(models, model)
		}
		provider.Models = models

		operations := make([]MediaOperation, 0, len(provider.Operations))
		seenOperations := make(map[string]struct{}, len(provider.Operations))
		for j := range provider.Operations {
			operation := provider.Operations[j]
			operation.Name = strings.TrimSpace(operation.Name)
			operation.Capability = strings.ToLower(strings.TrimSpace(operation.Capability))
			operation.Method = strings.ToUpper(strings.TrimSpace(operation.Method))
			operation.Path = normalizeMediaPath(operation.Path)
			operation.RequestFormat = strings.ToLower(strings.TrimSpace(operation.RequestFormat))
			operation.ModelMode = strings.ToLower(strings.TrimSpace(operation.ModelMode))
			operation.Model = strings.TrimSpace(operation.Model)
			operation.ResponseFormat = strings.ToLower(strings.TrimSpace(operation.ResponseFormat))
			operation.ResultPath = strings.TrimSpace(operation.ResultPath)
			if operation.TestRequest != nil {
				testRequest := *operation.TestRequest
				testRequest.JSON = strings.TrimSpace(testRequest.JSON)
				if operation.RequestFormat != MediaRequestJSON || (testRequest.JSON != "" && !json.Valid([]byte(testRequest.JSON))) {
					testRequest.JSON = ""
				}
				if operation.RequestFormat == MediaRequestMultipart && len(testRequest.MultipartFields) > 0 {
					fields := make(map[string]string, len(testRequest.MultipartFields))
					for key, value := range testRequest.MultipartFields {
						key = strings.TrimSpace(key)
						if key == "" {
							continue
						}
						fields[key] = value
					}
					testRequest.MultipartFields = fields
				} else {
					testRequest.MultipartFields = nil
				}
				if testRequest.JSON == "" && len(testRequest.MultipartFields) == 0 {
					operation.TestRequest = nil
				} else {
					operation.TestRequest = &testRequest
				}
			}
			key := strings.ToLower(operation.Name)
			if operation.Name == "" || operation.Path == "" {
				continue
			}
			if _, exists := seenOperations[key]; exists {
				continue
			}
			if _, ok := mediaMethods[operation.Method]; !ok {
				continue
			}
			if _, ok := mediaRequestFormats[operation.RequestFormat]; !ok {
				continue
			}
			if _, ok := mediaModelModes[operation.ModelMode]; !ok {
				continue
			}
			if _, ok := mediaResponseFormats[operation.ResponseFormat]; !ok {
				continue
			}
			if operation.Capability != "" {
				if _, ok := mediaCapabilities[operation.Capability]; !ok {
					continue
				}
			}
			if operation.Async != nil {
				async := *operation.Async
				async.TaskIDPath = strings.TrimSpace(async.TaskIDPath)
				async.PollMethod = strings.ToUpper(strings.TrimSpace(async.PollMethod))
				if async.PollMethod == "" {
					async.PollMethod = "GET"
				}
				async.PollPath = normalizeMediaPath(async.PollPath)
				async.StatusPath = strings.TrimSpace(async.StatusPath)
				async.SuccessValues = normalizeMediaValues(async.SuccessValues)
				async.FailureValues = normalizeMediaValues(async.FailureValues)
				async.ResultPath = strings.TrimSpace(async.ResultPath)
				async.PollInterval = strings.TrimSpace(async.PollInterval)
				if async.TaskIDPath == "" || async.PollPath == "" || async.StatusPath == "" || len(async.SuccessValues) == 0 {
					operation.Async = nil
				} else if _, ok := mediaMethods[async.PollMethod]; !ok {
					operation.Async = nil
				} else {
					operation.Async = &async
				}
			}
			seenOperations[key] = struct{}{}
			operations = append(operations, operation)
		}
		provider.Operations = operations
		providers = append(providers, provider)
	}
	cfg.MediaProviders = providers
}

// FindMediaProvider resolves one enabled media provider by kind and name.
func (cfg *Config) FindMediaProvider(kind, name string) *MediaProvider {
	if cfg == nil {
		return nil
	}
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	for i := range cfg.MediaProviders {
		provider := &cfg.MediaProviders[i]
		if provider.Disabled {
			continue
		}
		if strings.EqualFold(provider.Kind, kind) && strings.EqualFold(provider.Name, name) {
			return provider
		}
	}
	return nil
}

// HasMediaCapability reports whether a model can serve a capability.
func HasMediaCapability(model MediaModel, capability string) bool {
	want := normalizeMediaCapability(capability)
	if want == "" {
		return false
	}
	for _, item := range model.Capabilities {
		if normalizeMediaCapability(item) == want {
			return true
		}
	}
	return false
}

// MediaOperationCapability returns the semantic capability represented by an
// operation. A standard capability may be declared either explicitly or by
// using the capability name as the operation name.
func MediaOperationCapability(operation MediaOperation) string {
	if capability := normalizeMediaCapability(operation.Capability); capability != "" {
		return capability
	}
	name := normalizeMediaCapability(operation.Name)
	if _, ok := mediaCapabilities[name]; ok {
		return name
	}
	return ""
}

func normalizeMediaCapability(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.ReplaceAll(value, "_", "-")
}
