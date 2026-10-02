package ratelimit

type StringKey string

func (k StringKey) String() string { return string(k) }
