# ratelimitfx

Redis-backed rate limiter with a pluggable strategy.

- Sliding window via Redis sorted sets, evaluated atomically by a single Lua script (one round trip, no races between concurrent callers).
- Optional GCRA (`Config.Algorithm`): one string per key and O(1) work per request instead of a journal.
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
| `(*RedisLimiter).AllowNWithQuota(ctx, Key, limit)` | method | `AllowN` plus the window quota from the same script call; not part of `Limiter` |
| `Quota` | struct | `Allowed`, `Limit`, `Remaining`, `ResetAt`, `RetryAfter`, `Known` |
| `NewRedisLimiter(client, strategy, cfg)` | ctor | non-generic |
| `Strategy` | interface | `Name() string`, `Key(fmt.Stringer) string`, `Limit() RPM` |
| `PerKeyStrategy` | struct | the only built-in strategy; default cap `120` |
| `Key` | type alias | `= fmt.Stringer` — the caller-identity value |
| `RPM` | microtype | `int32` with `.RPM() int32` |
| `Config` | struct | `Window`, `MaxRequests`, `FailMode`, `KeyPrefix`, `CountRejected`, `Algorithm` |
| `Algorithm` | string type | `AlgorithmSlidingWindow` (`"sliding"`, default), `AlgorithmGCRA` (`"gcra"`), `String()` |
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

## Quota for response headers

`AllowNWithQuota` takes the same decision as `AllowN` and returns the state of the window from the same
Lua call, so `X-RateLimit-*` and `Retry-After` need no second Redis round trip. It is a method of
`*RedisLimiter`, not of the `Limiter` interface.

```go
quota, err := limiter.AllowNWithQuota(ctx, merchantID, rpm)
if err != nil {
    return err
}
if quota.Known {
    w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(quota.Remaining)))
    w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(quota.ResetAt.Unix(), 10))
}
if !quota.Allowed {
    w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(quota.RetryAfter.Seconds()))))
}
```

| Field | Meaning |
|-------|---------|
| `Allowed` | the decision, identical to `AllowN` |
| `Limit` | the `limit` argument |
| `Remaining` | `max(limit - entries in the window, 0)`, counted after this request was recorded |
| `ResetAt` | when the nearest slot frees: oldest entry in the window plus `Window` |
| `RetryAfter` | `0` when allowed, otherwise `ResetAt - now` |
| `Known` | `false` when there is no quota: `limit <= 0` (Redis is not touched) or `FailOpen` after a Redis error |

The method uses the same key, member format, `CountRejected` handling and `FailMode` as `AllowN`; the two
share one window and can be mixed. A Redis error is logged by the same code path as in `AllowN`; with
`FailClosed` it is returned as an error, with `FailOpen` the request is allowed with `Known == false`.

## GCRA

`Config.Algorithm = ratelimit.AlgorithmGCRA` switches `Allow`, `AllowN` and `AllowNWithQuota` from the
sliding-window journal to the Generic Cell Rate Algorithm. The empty and any unknown value keep the
journal; only the exact `"gcra"` selects GCRA, so a gateway can pass its env value through after validating it.

```go
limiter := ratelimit.NewRedisLimiter(
    client,
    ratelimit.PerKeyStrategy{RPM: 1200},
    ratelimit.Config{Algorithm: ratelimit.AlgorithmGCRA},
)
```

- One `EVALSHA` per request, `GET` plus at most one `SET`. The state is a single string: the theoretical
  arrival time (TAT) in microseconds. There is no journal to trim or count.
- The interval between permits is `floor(Window / limit)` (at least 1 microsecond); the burst is `limit`.
  An idle key allows `limit` requests at once, then one per interval.
- Keys are `<KeyPrefix>:gcra:<Strategy.Name()>:<Strategy.Key(k)>`, a separate namespace: they never
  collide with the ZSET keys of the journal, and the two counts are independent.
- `CountRejected: false` leaves the state untouched on a rejection. `true` moves the TAT to
  `now + limit * interval`, so a client that keeps hammering stays blocked and recovers within `Window`
  after it stops.

`Quota` under GCRA:

| Field | Meaning |
|-------|---------|
| `Remaining` | `limit - ceil((TAT - now) / interval)`, clamped to `[0, limit]` |
| `ResetAt` | the TAT after this request: when the bucket is full again. About `now + interval` after one request, not `now + Window` |
| `RetryAfter` | `0` when allowed, otherwise the exact time after which the same request passes. Build `Retry-After` from it, not from `ResetAt - now` |

GCRA is not a copy of the journal. A stream of requests that the journal admits in full is admitted in full
by GCRA, and the mean rate is the same, but once the journal has denied something the two diverge, and GCRA
can deny a request the journal would allow (`limit = 2`, 60 s window, requests at 0, 0, 30, 60, 60 s: the
journal denies the one at 30 s, GCRA denies the last one at 60 s).
Pods running different algorithms count in different keys, so during a rolling update a client can get
up to twice its limit. Switching back starts with an empty window.

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
`ratelimitfx:key-strategy:550e8400-e29b-41d4-a716-446655440000`. With `AlgorithmGCRA` the key is
`<KeyPrefix>:gcra:<Strategy.Name()>:<Strategy.Key(k)>`.

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

The two payment gateways use the same library, also fail closed, and share it between the per-IP and
per-merchant limits:

- payin-gateway-api: `Module`, `CountRejected: true`, key prefix `payin-gw:ratelimit`, 60 s window.
- payout-gateway-api: its own `NewRedisLimiter`, `CountRejected: false`, key prefix `payout-gateway:ratelimit`.

Both call `AllowN` per request; `AllowNWithQuota` (v0.5.0) is meant to replace their second Redis query
for the rate-limit headers. Each gateway chooses the algorithm with its own `RATE_LIMIT_ALGORITHM` env
(`sliding` by default, `gcra`); GCRA is used only together with `AllowNWithQuota`, and `Retry-After` comes from
`Quota.RetryAfter`.

## Testing

Tests use [miniredis](https://github.com/alicebob/miniredis) — no Docker required. When a test relies
on window expiry, drive the limiter's internal clock rather than sleeping.

```bash
go test ./...
```

## License

MIT — see [LICENSE](./LICENSE).
