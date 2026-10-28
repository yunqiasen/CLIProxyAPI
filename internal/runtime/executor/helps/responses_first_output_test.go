package helps

import (
	"context"
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
