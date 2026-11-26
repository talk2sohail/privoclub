package config

import (
	"strings"
	"testing"
	"time"
)

// R-04: what an upstream may make the gateway hold or do is set by the operator.
func TestUpstreamLimitsConfig(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CallTimeout != 10*time.Minute || cfg.RefreshGap != 5*time.Second || cfg.MaxResponseBytes != 16<<20 {
		t.Errorf("defaults: call timeout %s, refresh gap %s, max response %d; want 10m, 5s, 16 MiB",
			cfg.CallTimeout, cfg.RefreshGap, cfg.MaxResponseBytes)
	}

	cfg, err = Load(env(map[string]string{
		"PRIVO_UPSTREAM_CALL_TIMEOUT":       "30s",
		"PRIVO_UPSTREAM_REFRESH_GAP":        "250ms",
		"PRIVO_UPSTREAM_MAX_RESPONSE_BYTES": "65536",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CallTimeout != 30*time.Second || cfg.RefreshGap != 250*time.Millisecond || cfg.MaxResponseBytes != 65536 {
		t.Errorf("values: call timeout %s, refresh gap %s, max response %d", cfg.CallTimeout, cfg.RefreshGap, cfg.MaxResponseBytes)
	}

	for _, tc := range []struct{ name, value string }{
		{"PRIVO_UPSTREAM_CALL_TIMEOUT", "0s"},
		{"PRIVO_UPSTREAM_CALL_TIMEOUT", "soon"},
		{"PRIVO_UPSTREAM_REFRESH_GAP", "-1s"},
		{"PRIVO_UPSTREAM_MAX_RESPONSE_BYTES", "100"},        // below 1024: nothing would fit
		{"PRIVO_UPSTREAM_MAX_RESPONSE_BYTES", "16MiB"},      // an integer number of bytes
		{"PRIVO_UPSTREAM_MAX_RESPONSE_BYTES", "-5"},         //
		{"PRIVO_UPSTREAM_MAX_RESPONSE_BYTES", "2147483648"}, // above 1 GiB
	} {
		_, err := Load(env(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want one that names the variable", tc.name, tc.value, err)
		}
	}
}

func TestLimitSettingsDefaultsAndBounds(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.RateLimitTokenRPM != 120 || cfg.RateLimitUserRPM != 300 || cfg.RateLimitOrgRPM != 1200 ||
		cfg.MaxInFlight != 256 || cfg.OrgMaxInFlight != 128 || cfg.UpstreamMaxInFlight != 32 ||
		cfg.CircuitBreakerThreshold != 5 || cfg.CircuitBreakerCooldown != 30*time.Second {
		t.Fatalf("unexpected limit defaults: %+v", cfg)
	}

	// Zero disables each limiter/breaker; cooldown < 1s is allowed when threshold == 0.
	cfg, err = Load(env(map[string]string{
		"PRIVO_RATE_LIMIT_TOKEN_RPM":      "0",
		"PRIVO_RATE_LIMIT_USER_RPM":       "0",
		"PRIVO_RATE_LIMIT_ORG_RPM":        "0",
		"PRIVO_MAX_INFLIGHT":              "0",
		"PRIVO_ORG_MAX_INFLIGHT":          "0",
		"PRIVO_UPSTREAM_MAX_INFLIGHT":     "0",
		"PRIVO_CIRCUIT_BREAKER_THRESHOLD": "0",
		"PRIVO_CIRCUIT_BREAKER_COOLDOWN":  "0s",
	}))
	if err != nil {
		t.Fatalf("Load disabled limits: %v", err)
	}
	if cfg.RateLimitTokenRPM != 0 || cfg.RateLimitUserRPM != 0 || cfg.RateLimitOrgRPM != 0 ||
		cfg.MaxInFlight != 0 || cfg.OrgMaxInFlight != 0 || cfg.UpstreamMaxInFlight != 0 ||
		cfg.CircuitBreakerThreshold != 0 || cfg.CircuitBreakerCooldown != 0 {
		t.Fatalf("unexpected disabled limits: %+v", cfg)
	}

	// Custom valid positive values.
	cfg, err = Load(env(map[string]string{
		"PRIVO_RATE_LIMIT_TOKEN_RPM":      "10",
		"PRIVO_RATE_LIMIT_USER_RPM":       "20",
		"PRIVO_RATE_LIMIT_ORG_RPM":        "50",
		"PRIVO_MAX_INFLIGHT":              "16",
		"PRIVO_ORG_MAX_INFLIGHT":          "8",
		"PRIVO_UPSTREAM_MAX_INFLIGHT":     "4",
		"PRIVO_CIRCUIT_BREAKER_THRESHOLD": "3",
		"PRIVO_CIRCUIT_BREAKER_COOLDOWN":  "1s",
	}))
	if err != nil {
		t.Fatalf("Load custom limits: %v", err)
	}
	if cfg.RateLimitTokenRPM != 10 || cfg.RateLimitUserRPM != 20 || cfg.RateLimitOrgRPM != 50 ||
		cfg.MaxInFlight != 16 || cfg.OrgMaxInFlight != 8 || cfg.UpstreamMaxInFlight != 4 ||
		cfg.CircuitBreakerThreshold != 3 || cfg.CircuitBreakerCooldown != time.Second {
		t.Fatalf("unexpected custom limits: %+v", cfg)
	}

	for _, tc := range []map[string]string{
		{"PRIVO_RATE_LIMIT_TOKEN_RPM": "-1"},
		{"PRIVO_RATE_LIMIT_TOKEN_RPM": "fast"},
		{"PRIVO_RATE_LIMIT_USER_RPM": "-1"},
		{"PRIVO_RATE_LIMIT_ORG_RPM": "-1"},
		{"PRIVO_MAX_INFLIGHT": "-1"},
		{"PRIVO_ORG_MAX_INFLIGHT": "-1"},
		{"PRIVO_UPSTREAM_MAX_INFLIGHT": "-1"},
		{"PRIVO_CIRCUIT_BREAKER_THRESHOLD": "-1"},
		{"PRIVO_CIRCUIT_BREAKER_COOLDOWN": "500ms"}, // threshold defaults to 5 > 0
		{"PRIVO_CIRCUIT_BREAKER_COOLDOWN": "0s"},
		{"PRIVO_CIRCUIT_BREAKER_COOLDOWN": "-1s"},
		{"PRIVO_CIRCUIT_BREAKER_COOLDOWN": "soon"},
		{"PRIVO_CIRCUIT_BREAKER_THRESHOLD": "0", "PRIVO_CIRCUIT_BREAKER_COOLDOWN": "-1s"},
	} {
		var name string
		for k := range tc {
			if k == "PRIVO_CIRCUIT_BREAKER_COOLDOWN" || len(tc) == 1 {
				name = k
			}
		}
		_, err := Load(env(tc))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%v: error %v, want one naming %s", tc, err, name)
		}
	}
}
