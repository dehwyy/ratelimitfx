package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"10.0.0.0/8", "127.0.0.1", "fd00::/8"})
	require.NoError(t, err)

	tests := []struct {
		name    string
		trusted []netip.Prefix
		headers map[string][]string
		remote  string
		want    string
	}{
		{
			name:    "no trusted proxies ignores spoofed xff",
			trusted: nil,
			headers: map[string][]string{"X-Forwarded-For": {"1.2.3.4"}},
			remote:  "203.0.113.9:4000",
			want:    "203.0.113.9",
		},
		{
			name:    "no trusted proxies ignores x-real-ip",
			trusted: nil,
			headers: map[string][]string{"X-Real-IP": {"1.2.3.4"}},
			remote:  "203.0.113.9:4000",
			want:    "203.0.113.9",
		},
		{
			name:    "untrusted peer cannot forge xff even when proxies are configured",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"1.2.3.4, 10.0.0.1"}},
			remote:  "203.0.113.9:4000",
			want:    "203.0.113.9",
		},
		{
			name:    "trusted peer client from xff",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"198.51.100.7"}},
			remote:  "10.1.2.3:4000",
			want:    "198.51.100.7",
		},
		{
			name:    "client-prepended forged entry is ignored, rightmost untrusted wins",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"6.6.6.6, 198.51.100.7, 10.0.0.5"}},
			remote:  "10.1.2.3:4000",
			want:    "198.51.100.7",
		},
		{
			name:    "multiple xff header lines are joined",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"6.6.6.6", "198.51.100.7, 10.0.0.5"}},
			remote:  "10.1.2.3:4000",
			want:    "198.51.100.7",
		},
		{
			name:    "all hops trusted returns leftmost",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"10.9.9.9, 10.0.0.5"}},
			remote:  "10.1.2.3:4000",
			want:    "10.9.9.9",
		},
		{
			name:    "garbage entry stops the walk at last valid hop",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"198.51.100.7, garbage, 10.0.0.5"}},
			remote:  "10.1.2.3:4000",
			want:    "10.0.0.5",
		},
		{
			name:    "trusted peer x-real-ip when no xff",
			trusted: trusted,
			headers: map[string][]string{"X-Real-IP": {"198.51.100.2"}},
			remote:  "127.0.0.1:8080",
			want:    "198.51.100.2",
		},
		{
			name:    "trusted peer invalid x-real-ip falls back to peer",
			trusted: trusted,
			headers: map[string][]string{"X-Real-IP": {"nope"}},
			remote:  "127.0.0.1:8080",
			want:    "127.0.0.1",
		},
		{
			name:    "trusted peer without headers returns peer",
			trusted: trusted,
			headers: nil,
			remote:  "127.0.0.1:8080",
			want:    "127.0.0.1",
		},
		{
			name:    "ipv6 trusted peer",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"2001:db8::1"}},
			remote:  "[fd00::5]:4000",
			want:    "2001:db8::1",
		},
		{
			name:    "ipv4-mapped ipv6 peer matches ipv4 prefix",
			trusted: trusted,
			headers: map[string][]string{"X-Forwarded-For": {"198.51.100.7"}},
			remote:  "[::ffff:10.1.2.3]:4000",
			want:    "198.51.100.7",
		},
		{
			name:    "remote addr without port",
			trusted: nil,
			headers: nil,
			remote:  "203.0.113.9",
			want:    "203.0.113.9",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			for name, values := range tc.headers {
				for _, value := range values {
					r.Header.Add(name, value)
				}
			}
			require.Equal(t, tc.want, ClientIP(r, tc.trusted...))
		})
	}
}

func TestClientIP_SpoofedXFFDoesNotChangeLimiterKey(t *testing.T) {
	first := httptest.NewRequest(http.MethodGet, "/", nil)
	first.RemoteAddr = "203.0.113.9:1111"
	first.Header.Set("X-Forwarded-For", "1.1.1.1")

	second := httptest.NewRequest(http.MethodGet, "/", nil)
	second.RemoteAddr = "203.0.113.9:2222"
	second.Header.Set("X-Forwarded-For", "2.2.2.2")

	require.Equal(t, ClientIP(first), ClientIP(second))
}

func TestParseTrustedProxies(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []string
		wantErr bool
	}{
		{name: "empty", values: nil, want: []string{}},
		{name: "cidr is masked", values: []string{"10.1.2.3/8"}, want: []string{"10.0.0.0/8"}},
		{name: "bare ipv4", values: []string{"127.0.0.1"}, want: []string{"127.0.0.1/32"}},
		{name: "bare ipv6", values: []string{"::1"}, want: []string{"::1/128"}},
		{name: "blank entries skipped", values: []string{" ", "", " 10.0.0.0/8 "}, want: []string{"10.0.0.0/8"}},
		{name: "bad cidr", values: []string{"10.0.0.0/99"}, wantErr: true},
		{name: "bad ip", values: []string{"not-an-ip"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prefixes, err := ParseTrustedProxies(tc.values)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := make([]string, 0, len(prefixes))
			for _, prefix := range prefixes {
				got = append(got, prefix.String())
			}
			require.Equal(t, tc.want, got)
		})
	}
}
