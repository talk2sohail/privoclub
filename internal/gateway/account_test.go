package gateway

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"privo.club/privo/internal/upstream"
)

// ADR 0017: a call to a server that runs with each person's own account tells
// the person what to do, and the text reaches the client unchanged.

func TestNoGrantGivesTheConnectLink(t *testing.T) {
	g := &Gateway{BaseURL: "https://privo.example/", Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	c := call{item: upstream.Item{Alias: "linear"}, action: "tools/call"}
	err := g.clientFacing(c, fmt.Errorf("wrapped: %w", g.connectFirst("linear")))
	if err == nil || !strings.Contains(err.Error(), "https://privo.example/connect/linear") || strings.Contains(err.Error(), "//connect") {
		t.Fatalf("client sees %v", err)
	}
	// The link always points to Privo, never to the provider; without a public
	// URL there is no link at all.
	g.BaseURL = ""
	if err := g.connectFirst("linear"); strings.Contains(err.Error(), "http") {
		t.Fatalf("link without a public URL: %v", err)
	}
	if err := (&Gateway{BaseURL: "https://privo.example"}).connectFirst("a/b"); !strings.Contains(err.Error(), "/connect/a%2Fb") {
		t.Fatalf("alias not escaped: %v", err)
	}
}

func TestNoUserGivesTheInviteMessage(t *testing.T) {
	g := &Gateway{Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	c := call{item: upstream.Item{Alias: "linear"}, action: "tools/call"}
	err := g.clientFacing(c, noAccount("linear"))
	if err == nil || !strings.Contains(err.Error(), "invite") || !strings.Contains(err.Error(), `"linear"`) {
		t.Fatalf("client sees %v", err)
	}
}
