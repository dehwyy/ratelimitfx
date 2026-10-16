package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const gcraReplyLen = 4

var gcraScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local interval = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local burst = tonumber(ARGV[4])
local countRejected = ARGV[5] == '1'

local tat = now
local stored = redis.call('GET', key)
if stored then
	tat = math.max(tonumber(stored), now)
end

local candidate = tat + interval
local allowed = candidate - now <= burst
local final = tat
if allowed then
	final = candidate
elseif countRejected then
	final = now + burst
end

local resetAfter = final - now
if final ~= tat then
	redis.call('SET', key, string.format('%.0f', final), 'PX', math.ceil(resetAfter / 1000))
end

local remaining = limit - math.ceil(resetAfter / interval)
if remaining < 0 then
	remaining = 0
elseif remaining > limit then
	remaining = limit
end

local flag = 0
local retryAfter = 0
if allowed then
	flag = 1
else
	retryAfter = math.max(final - (limit - 1) * interval - now, 0)
end
return {flag, remaining, resetAfter, retryAfter}
`)

func (l *RedisLimiter) gcraKey(
	k Key,
) string {
	return fmt.Sprintf(
		"%s:gcra:%s:%s",
		l.cfg.KeyPrefix,
		l.strategy.Name(),
		l.strategy.Key(k),
	)
}

func (l *RedisLimiter) gcraInterval(
	limit int32,
) int64 {
	return max(
		l.cfg.Window.Microseconds()/int64(limit),
		1,
	)
}

func (l *RedisLimiter) gcraArgs(
	now time.Time,
	interval int64,
	limit int32,
) []any {
	countRejected := 0
	if l.cfg.CountRejected {
		countRejected = 1
	}

	return []any{
		now.UnixMicro(),
		interval,
		limit,
		interval * int64(limit),
		countRejected,
	}
}

func (l *RedisLimiter) gcra(
	ctx context.Context,
	k Key,
	limit int32,
) (Quota, error) {
	key := l.gcraKey(k)
	now := l.now()

	reply, err := gcraScript.Run(
		ctx,
		l.client,
		[]string{key},
		l.gcraArgs(
			now,
			l.gcraInterval(limit),
			limit,
		)...,
	).Int64Slice()
	if err == nil && len(reply) != gcraReplyLen {
		err = fmt.Errorf(
			"gcra script reply has %d values",
			len(reply),
		)
	}
	if err != nil {
		allowed, failure := l.scriptFailed(
			err,
			key,
		)
		return Quota{
			Allowed: allowed,
			Limit:   limit,
		}, failure
	}

	return gcraQuotaOf(
		reply,
		limit,
		now,
	), nil
}

func gcraQuotaOf(
	reply []int64,
	limit int32,
	now time.Time,
) Quota {
	return Quota{
		Allowed:    reply[0] == 1,
		Limit:      limit,
		Remaining:  int32(reply[1]),
		ResetAt:    now.Add(time.Duration(reply[2]) * time.Microsecond),
		RetryAfter: time.Duration(reply[3]) * time.Microsecond,
		Known:      true,
	}
}
