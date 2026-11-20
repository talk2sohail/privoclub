package oauthserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	oauth2 "authelia.com/provider/oauth2"
	"authelia.com/provider/oauth2/compose"
	hoauth2 "authelia.com/provider/oauth2/handler/oauth2"

	"privo.club/privo/internal/clientip"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// Scopes. There is one that means something: use the gateway as me.
// offline_access is accepted because some clients add it when it is listed;
// it changes nothing, a refresh token is always issued (ADR 0022 point 4).
const (
	ScopeMCP     = "mcp"
	ScopeOffline = "offline_access"
)

// Lifetimes (ADR 0022 points 8 and 9).
const (
	DefaultCodeTTL    = 60 * time.Second
	DefaultAccessTTL  = time.Hour
	DefaultRefreshTTL = 30 * 24 * time.Hour // idle: each refresh starts it again
	DefaultGrantTTL   = 90 * 24 * time.Hour // absolute, from consent
)

// Dynamic registration (ADR 0022 point 13).
const (
	maxRegistered      = 200             // registered clients in total
	registerPerAddress = 10              // registrations from one address in registerWindow
	registerWindow     = time.Hour       //
	unusedRegistration = time.Hour       // a registration nobody allowed is deleted after this
	idleRegistration   = DefaultGrantTTL // and one nobody has allowed for this long
	maxRequestBody     = 16 << 10
	registeredIDPrefix = "privo_client_"
)

// Names, not secrets: the shape of Privo's tokens and the labels the signing
// secret is sealed under.
const (
	tokenPrefix          = "privo_%s_" //nolint:gosec // a format, not a credential: privo_at_, privo_rt_, privo_ac_
	accessTokenPrefix    = "privo_at_" //nolint:gosec // a prefix, not a credential
	signingKeyLen        = 32
	signingKeyPurpose    = "oauth.signing"
	signingKeyCredential = "oauth-signing-key" //nolint:gosec // the label the secret is sealed under, not the secret
)

// Keys gives the organisation's data key (the keyring).
type Keys interface {
	Current(ctx context.Context, org store.OrgID) (uint32, []byte, error)
	DEK(ctx context.Context, org store.OrgID, version uint32) ([]byte, error)
}

// Auditor writes admin audit events (the audit writer).
type Auditor interface {
	Admin(ctx context.Context, org store.OrgID, ev store.AuditEvent) error
}

// Options configure the server.
type Options struct {
	Org     store.OrgID
	Store   Store
	Keys    Keys
	Audit   Auditor
	Clients *Clients
	// Issuer is the public URL without a trailing slash. The gateway it
	// issues tokens for is Issuer + "/mcp".
	Issuer string
	// Registration turns /oauth/register on.
	Registration bool
	// TrustedProxies are the reverse proxies in front of Privo, so the limit
	// on registrations per address counts the client, not the proxy (review
	// of 2026-11-15, S-01).
	TrustedProxies []netip.Prefix
	Logger         *slog.Logger
	Now            func() time.Time

	CodeTTL, AccessTTL, RefreshTTL, GrantTTL time.Duration
}

// Server is the authorization server.
type Server struct {
	o        Options
	resource string
	provider oauth2.Provider
	strategy *hoauth2.HMACCoreStrategy
	log      *slog.Logger

	regMu  sync.Mutex
	regs   map[string][]time.Time // registrations by address
	regsAt time.Time
}

