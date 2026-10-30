package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type attemptBudgetExecutor struct {
	id string

	mu    sync.Mutex
	calls int
	errs  []error
}

func (e *attemptBudgetExecutor) Identifier() string { return e.id }

func (e *attemptBudgetExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *attemptBudgetExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	n := e.calls
	errs := append([]error(nil), e.errs...)
	e.mu.Unlock()
	if n <= len(errs) && errs[n-1] != nil {
		return nil, errs[n-1]
	}
	return nil, io.EOF
}

func (e *attemptBudgetExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *attemptBudgetExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *attemptBudgetExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *attemptBudgetExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func newAttemptBudgetManager(t *testing.T, exec ProviderExecutor, authCount, maxRetryCredentials int, model string) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(2, 0, maxRetryCredentials)
	m.RegisterExecutor(exec)
	baseID := uuid.NewString()
	for i := 0; i < authCount; i++ {
		auth := &Auth{ID: baseID + "-" + strconv.Itoa(i), Provider: exec.Identifier(), Status: StatusActive}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth %d: %v", i, err)
		}
	}
	return m
}

// A dead site must not be retried once per credential, per outer round and per
// bootstrap restart. Transport failures share one whole-request budget.
func TestExecuteStreamTransportFailuresShareWholeRequestBudget(t *testing.T) {
	model := "attempt-budget-" + uuid.NewString()
	exec := &attemptBudgetExecutor{id: "attempt-budget"}
	m := newAttemptBudgetManager(t, exec, 8, 8, model)

	if _, err := m.ExecuteStream(context.Background(), []string{exec.id}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true}); err == nil {
		t.Fatal("expected transport failure")
	}
	limit := requestAttemptLimit(2)
	if calls := exec.Calls(); calls > limit {
		t.Fatalf("transport attempts = %d, want <= %d", calls, limit)
	}
}

// Mixed failures reset the per-round transport counter, so only the shared
// whole-request budget stops the multiplication across credentials and rounds.
func TestExecuteStreamMixedFailuresRespectWholeRequestAttemptLimit(t *testing.T) {
	model := "attempt-budget-mixed-" + uuid.NewString()
	exec := &attemptBudgetExecutor{id: "attempt-budget-mixed"}
	m := newAttemptBudgetManager(t, exec, 40, 8, model)

	if _, err := m.ExecuteStream(context.Background(), []string{exec.id}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true}); err == nil {
		t.Fatal("expected failure")
	}
	calls := exec.Calls()
	limit := requestAttemptLimit(2)
	if calls > limit {
		t.Fatalf("attempts = %d, want <= %d", calls, limit)
	}
	if calls < 2 {
		t.Fatalf("attempts = %d, expected multiple attempts before the cap", calls)
	}
}
