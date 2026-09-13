package auth

import (
	"context"
	"testing"
)

func TestRecoverPaymentCooldownFromProbe(t *testing.T) {
	for _, scenario := range []string{"recover", "newer_failure", "disabled", "different_config", "other_status", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			m := NewManager(nil, nil, nil)
			a := &Auth{ID: "probe-selected", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"api_key": "selected"}}
			if _, err := m.Register(ctx, a); err != nil {
				t.Fatal(err)
			}
			code := 402
			if scenario == "other_status" {
				code = 401
			}
			for _, model := range []string{"chosen", "other"} {
				m.MarkResult(ctx, Result{AuthID: a.ID, Model: model, Error: &Error{HTTPStatus: code, Message: "budget exhausted"}})
			}
			snapshot, _ := m.GetByID(a.ID)
			before := snapshot.ModelStates["other"].NextRetryAfter
			if scenario == "newer_failure" {
				m.MarkResult(ctx, Result{AuthID: a.ID, Model: "chosen", Error: &Error{HTTPStatus: 403, Message: "new failure"}})
			}
			if scenario == "disabled" || scenario == "different_config" {
				edited := snapshot.Clone()
				if scenario == "disabled" {
					edited.Disabled = true
				} else {
					edited.Attributes["api_key"] = "replacement"
				}
				if _, err := m.Update(ctx, edited); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "canceled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			beforeAttempt, _ := m.GetByID(a.ID)
			before = beforeAttempt.ModelStates["other"].NextRetryAfter
			recovered := m.RecoverPaymentCooldownFromProbe(ctx, snapshot, "chosen", "chosen")
			want := scenario == "recover"
			if recovered != want {
				t.Fatalf("recovered=%v want=%v", recovered, want)
			}
			got, _ := m.GetByID(a.ID)
			if want && (got.ModelStates["chosen"].Unavailable || !got.ModelStates["chosen"].NextRetryAfter.IsZero()) {
				t.Fatal("chosen model remains cooled")
			}
			if !got.ModelStates["other"].NextRetryAfter.Equal(before) {
				t.Fatal("other model cooldown changed")
			}
			if got.Success != snapshot.Success {
				t.Fatal("probe changed production usage counters")
			}
		})
	}
}

func TestProbePaymentRecoveryPersistsOnlySelectedModel(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	store := &recordingCooldownStateStore{}
	m.SetCooldownStateStore(store)
	a := &Auth{ID: "probe-persist", Provider: "codex", Attributes: map[string]string{"api_key": "selected"}}
	if _, err := m.Register(ctx, a); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"chosen", "other"} {
		m.MarkResult(ctx, Result{AuthID: a.ID, Model: model, Error: &Error{HTTPStatus: 402, Message: "budget exhausted"}})
	}
	snapshot, _ := m.GetByID(a.ID)
	if !m.RecoverPaymentCooldownFromProbe(ctx, snapshot, "chosen", "chosen") {
		t.Fatal("model not recovered")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	other := false
	for _, record := range store.records {
		if record.Model == "chosen" {
			t.Fatal("cleared model still persisted")
		}
		if record.Model == "other" {
			other = true
		}
	}
	if !other {
		t.Fatal("other model cooldown lost from persistence")
	}
}
