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
