# Media Provider Foundation and Image Provider Design

## Goal

Add dedicated Image, Video, and Audio provider management entries while keeping their day-to-day configuration almost identical to the existing OpenAI-compatible provider workflow. Build one shared media-provider foundation and activate all three media kinds through the same tested runtime and UI.

## Confirmed Baseline

CLIProxyAPI v7.2.100 already supports OpenAI-compatible image generation and editing through:

- `POST /v1/images/generations`
- `POST /v1/images/edits`
- `openai-compatibility[].models[].image: true`
- JSON and multipart forwarding
- existing credential retry, cooldown, and provider usage accounting

A live local probe also confirmed that an `img-lite` HuggingFace model can be configured as an OpenAI-compatible image model and called through CPA successfully. This baseline must be reused rather than rebuilt.

Current gaps are:

- no dedicated Image, Video, or Audio provider categories in the management UI;
- no shared configuration for non-standard operations such as upscale, super-resolution, background removal, watermark removal, music generation, cloning, or voice conversion;
- no operation-level way to declare that a request does not use a model;
- no generic asynchronous submit-and-poll contract for providers such as ModelScope;
- no generic video/audio provider path comparable to OpenAI-compatible image forwarding.

## Scope Decomposition

The delivery has four independently verifiable layers:

1. **Shared media foundation**: config, management API, scheduler integration, logging, retry/cooldown, custom operations, response normalization, and async polling.
2. **Image providers**: standard image generation/edit forwarding plus upscale, super-resolution, background removal, and custom image operations.
3. **Video providers**: text-to-video, image-to-video, watermark removal, and other configured synchronous or asynchronous operations.
4. **Audio providers**: speech/audio generation, music generation, voice cloning, voice conversion, and binary responses.

All three kinds use the same contracts and are active in the current management UI. Vendor-specific protocol adapters remain separate follow-up work when a channel needs more than HTTP body passthrough and explicit operation mapping.

## Chosen Architecture

### Shared backend section

Introduce one optional top-level section:

```yaml
media-providers:
  - name: "image-relay"
    kind: "image"
    base-url: "https://image.example/v1"
    priority: 10
    disabled: false
    disable-cooling: false
    api-key-entries:
      - api-key: "sk-a"
        priority: 20
        proxy-url: ""
      - api-key: "sk-b"
        priority: 10
        proxy-url: ""
    headers:
      X-Custom-Header: "value"
    models:
      - name: "upstream-image-model"
        alias: "public-image-model"
        capabilities: ["generate", "edit"]
    operations:
      - name: "remove-background"
        capability: "remove-background"
        method: "POST"
        path: "/images/remove-background"
        request-format: "multipart"
        model-mode: "none"
        response-format: "json-url"
        result-path: "data.0.url"
```

`kind` accepts `image`, `video`, or `audio`. All three values are active in the runtime, management API, model catalog, request logs, and management UI.

### Reused provider semantics

Media providers reuse the established provider behavior:

- provider name;
- Base URL;
- multiple API keys;
- provider and per-key priority;
- per-key proxy URL;
- headers;
- prefix;
- disabled state;
- cooldown override;
- model aliases;
- connection testing;
- request-log provider identity;
- success/failure statistics.

The existing `openai-compatibility` section remains unchanged. Existing image providers configured there keep working and stay visible under OpenAI Compatible until explicitly recreated under Image Providers.

### Media models

A media model contains:

- `name`: upstream model name;
- `alias`: client-facing model name;
- `display-name`: optional catalog name;
- `force-mapping`: optional response model rewrite;
- `capabilities`: operation names supported by the model.

Image capabilities in the first delivery are:

- `generate`
- `edit`
- `upscale`
- `super-resolution`
- `remove-background`

A model with `generate` or `edit` is registered as an OpenAI image model so the existing `/v1/images/generations` and `/v1/images/edits` handlers and routing continue to work.

### Operations without models

A media operation is a routable capability independent of a model. It contains:

