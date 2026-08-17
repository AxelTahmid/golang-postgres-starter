// Package clientip resolves the address of the client that originated a request.
// Forwarding headers are trusted only when the immediate peer is a configured proxy.
package clientip

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

const (
	forwardedForHeader = "X-Forwarded-For"
	realIPHeader       = "X-Real-IP"
	trueClientIPHeader = "True-Client-IP"
)

type clientAddrKey struct{}

// Resolver resolves client addresses through a fixed set of trusted proxies.
// The zero value trusts no forwarding headers.
type Resolver struct {
	trusted []netip.Prefix
}

func NewResolver(trusted []netip.Prefix) *Resolver {
	normalized := make([]netip.Prefix, 0, len(trusted))
	for _, prefix := range trusted {
		if prefix.IsValid() {
			normalized = append(normalized, prefix.Masked())
		}
	}
	return &Resolver{trusted: slices.Clip(normalized)}
}

// ParsePrefixes accepts CIDR blocks and bare addresses. Invalid entries are
// dropped, which safely stops trusting that proxy.
func ParsePrefixes(entries []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(trimmed); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(trimmed); err == nil {
			addr = normalize(addr)
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		slog.Warn("clientip: ignoring unparseable trusted proxy entry", "entry", trimmed)
	}
	return slices.Clip(prefixes)
}

// Middleware computes the client address once, publishes it through the
// request context and mirrors it to RemoteAddr for standard middleware.
func (rs *Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(trueClientIPHeader)
		addr, ok := rs.Resolve(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), clientAddrKey{}, addr))
		r.RemoteAddr = addr.String()
		next.ServeHTTP(w, r)
	})
}

// Resolve returns the peer for direct requests. For trusted proxies it walks
// X-Forwarded-For right-to-left to find the first untrusted hop, then falls
// back to X-Real-IP and finally the peer.
func (rs *Resolver) Resolve(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return netip.Addr{}, false
	}
	if !rs.IsTrusted(peer) {
		return peer, true
	}
	if addr, found := rs.fromForwardedFor(r.Header.Values(forwardedForHeader)); found {
		return addr, true
	}
	if addr, found := parseAddr(r.Header.Get(realIPHeader)); found {
		return addr, true
	}
	return peer, true
}

func (rs *Resolver) IsTrusted(addr netip.Addr) bool {
	addr = normalize(addr)
	for _, prefix := range rs.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (rs *Resolver) fromForwardedFor(values []string) (netip.Addr, bool) {
	var entries []string
	for _, value := range values {
		entries = append(entries, strings.Split(value, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		addr, ok := parseAddr(entries[i])
		if !ok {
			return netip.Addr{}, false
		}
		if !rs.IsTrusted(addr) {
			return addr, true
		}
	}
	return netip.Addr{}, false
}

func FromContext(ctx context.Context) (netip.Addr, bool) {
	if ctx == nil {
		return netip.Addr{}, false
	}
	addr, ok := ctx.Value(clientAddrKey{}).(netip.Addr)
	return addr, ok && addr.IsValid()
}

func AddrFromRequest(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	if addr, ok := FromContext(r.Context()); ok {
		return addr, true
	}
	return parseAddr(r.RemoteAddr)
}

func FromRequest(r *http.Request) string {
	addr, ok := AddrFromRequest(r)
	if !ok {
		return ""
	}
	return addr.String()
}

func parseAddr(value string) (netip.Addr, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(trimmed); err == nil {
		return normalize(addr), true
	}
	if addrPort, err := netip.ParseAddrPort(trimmed); err == nil {
		return normalize(addrPort.Addr()), true
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		if addr, parseErr := netip.ParseAddr(strings.TrimSpace(host)); parseErr == nil {
			return normalize(addr), true
		}
	}
	return netip.Addr{}, false
}

func normalize(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}
