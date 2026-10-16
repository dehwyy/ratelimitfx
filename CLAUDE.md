# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`github.com/dehwyy/ratelimitfx` — standalone Go library: a Redis-backed rate limiter (sliding-window journal by default, optional GCRA behind `Config.Algorithm`) with one pluggable strategy. Single-module repo, not part of the `paylonium` Go workspace despite its location.

`module.go` offers `Module[C](func(*C) Config)`: provides `*RedisLimiter` and `Limiter` from a `*redis.Client` or `redis.Cmdable` in the graph and an optional `Strategy` (default `PerKeyStrategy{}`).

## Commands

```bash
go test ./...                 # all tests (uses miniredis, no Docker)
go test -race -count=1 ./...  # the command CI runs (vet + race)
go test ./pkg/ratelimit -run TestRedisLimiter_AllowsUpToLimit -v  # single test
go vet ./...
go run ./examples/http        # demo server on :8080; REDIS_ADDR=localhost:6379
```

Release: push a tag `vX.Y.Z` → `release.yml` runs tests and `gh release create --generate-notes`.

Go toolchain is pinned in `go.mod` and both workflows — keep them in sync when bumping.

## Architecture

All production code lives under `pkg/ratelimit/`. The design is intentionally small.

### Core contract: `Limiter` + `Strategy`

`Limiter` (limiter.go) is the public interface — **not generic**:
- `Allow(ctx, k Key)` uses the strategy's static cap.
- `AllowN(ctx, k Key, limit int32)` takes a per-request cap and **ignores** `Strategy.Limit()` — the escape hatch for runtime-varying caps (e.g. per-tenant RPM loaded from cache).

`Key` (domain.go) is a type alias for `fmt.Stringer`. The caller passes any value that stringifies to a stable identity (UUID, IP, API key). There is no dedicated `IP` type.

`Strategy` (strategy.go) is what callers plug in. Three methods:
- `Name() string` — used as a Redis key namespace, collision prevention between strategies.
- `Key(k fmt.Stringer) string` — how to serialise the caller identity.
- `Limit() RPM` — default static cap (requests per window, not per second — `Window` default is `1m`).

The only built-in strategy is `PerKeyStrategy` (`Name()` returns `"key-strategy"`, default cap `DefaultPerKeyLimit = 120`). There is no `PerIPStrategy` or `PerMerchantStrategy`. To key by IP, wrap the `ClientIP(r, trusted...)` string in `StringKey` (see `examples/http`).

`RedisLimiter` (limiter.go) is the only `Limiter` implementation, built via `NewRedisLimiter(client, strategy, cfg)`.

`(*RedisLimiter).AllowNWithQuota(ctx, k, limit) (Quota, error)` (allow_n_with_quota.go) is **not** part of `Limiter`: fakes of `Limiter` in consumers (a dozen of them) must keep compiling. It returns the `AllowN` decision plus the window quota (`Quota` in quota.go: `Allowed`, `Limit`, `Remaining`, `ResetAt`, `RetryAfter`, `Known`) from one script call. The field names are free of ZSET vocabulary: both algorithms fill the same fields (GCRA semantics in the GCRA section).

The sliding-window (journal) Redis key format is fixed: `<KeyPrefix>:<Strategy.Name()>:<Strategy.Key(k)>`. Changing it breaks keyspace for running deployments. GCRA keys have their own namespace, `<KeyPrefix>:gcra:<Strategy.Name()>:<Strategy.Key(k)>` (a string, not a ZSET): sharing the journal key would give `WRONGTYPE`.

### Sliding window implementation

Single atomic Lua script per `Allow`/`AllowN` call (limiter.go `slidingWindowScript`, run via `redis.Script`; its text is part of the contract, never edit it — running instances have its SHA cached):
1. `ZREMRANGEBYSCORE key 0 <now - window>` — drop expired entries.
2. `ZCARD key` — `allowed = count < limit`.
3. `ZADD key <nowMs> <member>` — only if allowed, or if `Config.CountRejected` is true.
4. `PEXPIRE key <2*window>` — TTL safety net.

`Config.CountRejected` (default false): rejected requests are not recorded, so a hammering client is not blocked forever. `true` restores the pre-v0.3.1 behaviour. The clock is `RedisLimiter.now` (defaults to `time.Now`, overridable in package tests).

`AllowNWithQuota` runs a second script (`quotaWindowScript`, same ARGV, same four steps) that additionally returns `{allowed, count after the conditional ZADD, score of the oldest entry or -1}`. It writes to the same ZSET as `slidingWindowScript`, so old and new pods can share a window. The key, ARGV list and error handling are shared with `allow` through `windowKey`, `windowArgs` and `scriptFailed` (window.go): change them in one place only.

The member format `<nanoUnix>-<atomic-seq>` (package-level `memberSeq`) guarantees uniqueness even when multiple goroutines hit the same nanosecond — required because ZADD dedupes by member.

### GCRA (`Config.Algorithm`)

