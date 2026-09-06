package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestForwardResponsesWebsocketPrefersPendingErrorOverClosedData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		// Both channels are ready at EOF. Repetition exercises both select paths
		// without involving sockets, registry state, or background scheduling.
		for iteration := 0; iteration < 100; iteration++ {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses/ws", nil)
			data := make(chan []byte)
			close(data)
			want := &interfaces.ErrorMessage{StatusCode: status, Error: errors.New("fixture credential failure")}
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- want
			close(errs)
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
			var cancelValue interface{}
			var cancelCalls, replayCalls int
			_, _, _, got, errForward := h.forwardResponsesWebsocket(
				ctx, nil,
				func(values ...interface{}) {
					cancelCalls++
					if len(values) > 0 {
						cancelValue = values[0]
					}
				},
				data, errs, nil, "fixture-session",
				responsesWebsocketForwardOptions{
					suppressError: func(errMsg *interfaces.ErrorMessage) bool {
						replayCalls++
						return shouldReplayResponsesWebsocketPinnedAuthFailure(errMsg)
					},
				},
			)
			if got != want || errForward != nil || replayCalls != 1 || cancelCalls != 1 || cancelValue != want.Error {
				t.Fatalf("status=%d iteration=%d: original_error=%t forward_error=%v replay_calls=%d cancel_calls=%d original_cancel=%t", status, iteration, got == want, errForward, replayCalls, cancelCalls, cancelValue == want.Error)
			}
		}
	}
}
