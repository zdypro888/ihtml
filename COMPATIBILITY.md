# SDK compatibility policy

ihtml is currently a pre-v1 Go SDK. The following rules define which contracts
third-party integrations may rely on while the API approaches v1.

## Public contracts

- Exported identifiers in `ihtml`, `ihtml/agent`, `ihtml/agent/eino`,
  `ihtml/assistant`, `ihtml/chat`, `ihtml/chat/config`,
  `ihtml/sqlstore`, `ihtml/redisbroadcast`, and `ihtml/storetest` are public
  unless their documentation explicitly marks them deprecated.
- New features should use new capability interfaces. Existing interfaces must
  not gain methods casually, because that breaks downstream implementations.
- The legacy panic/delayed-validation constructors remain available; new code
  should use `NewHandler`, `chat.NewChecked`, and
  `chat/config.NewLoaderChecked`.
- Protocol strings have exported constants. JSON/WS additions are additive
  within a protocol version; incompatible wire changes require a new version.

## Configuration and data

- The minimum toolchain is Go 1.26.9. The optional native Responses adapter
  uses the official OpenAI SDK v3, which requires Go 1.26. Core Handler builds
  remain Eino-free; Go modules still resolve the module's minimum toolchain.
- `agent/eino.Config` is a programmatic runtime contract, not the strict
  `chat/config` JSON schema. Hosts resolve credentials explicitly. Its default
  transport refuses redirects; an injected HTTPClient owns transport safety.
- Runtime events/checkpoints are private model working memory, not product
  transcripts. A host sharing a RuntimeStore must supply a unique namespace
  and tenant/capability-scoped SessionID. The default namespace is suitable
  only for one application. Retain the old namespace/validator when migrating
  a persisted application; nbco explicitly retains `eino:chat` and numeric IDs.

- Host configuration and AI configuration have independent schema versions.
- A strict reader rejects unknown fields, duplicate keys, trailing documents,
  and unsupported versions. Lenient parsing is not part of the contract.
- A new reader must continue to read every schema version it claims to support.
- AI configuration schema v2 is current and the loader continues to read the
  frozen v1 shape. Runtime ceilings, retries, tool discovery/budgets,
  summarization, and application scope are v2-only fields; a strict v1 document
  rejects them instead of silently adopting semantics introduced later.
- Secret values are never configuration fields. Only explicit references are
  passed to the host's `SecretResolver`.
- Store values have snapshot ownership: implementations and callers must not
  retain mutable aliases across a method boundary.
- KV values are byte-preserving `json.RawMessage` snapshots. Implementations
  must not compact, normalize, HTML-escape, or otherwise re-encode valid JSON
  between `KVSet` and `KVGet`/`UserState`.
- Store capabilities are opt-in interfaces (`UserStateStore`,
  `VersionedUserStateStore`, `RevisionStore`). A wrapper that embeds a store
  implementing `VersionedUserStateStore` must override **both** replacement
  methods, because the service layer prefers the versioned one. Third-party
  stores should pass `storetest.Run`; multi-replica deployments require the
  versioned capability.
- Persistent state has non-disableable structural and aggregate ceilings. One
  user may have at most 512 Items and 8 MiB of aggregate Item source;
  `Item.Content` is limited to 512 KiB and each Item has at most 64 metadata
  entries with 128-byte keys and 1-KiB values (`MaxItemsPerUser`,
  `MaxUserItemBytes`, `MaxItemContentBytes`, `MaxItemMetaEntries`,
  `MaxItemMetaKeyBytes`, `MaxItemMetaValueBytes`). One Draft or Published
  template version is limited to a conservative 1-MiB structural estimate and
  the per-user template catalog's logical structure to 8 MiB
  (`MaxTemplateVersionBytes`, `MaxTemplateCatalogBytes`); the encoded internal
  catalog record has a separate 16-MiB storage envelope
  (`MaxTemplateCatalogStorageBytes`) for JSON framing and escaping. Public and
  reserved KV together are limited to 4096 entries and 32 MiB of aggregate
  key/raw-value source (`MaxKVEntriesPerUser`, `MaxUserKVBytes`); public values
  retain the separate 64-KiB `MaxKVValueBytes` limit. Revisions are limited to
  4096 entries and a 64-MiB aggregate Item/Page/Template source estimate
  (`MaxRevisionsPerUser`, `MaxUserRevisionBytes`).
