# Native Embeddings and Rerank

CPA exposes `POST /v1/embeddings` and `POST /v1/rerank` through ordinary client
API-key authentication. No forwarding plugin, management-key gateway, or second
credential pool is required. Configure these models under
`openai-compatibility`; existing chat, Responses, image, and media routes keep
their current behavior.

## Configuration

```yaml
openai-compatibility:
  - name: retrieval
    base-url: https://provider.example/v1
    api-key-entries:
      - api-key: UPSTREAM_KEY
        # proxy-url: direct
    models:
      - name: vector-v1
        alias: vector
        type: embeddings
      - name: rank-v1
        alias: rank
        type: rerank
        # upstream-path: /custom-rank
```

- `type` is `embeddings` or `rerank`. Omit it for existing chat/image models.
  An unknown type, `type` combined with `image: true`, or `upstream-path` without
  a retrieval type is a configuration error.
- `upstream-path` is optional. It is **appended** to `base-url`: the example
  calls `https://provider.example/v1/embeddings` and
  `https://provider.example/v1/rerank`. `/custom-rank` would call
  `https://provider.example/v1/custom-rank`, not replace `/v1`.
- Paths stay on the configured origin and base path; absolute URLs, query
  strings, fragments, backslashes, and dot-segment traversal are rejected.
- Provider headers, per-key proxies, priority/weights, prefixes, cooldowns, and
  retry limits keep their existing CPA meaning. A base-URL-only provider may
  omit keys for a local upstream that needs no authentication.
- Model type/path changes participate in config reload hashing. Retrieval
  models remain available through `/v1/models`, but are hidden from Codex's
  chat model selector and excluded from automatic chat model selection.
  Retrieval requests use an explicit configured model/alias, not chat `auto`.

## Requests and responses

Use a **CPA client key**, not the management key or upstream key:

```sh
curl http://127.0.0.1:8317/v1/embeddings \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"vector","input":["first text","second text"]}'

curl http://127.0.0.1:8317/v1/rerank \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"rank","query":"question","documents":["document A","document B"],"top_n":1}'
```

Embeddings use the OpenAI envelope: text, a text batch, token IDs, or a batch of
nonnegative token-ID arrays; optional `dimensions` and `encoding_format`
(`float` or `base64`). Rerank uses the Cohere-compatible envelope: `query`,
`documents` (strings or document objects), and optional positive `top_n`.
Both endpoints are non-streaming.

The executor replaces the routed model name, applies configured payload rules,
and preserves other fields. Payload-rule protocol selectors are
`openai-embeddings` and `cohere-rerank`; rules can also match the request path.
No chat translation or thinking injection runs. Valid upstream JSON is returned
unchanged, including vector precision, result ordering, documents, extension
fields, and provider-specific usage units.

Malformed JSON, empty success bodies, chat/error envelopes disguised as a
success, incomplete embedding batches, duplicate/out-of-range indexes, invalid
vectors (including non-finite float32 values inside base64), and inconsistent
dimensions produce `502`, not a successful probe.
Upstream non-2xx responses retain the shared CPA error handling. Allowed response
headers follow `passthrough-headers`; upstream `Retry-After` also informs the
shared credential scheduler.

## Routing and accounting

Production calls use CPA's existing credential selection and failover. Multiple
keys/providers may serve the **same upstream embedding model** under one alias.
An embedding alias that maps to different upstream model names is rejected
before inference: silently mixing vector spaces would corrupt a retrieval
index. Give different embedding models distinct aliases. Names are only a
configuration check; administrators still need to ensure identically named
upstream deployments actually use the same vector space.

An alias mixing endpoint types is also rejected, including an alias shared
with an OAuth chat model. Rerank may use normal alias
pools; unlike embeddings, its scores are consumed within a single response.
Client cancellation reaches the upstream. This path adds no response or idle
network timeout.

Request logs and usage retain the client alias and selected credential.
OpenAI `usage` and Cohere `meta.tokens` count real tokens. Cohere `search_units`
stays in the returned response and is not mislabeled as tokens.

Native retrieval currently requires local OpenAI-compatible routing. CPA Home
routing has no retrieval model-capability contract, so it returns an explicit
request error rather than silently passing retrieval through chat handling.

## Management UI and selected-key probes

In **AI Providers -> OpenAI Compatible -> Custom models**, expand a model and
choose **Endpoint type**. Set its optional **Upstream path** there. Retrieval
models hide chat-only thinking/image controls; choosing the default type brings
those controls back.

A provider test uses the first key by default, or the key selected on its row.
Only **Test all** tests the entire list. Retrieval tests use
`POST /v0/management/provider-connectivity-test` with
`provider: openai-compatibility`, the requested `model`, one `api_key` or
`auth_index`, and an `openai_config` draft. They reuse production model
resolution, synthesis, payload rules, executor, and response validation.

Probes address the public alias, including its provider prefix, so alias-specific
payload rules also match when the UI model selector displays the upstream name.
Save and test share their builder/serializer. Unsaved type, path, alias, prefix,
headers, and explicit clears apply immediately to probes. Draft key pools are
ignored; an error never rotates to another key. The saved configuration and
production scheduler are left unchanged. Saving an existing provider preserves
the latest server-managed fields read immediately before saving, rather than
replaying stale hidden options from the open form. A settings change or UI unmount
cancels the probe; there is no separate whole-response browser deadline.
Existing non-retrieval Chat and Codex probe transports remain unchanged.

## Verification

The regression seams cover public authenticated routes, real config synthesis,
scheduling/failover, request/probe parity, config load/save/reload, usage, and
Codex catalog visibility. Run:

```sh
go test ./... -count=1 -timeout=180s
go test -race ./internal/api ./internal/api/handlers/management \
  ./internal/runtime/executor ./sdk/cliproxy/auth -run Retrieval -count=3 -timeout=180s
go build -o /tmp/cpa-retrieval-server ./cmd/server
python3 test/retrieval_live_smoke.py --binary /tmp/cpa-retrieval-server \
  --panel static/management.html
node test/provider_usage_match_test.mjs
```

The binary smoke test starts its own temporary CPA and local upstream with
synthetic keys. It checks successful public calls, API authentication, alias
translation, exactly-one-key draft probes, invalid successes, rate limits,
configuration isolation, and the exact served panel. `--serve --state FILE`
keeps that temporary pair running for browser QA; Ctrl+C cleans it up.

Local fixtures do not certify a paid upstream's model support or availability.
Live site acceptance should use one selected key, not a sweep of credentials.

The plugin reference was `KorenKrita/cliproxy-embeddings-rerank-forward` at
`36a44f5`: protocol preservation and path behavior informed this implementation.
Its independent credentials, cooldowns, and management-key gateway were not
adopted.
