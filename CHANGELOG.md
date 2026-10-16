# Changelog

## v0.6.0

### Added

- `Config.Algorithm` (`Algorithm`): `AlgorithmSlidingWindow` (`"sliding"`) or `AlgorithmGCRA` (`"gcra"`),
  with `String()`. The empty and any unknown value select the sliding window of v0.5.0; only the exact
  `AlgorithmGCRA` selects GCRA. The default is unchanged.
- GCRA: one O(1) script call per request (`GET` and at most one `SET`) instead of `ZREMRANGEBYSCORE`,
  `ZCARD`, `ZADD` and `PEXPIRE`. The state is one string per key, the theoretical arrival time in
  microseconds, so the memory per key no longer grows with the limit.
- `Allow`, `AllowN` and `AllowNWithQuota` all follow `Config.Algorithm`; signatures and `Limiter` are unchanged.

### GCRA semantics

- Key: `<KeyPrefix>:gcra:<Strategy.Name()>:<Strategy.Key(k)>`. It lives next to the sliding-window ZSET
  without a `WRONGTYPE` clash, and the two counts are independent.
- Emission interval `I = floor(Window / limit)` (at least 1 microsecond), burst `limit`: up to `limit`
  requests are allowed at once, then one per `I`. The rate error is below 0.1 % for `limit <= 60000` per minute.
- `CountRejected = false`: a rejected request does not change the state. `true`: it sets the arrival
  time to `now + limit * I`, so a hammering client stays blocked while it keeps sending and recovers
  within `Window` after it stops.
- `Quota`: `Remaining` is in `[0, limit]`; `ResetAt` is the moment the bucket is full again; `RetryAfter`
  is `0` when allowed, otherwise the exact time after which the same request passes. After one request
  `ResetAt` is about `now + I`, not `now + Window`.
- `Retry-After` must be taken from `Quota.RetryAfter`, not from `ResetAt - now`.
- Redis errors and malformed replies are logged and handled like in the sliding window (same message,
  same fields, `Config.FailMode`).

### Notes

- GCRA is not a drop-in copy of the journal. A stream of requests that the journal admits in full is admitted in
  full by GCRA, and the mean rate is the same, but once the journal has denied something the two diverge, and
  GCRA can deny a request the journal would allow. Example with `limit = 2` and a 60 s window, requests at 0, 0,
  30, 60, 60 s: the journal denies the one at 30 s, GCRA denies the last one at 60 s.
- Pods with different `Algorithm` count in different keys: during a rolling update a client can get up to
  twice its limit. Switching back to `sliding` starts with an empty window.
- `slidingWindowScript` and `quotaWindowScript` are unchanged byte for byte: with the default algorithm
  v0.6.0 and v0.5.0 behave identically.

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
