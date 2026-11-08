package api

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// clientIPHeader is the request header that carries the address of the
// client when the server runs behind a proxy, or "" when the connection's
// own address is the client's. In production (DM_PRODUCTION) every request comes from the host's edge
// proxy, which sets X-Real-IP to the client's address and overwrites the one
// a client sends; CLIENT_IP_HEADER names another header behind another proxy,
// or "none" to use the connection's address.
func clientIPHeader() string {
	switch h := os.Getenv("CLIENT_IP_HEADER"); {
	case h == "none":
		return ""
	case h != "":
		return h
	case os.Getenv("DM_PRODUCTION") != "":
		return "X-Real-IP"
	}
	return ""
}

// keyByClientIP returns the rate limiter key of a request: the client's
// address, read from header when set. Without it, every client behind the
// proxy would share the proxy's address, and so one limit.
func keyByClientIP(header string) func(r *http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		if header != "" {
			if ip := net.ParseIP(strings.TrimSpace(r.Header.Get(header))); ip != nil {
				return canonicalIP(ip), nil
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil {
			return canonicalIP(ip), nil
		}
		return host, nil
	}
}

// canonicalIP keys an IPv6 client by its /64: one host usually holds the
// whole prefix, and would otherwise get a fresh limit per address.
func canonicalIP(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// envLimit reads a positive per-minute limit from the environment.
func envLimit(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
