package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		if net.ParseIP(xrip) != nil {
			return xrip
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	return ""
}

func readAPIKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("X-API-KEY")); k != "" {
		return k
	}
	return ""
}

// apiKeysEqual uses constant-time comparison to prevent timing attacks.
func apiKeysEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
