package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if isCodexOpenAIImageRequest(opts) {
		return e.executeOpenAIImageStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated, body := translateCodexRequestPair(from, to, baseModel, originalPayload, req.Payload, true, helps.APIKeyModelIsCompat(req))

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	reasoningSummaryDelivery := gjson.GetBytes(body, "stream_options.reasoning_summary_delivery")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	if reasoningSummaryDelivery.Exists() {
		body, _ = sjson.SetBytes(body, "stream_options.reasoning_summary_delivery", reasoningSummaryDelivery.Value())
	}
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = normalizeCodexInstructions(body)
	if codexAuthDisablesImageGeneration(auth) || e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, opts.Headers)
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "codex executor", body)
	body = normalizeCodexParallelToolCalls(body, opts.Headers)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, baseModel)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return nil, errReplay
	}
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	var identityState codexIdentityConfuseState
	httpReq, upstreamBody, identityState, err := e.cacheHelper(ctx, from, url, auth, req, originalPayloadSource, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg, opts.Headers)
	applyModelHeaderOverrides(httpReq.Header, baseModel)
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	requestLog := helps.UpstreamRequestLog{
		URL:          url,
		Method:       http.MethodPost,
		Headers:      httpReq.Header.Clone(),
		Body:         upstreamBody,
		Provider:     e.Identifier(),
		AuthID:       authID,
		AuthLabel:    authLabel,
		ProviderName: helps.RequestLogProviderName(auth),
		AuthType:     authType,
		AuthValue:    authValue,
	}
	helps.RecordAPIRequest(ctx, e.cfg, requestLog)

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	signatureRepairUsed := opts.ExecutionLifecycle != nil
	recordSignatureRetry := helps.ResponsesSignatureRetryRecorder(ctx, e.cfg, requestLog, func(status int, rejection []byte) error {
		signatureRepairUsed = true
		return clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, status, rejection)
	})
	attemptCtx, firstOutput := helps.StartResponsesFirstOutputWatch(ctx, auth)
	httpReq = httpReq.WithContext(attemptCtx)
	// The stream goroutine owns the watch only after the response is accepted.
	watchHandedOff := false
	defer func() {
		if !watchHandedOff {
			firstOutput.Close()
		}
	}()
	var httpResp *http.Response
	if opts.ExecutionLifecycle != nil {
		httpResp, err = httpClient.Do(httpReq)
	} else {
		httpResp, err = helps.DoWithResponsesSignatureRecovery(httpClient, httpReq, recordSignatureRetry)
	}
	if err != nil {
		err = firstOutput.Failure(err)
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr = firstOutput.Failure(readErr); readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		data = applyCodexIdentityConfuseResponsePayload(data, identityState)
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = helps.ResponsesChannelCapacityError(baseURL, baseModel, httpResp.StatusCode, data, newCodexStatusErr(httpResp.StatusCode, data))
		return nil, err
	}
	responseHeaders := httpResp.Header.Clone()
	out := make(chan cliproxyexecutor.StreamChunk)
	watchHandedOff = true
	go func() {
		defer close(out)
		defer firstOutput.Close()
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		reader := helps.NewResponsesSSEReader(httpResp.Body, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		var readErr error
		var bootstrap helps.ResponsesStreamBootstrap
		for {
			frame, errFrame := reader.Next()
			if errFrame != nil {
				readErr = errFrame
				break
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, frame.Raw)
			translatedLine := bytes.Clone(frame.Raw)
			terminalSuccess := false
			eventType := ""

			if len(frame.Data) > 0 {
				data := applyCodexIdentityConfuseResponsePayload(bytes.TrimSpace(frame.Data), identityState)
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if !json.Valid(data) {
					break
				}
				var compact bytes.Buffer
				_ = json.Compact(&compact, data)
				data = compact.Bytes()
				if gjson.GetBytes(data, "type").String() == "" && frame.Event != "" {
					data, _ = sjson.SetBytes(data, "type", frame.Event)
				}
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				translatedLine = append(append([]byte("data: "), data...), '\n', '\n')
				eventType = gjson.GetBytes(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErr(data); ok {
					if !bootstrap.Committed() && !signatureRepairUsed {
						if repaired, canRetry := helps.PortableResponsesSignatureRetry(upstreamBody, terminalBody); canRetry {
							retry := helps.CloneResponsesRetryRequest(httpReq, repaired)
							closeHTTPResponseBody(httpResp, "codex executor: close rejected response body")
							errRetry := recordSignatureRetry(nil, terminalBody, retry, repaired)
							var next *http.Response
							if errRetry == nil {
								next, errRetry = httpClient.Do(retry)
							}
							if errRetry = firstOutput.Failure(errRetry); errRetry != nil {
								closeHTTPResponseBody(next, "codex executor: close timed-out retry response")
								helps.RecordAPIResponseError(ctx, e.cfg, errRetry)
								reporter.PublishFailure(ctx, errRetry)
								select {
								case out <- cliproxyexecutor.StreamChunk{Err: errRetry}:
								case <-ctx.Done():
								}
								return
							}
							httpResp = next
							helps.RecordAPIResponseMetadata(ctx, e.cfg, next.StatusCode, next.Header.Clone())
							if next.StatusCode >= 200 && next.StatusCode < 300 {
								reader = helps.NewResponsesSSEReader(next.Body, 52_428_800)
								bootstrap.Reset()
								param = nil
								claudeInputTokens = helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
								continue
							}
							rejected, errRead := io.ReadAll(next.Body)
							helps.AppendAPIResponseChunk(ctx, e.cfg, rejected)
							if errRead = firstOutput.Failure(errRead); errRead != nil {
								helps.RecordAPIResponseError(ctx, e.cfg, errRead)
								reporter.PublishFailure(ctx, errRead)
								select {
								case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
								case <-ctx.Done():
								}
								return
							}
							streamErr = newCodexStatusErr(next.StatusCode, rejected)
							terminalBody = rejected
						}
					}

					terminalStatus := streamErr.StatusCode()
					terminalErr := helps.ResponsesChannelCapacityError(baseURL, baseModel, terminalStatus, terminalBody, streamErr)
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, terminalStatus, terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						select {
						case out <- cliproxyexecutor.StreamChunk{Err: errClearReplay}:
						case <-ctx.Done():
						}
						return
					}
					helps.RecordAPIResponseError(ctx, e.cfg, terminalErr)
					reporter.PublishFailure(ctx, terminalErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: terminalErr}:
					case <-ctx.Done():
					}
					return
				}
				if errFirstOutput := firstOutput.Observe(eventType, data); errFirstOutput != nil {
					readErr = errFirstOutput
					break
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete":
					terminalSuccess = true
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					if eventType == "response.completed" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
					}
					translatedLine = append(append([]byte("data: "), data...), '\n', '\n')
				}
			}

			translatedLine = applyCodexIdentityExposeResponsePayload(translatedLine, identityState)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			bootstrapEvent := eventType
			if opts.ExecutionLifecycle != nil {
				bootstrapEvent = "managed_execution"
			}
			chunks = bootstrap.Push(bootstrapEvent, frame.Data, chunks)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
			if terminalSuccess {
				return
			}
		}
		if errScan := readErr; errScan != nil && errScan != io.EOF {
			if ctx.Err() != nil {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		}
		if errFirstOutput := firstOutput.Failure(nil); errFirstOutput != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errFirstOutput)
			reporter.PublishFailure(ctx, errFirstOutput)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errFirstOutput}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := newCodexIncompleteStreamError()
		if !bootstrap.Committed() && opts.ExecutionLifecycle == nil {
			// A relay EOF before output is an upstream fault, not invalid client input.
			// Let the existing bounded credential policy try the next available key.
			streamErr.beforeOutput = true
		}
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: responseHeaders, Chunks: out}, nil
}
