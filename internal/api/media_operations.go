package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkhandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const mediaOperationBodyLimit = 128 << 20

// handleMediaOperation dispatches a configured media operation to the matching provider pool.
// The route deliberately keeps model selection separate from credential selection: operations
// such as background removal can be valid without a model, while a configured operation may
// still receive a model alias in MediaModelMetadataKey for the executor to rewrite.
func (s *Server) handleMediaOperation(c *gin.Context) {
	if c == nil {
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.Param("kind")))
	operationName := strings.TrimSpace(c.Param("operation"))
	s.handleMediaOperationFor(c, kind, operationName)
}

func (s *Server) handleMediaOperationFor(c *gin.Context, kind, operationName string) {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		s.writeMediaOperationError(c, http.StatusServiceUnavailable, fmt.Errorf("media auth manager unavailable"))
		return
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	operationName = strings.TrimSpace(operationName)
	if !validMediaKind(kind) || operationName == "" {
		s.writeMediaOperationError(c, http.StatusBadRequest, fmt.Errorf("invalid media operation"))
		return
	}

	body, errRead := readMediaOperationBody(c.Request)
	if errRead != nil {
		s.writeMediaOperationError(c, http.StatusBadRequest, errRead)
		return
	}
	contentType := ""
	if c.Request != nil {
		contentType = c.GetHeader("Content-Type")
	}
	requestedModel := mediaRequestedModel(body, contentType, queryValues(c.Request))
	providers := s.mediaOperationProviders(kind, operationName, requestedModel)
	if len(providers) == 0 {
		if s.mediaOperationExists(kind, operationName) && requestedModel == "" && s.mediaOperationRequiresModel(kind, operationName) {
			s.writeMediaOperationError(c, http.StatusBadRequest, fmt.Errorf("model is required for media operation %s", operationName))
			return
		}
		s.writeMediaOperationError(c, http.StatusNotFound, fmt.Errorf("media operation %s is not configured", operationName))
		return
	}

	requestPath := ""
	query := queryValues(c.Request)
	if c.Request != nil && c.Request.URL != nil {
		requestPath = c.Request.URL.Path
	}
	metadata := map[string]any{
		coreexecutor.MediaKindMetadataKey:      kind,
		coreexecutor.MediaOperationMetadataKey: operationName,
		coreexecutor.MediaModelMetadataKey:     requestedModel,
		coreexecutor.RequestPathMetadataKey:    requestPath,
		coreexecutor.RequestedModelMetadataKey: requestedModel,
	}
	if requestedModel != "" {
		logging.SetGinRequestModel(c, requestedModel)
	}
	ctx := c.Request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, "gin", c)
	req := coreexecutor.Request{Payload: body}
	if c.Request != nil {
		req.Payload = body
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Headers:         c.Request.Header.Clone(),
		OriginalRequest: body,
		SourceFormat:    sdktranslator.FromString("media"),
		ResponseFormat:  sdktranslator.FromString("media"),
		Query:           query,
		Metadata:        metadata,
	}
	resp, errExecute := s.handlers.AuthManager.Execute(ctx, providers, req, opts)
	if errExecute != nil {
		s.writeMediaOperationError(c, mediaErrorStatus(errExecute), errExecute)
		return
	}

	sdkhandlers.WriteUpstreamHeaders(c.Writer.Header(), resp.Headers)
	if c.Writer.Header().Get("Content-Type") == "" {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/") {
			c.Header("Content-Type", "application/json")
		} else {
			c.Header("Content-Type", "application/octet-stream")
		}
	}
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(resp.Payload)
}

func readMediaOperationBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	body, errRead := io.ReadAll(io.LimitReader(req.Body, mediaOperationBodyLimit+1))
	if errRead != nil {
		return nil, fmt.Errorf("failed to read media request: %w", errRead)
	}
	if len(body) > mediaOperationBodyLimit {
		return nil, fmt.Errorf("media request body exceeds %d bytes", mediaOperationBodyLimit)
	}
	return body, nil
}

func validMediaKind(kind string) bool {
	switch kind {
	case config.MediaKindImage, config.MediaKindVideo, config.MediaKindAudio:
		return true
	default:
		return false
	}
}

