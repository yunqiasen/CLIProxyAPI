package auth

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const agentBudgetPoolFixture = `{"error":{"message":"Budget pool quota has been exhausted. Please ask an administrator to increase the limit or select another budget pool.","type":"bad_response_status_code","param":"","code":"bad_response_status_code"}}`

func TestRestoreCooldownStatesDropsOnlyAgentBudgetPoolRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(30 * time.Minute)
	dir := t.TempDir()
	store := NewFileCooldownStateStoreWithAuthDir(dir, dir)
	records := []CooldownStateRecord{
		{Provider: "codex", AuthID: "agent-restore", Model: "cpa-5.6s", Status: "cooling", NextRetryAfter: next, Reason: "payment_required", LastError: &Error{HTTPStatus: 402, Message: agentBudgetPoolFixture}, UpdatedAt: now},
		{Provider: "codex", AuthID: "agent-restore", Status: "cooling", NextRetryAfter: next, Reason: "payment_required", LastError: &Error{HTTPStatus: 402, Message: agentBudgetPoolFixture}, UpdatedAt: now},
		{Provider: "codex", AuthID: "agent-restore", Model: "cpa-6a", Status: "cooling", NextRetryAfter: next, Reason: "quota", Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next}, LastError: &Error{HTTPStatus: 429, Message: "rate limited"}, UpdatedAt: now},
		{Provider: "codex", AuthID: "other-restore", Model: "cpa-5.6s", Status: "cooling", NextRetryAfter: next, Reason: "payment_required", LastError: &Error{HTTPStatus: 402, Message: agentBudgetPoolFixture}, UpdatedAt: now},
	}
	if err := store.Save(ctx, records); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetCooldownStateStore(store)
	for _, a := range []*Auth{
		{ID: "agent-restore", Provider: "codex", Attributes: map[string]string{"base_url": "https://agentrouter.org/v1"}, Success: 3, Failed: 2},
		{ID: "other-restore", Provider: "codex", Attributes: map[string]string{"base_url": "https://other.example/v1"}},
	} {
		if _, err := manager.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.RestoreCooldownStates(ctx); err != nil {
		t.Fatal(err)
	}
	agent, ok := manager.GetByID("agent-restore")
	if !ok {
		t.Fatal("missing restored auth")
	}
	if _, err := (&FillFirstSelector{}).Pick(ctx, "codex", "cpa-5.6s", coreexecutor.Options{}, []*Auth{agent}); err != nil {
		t.Fatalf("persisted Agent budget failure still blocks recovered upstream: %v", err)
	}
	if _, err := (&FillFirstSelector{}).Pick(ctx, "codex", "cpa-6a", coreexecutor.Options{}, []*Auth{agent}); err == nil {
		t.Fatal("migration cleared another model's real rate-limit cooldown")
	}
	if agent.Success != 3 || agent.Failed != 2 {
		t.Fatal("migration fabricated a successful request")
	}
	other, _ := manager.GetByID("other-restore")
	if _, err := (&FillFirstSelector{}).Pick(ctx, "codex", "cpa-5.6s", coreexecutor.Options{}, []*Auth{other}); err == nil {
		t.Fatal("migration changed another provider's payment cooldown")
	}
	persisted, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	for _, record := range persisted {
		if record.AuthID == "agent-restore" && (record.Model == "cpa-5.6s" || (record.LastError != nil && record.LastError.Message == agentBudgetPoolFixture)) {
			t.Fatalf("budget-pool cooldown was persisted again: model=%q", record.Model)
		}
		if (record.AuthID == "agent-restore" && record.Model == "cpa-6a") || (record.AuthID == "other-restore" && record.Model == "cpa-5.6s") {
			retained++
		}
	}
	if retained != 2 {
		t.Fatalf("retained model cooldowns=%d, want real rate limit and other provider payment", retained)
	}
}

type budgetResultHook struct {
	NoopHook
	results []Result
}

func (h *budgetResultHook) OnResult(_ context.Context, result Result) {
	h.results = append(h.results, result)
}