- `name`: stable public operation slug;
- `capability`: semantic capability;
- `method`: upstream HTTP method;
- `path`: upstream path relative to Base URL;
- `request-format`: `json`, `multipart`, or `binary`;
- `model-mode`: `required`, `optional`, or `none`;
- `model`: optional fixed upstream model;
- `response-format`: `passthrough`, `json-url`, `json-base64`, or `binary`;
- `result-path`: dot path used by JSON result extraction;
- optional asynchronous polling settings.

The public generic route is:

```text
ANY /v1/media/:kind/:operation
```

For Image providers, convenience aliases are also exposed:

```text
POST /v1/images/upscale
POST /v1/images/super-resolution
POST /v1/images/background/remove
```

Clients do not send a fake model for `model-mode: none`. Internally, CPA registers a hidden operation-routing identity so the existing auth scheduler can still provide key priority, retry, cooldown, and failover. Hidden operation identities are excluded from `/v1/models`.

### Request forwarding

The media executor performs these steps:

1. Resolve provider and key through the existing auth manager.
2. Resolve the operation from the request path and selected provider config.
3. Preserve the incoming body for matching request formats.
4. Rewrite the model only when the operation declares a fixed or selected upstream model.
5. Join Base URL and operation path without duplicating slashes.
6. Apply provider headers, API-key authorization, and per-key proxy.
7. Record upstream request and response metadata through existing request logging.
8. Return successful JSON or binary content with upstream content type.
9. Convert non-2xx responses into retry-aware executor errors so another eligible key/provider can be selected.

No arbitrary code or expression templates are added. Field-level vendor transformations are implemented as explicit tested adapter profiles when a provider protocol requires them.

### Async submit and poll

An operation may declare:

```yaml
async:
  task-id-path: "task_id"
  poll-method: "GET"
  poll-path: "/v1/tasks/{task_id}"
  status-path: "task_status"
  success-values: ["SUCCEED", "succeeded", "completed"]
  failure-values: ["FAILED", "failed", "cancelled"]
  result-path: "output_images.0"
  poll-interval: "3s"
```

After a successful submit response, CPA extracts the task ID and polls using the same selected credential. Polling ends when the request context is cancelled, a success state is reached, or a configured failure state is reached. No fixed overall network timeout is introduced after the upstream connection is established.

### Provider protocol coverage

The shared runtime supports three protocol shapes without adding a separate profile field:

1. OpenAI-style image behavior through `/images/generations` and `/images/edits`. This covers img-lite and compatible relays.
2. Configured HTTP operations for image, video, or audio endpoints whose paths and body types differ.
3. Submit-and-poll HTTP operations for asynchronous media channels such as ModelScope-style task APIs.

Provider-specific field mapping and Gradio queue transport stay in explicit, tested adapters rather than unvalidated expression templates. Such adapters can be added after a concrete channel contract is available.

## Management API

Add CRUD endpoints under the existing protected management group:

```text
GET    /v0/management/media-providers
PUT    /v0/management/media-providers
PATCH  /v0/management/media-providers
DELETE /v0/management/media-providers
```

Responses include runtime `auth-index` values for each key, matching the existing OpenAI-compatible provider behavior. Persisted YAML never stores `auth-index`.

Normalization rules:

- trim names, kinds, paths, aliases, and Base URLs;
- reject entries without name, valid kind, or Base URL;
- remove blank key entries, model entries, and operations;
- normalize header names and values through existing header normalization;
- normalize operation methods to uppercase;
- reject duplicate operation names within one provider;
- reject unsupported request, model, or response modes;
- retain original list order.

### Stable credential identity and usage ownership

Each provider without keys and each individual key entry has an internal `auth-id`. The field is persisted in YAML as `auth-id`, omitted from management JSON, and projected to the UI only through the existing opaque `auth-index`. Users do not configure or edit this value.

On the first management edit, an entry without a persisted `auth-id` receives the same deterministic identity used by the pre-migration runtime. Later edits preserve that identity when the provider name, Base URL, key value, proxy, model list, or operation list changes. Adding a key creates a new identity only for the new slot; deleting a key or provider does not make its identity reusable by another entry. This keeps success/failure totals attached to the logical credential instead of resetting after ordinary edits.

