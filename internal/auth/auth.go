// Package auth verifies callers and decides what they may do (ADR 0003, 0011).
package auth

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

// Principal is an authenticated caller.
type Principal struct {
	OrgID   store.OrgID
	TokenID string
	Name    string
	// Roles are the signed-in person's current roles, or a token's one role
	// (ADR 0025). Code asks Has or Can, never compares a role.
	Roles  []string
	Kind   string // gateway | control
	Person string // a label: who the caller is called. It gives no access (F27)
	// UserID is the member the caller is: a signed-in person (kind "session",
	// no token id), or the member a token belongs to. It alone decides whose
	// teams and upstream accounts the caller gets; "" is a token of nobody.
	UserID string
	// Client names the AI client when the caller signed in with OAuth (ADR
	// 0022): TokenID is then "oauth:" and the grant's id. "" otherwise.
	Client string
}

// OAuthPrefix starts the TokenID of a caller that signed in with OAuth.
const OAuthPrefix = "oauth:"

// ScopeGateway is the one scope of the gateway: use it as the caller. Every
// credential the gateway accepts carries it; it is named in the 401 so an
// OAuth client knows what to ask for (ADR 0022 point 5).
const ScopeGateway = "mcp"

// AccessTokens verifies the access tokens of Privo's own authorization
// server (internal/oauthserver).
type AccessTokens interface {
	// Recognizes reports whether a bearer value has the shape of an access
	// token of the server.
	Recognizes(bearer string) bool
	// VerifyAccessToken returns the grant, the member it acts as, read now,
	// and when the token runs out.
	VerifyAccessToken(ctx context.Context, bearer string) (store.ClientGrant, store.User, time.Time, error)
}

// ActorID is how audit records name the principal: the token id, or
// "user:<id>" for a signed-in person.
func (p Principal) ActorID() string {
	if p.TokenID == "" && p.UserID != "" {
		return "user:" + p.UserID
	}
	return p.TokenID
}

// Permission is something a role may do.
type Permission string

// Permissions checked by the admin API and gateway.
const (
	PermUseGateway    Permission = "gateway.use"
	PermManageServers Permission = "servers.manage"
	PermManageTokens  Permission = "tokens.manage"
	PermReadAudit     Permission = "audit.read"
	PermManageAudit   Permission = "audit.manage"
	PermReadServers   Permission = "servers.read"
	PermManagePolicy  Permission = "policy.manage"
	PermDecide        Permission = "approvals.decide"
	PermManageMembers Permission = "members.manage"
)

var grants = map[string]map[Permission]bool{
	store.RoleOwner: {PermUseGateway: true, PermManageServers: true, PermManageTokens: true, PermReadAudit: true, PermManageAudit: true,
		PermReadServers: true, PermManagePolicy: true, PermDecide: true, PermManageMembers: true},
	store.RoleAdmin: {PermUseGateway: true, PermManageServers: true, PermManageTokens: true, PermReadAudit: true, PermManageAudit: true,
		PermReadServers: true, PermManagePolicy: true, PermDecide: true, PermManageMembers: true},
	store.RoleAuditor: {PermReadAudit: true, PermReadServers: true},
	// Approvers decide approvals; they do not run tools, so a token that can
	// approve is never in an AI client's hands.
	store.RoleApprover: {PermReadServers: true, PermDecide: true},
	store.RoleMember:   {PermUseGateway: true, PermReadServers: true},
}

// Can reports whether any of p's roles grants perm. No role, or only unknown
// ones, grants nothing.
func (p Principal) Can(perm Permission) bool { return RolesCan(p.Roles, perm) }

// Has reports whether p holds role.
func (p Principal) Has(role string) bool { return slices.Contains(p.Roles, role) }

// RolesCan reports whether any of roles grants perm.
func RolesCan(roles []string, perm Permission) bool {
	for _, r := range roles {
		if grants[r][perm] {
			return true
		}
	}
	return false
}

// ErrUnauthenticated is returned for any token that does not verify.
var ErrUnauthenticated = errors.New("auth: invalid or expired token")

// CacheTTL bounds how long a verified token is trusted without a fresh lookup.
// It is what makes "revocation within 5 seconds" (ADR 0003) hold.
const CacheTTL = 2 * time.Second

// TokenStore is the store subset auth needs.
type TokenStore interface {
	TokenByID(ctx context.Context, id string) (store.APIToken, error)
}

// Authenticator verifies personal access tokens.
type Authenticator struct {
	store TokenStore
	now   func() time.Time
	oauth AccessTokens // nil: the authorization server is off

	mu    sync.Mutex
	cache map[string]cachedPrincipal // keyed by full token hash
}

type cachedPrincipal struct {
	p        Principal
	expires  time.Time
	tokenExp time.Time
}

