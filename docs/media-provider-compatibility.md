# Media provider compatibility

Image, video and audio operations share `MediaExecutor`. Provider connectivity
probes synthesize exactly one selected credential and call the same executor.
Routing and adapters live in `internal/mediarouting`; wire conversion and result
inspection live under `internal/runtime/executor/helps`.

## Routing and scheduling contracts

- Management creation uses `POST /v8/management/media-providers`; the legacy
  `/v0/management/media-providers` route remains available. Creation appends under
  the configuration lock and allocates fresh credential identities.
- Provider priority and per-key priority retain their existing meaning. Per-key
  `weight` is independent, survives edit/save/reload and participates in weighted
  round-robin. A zero-weight key is excluded before selecting a priority tier in
  that strategy, so it does not hide eligible keys in lower tiers.
- Model prefixes and aliases resolve to the upstream model before execution.
  An explicit operation `model` still overrides the requested model.
- Internal operation selection keys are not public catalog models. Cooldowns use
  the resolved upstream model, or the operation identity for model-free APIs;
  throttling transcription does not automatically block speech on another model.
- Base URL and operation paths merge overlapping segments and preserve their
  query parameters. Operation query values override conflicting base values.

## Request and probe parity

A per-key test sends the save-shaped provider draft, without its credential pool,
plus one selected credential and its explicit proxy override. It shares model
resolution, path construction, authentication, adapter conversion, polling and
result inspection with production. The test does not save the draft, scan other
keys, or rotate credentials after failure. No provider test runs automatically.

**Test CPA API** instead calls the public gateway using its saved routing and the
selected operation/model. It is a real request, not a guarantee that an unsaved
provider draft has been installed. Both kinds of test can consume upstream quota.

`test-request.json` must contain valid JSON and match `request-format: json`.
For multipart tests, each line is `field=value`; commas and additional equals
signs belong to the value. Changing request format clears incompatible test
payloads. Caller cancellation is retained; there is no whole-response deadline.

## Completion and accepted work

HTTP 200 alone is not a successful probe. The executor inspects the result and
records `completed`, `pending` or `unverified` in response metadata. The public
`X-CPA-Media-Result` header comes from that metadata, independently of upstream
header passthrough. Upstream and plugin headers cannot spoof this CPA-owned field.
Generic media routes also filter cookies, hop-by-hop and reserved response headers.

A usable result can be recognized media bytes, a result URL, completed transcript,
or a voice identity returned by cloning. An empty optional `b64_json` next to a
valid URL is allowed. A task ID alongside a completed result is provenance, not
by itself a pending receipt. Explicit pending/failure states remain authoritative
even when response normalization extracts only a URL or base64 field. The original status envelope
and the delivered output are inspected together, so a preview image does not
turn a processing task into a successful probe. Malformed nonempty base64 and
empty output do not certify completion. Unknown custom response shapes
can still pass through, but receive `unverified` instead of a green probe. URL
recognition is not a download or a guarantee that the remote asset remains valid.

After a successful upstream HTTP response, failures while reading its body,
polling or validating results are request-stopping errors. They retain an available
task ID and never create a second task on another key or another retry round.
Configured retry actions and Home redispatch respect that boundary. This also
keeps polling errors from cooling otherwise healthy local credentials. Before an
upstream acceptance response, the existing request retry policy still applies.

## Protocol adapters

`adapter` accepts `auto` (also the empty default), `passthrough`,
`cloudflare-workers-ai`, or `mimo-audio`. Automatic selection uses only the exact
first-party hosts `api.cloudflare.com` for images and `api.xiaomimimo.com` for audio.
Custom relay hosts stay passthrough unless an adapter is selected explicitly.

### Cloudflare Workers AI

- FLUX Schnell maps `num_steps` to `steps` and validates its fixed-size/single-image
  contract rather than forwarding unsupported generation fields.
- FLUX.2 Klein converts JSON generation input to multipart, and `size` to
  `width`/`height`. Its fixed step count is four.
- Klein edits map OpenAI `image` / `image[]` files to `input_image_0` through
  `input_image_3`, or retain those native names. Up to four references are accepted.
  Unsupported masks, duplicate slots and missing references fail before submission.
- Put the model placeholder in the operation path when required by Workers AI;
  the executor keeps URL-addressed models out of the request body.

Reference: [Cloudflare Klein model announcement](https://developers.cloudflare.com/changelog/2026-01-15-flux-2-klein-4b-workers-ai/).

### MiMo audio

Standard speech `input`, `voice`, `instructions` and `response_format` are translated
to native `messages` / `audio`. Standard callers receive audio bytes; existing
native message requests retain their configured response shape. TTS responses must
have `finish_reason: stop`. A truncated response is not a successful audio result.

Transcription accepts a single WAV/MP3 multipart `file`, optional `language`
(`auto`, `zh`, `en`) and `response_format` (`json`, `text`). Encoded input is limited
to 10 MiB by the upstream contract. Native ASR JSON remains supported.

For a provider already using the MiMo `/v1` base URL, merge the following entries
into its existing `models` and `operations`; keep its credentials and other entries:

```yaml
models:
  - name: mimo-v2.5-asr
    alias: mimo-asr
    capabilities: [transcribe]
operations:
  - name: transcribe
    capability: transcribe
    method: POST
    path: /chat/completions
    request-format: multipart
    model-mode: required
    response-format: passthrough
    test-request:
      multipart-fields:
        language: en
```

The management test supplies a synthetic WAV file. This template is not a balance
or model-access check; a credential with ASR access and quota is still required.

References: [MiMo speech API](https://mimo.mi.com/static/docs/api/audio/tts.md),
[MiMo speech recognition API](https://mimo.mi.com/static/docs/api/audio/Speech-Recognition.md).

## Regression checks

```sh
go test ./internal/api ./internal/api/handlers/management ./internal/runtime/executor \
  ./internal/watcher/synthesizer ./sdk/api/handlers ./sdk/cliproxy/auth \
  -run 'Media|SpoofedMediaResult' -count=1
go test ./...
go build ./cmd/server
node test/provider_usage_match_test.mjs
```

In the separate management UI repository, run `bun run verify` and exercise
create/edit, priority/weight persistence, selected-key draft proxy, invalid/pending
results and request-format changes in a browser. Live acceptance pins one key per
site, checks the real public route and inspects actual media artifacts. Missing
quota or channels must be reported separately from CPA compatibility results.
