# Changelog

This project follows Semantic Versioning once release tags are published. Until
the first tagged release, changes are recorded under **Unreleased**.

## Unreleased

### Added

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
- Validation-first `ihtml.NewHandler` and `chat.NewChecked` constructors.
- Capability-sized Service interfaces and request-bound `ScopedService`.
- `WithAccessPolicyE` for fail-closed external authorization providers.
- Public chat/event protocol constants and chat `protocol_version` capability.
- Segmented streaming reasoning (`thinking_id` / `thinking_done`) and a
  human-readable tool timeline with redacted, collapsible technical details.
- `TemperatureOverride` for providers that need an explicit zero temperature.
- `ErrReservedKVKey` and `ReservedKVPrefix` let SDK hosts detect kernel-owned
  KV access without parsing error text.
- `WithOrderedBroadcaster` is an explicit incremental-event opt-in for hosts
  that globally serialize every same-user write through publication. Ordinary
  external broadcasters retain the safe `resync` default.

### Changed

- Minimum Go version is now 1.25.0 (was pinned to the 1.26.5 patch release).
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
- Handler read APIs defensively copy third-party Store values before sorting or
  returning them to callers.
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

### Security

- Claude credentials must be explicit; the adapter cannot fall back to ambient
  `ANTHROPIC_*` credentials for a configured endpoint.
- Secret resolution errors are typed and redacted, and disabled AI sections do
  not invoke a resolver.
