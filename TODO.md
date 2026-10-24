# TODO

Planned changes that are not defects. Defects go in
[`KNOWN_ISSUES.md`](KNOWN_ISSUES.md). An entry is removed by the change that
does it.

## Error responses as RFC 9457 problem details

- **Area**: core, `errors.go`.
- **Added**: 2026-10-23.
- **Detail**: error responses use Gas's own envelope,
  `{"error":{"status","code","message","fields","details"}}`. RFC 9457
  `application/problem+json` (`type`, `title`, `status`, `detail`, plus
  extension members) is the standard shape clients and gateways already
  understand. Moving to it changes every error response, so it is a
  breaking change.
