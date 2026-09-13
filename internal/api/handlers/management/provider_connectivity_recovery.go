package management

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// A probe of an unsaved draft must never certify the production configuration.
func (h *Handler) codexRecoverySnapshot(body providerConnectivityTestRequest, cfg *config.Config) *coreauth.Auth {
	if h.authManager == nil || body.AuthIndex == "" {
		return nil
	}
	saved := h.authByIndex(body.AuthIndex)
	if saved == nil {
		return nil
	}
	_, savedCfg, err := h.codexConnectivityAuth(providerConnectivityTestRequest{AuthIndex: body.AuthIndex})
	if err != nil || !reflect.DeepEqual(cfg, savedCfg) {
		return nil
	}
	return saved
}

func (h *Handler) recoverCodexProbe(ctx context.Context, body providerConnectivityTestRequest, cfg *config.Config, snapshot *coreauth.Auth, upstreamModel string, payload []byte) {
	if snapshot == nil || ctx.Err() != nil || !completedCodexProbe(payload) {
		return
	}
	// Recheck settings after the network call, including configuration hot reload.
	if h.codexRecoverySnapshot(body, cfg) == nil {
		return
	}
	h.authManager.RecoverPaymentCooldownFromProbe(ctx, snapshot, body.Model, upstreamModel)
}

func completedCodexProbe(payload []byte) bool {
	reader := helps.NewResponsesSSEReader(bytes.NewReader(payload), len(payload)+1)
	completed := false
	for {
		frame, err := reader.Next()
		if err == io.EOF {
			return completed
		}
		if err != nil {
			return false
		}
		data := bytes.TrimSpace(frame.Data)
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var event struct {
			Type     string          `json:"type"`
			Error    json.RawMessage `json:"error"`
			Response struct {
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false
		}
		hasError := func(raw json.RawMessage) bool {
			return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		}
		if hasError(event.Error) || hasError(event.Response.Error) {
			return false
		}
		switch frame.Event {
		case "error", "response.failed", "response.incomplete", "response.cancelled":
			return false
		}
		kind := event.Type
		if kind == "" {
			kind = frame.Event
		}
		switch kind {
		case "error", "response.failed", "response.incomplete", "response.cancelled":
			return false
		case "response.completed":
			if event.Response.Status != "completed" {
				return false
			}
			completed = true
		}
	}
}
