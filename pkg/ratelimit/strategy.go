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
	Limit() RPM
}

// PerMerchantStrategy keys by merchant UUID. Default cap 120 rpm.
type PerMerchantStrategy struct {
	RPM RPM
}

const DefaultPerMerchantLimit RPM = 120

func (s PerMerchantStrategy) Name() string { return "merchant" }

func (s PerMerchantStrategy) Key(id uuid.UUID) string { return id.String() }

func (s PerMerchantStrategy) Limit() RPM {
	if s.RPM <= 0 {
		return DefaultPerMerchantLimit
	}
	return s.RPM
}

// PerIPStrategy keys by client IP string. Default cap 60 rpm.
type PerIPStrategy struct {
	RPM RPM
}

const DefaultPerIPLimit RPM = 60

func (s PerIPStrategy) Name() string { return "ip" }

func (s PerIPStrategy) Key(ip IP) string { return ip.String() }

func (s PerIPStrategy) Limit() RPM {
	if s.RPM <= 0 {
		return DefaultPerIPLimit
	}
	return s.RPM
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
