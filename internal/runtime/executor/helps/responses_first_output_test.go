package helps

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func startTestFirstOutputWatch(t *testing.T) (context.Context, *ResponsesFirstOutputWatch) {
	t.Helper()
	auth := &coreauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{coreauth.AttributeResponsesFirstOutputTimeoutSeconds: "3600"},
	}
	ctx, watch := StartResponsesFirstOutputWatch(context.Background(), auth)
	if watch == nil {
		t.Fatal("watch not started")
	}
	return ctx, watch
}

func TestResponsesFirstOutputWatchResetIgnoresStaleExpiry(t *testing.T) {
	_, watch := startTestFirstOutputWatch(t)

	staleGen := watch.generation
	attemptCtx := watch.Reset(context.Background())
	if attemptCtx.Err() != nil {
		t.Fatal("reset attempt context cancelled early")
	}

	watch.expire(staleGen)
	if watch.expired || watch.stopped {
		t.Fatal("stale generation expired the current attempt")
	}
	if attemptCtx.Err() != nil {
		t.Fatal("stale generation cancelled the current attempt")
	}

	watch.expire(watch.generation)
	if !watch.expired {
		t.Fatal("current generation did not expire")
	}
	if attemptCtx.Err() == nil {
		t.Fatal("current attempt context not cancelled on expiry")
	}
	watch.Close()
}

func TestResponsesFirstOutputWatchObserveStopsExpiry(t *testing.T) {
	_, watch := startTestFirstOutputWatch(t)

	if err := watch.Observe("response.output_text.delta", []byte(`{"delta":"x"}`)); err != nil {
		t.Fatalf("observe real event: %v", err)
	}
	watch.expire(watch.generation)
	if watch.expired {
		t.Fatal("expired after real output was observed")
	}
	watch.Close()
}

func TestResponsesFirstOutputWatchResetAfterToolOutputStaysStopped(t *testing.T) {
	for _, event := range []string{"response.output_item.added", "response.output_item.done"} {
		t.Run(event, func(t *testing.T) {
			attemptCtx, watch := startTestFirstOutputWatch(t)
			t.Cleanup(watch.Close)
			if err := watch.Observe(event, []byte(`{"item":{"type":"function_call","call_id":"call_tool","name":"lookup","arguments":"{}"}}`)); err != nil {
				t.Fatal(err)
			}
			retryCtx := watch.Reset(context.Background())
			watch.expire(watch.generation)
			if err := watch.Failure(nil); err != nil {
				t.Fatalf("watch restarted after tool output: %v", err)
			}
			if attemptCtx.Err() != nil || retryCtx.Err() != nil {
				t.Fatal("stopped watch canceled an output-bearing context")
			}
		})
	}
}

func TestResponsesFirstOutputWatchResetSilentAttemptPreservesCallerCancellation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expired bool
	}{
		{name: "before_expiry"},
		{name: "after_expiry", expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			auth := &coreauth.Auth{Provider: "codex", Attributes: map[string]string{coreauth.AttributeResponsesFirstOutputTimeoutSeconds: "3600"}}
			attemptCtx, watch := StartResponsesFirstOutputWatch(parent, auth)
			t.Cleanup(watch.Close)
			if tc.expired {
				watch.expire(watch.generation)
			}
			retryCtx := watch.Reset(parent)
			if retryCtx.Err() != nil || watch.Failure(nil) != nil || parent.Err() != nil {
				t.Fatal("silent attempt did not receive a fresh live context")
			}
			if !errors.Is(attemptCtx.Err(), context.Canceled) {
				t.Fatal("superseded attempt context stayed live")
			}
			cancel()
			if !errors.Is(retryCtx.Err(), context.Canceled) || !errors.Is(watch.Failure(nil), context.Canceled) {
				t.Fatal("retry detached from caller cancellation")
			}
		})
	}
}
