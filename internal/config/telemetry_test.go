package config

import (
	"strings"
	"testing"
)

// ADR 0024: statistics are kept 30 days unless set; metrics are off unless an
// address is given, and never on the public port.
func TestTelemetrySettings(t *testing.T) {
	cfg, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TelemetryRetentionDays != 30 || cfg.MetricsAddr != "" {
		t.Fatalf("defaults: retention %d, metrics %q", cfg.TelemetryRetentionDays, cfg.MetricsAddr)
	}
	cfg, err = Load(env(map[string]string{"PRIVO_TELEMETRY_RETENTION_DAYS": "0", "PRIVO_METRICS_ADDR": "127.0.0.1:9464"}))
	if err != nil || cfg.TelemetryRetentionDays != 0 || cfg.MetricsAddr != "127.0.0.1:9464" {
		t.Fatalf("off and an address: %+v, %v", cfg, err)
	}
	for _, tc := range []struct{ name, value string }{
		{"PRIVO_TELEMETRY_RETENTION_DAYS", "-1"},
		{"PRIVO_TELEMETRY_RETENTION_DAYS", "401"},
		{"PRIVO_TELEMETRY_RETENTION_DAYS", "a month"},
		{"PRIVO_METRICS_ADDR", "9464"},
		{"PRIVO_METRICS_ADDR", "localhost:"},
		{"PRIVO_METRICS_ADDR", ":8080"}, // the public listener
	} {
		_, err := Load(env(map[string]string{tc.name: tc.value}))
		if err == nil || !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s=%q: error %v, want one that names the variable", tc.name, tc.value, err)
		}
	}
}
