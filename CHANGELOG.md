# Changelog

This project follows Semantic Versioning once release tags are published. Until
the first tagged release, changes are recorded under **Unreleased**.

## Unreleased

### Added

- `uitools` exposes scoped workspace read/write definitions and host-governed
  preparation/execution, with mandatory card code policy and atomic publication.
- Compact chat layout keeps controls visible in short embedded panels.

- Public per-card confirmation presentation text is safely rendered and covered by
  confirmation signatures; absent presentation preserves legacy signatures.
- `mountChat.theme.get/set/onChange` shares the workspace theme API without
  resetting conversation state, with instance-local events and teardown.

- `assistant.SessionRunner` exposes bounded host-owned session scheduling without
  requiring the default event projection: deep-cloned frames, per-event parent
  cancellation, explicit stop causes, queue and byte ceilings, tracked inline
  control callbacks, fail-closed panic handling, and Close that waits for real
  host unwind. Ordinary assistant backends share the runner; confirmation-specific
  scheduling remains intact.

### Fixed

- Chinese IME confirmation no longer leaves a 250 ms send cooldown: the next
  independent Enter sends immediately, and the trailing confirmation key does
  not insert an accidental newline. Shift+Enter still inserts a newline.
- Reasoning content replaces the transient working placeholder without duplicate status text.
- Inline chat fits short host viewports without compressing messages into the composer.
- Streaming answers and reasoning follow their rendered height, including delayed
  Markdown, disclosure changes and host resizing. Upward wheel/touch/keyboard or
  scrollbar reading pauses following; “Jump to latest” resumes it. Pending frames
  respect a user pause, and observers are released when an instance is destroyed.


### Added

- `agent` engine-neutral contracts and `agent/eino` reusable native Eino runtime,
  extracted from nbco with Responses encrypted reasoning replay, native tool
  loops, cancellation, token budgets, scoped durable sessions and regressions.
- `assistant.NewChecked` host-engine ChatBackend bridge with queued turns,
  cancellation, snapshot-to-delta projection, generic tool events and explicit
  host preparation/completion/approval hooks. No business database dependency.
- Standalone `ihtml.mountChat`, shared chat rendering, send/cancel/clear API,
  abortable context extension and redacted event hooks; `example/assistant` and
  `docs/ai-integration.md` demonstrate cross-project integration.
- `chat.Config.ModelFactory` for explicit host model transport injection
  without creating a second Agent owner.

- MIT license (`LICENSE`).
- `VersionedUserStateStore` optional capability: per-user optimistic-concurrency
  tokens with conditional whole-state replacement (`ErrVersionConflict`). All
  built-in stores implement it; the service layer retries conflicts with
  jittered backoff, making multi-replica deployments safe without sticky
  routing. Template `expected_revision` CAS is now enforced by the conditional
  write instead of an in-memory comparison.
- `ihtml/redisbroadcast`: Redis Pub/Sub `Broadcaster` for multi-replica
  real-time events with a per-user globally monotonic version (atomic Lua
  INCR+PUBLISH). The host owns the `redis.UniversalClient` lifecycle.
- `ihtml/storetest`: the official conformance suite for third-party `Store`
  implementations (`storetest.Run`). Built-in stores and the sqlstore SQLite
  dialect are validated by the same suite.
- Public hard resource ceilings for Item source/metadata (512-KiB content,
  8-MiB user aggregate), templates (1 MiB per version / 8 MiB per logical
  catalog, plus a 16-MiB encoded catalog envelope), KV maps (4096 entries /
  32 MiB), and retained revisions (4096 / 64 MiB). These remain active when
  ordinary revision retention uses `WithMaxRevisions(0)`.
- `ui_page_inspect` chat tool + `inspect_request`/`inspect_result` WebSocket
  frames: the assistant can read the live page render state (per-item
  presence/visibility/size/text/errors) through the active session, with a
  graceful offline timeout. Additive within chat protocol version 1.
