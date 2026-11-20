package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/web"
)

// Servers where each person uses their own account, invites, and admins
// disconnecting a person (ADR 0017).

// InviteTTL is how long an invite link works.
const InviteTTL = 72 * time.Hour

type oauthReq struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"` // write-only
	Scopes       []string `json:"scopes"`
	Issuer       string   `json:"issuer"`
	// Entered by hand, for a server that does not say where people sign in
	// (ADR 0023): its authorization server's endpoints, when that server
	// publishes no metadata either. They need a client id.
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RevocationEndpoint    string `json:"revocation_endpoint"`
	// Resource is sent as the resource parameter when the server does not
	// name one; empty sends none.
	Resource string `json:"resource"`
}

// personalKeyReq is how each person's own key is sent to a server (ADR 0023).
type personalKeyReq struct {
	Header  string  `json:"header"`   // default Authorization
	Prefix  *string `json:"prefix"`   // default "Bearer " for Authorization, "" otherwise
	Help    string  `json:"help"`     // where a person gets their key, shown on the connect page
	HelpURL string  `json:"help_url"` // a link there
}

// Limits on what an admin writes for a personal-key server.
const (
	maxKeyPrefix = 32
	maxKeyHelp   = 500
)

func (k *personalKeyReq) check(static map[string]string) (store.ServerKey, error) {
	out := store.ServerKey{Header: "Authorization", Prefix: "Bearer "}
	if k.Header != "" {
		if !validHeaderName(k.Header) || keyReservedHeader(http.CanonicalHeaderKey(k.Header)) {
			return out, errorf(http.StatusUnprocessableEntity, "personal_key.header %q is invalid or reserved", k.Header)
		}
		out.Header = http.CanonicalHeaderKey(k.Header)
		if out.Header != "Authorization" {
			out.Prefix = ""
		}
	}
	if k.Prefix != nil {
		p := *k.Prefix
		if len(p) > maxKeyPrefix || strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r > 0x7e }) {
			return out, errorf(http.StatusUnprocessableEntity, "personal_key.prefix is at most %d printable characters", maxKeyPrefix)
		}
		out.Prefix = p
	}
	if len(static) > 0 {
		// Per-person connections carry the person's credential and nothing
		// shared, as for oauth servers.
		return out, errorf(http.StatusUnprocessableEntity, "a server that takes each person's own key takes no shared headers")
	}
	help := strings.TrimSpace(k.Help)
	if len(help) > maxKeyHelp || strings.ContainsFunc(help, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return out, errorf(http.StatusUnprocessableEntity, "personal_key.help is at most %d characters without control characters", maxKeyHelp)
	}
	out.Help = help
	if k.HelpURL != "" {
		u, err := url.Parse(k.HelpURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(k.HelpURL) > 2048 {
			return out, errorf(http.StatusUnprocessableEntity, "personal_key.help_url must be an https address")
		}
		out.HelpURL = u.String()
	}
	return out, nil
}

// keyReservedHeader names the headers a person's own key may not go in:
// cookies, and headers a server or a proxy in front of it may read as a
// statement of who the caller is or how the body is encoded, so the value a
// member types would become a claim about their identity (review of
// 2026-11-15, U-05).
func keyReservedHeader(canon string) bool {
	switch canon {
	case "Cookie", "Forwarded", "X-Real-Ip", "Remote-User", "X-Remote-User", "X-User", "X-Auth-User", "X-Authenticated-User",
		"X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Preferred-Username", "X-Webauth-User",
		"Accept-Encoding", "Content-Encoding", "Origin", "Referer", "User-Agent":
		return true
	}
	return strings.HasPrefix(canon, "X-Forwarded-") || strings.HasPrefix(canon, "X-Auth-Request-") || strings.HasPrefix(canon, "Cf-")
}

