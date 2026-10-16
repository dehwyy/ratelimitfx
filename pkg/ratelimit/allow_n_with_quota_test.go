package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

const quotaTestKey = "ratelimitfx:key-strategy:a"

type quotaClock struct {
	current time.Time
}

func (c *quotaClock) now() time.Time {
	return c.current
}

func (c *quotaClock) advance(d time.Duration) {
	c.current = c.current.Add(d)
}

func newQuotaLimiter(
	t *testing.T,
	cfg Config,
	base time.Time,
) (*miniredis.Miniredis, *redis.Client, *RedisLimiter, *quotaClock) {
	t.Helper()
	mr, client := newTestClient(t)
	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 1000,
		},
		cfg,
	)
	clock := &quotaClock{
		current: base,
	}
	limiter.now = clock.now
	return mr, client, limiter, clock
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() {
		log.Logger = previous
	})
	return &buf
}

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(
		bytes.TrimSpace(buf.Bytes()),
		[]byte("\n"),
	) {
		if len(raw) == 0 {
			continue
		}
		var line map[string]any
		require.NoError(
			t,
			json.Unmarshal(
				raw,
				&line,
			),
		)
		lines = append(
			lines,
			line,
		)
	}
	return lines
}

func payinQuota(
	t *testing.T,
	client *redis.Client,
	limit int32,
	now time.Time,
	window time.Duration,
) (int32, time.Time) {
	t.Helper()
	ctx := context.Background()
	windowStart := strconv.FormatInt(
		now.Add(-window).UnixMilli(),
		10,
	)

	count, err := client.ZCount(
		ctx,
		quotaTestKey,
		"("+windowStart,
		"+inf",
	).Result()
	require.NoError(
		t,
		err,
	)
	oldest, err := client.ZRangeByScoreWithScores(
		ctx,
		quotaTestKey,
		&redis.ZRangeBy{
			Min:    "(" + windowStart,
			Max:    "+inf",
			Offset: 0,
			Count:  1,
		},
	).Result()
	require.NoError(
		t,
		err,
	)

	resetAt := now
	if len(oldest) > 0 {
		resetAt = time.UnixMilli(int64(oldest[0].Score)).Add(window)
	}
	return int32(max(
		int64(limit)-count,
		0,
	)), resetAt
}

func payoutQuota(
	t *testing.T,
	client *redis.Client,
	limit int32,
	now time.Time,
	window time.Duration,
) (int32, int64) {
	t.Helper()
	ctx := context.Background()

	count, err := client.ZCard(
		ctx,
		quotaTestKey,
	).Result()
	require.NoError(
		t,
		err,
	)
	oldest, err := client.ZRangeWithScores(
		ctx,
		quotaTestKey,
		0,
		0,
	).Result()
	require.NoError(
		t,
		err,
	)

	reset := int64(window / time.Second)
	if len(oldest) > 0 {
		expiresAt := time.UnixMilli(int64(oldest[0].Score)).Add(window)
		reset = int64((expiresAt.Sub(now) + time.Second - 1) / time.Second)
		if reset < 1 {
			reset = 1
		}
	}
	return int32(max(
		int64(limit)-count,
		0,
	)), reset
}

func TestAllowNWithQuota_CountsDownToZero(t *testing.T) {
	base := time.UnixMilli(1_760_000_000_000)
	_, _, limiter, clock := newQuotaLimiter(
		t,
		Config{},
		base,
	)

	for i := range 3 {
		quota, err := limiter.AllowNWithQuota(
			context.Background(),
			testKey("a"),
			3,
		)
		require.NoError(
			t,
			err,
		)
		require.Equal(
			t,
			Quota{
				Allowed:   true,
				Limit:     3,
				Remaining: int32(2 - i),
				ResetAt:   base.Add(time.Minute),
				Known:     true,
			},
			quota,
		)
		clock.advance(time.Second)
	}
}

func TestAllowNWithQuota_Denied(t *testing.T) {
	tests := []struct {
		name          string
		countRejected bool
		wantEntries   int
	}{
		{
			name:          "rejected not counted",
			countRejected: false,
			wantEntries:   2,
		},
		{
			name:          "rejected counted",
			countRejected: true,
			wantEntries:   5,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				base := time.UnixMilli(1_760_000_000_000)
				mr, _, limiter, clock := newQuotaLimiter(
					t,
					Config{
						CountRejected: tt.countRejected,
					},
					base,
				)

				for range 2 {
					quota, err := limiter.AllowNWithQuota(
						context.Background(),
						testKey("a"),
						2,
					)
					require.NoError(
						t,
						err,
					)
					require.True(
						t,
						quota.Allowed,
					)
					clock.advance(time.Second)
				}

				for range 3 {
					clock.advance(5 * time.Second)
					quota, err := limiter.AllowNWithQuota(
						context.Background(),
						testKey("a"),
						2,
					)
					require.NoError(
						t,
						err,
					)
					require.False(
						t,
						quota.Allowed,
					)
					require.True(
						t,
						quota.Known,
					)
					require.Equal(
						t,
						int32(2),
						quota.Limit,
					)
					require.Equal(
						t,
						int32(0),
						quota.Remaining,
					)
					require.Equal(
						t,
						base.Add(time.Minute),
						quota.ResetAt,
					)
					require.Equal(
						t,
						quota.ResetAt.Sub(clock.now()),
						quota.RetryAfter,
					)
					require.Positive(
						t,
						quota.RetryAfter,
					)
				}

				require.Equal(
					t,
					tt.wantEntries,
					windowCount(
						t,
						mr,
						quotaTestKey,
					),
				)
			},
		)
	}
}

