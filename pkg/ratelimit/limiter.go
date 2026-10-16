package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

var memberSeq uint64

var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]
local ttl = tonumber(ARGV[5])
local countRejected = ARGV[6] == '1'

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local allowed = redis.call('ZCARD', key) < limit
if allowed or countRejected then
	redis.call('ZADD', key, now, member)
end
redis.call('PEXPIRE', key, ttl)
if allowed then
	return 1
end
return 0
`)

// Limiter checks whether a request identified by K is allowed within the configured window.
// Allow uses the Strategy.Limit() static cap; AllowN takes a per-request limit for cases
// where the effective cap depends on runtime state (e.g. a per-tenant config loaded from cache).
type Limiter interface {
	Allow(ctx context.Context, k Key) (bool, error)
	AllowN(ctx context.Context, k Key, limit int32) (bool, error)
}

// RedisLimiter is a Redis sliding-window implementation of Limiter.
type RedisLimiter struct {
	client   redis.Cmdable
	strategy Strategy
	cfg      Config
	now      func() time.Time
}

var _ Limiter = (*RedisLimiter)(nil)

// NewRedisLimiter builds a RedisLimiter with the given client, strategy, and config.
func NewRedisLimiter(
	client redis.Cmdable,
	strategy Strategy,
	cfg Config,
) *RedisLimiter {
	return &RedisLimiter{
		client:   client,
		strategy: strategy,
		cfg:      cfg.withDefaults(),
		now:      time.Now,
	}
}

// Allow reports whether k is below the strategy's static limit within the current sliding window.
// On Redis error the decision follows Config.FailMode.
func (l *RedisLimiter) Allow(
	ctx context.Context,
	k Key,
) (bool, error) {
	return l.allow(
		ctx,
		k,
		l.strategy.Limit().RPM(),
	)
}

// AllowN reports whether k is below the given per-request limit within the current sliding window.
// The Strategy's Limit() is ignored. Use this when the effective cap depends on runtime state
// the Strategy cannot express (e.g. per-tenant config loaded from cache each request).
// On Redis error the decision follows Config.FailMode.
func (l *RedisLimiter) AllowN(
	ctx context.Context,
	k Key,
	limit int32,
) (bool, error) {
	return l.allow(
		ctx,
		k,
		limit,
	)
}

func (l *RedisLimiter) allow(
	ctx context.Context,
	k Key,
	limit int32,
) (bool, error) {
	if limit <= 0 {
		return true, nil
	}

	if l.cfg.Algorithm == AlgorithmGCRA {
		quota, err := l.gcra(
			ctx,
			k,
			limit,
		)
		return quota.Allowed, err
	}

	key := l.windowKey(k)
	now := l.now()

	result, err := slidingWindowScript.Run(
		ctx,
		l.client,
		[]string{key},
		l.windowArgs(
			now,
			limit,
		)...,
	).Int64()
	if err != nil {
		return l.scriptFailed(
			err,
			key,
		)
	}

	return result == 1, nil
}

func failModeString(m FailMode) string {
	switch m {
	case FailOpen:
		return "fail_open"
	default:
		return "fail_closed"
	}
}
