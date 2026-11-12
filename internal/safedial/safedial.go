// Package safedial refuses outbound connections to addresses a portal must
// never reach on behalf of a user (security.md section 5): loopback, private,
// link-local (including cloud metadata such as 169.254.169.254), CGNAT,
// multicast, unspecified and reserved addresses (0/8, 198.18/15, 240/4) and
// the NAT64 and 6to4 prefixes.
//
// The check runs on the address the connection is actually made to, after DNS
// resolution and for every redirect, so the resolved address is pinned for the
// connection and DNS rebinding cannot slip a private address in between the
// check and the connect.
package safedial

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// reserved are ranges that are not public unicast destinations: "this
// network" (0/8), benchmarking (198.18/15, also used by fake-IP DNS of
// proxies), the reserved class E block with broadcast (240/4), and the IPv6
// translation prefixes NAT64 (64:ff9b::/96) and 6to4 (2002::/16), which
// embed an IPv4 address that could be private.
var reserved = []struct {
	net    *net.IPNet
	reason string
}{
	{mustCIDR("0.0.0.0/8"), "reserved (this network)"},
	{mustCIDR("198.18.0.0/15"), "reserved (benchmarking)"},
	{mustCIDR("240.0.0.0/4"), "reserved"},
	{mustCIDR("64:ff9b::/96"), "NAT64"},
	{mustCIDR("64:ff9b:1::/48"), "NAT64"},
	{mustCIDR("2002::/16"), "6to4"},
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// Reason returns why ip is refused, or "" when it may be used. With
// allowPrivate (the development switch OBX_ALLOW_PRIVATE_URLS) loopback,
// private and CGNAT addresses are allowed, but link-local addresses, and so
// cloud metadata endpoints, never are.
func Reason(ip net.IP, allowPrivate bool) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsMulticast():
		return "multicast"
	case allowPrivate:
		return ""
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private"
	case cgnat.Contains(ip):
		return "shared (CGNAT)"
	}
	for _, r := range reserved {
		if r.net.Contains(ip) {
			return r.reason
		}
	}
	return ""
}

// ErrRefused is wrapped by every refused connection.
var ErrRefused = errors.New("address refused")

// Control is a net.Dialer Control function that refuses unsafe addresses.
func Control(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrRefused, address)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: %s is not an IP address", ErrRefused, host)
		}
		if reason := Reason(ip, allowPrivate); reason != "" {
			return fmt.Errorf("%w: %s is a %s address", ErrRefused, ip, reason)
		}
		return nil
	}
}

// Transport returns an HTTP transport that checks every connection with
// Control, ignores proxy environment variables (a proxy would hide the real
// target) and has timeouts on every step.
func Transport(allowPrivate bool) *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: Control(allowPrivate)}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}
