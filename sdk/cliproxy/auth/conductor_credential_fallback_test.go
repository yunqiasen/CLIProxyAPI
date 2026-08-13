package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type credentialFallbackStatusError struct {
	message string
}

func (e *credentialFallbackStatusError) Error() string {
	return e.message
}

func (*credentialFallbackStatusError) StatusCode() int {
	return http.StatusBadGateway
}

func (e *credentialFallbackStatusError) IsCredentialFallback() bool {
	return e != nil
}

func registerCredentialFallbackAuths(t *testing.T, manager *Manager, model string) (*Auth, *Auth) {
	t.Helper()
	badAuth := &Auth{ID: "aa-refused-auth", Provider: "claude"}
	goodAuth := &Auth{ID: "bb-good-auth", Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	for _, auth := range []*Auth{badAuth, goodAuth} {
		reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})
	return badAuth, goodAuth
}

func TestManagerCredentialFallbackExecuteRotatesWithoutCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	refusalErr := &credentialFallbackStatusError{message: `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`}
	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"aa-refused-auth": refusalErr,
		},
	}
	manager.RegisterExecutor(executor)

	model := "opus5"
	badAuth, goodAuth := registerCredentialFallbackAuths(t, manager, model)
	response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if string(response.Payload) != goodAuth.ID {
		t.Fatalf("payload = %q, want %q", response.Payload, goodAuth.ID)
	}
	if got := executor.ExecuteCalls(); len(got) != 2 || got[0] != badAuth.ID || got[1] != goodAuth.ID {
		t.Fatalf("execute calls = %v, want [%s %s]", got, badAuth.ID, goodAuth.ID)
	}

	updatedBad, _ := manager.GetByID(badAuth.ID)
	if updatedBad.Failed != 1 || updatedBad.Unavailable || !updatedBad.NextRetryAfter.IsZero() || updatedBad.ModelStates[model] != nil {
		t.Fatalf("refused auth state = %#v, want failed count without cooldown", updatedBad)
	}
	updatedGood, _ := manager.GetByID(goodAuth.ID)
	if updatedGood.Success != 1 || updatedGood.Failed != 0 {
		t.Fatalf("good auth totals = success=%d failed=%d, want 1/0", updatedGood.Success, updatedGood.Failed)
	}
}

func TestManagerCredentialFallbackStreamRotatesWithoutCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	refusalErr := &credentialFallbackStatusError{message: `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`}
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-refused-auth": refusalErr,
		},
	}
	manager.RegisterExecutor(executor)

	model := "opus5"
	badAuth, goodAuth := registerCredentialFallbackAuths(t, manager, model)
	result, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var payload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("fallback stream error = %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != goodAuth.ID {
		t.Fatalf("payload = %q, want %q", payload, goodAuth.ID)
	}
	if got := executor.StreamCalls(); len(got) != 2 || got[0] != badAuth.ID || got[1] != goodAuth.ID {
		t.Fatalf("stream calls = %v, want [%s %s]", got, badAuth.ID, goodAuth.ID)
	}

	updatedBad, _ := manager.GetByID(badAuth.ID)
	if updatedBad.Failed != 1 || updatedBad.Unavailable || !updatedBad.NextRetryAfter.IsZero() || updatedBad.ModelStates[model] != nil {
		t.Fatalf("refused auth state = %#v, want failed count without cooldown", updatedBad)
	}
	updatedGood, _ := manager.GetByID(goodAuth.ID)
	if updatedGood.Success != 1 || updatedGood.Failed != 0 {
		t.Fatalf("good auth totals = success=%d failed=%d, want 1/0", updatedGood.Success, updatedGood.Failed)
	}
}

func TestManagerCredentialFallbackAllStreamsFailWithoutLeakingPayload(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	refusalErr := &credentialFallbackStatusError{message: `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`}
	executor := &authFallbackExecutor{
		id: "claude",
		streamFirstErrors: map[string]error{
			"aa-refused-auth": refusalErr,
			"bb-good-auth":    refusalErr,
		},
	}
	manager.RegisterExecutor(executor)

	model := "opus5"
	firstAuth, secondAuth := registerCredentialFallbackAuths(t, manager, model)
	result, errExecute := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v, want terminal stream error", errExecute)
	}
	var payload []byte
	var streamErr error
	for chunk := range result.Chunks {
		payload = append(payload, chunk.Payload...)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if len(payload) != 0 {
		t.Fatalf("all-refused stream leaked payload: %q", payload)
	}
	if streamErr == nil {
		t.Fatal("terminal stream error = nil")
	}
	if got := executor.StreamCalls(); len(got) != 2 || got[0] != firstAuth.ID || got[1] != secondAuth.ID {
		t.Fatalf("stream calls = %v, want [%s %s]", got, firstAuth.ID, secondAuth.ID)
	}
	for _, authID := range []string{firstAuth.ID, secondAuth.ID} {
		updated, _ := manager.GetByID(authID)
		if updated.Failed != 1 || updated.Unavailable || updated.ModelStates[model] != nil {
			t.Fatalf("auth %s state = %#v, want failed count without cooldown", authID, updated)
		}
	}
}