// New returns an authenticator.
func New(s TokenStore) *Authenticator {
	return &Authenticator{store: s, now: time.Now, cache: map[string]cachedPrincipal{}}
}

// AcceptOAuth lets the gateway accept access tokens of Privo's own
// authorization server. Call it before the authenticator is used.
func (a *Authenticator) AcceptOAuth(t AccessTokens) { a.oauth = t }

// VerifyGateway checks a bearer token for the gateway: a personal token, or
// an OAuth access token when the authorization server is on. Only this
// method accepts the second kind, so an access token is never a credential
// for the admin API, whatever role its member has (ADR 0022 point 4).
//
// The member's role is read when the token is verified and kept for
// CacheTTL, like everything else here: a role change, a removal or a
// revocation counts within two seconds.
func (a *Authenticator) VerifyGateway(ctx context.Context, bearer string) (Principal, time.Time, error) {
	if a.oauth == nil || !a.oauth.Recognizes(bearer) {
		return a.Verify(ctx, bearer)
	}
	key := token.HashSecret(bearer)
	now := a.now()
	a.mu.Lock()
	c, ok := a.cache[key]
	a.mu.Unlock()
	if ok && now.Before(c.expires) && now.Before(c.tokenExp) {
		return c.p, c.tokenExp, nil
	}
	g, u, exp, err := a.oauth.VerifyAccessToken(ctx, bearer)
	if err != nil || !now.Before(exp) {
		a.mu.Lock()
		delete(a.cache, key)
		a.mu.Unlock()
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	name := g.ClientName
	if name == "" {
		name = g.ClientID
	}
	p := Principal{OrgID: u.OrgID, TokenID: OAuthPrefix + g.ID, Name: name, Roles: []string{store.RoleMember}, Kind: store.KindGateway, Person: u.Person, UserID: u.ID, Client: name}
	a.mu.Lock()
	if len(a.cache) > 10000 {
		a.cache = map[string]cachedPrincipal{}
	}
	a.cache[key] = cachedPrincipal{p: p, expires: now.Add(CacheTTL), tokenExp: exp}
	a.mu.Unlock()
	return p, exp, nil
}

// Verify checks a personal bearer token and returns its principal. It never
// accepts an OAuth access token: see VerifyGateway.
func (a *Authenticator) Verify(ctx context.Context, bearer string) (Principal, time.Time, error) {
	id, secret, err := token.Parse(bearer)
	if err != nil {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	key := token.HashSecret(bearer)
	now := a.now()

	a.mu.Lock()
	c, ok := a.cache[key]
	a.mu.Unlock()
	if ok && now.Before(c.expires) && now.Before(c.tokenExp) {
		return c.p, c.tokenExp, nil
	}

	t, err := a.store.TokenByID(ctx, id)
	if err != nil || t.RevokedAt != nil || !now.Before(t.ExpiresAt) || !token.Verify(secret, t.SecretHash) {
		a.mu.Lock()
		delete(a.cache, key)
		a.mu.Unlock()
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	person := t.Person
	if person == "" {
		person = t.Name
	}
	p := Principal{OrgID: t.OrgID, TokenID: t.ID, Name: t.Name, Roles: []string{t.Role}, Kind: t.Kind, Person: person, UserID: t.UserID}
	a.mu.Lock()
	if len(a.cache) > 10000 {
		a.cache = map[string]cachedPrincipal{}
	}
	a.cache[key] = cachedPrincipal{p: p, expires: now.Add(CacheTTL), tokenExp: t.ExpiresAt}
	a.mu.Unlock()
	return p, t.ExpiresAt, nil
}

const principalKey = "privo.principal"

// MCPVerifier adapts Verify to the MCP SDK's bearer-token middleware.
func (a *Authenticator) MCPVerifier() mcpauth.TokenVerifier {
	return func(ctx context.Context, bearer string, _ httpRequest) (*mcpauth.TokenInfo, error) {
		p, exp, err := a.VerifyGateway(ctx, bearer)
		if err != nil {
			return nil, mcpauth.ErrInvalidToken
		}
		// Only gateway tokens work on /mcp; control tokens are for people.
		if p.Kind != store.KindGateway || !p.Can(PermUseGateway) {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: p.TokenID, Expiration: exp, Scopes: []string{ScopeGateway}, Extra: map[string]any{principalKey: p}}, nil
	}
}

// FromTokenInfo extracts the principal placed by MCPVerifier.
func FromTokenInfo(ti *mcpauth.TokenInfo) (Principal, bool) {
	if ti == nil {
		return Principal{}, false
	}
	p, ok := ti.Extra[principalKey].(Principal)
	return p, ok
}

type principalCtxKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// FromContext returns the principal placed by WithPrincipal, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	return p, ok
}