func TestAllowNWithQuota_ParityWithGatewayQuota(t *testing.T) {
	tests := []struct {
		name          string
		countRejected bool
	}{
		{
			name:          "rejected not counted",
			countRejected: false,
		},
		{
			name:          "rejected counted",
			countRejected: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				base := time.Unix(
					1_760_000_000,
					123_456_789,
				)
				_, client, limiter, clock := newQuotaLimiter(
					t,
					Config{
						CountRejected: tt.countRejected,
					},
					base,
				)

				const limit = int32(4)
				steps := []time.Duration{
					0,
					3 * time.Second,
					7*time.Second + 300*time.Millisecond,
					time.Millisecond,
					11 * time.Second,
					500 * time.Microsecond,
					9 * time.Second,
					13*time.Second + 999*time.Millisecond,
					17 * time.Second,
					29*time.Second + 1*time.Millisecond,
					8 * time.Second,
					31 * time.Second,
					59 * time.Second,
					time.Second,
				}

				for i, step := range steps {
					clock.advance(step)
					quota, err := limiter.AllowNWithQuota(
						context.Background(),
						testKey("a"),
						limit,
					)
					require.NoError(
						t,
						err,
						"step %d",
						i,
					)
					require.True(
						t,
						quota.Known,
						"step %d",
						i,
					)

					remaining, resetAt := payinQuota(
						t,
						client,
						limit,
						clock.now(),
						time.Minute,
					)
					require.Equal(
						t,
						remaining,
						quota.Remaining,
						"payin remaining at step %d",
						i,
					)
					require.Equal(
						t,
						resetAt.UnixMilli(),
						quota.ResetAt.UnixMilli(),
						"payin reset at step %d",
						i,
					)

					payoutRemaining, payoutReset := payoutQuota(
						t,
						client,
						limit,
						clock.now(),
						time.Minute,
					)
					require.Equal(
						t,
						payoutRemaining,
						quota.Remaining,
						"payout remaining at step %d",
						i,
					)
					wantReset := int64((quota.ResetAt.Sub(clock.now()) + time.Second - 1) / time.Second)
					require.Equal(
						t,
						payoutReset,
						max(
							wantReset,
							1,
						),
						"payout reset at step %d",
						i,
					)
				}
			},
		)
	}
}

func TestAllowNWithQuota_SharesWindowWithAllowN(t *testing.T) {
	base := time.UnixMilli(1_760_000_000_000)
	mr, _, limiter, clock := newQuotaLimiter(
		t,
		Config{},
		base,
	)
	ctx := context.Background()

	allowed, err := limiter.AllowN(
		ctx,
		testKey("a"),
		3,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)
	clock.advance(time.Second)

	quota, err := limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		3,
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
		int32(1),
		quota.Remaining,
	)
	require.Equal(
		t,
		base.Add(time.Minute),
		quota.ResetAt,
	)
	clock.advance(time.Second)

	allowed, err = limiter.AllowN(
		ctx,
		testKey("a"),
		3,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		allowed,
	)
	clock.advance(time.Second)

	quota, err = limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		3,
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		quota.Allowed,
	)
	require.Equal(
		t,
		int32(0),
		quota.Remaining,
	)

	allowed, err = limiter.AllowN(
		ctx,
		testKey("a"),
		3,
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
		3,
		windowCount(
			t,
			mr,
			quotaTestKey,
		),
	)
}

func TestAllowNWithQuota_WindowExpiryRestoresQuota(t *testing.T) {
	base := time.UnixMilli(1_760_000_000_000)
	_, _, limiter, clock := newQuotaLimiter(
		t,
		Config{},
		base,
	)
	ctx := context.Background()

	first, err := limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		2,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		first.Allowed,
	)
	clock.advance(20 * time.Second)

	second, err := limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		2,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		second.Allowed,
	)
	require.Equal(
		t,
		int32(0),
		second.Remaining,
	)
	clock.advance(25 * time.Second)

	denied, err := limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		2,
	)
	require.NoError(
		t,
		err,
	)
	require.False(
		t,
		denied.Allowed,
	)
	require.Equal(
		t,
		base.Add(time.Minute),
		denied.ResetAt,
	)
	require.Equal(
		t,
		15*time.Second,
		denied.RetryAfter,
	)
	clock.advance(16 * time.Second)

	restored, err := limiter.AllowNWithQuota(
		ctx,
		testKey("a"),
		2,
	)
	require.NoError(
		t,
		err,
	)
	require.True(
		t,
		restored.Allowed,
	)
	require.Equal(
		t,
		int32(0),
		restored.Remaining,
	)
	require.Equal(
		t,
		base.Add(20*time.Second).Add(time.Minute),
		restored.ResetAt,
	)
	require.Zero(
		t,
		restored.RetryAfter,
	)
}

