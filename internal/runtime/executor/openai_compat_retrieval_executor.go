package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

func (e *OpenAICompatExecutor) executeRetrieval(ctx context.Context, selected *auth.Auth, req ex.Request, opts ex.Options, kind string) (resp ex.Response, err error) {
	reporter := helps.NewExecutorUsageReporter(ctx, e, req.Model, selected)
	defer reporter.TrackFailure(ctx, &err)
	info, ok := auth.ResolvedAPIKeyModelInfo(req)
	if !ok || info.Type != kind || selected == nil {
		return resp, &auth.Error{Code: "request_scoped", HTTPStatus: 400, Message: "model is not configured for this retrieval endpoint"}
	}
	baseURL, _ := e.resolveCredentials(selected)
	endpoint, err := helps.RetrievalURL(baseURL, info.UpstreamPath, kind)
	if err != nil {
		return resp, statusErr{code: 400, msg: err.Error()}
	}
	body, err := sjson.SetBytes(req.Payload, "model", req.Model)
	if err != nil {
		return resp, err
	}
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, req.Model, opts.SourceFormat.String(), opts.SourceFormat.String(), "", body, opts.OriginalRequest, helps.PayloadRequestedModel(opts, req.Model), helps.PayloadRequestPath(opts), opts.Headers)
	// Routing owns model identity even when a payload rule changes other fields.
	body, err = sjson.SetBytes(body, "model", req.Model)
	if err != nil {
		return resp, err
	}
	if err = helps.ValidateRetrievalRequest(body, kind); err != nil {
		return resp, statusErr{code: 400, msg: err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "cli-proxy-openai-compat")
	if err = e.PrepareRequest(request, selected); err != nil {
		return resp, err
	}
	accountType, accountValue := selected.AccountInfo()
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: endpoint, Method: http.MethodPost, Headers: request.Header.Clone(), Body: body, Provider: e.Identifier(), AuthID: selected.ID, AuthLabel: selected.Label, AuthType: accountType, AuthValue: accountValue})
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, selected, 0))
	result, err := client.Do(request)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := result.Body.Close(); errClose != nil {
			log.Errorf("retrieval: close response: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, result.StatusCode, result.Header.Clone())
	output, err := io.ReadAll(result.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, output)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return resp, statusErrWithHeaders{statusErr: statusErr{code: result.StatusCode, msg: string(output), retryAfter: helps.RetrievalRetryAfter(result.Header, time.Now())}, headers: result.Header.Clone()}
	}
	if err = helps.ValidateRetrievalResponse(output, body, kind); err != nil {
		return resp, statusErr{code: 502, msg: err.Error()}
	}
	reporter.Publish(ctx, helps.ParseRetrievalUsage(output, kind))
	reporter.EnsurePublished(ctx)
	return ex.Response{Payload: output, Headers: result.Header.Clone()}, nil
}
