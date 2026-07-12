# Changelog

This project follows Semantic Versioning once release tags are published. Until
the first tagged release, changes are recorded under **Unreleased**.

## Unreleased

### Added

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