func (s *Server) mediaOperationProviders(kind, operationName, requestedModel string) []string {
	if s == nil || s.cfg == nil {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	declared := make([]string, 0)
	generic := make([]string, 0)
	seen := make(map[string]struct{})
	for i := range s.cfg.MediaProviders {
		provider := &s.cfg.MediaProviders[i]
		if provider.Disabled || !strings.EqualFold(strings.TrimSpace(provider.Kind), kind) {
			continue
		}
		operation, ok := findMediaOperation(provider, operationName)
		if !ok || !mediaOperationAcceptsModel(provider, operation, requestedModel) {
			continue
		}
		key := mediaProviderKey(provider)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if requestedModel != "" && mediaProviderDeclaresModel(provider, requestedModel) {
			declared = append(declared, key)
			continue
		}
		generic = append(generic, key)
	}
	// A provider that declares the requested model wins over providers that only
	// accept it because they declare no models at all.
	if len(declared) > 0 {
		return declared
	}
	return generic
}

func (s *Server) mediaOperationExists(kind, operationName string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	for i := range s.cfg.MediaProviders {
		provider := &s.cfg.MediaProviders[i]
		if provider.Disabled || !strings.EqualFold(strings.TrimSpace(provider.Kind), kind) {
			continue
		}
		if _, ok := findMediaOperation(provider, operationName); ok {
			return true
		}
	}
	return false
}

func (s *Server) mediaOperationRequiresModel(kind, operationName string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	for i := range s.cfg.MediaProviders {
		provider := &s.cfg.MediaProviders[i]
		if provider.Disabled || !strings.EqualFold(strings.TrimSpace(provider.Kind), kind) {
			continue
		}
		if operation, ok := findMediaOperation(provider, operationName); ok {
			return operation.ModelMode == config.MediaModelRequired && strings.TrimSpace(operation.Model) == ""
		}
	}
	return false
}

func mediaProviderKey(provider *config.MediaProvider) string {
	if provider == nil {
		return ""
	}
	return util.MediaProviderKey(provider.Kind, provider.Name)
}

func findMediaOperation(provider *config.MediaProvider, requested string) (config.MediaOperation, bool) {
	if provider == nil {
		return config.MediaOperation{}, false
	}
	requested = normalizeMediaOperationName(requested)
	for i := range provider.Operations {
		operation := provider.Operations[i]
		if normalizeMediaOperationName(operation.Name) == requested {
			return operation, true
		}
	}
	for i := range provider.Operations {
		operation := provider.Operations[i]
		if normalizeMediaOperationName(operation.Capability) == requested {
			return operation, true
		}
	}
	return config.MediaOperation{}, false
}

func normalizeMediaOperationName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func mediaOperationAcceptsModel(provider *config.MediaProvider, operation config.MediaOperation, requested string) bool {
	requested = strings.TrimSpace(requested)
	if operation.ModelMode == config.MediaModelNone {
		// A model-free operation still must not capture a request that explicitly
		// names a model served by another provider, so only accept the request when
		// the provider declares no models or declares the requested one.
		if requested == "" || len(provider.Models) == 0 {
			return true
		}
		return mediaProviderDeclaresModel(provider, requested)
	}
	if requested == "" {
		return operation.ModelMode != config.MediaModelRequired || strings.TrimSpace(operation.Model) != ""
	}
	if strings.TrimSpace(operation.Model) != "" || len(provider.Models) == 0 {
		return true
	}
	capability := config.MediaOperationCapability(operation)
	for i := range provider.Models {
		model := provider.Models[i]
		if !strings.EqualFold(strings.TrimSpace(model.Name), requested) && !strings.EqualFold(strings.TrimSpace(model.Alias), requested) {
			continue
		}
		if capability == "" || len(model.Capabilities) == 0 || config.HasMediaCapability(model, capability) {
			return true
		}
	}
	return false
}

func mediaProviderDeclaresModel(provider *config.MediaProvider, requested string) bool {
	for i := range provider.Models {
		model := provider.Models[i]
		if strings.EqualFold(strings.TrimSpace(model.Name), requested) || strings.EqualFold(strings.TrimSpace(model.Alias), requested) {
			return true
		}
	}
	return false
}

func mediaRequestedModel(body []byte, contentType string, query url.Values) string {
	mediaType, params, errParse := mime.ParseMediaType(strings.TrimSpace(contentType))
	if errParse == nil && strings.HasPrefix(strings.ToLower(mediaType), "multipart/") && strings.TrimSpace(params["boundary"]) != "" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, errNext := reader.NextPart()
			if errNext == io.EOF {
				break
			}
			if errNext != nil {
				return strings.TrimSpace(query.Get("model"))
			}
			if part.FormName() == "model" {
				value, _ := io.ReadAll(io.LimitReader(part, 1<<20))
				if model := strings.TrimSpace(string(value)); model != "" {
					return model
				}
			}
		}
		return strings.TrimSpace(query.Get("model"))
	}
	if json.Valid(body) {
		if model := strings.TrimSpace(gjson.GetBytes(body, "model").String()); model != "" {
			return model
		}
	}
	return strings.TrimSpace(query.Get("model"))
}

func queryValues(req *http.Request) url.Values {
	if req == nil || req.URL == nil {
		return url.Values{}
	}
	return req.URL.Query()
}

func mediaErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	if status, ok := err.(interface{ StatusCode() int }); ok && status != nil {
		if code := status.StatusCode(); code > 0 {
			return code
		}
	}
	return http.StatusBadGateway
}

func (s *Server) writeMediaOperationError(c *gin.Context, status int, err error) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if err == nil {
		err = fmt.Errorf("media operation failed")
	}
	if s != nil && s.handlers != nil {
		s.handlers.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: status, Error: err})
		return
	}
	c.JSON(status, sdkhandlers.ErrorResponse{Error: sdkhandlers.ErrorDetail{Message: err.Error(), Type: "server_error"}})
}

// mediaOperationAlias adapts a concise image route to the generic operation handler.
func (s *Server) mediaOperationAlias(kind, operation string) gin.HandlerFunc {
	return func(c *gin.Context) {
		s.handleMediaOperationFor(c, kind, operation)
	}
}
