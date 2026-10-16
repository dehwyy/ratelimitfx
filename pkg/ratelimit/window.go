package ratelimit

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

func (l *RedisLimiter) windowKey(
	k Key,
) string {
	return fmt.Sprintf(
		"%s:%s:%s",
		l.cfg.KeyPrefix,
		l.strategy.Name(),
		l.strategy.Key(k),
	)
}

func (l *RedisLimiter) windowArgs(
	now time.Time,
	limit int32,
) []any {
	member := fmt.Sprintf(
		"%d-%d",
		now.UnixNano(),
		atomic.AddUint64(
			&memberSeq,
			1,
		),
	)

	countRejected := 0
	if l.cfg.CountRejected {
		countRejected = 1
	}

	return []any{
		now.UnixMilli(),
		l.cfg.Window.Milliseconds(),
		limit,
		member,
		(l.cfg.Window * 2).Milliseconds(),
		countRejected,
	}
}

func (l *RedisLimiter) scriptFailed(
	err error,
	key string,
) (bool, error) {
	log.Error().
		Err(err).
		Str(
			"ratelimit_strategy",
			l.strategy.Name(),
		).
		Str(
			"ratelimit_key",
			key,
		).
		Str(
			"ratelimit_fail_mode",
			failModeString(l.cfg.FailMode),
		).
		Msg("ratelimitfx: redis script failed")
	if l.cfg.FailMode == FailOpen {
		return true, nil
	}
	return false, fmt.Errorf(
		"ratelimitfx: redis script: %w",
		err,
	)
}
