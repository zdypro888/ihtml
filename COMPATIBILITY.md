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
  should use `NewHandler` and `NewChecked`.
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
- Store capabilities are opt-in interfaces (`UserStateStore`,
  `VersionedUserStateStore`, `RevisionStore`). A wrapper that embeds a store
  implementing `VersionedUserStateStore` must override **both** replacement
  methods, because the service layer prefers the versioned one. Third-party
  stores should pass `storetest.Run`; multi-replica deployments require the
  versioned capability.
- `sqlstore` schema changes ship behind the idempotent `Init()`; upgrading to a
  version that adds a table (for example `ihtml_user_version`) requires one
  `Init()` run before writes succeed. Reads never require the migration.

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