Historical request-log fallback is deliberately narrower than credential identity. A record may be remapped only when its normalized protocol exactly matches the current credential protocol and the existing unique Base URL rules also match. Media kinds therefore do not borrow history from another kind or from a deleted provider merely because the Base URL is shared. Exact `auth-id` history remains authoritative.

## Management UI

Add three visible provider entries:

- Image Providers
- Video Providers
- Audio Providers

All three use the existing provider workbench and resource table. They are not separate pages with copied CRUD logic.

The form keeps the OpenAI-compatible layout:

1. provider name;
2. Base URL;
3. provider priority and disabled/cooling controls;
4. multiple API keys with per-key priority and proxy;
5. headers;
6. models and aliases;
7. connection test;
8. media operation section.

Image-specific adjustments:

- model rows show capability checkboxes instead of the single `image` checkbox;
- models are optional when at least one no-model operation exists;
- operation cards expose method, path, request format, model mode, response format, and result path;
- async settings stay collapsed unless enabled;
- the Image connectivity test sends a minimal generation request or the selected operation test payload through the management `api-call` endpoint;
- list cards retain existing success/failure totals and request-log hover details using the media provider name.

Video and Audio entries use the same active resource shell, CRUD flow, multi-key editor, model capability editor, operation editor, and connectivity test as Image Providers. Empty categories display an empty state rather than fake provider data.

## img-lite Channel Compatibility

The observed channels map as follows:

- img-lite gateway: `openai-image` profile; generation through CPA was verified live.
- Gitee AI: standard generation/edit endpoints with JSON and multipart forwarding.
- SiliconFlow: standard generation plus a custom edit operation that points to `/images/generations` with JSON image input.
- ModelScope: async submit-and-poll profile.
- Agnes: custom JSON operation using `/images/generations` for both generation and edit.
- HuggingFace Spaces: requires a dedicated Gradio queue adapter profile; it is not treated as a normal OpenAI-compatible REST endpoint.

Credentials for Gitee, SiliconFlow, ModelScope, and Agnes are required for live provider verification. Unit and fixture tests cover request construction before those live keys are supplied.

## Error Handling

- Invalid management payloads return HTTP 400 with the exact invalid field.
- Unknown provider kind or operation returns HTTP 404.
- Missing required model/input returns HTTP 400.
- Upstream non-2xx responses preserve status details for retry classification and request logs.
- Async terminal failures return HTTP 502 with the upstream task failure message.
- Binary response forwarding preserves `Content-Type` and `Content-Disposition`.
- Secrets and binary bodies are redacted or summarized by existing logging helpers.

## Testing

Backend tests cover:

- YAML/JSON round trip and sanitization;
- management CRUD and auth-index response behavior;
- watcher synthesis for multiple keys and per-key priority;
- hot-reload diff detection;
- media model and hidden operation registration;
- standard image JSON forwarding;
- multipart forwarding;
- no-model operation routing;
- async submit-and-poll success/failure/cancellation;
- retry across keys/providers;
- provider-name request logging;
- route registration and error responses.

UI tests cover:

- media config normalization and serialization;
- preserving unknown fields during concurrent config mutations;
- three category descriptors and routing;
- provider list partitioning by kind;
- form defaults and validation;
- optional model behavior;
- operation editor serialization;
- image connectivity request construction;
- success/failure usage matching;
- full type-check, lint, Bun/Node tests, production build, and bundled management regression tokens.

Final local integration tests run:

```text
client -> CPA /v1/images/generations -> configured image provider -> image response
client -> CPA /v1/media/video/:operation -> configured video provider -> video task/result
client -> CPA /v1/media/audio/:operation -> configured audio provider -> JSON/binary result
```

Local fixtures cover all three kinds, multi-key failover, zero-key operation routing, async polling, config hot reload, and request-log provider identity. Direct channel probes are added when their endpoint contracts and keys are available.

## Compatibility and Delivery Boundary

- Existing configuration fields and routes retain their behavior.
- `media-providers` is optional and has no effect when absent.
- Existing `openai-compatibility` image models remain valid.
- Media UI source is built in the management repository, then copied as `dist/index.html` to backend `static/management.html`.
- All work stays on local feature branches until an explicit push/deployment instruction is given.
