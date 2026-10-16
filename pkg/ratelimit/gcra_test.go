package ratelimit

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const gcraTestKey = "ratelimitfx:gcra:key-strategy:a"

var gcraBase = time.UnixMicro(1_760_000_000_000_000)

func newGCRALimiter(
	t *testing.T,
	rpm RPM,
	cfg Config,
) (*miniredis.Miniredis, *redis.Client, *RedisLimiter, *quotaClock) {
	t.Helper()
	cfg.Algorithm = AlgorithmGCRA
	mr, client := newTestClient(t)
	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: rpm,
		},
		cfg,
	)
	clock := &quotaClock{
		current: gcraBase,
	}
	limiter.now = clock.now
	return mr, client, limiter, clock
}

func gcraStoredTAT(
	t *testing.T,
	mr *miniredis.Miniredis,
) int64 {
	t.Helper()
	stored, err := mr.Get(gcraTestKey)
	require.NoError(
		t,
		err,
	)
	tat, err := strconv.ParseInt(
		stored,
		10,
		64,
	)
	require.NoError(
		t,
		err,
	)
	return tat
}

func gcraAllow(
	t *testing.T,
	limiter *RedisLimiter,
	limit int32,
) Quota {
	t.Helper()
	quota, err := limiter.AllowNWithQuota(
		context.Background(),
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	return quota
}

func TestAlgorithm_String(t *testing.T) {
	require.Equal(
		t,
		"sliding",
		AlgorithmSlidingWindow.String(),
	)
	require.Equal(
		t,
		"gcra",
		AlgorithmGCRA.String(),
	)
	require.Equal(
		t,
		"",
		Algorithm("").String(),
	)
}

func TestGCRA_BurstIsExactlyLimit(t *testing.T) {
	tests := []struct {
		limit    int32
		interval time.Duration
	}{
		{
			limit:    7,
			interval: 8571428 * time.Microsecond,
		},
		{
			limit:    1200,
			interval: 50 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(
			strconv.Itoa(int(tt.limit)),
			func(t *testing.T) {
				_, _, limiter, _ := newGCRALimiter(
					t,
					RPM(tt.limit),
					Config{},
				)

				for i := range tt.limit {
					quota := gcraAllow(
						t,
						limiter,
						tt.limit,
					)
					require.True(
						t,
						quota.Allowed,
					)
					require.True(
						t,
						quota.Known,
					)
					require.Equal(
						t,
						tt.limit,
						quota.Limit,
					)
					require.Equal(
						t,
						tt.limit-1-i,
						quota.Remaining,
					)
					require.Equal(
						t,
						gcraBase.Add(tt.interval*time.Duration(i+1)),
						quota.ResetAt,
					)
					require.Zero(
						t,
						quota.RetryAfter,
					)
				}

				denied := gcraAllow(
					t,
					limiter,
					tt.limit,
				)
				require.False(
					t,
					denied.Allowed,
				)
				require.True(
					t,
					denied.Known,
				)
				require.Zero(
					t,
					denied.Remaining,
				)
				require.Equal(
					t,
					tt.interval,
					denied.RetryAfter,
				)
				require.Equal(
					t,
					gcraBase.Add(tt.interval*time.Duration(tt.limit)),
					denied.ResetAt,
				)
			},
		)
	}
}

func TestGCRA_BurstFromNearlyFullBucket(t *testing.T) {
	tests := []struct {
		limit    int32
		interval int64
	}{
		{
			limit:    7,
			interval: 8571428,
		},
		{
			limit:    1200,
			interval: 50_000,
		},
		{
			limit:    1_000_000,
			interval: 60,
		},
	}

	for _, tt := range tests {
		t.Run(
			strconv.Itoa(int(tt.limit)),
			func(t *testing.T) {
				mr, _, limiter, _ := newGCRALimiter(
					t,
					RPM(tt.limit),
					Config{},
				)
				require.NoError(
					t,
					mr.Set(
						gcraTestKey,
						strconv.FormatInt(
							gcraBase.UnixMicro()+int64(tt.limit-1)*tt.interval,
							10,
						),
					),
				)

				last := gcraAllow(
					t,
					limiter,
					tt.limit,
				)
				require.True(
					t,
					last.Allowed,
				)
				require.Zero(
					t,
					last.Remaining,
				)
				require.Equal(
					t,
					gcraBase.UnixMicro()+int64(tt.limit)*tt.interval,
					last.ResetAt.UnixMicro(),
				)

				denied := gcraAllow(
					t,
					limiter,
					tt.limit,
				)
				require.False(
					t,
					denied.Allowed,
				)
				require.Zero(
					t,
					denied.Remaining,
				)
				require.Equal(
					t,
					time.Duration(tt.interval)*time.Microsecond,
					denied.RetryAfter,
				)
			},
		)
	}
}

func TestGCRA_FirstRequestOnLargeLimit(t *testing.T) {
	mr, _, limiter, _ := newGCRALimiter(
		t,
		1_000_000,
		Config{},
	)

	quota := gcraAllow(
		t,
		limiter,
		1_000_000,
	)

	require.True(
		t,
		quota.Allowed,
	)
	require.Equal(
		t,
		int32(999_999),
		quota.Remaining,
	)
	require.Equal(
		t,
		gcraBase.Add(60*time.Microsecond),
		quota.ResetAt,
	)
	require.Equal(
		t,
		gcraBase.UnixMicro()+60,
		gcraStoredTAT(
			t,
			mr,
		),
	)
	require.Equal(
		t,
		time.Millisecond,
		mr.TTL(gcraTestKey),
	)
}

func TestGCRA_RefillsOnePermitPerInterval(t *testing.T) {
	const limit = 7
	interval := 8571428 * time.Microsecond
	_, _, limiter, clock := newGCRALimiter(
		t,
		limit,
		Config{},
	)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}

	denied := gcraAllow(
		t,
		limiter,
		limit,
	)
	require.False(
		t,
		denied.Allowed,
	)
	require.Equal(
		t,
		interval,
		denied.RetryAfter,
	)

	clock.advance(denied.RetryAfter - time.Microsecond)
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	clock.advance(time.Microsecond)
	passed := gcraAllow(
		t,
		limiter,
		limit,
	)
	require.True(
		t,
		passed.Allowed,
	)
	require.Zero(
		t,
		passed.Remaining,
	)
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	clock.advance(3 * interval)
	for i := range 3 {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
			i,
		)
	}
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)
}

