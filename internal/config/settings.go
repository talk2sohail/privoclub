package config

import "strings"

// Setting describes one variable the process reads. The list below is the
// single registry of them (ADR 0015): tests fail when the code reads a PRIVO_
// name that is not here, when a name here is read by nothing, and when one is
// missing from the configuration reference (docs/site/installation/
// configuration.md, which names no other) or from docs/privo.example.yaml.
//
// To add a setting: add it here, read it (in Load or where it belongs), and
// document it in the configuration reference and the example file.
type Setting struct {
	// Name is the environment variable, such as PRIVO_BACKUP_INTERVAL.
	Name string
	// Secret marks a value that must never sit in the configuration file or be
	// shown. Every secret has a sibling Name+"_FILE" that takes a path instead.
	Secret bool
	// NoFile marks a setting the file cannot hold, because it is how the file is found.
	NoFile bool
}

// Key is the name of the setting in the configuration file: the variable name
// in lower case without the PRIVO_ prefix.
func (s Setting) Key() string {
	return strings.ToLower(strings.TrimPrefix(s.Name, "PRIVO_"))
}

// Settings is the registry, in the order `privo config show` prints it.
var Settings = []Setting{
	{Name: "PRIVO_CONFIG_FILE", NoFile: true},
	{Name: "PRIVO_LISTEN"},
	{Name: "PRIVO_DATA_DIR"},
	{Name: "PRIVO_PUBLIC_URL"},
	{Name: "PRIVO_LOG_LEVEL"},
	{Name: "PRIVO_LOG_FORMAT"},
	{Name: "PRIVO_SHUTDOWN_TIMEOUT"},
	{Name: "PRIVO_MASTER_KEY", Secret: true},
	{Name: "PRIVO_MASTER_KEY_FILE"},
	{Name: "PRIVO_MASTER_KEY_PREVIOUS", Secret: true},
	{Name: "PRIVO_MASTER_KEY_PREVIOUS_FILE"},
	{Name: "PRIVO_BOOTSTRAP_TOKEN", Secret: true},
	{Name: "PRIVO_BOOTSTRAP_TOKEN_FILE"},
	{Name: "PRIVO_TOKEN_TTL"},
	{Name: "PRIVO_APPROVAL_HOLD"},
	{Name: "PRIVO_RUNNER"},
	{Name: "PRIVO_INSECURE_LOCAL_RUNNER"},
	{Name: "PRIVO_EGRESS_ALLOW_CIDRS"},
	{Name: "PRIVO_TRUSTED_PROXIES"},
	{Name: "PRIVO_UPSTREAM_CALL_TIMEOUT"},
	{Name: "PRIVO_UPSTREAM_MAX_RESPONSE_BYTES"},
	{Name: "PRIVO_UPSTREAM_REFRESH_GAP"},
	{Name: "PRIVO_UPSTREAM_RETRY_BASE"},
	{Name: "PRIVO_UPSTREAM_RETRY_MAX"},
	{Name: "PRIVO_RATE_LIMIT_TOKEN_RPM"},
	{Name: "PRIVO_RATE_LIMIT_USER_RPM"},
	{Name: "PRIVO_RATE_LIMIT_ORG_RPM"},
	{Name: "PRIVO_MAX_INFLIGHT"},
	{Name: "PRIVO_ORG_MAX_INFLIGHT"},
	{Name: "PRIVO_UPSTREAM_MAX_INFLIGHT"},
	{Name: "PRIVO_CIRCUIT_BREAKER_THRESHOLD"},
	{Name: "PRIVO_CIRCUIT_BREAKER_COOLDOWN"},
	{Name: "PRIVO_BACKUP_INTERVAL"},
	{Name: "PRIVO_BACKUP_KEEP"},
	{Name: "PRIVO_BACKUP_DIR"},
	{Name: "PRIVO_TELEMETRY_RETENTION_DAYS"},
	{Name: "PRIVO_METRICS_ADDR"},
	{Name: "PRIVO_GITHUB_CLIENT_ID"},
	{Name: "PRIVO_GITHUB_CLIENT_SECRET", Secret: true},
	{Name: "PRIVO_GITHUB_CLIENT_SECRET_FILE"},
	{Name: "PRIVO_GITHUB_BASE_URL"},
	{Name: "PRIVO_GITHUB_API_URL"},
	{Name: "PRIVO_GITHUB_ORG_IDS"},
	{Name: "PRIVO_SESSION_IDLE_TIMEOUT"},
	{Name: "PRIVO_SESSION_MAX_AGE"},
	{Name: "PRIVO_OAUTH_SERVER"},
	{Name: "PRIVO_OAUTH_CLIENTS"},
	{Name: "PRIVO_OAUTH_DYNAMIC_REGISTRATION"},
}

// Lookup returns the registered setting with the given variable name.
func Lookup(name string) (Setting, bool) {
	for _, s := range Settings {
		if s.Name == name {
			return s, true
		}
	}
	return Setting{}, false
}

func byKey(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key() == key {
			return s, true
		}
	}
	return Setting{}, false
}
