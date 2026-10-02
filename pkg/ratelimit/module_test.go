package ratelimit

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

type moduleTestConfig struct {
	Limit RPM
}

func TestStringKey(t *testing.T) {
	require.Equal(t, "abc", StringKey("abc").String())
	var key Key = StringKey("abc")
	require.Equal(t, "abc", key.String())
}

func TestModule(t *testing.T) {
	tests := []struct {
		name     string
		provide  func(mr *miniredis.Miniredis) []any
		wantErr  bool
		wantName string
	}{
		{
			name: "redis client and default strategy",
			provide: func(mr *miniredis.Miniredis) []any {
				return []any{func() *redis.Client { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) }}
			},
			wantName: "key-strategy",
		},
		{
			name: "cmdable and custom strategy",
			provide: func(mr *miniredis.Miniredis) []any {
				return []any{
					func() redis.Cmdable { return redis.NewClient(&redis.Options{Addr: mr.Addr()}) },
					func() Strategy { return PerKeyStrategy{RPM: 1} },
				}
			},
			wantName: "key-strategy",
		},
		{
			name:    "no redis client",
			provide: func(*miniredis.Miniredis) []any { return nil },
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)

			var limiter Limiter
			var concrete *RedisLimiter
			app := fx.New(
				fx.NopLogger,
				fx.Provide(func() *moduleTestConfig { return &moduleTestConfig{Limit: 1} }),
				fx.Provide(tc.provide(mr)...),
				Module(func(c *moduleTestConfig) Config {
					return Config{KeyPrefix: "mod"}
				}),
				fx.Populate(&limiter, &concrete),
			)

			if tc.wantErr {
				require.ErrorIs(t, app.Err(), ErrNoRedisClient)
				return
			}
			require.NoError(t, app.Err())
			require.Same(t, limiter, concrete)
			require.Equal(t, tc.wantName, concrete.strategy.Name())

			ctx := context.Background()
			allowed, err := limiter.AllowN(ctx, StringKey("k"), 1)
			require.NoError(t, err)
			require.True(t, allowed)
			allowed, err = limiter.AllowN(ctx, StringKey("k"), 1)
			require.NoError(t, err)
			require.False(t, allowed)
		})
	}
}