type commandRecorder struct {
	names []string
}

func (recorder *commandRecorder) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (recorder *commandRecorder) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		recorder.names = append(
			recorder.names,
			cmd.Name(),
		)
		return next(
			ctx,
			cmd,
		)
	}
}

func (recorder *commandRecorder) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		recorder.names = append(
			recorder.names,
			"pipeline",
		)
		return next(
			ctx,
			cmds,
		)
	}
}

func TestAllowNWithQuota_SingleRoundTrip(t *testing.T) {
	base := time.UnixMilli(1_760_000_000_000)
	_, client, limiter, clock := newQuotaLimiter(
		t,
		Config{},
		base,
	)

	_, err := limiter.AllowNWithQuota(
		context.Background(),
		testKey("a"),
		5,
	)
	require.NoError(
		t,
		err,
	)

	recorder := &commandRecorder{}
	client.AddHook(recorder)
	for range 3 {
		clock.advance(time.Second)
		_, err = limiter.AllowNWithQuota(
			context.Background(),
			testKey("a"),
			5,
		)
		require.NoError(
			t,
			err,
		)
	}

	require.Equal(
		t,
		[]string{"evalsha", "evalsha", "evalsha"},
		recorder.names,
	)
}

func TestAllowNWithQuota_NonPositiveLimitSkipsRedis(t *testing.T) {
	for _, limit := range []int32{0, -1} {
		t.Run(
			strconv.Itoa(int(limit)),
			func(t *testing.T) {
				mr, _, limiter, _ := newQuotaLimiter(
					t,
					Config{},
					time.UnixMilli(1_760_000_000_000),
				)

				quota, err := limiter.AllowNWithQuota(
					context.Background(),
					testKey("a"),
					limit,
				)
				require.NoError(
					t,
					err,
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

func TestAllowNWithQuota_RedisDown(t *testing.T) {
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
				mr, _, limiter, _ := newQuotaLimiter(
					t,
					Config{
						FailMode: tt.failMode,
					},
					time.UnixMilli(1_760_000_000_000),
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

				allowed, allowErr := limiter.AllowN(
					context.Background(),
					testKey("a"),
					5,
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
					require.Error(
						t,
						err,
					)
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
					quotaTestKey,
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

type shortReplyHook struct{}

func (shortReplyHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (shortReplyHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if err := next(
			ctx,
			cmd,
		); err != nil {
			return err
		}
		if script, ok := cmd.(*redis.Cmd); ok {
			script.SetVal([]any{int64(1), int64(1)})
		}
		return nil
	}
}

func (shortReplyHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestAllowNWithQuota_MalformedReplyFollowsFailMode(t *testing.T) {
	_, client, limiter, _ := newQuotaLimiter(
		t,
		Config{
			FailMode: FailClosed,
		},
		time.UnixMilli(1_760_000_000_000),
	)
	client.AddHook(shortReplyHook{})

	quota, err := limiter.AllowNWithQuota(
		context.Background(),
		testKey("a"),
		5,
	)
	require.ErrorContains(
		t,
		err,
		"quota script reply has 2 values",
	)
	require.False(
		t,
		quota.Allowed,
	)
	require.False(
		t,
		quota.Known,
	)
}

func TestAllowNWithQuota_ConcurrentAllowIsAtomic(t *testing.T) {
	_, client := newTestClient(t)
	limiter := NewRedisLimiter(
		client,
		PerKeyStrategy{
			RPM: 10,
		},
		Config{},
	)

	const callers = 50
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
				10,
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
			int32(9),
		)
		if quota.Allowed {
			allowedCount++
		}
	}
	require.Equal(
		t,
		10,
		allowedCount,
	)
}

func TestQuotaOf_EmptyWindow(t *testing.T) {
	_, _, limiter, _ := newQuotaLimiter(
		t,
		Config{},
		time.UnixMilli(1_760_000_000_000),
	)
	now := time.UnixMilli(1_760_000_000_000)

	quota := limiter.quotaOf(
		[]int64{1, 0, -1},
		5,
		now,
	)

	require.Equal(
		t,
		Quota{
			Allowed:   true,
			Limit:     5,
			Remaining: 5,
			ResetAt:   now.Add(time.Minute),
			Known:     true,
		},
		quota,
	)
}
