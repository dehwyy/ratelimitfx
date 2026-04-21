package ratelimit

import (
	"github.com/google/uuid"
	"go.uber.org/fx"
)

// ModuleMerchant provides a *RedisLimiter[uuid.UUID] backed by PerMerchantStrategy.
// Callers must provide a *redis.Client (or redis.Cmdable) and a Config in the fx graph.
func ModuleMerchant(
	rpm RPM,
) fx.Option {
	return fx.Module(
		"ratelimitfx.merchant",
		fx.Provide(
			func() Strategy[uuid.UUID] {
				return PerMerchantStrategy{
					RPM: rpm,
				}
			},
			fx.Annotate(
				NewRedisLimiter[uuid.UUID],
				fx.As(new(Limiter[uuid.UUID])),
			),
		),
	)
}

// ModuleIP provides a *RedisLimiter[string] backed by PerIPStrategy.
func ModuleIP(
	rpm RPM,
) fx.Option {
	return fx.Module(
		"ratelimitfx.ip",
		fx.Provide(
			func() Strategy[IP] {
				return PerIPStrategy{
					RPM: rpm,
				}
			},
			fx.Annotate(
				NewRedisLimiter[IP],
				fx.As(new(Limiter[IP])),
			),
		),
	)
}
