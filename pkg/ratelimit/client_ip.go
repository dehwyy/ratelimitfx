package ratelimit

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func ClientIP(
	r *http.Request,
	trusted ...netip.Prefix,
) string {
	peer := remoteHost(r.RemoteAddr)
	peerAddr, err := netip.ParseAddr(peer)
	if err != nil || !isTrusted(peerAddr, trusted) {
		return peer
	}

	current := peerAddr.Unmap()
	forwarded := forwardedChain(r)
	if len(forwarded) == 0 {
		realIP, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP")))
		if err != nil {
			return current.String()
		}
		return realIP.Unmap().String()
	}

	for i := len(forwarded) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(forwarded[i])
		if err != nil {
			return current.String()
		}
		current = hop.Unmap()
		if !isTrusted(current, trusted) {
			return current.String()
		}
	}
	return current.String()
}

func forwardedChain(r *http.Request) []string {
	var chain []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				chain = append(chain, part)
			}
		}
	}
	return chain
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