func TestGCRA_RetryAfterIsExactWhenBucketIsPartlyDrained(t *testing.T) {
	const limit = 10
	interval := 6 * time.Second
	_, _, limiter, clock := newGCRALimiter(
		t,
		limit,
		Config{},
	)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}

	clock.advance(interval + 2*time.Second)
	require.True(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	denied := gcraAllow(
		t,
		limiter,
		limit,
	)
	require.False(
		t,
		denied.Allowed,
	)
	require.Equal(
		t,
		4*time.Second,
		denied.RetryAfter,
	)

	clock.advance(denied.RetryAfter - time.Microsecond)
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)
	clock.advance(time.Microsecond)
	require.True(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)
}

func TestGCRA_IdleWindowRestoresFullBurst(t *testing.T) {
	const limit = 7
	mr, _, limiter, clock := newGCRALimiter(
		t,
		limit,
		Config{},
	)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	ttl := mr.TTL(gcraTestKey)
	require.Positive(
		t,
		ttl,
	)
	require.LessOrEqual(
		t,
		ttl,
		time.Minute,
	)

	clock.advance(time.Minute)
	for i := range limit {
		quota := gcraAllow(
			t,
			limiter,
			limit,
		)
		require.True(
			t,
			quota.Allowed,
		)
		require.Equal(
			t,
			int32(limit-1-i),
			quota.Remaining,
		)
	}
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	mr.FastForward(time.Minute)
	require.False(
		t,
		mr.Exists(gcraTestKey),
	)
	clock.advance(time.Minute)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)
}