- Values returned by either a legacy `Store` or `UserStateStore` are validated
  before they are cloned, served, snapshotted, or used as a mutation base.
  Invalid structure or any hard-limit violation fails closed with an error;
  the SDK does not silently truncate a third-party Store or expose a partial
  state. Before upgrading a deployment that may contain legacy oversized data,
  scan and migrate, trim, or remove those records offline. Once rejected, such
  data may not be readable through ordinary Handler APIs for in-band cleanup.
- `WithMaxRevisions` is a retention target inside the immutable revision safety
  envelope. Its default is 50; `WithMaxRevisions(0)` removes that lower
  count-based target but still trims at `MaxRevisionsPerUser` (4096) and
  `MaxUserRevisionBytes` (64 MiB). It does not make revision retention
  unbounded.
- Version 0 means that a user has never been touched. Every successful mutation
  (including deletion of an absent KV key) must advance the per-user version.
  `EraseUser` clears Items/KV/Revisions but must atomically persist a newer empty
  tombstone; resetting the version permits an ABA race in which a stale
  conditional write resurrects deleted data. SQL tombstones retain the user key
  in `ihtml_user_version`, so `PurgeUser` clears business payloads but is not by
  itself a physical-erasure/GDPR workflow.
- `sqlstore` schema changes ship behind the idempotent `Init()`; upgrading to a
  version that adds a table (for example `ihtml_user_version`) requires one
  `Init()` run before writes succeed. Reads never require the migration and
  whole-user reads use a database-consistent snapshot.
- FileStore disk format v2 writes both byte-exact `kv_bytes` and a semantic
  legacy `kv` copy. This supports sequential upgrade/rollback reads; old and new
  FileStore binaries must not run concurrently as writers because old releases
  do not participate in the new `.ihtml.lock` protocol. Stop writes and switch
  the FileStore process as one unit. FileStore now fails during construction on
  platforms without supported cross-process file locking instead of returning
  an instance whose first operation would fail.
- Public `KVService`/`ScopedService` calls reject every key beginning with
  `ReservedKVPrefix` for Get, Set and Delete using `ErrReservedKVKey`. Existing
  direct callers must migrate to Page/Template/Error/layout-specific APIs.
- Conditional KV is additive: existing `KVService`, `Service` and
  `ScopedService` interfaces are unchanged. `*Handler` implements the separate
  `VersionedKVService`; values returned by `ScopeService`/`ServiceForRequest`
  may be asserted to `ScopedVersionedKVService`. HTTP KV GET adds `revision`,
  while PUT only becomes conditional when `expected_revision` is present, so
  old clients retain unconditional-write behavior. Built-in versioned Stores
  provide cross-replica single-key CAS; legacy Stores only provide the existing
  per-Handler locking guarantee.
- Atomic page publication is additive: existing `PageService`, `Service` and
  `ScopedService` interfaces remain unchanged. `*Handler` implements the
  separate `PagePublicationService`; scoped Handler values may be asserted to
  `ScopedPagePublicationService`. A publication replaces only the named page's
  registry entry and complete Item set in one state transition. The legacy
  `PUT /api/pages` and `PUT /api/items` routes retain their original behavior.

## Browser and event integration

- `mountChat` is the additive inline chat-only mount. It returns its own API,
  with the same identity/cache isolation and destroy lifecycle as `mount`.
  `initialMessages` are display-only, not canonical model history.
  `chat.getMessages()` returns at most 100 display messages / 512 KiB, excluding
  notices and tool/reasoning state; hosts own identity-scoped snapshot retention.
  Await `ready()` before sending and `refreshAuth()` before a user switch. Its
  `contextProvider` receives an abort signal and produces untrusted context;
  `onChatEvent` receives a detached, best-effort event without confirmation
  credentials. No hook replaces server authorization or the host transcript.
- `assistant.NewChecked` bridges one existing Engine. It requires a trusted
  PrepareTurn and explicit model capabilities. It does not implement signed
  confirmation cards, inspection or checkpoint resumption. ApprovalRequired
  tools are excluded without a host ToolExecutionGuard; the host remains
  responsible for authorization, live revocation and durable tool receipts.

