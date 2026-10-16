# Changelog

## v0.5.0

### Added

- `(*RedisLimiter).AllowNWithQuota(ctx, k, limit) (Quota, error)`: the same decision as `AllowN`
  together with the window quota, returned by one Lua call (one round trip). Rate-limit response
  headers no longer need a second Redis query (`ZCOUNT` + `ZRANGEBYSCORE`, or `ZCARD` + `ZRANGE`).
- `Quota`: `Allowed`, `Limit`, `Remaining` (never negative), `ResetAt` (oldest entry + `Window`),
  `RetryAfter` (zero when allowed, otherwise `ResetAt - now`), `Known` (false when there is no quota:
  `limit <= 0`, or `FailOpen` after a Redis error).

### Notes

- `AllowNWithQuota` uses its own script but the same key, member format, `CountRejected` handling,
  `PEXPIRE` and clock as `AllowN`; both methods share one window and can be mixed freely, also across
  pods running different versions.
- `Limiter` is unchanged: the method lives on `*RedisLimiter`, so existing fakes keep compiling.
- Redis errors are logged by the same code path as `AllowN` (same message and fields) and follow
  `Config.FailMode`.
- Internal: key, member and script-failure handling of `Allow`/`AllowN` moved into shared private
  helpers; their behaviour and the sliding-window script are unchanged.

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
