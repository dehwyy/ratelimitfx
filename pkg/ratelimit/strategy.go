package ratelimit

import "fmt"

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
