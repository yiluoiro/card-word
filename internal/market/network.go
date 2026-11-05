package market

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// Resolve once, validate every answer, then dial a numeric address to avoid
// DNS rebinding between validation and connection establishment.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range nonPublicNetworks {
		if block.Contains(ip) {
			return false
		}
	}
	return ip.Is4() || netip.MustParsePrefix("2000::/3").Contains(ip)
}

var nonPublicNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func validateMarketURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("market: public https URL without credentials required")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && (!publicAddress(ip) || ip.Zone() != "") {
		return errors.New("market: non-public address denied")
	}
	return nil
}

type lookupIPs func(context.Context, string, string) ([]netip.Addr, error)
type dialIP func(context.Context, string, string) (net.Conn, error)

func dialPublic(ctx context.Context, network, address string, lookup lookupIPs, dial dialIP) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("market: no destination addresses")
	}
	for _, ip := range ips {
		if !publicAddress(ip) || ip.Zone() != "" {
			return nil, errors.New("market: non-public DNS answer denied")
		}
	}
	for _, ip := range ips {
		var conn net.Conn
		conn, err = dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

func marketTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		// No environment proxy: it could resolve/reach a prohibited target.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPublic(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConns:          16,
	}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if err := validateMarketURL(req.URL.String()); err != nil {
		return nil, err
	}
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}