// NewDocumentClient returns the HTTP client metadata documents are fetched
// with: public addresses only (the egress policy), five seconds, and no
// redirect followed, so a document is what its own address serves.
func NewDocumentClient(p egress.Policy) *http.Client {
	c := egress.NewClient(egress.Options{Policy: p, Timeout: docTimeout, DialTimeout: docTimeout})
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

// New builds the server. It reads the signing secret, and makes and stores
// one the first time (a write at startup, never in a request).
func New(ctx context.Context, o Options) (*Server, error) {
	if o.Store == nil || o.Keys == nil || o.Audit == nil || o.Clients == nil || o.Org == "" {
		return nil, errors.New("oauthserver: store, keys, audit, clients and org are required")
	}
	// Whether registered clients are accepted follows whether registration is
	// on (S-04): one switch, not two.
	o.Clients.Registration = o.Registration
	u, err := url.Parse(o.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.HasSuffix(o.Issuer, "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("oauthserver: the issuer %q is not a base URL", o.Issuer)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	for _, d := range []struct {
		v   *time.Duration
		def time.Duration
	}{{&o.CodeTTL, DefaultCodeTTL}, {&o.AccessTTL, DefaultAccessTTL}, {&o.RefreshTTL, DefaultRefreshTTL}, {&o.GrantTTL, DefaultGrantTTL}} {
		if *d.v <= 0 {
			*d.v = d.def
		}
	}
	s := &Server{o: o, resource: o.Issuer + "/mcp", log: o.Logger, regs: map[string][]time.Time{}}
	secret, err := s.signingKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauthserver: signing key: %w", err)
	}
	cfg := &oauth2.Config{
		GlobalSecret:                            secret,
		EnforcePKCE:                             true,
		EnforcePKCEForPublicClients:             true,
		EnablePKCEPlainChallengeMethod:          false,
		AccessTokenLifespan:                     o.AccessTTL,
		AuthorizeCodeLifespan:                   o.CodeTTL,
		RefreshTokenLifespan:                    o.RefreshTTL,
		AuthorizationServerIdentificationIssuer: o.Issuer,
		ScopeStrategy:                           oauth2.ExactScopeStrategy,
		RefreshTokenScopes:                      []string{}, // a refresh token is always issued
		SendDebugMessagesToClients:              false,
	}
	cfg.ResponseModeParameterHandlers = oauth2.ResponseModeParameterHandlers{&oauth2.RFC9207ResponseModeParameterHandler{Config: cfg}}
	s.strategy = hoauth2.NewHMACCoreStrategy(cfg, tokenPrefix)
	s.provider = compose.Compose(cfg, &storage{s: s}, s.strategy,
		compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory, compose.OAuth2TokenRevocationFactory)
	return s, nil
}

func (s *Server) now() time.Time { return s.o.Now() }

// Issuer is the public URL the server issues tokens under.
func (s *Server) Issuer() string { return s.o.Issuer }

// Registration reports whether clients may register themselves.
func (s *Server) Registration() bool { return s.o.Registration }

// Resource is the gateway the tokens are for.
func (s *Server) Resource() string { return s.resource }

func (s *Server) signingBinding(version uint32) vault.Binding {
	return vault.Binding{OrgID: string(s.o.Org), CredentialID: signingKeyCredential, Purpose: signingKeyPurpose, KeyVersion: version, RowVersion: 1}
}

// signingKey returns the secret the tokens are signed with. The secret is
// random, sealed with the organisation's data key and stored once; a second
// process that starts at the same moment reads the one that was stored first.
func (s *Server) signingKey(ctx context.Context) ([]byte, error) {
	version, sealed, err := s.o.Store.OAuthSigningKey(ctx, s.o.Org)
	if errors.Is(err, store.ErrNotFound) {
		if err := s.makeSigningKey(ctx); err != nil {
			return nil, err
		}
		version, sealed, err = s.o.Store.OAuthSigningKey(ctx, s.o.Org)
	}
	if err != nil {
		return nil, err
	}
	dek, err := s.o.Keys.DEK(ctx, s.o.Org, version)
	if err != nil {
		return nil, err
	}
	secret, err := vault.Open(dek, sealed, s.signingBinding(version))
	if err != nil {
		return nil, err
	}
	if len(secret) != signingKeyLen {
		return nil, errors.New("the stored secret has the wrong length")
	}
	return secret, nil
}

// makeSigningKey stores a new random secret unless the org has one by now.
func (s *Server) makeSigningKey(ctx context.Context) error {
	secret := make([]byte, signingKeyLen)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	version, dek, err := s.o.Keys.Current(ctx, s.o.Org)
	if err != nil {
		return err
	}
	box, err := vault.Seal(dek, secret, s.signingBinding(version))
	if err != nil {
		return err
	}
	return s.o.Store.CreateOAuthSigningKey(ctx, s.o.Org, version, box, s.now())
}

// Metadata is the authorization server metadata (RFC 8414), with what the
// MCP clients look for: S256 only, public clients, client metadata documents.
func (s *Server) Metadata() map[string]any {
	m := map[string]any{
		"issuer":                                         s.o.Issuer,
		"authorization_endpoint":                         s.o.Issuer + "/oauth/authorize",
		"token_endpoint":                                 s.o.Issuer + "/oauth/token",
		"revocation_endpoint":                            s.o.Issuer + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"scopes_supported":                               []string{ScopeMCP, ScopeOffline},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	}
	if s.o.Registration {
		m["registration_endpoint"] = s.o.Issuer + "/oauth/register"
	}
	return m
}

// Routes registers the endpoints that need no session: the metadata, the
// token endpoint, revocation, and registration when it is on. The
// authorization endpoint is a page and belongs to the website.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.metadata)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	if s.o.Registration {
		mux.HandleFunc("POST /oauth/register", s.register)
	}
}

