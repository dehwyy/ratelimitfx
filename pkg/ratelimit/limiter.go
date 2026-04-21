package ratelimit

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

var memberSeq uint64

// Limiter checks whether a request identified by K is allowed within the configured window.
// Allow uses the Strategy.Limit() static cap; AllowN takes a per-request limit for cases
// where the effective cap depends on runtime state (e.g. a per-tenant config loaded from cache).
type Limiter[K any] interface {
	Allow(ctx context.Context, k K) (bool, error)
	AllowN(ctx context.Context, k K, limit int32) (bool, error)
}

// RedisLimiter is a Redis sliding-window implementation of Limiter.
type RedisLimiter[K any] struct {
	client   redis.Cmdable
	strategy Strategy[K]
	cfg      Config
}

var _ Limiter[any] = (*RedisLimiter[any])(nil)

// NewRedisLimiter builds a RedisLimiter with the given client, strategy, and config.
func NewRedisLimiter[K any](
	client redis.Cmdable,
	strategy Strategy[K],
	cfg Config,
) *RedisLimiter[K] {
	return &RedisLimiter[K]{
		client:   client,
		strategy: strategy,
		cfg:      cfg.withDefaults(),
	}
}

// Allow reports whether k is below the strategy's static limit within the current sliding window.
// On Redis error the decision follows Config.FailMode.
func (l *RedisLimiter[K]) Allow(
	ctx context.Context,
	k K,
) (bool, error) {
	return l.allow(ctx, k, l.strategy.Limit().RPM())
}

// AllowN reports whether k is below the given per-request limit within the current sliding window.
// The Strategy's Limit() is ignored. Use this when the effective cap depends on runtime state
// the Strategy cannot express (e.g. per-tenant config loaded from cache each request).
// On Redis error the decision follows Config.FailMode.
func (l *RedisLimiter[K]) AllowN(
	ctx context.Context,
	k K,
	limit int32,
) (bool, error) {
	return l.allow(ctx, k, limit)
}

func (l *RedisLimiter[K]) allow(
	ctx context.Context,
	k K,
	limit int32,
) (bool, error) {
	if limit <= 0 {
		return true, nil
	}

	key := fmt.Sprintf(
		"%s:%s:%s",
		l.cfg.KeyPrefix,
		l.strategy.Name(),
		l.strategy.Key(k),
	)

	now := time.Now()
	windowStart := now.Add(-l.cfg.Window).UnixMilli()
	member := fmt.Sprintf(
		"%d-%d",
		now.UnixNano(),
		atomic.AddUint64(&memberSeq, 1),
	)
	ttl := l.cfg.Window * 2

	pipe := l.client.Pipeline()
	pipe.ZRemRangeByScore(
		ctx,
		key,
		"0",
		fmt.Sprintf("%d", windowStart),
	)
	pipe.ZAdd(
		ctx,
		key,
		redis.Z{
			Score:  float64(now.UnixMilli()),
			Member: member,
		},
	)
	countCmd := pipe.ZCard(ctx, key)
	pipe.Expire(
		ctx,
		key,
		ttl,
	)

	if _, err := pipe.Exec(ctx); err != nil {
		log.Error().
			Err(err).
			Str("ratelimit_strategy", l.strategy.Name()).
			Str("ratelimit_key", key).
			Str("ratelimit_fail_mode", failModeString(l.cfg.FailMode)).
			Msg("ratelimitfx: redis pipeline failed")
		if l.cfg.FailMode == FailOpen {
			return true, nil
		}
		return false, fmt.Errorf("ratelimitfx: redis pipeline: %w", err)
	}

	return countCmd.Val() <= int64(limit), nil
}

func failModeString(m FailMode) string {
	switch m {
	case FailOpen:
		return "fail_open"
	default:
		return "fail_closed"
	}
}