- Same-page mounts isolate ordinary Item CSS selectors, injected lexical
  runtime bindings, runtime-generated accessibility IDs, and local persistence.
  Inline classic Item scripts now execute in an Item-local function scope.
  Code that intentionally exported a top-level `var` or function must assign it
  explicitly (for example, `window.foo = foo`); accidental globals are no
  longer part of the compatibility contract.
  Item-authored DOM IDs and document-level CSS names such as `@keyframes` are
  not rewritten; Item code must query from its instance API's `root`, and CSS
  authors must namespace globally named at-rules. External stylesheets remain
  document-global when a trusted host explicitly enables them. Hosts should
  give each mount a stable `storageKey` (or element `id`) when its DOM position
  may change between reloads. After a mount resolves its user successfully, the
  first claimant copies legacy unscoped `ihtml.theme`, `ihtml.model`,
  `ihtml.agent_mode`, and session `ihtml.csid` values into its scoped namespace;
  legacy keys remain in place for rollback and are never shared with a second
  instance/user.
- `mount` validates options before mutating the host. `base` must be an HTTP(S)
  URL without embedded credentials; its query and fragment are discarded.
  Put ordinary request parameters in `params` and credentials in the explicit
  auth callbacks.
- The primary mounted API remains available as `window.ihtml`; destroying it
  promotes the first surviving instance, and compatibility properties such as
  `user`, `config`, and `root` follow that primary dynamically. Multi-instance
  host code must still retain the object returned by `mount()` for
  instance-specific work.
  Inline Item code should use its injected lexical `ihtml`. An explicitly
  enabled external classic Item script must synchronously capture its owner API
  before using it from later callbacks; asynchronously re-reading
  `window.ihtml` resolves the compatibility primary and is not instance-specific.
  External ES modules have no reliable `document.currentScript`; a host must
  pass its retained mount API through an explicit initializer, or use an inline
  module that can receive the lexical binding.
- An injected `Broadcaster` receives `resync` notifications, not incremental
  state payloads. Consumers must treat them as instructions to reread the
  committed Store state. `WithOrderedBroadcaster` is available only when the
  host globally serializes each same-user Handler write from before Store commit
  through Publish across every replica (for example, one global writer or a
  distributed lock around the complete call). Broadcaster-local ordering alone
  is insufficient; Redis Pub/Sub and NATS should use the safe default.
- `redisbroadcast` now uses `<prefix>:{ihtml:<sha256>}:ver|ev` instead of the
  legacy raw-user namespace. For rolling upgrades, enable
  `Options.LegacyNamespaceMigration` on every new instance before introducing
  it beside old instances; it temporarily dual-subscribes/publishes and strips
  old incrementals to authoritative resyncs. The option is off by default
  because legacy Redis keys/channels expose the raw user. Disable it after all
  old instances exit, then remove orphaned `<prefix>:ver:<user>` keys according
  to the host's retention policy. `KeyPrefix` must not contain `{` or `}`.
- `ui_page_inspect` is an additive paginated protocol: callers follow
  `next_offset` while `truncated` is true, keep the same optional `ids` filter,
  and must not assume more than 20 items in one result. A new server locally
  filters and paginates legacy all-items responses; a new browser answers a
  legacy request in its historical one-shot mode (capped at 200 items).
- Every server-to-client chat event, including output from a third-party
  `ChatBackend`, passes through the Handler's final WebSocket egress guard. An
  encoded event is limited to `MaxChatOutboundFrameBytes` (1 MiB - 1 byte); one
  accepted user turn is limited to 4096 events and 4 MiB, and one connection to
  65536 events and 64 MiB. Exceeding any boundary produces a fixed safe error
  and closes the connection with WebSocket status 1009. Accounting follows the
  Handler's accepted active/pending turns rather than backend-supplied turn IDs.
