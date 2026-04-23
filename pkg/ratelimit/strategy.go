package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Strategy maps a caller-identity value K to a Redis key fragment and a request cap.
// Name is used in the Redis key prefix to prevent collisions between strategies.
type Strategy interface {
	Name() string
	Key(k fmt.Stringer) string
	Limit() RPM
}

// PerKeyStrategy keys by merchant UUID. Default cap 120 rpm.
type PerKeyStrategy struct {
	RPM RPM
}

const DefaultPerKeyLimit RPM = 120

func (s PerKeyStrategy) Name() string { return "key-strategy" }

func (s PerKeyStrategy) Key(key fmt.Stringer) string { return key.String() }

func (s PerKeyStrategy) Limit() RPM {
	if s.RPM <= 0 {
		return DefaultPerKeyLimit
	}
	return s.RPM
}

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
