# ratelimitfx

Redis-backed sliding-window rate limiter with a pluggable strategy.

- Sliding window via Redis sorted sets, evaluated atomically by a single Lua script (one round trip, no races between concurrent callers).
- `Limiter` interface — small, mock-friendly, strategy-scoped Redis keys.
- Built-in `PerKeyStrategy` keyed by any `fmt.Stringer` — bring your own strategy via the `Strategy` interface.
- `FailMode` — `FailClosed` (default, deny on Redis error) or `FailOpen` (allow).

This library has no fx wiring. There is no `module.go`. Each consuming service hand-rolls its own
`fx.Provide` around `NewRedisLimiter` and the strategy.

## Install

```bash
go get github.com/dehwyy/ratelimitfx@latest
```

## API surface

| Symbol | Kind | Notes |
|--------|------|-------|
| `Limiter` | interface | `Allow(ctx, Key)` / `AllowN(ctx, Key, limit)` |
| `RedisLimiter` | struct | the only `Limiter` implementation |
| `NewRedisLimiter(client, strategy, cfg)` | ctor | non-generic |
| `Strategy` | interface | `Name() string`, `Key(fmt.Stringer) string`, `Limit() RPM` |
| `PerKeyStrategy` | struct | the only built-in strategy; default cap `120` |
| `Key` | type alias | `= fmt.Stringer` — the caller-identity value |
| `RPM` | microtype | `int32` with `.RPM() int32` |
| `Config` | struct | `Window`, `MaxRequests`, `FailMode`, `KeyPrefix`, `CountRejected` |
| `ClientIP(r, trusted ...netip.Prefix) string` | func | client IP; `X-Forwarded-For` / `X-Real-IP` are honoured only when `RemoteAddr` is a trusted proxy |
| `ParseTrustedProxies([]string) ([]netip.Prefix, error)` | func | parses CIDRs and bare IPs |
| `StringKey` | microtype | `string` implementing `fmt.Stringer`, ready to use as a `Key` |
| `Module[C](func(*C) Config)` | fx option | provides `*RedisLimiter` and `Limiter` |

There is no `IP` type, no `PerIPStrategy`, and no `PerMerchantStrategy`. Identity values are passed as
any `fmt.Stringer`; if you want to key by IP, wrap the `ClientIP` string in `StringKey`.

## Quick start — HTTP middleware

```go
import (
    "net/http"

    "github.com/dehwyy/ratelimitfx/pkg/ratelimit"
    "github.com/redis/go-redis/v9"
)

client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
limiter := ratelimit.NewRedisLimiter(
    client,
    ratelimit.PerKeyStrategy{RPM: 60},
    ratelimit.Config{FailMode: ratelimit.FailClosed},
)

mw := func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        allowed, err := limiter.Allow(r.Context(), ratelimit.StringKey(ratelimit.ClientIP(r, trusted...)))
        if err != nil || !allowed {
            http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

Full example at [`examples/http`](./examples/http).

## Client IP and trusted proxies

`ClientIP` never trusts forwarding headers from an arbitrary peer. With no trusted proxies it returns
the host of `RemoteAddr` and ignores `X-Forwarded-For` and `X-Real-IP`, so a client cannot choose its
own rate-limit key by spoofing a header.

```go
trusted, err := ratelimit.ParseTrustedProxies([]string{"10.0.0.0/8", "127.0.0.1"})
ip := ratelimit.ClientIP(r, trusted...)
```

When `RemoteAddr` is inside a trusted prefix, the `X-Forwarded-For` chain is walked from the right and
the first address that is not a trusted proxy is the client. Entries a client prepends on the left are
never reached. An unparsable entry stops the walk at the last valid hop. `X-Real-IP` is used only when
there is no `X-Forwarded-For` and the peer is trusted.

## fx module

```go
ratelimit.Module(func(cfg *config.Config) ratelimit.Config {
    return ratelimit.Config{KeyPrefix: cfg.RateLimitPrefix}
})
```

The graph must contain a `*redis.Client` or `redis.Cmdable`; a `ratelimit.Strategy` is optional
(default `PerKeyStrategy{}`). The module provides `*RedisLimiter` and `Limiter`.

## Dynamic per-request limits

Use `AllowN` when the effective cap depends on runtime state — for example a per-tenant
configuration loaded from cache. The Strategy's `Limit()` is ignored; the Strategy still provides
the Redis key name and namespace, only the cap changes per call.

```go
rpm, _ := configCache.Get(ctx, merchantID) // varies per merchant
allowed, err := limiter.AllowN(ctx, merchantID, rpm)
```

## Custom strategy

```go
type apiKeyStrategy struct{ max ratelimit.RPM }

func (s apiKeyStrategy) Name() string                  { return "apikey" }
func (s apiKeyStrategy) Key(k fmt.Stringer) string     { return k.String() }
func (s apiKeyStrategy) Limit() ratelimit.RPM          { return s.max }
```

## Config defaults

| Field       | Default        |
|-------------|----------------|
| `Window`    | `1m`           |
| `FailMode`  | `FailClosed`   |
| `KeyPrefix` | `ratelimitfx`  |
| `CountRejected` | `false` (only allowed requests count) |

The static cap comes from `Strategy.Limit()` (`PerKeyStrategy` default is `120`).
`RPM` is named for the default minute window; if you override `Window`, the value becomes
requests-per-window. A cap of `<= 0` disables limiting and short-circuits without touching Redis.

Redis keys are `<KeyPrefix>:<Strategy.Name()>:<Strategy.Key(k)>`, e.g.
`ratelimitfx:key-strategy:550e8400-e29b-41d4-a716-446655440000`.

## Counting rejected requests

By default (`CountRejected: false`) only allowed requests are recorded in the window. A client that
keeps sending requests while blocked does not extend its own block: it is let through again as soon
as its earlier allowed requests leave the window.

With `CountRejected: true` rejected requests are recorded too, so a client that keeps hammering stays
blocked until it stops for a full window (the behaviour of v0.3.0 and earlier).

```go
limiter := ratelimit.NewRedisLimiter(
    client,
    ratelimit.PerKeyStrategy{RPM: 60},
    ratelimit.Config{CountRejected: true},
)
```

See [CHANGELOG](./CHANGELOG.md) for the upgrade note.

## Fail-mode semantics

The decision is a single atomic Lua script (`EVALSHA`, falling back to `EVAL`), so it requires a Redis
that supports scripting. `FailClosed` (default) logs the Redis error at `Error` level and returns `(false, err)` — the caller
denies the request and surfaces the error. `FailOpen` logs the error and returns `(true, nil)` — the
caller lets the request through.

## How Paylonium uses it

Redis-backed consumers (merchant-api, widgetapi, payin-processing) use this library and run fail
closed: when Redis is unreachable they deny the request.

The two payment gateways (payin-gateway-api, payout-gateway-api) do **not** use this Redis limiter.
They run an in-process `golang.org/x/time/rate` token bucket per merchant (see each service's
`pkg/ratelimit`). That limiter is **per-pod**: with `N` replicas the effective global cap is
`N × rps`. On a per-merchant RPS lookup error those gateways also fail closed (return `503`).

## Testing

Tests use [miniredis](https://github.com/alicebob/miniredis) — no Docker required. When a test relies
on window expiry, drive the limiter's internal clock rather than sleeping.

```bash
go test ./...
```

## License

MIT — see [LICENSE](./LICENSE).
