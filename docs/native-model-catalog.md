# Native model catalog display policy

CPA can curate public discovery lists without disabling models. Open **Center Info →
Model List → Manage Display** to edit the instance-wide policy. All client keys
share the rules, while their existing model eligibility remains unchanged.

## Configuration

The default is a no-op. Hiding alone does not enable alphabetical sorting.

```yaml
client:
  model-catalog:
    order: preserve # preserve (default), asc, desc
    hidden:
      - unused-model
      - '*-preview'
    pinned:
      - preferred-model
      - 'team/*'
```

Rules use case-sensitive client-facing identifiers, including aliases and channel
prefixes. Only `*` is special; it matches zero or more characters. Whitespace-only
rules are discarded. Unmatched rules remain saved and apply to future matching
models. A Gemini entry can match its bare ID or `models/`-prefixed name. Claude
cloaking is unchanged: select the Claude format to inspect the actual emitted ID.

The order of operations is hide → optional stable sort → ordered pinning. Pin
patterns keep their own configured order; matches within one pattern keep their
relative catalog order. Overlapping pins do not duplicate entries. Hiding always
wins. Pinning has no effect on provider priority or credential selection.

Known hidden models remain callable under the same model configuration and access
rules. Hiding is neither an authorization boundary nor a model-disable mechanism.
A hide-all rule returns an empty list, not a request-denial policy.

## Editor and restoration

The editor uses a management-only inventory rather than the filtered public list.
It retains hidden entries, displays the matching rules and previews the native
result on the backend. Save persists only this policy; Cancel does not persist.
Settings reload without recompilation or a container restart.

For an exact hide rule, restore removes the matching rule. For wildcard-hidden
entries, edit the shown matching rules explicitly: removing a wildcard can reveal
several entries, and an additional matching hide rule can still keep an entry
hidden. There is no implicit per-model allow override.

External clients may cache the catalog or impose their own ordering. Refresh their
model list to observe the new response. This feature controls CPA's response, not
third-party picker rendering.

## Management API

New operations use the existing authenticated V8 API:

- `GET /v8/management/config/client/model-catalog`: saved group; an absent group
  follows the existing 404/unconfigured contract.
- `PUT /v8/management/config/client/model-catalog`: replace the policy object.
- `PATCH /v8/management/config/client/model-catalog`: update supplied fields;
  arrays replace whole lists, and an empty array clears a list.
- `DELETE /v8/management/config/client/model-catalog`: return to defaults.
- `GET /v8/management/models/catalog?format=openai`: inventory and active native
  policy. Formats: `openai`, `claude`, `gemini`, `codex`, `grok`.
- `POST /v8/management/models/catalog/preview`: body containing `format` and
  a draft `policy`; no persistence or inference.

Inventory and preview return `format`, `policy`, `entries`, `visible_ids`,
`counts` and `model_sort_enabled`. Entries contain `id`, `label`, `format`,
`hidden`, `hidden_rules`, `pinned_rules` and zero-based `position` (`-1` when
hidden). Counts contain `total`, `visible` and `hidden`. No provider credentials
are returned. The public discovery endpoints have no include-hidden bypass.

A successful configuration write confirms persistence; the existing runtime
reload is asynchronous. Read the active inventory and refresh the public catalog
to verify application. Invalid settings or failed writes retain the previous
policy. Existing V0 clients and unrelated grouped configuration are unchanged.

## Compatibility

OpenAI, Claude, Gemini, Codex and Grok Shell catalog routes use one presentation
stage, including applicable Home-backed lists. Model metadata is preserved,
Claude boundary IDs follow the final list and Codex prompt data stays compact.
Unknown catalog shapes pass through unchanged.

No plugin installation is required. Existing response plugins still run first.
If Model Sort is also enabled, its filtering/order and the native policy both
apply; the editor shows an overlap notice. The native preview describes only
native rules and cannot restore an entry another plugin already removed. No user
plugin configuration is automatically disabled or migrated.

Management inventory reuses existing catalog builders with an in-process-only
capture context, before presentation transforms. In Home mode it uses the first
configured client key, never the management bearer; missing client context or a
failed catalog source produces an error rather than a successful empty inventory.
Existing management availability restrictions still apply.

## Verification

Regression coverage exercises actual HTTP catalog routes, private inventory and
preview, configuration parsing/reload, direct requests to hidden models with mock
upstreams, and concurrent policy snapshots. UI coverage verifies serialization,
draft actions, connection isolation and localized controls; browser acceptance
covers interactive hiding/restoration and ordering with fixture configuration.
The production configuration is not edited for these tests.
