// Example: per-IP rate limiter as an HTTP middleware.
//
// Run: go run ./examples/http
// Requires a Redis instance at localhost:6379 (override via REDIS_ADDR).
// Set TRUSTED_PROXIES (comma-separated CIDRs/IPs) to honour X-Forwarded-For from your ingress.
package main

import (
	"log"
	"net/http"
	"net/netip"
	"os"
	"strings"

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

	limiter := ratelimit.NewRedisLimiter(
		client,
		ratelimit.PerKeyStrategy{
			RPM: 10,
		},
		ratelimit.Config{
			FailMode: ratelimit.FailClosed,
		},
	)

	trusted, err := ratelimit.ParseTrustedProxies(strings.Split(os.Getenv("TRUSTED_PROXIES"), ","))
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("hello\n")); err != nil {
			log.Print(err)
		}
	})

	handler := rateLimitMiddleware(limiter, trusted)(mux)

	log.Fatal(http.ListenAndServe(":8080", handler))
}

func rateLimitMiddleware(
	limiter ratelimit.Limiter,
	trusted []netip.Prefix,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ratelimit.StringKey(ratelimit.ClientIP(r, trusted...))
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