`Algorithm` (algorithm.go): `AlgorithmSlidingWindow = "sliding"`, `AlgorithmGCRA = "gcra"` (the values of the gateways' `RATE_LIMIT_ALGORITHM` env), `String()`. Only the exact `AlgorithmGCRA` selects GCRA; the empty and unknown values mean the journal. `withDefaults` does not touch the field; env validation and the Warn on a bad value belong to the gateway. `Limiter` is not extended.

`allow` and `AllowNWithQuota` branch right after the `limit <= 0` short-circuit into one private path, `gcra` (gcra.go); `Allow`/`AllowN` use its `Allowed`. `gcraScript` (its text is part of the contract once released) keeps the theoretical arrival time (TAT) as an integer string in microseconds (a Lua double is exact up to 2^53, unix microseconds are about 1.8e15; pass it to `SET` with `string.format('%.0f')`, never as a bare number):
1. `tat = max(GET key, now)`, `I = max(floor(Window / limit), 1)` microseconds, burst `P = limit * I`.
2. Allowed when `tat + I - now <= P`; then `SET key tat + I PX ceil((tat + I - now) / 1000)`.
3. Rejected: no write; with `CountRejected` the TAT becomes `now + P`.
4. The script returns integers `{allowed, remaining, reset_after, retry_after}` (microseconds); `gcraQuotaOf` turns them into `Quota` with `ResetAt = now + reset_after`.

`RetryAfter` is `TAT' - (limit - 1) * I - now`, the exact time after which the same request passes; consumers must build `Retry-After` from it, not from `ResetAt - now` (after one request `ResetAt` is only about `now + I`). A change of `limit` between calls keeps the state; `Remaining` is clamped to `[0, limit]`. A reply of the wrong length and Redis errors go through `scriptFailed` (same message and log fields as the journal, `Config.FailMode`).

A stream the journal admits in full is admitted in full by GCRA (tested), but after a denial the two diverge and GCRA can deny a request the journal would allow (limit 2, 60 s: requests at 0, 0, 30, 60, 60 s; tested), so the two are not interchangeable. Pods with different algorithms count in different keys (up to 2x the limit while a rollout lasts).

### Fail modes

`Config.FailMode` (config.go) determines script-error behavior in `allow`:
- `FailClosed` (default): log at Error level, return `(false, err)` — caller denies and surfaces error.
- `FailOpen`: log at Error level, return `(true, nil)` — caller lets request through.

Script errors (including pipeline/connection errors) follow `FailMode`.

`AllowNWithQuota` follows the same rules: `FailClosed` returns `Quota{Limit: limit}` and the error, `FailOpen` returns `Quota{Allowed: true, Limit: limit}` (`Known == false`).

Short-circuit: `limit <= 0` returns `(true, nil)` without touching Redis. This is how callers disable limiting via config without special-casing it upstream.

### Microtypes

`domain.go` defines, per project style (single-field domain types become `type X underlying`, never structs or constructors):
- `RPM int32` with `.RPM() int32` accessor (used by `Strategy.Limit`).
- `Key` — a type alias for `fmt.Stringer`, the caller-identity value.

`ClientIP(r, trusted ...netip.Prefix) string` (client_ip.go) returns the `RemoteAddr` host unless that peer is in `trusted`; only then is `X-Forwarded-For` (walked right to left, first non-trusted hop) or `X-Real-IP` honoured. Never reintroduce trust of `X-Forwarded-For[0]`. `ParseTrustedProxies` builds the prefixes. It returns a plain `string`; wrap it in `StringKey` before passing it as a `Key`.

### Tests

Everything uses `miniredis.RunT(t)` — no Docker. When adding tests that rely on window expiry, set `limiter.now` to a fake clock instead of sleeping; `mr.FastForward` only moves key TTLs, not the window scores.

## How Paylonium consumes this library

Redis-backed consumers (merchant-api, widgetapi, payin-processing) use this limiter and run fail closed.

The two payment gateways use it too, also fail closed, for the per-IP and per-merchant limits:
- payin-gateway-api: `Module`, `CountRejected: true`, prefix `payin-gw:ratelimit`, 60 s window.
- payout-gateway-api: its own `NewRedisLimiter`, `CountRejected: false`, prefix `payout-gateway:ratelimit`.

Both call `AllowN` and then query the window a second time to build `X-RateLimit-*` and `Retry-After`; `AllowNWithQuota` (v0.5.0) removes that second round trip. The switch is an env flag of each gateway (default off); the library has no flag.

The algorithm is `Config.Algorithm`, set from the gateways' `RATE_LIMIT_ALGORITHM` env (`sliding` default, `gcra`); GCRA only together with `AllowNWithQuota`, since the old quota path reads the ZSET. The library has no flag for it either.

## Conventions enforced by the existing code

- Function calls with ≥2 args: every arg on its own line, opening brace on the call line, closing `)` on its own line (see `NewRedisLimiter`, `pipe.ZAdd`).
- Struct literals with ≥1 field: every field on its own line (see `redis.Z`, `PerKeyStrategy{RPM: ...}` at call sites).
- `var _ Limiter = (*RedisLimiter)(nil)` interface compliance check next to the impl — keep this pattern if adding new Limiter implementations.
- Error wrapping uses the package prefix: `fmt.Errorf("ratelimitfx: redis pipeline: %w", err)`.
- Logging via `github.com/rs/zerolog/log` with structured fields (`ratelimit_strategy`, `ratelimit_key`, `ratelimit_fail_mode`) — keep these field names stable; downstream log queries depend on them.