func printableASCII(s string, limit int) bool {
	if len(s) > limit {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// configureOAuth runs discovery and registration for a new oauth server and
// returns its settings and the client secret to seal (empty when none).
func (a *API) configureOAuth(ctx context.Context, srv store.Server, req *oauthReq) (store.ServerOAuth, string, error) {
	if a.OAuth == nil || a.Grants == nil || a.PublicURL == nil {
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity,
			"servers that use each person's own account need PRIVO_PUBLIC_URL and the admin site (GitHub sign-in) to be configured")
	}
	if !printableASCII(req.ClientID, 255) || !printableASCII(req.ClientSecret, 512) || (req.ClientSecret != "" && req.ClientID == "") {
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "oauth.client_id and oauth.client_secret must be printable without spaces, and a secret needs a client id")
	}
	scopes := upstreamauth.CleanScopes(req.Scopes)
	if len(scopes) != len(req.Scopes) {
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "oauth.scopes holds an invalid or repeated scope")
	}
	manual := req.AuthorizationEndpoint != "" || req.TokenEndpoint != ""
	d, err := a.OAuth.Discover(ctx, srv.Config.URL, srv.Transport, req.Issuer)
	if manual {
		// Endpoints by hand only for a server that does not say where its
		// people sign in: one that does is checked against what it names,
		// and hand-entered settings would skip that check (review of
		// 2026-11-15, U-08).
		if err == nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity,
				"the server says where its people sign in; leave oauth.authorization_endpoint and oauth.token_endpoint out")
		}
		return a.manualOAuth(ctx, req, scopes)
	}
	switch {
	case errors.Is(err, upstreamauth.ErrNoAuthNeeded):
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "the server answered without asking for authorization; add it without oauth")
	case errors.Is(err, upstreamauth.ErrNoResourceMetadata) && req.Issuer != "":
		// The server does not say where its people sign in; the admin did.
		sm, merr := a.OAuth.ServerMetadataFor(ctx, req.Issuer)
		if merr != nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "the server names no authorization server, and %s could not be used: %v%s", req.Issuer, merr, slowHint(merr))
		}
		d = upstreamauth.Discovery{Server: sm}
	case errors.Is(err, upstreamauth.ErrNoResourceMetadata):
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity,
			"the server does not say where its people sign in (no protected resource metadata); give oauth.issuer, or oauth.authorization_endpoint and oauth.token_endpoint with oauth.client_id")
	case err != nil:
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "finding the server's sign-in failed: %v%s", err, slowHint(err))
	}
	if req.Resource != "" {
		if d.Resource != "" {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "the server names its own resource (%s); leave oauth.resource out", d.Resource)
		}
		if err := upstreamauth.CheckEndpoint(req.Resource); err != nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "oauth.resource: %v", err)
		}
		d.Resource = req.Resource
	}
	callback := upstreamauth.CallbackURL(a.PublicURL)
	method, err := upstreamauth.ChooseRegistration(req.ClientID, d.Server, callback)
	if err != nil {
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	if len(scopes) == 0 {
		scopes = d.Scopes
	}
	o := store.ServerOAuth{Issuer: d.Server.Issuer, AuthorizationEndpoint: d.Server.AuthorizationEndpoint, TokenEndpoint: d.Server.TokenEndpoint,
		RevocationEndpoint: d.Server.RevocationEndpoint, RegistrationEndpoint: d.Server.RegistrationEndpoint, IssParameter: d.Server.IssParameterSupported,
		Resource: d.Resource, Scopes: scopes, Registration: method, TokenAuthMethod: "none", DiscoveredAt: time.Now().UTC()}
	secret := ""
	switch method {
	case upstreamauth.RegistrationAdmin:
		o.ClientID, secret = req.ClientID, req.ClientSecret
		o.TokenAuthMethod = upstreamauth.TokenAuthMethod(d.Server, secret != "")
	case upstreamauth.RegistrationDynamic:
		reg, err := a.OAuth.Register(ctx, d.Server.RegistrationEndpoint, a.PublicURL)
		if err != nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "registering Privo with the authorization server failed: %v%s", err, slowHint(err))
		}
		o.ClientID, secret, o.TokenAuthMethod = reg.ClientID, reg.ClientSecret, reg.AuthMethod
	}
	o.HasClientSecret = secret != ""
	return o, secret, nil
}

// manualOAuth takes OAuth settings an administrator entered by hand for a
// server whose authorization server publishes no metadata (ADR 0023). Nothing
// is discovered, so there is nothing to register with: the admin gives the
// client id. The endpoints must pass the egress policy now, as every request
// to them will later.
func (a *API) manualOAuth(ctx context.Context, req *oauthReq, scopes []string) (store.ServerOAuth, string, error) {
	if req.AuthorizationEndpoint == "" || req.TokenEndpoint == "" || req.ClientID == "" {
		return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity,
			"OAuth settings entered by hand need oauth.authorization_endpoint, oauth.token_endpoint and oauth.client_id")
	}
	for name, raw := range map[string]string{"authorization_endpoint": req.AuthorizationEndpoint, "token_endpoint": req.TokenEndpoint,
		"revocation_endpoint": req.RevocationEndpoint, "issuer": req.Issuer, "resource": req.Resource} {
		if raw == "" {
			continue
		}
		if err := upstreamauth.CheckEndpoint(raw); err != nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "oauth.%s: %v", name, err)
		}
		if name == "resource" {
			continue // sent as a parameter, never requested
		}
		if _, err := a.Egress.CheckURL(ctx, raw, nil); err != nil {
			return store.ServerOAuth{}, "", errorf(http.StatusUnprocessableEntity, "egress policy refused oauth.%s: %v", name, err)
		}
	}
	method := "none"
	if req.ClientSecret != "" {
		method = "client_secret_basic"
	}
	o := store.ServerOAuth{Issuer: req.Issuer, AuthorizationEndpoint: req.AuthorizationEndpoint, TokenEndpoint: req.TokenEndpoint,
		RevocationEndpoint: req.RevocationEndpoint, Resource: req.Resource, Scopes: scopes, ClientID: req.ClientID,
		Registration: store.RegistrationManual, TokenAuthMethod: method, HasClientSecret: req.ClientSecret != "", DiscoveredAt: time.Now().UTC()}
	return o, req.ClientSecret, nil
}

