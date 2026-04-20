package ratelimit

import (
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Strategy maps a caller-identity value K to a Redis key fragment and a request cap.
// Name is used in the Redis key prefix to prevent collisions between strategies.
type Strategy[K any] interface {
	Name() string
	Key(k K) string
	Limit() int32
}

// PerMerchantStrategy keys by merchant UUID. Default cap 120 rpm.
type PerMerchantStrategy struct {
	MaxRequests int32
}

const DefaultPerMerchantLimit int32 = 120

func (s PerMerchantStrategy) Name() string { return "merchant" }

func (s PerMerchantStrategy) Key(id uuid.UUID) string { return id.String() }

func (s PerMerchantStrategy) Limit() int32 {
	if s.MaxRequests <= 0 {
		return DefaultPerMerchantLimit
	}
	return s.MaxRequests
}

// PerIPStrategy keys by client IP string. Default cap 60 rpm.
type PerIPStrategy struct {
	MaxRequests int32
}

const DefaultPerIPLimit int32 = 60

func (s PerIPStrategy) Name() string { return "ip" }

func (s PerIPStrategy) Key(ip string) string { return ip }

func (s PerIPStrategy) Limit() int32 {
	if s.MaxRequests <= 0 {
		return DefaultPerIPLimit
	}
	return s.MaxRequests
}

// ClientIP extracts a client IP from an HTTP request.
// Preference: first entry of X-Forwarded-For, else X-Real-IP, else RemoteAddr host.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