func TestGCRA_RejectedNotRecordedByDefault(t *testing.T) {
	const limit = 5
	mr, _, limiter, clock := newGCRALimiter(
		t,
		limit,
		Config{},
	)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}
	stored, err := mr.Get(gcraTestKey)
	require.NoError(
		t,
		err,
	)
	ttl := mr.TTL(gcraTestKey)

	for range 4 {
		clock.advance(100 * time.Millisecond)
		require.False(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}

	after, err := mr.Get(gcraTestKey)
	require.NoError(
		t,
		err,
	)
	require.Equal(
		t,
		stored,
		after,
	)
	require.Equal(
		t,
		ttl,
		mr.TTL(gcraTestKey),
	)
}

func TestGCRA_RejectedCapsDebtAtBurstWhenCounted(t *testing.T) {
	const limit = 5
	interval := 12 * time.Second
	mr, _, limiter, clock := newGCRALimiter(
		t,
		limit,
		Config{
			CountRejected: true,
		},
	)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}

	for range 6 {
		clock.advance(time.Second)
		denied := gcraAllow(
			t,
			limiter,
			limit,
		)
		require.False(
			t,
			denied.Allowed,
		)
		require.Zero(
			t,
			denied.Remaining,
		)
		require.Equal(
			t,
			clock.now().Add(limit*interval),
			denied.ResetAt,
		)
		require.Equal(
			t,
			clock.now().UnixMicro()+limit*interval.Microseconds(),
			gcraStoredTAT(
				t,
				mr,
			),
		)
	}

	last := gcraAllow(
		t,
		limiter,
		limit,
	)
	require.Equal(
		t,
		interval,
		last.RetryAfter,
	)
	clock.advance(interval - time.Microsecond)
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)

	clock.advance(time.Minute)
	for range limit {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				limit,
			).Allowed,
		)
	}
}

func TestGCRA_HammeringKeepsBlockOnlyWhenRejectedCounted(t *testing.T) {
	const limit = 5
	interval := 12 * time.Second
	tests := []struct {
		name          string
		countRejected bool
		wantAllowed   int
	}{
		{
			name:          "not counted",
			countRejected: false,
			wantAllowed:   10,
		},
		{
			name:          "counted",
			countRejected: true,
			wantAllowed:   0,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				_, _, limiter, clock := newGCRALimiter(
					t,
					limit,
					Config{
						CountRejected: tt.countRejected,
					},
				)
				for range limit {
					require.True(
						t,
						gcraAllow(
							t,
							limiter,
							limit,
						).Allowed,
					)
				}

				allowedCount := 0
				for range 20 {
					clock.advance(interval / 2)
					if gcraAllow(
						t,
						limiter,
						limit,
					).Allowed {
						allowedCount++
					}
				}

				require.Equal(
					t,
					tt.wantAllowed,
					allowedCount,
				)
			},
		)
	}
}

func TestGCRA_MethodsShareOneBucket(t *testing.T) {
	const limit = 5
	mr, _, limiter, _ := newGCRALimiter(
		t,
		limit,
		Config{},
	)
	ctx := context.Background()

	allowed, err := limiter.Allow(
		ctx,
		testKey("a"),
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)
	allowed, err = limiter.AllowN(
		ctx,
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)
	quota := gcraAllow(
		t,
		limiter,
		limit,
	)
	require.True(
		t,
		quota.Allowed,
	)
	require.Equal(
		t,
		int32(2),
		quota.Remaining,
	)
	allowed, err = limiter.Allow(
		ctx,
		testKey("a"),
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)
	allowed, err = limiter.AllowN(
		ctx,
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)

	allowed, err = limiter.Allow(
		ctx,
		testKey("a"),
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		allowed,
	)
	allowed, err = limiter.AllowN(
		ctx,
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		allowed,
	)
	require.False(
		t,
		gcraAllow(
			t,
			limiter,
			limit,
		).Allowed,
	)
	require.Equal(
		t,
		[]string{gcraTestKey},
		mr.Keys(),
	)
}

