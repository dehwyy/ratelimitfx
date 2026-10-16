package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const quotaReplyLen = 3

var quotaWindowScript = redis.NewScript(`
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]
local ttl = tonumber(ARGV[5])
local countRejected = ARGV[6] == '1'

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)
local allowed = count < limit
if allowed or countRejected then
	count = count + redis.call('ZADD', key, now, member)
end
redis.call('PEXPIRE', key, ttl)

local oldest = -1
local head = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
if head[2] then
	oldest = tonumber(head[2])
end

local flag = 0
if allowed then
	flag = 1
end
return {flag, count, oldest}
`)

func (l *RedisLimiter) AllowNWithQuota(
	ctx context.Context,
	k Key,
	limit int32,
) (Quota, error) {
	if limit <= 0 {
		return Quota{
			Allowed: true,
			Limit:   limit,
		}, nil
	}

	key := l.windowKey(k)
	now := l.now()

	reply, err := quotaWindowScript.Run(
		ctx,
		l.client,
		[]string{key},
		l.windowArgs(
			now,
			limit,
		)...,
	).Int64Slice()
	if err == nil && len(reply) != quotaReplyLen {
		err = fmt.Errorf(
			"quota script reply has %d values",
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

	return l.quotaOf(
		reply,
		limit,
		now,
	), nil
}

func (l *RedisLimiter) quotaOf(
	reply []int64,
	limit int32,
	now time.Time,
) Quota {
	allowed := reply[0] == 1
	resetAt := now.Add(l.cfg.Window)
	if reply[2] >= 0 {
		resetAt = time.UnixMilli(reply[2]).Add(l.cfg.Window)
	}

	remaining := max(
		int64(limit)-reply[1],
		0,
	)

	quota := Quota{
		Allowed:   allowed,
		Limit:     limit,
		Remaining: int32(remaining),
		ResetAt:   resetAt,
		Known:     true,
	}
	if !allowed {
		quota.RetryAfter = max(
			resetAt.Sub(now),
			0,
		)
	}

	return quota
}
