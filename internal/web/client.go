package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies is which hops may tell this service who a request is from.
//
// # Why this is not simply "read X-Forwarded-For"
//
// Both answers are wrong on their own. `RemoteAddr` behind a proxy is the
// proxy, so a limiter keyed on it would put every reader in the world in one
// bucket and refuse the site to everybody the moment one script ran. And
// `X-Forwarded-For` is a header the client sends: a caller who can reach this
// service directly sets it to a different value on every request and walks
// through any per-address limit as if it were not there.
//
// So the header is believed exactly as far as the hops that set it can be. A
// request arriving from an address in this list is assumed to have come through
// somebody's proxy and its header is read; a request arriving from anywhere
// else is taken at its own address, whatever it claims about itself.
//
// # The default is the private ranges, and that is the shape of the deployment
//
// These containers are distroless and terminate no TLS, so there is always
// something in front: an ingress, a load balancer, a reverse proxy. In a
// cluster that hop has a private address, and on the local runner it is
// loopback. An installation that really does face the internet with nothing in
// front wants an empty list, which is what makes every reader their own bucket
// again.
type TrustedProxies []netip.Prefix

// DefaultTrustedProxies is loopback and the private ranges.
func DefaultTrustedProxies() TrustedProxies {
	return ParsedOrPanic(
		"127.0.0.0/8", "::1/128", // loopback: the local runner
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", "fe80::/10", // link-local
		"fc00::/7", // IPv6 unique local, which is what a cluster hands out
	)
}

// ParsedOrPanic is for the default list, which is written here and cannot be
// wrong at runtime.
func ParsedOrPanic(values ...string) TrustedProxies {
	parsed, err := ParseTrustedProxies(values)
	if err != nil {
		panic(err)
	}
	return parsed
}

// ParseTrustedProxies reads a configured list.
//
// A bare address is accepted as well as a prefix, because "the proxy is at
// 10.4.2.1" is how an operator thinks about one hop, and making them write
// `/32` is a way to get a startup failure rather than a correct setting.
func ParseTrustedProxies(values []string) (TrustedProxies, error) {
	var parsed TrustedProxies
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			parsed = append(parsed, prefix)
			continue
		}
		address, err := netip.ParseAddr(value)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or a CIDR range", value)
		}
		parsed = append(parsed, netip.PrefixFrom(address, address.BitLen()))
	}
	return parsed, nil
}

// trusts reports whether a hop may speak for somebody else.
func (t TrustedProxies) trusts(address netip.Addr) bool {
	// Unmapped, so a proxy written as 10.0.0.1 also matches a connection
	// reported as ::ffff:10.0.0.1.
	address = address.Unmap()
	for _, prefix := range t {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// ClientAddress is who a request is from, for the purpose of bounding it.
//
// The forwarded chain is read from the right, which is the only end that can
// be trusted: each hop appends the address it heard from, so the rightmost
// entries were written by the hops nearest this service and the leftmost by
// whoever sent the request — who may have written anything at all. Walking
// right past the hops we know, the first address we do not recognise is the
// furthest point we have any reason to believe.
func (t TrustedProxies) ClientAddress(r *http.Request) string {
	peer := hostOf(r.RemoteAddr)

	address, err := netip.ParseAddr(peer)
	if err != nil || !t.trusts(address) {
		// Not a hop we trust, so nothing it says about anybody else counts.
		return peer
	}

	chain := forwardedChain(r)
	for i := len(chain) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(chain[i])
		if err != nil {
			// Unparseable, so the chain stops being evidence here.
			return peer
		}
		if !t.trusts(hop) {
			return hop.Unmap().String()
		}
	}
	// Every hop in the chain is one of ours, which means the chain never
	// reached anybody else: the request came from inside.
	return peer
}

// forwardedChain is the X-Forwarded-For entries, left to right, across however
// many header lines a chain of proxies produced.
func forwardedChain(r *http.Request) []string {
	var chain []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, entry := range strings.Split(header, ",") {
			if entry = strings.TrimSpace(entry); entry != "" {
				chain = append(chain, hostOf(entry))
			}
		}
	}
	return chain
}

// hostOf drops the port, without which every request from one machine is a
// new caller.
func hostOf(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	// A bare IPv6 address has colons of its own and no port to cut.
	return strings.Trim(address, "[]")
}
