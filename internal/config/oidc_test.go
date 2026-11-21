package config

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// ADR 0034: one OIDC provider beside GitHub, checked at startup.
func TestOIDCSettings(t *testing.T) {
	pub, _ := url.Parse("https://privo.example")
	w, err := LoadWeb(envOf(map[string]string{}))
	if err != nil || w.OIDC != nil {
		t.Fatalf("nothing set: %v %+v", err, w.OIDC)
	}

	google := map[string]string{"PRIVO_OIDC_ISSUER": "https://accounts.google.com", "PRIVO_OIDC_CLIENT_ID": "1234-abc.apps.googleusercontent.com",
		"PRIVO_OIDC_CLIENT_SECRET": "s3cret", "PRIVO_OIDC_ALLOWED_DOMAINS": "Example.com, example.org"}
	w, err = LoadWeb(envOf(google))
	if err != nil {
		t.Fatal(err)
	}
	if w.OIDC.Kind != OIDCGoogle || w.OIDC.Name != "Google" || !slices.Equal(w.OIDC.AllowedDomains, []string{"example.com", "example.org"}) {
		t.Fatalf("google: %+v", w.OIDC)
	}
	// Only OIDC set: sign-in runs without GitHub, and GitHub is off.
	if m := w.Missing(pub); len(m) != 0 || w.GitHubOn() {
		t.Fatalf("only OIDC: missing %v, github %v", m, w.GitHubOn())
	}

	ms := map[string]string{"PRIVO_OIDC_ISSUER": "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0",
		"PRIVO_OIDC_CLIENT_ID": "6731de76-14a6-49ae-97bc-6eba6914391e", "PRIVO_OIDC_CLIENT_SECRET": "s3cret"}
	if w, err = LoadWeb(envOf(ms)); err != nil || w.OIDC.Kind != OIDCMicrosoft || w.OIDC.Name != "Microsoft" {
		t.Fatalf("microsoft: %v %+v", err, w.OIDC)
	}
	keycloak := map[string]string{"PRIVO_OIDC_ISSUER": "https://sso.example.com/realms/acme", "PRIVO_OIDC_CLIENT_ID": "privo",
		"PRIVO_OIDC_CLIENT_SECRET": "s3cret", "PRIVO_OIDC_NAME": "Acme SSO"}
	if w, err = LoadWeb(envOf(keycloak)); err != nil || w.OIDC.Kind != OIDCGeneric || w.OIDC.Name != "Acme SSO" {
		t.Fatalf("generic: %v %+v", err, w.OIDC)
	}

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"multi-tenant common":   {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoftonline.com/common/v2.0"), "one tenant"},
		"multi-tenant orgs":     {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoftonline.com/organizations/v2.0"), "one tenant"},
		"tenant template":       {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoftonline.com/{tenantid}/v2.0"), "one tenant"},
		"microsoft v1 issuer":   {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47"), "v2.0"},
		"microsoft and domains": {with(ms, "PRIVO_OIDC_ALLOWED_DOMAINS", "example.com"), "not verified"},
		"personal accounts":     {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoftonline.com/9188040d-6c67-4c5b-b112-36a304b66dad/v2.0"), "one tenant"},
		"other Microsoft host":  {with(ms, "PRIVO_OIDC_ISSUER", "https://login.microsoft.com/common/v2.0"), "one tenant"},
		"b2c with domains":      {with(with(ms, "PRIVO_OIDC_ISSUER", "https://acme.b2clogin.com/72f988bf-86f1-41af-91ab-2d7cd011db47/v2.0/"), "PRIVO_OIDC_ALLOWED_DOMAINS", "example.com"), "not verified"},
		"google without scheme": {with(google, "PRIVO_OIDC_ISSUER", "https://accounts.google.com/"), "exactly"},
		"plain http":            {with(keycloak, "PRIVO_OIDC_ISSUER", "http://sso.example.com/realms/acme"), "https"},
		"query":                 {with(keycloak, "PRIVO_OIDC_ISSUER", "https://sso.example.com/?x=1"), "query"},
		"bad domain":            {with(google, "PRIVO_OIDC_ALLOWED_DOMAINS", "example.com,not a domain"), "not a domain"},
		"client id with spaces": {with(keycloak, "PRIVO_OIDC_CLIENT_ID", "a b"), "PRIVO_OIDC_CLIENT_ID"},
		"long name":             {with(keycloak, "PRIVO_OIDC_NAME", strings.Repeat("n", 41)), "PRIVO_OIDC_NAME"},
	} {
		if _, err := LoadWeb(envOf(tc.env)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the error repeats the secret", name)
		}
	}

	// A half-set provider fails closed: it is listed as missing even when
	// GitHub is complete.
	half := map[string]string{"PRIVO_GITHUB_CLIENT_ID": "Iv1.abc", "PRIVO_GITHUB_CLIENT_SECRET": "gh", "PRIVO_OIDC_ISSUER": "https://accounts.google.com"}
	w, err = LoadWeb(envOf(half))
	if err != nil {
		t.Fatal(err)
	}
	if m := w.Missing(pub); !slices.Equal(m, []string{"PRIVO_OIDC_CLIENT_ID", "PRIVO_OIDC_CLIENT_SECRET"}) {
		t.Fatalf("half-set OIDC: %v", m)
	}
	// Neither provider: GitHub's names, as before.
	w, _ = LoadWeb(envOf(map[string]string{}))
	if m := w.Missing(pub); !slices.Equal(m, []string{"PRIVO_GITHUB_CLIENT_ID", "PRIVO_GITHUB_CLIENT_SECRET"}) {
		t.Fatalf("nothing: %v", m)
	}
}

func with(m map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}
