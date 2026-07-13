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
- Same-page multi-instance `ihtml.mount`: instances are fully isolated and
  individually destroyable; the first instance still merges into
  `window.ihtml` for backward compatibility. Declarative auto-mount now mounts
  every `[data-ihtml]` element.
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

### Changed

- Minimum Go version is now 1.25.0 (was pinned to the 1.26.5 patch release).
- `FileStore` fsyncs data files before rename and syncs the directory after
  rename/erase, guaranteeing crash/power-loss consistency of the atomic write.
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

### Security

- Claude credentials must be explicit; the adapter cannot fall back to ambient
  `ANTHROPIC_*` credentials for a configured endpoint.
- Secret resolution errors are typed and redacted, and disabled AI sections do
  not invoke a resolver.
