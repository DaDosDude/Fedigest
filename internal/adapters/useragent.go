package adapters

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// UserAgent is sent on every outgoing request to a fedi instance. Some
// Cloudflare-fronted instances block generic library agents. main sets it
// from SITE_DOMAIN.
var UserAgent = "Fedigest/1.0"

// BlockPrivateNetworks refuses connections to loopback, private and
// link-local addresses, so a pasted URL can't make the server fetch from
// its own network. main turns it on; tests leave it off for local fakes.
var BlockPrivateNetworks = false

var errPrivateAddress = errors.New("refusing to connect to a private network address")

type userAgentTransport struct{ base http.RoundTripper }

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkPaused(req); err != nil {
		return nil, err // the instance asked us to slow down (pause.go)
	}
	if err := waitTurn(req.Context(), req.URL.Host); err != nil {
		return nil, err // one request a second per instance (schedule.go)
	}
	if err := checkPaused(req); err != nil {
		return nil, err // it refused someone else while this one waited
	}
	if req.Header.Get("User-Agent") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", UserAgent)
	}
	return t.base.RoundTrip(req)
}

// publicOnly is a dialer Control hook; it sees the resolved address, so a
// public name pointing at a private address is caught too.
func publicOnly(network, address string, _ syscall.RawConn) error {
	if !BlockPrivateNetworks {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return errPrivateAddress
	}
	for _, n := range reservedNets {
		if n.Contains(ip) {
			return errPrivateAddress
		}
	}
	return nil
}

// reservedNets are other non-public ranges: shared address space (carrier
// NAT, and VPNs such as Tailscale), benchmarking, and reserved.
var reservedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		_, n, _ := net.ParseCIDR(cidr)
		out = append(out, n)
	}
	return out
}()

var outgoingTransport = func() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: publicOnly}).DialContext
	return t
}()

// NewHTTPClient returns a client with the given timeout that sends UserAgent.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: userAgentTransport{base: outgoingTransport}}
}
