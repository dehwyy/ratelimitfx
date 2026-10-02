package ratelimit

import (
	"errors"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
)

var ErrNoRedisClient = errors.New("ratelimitfx: no redis client in the fx graph")

type moduleParams struct {
	fx.In

	Client   *redis.Client `optional:"true"`
	Cmdable  redis.Cmdable `optional:"true"`
	Strategy Strategy      `optional:"true"`
}

func Module[C any](configFn func(*C) Config) fx.Option {
	return fx.Module(
		"ratelimitfx",
		fx.Provide(
			func(
				appConfig *C,
				params moduleParams,
			) (*RedisLimiter, error) {
				var client redis.Cmdable
				switch {
				case params.Cmdable != nil:
					client = params.Cmdable
				case params.Client != nil:
					client = params.Client
				default:
					return nil, ErrNoRedisClient
				}

				var strategy Strategy = PerKeyStrategy{}
				if params.Strategy != nil {
					strategy = params.Strategy
				}

				return NewRedisLimiter(
					client,
					strategy,
					configFn(appConfig),
				), nil
			},
			func(limiter *RedisLimiter) Limiter {
				return limiter
			},
		),
	)
}