func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = json.NewEncoder(w).Encode(s.Metadata())
}

// ---- the authorization request

// AuthRequest is an authorization request that passed every check: the
// client is accepted, the redirect address is the client's, and the request
// itself is one the library and Privo's own rules allow.
type AuthRequest struct {
	Client      Client
	RedirectURI string
	Query       url.Values // the request as it came, to be sealed and checked again
	ar          oauth2.AuthorizeRequester
	ctx         context.Context
}

// RedirectHost is where the person is sent back to, for the consent page.
func (r *AuthRequest) RedirectHost() string {
	u, err := url.Parse(r.RedirectURI)
	if err != nil {
		return ""
	}
	return u.Host
}

// RedirectOrigin is the scheme and host of the redirect address, for the
// consent page's form-action.
func (r *AuthRequest) RedirectOrigin() string {
	u, err := url.Parse(r.RedirectURI)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// AuthError is a refused authorization request. Either it is shown as a page
// and nothing is sent anywhere (the client or its redirect address is not
// accepted, so there is no address that may be told), or Redirect sends the
// error to the client's own redirect address.
type AuthError struct {
	// Page is set when the error must not be redirected.
	Page     bool
	Status   int
	Title    string
	Detail   string
	Host     string // the client's host, when it is known and not accepted
	Redirect func(w http.ResponseWriter)
}

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// ParseAuthorize checks an authorization request. It writes nothing and
// fetches at most the client's metadata document.
func (s *Server) ParseAuthorize(ctx context.Context, q url.Values) (*AuthRequest, *AuthError) {
	page := func(status int, title, detail, host string) *AuthError {
		return &AuthError{Page: true, Status: status, Title: title, Detail: detail, Host: host}
	}
	for k, v := range q {
		if len(v) != 1 {
			return nil, page(http.StatusBadRequest, "This request cannot be used", "The parameter "+k+" is given more than once.", "")
		}
	}
	// The answer goes in the query of the redirect address, the default for
	// the code flow; no response_mode is offered. The other modes (form_post,
	// and the JWT modes, which are not configured and made the library answer
	// 500) are refused as a page before the library sees them (review of
	// 2026-11-15, S-08).
	if _, given := q["response_mode"]; given {
		return nil, page(http.StatusBadRequest, "This request cannot be used", "The response_mode parameter is not supported; leave it out.", "")
	}
	// 1. The client, by Privo's policy. Never redirected: there is no address
	// yet that is known to be the client's.
	client, err := s.o.Clients.Lookup(ctx, q.Get("client_id"))
	if err != nil {
		var na *NotAcceptedError
		if errors.As(err, &na) {
			return nil, page(http.StatusBadRequest, "This client cannot connect", na.Reason, na.Host)
		}
		s.log.Error("oauthserver: look up client", "err", err)
		return nil, page(http.StatusInternalServerError, "Something went wrong", "Try again from your AI client.", "")
	}
	// 2. The redirect address, by Privo's rule (ADR 0022 point 11).
	redirectURI := q.Get("redirect_uri")
	if !MatchRedirect(client.RedirectURIs, redirectURI) {
		return nil, page(http.StatusBadRequest, "This client cannot connect",
			"The address it asked to be sent back to is not one of its own. Nothing was sent there.", client.Host())
	}
	// 3. The library, which now compares against exactly that address.
	ctx = context.WithValue(ctx, redirectKey{}, redirectURI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	if err != nil {
		return nil, page(http.StatusBadRequest, "This request cannot be used", "It could not be read.", "")
	}
	ar, err := s.provider.NewAuthorizeRequest(ctx, req)
	out := &AuthRequest{Client: client, RedirectURI: redirectURI, Query: q, ar: ar, ctx: ctx}
	fail := func(e error) *AuthError {
		if ar == nil || ar.GetRedirectURI() == nil || ar.GetRedirectURI().String() != redirectURI {
			// The library did not get as far as accepting the address.
			rfc := oauth2.ErrorToRFC6749Error(e)
			return page(http.StatusBadRequest, "This request cannot be used", "The client's request was refused: "+rfc.ErrorField+".", "")
		}
		return &AuthError{Redirect: func(w http.ResponseWriter) { s.provider.WriteAuthorizeError(ctx, w, ar, e) }}
	}
	if err != nil {
		return nil, fail(err)
	}
	// 4. Privo's own rules on top (points 8 and 9): S256 and this gateway.
	if q.Get("code_challenge_method") != "S256" || !challengeRe.MatchString(q.Get("code_challenge")) {
		return nil, fail(oauth2.ErrInvalidRequest.WithHint("A code_challenge with the method S256 is required."))
	}
	if q.Get("resource") != s.resource {
		return nil, fail(oauth2.ErrInvalidTarget.WithHint("The resource parameter must be " + s.resource + "."))
	}
	if q.Get("request") != "" || q.Get("request_uri") != "" {
		return nil, fail(oauth2.ErrInvalidRequest.WithHint("Request objects are not supported."))
	}
	return out, nil
}

// Allow issues an authorization code for a member who pressed Allow and
// sends the browser back to the client. This is the first write of the flow.
func (s *Server) Allow(w http.ResponseWriter, req *AuthRequest, u store.User) {
	ctx := context.WithValue(req.ctx, consentKey{}, req.Client)
	ar := req.ar
	ar.GrantScope(ScopeMCP)
	if ar.GetRequestedScopes().Has(ScopeOffline) {
		ar.GrantScope(ScopeOffline)
	}
	ar.GrantAudience(s.resource)
	ar.GrantResource(s.resource)
	resp, err := s.provider.NewAuthorizeResponse(ctx, ar, &oauth2.DefaultSession{Subject: u.ID, Username: u.Person})
	if err != nil {
		s.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	if req.Client.Kind == store.ClientApproved || req.Client.Kind == store.ClientRegistered {
		if err := s.o.Store.MarkOAuthClientAuthorized(ctx, s.o.Org, req.Client.ID, s.now()); err != nil {
			s.log.Error("oauthserver: mark client", "err", err)
		}
	}
	s.audit(ctx, "oauth.grant.allow", u.Person, "user:"+u.ID, u.ID, req.Client.Name, ar.GetID(), "allow", "ok")
	s.provider.WriteAuthorizeResponse(ctx, w, ar, resp)
}

// Deny sends the browser back to the client with access_denied.
func (s *Server) Deny(w http.ResponseWriter, req *AuthRequest, u store.User) {
	s.audit(req.ctx, "oauth.grant.deny", u.Person, "user:"+u.ID, u.ID, req.Client.Name, "", "deny", "denied")
	s.provider.WriteAuthorizeError(req.ctx, w, req.ar, oauth2.ErrAccessDenied.WithHint("The person did not allow this client."))
}

func (s *Server) audit(ctx context.Context, action, actor, actorID, userID, client, item, decision, result string) {
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: s.now(), Actor: actor, ActorID: actorID, UserID: userID,
		Client: client, Item: item, Action: action, Decision: decision, Outcome: result}
	// The request's context may be over (a client that hung up); the record
	// is still written.
	if err := s.o.Audit.Admin(context.WithoutCancel(ctx), s.o.Org, ev); err != nil {
		s.log.Error("oauthserver: audit", "action", action, "err", err)
	}
}

// ---- the token endpoint

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "The request body could not be read as a form.")
		return
	}
	grantType := r.PostForm.Get("grant_type")
	// The grant type is taken exactly as written. The library splits it on
	// spaces, so "authorization_code " would be a code exchange to it but not
	// to the checks below (review of 2026-11-15, S-02).
	if g := r.PostForm["grant_type"]; len(g) != 1 || (grantType != "authorization_code" && grantType != "refresh_token") {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "The grant_type must be authorization_code or refresh_token.")
		return
	}
	// Privo's own rule in front of the library: a code is exchanged for this
	// gateway and for nothing else, and says so (ADR 0022 point 9). A refresh
	// may leave the resource out; if it names one, it is this gateway.
	if res, given := r.PostForm["resource"]; (grantType == "authorization_code" && !given) || (given && (len(res) != 1 || res[0] != s.resource)) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "The resource parameter must be "+s.resource+".")
		return
	}
	out := &outcome{}
	ctx := context.WithValue(r.Context(), outcomeKey{}, out)
	ar, err := s.provider.NewAccessRequest(ctx, r, &oauth2.DefaultSession{})
	if err == nil {
		var resp oauth2.AccessResponder
		if resp, err = s.provider.NewAccessResponse(ctx, ar); err == nil {
			if grantType == "authorization_code" {
				sub := ar.GetSession().GetSubject()
				s.audit(ctx, "oauth.token.exchange", ar.GetSession().GetUsername(), "user:"+sub, sub, s.clientName(ctx, ar.GetClient().GetID()), ar.GetID(), "allow", "ok")
			}
			s.provider.WriteAccessResponse(ctx, w, ar, resp)
			return
		}
	}
	// A storage failure is rolled back by the library or, at the latest,
	// when this request's context ends.
	rfc := oauth2.ErrorToRFC6749Error(err)
	if rfc.CodeField >= http.StatusInternalServerError {
		s.log.Error("oauthserver: token endpoint", "grant_type", grantType, "err", err)
	}
	for _, g := range out.ended {
		reason := "a code was used twice"
		if out.refreshReuse {
			reason = "a refresh token that was already used was presented again"
		}
		s.log.Warn("oauthserver: a client's access was ended", "reason", reason, "client", g.ClientName, "user_id", g.UserID)
		s.audit(ctx, "oauth.grant.reuse", g.ClientName, "client:"+g.ClientID, g.UserID, g.ClientName, g.ID, "deny", reason)
	}
	s.provider.WriteAccessError(ctx, w, ar, err)
}

