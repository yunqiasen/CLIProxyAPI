package helps_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/requestlog"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

func TestRequestObservationPreservesValidIncompleteResponse(t *testing.T) {
	source := requestlog.NewStore(4)
	ctx := requestlog.WithScope(context.Background(), source, requestlog.Scope{TraceID: "trace"})
	ctx, owner := requestlog.Start(ctx, "incomplete", "alias")
	cfg := &config.Config{}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{Provider: "codex", AuthID: "key-ref", Body: []byte(`{"model":"upstream"}`)})
	helps.RecordAPIResponseMetadata(ctx, cfg, 200, nil)
	helps.AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`))
	owner.Finish(requestlog.Succeeded, 200, nil)
	result, _ := source.Get("incomplete")
	if result.State != requestlog.Succeeded || result.ErrorKind != "" {
		t.Fatalf("observation changed a valid incomplete response outcome: %+v", result)
	}
}

func TestRequestObservationClassifiesExplicitStreamErrorWithoutSavingMessage(t *testing.T) {
	source := requestlog.NewStore(4)
	ctx := requestlog.WithScope(context.Background(), source, requestlog.Scope{TraceID: "trace"})
	ctx, owner := requestlog.Start(ctx, "error", "alias")
	cfg := &config.Config{}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{Provider: "codex", AuthID: "key-ref"})
	helps.RecordAPIResponseMetadata(ctx, cfg, 200, nil)
	helps.AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"private fixture content"}}}`))
	owner.Finish(requestlog.Succeeded, 200, nil)
	result, _ := source.Get("error")
	if result.State != requestlog.Failed || result.ErrorKind != "rate_limit" {
		t.Fatalf("explicit stream error lost: %+v", result)
	}
}
