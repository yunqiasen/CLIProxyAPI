package auth

import (
	"context"
	"reflect"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// RecoverPaymentCooldownFromProbe resumes only an unchanged payment-cooled model.
// The caller must verify a completed probe using the saved request configuration.
// Probes do not count as production requests or clear unrelated cooldowns.
func (m *Manager) RecoverPaymentCooldownFromProbe(ctx context.Context, snapshot *Auth, routeModel, upstreamModel string) bool {
	if snapshot == nil || ctx.Err() != nil {
		return false
	}
	m.mu.Lock()
	current := m.auths[snapshot.ID]
	if current == nil || current.Disabled || current.Status == StatusDisabled ||
		!current.UpdatedAt.Equal(snapshot.UpdatedAt) || current.Provider != snapshot.Provider ||
		current.Prefix != snapshot.Prefix || current.ProxyURL != snapshot.ProxyURL ||
		!reflect.DeepEqual(current.Attributes, snapshot.Attributes) {
		m.mu.Unlock()
		return false
	}
	modelKey := canonicalModelKey(m.stateModelForExecution(current, routeModel, upstreamModel, false))
	state := current.ModelStates[modelKey]
	expected := snapshot.ModelStates[modelKey]
	if modelKey == "" || state == nil || expected == nil || !reflect.DeepEqual(state, expected) ||
		!state.Unavailable || state.LastError == nil ||
		(state.LastError.HTTPStatus != 402 && state.LastError.HTTPStatus != 403) || ctx.Err() != nil {
		m.mu.Unlock()
		return false
	}
	now := time.Now()
	resetModelState(state, now)
	updateAggregatedAvailability(current, now)
	if !hasModelError(current, now) {
		current.LastError = nil
		current.StatusMessage = ""
		current.Status = StatusActive
	}
	current.UpdatedAt = now
	_ = m.persist(ctx, current)
	// Keep scheduler/registry changes ordered with subsequent production failures.
	if m.scheduler != nil {
		m.scheduler.upsertAuth(current.Clone())
	}
	registry.GetGlobalRegistry().ClearModelQuotaExceeded(current.ID, modelKey)
	registry.GetGlobalRegistry().ResumeClientModel(current.ID, modelKey)
	m.mu.Unlock()
	m.persistCooldownStates(context.Background())
	return true
}
