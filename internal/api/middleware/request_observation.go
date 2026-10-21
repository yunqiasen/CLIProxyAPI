package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
)

// RequestObservationMiddleware supplies metadata without reading bodies or wrapping streams.
func RequestObservationMiddleware(store *requestlog.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		modelAPI := false
		for _, prefix := range []string{"/v1/", "/v1beta/", "/openai/v1/", "/backend-api/codex/"} {
			if strings.HasPrefix(path, prefix) {
				modelAPI = true
				break
			}
		}
		if !modelAPI || (c.Request.Method != http.MethodPost && !isResponsesWebsocketUpgrade(c.Request)) {
			c.Next()
			return
		}
		trace := logging.GetGinRequestID(c)
		if trace == "" {
			trace, _ = logging.GenerateRequestID()
			logging.SetGinRequestID(c, trace)
		}
		ctx := requestlog.WithScope(c.Request.Context(), store, requestlog.Scope{TraceID: trace, URL: path, Method: c.Request.Method, IP: c.ClientIP()})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
		// Failed input/auth validation can precede the model execution lifecycle.
		// A WebSocket with no executed calls is an idle transport, not an active call.
		if requestlog.HasCalls(ctx) || c.Request.Method != http.MethodPost {
			return
		}
		status := c.Writer.Status()
		if status < 400 && logging.GetGinRequestModel(c) == "" {
			return
		}
		_, r := requestlog.Start(ctx, trace, logging.GetGinRequestModel(c))
		state := requestlog.Succeeded
		var err error
		if status >= 400 {
			state = requestlog.Failed
		}
		if ctx.Err() != nil {
			state = requestlog.Canceled
			err = context.Canceled
		}
		r.Finish(state, status, err)
	}
}
