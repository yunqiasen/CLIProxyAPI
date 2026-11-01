package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	apiHandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// AuthFileModelExecutor is the production model execution seam used by the
// auth-file model probe. It is satisfied by *handlers.BaseAPIHandler.
type AuthFileModelExecutor interface {
	ExecuteModel(context.Context, apiHandlers.ModelExecutionRequest) (apiHandlers.ModelExecutionResponse, *interfaces.ErrorMessage)
}

// SetModelExecutor wires the production executor used by the credential probe.
func (h *Handler) SetModelExecutor(executor AuthFileModelExecutor) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.modelExecutor = executor
	h.mu.Unlock()
}

func (h *Handler) currentModelExecutor() AuthFileModelExecutor {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	executor := h.modelExecutor
	h.mu.Unlock()
	return executor
}

// authFileModelProbeRequest asks for a single-model smoke test pinned to one credential.
type authFileModelProbeRequest struct {
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
}

const authFileModelProbeDefaultPrompt = "Reply with the single word: ok"

// authFileModelProbeMaxTokens leaves headroom for reasoning models, which can
// spend a small budget entirely on hidden reasoning and return empty content
// with finish_reason=length.
const authFileModelProbeMaxTokens = 512

// ProbeAuthFileModel runs one non-streaming chat request through the production
// executor while pinning the credential card the operator clicked.
func (h *Handler) ProbeAuthFileModel(c *gin.Context) {
	var body authFileModelProbeRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	modelName := strings.TrimSpace(body.Model)
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	auth, errAuth := h.resolveProbeAuth(body.Name, body.AuthIndex)
	if errAuth != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errAuth.Error()})
		return
	}

	executor := h.currentModelExecutor()
	if executor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "model executor unavailable"})
		return
	}

	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		prompt = authFileModelProbeDefaultPrompt
	}
	payload, errMarshal := json.Marshal(map[string]any{
		"model":      modelName,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
		"stream":     false,
		"max_tokens": authFileModelProbeMaxTokens,
	})
	if errMarshal != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build probe payload"})
		return
	}

	auth.EnsureIndex()
	startedAt := time.Now()
	response, errMsg := executor.ExecuteModel(c.Request.Context(), apiHandlers.ModelExecutionRequest{
		EntryProtocol: string(sdktranslator.FormatOpenAI),
		ExitProtocol:  string(sdktranslator.FormatOpenAI),
		Model:         modelName,
		Body:          payload,
		AuthID:        auth.ID,
		Path:          "/v1/chat/completions",
	})
	elapsedMs := time.Since(startedAt).Milliseconds()
	if errMsg != nil {
		status := errMsg.StatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		message := ""
		if errMsg.Error != nil {
			message = errMsg.Error.Error()
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":         false,
			"status":     status,
			"error":      message,
			"model":      modelName,
			"auth_id":    auth.ID,
			"auth_index": auth.Index,
			"latency_ms": elapsedMs,
			"body":       string(errMsg.Body),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"status":     response.StatusCode,
		"model":      modelName,
		"auth_id":    auth.ID,
		"auth_index": auth.Index,
		"latency_ms": elapsedMs,
		"text":       probeResponseText(response.Body),
		"usage":      probeResponseUsage(response.Body),
		"finish":     probeResponseFinishReason(response.Body),
		"body":       string(response.Body),
	})
}

func (h *Handler) resolveProbeAuth(name, authIndex string) (*coreauth.Auth, error) {
	name = strings.TrimSpace(name)
	authIndex = strings.TrimSpace(authIndex)
	if h == nil || h.authManager == nil {
		return nil, fmt.Errorf("auth manager unavailable")
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if authIndex != "" && auth.Index == authIndex {
			return auth, nil
		}
		if authIndex == "" && name != "" && (auth.FileName == name || auth.ID == name) {
			return auth, nil
		}
	}
	return nil, fmt.Errorf("auth file not found")
}

func probeResponseText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return ""
	}
	if len(parsed.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content)
}

type probeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func probeResponseUsage(body []byte) *probeUsage {
	if len(body) == 0 {
		return nil
	}
	var parsed struct {
		Usage *probeUsage `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return nil
	}
	return parsed.Usage
}

func probeResponseFinishReason(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(body, &parsed); errUnmarshal != nil {
		return ""
	}
	if len(parsed.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(parsed.Choices[0].FinishReason)
}
