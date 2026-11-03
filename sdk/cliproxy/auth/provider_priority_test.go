package auth

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func providerPriorityAuth(id, group, provider string, supplierPriority, keyPriority int) *Auth {
	return &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{
		AttributeProviderGroup: group, AttributeProviderPriority: strconv.Itoa(supplierPriority),
		"priority": strconv.Itoa(keyPriority),
	}}
}

func priorityTestManager(t *testing.T, selector Selector, auths ...*Auth) (*Manager, []string) {
	t.Helper()
	manager := NewManager(nil, selector, nil)
	providers := []string{}
	seen := map[string]bool{}
	for _, auth := range auths {
		if !seen[auth.Provider] {
			providers = append(providers, auth.Provider)
			seen[auth.Provider] = true
			manager.RegisterExecutor(schedulerTestExecutor{provider: auth.Provider})
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "priority-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	return manager, providers
}

func priorityExecute(t *testing.T, manager *Manager, providers []string, ctx context.Context, metadata map[string]any) string {
	t.Helper()
	if metadata == nil {
		metadata = map[string]any{}
	}
	selected := ""
	metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(id string) { selected = id }
	_, err := manager.Execute(ctx, providers, cliproxyexecutor.Request{Model: "priority-model"}, cliproxyexecutor.Options{Metadata: metadata})
	if err != nil {
		t.Fatal(err)
	}
	if selected == "" {
		t.Fatal("no selected credential reported")
	}
	return selected
}

func TestProviderPriority_ManagerUpdateAndMixedProtocolSelection(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, strategy := range []string{"fill-first", "round-robin", "weighted"} {
			t.Run(fmt.Sprintf("mixed=%t/%s", mixed, strategy), func(t *testing.T) {
				var selector Selector = &FillFirstSelector{}
				switch strategy {
				case "round-robin":
					selector = &RoundRobinSelector{}
				case "weighted":
					selector = &WeightedRoundRobinSelector{}
				}
				otherProtocol := "codex"
				if mixed {
					otherProtocol = "claude"
				}
				anyHigh := providerPriorityAuth("z-any-high", "any", "codex", 10, 9)
				anyLow := providerPriorityAuth("y-any-low", "any", "codex", 10, -50)
				agent := providerPriorityAuth("a-agent", "agent", otherProtocol, 9, 100)
				manager, providers := priorityTestManager(t, selector, anyHigh, anyLow, agent)
				assertPick := func(want string) {
					t.Helper()
					if got := priorityExecute(t, manager, providers, context.Background(), nil); got != want {
						t.Fatalf("selected=%s, want=%s", got, want)
					}
				}
				update := func(id string, edit func(*Auth)) {
					t.Helper()
					a, _ := manager.GetByID(id)
					a = a.Clone()
					edit(a)
					if _, err := manager.Update(context.Background(), a); err != nil {
						t.Fatal(err)
					}
				}
				assertPick(anyHigh.ID)
				// The cached supplier bucket must honor key-only hot updates.
				update(anyLow.ID, func(a *Auth) { a.Attributes["priority"] = "20" })
				assertPick(anyLow.ID)
				// The same cached model must see supplier-only hot updates.
				update(agent.ID, func(a *Auth) { a.Attributes[AttributeProviderPriority] = "11" })
				assertPick(agent.ID)
				update(agent.ID, func(a *Auth) { a.Disabled = true })
				assertPick(anyLow.ID)
				update(anyLow.ID, func(a *Auth) { a.Disabled = true })
				assertPick(anyHigh.ID)
				// A probe pin remains authoritative even below the normal supplier tier.
				update(agent.ID, func(a *Auth) { a.Disabled = false; a.Attributes[AttributeProviderPriority] = "9" })
				if got := priorityExecute(t, manager, providers, context.Background(), map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: agent.ID}); got != agent.ID {
					t.Fatalf("pin selected %s", got)
				}
			})
		}
	}
}

