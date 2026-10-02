# Changelog

## v0.4.0

### Security

- `ClientIP` no longer trusts `X-Forwarded-For[0]` / `X-Real-IP` from any peer. Forwarding headers are
  honoured only when `RemoteAddr` belongs to a trusted proxy; the XFF chain is walked right to left
  and the first non-trusted hop is the client. Previously a client could rotate the header value and
  bypass the limit.

### Changed (behaviour)

- `ClientIP(r)` with no trusted proxies now returns the `RemoteAddr` host. Services behind an ingress
  must pass their proxy CIDRs, otherwise all clients share the ingress address:
  `ClientIP(r, trusted...)`. The signature is source-compatible (variadic).

### Added

- `ParseTrustedProxies([]string) ([]netip.Prefix, error)`.
- `StringKey` microtype.
- `Module[C](func(*C) Config)` fx option providing `*RedisLimiter` and `Limiter`; `ErrNoRedisClient`.

## v0.3.1

### Changed (behaviour)

- Rejected requests are no longer recorded in the sliding window by default. Previously every call
  was added to the ZSET before the limit check, so a client that kept sending requests while blocked
  stayed blocked indefinitely. Now only allowed requests count.
- To keep the old behaviour set `Config.CountRejected = true`. Existing consumers
  (widgetapi, payin-processing) that relied on "hammering extends the block" must opt in explicitly.

### Added

- `Config.CountRejected` (default `false`).

### Fixed

- The window update is now a single atomic Lua script instead of a pipeline, so concurrent callers
  can no longer overshoot the limit between the check and the write.
- README and CLAUDE.md describe the current API and the atomic implementation.

## v0.3.0

- `Limiter` is no longer generic; strategy keys are `fmt.Stringer`.
