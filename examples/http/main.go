// Example: per-IP rate limiter as an HTTP middleware.
//
// Run: go run ./examples/http
// Requires a Redis instance at localhost:6379 (override via REDIS_ADDR).
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/dehwyy/ratelimitfx/pkg/ratelimit"
	"github.com/redis/go-redis/v9"
)

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(
		&redis.Options{
			Addr: addr,
		},
	)
	defer client.Close()

	limiter := ratelimit.NewRedisLimiter[string](
		client,
		ratelimit.PerIPStrategy{
			MaxRequests: 10,
		},
		ratelimit.Config{
			FailMode: ratelimit.FailClosed,
		},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "hello")
	})

	handler := rateLimitMiddleware(limiter)(mux)

	_ = http.ListenAndServe(":8080", handler)
}

func rateLimitMiddleware(
	limiter ratelimit.Limiter[string],
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ratelimit.ClientIP(r)
			allowed, err := limiter.Allow(r.Context(), ip)
			if err != nil {
				http.Error(
					w,
					"rate limit check failed",
					http.StatusTooManyRequests,
				)
				return
			}
			if !allowed {
				http.Error(
					w,
					"rate limit exceeded",
					http.StatusTooManyRequests,
				)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
