package ratelimit

type RPM int32

func (r RPM) RPM() int32 {
	return int32(r)
}

type IP string

func (i IP) String() string {
	return string(i)
}