func TestGCRA_JournalAndGCRAShareKeyPrefixWithoutWrongType(t *testing.T) {
	const limit = 5
	mr, client := newTestClient(t)
	clock := &quotaClock{
		current: gcraBase,
	}
	journal := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{},
	)
	journal.now = clock.now
	gcra := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{
			Algorithm: AlgorithmGCRA,
		},
	)
	gcra.now = clock.now
	ctx := context.Background()

	for range 3 {
		allowed, err := journal.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		require.True(
			t,
			allowed,
		)
	}
	for range limit {
		allowed, err := gcra.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		require.True(
			t,
			allowed,
		)
	}
	for range 2 {
		allowed, err := journal.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		require.True(
			t,
			allowed,
		)
	}

	allowed, err := gcra.AllowN(
		ctx,
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		allowed,
	)
	allowed, err = journal.AllowN(
		ctx,
		testKey("a"),
		limit,
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		allowed,
	)
	require.Equal(
		t,
		"zset",
		mr.Type(quotaTestKey),
	)
	require.Equal(
		t,
		"string",
		mr.Type(gcraTestKey),
	)
	require.Len(
		t,
		mr.Keys(),
		2,
	)
}

func TestGCRA_OnlyExactAlgorithmSelectsGCRA(t *testing.T) {
	tests := []struct {
		name      string
		algorithm Algorithm
	}{
		{
			name:      "empty",
			algorithm: "",
		},
		{
			name:      "sliding",
			algorithm: AlgorithmSlidingWindow,
		},
		{
			name:      "unknown",
			algorithm: "token-bucket",
		},
		{
			name:      "upper case",
			algorithm: "GCRA",
		},
		{
			name:      "padded",
			algorithm: " gcra",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				mr, _, limiter, _ := newQuotaLimiter(
					t,
					Config{
						Algorithm: tt.algorithm,
					},
					gcraBase,
				)

				allowed, err := limiter.AllowN(
					context.Background(),
					testKey("a"),
					5,
				)
				require.NoError(
					t,
					err,
				)
				require.True(
					t,
					allowed,
				)
				quota, err := limiter.AllowNWithQuota(
					context.Background(),
					testKey("a"),
					5,
				)
				require.NoError(
					t,
					err,
				)
				require.True(
					t,
					quota.Allowed,
				)
				require.Equal(
					t,
					int32(3),
					quota.Remaining,
				)

				require.Equal(
					t,
					[]string{quotaTestKey},
					mr.Keys(),
				)
				require.Equal(
					t,
					"zset",
					mr.Type(quotaTestKey),
				)
			},
		)
	}
}

func TestGCRA_NonPositiveLimitSkipsRedis(t *testing.T) {
	for _, limit := range []int32{0, -1} {
		t.Run(
			strconv.Itoa(int(limit)),
			func(t *testing.T) {
				mr, _, limiter, _ := newGCRALimiter(
					t,
					5,
					Config{},
				)

				allowed, err := limiter.AllowN(
					context.Background(),
					testKey("a"),
					limit,
				)
				require.NoError(
					t,
					err,
				)
				require.True(
					t,
					allowed,
				)
				quota := gcraAllow(
					t,
					limiter,
					limit,
				)
				require.Equal(
					t,
					Quota{
						Allowed: true,
						Limit:   limit,
					},
					quota,
				)
				require.Empty(
					t,
					mr.Keys(),
				)
			},
		)
	}
}

