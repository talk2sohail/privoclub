package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Kinds of OpenID Connect provider (ADR 0034). Google and Microsoft differ in
// which claim limits who may sign in (point 6); every other provider is
// generic.
const (
	OIDCGoogle    = "google"
	OIDCMicrosoft = "microsoft"
	OIDCGeneric   = "generic"
)

// GoogleIssuer is Google's issuer, exactly as its tokens carry it.
const GoogleIssuer = "https://accounts.google.com"

const (
	maxOIDCDomains = 50
	maxOIDCName    = 40
)

var (
	oidcClientIDRe = regexp.MustCompile(`^[\x21-\x7e]{1,200}$`)
	domainRe       = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	// Microsoft's multi-tenant issuers and its template: the issuer must name
	// one tenant (ADR 0034 point 4).
	microsoftShared = []string{"common", "organizations", "consumers", "{tenantid}"}
)

// OIDC is one OpenID Connect sign-in provider (ADR 0034). It is nil in Web
// when none of its variables is set.
type OIDC struct {
	// Issuer is the issuer URL exactly as configured: discovery must report
	// it unchanged, and identities are keyed by it (PRIVO_OIDC_ISSUER).
	Issuer string
	// ClientID and ClientSecret are the client registered at the provider
	// (PRIVO_OIDC_CLIENT_ID, PRIVO_OIDC_CLIENT_SECRET or its _FILE).
	ClientID     string
	ClientSecret Secret
	// Name is the sign-in button's label (PRIVO_OIDC_NAME).
	Name string
	// AllowedDomains, when set, limit who may sign in (point 6):
	// PRIVO_OIDC_ALLOWED_DOMAINS, lower case.
	AllowedDomains []string
	// Kind is OIDCGoogle, OIDCMicrosoft or OIDCGeneric, from the issuer.
	Kind string
}

// missing lists the provider's required variables that are not set.
func (o *OIDC) missing() []string {
	var out []string
	if o.Issuer == "" {
		out = append(out, "PRIVO_OIDC_ISSUER")
	}
	if o.ClientID == "" {
		out = append(out, "PRIVO_OIDC_CLIENT_ID")
	}
	if o.ClientSecret == "" {
		out = append(out, "PRIVO_OIDC_CLIENT_SECRET")
	}
	return out
}

// loadOIDC reads the OIDC provider. It returns nil when none of its
// variables is set.
func loadOIDC(getenv func(string) string) (*OIDC, []error) {
	names := []string{"PRIVO_OIDC_ISSUER", "PRIVO_OIDC_CLIENT_ID", "PRIVO_OIDC_CLIENT_SECRET", "PRIVO_OIDC_CLIENT_SECRET_FILE", "PRIVO_OIDC_NAME", "PRIVO_OIDC_ALLOWED_DOMAINS"}
	if !slices.ContainsFunc(names, func(n string) bool { return strings.TrimSpace(getenv(n)) != "" }) {
		return nil, nil
	}
	var errs []error
	o := &OIDC{ClientID: strings.TrimSpace(getenv("PRIVO_OIDC_CLIENT_ID"))}

	if raw := strings.TrimSpace(getenv("PRIVO_OIDC_ISSUER")); raw != "" {
		kind, err := checkIssuer(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("PRIVO_OIDC_ISSUER: %w", err))
		} else {
			o.Issuer, o.Kind = raw, kind
		}
	}
	if o.ClientID != "" && !oidcClientIDRe.MatchString(o.ClientID) {
		errs = append(errs, errors.New("PRIVO_OIDC_CLIENT_ID: must be 1 to 200 printable characters without spaces (copy it from the provider)"))
		o.ClientID = ""
	}
	secret, err := loadSecretPair(getenv, "PRIVO_OIDC_CLIENT_SECRET", "the provider")
	if err != nil {
		errs = append(errs, err)
	}
	o.ClientSecret = secret

	o.Name = strings.TrimSpace(getenv("PRIVO_OIDC_NAME"))
	if len(o.Name) > maxOIDCName || strings.ContainsFunc(o.Name, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		errs = append(errs, fmt.Errorf("PRIVO_OIDC_NAME: at most %d characters, no control characters", maxOIDCName))
		o.Name = ""
	}
	if o.Name == "" {
		switch o.Kind {
		case OIDCGoogle:
			o.Name = "Google"
		case OIDCMicrosoft:
			o.Name = "Microsoft"
		default:
			o.Name = "single sign-on"
		}
	}

	if raw := strings.TrimSpace(getenv("PRIVO_OIDC_ALLOWED_DOMAINS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			d := strings.ToLower(strings.TrimSpace(part))
			if !domainRe.MatchString(d) {
				errs = append(errs, fmt.Errorf("PRIVO_OIDC_ALLOWED_DOMAINS: %q is not a domain name", clipName(d)))
				continue
			}
			if !slices.Contains(o.AllowedDomains, d) {
				o.AllowedDomains = append(o.AllowedDomains, d)
			}
		}
		if len(o.AllowedDomains) > maxOIDCDomains {
			errs = append(errs, fmt.Errorf("PRIVO_OIDC_ALLOWED_DOMAINS: at most %d domains", maxOIDCDomains))
		}
		// Microsoft's email claim is not verified (nOAuth): its tenant, in the
		// issuer, is the limit (ADR 0034 point 6).
		if o.Kind == OIDCMicrosoft {
			errs = append(errs, errors.New("PRIVO_OIDC_ALLOWED_DOMAINS: not used with Microsoft, whose email claim is not verified; the tenant in PRIVO_OIDC_ISSUER is the limit"))
		}
	}
	return o, errs
}

// checkIssuer checks an issuer URL and says which kind of provider it is.
func checkIssuer(raw string) (string, error) {
	u, err := parsePublicURL(raw)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(raw, " \t") {
		return "", errors.New("must not contain spaces")
	}
	if raw == GoogleIssuer {
		return OIDCGoogle, nil
	}
	if host := strings.ToLower(u.Hostname()); host == "accounts.google.com" {
		return "", fmt.Errorf("the Google issuer is exactly %s", GoogleIssuer)
	} else if microsoftHost(host) {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 || parts[1] != "v2.0" || parts[0] == "" {
			return "", errors.New("a Microsoft issuer is https://login.microsoftonline.com/<tenant id>/v2.0")
		}
		tenant, err := url.PathUnescape(parts[0])
		if err != nil || slices.Contains(microsoftShared, strings.ToLower(tenant)) || strings.EqualFold(tenant, microsoftConsumersTenant) {
			return "", errors.New("a Microsoft issuer must name one tenant: multi-tenant endpoints (common, organizations, consumers) are not accepted (ADR 0034)")
		}
		return OIDCMicrosoft, nil
	}
	return OIDCGeneric, nil
}

// microsoftConsumersTenant is the tenant of personal Microsoft accounts:
// "consumers" by another name (review I-1).
const microsoftConsumersTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

// microsoftHost reports a Microsoft identity host, so its issuer gets the
// Microsoft rules: one tenant, the email never trusted (review I-1).
func microsoftHost(host string) bool {
	switch host {
	case "login.microsoftonline.com", "login.microsoft.com", "login.windows.net", "sts.windows.net",
		"login.microsoftonline.us", "login.partner.microsoftonline.cn", "login.chinacloudapi.cn":
		return true
	}
	for _, suffix := range []string{".microsoftonline.com", ".ciamlogin.com", ".b2clogin.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func clipName(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
