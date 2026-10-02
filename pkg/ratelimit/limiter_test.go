package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type testKey string

func (k testKey) String() string { return string(k) }

type testStrategy string

func (s testStrategy) Name() string              { return string(s) }
func (s testStrategy) Key(k fmt.Stringer) string { return k.String() }
func (s testStrategy) Limit() RPM                { return 1 }

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

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 3,
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

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 2,
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

	merchantLimiter := NewRedisLimiter(
		client,
		testStrategy("merchant"),
		Config{},
	)
	ipLimiter := NewRedisLimiter(
		client,
		testStrategy("ip"),
		Config{},
	)

	ctx := context.Background()
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	allowed, err := merchantLimiter.Allow(ctx, id)
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = ipLimiter.Allow(ctx, id)
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

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 1,
		},
		Config{
			KeyPrefix: "widgetapi_rl",
		},
	)

	ctx := context.Background()
	_, err := limiter.Allow(ctx, testKey("1.2.3.4"))
	require.NoError(t, err)

	require.Contains(t, mr.Keys(), "widgetapi_rl:key-strategy:1.2.3.4")
}

func TestRedisLimiter_FailClosedOnRedisError(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 10,
		},
		Config{
			FailMode: FailClosed,
		},
	)

	mr.Close()

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, testKey("1.2.3.4"))
	require.Error(t, err)
	require.False(t, allowed, "fail-closed must deny on redis error")
}

func TestRedisLimiter_FailOpenOnRedisError(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 10,
		},
		Config{
			FailMode: FailOpen,
		},
	)

	mr.Close()

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, testKey("1.2.3.4"))
	require.NoError(t, err, "fail-open must swallow redis error")
	require.True(t, allowed)
}

func TestRedisLimiter_ZeroLimitAllowsAll(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: -1,
		},
		Config{},
	)

	allowed, err := limiter.Allow(context.Background(), testKey("1.2.3.4"))
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestRedisLimiter_AllowNOverridesStrategyLimit(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 1000,
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

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 60,
		},
		Config{},
	)

	allowed, err := limiter.AllowN(context.Background(), testKey("1.2.3.4"), 0)
	require.NoError(t, err)
	require.True(t, allowed)
}

func windowCount(t *testing.T, mr *miniredis.Miniredis, key string) int {
	t.Helper()
	members, err := mr.ZMembers(key)
	if err != nil {
		return 0
	}
	return len(members)
}

func TestRedisLimiter_CountsAllowedRequests(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 3,
		},
		Config{},
	)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.True(t, allowed)
	}

	require.Equal(t, 3, windowCount(t, mr, "ratelimitfx:key-strategy:a"))
}

func TestRedisLimiter_RejectedNotCountedByDefault(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 2,
		},
		Config{},
	)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.True(t, allowed)
	}
	for i := 0; i < 5; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.False(t, allowed)
	}

	require.Equal(t, 2, windowCount(t, mr, "ratelimitfx:key-strategy:a"))
}

func TestRedisLimiter_RejectedCountedWhenEnabled(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 2,
		},
		Config{
			CountRejected: true,
		},
	)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.True(t, allowed)
	}
	for i := 0; i < 5; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.False(t, allowed)
	}

	require.Equal(t, 7, windowCount(t, mr, "ratelimitfx:key-strategy:a"))
}

func TestRedisLimiter_HammeringClientRecoversWhenRejectedNotCounted(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 1,
		},
		Config{
			Window: time.Minute,
		},
	)
	clock := time.Now()
	limiter.now = func() time.Time { return clock }

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.True(t, allowed)

	for i := 0; i < 10; i++ {
		clock = clock.Add(5 * time.Second)
		allowed, err = limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.False(t, allowed)
	}

	clock = clock.Add(11 * time.Second)
	allowed, err = limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.True(t, allowed, "first allowed request has left the window, hammering must not extend the block")
}

func TestRedisLimiter_HammeringClientStaysBlockedWhenRejectedCounted(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 1,
		},
		Config{
			Window:        time.Minute,
			CountRejected: true,
		},
	)
	clock := time.Now()
	limiter.now = func() time.Time { return clock }

	ctx := context.Background()
	allowed, err := limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.True(t, allowed)

	for i := 0; i < 10; i++ {
		clock = clock.Add(5 * time.Second)
		allowed, err = limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.False(t, allowed)
	}

	clock = clock.Add(11 * time.Second)
	allowed, err = limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.False(t, allowed, "rejected requests keep the window full")
}

func TestRedisLimiter_WindowExpiryDropsOldEntries(t *testing.T) {
	mr, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 2,
		},
		Config{
			Window: time.Minute,
		},
	)
	clock := time.Now()
	limiter.now = func() time.Time { return clock }

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		allowed, err := limiter.Allow(ctx, testKey("a"))
		require.NoError(t, err)
		require.True(t, allowed)
		clock = clock.Add(20 * time.Second)
	}

	allowed, err := limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.False(t, allowed)

	clock = clock.Add(25 * time.Second)
	allowed, err = limiter.Allow(ctx, testKey("a"))
	require.NoError(t, err)
	require.True(t, allowed, "oldest entry left the window")

	require.Equal(t, 2, windowCount(t, mr, "ratelimitfx:key-strategy:a"))
}

func TestRedisLimiter_ConcurrentAllowIsAtomic(t *testing.T) {
	_, client := newTestClient(t)

	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 10,
		},
		Config{},
	)

	ctx := context.Background()
	var allowedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, err := limiter.Allow(ctx, testKey("a"))
			if err == nil && allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	require.EqualValues(t, 10, allowedCount.Load())
}
