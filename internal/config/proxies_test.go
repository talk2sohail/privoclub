package config

import (
	"strings"
	"testing"
)

// Review of 2026-11-15, S-01: the proxies in front of Privo are named as CIDRs.
func TestTrustedProxies(t *testing.T) {
	cfg, err := Load(env(map[string]string{}))
	if err != nil || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("default: %v, %v", cfg.TrustedProxies, err)
	}
	cfg, err = Load(env(map[string]string{"PRIVO_TRUSTED_PROXIES": " 10.0.0.0/8, 172.16.5.9/12 ,2001:db8::/32"}))
	if err != nil || len(cfg.TrustedProxies) != 3 || cfg.TrustedProxies[1].String() != "172.16.0.0/12" {
		t.Fatalf("parsed: %v, %v", cfg.TrustedProxies, err)
	}
	if _, err := Load(env(map[string]string{"PRIVO_TRUSTED_PROXIES": "10.0.0.1"})); err == nil || !strings.Contains(err.Error(), "PRIVO_TRUSTED_PROXIES") {
		t.Fatalf("a bare address is refused: %v", err)
	}
}
