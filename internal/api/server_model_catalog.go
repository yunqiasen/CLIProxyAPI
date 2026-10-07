package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelcatalog"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
)

func (s *Server) modelCatalogInventory(c *gin.Context) { s.modelCatalogView(c, false) }
func (s *Server) modelCatalogPreview(c *gin.Context)   { s.modelCatalogView(c, true) }

func (s *Server) modelCatalogView(c *gin.Context, preview bool) {
	c.Header("Cache-Control", "no-store")
	var input struct {
		Format string                     `json:"format"`
		Policy *config.ModelCatalogPolicy `json:"policy"`
	}
	input.Format = c.Query("format")
	if preview {
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&input)
		if err == nil {
			var extra any
			if trailingErr := decoder.Decode(&extra); trailingErr != io.EOF {
				err = trailingErr
				if err == nil {
					err = errors.New("unexpected trailing JSON value")
				}
			}
		}
		if err != nil || input.Policy == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_preview", "message": "expected format and policy object"})
			return
		}
	}
	format := strings.ToLower(strings.TrimSpace(input.Format))
	if format == "" {
		format = "openai"
	}
	switch format {
	case "openai", "claude", "gemini", "codex", "grok":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_format"})
		return
	}
	cfg := s.mgmt.CatalogConfigSnapshot()
	if cfg == nil || s.handlers == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog_unavailable"})
		return
	}
	policy := s.handlers.CatalogDisplayPolicy()
	if preview {
		var err error
		policy, err = input.Policy.Normalized()
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_policy", "message": err.Error()})
			return
		}
	}
	// Construct only catalog handlers, using an independent config snapshot.
	base := handlers.NewBaseAPIHandlers(effectiveSDKConfig(cfg), s.handlers.AuthManager)
	catalogServer := &Server{cfg: cfg, handlers: base}
	local := c.Copy()
	local.Request = c.Request.Clone(c.Request.Context())
	local.Request.Method = http.MethodGet
	local.Request.Header = make(http.Header)
	local.Request.URL = &url.URL{Path: "/v1/models"}
	if len(cfg.APIKeys) > 0 && strings.TrimSpace(cfg.APIKeys[0]) != "" {
		local.Request.Header.Set("Authorization", "Bearer "+cfg.APIKeys[0])
	} else if cfg.Home.Enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog_client_key_required"})
		return
	}
	var build gin.HandlerFunc
	switch format {
	case "gemini":
		local.Request.URL.Path = "/v1beta/models"
		build = catalogServer.geminiModelsHandler(gemini.NewGeminiAPIHandler(base))
	case "grok":
		build = catalogServer.handleGrokModels
	default:
		if format == "claude" {
			local.Request.Header.Set("Anthropic-Version", "2023-06-01")
		}
		if format == "codex" {
			local.Request.URL.RawQuery = "client_version=cpa"
		}
		build = catalogServer.unifiedModelsHandler(openai.NewOpenAIAPIHandler(base), claude.NewClaudeCodeAPIHandler(base))
	}
	body, status := handlers.CaptureModelCatalog(local, build)
	if status != http.StatusOK {
		c.Data(status, "application/json", body)
		return
	}
	result, err := modelcatalog.Inspect(body, policy, format)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid_catalog"})
		return
	}
	pluginEnabled := cfg.Plugins.Configs["model-sort"].Enabled
	result.ModelSortEnabled = cfg.Plugins.Enabled && pluginEnabled != nil && *pluginEnabled
	c.JSON(http.StatusOK, result)
}
