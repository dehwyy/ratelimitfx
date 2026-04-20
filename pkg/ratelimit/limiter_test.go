package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(
		&redis.Options{
			Addr: mr.Addr(),
		},
	)
	t.Cleanup(func() {
		_ = client.Close()
	})
	return mr, client
}

func TestRedisLimiter_AllowsUpToLimit(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter[uuid.UUID](
		client,
		PerMerchantStrategy{
			MaxRequests: 3,
		},
		Config{},
	)

	ctx := context.Background()
	id := uuid.New()

	for i := 0; i < 3; i++ {
		allowed, err := limiter.Allow(ctx, id)
		require.NoError(t, err)
		require.True(t, allowed, "request %d must be allowed", i+1)
	}

	allowed, err := limiter.Allow(ctx, id)
	require.NoError(t, err)
	require.False(t, allowed, "4th request must be denied")
}

func TestRedisLimiter_SlidingWindowReleasesAfterWindow(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter[uuid.UUID](
		client,
		PerMerchantStrategy{
			MaxRequests: 2,
		},
		Config{
			Window: time.Second,
		},
	)

	ctx := context.Background()
	id := uuid.New()

	for i := 0; i < 2; i++ {
		allowed, err := limiter.Allow(ctx, id)
		require.NoError(t, err)
		require.True(t, allowed)
	}

	allowed, err := limiter.Allow(ctx, id)
	require.NoError(t, err)
	require.False(t, allowed)

	mr.FastForward(2 * time.Second)

	allowed, err = limiter.Allow(ctx, id)
	require.NoError(t, err)
	require.True(t, allowed, "window must release prior entries")
}

func TestRedisLimiter_KeysAreStrategyScoped(t *testing.T) {
	mr, client := newTestClient(t)

	merchantLimiter := NewRedisLimiter[uuid.UUID](
		client,
		PerMerchantStrategy{
			MaxRequests: 1,
		},
		Config{},
	)
	ipLimiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: 1,
		},
		Config{},
	)

	ctx := context.Background()
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	allowed, err := merchantLimiter.Allow(ctx, id)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = ipLimiter.Allow(ctx, id.String())
	require.NoError(t, err)
	require.True(t, allowed, "IP strategy uses a different key namespace than merchant strategy")

	keys := mr.Keys()
	require.Len(t, keys, 2)
	var foundMerchant, foundIP bool
	for _, k := range keys {
		switch {
		case k == "ratelimitfx:merchant:"+id.String():
			foundMerchant = true
		case k == "ratelimitfx:ip:"+id.String():
			foundIP = true
		}
	}
	require.True(t, foundMerchant, "merchant key must exist: keys=%v", keys)
	require.True(t, foundIP, "ip key must exist: keys=%v", keys)
}

func TestRedisLimiter_CustomKeyPrefix(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: 1,
		},
		Config{
			KeyPrefix: "widgetapi_rl",
		},
	)

	ctx := context.Background()
	_, err := limiter.Allow(ctx, "1.2.3.4")
	require.NoError(t, err)

	require.Contains(t, mr.Keys(), "widgetapi_rl:ip:1.2.3.4")
}

func TestRedisLimiter_FailClosedOnRedisError(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: 10,
		},
		Config{
			FailMode: FailClosed,
		},
	)

	mr.Close()

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, "1.2.3.4")
	require.Error(t, err)
	require.False(t, allowed, "fail-closed must deny on redis error")
}

func TestRedisLimiter_FailOpenOnRedisError(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: 10,
		},
		Config{
			FailMode: FailOpen,
		},
	)

	mr.Close()

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, "1.2.3.4")
	require.NoError(t, err, "fail-open must swallow redis error")
	require.True(t, allowed)
}

func TestRedisLimiter_ZeroLimitAllowsAll(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: -1,
		},
		Config{},
	)

	allowed, err := limiter.Allow(context.Background(), "1.2.3.4")
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestRedisLimiter_AllowNOverridesStrategyLimit(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter[uuid.UUID](
		client,
		PerMerchantStrategy{
			MaxRequests: 1000,
		},
		Config{},
	)

	ctx := context.Background()
	id := uuid.New()

	for i := 0; i < 3; i++ {
		allowed, err := limiter.AllowN(ctx, id, 3)
		require.NoError(t, err)
		require.True(t, allowed, "request %d must be allowed under per-request limit of 3", i+1)
	}

	allowed, err := limiter.AllowN(ctx, id, 3)
	require.NoError(t, err)
	require.False(t, allowed, "4th request must be denied despite strategy allowing 1000")
}

func TestRedisLimiter_AllowNZeroLimitAllowsAll(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter[string](
		client,
		PerIPStrategy{
			MaxRequests: 60,
		},
		Config{},
	)

	allowed, err := limiter.AllowN(context.Background(), "1.2.3.4", 0)
	require.NoError(t, err)
	require.True(t, allowed)
}
