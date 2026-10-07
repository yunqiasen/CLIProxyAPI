# CPA Request Compatibility

CPA mediates client conversations across upstream providers with different protocol capabilities.

## Language

**Client model alias**:
The model name requested by the client. It is distinct from the actual model selected at an upstream provider.
_Avoid_: Upstream model when referring to the client-facing name.

**Route-bound state**:
Conversation state whose validity depends on the upstream route that produced it, such as encrypted reasoning or a stored response reference.
_Avoid_: Portable history.

**Portable history**:
Readable conversation content that can be carried between compatible routes, including messages, summaries and tool results.
_Avoid_: Complete history when required content is missing.

**Compaction owner**:
The service or adapter that produced a compacted state and understands its format. An opaque upstream state and an adapter-owned summary are different kinds of state.
_Avoid_: Universal summary blob.

**Client-orchestrated compaction**:
A model-backed summary requested and assembled into history by the client. Local orchestration does not imply offline or free processing.
_Avoid_: Offline compaction.

**Remote compaction**:
A compaction operation requested through a dedicated upstream protocol. A standalone compacted window, a streamed compaction item and threshold-triggered compaction are distinct contracts.
_Avoid_: Plain text summary as a synonym for every compact response.

**Provider connectivity probe**:
A check of one selected credential and model using the currently selected provider settings, including an unsaved edit draft. The probe reflects the same compatibility policy as production requests; its success is not certification of every optional tool.
_Avoid_: All-key test or universal capability certification.

**Terminal integrity**:
Faithful delivery of a Responses completion, failure or incomplete outcome together with its associated output and error details. A disconnected stream is not a successful completion.
_Avoid_: Automatic continuation as a synonym for stream repair.

## Model catalog language

**Model catalog**:
The discovery list of client-facing models offered by CPA. Catalog visibility is distinct from permission or eligibility to execute a request.
_Avoid_: Routing table or access-control list.

**Catalog model identifier**:
The model identifier exposed to a client, including a configured alias when that alias is what the client uses. It is distinct from an upstream provider's internal model name.
_Avoid_: Upstream model name when identifying an entry presented to the client.

**Catalog display policy**:
The manually configured, instance-wide rules that determine catalog visibility, pinning and ordering. They are shared across client keys and describe discovery rather than credential selection or request failover.
_Avoid_: Provider scheduling policy.

**Catalog pattern**:
A manually supplied match rule for client-facing catalog identifiers, either an exact identifier or a `*` wildcard pattern. Its membership includes current and future matching models, independently of their upstream provider.
_Avoid_: Provider selector or access grant.

**Pinned model**:
A catalog entry placed ahead of ordinary entries for easier discovery. Pinning expresses presentation preference, not execution priority.
_Avoid_: Preferred upstream or priority credential.

**Catalog-hidden model**:
A model omitted from client-facing discovery while retaining its existing request eligibility. Hiding alone neither disables the model nor changes access permissions.
_Avoid_: Disabled model or denied model.

**Management inventory**:
The management view of models eligible for catalog presentation, including entries hidden by a catalog display policy. It supports discovering and restoring hidden entries without granting new model access.
_Avoid_: Public catalog or unrestricted inventory.

**Client display order**:
The order ultimately shown by an external client's model picker. A client may reorder or cache CPA's catalog instead of displaying the supplied order directly.
_Avoid_: Guaranteed catalog rendering order.
