package upstreamauth

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
)

// A server that never answers gives a TimeoutError naming its host and the
// wait, never the path or query (they may carry a code or a token).
func TestTimeoutNamesTheHostOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	c := &Client{HTTP: egress.NewClient(egress.Options{Policy: egress.Policy{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}}), Timeout: time.Second}
	_, err = c.ServerMetadataFor(context.Background(), "https://"+ln.Addr().String()+"/tenant-secret")
	var te *TimeoutError
	if !errors.As(err, &te) || te.Host != ln.Addr().String() || te.Wait != time.Second {
		t.Fatalf("error %v (%T)", err, err)
	}
	if msg := err.Error(); strings.Contains(msg, "tenant-secret") || !strings.Contains(msg, "did not answer within 1 second") {
		t.Fatalf("message: %q", msg)
	}
	// A caller that gives up is not the server's timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ServerMetadataFor(ctx, "https://"+ln.Addr().String()); errors.As(err, &te) {
		t.Fatalf("a cancelled request was called a timeout: %v", err)
	}
}
