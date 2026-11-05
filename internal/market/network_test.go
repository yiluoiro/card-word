package market

import (
	"context"
	"net"
	"net/netip"
	"testing"
)

func TestPublicDialPinsValidatedAddresses(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		lookups, dials := 0, 0
		lookup := func(context.Context, string, string) ([]netip.Addr, error) {
			lookups++
			ips := []netip.Addr{netip.MustParseAddr("8.8.8.8")}
			if mixed {
				ips = append(ips, netip.MustParseAddr("127.0.0.1"))
			}
			return ips, nil
		}
		dial := func(_ context.Context, network, address string) (net.Conn, error) {
			dials++
			if address != "8.8.8.8:443" || network != "tcp" {
				t.Errorf("unvalidated dial: %s %s", network, address)
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}
		conn, err := dialPublic(context.Background(), "tcp", "market.example:443", lookup, dial)
		if conn != nil {
			conn.Close()
		}
		if mixed {
			if err == nil || dials != 0 {
				t.Fatalf("mixed DNS accepted: err=%v dials=%d", err, dials)
			}
		} else if err != nil || dials != 1 {
			t.Fatalf("public DNS rejected: %v", err)
		}
		if lookups != 1 {
			t.Fatalf("DNS resolved %d times", lookups)
		}
	}
}

func TestPublicAddressPolicy(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "224.0.0.1", "240.1.2.3", "::", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		if publicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("accepted special address %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("rejected public address %s", raw)
		}
	}
	tr := marketTransport()
	if tr.Proxy != nil || tr.DialContext == nil {
		t.Fatal("unsafe transport configuration")
	}
}