- Same-page multi-instance `ihtml.mount`: runtime-managed styles, API bindings,
  cache namespaces and accessibility IDs are isolated and each instance is
  destroyable. The compatibility `window.ihtml` view follows the current
  primary instance. Declarative auto-mount now mounts every `[data-ihtml]`.
- Mobile sidebar drawer touch gestures: edge swipe-right opens, swipe-left
  closes.
- CI: staticcheck + govulncheck jobs and a Go version matrix (minimum
  supported and latest stable).
- Reusable `chat/config` SDK package with strict, versioned JSON decoding,
  typed errors, explicit `SecretResolver` injection, and secure file loading.
- AI configuration schema v2 adds runtime/tool/context/application controls;
  the frozen v1 document shape remains readable and rejects v2-only fields.
- Validation-first `ihtml.NewHandler` and `chat.NewChecked` constructors.
- Capability-sized Service interfaces and request-bound `ScopedService`.
- `WithAccessPolicyE` for fail-closed external authorization providers.
- Public chat/event protocol constants and chat `protocol_version` capability.
- A backend-agnostic WebSocket egress guard: encoded events are at most 1 MiB -
  1 byte, each turn at most 4096 events / 4 MiB, and each connection at most
  65536 events / 64 MiB. Unsafe or oversized third-party `ChatBackend` output
  fails closed with status 1009.
- Additive rich-answer `content_format` metadata (`text` / `markdown` / `html`)
  plus `assistant_reset` for clearing a rejected partial stream before retry.
- Segmented streaming reasoning (`thinking_id` / `thinking_done`) and a
  human-readable tool timeline with redacted, collapsible technical details.
- Per-turn semantic tool safety budgets shared by the root and Deep subagents:
  total, per-tool, and canonical-identical-argument limits reject execution
  with stable `assistant_budget` metadata. Strict JSON config exposes all three.
- Host-injected per-turn generation overrides (`RuntimeConfigProvider`) and a
  privacy-preserving, failure-isolated `TurnObserver` lifecycle API.
- Public Backend ceilings bound outstanding critical host callbacks,
  best-effort observation callbacks, and model/Service operations independently;
  a stalled observer can no longer consume authorization/confirmation capacity.
- Provider-body, per-call, per-turn and model-event output hard limits (defaults
  8 MiB / 1 MiB / 4 MiB / 4096), with stable `ErrModelOutputLimit` /
  `ModelOutputLimitError` diagnostics and schema-v2 configuration. Visible
  answer and reasoning projections each default to a separate non-disableable
  256-KiB limit independent of provider token claims.
- Turn/write host interceptors are deadline-bounded and panic-isolated;
  unreviewed callback errors are projected as generic user-safe failures while
  `NewUserFacingError` explicitly opts reviewed text into browser display.
- Conservative Eino model retries, normalized finish/usage metadata, and a
  no-tool final-answer repair pass for empty or token-truncated responses after
  successful tool evidence.
- Secure long-session summarization plus Deep-mode dynamic tool discovery;
  strict JSON configuration exposes both features and their thresholds.
- Capability-scoped opaque session IDs rotate persisted AI history when the
  host's authorization, prompt, confirmation, probe, or API boundary changes.
- Confirmation cards are HMAC-bound to their logical session, application
  capability scope, and semantic-tool protocol version. The additive
  `cancel_tool` client event shares a bounded FIFO with confirmation and returns
  an ID+nonce-correlated `cancel_tool_result`; the UI only claims an operation
  was not run after that authoritative acknowledgement succeeds.
- External confirmation nonce stores, like external session stores, now require
  an application `SessionNamespace` in checked construction; unchecked legacy
  construction generates an isolated ephemeral namespace.
- `TemperatureOverride` for providers that need an explicit zero temperature.
- `ErrReservedKVKey` and `ReservedKVPrefix` let SDK hosts detect kernel-owned
  KV access without parsing error text.
