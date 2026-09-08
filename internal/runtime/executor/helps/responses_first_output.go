package helps

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ResponsesFirstOutputWatch bounds only an opted-in HTTP attempt's provisional prefix.
// The parent stays live for credential recovery; output and expiry are serialized.
type ResponsesFirstOutputWatch struct {
	parent           context.Context
	cancel           context.CancelFunc
	timer            *time.Timer
	mu               sync.Mutex
	stopped, expired bool
}

// StartResponsesFirstOutputWatch leaves unconfigured credentials unchanged.
func StartResponsesFirstOutputWatch(ctx context.Context, auth *coreauth.Auth) (context.Context, *ResponsesFirstOutputWatch) {
	if auth == nil || auth.Provider != "codex" {
		return ctx, nil
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(auth.Attributes[coreauth.AttributeResponsesFirstOutputTimeoutSeconds]), 10, 32)
	if err != nil || seconds <= 0 {
		return ctx, nil
	}
	attemptCtx, cancel := context.WithCancel(ctx)
	w := &ResponsesFirstOutputWatch{parent: ctx, cancel: cancel}
	w.timer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.stopped {
			w.stopped, w.expired = true, true
			w.cancel()
		}
	})
	return attemptCtx, w
}

// Observe must run before publishing a frame. Once expiry wins, no frame is exposed.
// Provisional lifecycle/scaffolding events never extend or stop the wait.
func (w *ResponsesFirstOutputWatch) Observe(event string, data []byte) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.failureLocked(nil); err != nil {
		return err
	}
	if !ResponsesProvisionalEvent(event, data) {
		w.stopped = true
		w.timer.Stop()
	}
	return nil
}

// Failure distinguishes expiry of this attempt from caller cancellation.
func (w *ResponsesFirstOutputWatch) Failure(err error) error {
	if w == nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failureLocked(err)
}

func (w *ResponsesFirstOutputWatch) failureLocked(err error) error {
	if parentErr := w.parent.Err(); parentErr != nil {
		return parentErr
	}
	if w.expired {
		return ResponsesFirstOutputTimeoutError{}
	}
	return err
}

func (w *ResponsesFirstOutputWatch) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	w.timer.Stop()
	w.mu.Unlock()
	w.cancel()
}

// ResponsesFirstOutputTimeoutError is retryable only before the caller commits output.
// Stream ownership and the existing credential policy retain that boundary.
type ResponsesFirstOutputTimeoutError struct{}

func (ResponsesFirstOutputTimeoutError) Error() string {
	return `{"error":{"type":"server_error","code":"` + clienterror.CodeUpstreamResponseTimeout + `","message":"Upstream produced no output before the configured first-output wait limit."}}`
}

func (ResponsesFirstOutputTimeoutError) StatusCode() int { return http.StatusGatewayTimeout }
