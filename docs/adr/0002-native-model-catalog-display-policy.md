---
status: accepted
---

# Own model catalog presentation without changing routing

The user accepted integrating the Model Sort capability natively in CPA, with the primary editor reached through Management Center Info's existing model list rather than general settings. CPA will own a persisted, reloadable, instance-wide catalog display policy shared across client keys, with manually configured hiding as its primary purpose and pinning or ordering as secondary controls; a frontend-only rearrangement would not improve external clients, while an independently installed overlapping plugin would split ownership of the same policy. Catalog-hidden entries retain their existing request eligibility, pinned entries gain no scheduling priority, and the management inventory remains visible for restoring hidden entries.

The user confirmed global scope and manual configuration. With no display policy configured, existing catalog visibility and ordering stay unchanged; the plugin's default ascending sort is not adopted. The policy applies to supported public catalog formats. Hiding a model removes it from discovery only: a direct request using its known client-facing identifier retains the same eligibility as before, subject to existing access controls, model configuration and upstream availability.

## Consequences

Provider configuration, credentials, model aliases, capability resolution, access controls and failover are outside this change. Existing per-client catalog eligibility and metadata remain authoritative; presentation rules must not reintroduce filtered entries or change ordinary model calls. External clients may cache or reorder catalog responses, so this decision promises CPA response behavior rather than control over every client's interface.

The user confirmed preserving the plugin's existing presentation capabilities: manually selected ascending or descending ordering, ordered pinning, hiding, exact identifiers and `*` patterns, and configuration reload. Manually configured patterns continue to match newly available models; hiding takes precedence over pinning. Matching uses the client-facing catalog identifier, with Gemini's bare identifier and `models/`-prefixed form treated equivalently. These capabilities remain secondary to the primary hiding workflow and do not opt unconfigured installations into sorting or hiding.

This records the aligned scope, not an implemented feature. On 2026-10-06 the user explicitly approved the displayed alignment-to-specification handoff. The resulting [specification](/home/div/1_Project_dir/AI/CLIProxyAPI/.scratch/native-model-catalog/spec.md) is published locally; the user approved its ticket decomposition and implementation on 2026-10-07. Delivery verification is recorded with the implementation ticket.

Reference: [Model Sort](https://github.com/dotiful/cpa-plugin-model-sort).