- `content_format` is an additive field on assistant answer events. It applies
  only to `assistant_delta.delta` and `assistant_done.message`; status,
  reasoning, error, and tool events remain plain text. An empty field keeps the
  legacy client auto-detection behavior, and clients must render unknown values
  as plain text. A non-empty `assistant_done.content_format` is authoritative
  for its final snapshot; omission preserves the stream format. The built-in
  Eino backend declares `html`. HTML is untrusted model output, not executable
  Item code: third-party clients must parse it in an inert document and rebuild
  it through an element/attribute/style/URL allowlist before DOM insertion;
  directly assigning it to `innerHTML` is outside the protocol's safety model.
- `preview_only` is an additive field on `/api/chat/models`. It distinguishes a
  deterministic integration preview from `model_ready`; preview responses may
  exercise the chat UI, but must never be presented as real model answers.
- `assistant_reset` is an additive event emitted when a partially streamed
  model attempt is rejected before retry. New clients clear the current turn's
  projected answer; legacy clients may ignore it because
  `assistant_done.message` remains the authoritative snapshot.
- `assistant_done.finish_reason`, `usage`, and `repaired` are additive optional
  diagnostics. Providers may omit counters or use new finish-reason strings;
  clients must tolerate both. They are not an authorization or reconciled
  billing source. `repaired=true` means the visible answer was regenerated from
  already-completed tool evidence without replaying any tool.
- Persisted Eino history is keyed by an opaque digest of user, browser csid and
  the effective capability boundary. Changes to read-only access, API/tool
  catalog, confirmation policy, trusted host prompt or probe forwarding rotate
  that internal scope; external `SessionStore` implementations must treat the
  key as opaque and must not attempt to derive an identity from it. The first
  upgrade to this scheme intentionally starts a new scoped history for existing
  csids; legacy opaque records may be removed later under the host's retention
  policy rather than copied across an unknown permission boundary.
- A custom shared `SessionStore` or `ConfirmNonceStore` requires an explicit
  stable `SessionNamespace` when constructed through `NewChecked`; replicas of
  one application use the same value, while different applications or tenants
  use different values. This scopes both persisted history and confirmation
  cards, even when applications share a confirmation key and nonce database.
  `SessionCapabilityVersion` is the host-controlled rotation hook for callback
  or authorization semantics that cannot be fingerprinted. The unchecked
  legacy `New` constructor uses a random per-instance namespace when either
  external Store is present and the namespace is missing, preventing
  cross-application reuse at the cost of restart/replica continuity.
- Backend resource ceilings are public configuration: outstanding critical host
  callbacks, observer callbacks, and model/Service operations default to
  32/32/64. Zero selects those defaults; explicit values are limited to
  1..4096. `TurnObserver` delivery is best effort and uses the isolated observer
  ceiling: accepted snapshots keep per-turn order, but overload or an exporter
  that ignores its context may drop later snapshots without failing the turn.
- Model output has local hard boundaries independent of provider token/usage
  claims. Defaults (and accepted schema-v2 / `NewChecked` ranges) are: provider
  response body 8 MiB (1..64 MiB), each model call 1 MiB (64 KiB..16 MiB), the
  shared turn 4 MiB (64 KiB..64 MiB and never below the per-call value), model
  stream events 4096 (16..65536), the visible final answer 256 KiB
  (16..512 KiB), and projected reasoning per turn 256 KiB (16..768 KiB) with an
  additional fixed 64-KiB ceiling per reasoning segment. Content, reasoning,
  tool calls and extension payloads all participate, including Deep subagents,
  summarization and final repair calls. Visible answer and reasoning projections
  are separate from the model-output budget, and streamed text is coalesced
  into bounded delta frames.
  Schema-v2 exposes these as `max_provider_response_bytes`,
  `max_model_output_bytes_per_call`, `max_model_output_bytes_per_turn`,
  `max_model_output_events_per_turn`, `max_assistant_message_bytes`, and
  `max_reasoning_bytes_per_turn` under `ai.agent`. Zero selects safe defaults;
  checked/programmatic and strict JSON configuration reject values outside the
  ranges above, so these safety boundaries cannot be disabled.
  Frozen schema-v1 documents cannot name the fields and receive the same safe
  runtime defaults when loaded by a v2 reader. Programmatic limit failures wrap
  `chat.ErrModelOutputLimit` and expose `*chat.ModelOutputLimitError`.