func TestProviderPriority_EqualSuppliersKeepIndependentKeyTiers(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed=%t/weighted=%t", mixed, weighted), func(t *testing.T) {
				var selector Selector = &RoundRobinSelector{}
				if weighted {
					selector = &WeightedRoundRobinSelector{}
				}
				protocol := "codex"
				if mixed {
					protocol = "claude"
				}
				a := providerPriorityAuth("a-high", "a", "codex", 10, -1)
				b := providerPriorityAuth("b-high", "b", protocol, 10, 100)
				manager, providers := priorityTestManager(t, selector, a, b,
					providerPriorityAuth("a-low", "a", "codex", 10, -2),
					providerPriorityAuth("b-low", "b", protocol, 10, 99))
				counts := map[string]int{}
				for i := 0; i < 12; i++ {
					counts[priorityExecute(t, manager, providers, context.Background(), nil)]++
				}
				if counts[a.ID] != 6 || counts[b.ID] != 6 || len(counts) != 2 {
					t.Fatalf("equal suppliers should each retain their top key: %v", counts)
				}
			})
		}
	}
}

func TestProviderPriority_ZeroWeightAndWebsocketDoNotHideSupplierBackups(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		t.Run(fmt.Sprintf("weighted=%t", weighted), func(t *testing.T) {
			high := providerPriorityAuth("any-high", "any", "codex", 10, 9)
			low := providerPriorityAuth("any-low", "any", "codex", 10, -5)
			agent := providerPriorityAuth("agent", "agent", "codex", 9, 100)
			agent.Attributes["websockets"] = "true"
			var selector Selector = &FillFirstSelector{}
			want := high.ID
			if weighted {
				selector = &WeightedRoundRobinSelector{}
				high.Attributes[AttributeWeight] = "0"
				want = low.ID
			}
			manager, providers := priorityTestManager(t, selector, high, low, agent)
			if got := priorityExecute(t, manager, providers, cliproxyexecutor.WithDownstreamWebsocket(context.Background()), nil); got != want {
				t.Fatalf("selected=%s, want=%s", got, want)
			}
		})
	}
}

func TestProviderPriority_StandaloneLegacyAndIntegerBoundaries(t *testing.T) {
	cases := []struct{ supplier, other int }{{0, -1}, {-10, -20}, {math.MaxInt, math.MaxInt - 1}, {math.MinInt + 1, math.MinInt}}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.supplier), func(t *testing.T) {
			a := providerPriorityAuth("preferred", "a", "codex", tc.supplier, math.MinInt)
			b := providerPriorityAuth("backup", "b", "codex", tc.other, math.MaxInt)
			selected, err := (&FillFirstSelector{}).Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, []*Auth{b, a})
			if err != nil || selected.ID != a.ID {
				t.Fatalf("selected=%v err=%v", selected, err)
			}
		})
	}
	legacy := &Auth{ID: "legacy", Attributes: map[string]string{"priority": "20"}}
	grouped := providerPriorityAuth("grouped", "a", "codex", 10, 100)
	selected, err := (&FillFirstSelector{}).Pick(context.Background(), "codex", "", cliproxyexecutor.Options{}, []*Auth{grouped, legacy})
	if err != nil || selected.ID != legacy.ID {
		t.Fatalf("legacy priority changed: selected=%v err=%v", selected, err)
	}
}

func TestProviderPriority_CooldownKeepsLowerKeyBeforeLowerSupplier(t *testing.T) {
	a := providerPriorityAuth("a-high", "a", "codex", 10, 100)
	a.ModelStates = map[string]*ModelState{"priority-model": {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), Quota: QuotaState{Exceeded: true}}}
	b := providerPriorityAuth("a-low", "a", "codex", 10, -100)
	c := providerPriorityAuth("b-high", "b", "codex", 9, 200)
	for _, selector := range []Selector{&FillFirstSelector{}, &RoundRobinSelector{}, &WeightedRoundRobinSelector{}} {
		selected, err := selector.Pick(context.Background(), "codex", "priority-model", cliproxyexecutor.Options{}, []*Auth{a, b, c})
		if err != nil || selected.ID != b.ID {
			t.Fatalf("selected=%v err=%v", selected, err)
		}
	}
}

