package ratelimit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerKeyStrategy_DefaultLimit(t *testing.T) {
	require.Equal(t, DefaultPerKeyLimit, PerKeyStrategy{}.Limit())
	require.Equal(t, RPM(500), PerKeyStrategy{RPM: 500}.Limit())
	require.Equal(t, "key-strategy", PerKeyStrategy{}.Name())
}