func (s *Server) clientName(ctx context.Context, id string) string {
	if c, err := s.o.Clients.Accepted(ctx, id); err == nil && c.Name != "" {
		return c.Name
	}
	return id
}

// revoke is the revocation endpoint (RFC 7009). Revoking either token ends
// the grant: the client is done with it.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "The request body could not be read as a form.")
		return
	}
	out := &outcome{}
	ctx := context.WithValue(r.Context(), outcomeKey{}, out)
	err := s.provider.NewRevocationRequest(ctx, r)
	for _, g := range out.ended {
		s.audit(ctx, "oauth.grant.revoke", g.ClientName, "client:"+g.ClientID, g.UserID, g.ClientName, g.ID, "allow", "revoked by the client")
	}
	s.provider.WriteRevocationResponse(ctx, w, err)
}

// ---- verification, for the gateway

// ErrInvalidToken is returned for any access token that does not verify.
var ErrInvalidToken = errors.New("oauthserver: invalid access token")

// IsAccessToken reports whether a bearer value has the shape of one of this
// server's access tokens, so the authenticator knows whom to ask.
func IsAccessToken(bearer string) bool { return strings.HasPrefix(bearer, accessTokenPrefix) }

// Recognizes is IsAccessToken, for the authenticator.
func (s *Server) Recognizes(bearer string) bool { return IsAccessToken(bearer) }

