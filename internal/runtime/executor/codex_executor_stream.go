package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
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
	preserveNativeOutput := helps.IsNativeCodexRequest(req.Payload, opts)
	isGrokClient := grokbuild.IsGrokClientContext(ctx, opts.Headers)
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := e.resolveCodexModelIsCompat(auth, req, baseModel)
	originalTranslated, body, updatesChanged := translateCodexRequestPairWithUpdateIntent(from, to, baseModel, originalPayload, req.Payload, true, isCompat)

	body = helps.RestorePublicResponsesCompactionFields(body, req.Payload, baseURL, from.String())
	originalTranslated = helps.RestorePublicResponsesCompactionFields(originalTranslated, originalPayload, baseURL, from.String())

	body, err = helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequestForExecutor(e.cfg, e.Identifier(), baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
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
	body = normalizeCodexInstructions(body, preserveNativeOutput)
	if e.cfg == nil || e.cfg.DisableImageGeneration == config.DisableImageGenerationOff {
		body = ensureImageGenerationTool(body, baseModel, auth, helps.CodexPolicyHeaders(ctx, opts.Headers, auth, baseModel))
	}
	body = sanitizeOpenAIResponsesReasoningEncryptedContentWithCompat(ctx, "codex executor", body, isCompat)
	body = normalizeCodexParallelToolCalls(body, helps.CodexPolicyHeaders(ctx, opts.Headers, auth, baseModel))
	body = helps.NormalizeCodexToolSchemas(body)
	body, optimizeMultiAgentV2 := helps.OptimizeCodexMultiAgentV2RequestForAuth(ctx, opts.Headers, body, e.cfg, auth, isCompat)
	body, replayScope, errReplay := applyCodexReasoningReplayCacheRequired(ctx, from, req, opts, body)
	if errReplay != nil {
		return nil, errReplay
	}
	body, routeState := helps.PrepareCodexRouteState(auth, baseModel,
		xaiReasoningReplayIsolateSessionKey(ctx, codexReasoningReplaySessionKey(ctx, from, req, opts, body)), body)
	reporter.SetTranslatedReasoningEffort(body, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, upstreamBody, identityState, err := e.cacheHelperForAuth(ctx, from, url, auth, req, originalPayload, body, opts.Headers)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg, opts.Headers)
	applyCodexRoutingHint(ctx, httpReq.Header, auth, baseModel, upstreamBody, opts.Headers)
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
		ProviderName: helps.RequestLogProviderName(auth),
		AuthID:       authID,
		AuthLabel:    authLabel,
		AuthType:     authType,
		AuthValue:    authValue,
	}
	helps.RecordAPIRequest(ctx, e.cfg, requestLog)

	httpClient := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	signatureRepairUsed := opts.ExecutionLifecycle != nil
	recordSignatureRetry := helps.ResponsesSignatureRetryRecorder(ctx, e.cfg, requestLog, func(status int, rejection []byte) error {
		signatureRepairUsed = true
		return clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, status, rejection)
	})
	attemptCtx, firstOutput := helps.StartResponsesFirstOutputWatch(ctx, auth)
	httpReq = httpReq.WithContext(attemptCtx)
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
		if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
			return nil, errClearReplay
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = helps.ResponsesChannelCapacityError(baseURL, baseModel, httpResp.StatusCode, data, newCodexStatusErrWithCooling(httpResp.StatusCode, data, e.modelLevelCooling()))
		return nil, err
	}

	buffering := e.cfg != nil && e.cfg.Codex.StreamBootstrapBuffering
	var bootstrapTimeout time.Duration
	var bootstrapStart time.Time
	if buffering {
		bootstrapTimeout = e.cfg.Codex.StreamBootstrapTimeoutDuration()
		bootstrapStart = nowCodexBootstrap()
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, 52_428_800) // 50MB
	claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
	var param any
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte

	var bufferedChunks [][]byte
	bufferedFrames := 0
	bufferedBytes := 0
	var initialChunks [][]byte
	streamStarted := false
	immediateTerminal := false
	var bootstrapTerminalErr error

	closeBootstrapBody := func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}

	meaningfulOutput := false
	sawOutputDelta := false
	compactionContract := helps.NewResponsesCompactionStream(body)
	if buffering {
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			var translatedLine []byte
			isHandshake := false
			terminalSuccess := false

			if transformed, ok := grokbuild.TransformKeepaliveSSELine(line, isGrokClient); ok {
				translatedLine = bytes.Clone(transformed)
				isHandshake = true
			} else if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				data = helps.RestoreCodexMultiAgentV2Response(data, optimizeMultiAgentV2)
				observeCodexTokenEvent(reporter, data)
				translatedLine = append([]byte("data: "), data...)
				eventType := gjson.GetBytes(data, "type").String()
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					closeBootstrapBody()
					if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, errClearReplay)
						reporter.PublishFailure(ctx, errClearReplay)
						return nil, errClearReplay
					}
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					if isCodexOverloadBootstrapFailure(terminalBody) {
						timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
						timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
						if !timeoutReached {
							helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d buffered lines, failing over", bufferedFrames)
							return nil, newCodexBootstrapOverloadErr(terminalBody)
						}
						helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap overload rejection after %d lines / %v, time budget exhausted; delivering in-stream", bufferedFrames, timeSinceStart)
					}
					bootstrapTerminalErr = helps.ResponsesChannelCapacityError(baseURL, baseModel, streamErr.StatusCode(), terminalBody, streamErr)
					if fallback, ok := bootstrapTerminalErr.(interface{ IsCredentialFallback() bool }); ok && fallback.IsCredentialFallback() {
						// Preserve attempt-local classification at the manager boundary.
						return nil, bootstrapTerminalErr
					}
					break
				}
				if helps.HasMeaningfulCodexOutputDelta(data) {
					sawOutputDelta = true
					meaningfulOutput = true
				}
				if helps.IsCodexTerminalEmptyIncomplete(data, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
					closeBootstrapBody()
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					bootstrapTerminalErr = streamErr
					break
				}
				if isCodexBootstrapBufferableEvent(eventType, data) {
					isHandshake = true
				}
				if errFirstOutput := firstOutput.Observe(eventType, data); errFirstOutput != nil {
					closeBootstrapBody()
					helps.RecordAPIResponseError(ctx, e.cfg, errFirstOutput)
					reporter.PublishFailure(ctx, errFirstOutput)
					return nil, errFirstOutput
				}
				if errContract := compactionContract.Observe(data); errContract != nil {
					closeBootstrapBody()
					failure := statusErr{code: http.StatusBadGateway, msg: errContract.Error()}
					helps.RecordAPIResponseError(ctx, e.cfg, failure)
					reporter.PublishFailure(ctx, failure)
					bootstrapTerminalErr = failure
					break
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete", "response.done":
					terminalSuccess = true
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					} else {
						reporter.EnsurePublished(ctx)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					if !preserveNativeOutput {
						data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					}
					if eventType == "response.completed" || eventType == "response.done" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
						routeState.Completed(ctx, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			} else {
				translatedLine = bytes.Clone(line)
				isHandshake = true
			}

			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			if isHandshake && !terminalSuccess {
				frameBytes := len(line)
				for i := range chunks {
					frameBytes += len(chunks[i])
				}
				timeSinceStart := nowCodexBootstrap().Sub(bootstrapStart)
				timeoutReached := bootstrapTimeout > 0 && timeSinceStart >= bootstrapTimeout
				if !timeoutReached && bufferedFrames < codexBootstrapMaxBufferedFrames && bufferedBytes+frameBytes <= codexBootstrapMaxBufferedBytes {
					bufferedFrames++
					bufferedBytes += frameBytes
					bufferedChunks = append(bufferedChunks, chunks...)
					continue
				}
				exhausted := "frame budget"
				if timeoutReached {
					exhausted = "time budget"
				} else if bufferedFrames < codexBootstrapMaxBufferedFrames {
					exhausted = "byte budget"
				}
				helps.LogWithRequestID(ctx).Debugf("codex executor: bootstrap %s exhausted after %d lines / %d bytes / %v, releasing stream without overload probing", exhausted, bufferedFrames, bufferedBytes, timeSinceStart)
			}

			initialChunks = chunks
			streamStarted = true
			if terminalSuccess {
				immediateTerminal = true
			}
			break
		}

		if !streamStarted && bootstrapTerminalErr == nil {
			closeBootstrapBody()
			if errScan := scanner.Err(); errScan != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				return nil, errScan
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if len(bufferedChunks) == 0 && len(initialChunks) == 0 {
				emptyErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before first payload"}
				helps.RecordAPIResponseError(ctx, e.cfg, emptyErr)
				reporter.PublishFailure(ctx, emptyErr)
				closedCh := make(chan cliproxyexecutor.StreamChunk)
				close(closedCh)
				return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: closedCh}, nil
			}
			streamErr := newCodexIncompleteStreamError()
			streamErr.beforeOutput = !meaningfulOutput && opts.ExecutionLifecycle == nil
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			reporter.PublishFailure(ctx, streamErr)
			return nil, streamErr
		}
	}

	chanCapacity := len(bufferedChunks) + len(initialChunks)
	if bootstrapTerminalErr != nil {
		chanCapacity++
	}
	out := make(chan cliproxyexecutor.StreamChunk, chanCapacity)
	for _, chunk := range bufferedChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	for _, chunk := range initialChunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	if bootstrapTerminalErr != nil {
		out <- cliproxyexecutor.StreamChunk{Err: bootstrapTerminalErr}
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}
	if immediateTerminal {
		closeBootstrapBody()
		close(out)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}

	emittedCount := 0
	for _, chunk := range bufferedChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	for _, chunk := range initialChunks {
		if len(chunk) > 0 {
			emittedCount++
		}
	}
	watchHandedOff = true
	var remaining io.Reader = httpResp.Body
	if buffering {
		remaining = &helps.ScannerLineReader{Scanner: scanner}
	}
	var bootstrap helps.ResponsesStreamBootstrap
	reader := helps.NewCodexResponsesSSEReader(ctx, remaining, 52_428_800, httpReq.URL.String(), upstreamBody)
	responseHeaders := httpResp.Header.Clone()
	go func() {
		defer close(out)
		defer firstOutput.Close()
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		for {
			frame, frameErr := reader.Next()
			if frameErr != nil {
				if frameErr != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, frameErr)
				}
				break
			}
			frame.Raw = applyCodexIdentityExposeResponsePayload(frame.Raw, identityState)
			frame.Data = applyCodexIdentityExposeResponsePayload(frame.Data, identityState)
			line := frame.Raw
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			var translatedLine []byte
			terminalSuccess := false
			bootstrapEvent := ""
			var bootstrapData []byte

			if isGrokClient && (frame.Event == "keepalive" || gjson.GetBytes(frame.Data, "type").String() == "keepalive") {
				translatedLine = grokbuild.KeepaliveSSEComment()
			} else if len(frame.Data) == 0 {
				translatedLine = bytes.Clone(line)
			} else {
				data := bytes.TrimSpace(frame.Data)
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
				observeCodexTokenEvent(reporter, data)
				translatedLine = append(append([]byte("data: "), data...), '\n', '\n')
				eventType := gjson.GetBytes(data, "type").String()
				bootstrapEvent = eventType
				bootstrapData = data
				if opts.ExecutionLifecycle != nil {
					bootstrapEvent = "managed_execution"
				}
				if streamErr, terminalBody, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					if !meaningfulOutput && !signatureRepairUsed {
						if repaired, canRetry := helps.PortableResponsesSignatureRetry(upstreamBody, terminalBody, httpReq.URL.String()); canRetry {
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
								reader = helps.NewCodexResponsesSSEReader(ctx, next.Body, 52_428_800, retry.URL.String(), repaired)
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
					streamErr := errFirstOutput
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				if errContract := compactionContract.Observe(data); errContract != nil {
					failure := statusErr{code: http.StatusBadGateway, msg: errContract.Error()}
					helps.RecordAPIResponseError(ctx, e.cfg, failure)
					reporter.PublishFailure(ctx, failure)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: failure}:
					case <-ctx.Done():
					}
					return
				}
				if helps.HasMeaningfulCodexOutputDelta(data) {
					sawOutputDelta = true
					meaningfulOutput = true
				}
				if helps.IsCodexTerminalEmptyIncomplete(data, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
					streamErr := newCodexEmptyIncompleteStreamError()
					helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed", "response.incomplete", "response.done":
					terminalSuccess = true
					data = normalizeCodexWebsocketCompletion(data)
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					} else {
						reporter.EnsurePublished(ctx)
					}
					publishCodexImageToolUsage(ctx, reporter, body, data)
					if !preserveNativeOutput {
						data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
					}
					if eventType == "response.completed" || eventType == "response.done" {
						cacheCodexReasoningReplayFromCompleted(replayScope, data)
						routeState.Completed(ctx, data)
					}
					translatedLine = append([]byte("data: "), data...)
				}
			}

			if preserveNativeOutput && len(frame.Raw) > 0 && len(frame.Data) > 0 && !(isGrokClient && (frame.Event == "keepalive" || gjson.GetBytes(frame.Data, "type").String() == "keepalive")) {
				translatedLine = bytes.Clone(frame.Raw)
			}

			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, body, translatedLine, &param, claudeInputTokens)
			chunks = bootstrap.Push(bootstrapEvent, bootstrapData, chunks)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
					if len(chunks[i]) > 0 {
						emittedCount++
					}
				case <-ctx.Done():
					return
				}
			}
			if terminalSuccess {
				return
			}
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
		streamErr.beforeOutput = !meaningfulOutput && opts.ExecutionLifecycle == nil
		helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: responseHeaders, Chunks: out}, nil
}
