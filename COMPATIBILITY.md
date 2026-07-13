# SDK compatibility policy

ihtml is currently a pre-v1 Go SDK. The following rules define which contracts
third-party integrations may rely on while the API approaches v1.

## Public contracts

- Exported identifiers in `ihtml`, `ihtml/chat`, `ihtml/chat/config`,
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

- Host configuration and AI configuration have independent schema versions.
- A strict reader rejects unknown fields, duplicate keys, trailing documents,
  and unsupported versions. Lenient parsing is not part of the contract.
- A new reader must continue to read every schema version it claims to support.
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

## Browser and event integration

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
