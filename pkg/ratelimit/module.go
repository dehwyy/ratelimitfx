package ratelimit

import (
	"github.com/google/uuid"
	"go.uber.org/fx"
)

// ModuleMerchant provides a *RedisLimiter[uuid.UUID] backed by PerMerchantStrategy.
// Callers must provide a *redis.Client (or redis.Cmdable) and a Config in the fx graph.
var ModuleMerchant = fx.Module(
	"ratelimitfx.merchant",
	fx.Provide(
		func() Strategy[uuid.UUID] {
			return PerMerchantStrategy{}
		},
		fx.Annotate(
			NewRedisLimiter[uuid.UUID],
			fx.As(new(Limiter[uuid.UUID])),
		),
	),
)

// ModuleIP provides a *RedisLimiter[string] backed by PerIPStrategy.
var ModuleIP = fx.Module(
	"ratelimitfx.ip",
	fx.Provide(
		func() Strategy[string] {
			return PerIPStrategy{}
		},
		fx.Annotate(
			NewRedisLimiter[string],
			fx.As(new(Limiter[string])),
		),
	),
)