// VerifyAccessToken checks an access token for the gateway and returns the
// grant it belongs to, the member it acts as, read now (so a role change or a
// removal counts at once), and when the token runs out.
func (s *Server) VerifyAccessToken(ctx context.Context, bearer string) (store.ClientGrant, store.User, time.Time, error) {
	fail := func() (store.ClientGrant, store.User, time.Time, error) {
		return store.ClientGrant{}, store.User{}, time.Time{}, ErrInvalidToken
	}
	if !IsAccessToken(bearer) || len(bearer) > 512 {
		return fail()
	}
	st := &storage{s: s}
	r, _, g, err := st.get(ctx, store.OAuthAccess, s.strategy.AccessTokenSignature(ctx, bearer))
	if err != nil {
		if !errors.Is(err, oauth2.ErrNotFound) {
			s.log.Error("oauthserver: read access token", "err", err)
		}
		return fail()
	}
	// The signature over the whole token, and the token's own time.
	if err := s.strategy.ValidateAccessToken(ctx, r, bearer); err != nil {
		return fail()
	}
	exp := r.GetSession().GetExpiresAt(oauth2.AccessToken)
	if exp.IsZero() || !s.now().Before(exp) {
		return fail()
	}
	if !r.GetGrantedScopes().Has(ScopeMCP) || !r.GetGrantedResource().Has(s.resource) || r.GetSession().GetSubject() != g.UserID {
		return fail()
	}
	u, err := s.o.Store.UserByID(ctx, s.o.Org, g.UserID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("oauthserver: read member", "err", err)
		}
		return fail()
	}
	if u.DisabledAt != nil || !slices.ContainsFunc(u.Roles, store.GatewayRole) {
		return fail()
	}
	if g.ExpiresAt.Before(exp) {
		exp = g.ExpiresAt
	}
	u.OrgID = s.o.Org
	return g, u, exp, nil
}