// connectURL is where people connect their account to an oauth server.
func (a *API) connectURL(alias string) string {
	if a.PublicURL == nil {
		return ""
	}
	return a.PublicURL.JoinPath("connect", alias).String()
}

// ---- invites

func validInvitePerson(p string) bool {
	if p == "" || len(p) > 128 {
		return false
	}
	return !strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// CreateInvite stores an invite and returns the link to give the person.
// roles must be a valid set without owner (ADR 0025); nil means member.
func CreateInvite(ctx context.Context, st *store.Store, org store.OrgID, publicURL *url.URL, person string, roles []string, createdBy string, ev store.AuditEvent) (string, time.Time, error) {
	if publicURL == nil {
		return "", time.Time{}, errorf(http.StatusUnprocessableEntity, "invites need PRIVO_PUBLIC_URL")
	}
	person = strings.TrimSpace(person)
	if !validInvitePerson(person) {
		return "", time.Time{}, errorf(http.StatusUnprocessableEntity, "person must be 1 to 128 characters without control characters")
	}
	roles, err := requestRoles(roles, "", store.RoleMember)
	if err != nil {
		return "", time.Time{}, err
	}
	if slices.Contains(roles, store.RoleOwner) {
		return "", time.Time{}, errorf(http.StatusUnprocessableEntity, "invites never make owners: admin alone, or any of member, approver and auditor")
	}
	plain, hash, err := web.NewInviteToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expires := now.Add(InviteTTL)
	ev.Action = "invite.create:" + strings.Join(roles, "+") + ":" + person
	err = st.CreateInvite(ctx, org, store.NewInvite{ID: ids.New(), SecretHash: hash, Person: person, Roles: roles, CreatedBy: createdBy, Now: now, ExpiresAt: expires}, ev)
	if errors.Is(err, store.ErrConflict) {
		return "", time.Time{}, errorf(http.StatusConflict, "%q is already a member", person)
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return publicURL.JoinPath("admin", "invite", plain).String(), expires, nil
}

func (a *API) createInvite(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Person string   `json:"person"`
		Roles  []string `json:"roles"`
		Role   string   `json:"role"` // before ADR 0025; still read for one release
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	roles, err := requestRoles(req.Roles, req.Role, store.RoleMember)
	if err != nil {
		return err
	}
	if elevated(roles) && !p.Has(store.RoleOwner) {
		return errorf(http.StatusForbidden, "only owners can invite admins")
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(), Decision: "allow", Outcome: "ok"}
	link, expires, err := CreateInvite(r.Context(), a.Store, p.OrgID, a.PublicURL, req.Person, roles, p.Name, ev)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": map[string]any{"person": strings.TrimSpace(req.Person), "roles": roles, "role": primaryRole(roles),
		"url": link, "expires_at": expires}})
	return nil
}

// ---- connections

type connectionView struct {
	Person      string    `json:"person"`
	Scopes      string    `json:"scopes"`
	Catalog     bool      `json:"catalog"`
	ConnectedAt time.Time `json:"connected_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	RefreshedAt time.Time `json:"refreshed_at"`
}

func (a *API) listConnections(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	list, err := a.Store.ListServerGrants(r.Context(), p.OrgID, s.ID)
	if err != nil {
		return err
	}
	out := make([]connectionView, 0, len(list))
	for _, g := range list {
		out = append(out, connectionView{Person: g.Person, Scopes: g.Scopes, Catalog: g.Catalog, ConnectedAt: g.ConnectedAt, LastUsedAt: g.LastUsedAt, RefreshedAt: g.RefreshedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
	return nil
}

func (a *API) deleteConnection(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	if a.Grants == nil {
		return errorf(http.StatusNotFound, "no connection for %q on %q", r.PathValue("person"), s.Alias)
	}
	u, err := a.Store.UserByPerson(r.Context(), p.OrgID, r.PathValue("person"))
	if errors.Is(err, store.ErrNotFound) {
		return errorf(http.StatusNotFound, "no connection for %q on %q", r.PathValue("person"), s.Alias)
	}
	if err != nil {
		return err
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(), Server: s.Alias,
		Action: "upstream.disconnect:" + s.Alias + ":" + u.Person, Decision: "allow", Outcome: "ok"}
	switch err := a.Grants.Disconnect(r.Context(), p.OrgID, s, u.ID, ev); {
	case errors.Is(err, store.ErrNotFound):
		return errorf(http.StatusNotFound, "no connection for %q on %q", u.Person, s.Alias)
	case err != nil:
		return err
	}
	a.Upstream.ClosePerson(s.ID, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"disconnected": u.Person, "server": s.Alias})
	return nil
}

// slowHint says what to do about a provider that did not answer in time
// (F26: "context deadline exceeded" told an admin nothing).
func slowHint(err error) string {
	var te *upstreamauth.TimeoutError
	if errors.As(err, &te) {
		return ". The provider may be down or slow; try again in a few minutes"
	}
	return ""
}
