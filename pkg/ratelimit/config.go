package ratelimit

import "time"

// FailMode controls behavior when the underlying Redis call fails.
type FailMode int

const (
	// FailClosed denies the request when Redis is unavailable.
	FailClosed FailMode = iota
	// FailOpen allows the request when Redis is unavailable.
	FailOpen
)

// Config configures a Limiter.
type Config struct {
	// Window is the sliding window size. Default 1m.
	Window time.Duration
	// MaxRequests is the per-window cap. Default depends on Strategy.
	MaxRequests int32
	// FailMode controls behavior on Redis errors. Default FailClosed.
	FailMode FailMode
	// KeyPrefix is prepended to every Redis key. Default "ratelimitfx".
	KeyPrefix string
}

const (
	DefaultWindow    = time.Minute
	DefaultKeyPrefix = "ratelimitfx"
)

func (c Config) withDefaults() Config {
	if c.Window <= 0 {
		c.Window = DefaultWindow
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = DefaultKeyPrefix
	}
	return c
}