// Prune deletes codes, tokens, grants and registrations that can no longer
// be used.
func (s *Server) Prune(ctx context.Context) error {
	return s.o.Store.PruneOAuth(ctx, s.o.Org, s.now(), unusedRegistration, idleRegistration)
}

// ---- dynamic registration (RFC 7591), only when it is switched on

type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// allowRegistration counts registrations by address, in memory.
func (s *Server) allowRegistration(addr string) bool {
	now := s.now()
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if now.Sub(s.regsAt) > registerWindow || len(s.regs) > 4096 {
		s.regs, s.regsAt = map[string][]time.Time{}, now
	}
	recent := s.regs[addr][:0]
	for _, t := range s.regs[addr] {
		if now.Sub(t) < registerWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= registerPerAddress {
		s.regs[addr] = recent
		return false
	}
	s.regs[addr] = append(recent, now)
	return true
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code, description string) { writeOAuthError(w, status, code, description) }
	var in registration
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(http.StatusBadRequest, "invalid_client_metadata", "The body is not a JSON client description.")
		return
	}
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" {
		fail(http.StatusBadRequest, "invalid_client_metadata", "Only public clients can register: token_endpoint_auth_method must be none.")
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			fail(http.StatusBadRequest, "invalid_client_metadata", "Only the authorization_code and refresh_token grants are offered.")
			return
		}
	}
	for _, t := range in.ResponseTypes {
		if t != "code" {
			fail(http.StatusBadRequest, "invalid_client_metadata", "Only the code response type is offered.")
			return
		}
	}
	if err := checkRedirects(in.RedirectURIs); err != nil {
		fail(http.StatusBadRequest, "invalid_redirect_uri", "The redirect addresses cannot be used: every one must be https, or http on the person's own computer, without a fragment.")
		return
	}
	if !s.allowRegistration(clientip.Key(clientip.From(r, s.o.TrustedProxies))) {
		w.Header().Set("Retry-After", "3600")
		fail(http.StatusTooManyRequests, "too_many_requests", "Too many clients were registered from this address. Try again later.")
		return
	}
	suffix := make([]byte, 18)
	if _, err := rand.Read(suffix); err != nil {
		fail(http.StatusInternalServerError, "server_error", "Try again.")
		return
	}
	name := cleanName(in.ClientName)
	if name == "" {
		name = "A client without a name"
	}
	now := s.now()
	c := store.OAuthClient{ClientID: registeredIDPrefix + base64.RawURLEncoding.EncodeToString(suffix), Kind: store.ClientRegistered, Name: name,
		RedirectURIs: in.RedirectURIs, CreatedAt: now}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: name, ActorID: "client:" + c.ClientID, Client: name,
		Action: "oauth.client.register", Decision: "allow", Outcome: "ok"}
	switch err := s.o.Store.SaveOAuthClient(r.Context(), s.o.Org, c, maxRegistered, &ev); {
	case errors.Is(err, store.ErrTooManyClients):
		w.Header().Set("Retry-After", "3600")
		fail(http.StatusTooManyRequests, "too_many_requests", "This Privo holds as many registered clients as it allows. Unused ones are removed after an hour.")
		return
	case err != nil:
		s.log.Error("oauthserver: register client", "err", err)
		fail(http.StatusInternalServerError, "server_error", "Try again.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                name,
		"redirect_uris":              in.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}
