package ratelimit

type Algorithm string

const (
	AlgorithmSlidingWindow Algorithm = "sliding"
	AlgorithmGCRA          Algorithm = "gcra"
)

func (a Algorithm) String() string {
	return string(a)
}
