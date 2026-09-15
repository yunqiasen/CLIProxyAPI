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