func TestAgentBudgetPoolClassificationPreservesCredentialFailures(t *testing.T) {
	withQuotaCooldownEnabled(t)
	cases := []struct {
		name     string
		provider string
		baseURL  string
		status   int
		message  string
		blocked  bool
	}{
		{"agent budget pool", "codex", "https://agentrouter.org/v1", 402, agentBudgetPoolFixture, false},
		{"case insensitive host", "codex", "https://AGENTROUTER.ORG/v1/", 402, agentBudgetPoolFixture, false},
		{"ordinary payment required", "codex", "https://agentrouter.org/v1", 402, `{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"Account quota exhausted"}}`, true},
		{"account quota", "codex", "https://agentrouter.org/v1", 403, `{"error":{"code":"insufficient_user_quota","message":"user quota is not enough"}}`, true},
		{"authentication failure", "codex", "https://agentrouter.org/v1", 401, agentBudgetPoolFixture, true},
		{"rate limit", "codex", "https://agentrouter.org/v1", 429, agentBudgetPoolFixture, true},
		{"other provider type", "claude", "https://agentrouter.org/v1", 402, agentBudgetPoolFixture, true},
		{"other site", "codex", "https://anyrouter.top/v1", 402, agentBudgetPoolFixture, true},
		{"host suffix", "codex", "https://agentrouter.org.example/v1", 402, agentBudgetPoolFixture, true},
		{"host in URL user info", "codex", "https://agentrouter.org@other.example/v1", 402, agentBudgetPoolFixture, true},
		{"invalid URL", "codex", "https://%", 402, agentBudgetPoolFixture, true},
		{"missing URL", "codex", "", 402, agentBudgetPoolFixture, true},
		{"wrong scheme", "codex", "file://agentrouter.org/v1", 402, agentBudgetPoolFixture, true},
		{"invalid JSON", "codex", "https://agentrouter.org/v1", 402, agentBudgetPoolFixture + "trailing", true},
		{"text only", "codex", "https://agentrouter.org/v1", 402, "Budget pool quota has been exhausted.", true},
		{"different type", "codex", "https://agentrouter.org/v1", 402, strings.Replace(agentBudgetPoolFixture, `"type":"bad_response_status_code"`, `"type":"insufficient_quota"`, 1), true},
		{"different code", "codex", "https://agentrouter.org/v1", 402, strings.Replace(agentBudgetPoolFixture, `"code":"bad_response_status_code"`, `"code":"insufficient_user_quota"`, 1), true},
		{"quoted message", "codex", "https://agentrouter.org/v1", 402, strings.Replace(agentBudgetPoolFixture, "Budget pool quota", "A user quoted: Budget pool quota", 1), true},
	}
	for _, tc := range cases {
		for _, model := range []string{"", "cpa-5.6s"} {
			t.Run(fmt.Sprintf("%s/model=%s", tc.name, model), func(t *testing.T) {
				ctx := context.Background()
				hook := &budgetResultHook{}
				manager := NewManager(nil, &FillFirstSelector{}, hook)
				a := &Auth{ID: "budget-classification", Provider: tc.provider, Status: StatusActive, Attributes: map[string]string{"base_url": tc.baseURL, "provider_name": "AgentRouter"}}
				if _, err := manager.Register(ctx, a); err != nil {
					t.Fatal(err)
				}
				failure := &Error{HTTPStatus: tc.status, Message: tc.message}
				manager.MarkResult(ctx, Result{AuthID: a.ID, Provider: "codex", Model: model, Error: failure})
				current, _ := manager.GetByID(a.ID)
				_, err := (&FillFirstSelector{}).Pick(ctx, tc.provider, model, coreexecutor.Options{}, []*Auth{current})
				if (err != nil) != tc.blocked {
					t.Fatalf("blocked=%v want=%v error=%v", err != nil, tc.blocked, err)
				}
				if current.Failed != 1 || current.Success != 0 || len(hook.results) != 1 || hook.results[0].Success || !reflect.DeepEqual(hook.results[0].Error, failure) {
					t.Fatal("classification lost the original error or changed failure accounting")
				}
			})
		}
	}
}

func TestAgentBudgetPoolFailurePreservesExistingBlocks(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, prior := range []string{"auth_disabled", "model_disabled", "auth_quota", "same_model_quota", "other_model_quota"} {
		t.Run(prior, func(t *testing.T) {
			ctx := context.Background()
			manager := NewManager(nil, nil, nil)
			a := &Auth{ID: "budget-existing", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"base_url": "https://agentrouter.org/v1"}}
			if prior == "auth_disabled" {
				a.Disabled = true
				a.Status = StatusDisabled
			}
			if prior == "model_disabled" {
				a.ModelStates = map[string]*ModelState{"cpa-5.6s": {Status: StatusDisabled}}
			}
			if _, err := manager.Register(ctx, a); err != nil {
				t.Fatal(err)
			}
			model := "cpa-5.6s"
			if strings.HasSuffix(prior, "quota") {
				if prior == "auth_quota" {
					model = ""
				} else if prior == "other_model_quota" {
					model = "cpa-6a"
				}
				manager.MarkResult(ctx, Result{AuthID: a.ID, Model: model, Error: &Error{HTTPStatus: 429, Message: "real quota failure"}})
			}
			before, _ := manager.GetByID(a.ID)
			budgetModel := "cpa-5.6s"
			if prior == "auth_quota" {
				budgetModel = ""
			}
			manager.MarkResult(ctx, Result{AuthID: a.ID, Model: budgetModel, Error: &Error{HTTPStatus: 402, Message: agentBudgetPoolFixture}})
			after, _ := manager.GetByID(a.ID)
			if after.Disabled != before.Disabled || after.Status != before.Status || !after.NextRetryAfter.Equal(before.NextRetryAfter) || !reflect.DeepEqual(after.ModelStates, before.ModelStates) || !reflect.DeepEqual(after.LastError, before.LastError) {
				t.Fatal("budget pool failure erased or overwrote an existing block")
			}
			if _, err := (&FillFirstSelector{}).Pick(ctx, "codex", model, coreexecutor.Options{}, []*Auth{after}); err == nil {
				t.Fatal("credential or model with an existing block became selectable")
			}
			if after.Failed != before.Failed+1 || after.Success != before.Success {
				t.Fatal("failure statistics were lost")
			}
		})
	}
}
