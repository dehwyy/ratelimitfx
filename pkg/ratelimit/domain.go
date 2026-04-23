package ratelimit

import "fmt"

type RPM int32

func (r RPM) RPM() int32 {
	return int32(r)
}

type Key fmt.Stringer
