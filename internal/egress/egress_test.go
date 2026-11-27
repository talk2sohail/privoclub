package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
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

func TestOnBlockedStrictlyRedactsAndNeverAltersRefusal(t *testing.T) {
	type ctxKey struct{}
	type event struct {
		tag    string
		target string
		reason string
	}
	var got []event
	record := func(ctx context.Context, target, reason string) {
		tag, _ := ctx.Value(ctxKey{}).(string)
		got = append(got, event{tag: tag, target: target, reason: reason})
	}

	p := Policy{OnBlocked: record}
	ctx := context.WithValue(context.Background(), ctxKey{}, "ctx-1")

	// 1. Policy.Check (direct)
	if err := p.Check(netip.MustParseAddr("169.254.169.254")); !errors.Is(err, ErrBlocked) {
		t.Fatalf("Policy.Check: %v", err)
	}

	// 2. Policy.CheckURL across refusal paths with userinfo, secret path, query, and fragment
	secrets := []string{"SECRETUSER", "SECRETPASS", "secret-token-path", "api_key", "TOPSECRET", "fragsecret"}
	for _, raw := range []string{
		"ftp://SECRETUSER:SECRETPASS@evil.example:21/secret-token-path?api_key=TOPSECRET#fragsecret",
		"https://SECRETUSER:SECRETPASS@example.com/secret-token-path?api_key=TOPSECRET#fragsecret",
		"http://127.0.0.1:8080/secret-token-path?api_key=TOPSECRET#fragsecret",
		"http://[::1]:8080/secret-token-path?api_key=TOPSECRET#fragsecret",
	} {
		if _, err := p.CheckURL(ctx, raw, nil); !errors.Is(err, ErrBlocked) {
			t.Fatalf("CheckURL(%q) = %v, want ErrBlocked", raw, err)
		}
	}

	// 3. NewClient dialer ControlContext via Options.OnBlocked
	client := NewClient(Options{
		SameHostOnly: true,
		OnBlocked:    record,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9999/secret-token-path?api_key=TOPSECRET#fragsecret", nil)
	if _, err := client.Do(req); !errors.Is(err, ErrBlocked) {
		t.Fatalf("client.Do loopback = %v, want ErrBlocked", err)
	}

	// 4. NewClient CheckRedirect refusal paths
	baseReq := &http.Request{URL: mustURL(t, "https://app.example/mcp")}
	for _, raw := range []string{
		"https://SECRETUSER:SECRETPASS@other.example/secret-token-path?api_key=TOPSECRET#fragsecret",
		"ftp://SECRETUSER:SECRETPASS@app.example/secret-token-path?api_key=TOPSECRET#fragsecret",
		"http://SECRETUSER:SECRETPASS@app.example/secret-token-path?api_key=TOPSECRET#fragsecret",
	} {
		r := (&http.Request{URL: mustURL(t, raw)}).WithContext(ctx)
		if err := client.CheckRedirect(r, []*http.Request{baseReq}); !errors.Is(err, ErrBlocked) {
			t.Fatalf("CheckRedirect(%q) = %v, want ErrBlocked", raw, err)
		}
	}

	want := []event{
		{tag: "", target: "169.254.169.254", reason: "link-local (includes cloud metadata)"},
		{tag: "ctx-1", target: "ftp://evil.example:21", reason: "scheme must be http or https"},
		{tag: "ctx-1", target: "https://example.com", reason: "credentials in URL"},
		{tag: "ctx-1", target: "http://127.0.0.1:8080", reason: "loopback"},
		{tag: "ctx-1", target: "http://[::1]:8080", reason: "loopback"},
		{tag: "ctx-1", target: "127.0.0.1", reason: "loopback"},
		{tag: "ctx-1", target: "https://other.example", reason: "cross-host redirect"},
		{tag: "ctx-1", target: "ftp://app.example", reason: "unsupported scheme"},
		{tag: "ctx-1", target: "http://app.example", reason: "redirect downgrades https to http"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
		for _, s := range secrets {
			if strings.Contains(got[i].target, s) || strings.Contains(got[i].reason, s) {
				t.Errorf("event[%d] leaked %q: %+v", i, s, got[i])
			}
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
