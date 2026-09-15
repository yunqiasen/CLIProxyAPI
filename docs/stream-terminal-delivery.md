# Stream Terminal Delivery

The wsrelay request queue now applies backpressure instead of dropping events when
its eight-message buffer is full. Terminal delivery precedes channel closure;
caller cancellation unblocks blocked delivery and synchronizes channel closure.
Connection cleanup retains already queued output and then publishes its terminal
error without blocking other sessions. The pending map is not replaced during
concurrent access, and each request keeps its own queue reference across send and
cancellation races.

WebSocket upstream timeline logging serializes per-context read-modify-write
publication. Independent requests do not share an I/O lock. Gin
protects individual Get and Set operations, but those locks alone did not prevent
concurrent request records from replacing each other. Auth fields and raw event
records are preserved together. This change does not invent missing authentication
information when a caller never supplied it.

Regression tests reproduce a full queue dropping stream_end and concurrent log
publication retaining only a subset of 64 auth-bearing requests. Focused race tests
cover cancellation, cleanup and publication. No automatic continuation, output
replay, added network timeout or synthetic successful terminal is introduced.
