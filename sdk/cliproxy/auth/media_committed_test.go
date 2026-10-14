package auth

import (
	"fmt"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type committedMediaTestError struct{}

func (committedMediaTestError) Error() string         { return "media task accepted; poll failed" }
func (committedMediaTestError) StatusCode() int       { return http.StatusBadGateway }
func (committedMediaTestError) IsRequestStop() bool   { return true }
func (committedMediaTestError) IsRequestScoped() bool { return true }

func TestHomeCommittedMediaFailureStopsRedispatch(t *testing.T) {
	for _, rounds := range []int{0, 3} {
		t.Run(fmt.Sprint(rounds), func(t *testing.T) {
			dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a", "home-retry-b"}, metadata: map[string]any{"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{Status: 502, Match: []string{"media task accepted"}, Action: "continue-and-cooldown"}}}}
			executor := &retryContractHomeExecutor{failure: committedMediaTestError{}}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(rounds, 0, 0)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)
			_, err := manager.Execute(t.Context(), []string{"home-retry-contract"}, ex.Request{Model: "media-model"}, ex.Options{})
			if err == nil || !isRequestStopError(err) {
				t.Fatalf("committed error lost: %v", err)
			}
			if got := executor.Calls(); len(got) != 1 {
				t.Fatalf("accepted task redispatched: %v", got)
			}
		})
	}
}
