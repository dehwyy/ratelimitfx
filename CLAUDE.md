# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`github.com/dehwyy/ratelimitfx` — standalone Go library: Redis-backed sliding-window rate limiter with pluggable strategies and optional Uber FX wiring. Single-module repo, not part of the `paylonium` Go workspace despite its location.

## Commands

```bash
go test ./...                 # all tests (uses miniredis, no Docker)
go test -race -count=1 ./...  # the command CI runs (vet + race)
go test ./pkg/ratelimit -run TestRedisLimiter_AllowsUpToLimit -v  # single test
go vet ./...
go run ./examples/http        # demo server on :8080; REDIS_ADDR=localhost:6379
```

Release: push a tag `vX.Y.Z` → `release.yml` runs tests and `gh release create --generate-notes`.

Go toolchain is pinned to `1.25.5` in `go.mod` and both workflows — keep them in sync when bumping.

## Architecture

All production code lives under `pkg/ratelimit/`. The design is intentionally small — the pieces make sense together, not in isolation.

### Core contract: `Limiter[K]` + `Strategy[K]`

`Limiter[K any]` (limiter.go) is the public interface: `Allow(ctx, k)` uses the strategy's static cap; `AllowN(ctx, k, limit)` takes a per-request cap and **ignores** `Strategy.Limit()` — this is the escape hatch for runtime-varying caps (e.g. per-tenant RPM loaded from cache).

`Strategy[K any]` (strategy.go) is what callers plug in. Three methods:
- `Name()` — used as a Redis key namespace, collision prevention between strategies
- `Key(k K) string` — how to serialise the caller identity
- `Limit() RPM` — default static cap (requests per window, not per second — `Window` default is `1m`)

Redis key format is fixed: `<KeyPrefix>:<Strategy.Name()>:<Strategy.Key(k)>`. Changing this format breaks keyspace for running deployments.

### Sliding window implementation

Single Redis pipeline per `Allow`/`AllowN` call (limiter.go `allow`):
1. `ZREMRANGEBYSCORE key 0 <now - window>` — drop expired entries
2. `ZADD key <nowMs> <nowNs-seq>` — record this hit with a unique member
3. `ZCARD key` — count surviving entries
4. `EXPIRE key <2*window>` — TTL safety net

The member format `<nanoUnix>-<atomic-seq>` (package-level `memberSeq`) guarantees uniqueness even when multiple goroutines hit the same millisecond — required because ZADD dedupes by member.

Decision: `count <= limit` → allow. Equal-to-limit still allows because this request was already added.

### Fail modes

`Config.FailMode` (config.go) determines pipeline-error behavior in `allow`:
- `FailClosed` (default): log at Error level, return `(false, err)` — caller denies and surfaces error
- `FailOpen`: log at Error level, return `(true, nil)` — caller lets request through

Short-circuit: `limit <= 0` returns `(true, nil)` without touching Redis. This is how callers disable limiting via config without special-casing it upstream.

### Microtypes

`domain.go` defines two single-field types — per project style, single-field domain types become `type X underlying`, never structs or constructors:
- `RPM int32` with `.RPM() int32` accessor (used by Strategy.Limit)
- `IP string` with `.String() string` accessor (used by PerIPStrategy.Key)

The generic `K` is `uuid.UUID` for `PerMerchantStrategy` and `IP` for `PerIPStrategy`. Callers building middleware must wrap: `ratelimit.IP(ratelimit.ClientIP(r))` — `ClientIP` returns `string`, the strategy key type is `IP`.

### FX integration

`module.go` exposes `ModuleMerchant(rpm)` and `ModuleIP(rpm)` as `fx.Option` factories. Each provides the typed `Strategy[K]` plus `NewRedisLimiter[K]` annotated `fx.As(new(Limiter[K]))`. Callers still need to provide `redis.Cmdable` and `Config` in the graph. `fx.Annotate` calls are kept multiline per project style.

### Tests

Everything uses `miniredis.RunT(t)` — no Docker. When adding tests that rely on time passing, use `mr.FastForward(duration)` rather than real `time.Sleep`; the sliding window keys off wall clock via `time.Now()` in `allow`, and miniredis advances its clock with FastForward.

## Conventions enforced by the existing code

- Function calls with ≥2 args: every arg on its own line, opening brace on the call line, closing `)` on its own line (see `NewRedisLimiter`, `pipe.ZAdd`, all of `module.go`).
- Struct literals with ≥1 field: every field on its own line (see `redis.Z`, `PerMerchantStrategy{RPM: ...}` at call sites).
- `var _ Limiter[any] = (*RedisLimiter[any])(nil)` interface compliance check next to the impl — keep this pattern if adding new Limiter implementations.
- Error wrapping uses the package prefix: `fmt.Errorf("ratelimitfx: redis pipeline: %w", err)`.
- Logging via `github.com/rs/zerolog/log` with structured fields (`ratelimit_strategy`, `ratelimit_key`, `ratelimit_fail_mode`) — keep these field names stable; downstream log queries depend on them.