func TestProviderPriority_WeightedKeysAndModelEligibility(t *testing.T) {
	a := providerPriorityAuth("a-weight-one", "any", "codex", 10, 9)
	b := providerPriorityAuth("b-weight-three", "any", "codex", 10, 9)
	c := providerPriorityAuth("c-other-model", "any", "codex", 10, 100)
	a.Attributes[AttributeWeight], b.Attributes[AttributeWeight] = "1", "3"
	manager, providers := priorityTestManager(t, &WeightedRoundRobinSelector{}, a, b,
		providerPriorityAuth("backup-weighted", "agent", "codex", 9, 1000))
	registry.GetGlobalRegistry().RegisterClient(c.ID, "codex", []*registry.ModelInfo{{ID: "different-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(c.ID) })
	if _, err := manager.Register(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i := 0; i < 16; i++ {
		counts[priorityExecute(t, manager, providers, context.Background(), nil)]++
	}
	if counts[a.ID] != 4 || counts[b.ID] != 12 || len(counts) != 2 {
		t.Fatalf("within-supplier weighted selection changed: %v", counts)
	}
}

func TestProviderPriority_ExistingAffinitySurvivesSupplierRecovery(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	defer selector.Stop()
	any := providerPriorityAuth("any", "any", "codex", 10, 9)
	agent := providerPriorityAuth("agent", "agent", "codex", 9, 10)
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"bound-session"}`)}
	first, err := selector.Pick(context.Background(), "codex", "priority-model", opts, []*Auth{agent})
	if err != nil || first.ID != agent.ID {
		t.Fatalf("initial binding: selected=%v err=%v", first, err)
	}
	bound, err := selector.Pick(context.Background(), "codex", "priority-model", opts, []*Auth{any, agent})
	if err != nil || bound.ID != agent.ID {
		t.Fatalf("existing binding changed on recovery: selected=%v err=%v", bound, err)
	}
	opts.OriginalRequest = []byte(`{"prompt_cache_key":"new-session"}`)
	fresh, err := selector.Pick(context.Background(), "codex", "priority-model", opts, []*Auth{any, agent})
	if err != nil || fresh.ID != any.ID {
		t.Fatalf("new binding ignored supplier priority: selected=%v err=%v", fresh, err)
	}
}

func TestProviderPriority_ExcludedGroupsPreserveLegacyWebsocketPreference(t *testing.T) {
	for _, exclusion := range []string{"tried", "zero-weight"} {
		t.Run(exclusion, func(t *testing.T) {
			httpAuth := &Auth{ID: "legacy-http", Provider: "codex", Attributes: map[string]string{"priority": "10"}}
			wsAuth := &Auth{ID: "legacy-ws", Provider: "codex", Attributes: map[string]string{"priority": "9", "websockets": "true"}}
			grouped := providerPriorityAuth("excluded-group", "any", "codex", 20, 20)
			var selector Selector = &FillFirstSelector{}
			tried := map[string]struct{}{}
			if exclusion == "tried" {
				tried[grouped.ID] = struct{}{}
			} else {
				selector = &WeightedRoundRobinSelector{}
				grouped.Attributes[AttributeWeight] = "0"
			}
			registerSchedulerModels(t, "codex", "priority-model", httpAuth.ID, wsAuth.ID, grouped.ID)
			scheduler := newSchedulerForTest(selector, httpAuth, wsAuth, grouped)
			got, err := scheduler.pickSingle(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), "codex", "priority-model", cliproxyexecutor.Options{}, tried)
			if err != nil || got.ID != wsAuth.ID {
				t.Fatalf("excluded group changed legacy websocket selection: selected=%v err=%v", got, err)
			}
		})
	}
}