func TestGCRA_RedisDown(t *testing.T) {
	tests := []struct {
		name     string
		failMode FailMode
		wantErr  bool
	}{
		{
			name:     "fail closed",
			failMode: FailClosed,
			wantErr:  true,
		},
		{
			name:     "fail open",
			failMode: FailOpen,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				mr, _, limiter, _ := newGCRALimiter(
					t,
					5,
					Config{
						FailMode: tt.failMode,
					},
				)
				limiter.client = redis.NewClient(
					&redis.Options{
						Addr:       mr.Addr(),
						MaxRetries: -1,
					},
				)
				t.Cleanup(func() {
					require.NoError(
						t,
						limiter.client.(*redis.Client).Close(),
					)
				})
				mr.Close()
				buf := captureLog(t)

				allowed, allowErr := limiter.Allow(
					context.Background(),
					testKey("a"),
				)
				allowLog := decodeLogLines(
					t,
					buf,
				)
				buf.Reset()

				quota, err := limiter.AllowNWithQuota(
					context.Background(),
					testKey("a"),
					5,
				)
				quotaLog := decodeLogLines(
					t,
					buf,
				)

				require.Equal(
					t,
					allowed,
					quota.Allowed,
				)
				require.False(
					t,
					quota.Known,
				)
				require.Equal(
					t,
					int32(5),
					quota.Limit,
				)
				if tt.wantErr {
					require.ErrorContains(
						t,
						err,
						"ratelimitfx: redis script:",
					)
					require.EqualError(
						t,
						err,
						allowErr.Error(),
					)
				} else {
					require.NoError(
						t,
						err,
					)
					require.NoError(
						t,
						allowErr,
					)
				}

				require.Len(
					t,
					quotaLog,
					1,
				)
				require.Equal(
					t,
					allowLog,
					quotaLog,
				)
				require.Equal(
					t,
					"error",
					quotaLog[0]["level"],
				)
				require.Equal(
					t,
					"ratelimitfx: redis script failed",
					quotaLog[0]["message"],
				)
				require.Equal(
					t,
					"key-strategy",
					quotaLog[0]["ratelimit_strategy"],
				)
				require.Equal(
					t,
					gcraTestKey,
					quotaLog[0]["ratelimit_key"],
				)
				require.Contains(
					t,
					quotaLog[0],
					"ratelimit_fail_mode",
				)
				require.Contains(
					t,
					quotaLog[0],
					"error",
				)
			},
		)
	}
}

func TestGCRA_MalformedReplyFollowsFailMode(t *testing.T) {
	tests := []struct {
		name     string
		failMode FailMode
		wantErr  bool
	}{
		{
			name:     "fail closed",
			failMode: FailClosed,
			wantErr:  true,
		},
		{
			name:     "fail open",
			failMode: FailOpen,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				_, client, limiter, _ := newGCRALimiter(
					t,
					5,
					Config{
						FailMode: tt.failMode,
					},
				)
				client.AddHook(shortReplyHook{})

				quota, err := limiter.AllowNWithQuota(
					context.Background(),
					testKey("a"),
					5,
				)

				require.Equal(
					t,
					!tt.wantErr,
					quota.Allowed,
				)
				require.False(
					t,
					quota.Known,
				)
				if tt.wantErr {
					require.ErrorContains(
						t,
						err,
						"gcra script reply has 2 values",
					)
				} else {
					require.NoError(
						t,
						err,
					)
				}
			},
		)
	}
}

func TestGCRA_SingleRoundTrip(t *testing.T) {
	_, client, limiter, clock := newGCRALimiter(
		t,
		5,
		Config{},
	)
	gcraAllow(
		t,
		limiter,
		5,
	)

	recorder := &commandRecorder{}
	client.AddHook(recorder)
	for range 3 {
		clock.advance(time.Second)
		gcraAllow(
			t,
			limiter,
			5,
		)
	}
	allowed, err := limiter.Allow(
		context.Background(),
		testKey("a"),
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)

	require.Equal(
		t,
		[]string{"evalsha", "evalsha", "evalsha", "evalsha"},
		recorder.names,
	)
}

func TestGCRA_ConcurrentAllowIsAtomic(t *testing.T) {
	const (
		callers = 50
		limit   = 10
	)
	_, _, limiter, _ := newGCRALimiter(
		t,
		limit,
		Config{},
	)

	quotas := make(
		[]Quota,
		callers,
	)
	errs := make(
		[]error,
		callers,
	)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			quotas[i], errs[i] = limiter.AllowNWithQuota(
				context.Background(),
				testKey("a"),
				limit,
			)
		}()
	}
	wg.Wait()

	allowedCount := 0
	for i, quota := range quotas {
		require.NoError(
			t,
			errs[i],
		)
		require.True(
			t,
			quota.Known,
		)
		require.GreaterOrEqual(
			t,
			quota.Remaining,
			int32(0),
		)
		require.LessOrEqual(
			t,
			quota.Remaining,
			int32(limit-1),
		)
		if quota.Allowed {
			allowedCount++
		}
	}
	require.Equal(
		t,
		limit,
		allowedCount,
	)
}

