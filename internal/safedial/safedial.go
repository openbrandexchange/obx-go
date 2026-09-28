// Package safedial refuses outbound connections to addresses a portal must
// never reach on behalf of a user (security.md section 5): loopback, private,
// link-local (including cloud metadata such as 169.254.169.254), CGNAT,
// multicast and unspecified addresses.
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
