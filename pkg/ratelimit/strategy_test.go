package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerKeyStrategy_DefaultLimit(t *testing.T) {
	require.Equal(t, DefaultPerKeyLimit, PerKeyStrategy{}.Limit())
	require.Equal(t, RPM(500), PerKeyStrategy{RPM: 500}.Limit())
	require.Equal(t, "key-strategy", PerKeyStrategy{}.Name())
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		remote  string
		want    string
	}{
		{
			name: "xff takes first entry",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.1, 10.0.0.1",
			},
			remote: "127.0.0.1:8080",
			want:   "203.0.113.1",
		},
		{
			name: "x-real-ip fallback",
			headers: map[string]string{
				"X-Real-IP": "198.51.100.2",
			},
			remote: "127.0.0.1:8080",
			want:   "198.51.100.2",
		},
		{
			name:    "remote addr fallback",
			headers: nil,
			remote:  "127.0.0.1:8080",
			want:    "127.0.0.1",
		},
		{
			name: "empty xff falls through",
			headers: map[string]string{
				"X-Forwarded-For": "",
				"X-Real-IP":       "198.51.100.3",
			},
			remote: "127.0.0.1:8080",
			want:   "198.51.100.3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			require.Equal(t, tc.want, ClientIP(r))
		})
	}
}