- Additive `VersionedKVService` / `ScopedVersionedKVService` single-key CAS,
  SHA-256 RawMessage revision tokens, typed `KVConflictError`, and compatible
  HTTP `revision` / optional `expected_revision` fields. Item layout saves now
  use one atomic conditional write; Item deletion transactionally removes its
  persisted layout references.
- `WithOrderedBroadcaster` is an explicit incremental-event opt-in for hosts
  that globally serialize every same-user write through publication. Ordinary
  external broadcasters retain the safe `resync` default.

### Changed

- Eino ADK is aligned with `v0.10.0-alpha.12` and the Claude adapter with
  `v0.1.23`. The bounded in-memory session store now runs Eino's official
  `SessionEventStore` conformance suite. Core builds are checked to remain
  Eino-free, so an existing host Agent can implement `ChatBackend` without
  linking the standalone `ihtml/chat` runtime.
- Model text/reasoning stream chunks are coalesced into UTF-8-safe bounded
  WebSocket deltas; the same bounded projection now drives both deltas and the
  authoritative `assistant_done` snapshot, including retry rollback/replay.
- Chat sessions now have a single in-process owner per scoped SessionID.
  Reconnect takeover is non-blocking but execution waits for the previous
  model/tool/manual lane to return; confirmation and pending-turn queues plus
  confirm/cancel frame rates are hard-bounded, and manual tools inherit the
  turn-duration deadline. Cross-process ownership still requires host routing
  or a distributed lease.
- Minimum Go version is now 1.26.9 for the official OpenAI Responses SDK and
  patched toolchain. gRPC/OpenTelemetry dependencies are updated to eliminate
  reachable vulnerabilities; CI lint tools are aligned with Go 1.26.
- `FileStore` serializes all in-process access plus cooperating processes with
  OS file locks, uses unique `0600` temporary/data files, fsyncs data before
  rename, and propagates directory-sync failures. Versioned CAS is therefore
  atomic across processes rather than only within one Go value. Its v2 disk
  format stores exact KV bytes and a semantic legacy `kv` copy for safe
  sequential rollback to an older binary. Waiting for either the process gate
  or OS lock now honors caller context cancellation.
- Item, page and template writes now persist the pre-change snapshot and the
  change itself in one atomic store operation (previously two, which could
  strand a snapshot on crash).
- `sqlstore` write transactions serialize per user through the new
  `ihtml_user_version` row (upsert-first lock ordering); existing deployments
  must re-run `Init()` once after upgrading, reads keep working before that.
- `SetPage` now counts toward the `writes` metric like every other mutation.
- The example delegates AI configuration to the public loader instead of
  owning a private parser in `package main`.
- The built-in assistant now treats ordinary questions and business-data
  queries as chat answers, reserving UI writes for explicit page-change
  requests. Its model contract emits semantic HTML without fixed business
  templates; the no-model demo truthfully previews rich answers without fake
  reasoning, fake tool calls, or page mutations.
- Handler read APIs defensively copy third-party Store values before sorting or
  returning them to callers.
- Handler reads and whole-state mutations now validate data returned by legacy
  `Store` and `UserStateStore` implementations against the same structural and
  aggregate limits as new writes. Invalid or oversized legacy state fails
  closed instead of being partially exposed or silently truncated and must be
  migrated or cleaned before upgrade.
- Production asset embedding no longer includes JavaScript test files.
- External/cross-replica broadcasters now receive authoritative `resync`
  notifications rather than state-bearing incrementals, because a database
  commit and message publication cannot share an ordering transaction. The
  built-in single-process hub retains incremental delivery.
- Redis publication is asynchronous and bounded; Redis Cluster keys share a
  hash slot and user identifiers are SHA-256-derived rather than stored in
  channels or keys as plaintext. This changes the Redis key/channel namespace;
  `Options.LegacyNamespaceMigration` provides an explicit short-lived dual
  publish/subscribe bridge for rolling upgrades, while remaining off by default
  so new deployments never reintroduce raw user identifiers.
