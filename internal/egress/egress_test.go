package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestCheck(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "fe80::1", "fc00::1", "0.0.0.0", "::",
		"100.64.0.1", "224.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"198.18.0.1", "240.0.0.1", "64:ff9b:1::a00:1", "2002:a00:1::1", "2001:0:4136:e378::1",
	}
	for _, s := range blocked {
		if err := (Policy{}).Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if err := (Policy{}).Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s blocked: %v", s, err)
		}
	}
}

func TestAllowList(t *testing.T) {
	allow, err := ParseAllowList(" 10.0.0.0/8, 127.0.0.1/32 ,")
	if err != nil {
		t.Fatal(err)
	}
	p := Policy{Allow: allow}
	for _, s := range []string{"10.9.9.9", "127.0.0.1", "::ffff:127.0.0.1"} {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s blocked despite allow list: %v", s, err)
		}
	}
	if err := p.Check(netip.MustParseAddr("127.0.0.2")); err == nil {
		t.Error("127.0.0.2 allowed outside the allow list")
	}
	if _, err := ParseAllowList("not-a-cidr"); err == nil {
		t.Error("bad CIDR accepted")
	}
}

func TestClientRefusesLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	_, err := NewClient(Options{}).Do(req)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback request: err = %v, want ErrBlocked", err)
	}

	allow, _ := ParseAllowList("127.0.0.0/8")
	resp, err := NewClient(Options{Policy: Policy{Allow: allow}}).Do(req.Clone(context.Background()))
	if err != nil {
		t.Fatalf("allow-listed loopback: %v", err)
	}
	_ = resp.Body.Close()
}

func TestClientChecksRedirects(t *testing.T) {
	allow, _ := ParseAllowList("127.0.0.0/8")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL, nil)
	_, err := NewClient(Options{Policy: Policy{Allow: allow}, SameHostOnly: true}).Do(req)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("cross-host redirect: err = %v, want ErrBlocked", err)
	}
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	p := Policy{}
	for _, raw := range []string{"http://127.0.0.1:1/mcp", "http://localhost/mcp", "ftp://example.com", "https://u:p@example.com/", "http://[::1]/"} {
		if _, err := p.CheckURL(ctx, raw, nil); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if _, err := p.CheckURL(ctx, "https://1.1.1.1/mcp", nil); err != nil {
		t.Errorf("public IP refused: %v", err)
	}
}
