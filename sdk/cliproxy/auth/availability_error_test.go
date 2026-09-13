package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestPaymentCooldownPreservesCauseAcrossSelectors(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model = "diagnostic-payment-model"
	reg := registry.GetGlobalRegistry()
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	ids := make([]string, 0, 10)
	for i := range 10 {
		id := fmt.Sprintf("payment-fixture-%d", i)
		ids = append(ids, id)
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		_, err := m.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: "codex", Attributes: map[string]string{"provider_name": "AgentRouter"}})
		if err != nil {
			t.Fatal(err)
		}
		status := 402
		message := `{"error":{"code":"bad_response_status_code","message":"Budget pool quota has been exhausted. Please ask an administrator to increase the limit or select another budget pool."}}`
		if i == 9 {
			status = 403
			message = `{"error":{"code":"insufficient_user_quota","message":"user quota is not enough"}}`
		}
		m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: &Error{HTTPStatus: status, Message: message}})
	}
	t.Cleanup(func() {
		for _, id := range ids {
			reg.UnregisterClient(id)
		}
	})
	auths := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		a, _ := m.GetByID(id)
		auths = append(auths, a)
	}
	checks := map[string]func() error{
		"round-robin": func() error {
			_, err := (&RoundRobinSelector{}).Pick(context.Background(), "codex", model, coreexecutor.Options{}, auths)
			return err
		},
		"weighted": func() error {
			_, err := (&WeightedRoundRobinSelector{}).Pick(context.Background(), "codex", model, coreexecutor.Options{}, auths)
			return err
		},
		"fill-first": func() error {
			_, err := (&FillFirstSelector{}).Pick(context.Background(), "codex", model, coreexecutor.Options{}, auths)
			return err
		},
		"scheduler": func() error {
			_, err := m.scheduler.pickSingle(context.Background(), "codex", model, coreexecutor.Options{}, nil)
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			err := check()
			if err == nil {
				t.Fatal("expected cooling error")
			}
			if gjson.Get(err.Error(), "error.code").String() != "upstream_credentials_cooling_down" {
				t.Fatalf("known quota cause lost: %v", err)
			}
			if gjson.Get(err.Error(), "error.reset_seconds").Int() < 1700 {
				t.Errorf("missing cooldown deadline: %v", err)
			}
			causes := gjson.Get(err.Error(), "error.causes").Array()
			if len(causes) != 2 {
				t.Errorf("expected budget and account causes: %v", err)
			}
		})
	}
	m.MarkResult(context.Background(), Result{AuthID: ids[0], Provider: "codex", Model: model, Success: true})
	if _, err := m.scheduler.pickSingle(context.Background(), "codex", model, coreexecutor.Options{}, nil); err != nil {
		t.Fatalf("healthy credential blocked: %v", err)
	}
	if blocked, _, _ := isAuthBlockedForModel(auths[1], model, auths[1].ModelStates[model].NextRetryAfter.Add(time.Second)); blocked {
		t.Fatal("expired cooldown remains blocked")
	}
}

func TestCredentialCooldownDiagnosticBoundaries(t *testing.T) {
	now := time.Now()
	known := &Auth{ID: "private-id", Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 402, Message: `{"error":{"message":"Budget pool quota has been exhausted. private-key-details"}}`}}
	for _, tc := range []struct {
		name  string
		auths []*Auth
		want  string
	}{
		{"empty", nil, "auth_not_found"},
		{"known", []*Auth{known}, "upstream_credentials_cooling_down"},
		{"disabled", []*Auth{{Disabled: true, Provider: "codex", LastError: known.LastError}}, "auth_unavailable"},
		{"mixed_unknown", []*Auth{known, {Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 500}}}, "auth_unavailable"},
		{"rate_limit", []*Auth{{Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Minute), Quota: QuotaState{Exceeded: true}, LastError: &Error{HTTPStatus: 429}}}, "model_cooldown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "", coreexecutor.Options{}, tc.auths)
			if err == nil {
				t.Fatal("expected error")
			}
			code := gjson.Get(err.Error(), "error.code").String()
			if e, ok := err.(*Error); ok {
				code = e.Code
			}
			if code != tc.want {
				t.Fatalf("code=%s want=%s", code, tc.want)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("diagnostic leaks credential data")
			}
			if tc.name == "known" {
				restored := restoreModelCooldownErrorModel(err, "cpa-6a")
				if gjson.Get(restored.Error(), "error.model").String() != "cpa-6a" || SafeResponseHeaders(restored).Get("Retry-After") == "" {
					t.Fatal("missing model/deadline")
				}
				if restored.(interface{ StatusCode() int }).StatusCode() != 503 {
					t.Fatal("wrong HTTP status")
				}
			}
		})
	}
	expired := known.Clone()
	expired.NextRetryAfter = now.Add(-time.Second)
	if _, err := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "", coreexecutor.Options{}, []*Auth{expired}); err != nil {
		t.Fatalf("expired credential blocked: %v", err)
	}
	if _, err := (&RoundRobinSelector{}).Pick(context.Background(), "codex", "", coreexecutor.Options{}, []*Auth{known, {ID: "healthy", Provider: "codex"}}); err != nil {
		t.Fatalf("healthy credential blocked: %v", err)
	}
}

func TestPaymentCooldownPreservesCauseThroughManager(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model = "payment-manager-alias"
	reg := registry.GetGlobalRegistry()
	for _, selector := range []Selector{&RoundRobinSelector{}, &FillFirstSelector{}, &WeightedRoundRobinSelector{}, &paymentDelegatingSelector{}} {
		m := NewManager(nil, selector, nil)
		m.RegisterExecutor(&paymentDiagnosticExecutor{})
		id := fmt.Sprintf("payment-manager-%T", selector)
		reg.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		_, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex"})
		if err != nil {
			t.Fatal(err)
		}
		m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 402, Message: `{"error":{"message":"Budget pool quota has been exhausted."}}`}})
		_, err = m.Execute(context.Background(), []string{"codex"}, coreexecutor.Request{Model: model, Payload: []byte(`{}`)}, coreexecutor.Options{})
		if err == nil || gjson.Get(err.Error(), "error.code").String() != "upstream_credentials_cooling_down" {
			t.Errorf("%T lost cause: %v", selector, err)
		}
		for _, requested := range []string{model, model + "(high)"} {
			_, _, err = m.scheduler.pickMixed(context.Background(), []string{"codex", "claude"}, requested, coreexecutor.Options{}, nil)
			if err == nil || gjson.Get(err.Error(), "error.code").String() != "upstream_credentials_cooling_down" {
				t.Errorf("mixed %s lost cause: %v", requested, err)
			}
		}
	}
}

type paymentDiagnosticExecutor struct{}

func (*paymentDiagnosticExecutor) Identifier() string { return "codex" }
func (*paymentDiagnosticExecutor) Execute(context.Context, *Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, fmt.Errorf("unexpected execution while cooling")
}
func (*paymentDiagnosticExecutor) ExecuteStream(context.Context, *Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, fmt.Errorf("unexpected stream while cooling")
}
func (*paymentDiagnosticExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (*paymentDiagnosticExecutor) CountTokens(context.Context, *Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (*paymentDiagnosticExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected request while cooling")
}

// Third-party selectors use the manager preselection path rather than fast scheduling.
type paymentDelegatingSelector struct{ RoundRobinSelector }

func (s *paymentDelegatingSelector) Pick(ctx context.Context, provider, model string, opts coreexecutor.Options, auths []*Auth) (*Auth, error) {
	return s.RoundRobinSelector.Pick(ctx, provider, model, opts, auths)
}