- Public `KVService` calls now reject reads and deletes under `__ihtml` as well
  as writes. Callers must use the Page/Template/Error/layout capabilities;
  missing HTTP KV reads now correctly return 404 instead of 400.
- Live page inspection is paginated (`offset`, `limit`, optional `ids`), capped
  at 20 items and a complete structured JSON budget per call, and reports
  viewport/effective-style visibility plus the resolved light/dark theme.
- Same-page instances use exact per-instance Item CSS scopes, per-instance Item
  runtime bindings, persistent-cache namespaces, and unique accessibility IDs.
  Legacy unscoped browser settings are claimed once after successful identity
  resolution, while `window.ihtml` dynamically follows the live primary mount.
- Browser mounts validate HTTP(S) base/menu/routing options before creating DOM
  and no longer leak a half-mounted instance when preflight fails.
- Inline classic Item scripts now use an Item-local function scope. Existing
  Items that deliberately exposed top-level `var` declarations or functions
  must export them explicitly with `window.name = value`.
- CI analysis tools are version-pinned instead of resolving mutable `latest`
  releases.

### Fixed

- `EraseUser` no longer resets optimistic-concurrency versions: it persists a
  newer empty tombstone so a stale pre-delete CAS cannot resurrect user data.
- PostgreSQL/MySQL whole-user reads use a repeatable snapshot and cannot combine
  rows from different committed versions; pre-migration reads no longer abort
  by probing a missing version table inside an already-open transaction.
- KV key quotas and page-error merging are conditional whole-state updates for
  versioned stores, preventing cross-replica quota overruns and lost reports.
- Redis publication no longer blocks HTTP writes during an outage, and queue
  overflow or an accepted publish failure produces an explicit global resync
  retry instead of a silent gap.
- Inspection results no longer byte-truncate JSON or claim off-screen/hidden
  items are visible; the demo verifies the exact Item it created.
- Deleting an absent KV key now advances every built-in Store's user version,
  matching the documented mutation contract and SQLStore behavior.
- FileStore reports unsupported cross-process locking from `NewFileStore`
  instead of deferring an inevitable failure until the first operation.
- SDK constructors reject typed-nil interface dependencies during validation
  instead of accepting them and panicking later on a request or Redis startup.
- The platform contract now names Linux, macOS, and Windows as release targets;
  uncommon targets are best-effort, and the optional Eino/Sonic chat adapter is
  explicitly documented as unavailable on Plan 9.
- Chat collection now preserves provider answer whitespace, separates multiple
  message outputs, honors `Stream=false` even when tools are enabled, and drops
  rejected mid-stream retry content from the authoritative final snapshot.

### Security

- Claude credentials must be explicit; the adapter cannot fall back to ambient
  `ANTHROPIC_*` credentials for a configured endpoint.
- Secret resolution errors are typed and redacted, and disabled AI sections do
  not invoke a resolver.

## 2026-10-21: reusable host chat transport and bounded provider SSE

- Add a supported chat-only `hostTransport.request/open` capability with no
  fallback, preserving identity epochs and socket/new-chat/destroy lifecycle.
- Add `agent/providerstream` for existing host tool runners: bounded decoding of
  all three provider SSE formats, multiline/CRLF events, text-only progress,
  error-propagating validation hooks and explicit terminal-envelope completion.
  Business grants, secrets, HTTP safety, history selection and native confirmation
  stay in the host; existing agent/eino streaming remains unchanged.

- Add opt-in compact embedded chat header for hosts with their own title,
  preserving accessible chat naming, read-only state and New Chat lifecycle.

- Use readable 16px composer text, compact single-line border-box sizing and
  border-aware auto growth so input padding cannot inflate a short message.
