package openai

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

// Embeddings forwards vector requests through the same auth and logging pipeline as chat.
func (h *OpenAIAPIHandler) Embeddings(c *gin.Context) { h.handleRetrieval(c, "embeddings") }

// Rerank forwards Cohere-compatible document ranking requests.
func (h *OpenAIAPIHandler) Rerank(c *gin.Context) { h.handleRetrieval(c, "rerank") }

func (h *OpenAIAPIHandler) handleRetrieval(c *gin.Context, kind string) {
	body, err := handlers.ReadRequestBody(c)
	if err == nil {
		err = helps.ValidateRetrievalRequest(body, kind)
	}
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: err})
		return
	}
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	defer cancel()
	result, headers, errMsg := h.ExecuteWithAuthManager(ctx, helps.RetrievalFormat(kind), gjson.GetBytes(body, "model").String(), body, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	c.Data(http.StatusOK, "application/json", result)
}
