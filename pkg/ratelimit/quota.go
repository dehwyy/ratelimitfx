package ratelimit

import "time"

type Quota struct {
	Allowed    bool
	Limit      int32
	Remaining  int32
	ResetAt    time.Time
	RetryAfter time.Duration
	Known      bool
}
