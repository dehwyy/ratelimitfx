# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`github.com/dehwyy/ratelimitfx` — standalone Go library: a Redis-backed sliding-window rate limiter with one pluggable strategy. Single-module repo, not part of the `paylonium` Go workspace despite its location.

There is no fx wiring in this library and no `module.go`. Consuming services hand-roll their own `fx.Provide` around `NewRedisLimiter` and a strategy.

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

The only built-in strategy is `PerKeyStrategy` (`Name()` returns `"key-strategy"`, default cap `DefaultPerKeyLimit = 120`). There is no `PerIPStrategy` or `PerMerchantStrategy`. To key by IP, wrap the `ClientIP(r)` string in your own `fmt.Stringer` (see `examples/http` `ipKey`).

`RedisLimiter` (limiter.go) is the only `Limiter` implementation, built via `NewRedisLimiter(client, strategy, cfg)`.

Redis key format is fixed: `<KeyPrefix>:<Strategy.Name()>:<Strategy.Key(k)>`. Changing it breaks keyspace for running deployments.

### Sliding window implementation

Single atomic Lua script per `Allow`/`AllowN` call (limiter.go `slidingWindowScript`, run via `redis.Script`):
1. `ZREMRANGEBYSCORE key 0 <now - window>` — drop expired entries.
2. `ZCARD key` — `allowed = count < limit`.
3. `ZADD key <nowMs> <member>` — only if allowed, or if `Config.CountRejected` is true.
4. `PEXPIRE key <2*window>` — TTL safety net.

`Config.CountRejected` (default false): rejected requests are not recorded, so a hammering client is not blocked forever. `true` restores the pre-v0.3.1 behaviour. The clock is `RedisLimiter.now` (defaults to `time.Now`, overridable in package tests).

The member format `<nanoUnix>-<atomic-seq>` (package-level `memberSeq`) guarantees uniqueness even when multiple goroutines hit the same nanosecond — required because ZADD dedupes by member.

### Fail modes

`Config.FailMode` (config.go) determines script-error behavior in `allow`:
- `FailClosed` (default): log at Error level, return `(false, err)` — caller denies and surfaces error.
- `FailOpen`: log at Error level, return `(true, nil)` — caller lets request through.

Script errors (including pipeline/connection errors) follow `FailMode`.

Short-circuit: `limit <= 0` returns `(true, nil)` without touching Redis. This is how callers disable limiting via config without special-casing it upstream.

### Microtypes

`domain.go` defines, per project style (single-field domain types become `type X underlying`, never structs or constructors):
- `RPM int32` with `.RPM() int32` accessor (used by `Strategy.Limit`).
- `Key` — a type alias for `fmt.Stringer`, the caller-identity value.

`ClientIP(r *http.Request) string` (strategy.go) extracts the client IP from `X-Forwarded-For`, then `X-Real-IP`, then `RemoteAddr`. It returns a plain `string`; callers wrap it in their own `fmt.Stringer` before passing it as a `Key`.

### Tests

Everything uses `miniredis.RunT(t)` — no Docker. When adding tests that rely on window expiry, set `limiter.now` to a fake clock instead of sleeping; `mr.FastForward` only moves key TTLs, not the window scores.

## How Paylonium consumes this library

Redis-backed consumers (merchant-api, widgetapi, payin-processing) use this limiter and run fail closed.

The two payment gateways (payin-gateway-api, payout-gateway-api) do **not** use this Redis limiter. They run an in-process `golang.org/x/time/rate` token bucket per merchant (their own `pkg/ratelimit`), which is **per-pod** — effective global cap is `N × rps` with `N` replicas. Those gateways fail closed on a per-merchant RPS lookup error (return `503`).

## Conventions enforced by the existing code

- Function calls with ≥2 args: every arg on its own line, opening brace on the call line, closing `)` on its own line (see `NewRedisLimiter`, `pipe.ZAdd`).
- Struct literals with ≥1 field: every field on its own line (see `redis.Z`, `PerKeyStrategy{RPM: ...}` at call sites).
- `var _ Limiter = (*RedisLimiter)(nil)` interface compliance check next to the impl — keep this pattern if adding new Limiter implementations.
- Error wrapping uses the package prefix: `fmt.Errorf("ratelimitfx: redis pipeline: %w", err)`.
- Logging via `github.com/rs/zerolog/log` with structured fields (`ratelimit_strategy`, `ratelimit_key`, `ratelimit_fail_mode`) — keep these field names stable; downstream log queries depend on them.
