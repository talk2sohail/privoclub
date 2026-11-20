package control

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
)

// A provider that does not answer is named, with the step and the wait,
// never "context deadline exceeded" (F26, seen with Figma on 2026-11-04).
func TestTimeoutNamesTheHostAndStep(t *testing.T) {
	e := newSessionEnv(t)
	withPersonalServers(e)
	e.api.OAuth = &upstreamauth.Client{HTTP: egress.NewClient(egress.Options{Policy: e.api.Egress}), Timeout: time.Second}
	_, owner := e.user(t, "boss", store.RoleOwner)
	// A provider that accepts connections and never says a word.
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
	host := ln.Addr().String()

	out := e.call(t, owner, http.MethodPost, "/api/v1/servers", map[string]any{"alias": "slow", "transport": "http", "url": "https://" + host + "/mcp", "oauth": map[string]any{}},
		http.StatusUnprocessableEntity)
	msg, _ := out["error"].(string)
	if strings.Contains(msg, "deadline") || !strings.Contains(msg, host) || !strings.Contains(msg, "did not answer within 1 second") || !strings.Contains(msg, "finding") {
		t.Fatalf("message: %q", msg)
	}
}