func TestGCRA_LimitChangeKeepsQuotaInRange(t *testing.T) {
	_, _, limiter, clock := newGCRALimiter(
		t,
		10,
		Config{},
	)
	for range 10 {
		require.True(
			t,
			gcraAllow(
				t,
				limiter,
				10,
			).Allowed,
		)
	}

	for _, limit := range []int32{3, 50, 1, 10, 1000, 2} {
		clock.advance(500 * time.Millisecond)
		quota := gcraAllow(
			t,
			limiter,
			limit,
		)
		require.True(
			t,
			quota.Known,
		)
		require.Equal(
			t,
			limit,
			quota.Limit,
		)
		require.GreaterOrEqual(
			t,
			quota.Remaining,
			int32(0),
		)
		require.LessOrEqual(
			t,
			quota.Remaining,
			limit,
		)
		require.True(
			t,
			quota.ResetAt.After(clock.now()),
		)
		require.Equal(
			t,
			quota.Allowed,
			quota.RetryAfter == 0,
		)
	}
}

func TestGCRA_TinyWindowKeepsPositiveInterval(t *testing.T) {
	mr, _, limiter, _ := newGCRALimiter(
		t,
		1,
		Config{
			Window: 100 * time.Microsecond,
		},
	)

	for range 3 {
		quota := gcraAllow(
			t,
			limiter,
			500,
		)
		require.Equal(
			t,
			quota.Allowed,
			quota.RetryAfter == 0,
		)
		require.True(
			t,
			quota.ResetAt.After(gcraBase),
		)
	}
	require.Positive(
		t,
		mr.TTL(gcraTestKey),
	)
}

func TestGCRA_AdmitsWhatJournalAdmitsInFull(t *testing.T) {
	const limit = 5
	_, client := newTestClient(t)
	journalClock := &quotaClock{
		current: gcraBase,
	}
	journal := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{
			KeyPrefix: "journal",
		},
	)
	journal.now = journalClock.now
	gcraClock := &quotaClock{
		current: gcraBase,
	}
	gcra := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{
			Algorithm: AlgorithmGCRA,
			KeyPrefix: "gcra",
		},
	)
	gcra.now = gcraClock.now
	source := rand.New(rand.NewPCG(
		7,
		11,
	))
	ctx := context.Background()

	admitted := 0
	for range 400 {
		step := time.Duration(source.IntN(20_000_000)) * time.Microsecond
		journalClock.advance(step)
		allowed, err := journal.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		if !allowed {
			continue
		}
		admitted++
		gcraClock.current = journalClock.current
		allowedByGCRA, err := gcra.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		require.True(
			t,
			allowedByGCRA,
			journalClock.current.Sub(gcraBase),
		)
	}
	require.Greater(
		t,
		admitted,
		100,
	)
}

func TestGCRA_DivergesFromJournalAfterDenial(t *testing.T) {
	const limit = 2
	offsets := []time.Duration{0, 0, 30 * time.Second, 60 * time.Second, 60 * time.Second}
	_, client := newTestClient(t)
	clock := &quotaClock{
		current: gcraBase,
	}
	journal := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{
			KeyPrefix: "journal",
		},
	)
	journal.now = clock.now
	gcra := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: limit,
		},
		Config{
			Algorithm: AlgorithmGCRA,
			KeyPrefix: "gcra",
		},
	)
	gcra.now = clock.now
	ctx := context.Background()

	var journalDecisions []bool
	var gcraDecisions []bool
	for _, offset := range offsets {
		clock.current = gcraBase.Add(offset)
		allowed, err := journal.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		journalDecisions = append(
			journalDecisions,
			allowed,
		)
		allowed, err = gcra.AllowN(
			ctx,
			testKey("a"),
			limit,
		)
		require.NoError(
			t,
			err,
		)
		gcraDecisions = append(
			gcraDecisions,
			allowed,
		)
	}

	require.Equal(
		t,
		[]bool{true, true, false, true, true},
		journalDecisions,
	)
	require.Equal(
		t,
		[]bool{true, true, true, true, false},
		gcraDecisions,
	)
}
