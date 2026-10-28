// Package auth verifies callers and decides what they may do (ADR 0003, 0011).
package auth

import (
	"context"
	"errors"
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
	Role    string
	Kind    string // gateway | control
	Person  string // the person the token belongs to
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
)

var grants = map[string]map[Permission]bool{
	store.RoleOwner: {PermUseGateway: true, PermManageServers: true, PermManageTokens: true, PermReadAudit: true, PermManageAudit: true,
		PermReadServers: true, PermManagePolicy: true, PermDecide: true},
	store.RoleAdmin: {PermUseGateway: true, PermManageServers: true, PermManageTokens: true, PermReadAudit: true, PermManageAudit: true,
		PermReadServers: true, PermManagePolicy: true, PermDecide: true},
	store.RoleAuditor: {PermReadAudit: true, PermReadServers: true},
	// Approvers decide approvals; they do not run tools, so a token that can
	// approve is never in an AI client's hands.
	store.RoleApprover: {PermReadServers: true, PermDecide: true},
	store.RoleMember:   {PermUseGateway: true, PermReadServers: true},
}

// Can reports whether p has perm.
func (p Principal) Can(perm Permission) bool { return grants[p.Role][perm] }

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

// Verify checks a bearer token and returns its principal.
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
	p := Principal{OrgID: t.OrgID, TokenID: t.ID, Name: t.Name, Role: t.Role, Kind: t.Kind, Person: person}
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
		p, exp, err := a.Verify(ctx, bearer)
		if err != nil {
			return nil, mcpauth.ErrInvalidToken
		}
		// Only gateway tokens work on /mcp; control tokens are for people.
		if p.Kind != store.KindGateway || !p.Can(PermUseGateway) {
			return nil, mcpauth.ErrInvalidToken
		}
		return &mcpauth.TokenInfo{UserID: p.TokenID, Expiration: exp, Extra: map[string]any{principalKey: p}}, nil
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

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