- One `Backend` now enforces a single in-process owner for each scoped session
  ID. A reconnect takes ownership without waiting in the WebSocket read path,
  but its turns and confirmations remain behind the previous owner's real
  model/tool shutdown barrier. This is not a distributed lease: deployments
  with multiple Backend processes must add sticky routing or an external
  session-ownership lock. A shared `SessionStore` alone does not serialize
  execution across replicas.
- `cancel_tool` is an additive client event in chat protocol version 1. It
  carries the unchanged signed `tool_call`; the built-in backend verifies the
  card in its issuing logical-session/application capability scope and
  atomically consumes the same nonce used by `confirm_tool`. Confirm and cancel
  share one bounded FIFO so websocket frame order is also nonce-CAS order. The
  backend replies with additive `cancel_tool_result`, correlated by the card's
  ID and nonce without echoing its token or arguments. New clients must remain
  in a disabled "cancelling" state until that acknowledgement succeeds; a
  failure or missing acknowledgement is not proof that the tool did not run.
  Old clients safely ignore the new result event.

## Deprecation

Before v1, unavoidable breaking changes will be grouped into a minor release and
documented in `CHANGELOG.md`. Once v1 is tagged, an exported API will remain for
at least one minor release after deprecation unless retaining it creates a
direct security vulnerability.

The repository license is MIT (see `LICENSE`) and the minimum supported Go
version is the `go` directive in `go.mod` (currently 1.25.0, verified in CI
alongside the latest stable release). Release tags must still be chosen and
published by the project owner before representing the module as a stable v1
dependency.

Release builds are supported on Linux, macOS, and Windows. Other `GOOS` targets
are best-effort: `FileStore` rejects platforms that do not provide the locking
contract it needs, and the optional `ihtml/chat` package currently inherits
Eino/Sonic's lack of Plan 9 support. Hosts targeting an uncommon platform should
cross-build the exact package set they ship instead of assuming every optional
adapter is portable there.

### Host-owned chat transports and bounded provider streams

`mountChat(host, {hostTransport: {request(path, init), open()}})` may use a
host-owned REST adapter and WebSocket-compatible object. `request` returns a
Response-like object and receives the SDK's abort signal; `open` returns a
synchronous socket with readyState, send/close and onopen/onclose/onerror/onmessage.
Both hooks are required. This capability is chat-only; ordinary mount rejects it.
There is no network fallback when a hook fails. Existing identity epochs,
refreshAuth, cancellation, new-chat disconnect and destroy apply to host sockets.
The host must cancel its own work when its socket closes. SDK paths and displayed
messages are not authority: hosts must use closed native dispatch, reauthorize
server-side, and require native human confirmation for mutations. Never interpret
model-supplied HTTP paths or display tool events as execution instructions.

`agent/providerstream.Decode` is an optional, framework-free bounded SSE decoder
for hosts with their own reviewed provider HTTP transport and tool runner. It
accepts Responses, Chat Completions and Anthropic Messages event streams, emits
cumulative answer text, and requires a terminal event before returning native JSON
for final semantic validation. It joins multiline SSE data and accepts CRLF.
Validation/display callback errors stop decoding; reasoning and tool arguments
never appear in the text hook. See package documentation for hard bounds. It does
not fetch URLs, invoke tools, retain history, choose credentials or confer
permissions. Hosts already using agent/eino should keep its existing SDK/model
streaming instead of stacking this decoder over it. HTTP cancellation, secret
filtering, current authorization and closing the reader remain host duties.

`mountChat({chatHeader: 'compact'})` omits the SDK's visible heading when a
host already supplies one. The chat region retains its localized accessible
name, read-only indicator and reachable New Chat button; clear/cancellation and
confirmation behavior are unchanged. The default and ordinary mount headers
remain unchanged. This is presentation only, never an authorization option.

`mountChat({onNewChat() { ... }})` receives a display-only notification after
explicit New Chat/chat.clear resets; mount, identity refresh and destroy do not
invoke it. Hosts may clear a selected persisted conversation. SDK display
snapshots never become trusted model history; hosts re-authorize resumed server
continuations. Auto-follow settles for two bounded animation frames and uses an
instant scroll, preserving user-upward intent through terminal rendering.
