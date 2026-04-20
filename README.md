# ratelimitfx

Redis-backed sliding-window rate limiter with pluggable strategies and an Uber FX module.

- Sliding window via Redis sorted sets (ZADD + ZREMRANGEBYSCORE + ZCARD in a single pipeline).
- Generic `Limiter[K any]` interface — mock-friendly, strategy-scoped Redis keys.
- Built-in strategies: per-merchant (UUID) and per-IP (string) — add your own via `Strategy[K]`.
- `FailMode` — `FailClosed` (default, deny on Redis error) or `FailOpen` (allow).
- Optional `fx.Module` wiring for services using `go.uber.org/fx`.

## Install

```bash
go get github.com/dehwyy/ratelimitfx@latest
```

## Quick start — per-IP HTTP middleware

```go
import (
    "net/http"

    "github.com/dehwyy/ratelimitfx/pkg/ratelimit"
    "github.com/redis/go-redis/v9"
)

client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
limiter := ratelimit.NewRedisLimiter[string](
    client,
    ratelimit.PerIPStrategy{MaxRequests: 60},
    ratelimit.Config{FailMode: ratelimit.FailClosed},
)

mw := func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        allowed, err := limiter.Allow(r.Context(), ratelimit.ClientIP(r))
        if err != nil || !allowed {
            http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

Full example at [`examples/http`](./examples/http).

## Per-merchant limiting

```go
import "github.com/google/uuid"

limiter := ratelimit.NewRedisLimiter[uuid.UUID](
    client,
    ratelimit.PerMerchantStrategy{MaxRequests: 120},
    ratelimit.Config{},
)

allowed, err := limiter.Allow(ctx, merchantID)
```

## Custom strategy

```go
type PerAPIKeyStrategy struct{ Max int32 }

func (s PerAPIKeyStrategy) Name() string             { return "apikey" }
func (s PerAPIKeyStrategy) Key(k string) string      { return k }
func (s PerAPIKeyStrategy) Limit() int32             { return s.Max }
```

## Config defaults

| Field         | Default                                    |
|---------------|--------------------------------------------|
| `Window`      | `1m`                                       |
| `MaxRequests` | from `Strategy.Limit()` (120 merchant / 60 IP) |
| `FailMode`    | `FailClosed`                               |
| `KeyPrefix`   | `ratelimitfx`                              |

Redis keys are `<KeyPrefix>:<Strategy.Name()>:<Strategy.Key(k)>`, e.g.
`ratelimitfx:merchant:550e8400-e29b-41d4-a716-446655440000`.

## FX usage

```go
import "github.com/dehwyy/ratelimitfx/pkg/ratelimit"

fx.New(
    fx.Provide(
        newRedisClient,          // your *redis.Client
        func() ratelimit.Config { return ratelimit.Config{} },
    ),
    ratelimit.ModuleMerchant,    // provides Limiter[uuid.UUID]
    ratelimit.ModuleIP,          // provides Limiter[string]
    fx.Invoke(runServer),
)
```

## Fail-mode semantics

`FailClosed` logs the Redis error at `Error` level and returns `(false, err)` — the caller
denies the request and surfaces the error. `FailOpen` logs the error and returns `(true, nil)`
— the caller lets the request through.

## Testing

Tests use [miniredis](https://github.com/alicebob/miniredis) — no Docker required.

```bash
go test ./...
```

## License

MIT — see [LICENSE](./LICENSE).
