package clientip

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestFrom(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12")}
	for _, tc := range []struct {
		name, remote string
		xff          []string
		trusted      []netip.Prefix
		want         string
	}{
		{"no proxies: the peer, whatever the header says", "203.0.113.9:4000", []string{"198.51.100.1"}, nil, "203.0.113.9"},
		{"an untrusted peer: its header is ignored", "203.0.113.9:4000", []string{"198.51.100.1"}, trusted, "203.0.113.9"},
		{"behind one proxy", "10.0.1.5:4000", []string{"198.51.100.7"}, trusted, "198.51.100.7"},
		{"behind two proxies", "172.18.0.3:4000", []string{"198.51.100.7, 10.0.1.5"}, trusted, "198.51.100.7"},
		{"a forged left part is not used", "10.0.1.5:4000", []string{"1.2.3.4, 198.51.100.7"}, trusted, "198.51.100.7"},
		{"split over two header lines", "10.0.1.5:4000", []string{"1.2.3.4", "198.51.100.7"}, trusted, "198.51.100.7"},
		{"only proxies: the peer", "10.0.1.5:4000", []string{"10.0.2.2"}, trusted, "10.0.1.5"},
		{"garbage stops at the trusted peer", "10.0.1.5:4000", []string{"not-an-ip"}, trusted, "10.0.1.5"},
		{"IPv6 client", "10.0.1.5:4000", []string{"2001:db8::1"}, trusted, "2001:db8::1"},
		{"mapped IPv4 peer", "[::ffff:10.0.1.5]:4000", []string{"198.51.100.7"}, trusted, "198.51.100.7"},
	} {
		r := &http.Request{RemoteAddr: tc.remote, Header: http.Header{"X-Forwarded-For": tc.xff}}
		if got := From(r, tc.trusted); got.String() != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestKey(t *testing.T) {
	if Key(netip.MustParseAddr("203.0.113.9")) != "203.0.113.9" {
		t.Error("IPv4 is its own key")
	}
	a, b := Key(netip.MustParseAddr("2001:db8:1:2::1")), Key(netip.MustParseAddr("2001:db8:1:2:ffff::9"))
	if a != b || a != "2001:db8:1:2::/64" {
		t.Errorf("one /64 is one key: %s, %s", a, b)
	}
	if Key(netip.MustParseAddr("2001:db8:1:3::1")) == a {
		t.Error("another /64 is another key")
	}
	if Key(netip.Addr{}) != "" {
		t.Error("no address, no key")
	}
}
