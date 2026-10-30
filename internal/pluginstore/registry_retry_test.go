package pluginstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// GitHub raw endpoints intermittently drop the TLS handshake through local
// proxies; one EOF must not blank the whole plugin store listing.
func TestFetchRegistryRetriesTransientTransportErrors(t *testing.T) {
	t.Parallel()

	var attempts int
	client := Client{registryBackoff: func(context.Context, int) error { return nil }, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts < registryFetchAttempts {
			return nil, fmt.Errorf("request failed: EOF")
		}
		return testHTTPResponse(req, 200, nil, `{"schema_version":1,"plugins":[]}`), nil
	})}
	registry, errFetch := client.FetchRegistry(context.Background())
	if errFetch != nil {
		t.Fatalf("fetch registry error=%v", errFetch)
	}
	if attempts != registryFetchAttempts {
		t.Fatalf("attempts=%d want %d", attempts, registryFetchAttempts)
	}
	if len(registry.Plugins) != 0 {
		t.Fatalf("plugins=%d want 0", len(registry.Plugins))
	}
}

func TestFetchRegistryStopsRetryingOnCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	var attempts int
	client := Client{HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		cancel()
		return nil, fmt.Errorf("request failed: EOF")
	})}
	if _, errFetch := client.FetchRegistry(ctx); errFetch == nil {
		t.Fatal("expected cancellation error")
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want 1", attempts)
	}
}

func TestFetchRegistryDoesNotRetryRateLimitErrors(t *testing.T) {
	t.Parallel()

	var attempts int
	client := Client{registryBackoff: func(context.Context, int) error { return nil }, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return nil, &RateLimitError{StatusCode: http.StatusTooManyRequests}
	})}
	_, errFetch := client.FetchRegistry(context.Background())
	var limit *RateLimitError
	if !errors.As(errFetch, &limit) {
		t.Fatalf("error=%v want rate limit", errFetch)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d want 1", attempts)
	}
}

type truncatedBody struct{}

func (truncatedBody) Read([]byte) (int, error) { return 0, fmt.Errorf("unexpected EOF") }
func (truncatedBody) Close() error             { return nil }

func TestFetchRegistryRetriesTruncatedBodies(t *testing.T) {
	t.Parallel()

	var attempts int
	client := Client{registryBackoff: func(context.Context, int) error { return nil }, HTTPClient: pluginIdentityDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: truncatedBody{}}, nil
		}
		return testHTTPResponse(req, 200, nil, `{"schema_version":1,"plugins":[]}`), nil
	})}
	if _, errFetch := client.FetchRegistry(context.Background()); errFetch != nil {
		t.Fatalf("fetch registry error=%v", errFetch)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d want 2", attempts)
	}
}

func TestFetchRegistryRetryBackoffHonorsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	backoffs := 0
	client := Client{registryBackoff: func(context.Context, int) error {
		backoffs++
		cancel()
		return context.Canceled
	}, HTTPClient: pluginIdentityDoerFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("request failed: EOF")
	})}
	if _, errFetch := client.FetchRegistry(ctx); errFetch == nil {
		t.Fatal("expected cancellation error")
	}
	if backoffs != 1 {
		t.Fatalf("backoffs=%d want 1", backoffs)
	}
}
