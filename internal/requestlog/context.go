package requestlog

import (
	"context"
	"sync/atomic"
)

type scopeKey struct{}
type requestKey struct{}
type requestScope struct {
	started atomic.Bool
	store   *Store
	info    Scope
}

func WithScope(ctx context.Context, store *Store, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeKey{}, &requestScope{store: store, info: scope})
}

// Start owns a new call unless a surrounding internal execution already owns it.
func Start(ctx context.Context, id, model string) (context.Context, *Request) {
	if ctx == nil || FromContext(ctx) != nil {
		return ctx, nil
	}
	scope, ok := ctx.Value(scopeKey{}).(*requestScope)
	if !ok || scope.store == nil {
		return ctx, nil
	}
	scope.started.Store(true)
	r := scope.store.Begin(Record{ID: id, TraceID: scope.info.TraceID, URL: scope.info.URL, Method: scope.info.Method, IP: scope.info.IP, Model: model})
	if r == nil {
		return ctx, nil
	}
	return context.WithValue(ctx, requestKey{}, r), r
}

func FromContext(ctx context.Context) *Request {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(requestKey{}).(*Request)
	return r
}

// WithAttempt isolates callbacks from a selected executor attempt. A same-key
// retry inside that executor advances only this handle, not earlier executions.
func WithAttempt(ctx context.Context) context.Context {
	r := FromContext(ctx)
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, requestKey{}, &Request{store: r.store, id: r.id})
}

// InheritScope preserves caller cancellation while adding only request observation metadata.
func InheritScope(parent, source context.Context) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	if source == nil || parent.Value(scopeKey{}) != nil {
		return parent
	}
	if scope, ok := source.Value(scopeKey{}).(*requestScope); ok {
		return context.WithValue(parent, scopeKey{}, scope)
	}
	return parent
}

func HasCalls(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	scope, ok := ctx.Value(scopeKey{}).(*requestScope)
	return ok && scope.started.Load()
}
