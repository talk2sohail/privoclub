package oauthserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	oauth2 "authelia.com/provider/oauth2"

	"privo.club/privo/internal/store"
)

// Store is what the authorization server needs of the store.
type Store interface {
	ClientStore
	BeginOAuthTx(ctx context.Context) (context.Context, error)
	CommitOAuthTx(ctx context.Context) error
	RollbackOAuthTx(ctx context.Context) error
	CreateClientGrant(ctx context.Context, org store.OrgID, g store.ClientGrant) error
	PutOAuthToken(ctx context.Context, org store.OrgID, t store.OAuthToken, now time.Time) error
	OAuthTokenBySignature(ctx context.Context, org store.OrgID, kind, signature string) (store.OAuthToken, store.ClientGrant, error)
	DeactivateOAuthToken(ctx context.Context, org store.OrgID, kind, signature string) (bool, error)
	DeleteOAuthToken(ctx context.Context, org store.OrgID, kind, signature string) error
	DeleteAccessTokens(ctx context.Context, org store.OrgID, grantID string) error
	RotateRefreshToken(ctx context.Context, org store.OrgID, grantID, signature string, now time.Time) error
	EndClientGrant(ctx context.Context, org store.OrgID, grantID string) (store.ClientGrant, error)
	MarkOAuthClientAuthorized(ctx context.Context, org store.OrgID, clientID string, now time.Time) error
	SaveOAuthClient(ctx context.Context, org store.OrgID, c store.OAuthClient, limit int, ev *store.AuditEvent) error
	OAuthSigningKey(ctx context.Context, org store.OrgID) (uint32, []byte, error)
	CreateOAuthSigningKey(ctx context.Context, org store.OrgID, version uint32, sealed []byte, now time.Time) error
	UserByID(ctx context.Context, org store.OrgID, id string) (store.User, error)
	PruneOAuth(ctx context.Context, org store.OrgID, now time.Time, unusedFor, idleFor time.Duration) error
}

// storage is the library's storage, kept in Privo's database. The library
// asks for it by the interfaces of its handlers (authorization code, PKCE,
// refresh, revocation) and for transactions; every method maps to one or two
// statements of the store.
type storage struct {
	s *Server
}

// What a request carries through the library in its context.
type (
	redirectKey struct{} // the one redirect address Privo's rule accepted
	consentKey  struct{} // the client a member is allowing, for the new grant
	outcomeKey  struct{} // what the storage did, for the audit
)

// outcome collects what happened inside the library during one request to
// the token or revocation endpoint, so the handler can audit it.
type outcome struct {
	codeReuse    bool // a used code was presented again
	refreshReuse bool // a rotated refresh token was presented again
	ended        []store.ClientGrant
}

func outcomeOf(ctx context.Context) *outcome {
	o, _ := ctx.Value(outcomeKey{}).(*outcome)
	if o == nil {
		return &outcome{} // nobody is listening
	}
	return o
}

// stored is a request of the library as it is kept in a token row.
type stored struct {
	ID              string                 `json:"id"`
	RequestedAt     time.Time              `json:"requested_at"`
	ClientID        string                 `json:"client_id"`
	Scopes          []string               `json:"scopes"`
	Granted         []string               `json:"granted"`
	Audience        []string               `json:"audience,omitempty"`
	GrantedAudience []string               `json:"granted_audience,omitempty"`
	Resource        []string               `json:"resource,omitempty"`
	GrantedResource []string               `json:"granted_resource,omitempty"`
	Form            url.Values             `json:"form,omitempty"`
	Session         *oauth2.DefaultSession `json:"session"`
}

func encode(r oauth2.Requester) ([]byte, time.Time, error) {
	sess, ok := r.GetSession().(*oauth2.DefaultSession)
	if !ok || sess == nil {
		return nil, time.Time{}, errors.New("oauthserver: a request without Privo's session")
	}
	b, err := json.Marshal(stored{
		ID: r.GetID(), RequestedAt: r.GetRequestedAt(), ClientID: r.GetClient().GetID(),
		Scopes: r.GetRequestedScopes(), Granted: r.GetGrantedScopes(),
		Audience: r.GetRequestedAudience(), GrantedAudience: r.GetGrantedAudience(),
		Resource: r.GetRequestedResource(), GrantedResource: r.GetGrantedResource(),
		Form: r.GetRequestForm(), Session: sess,
	})
	return b, r.GetRequestedAt(), err
}

func decode(b []byte) (*oauth2.Request, error) {
	var st stored
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.Session == nil {
		st.Session = &oauth2.DefaultSession{}
	}
	if st.Form == nil {
		st.Form = url.Values{}
	}
	return &oauth2.Request{
		ID: st.ID, RequestedAt: st.RequestedAt, Client: libraryClient(st.ClientID, nil, ""),
		RequestedScope: st.Scopes, GrantedScope: st.Granted,
		RequestedAudience: st.Audience, GrantedAudience: st.GrantedAudience,
		RequestedResource: st.Resource, GrantedResource: st.GrantedResource,
		Form: st.Form, Session: st.Session,
	}, nil
}

// libraryClient is a client as the library wants it: public, the two grants,
// the code response, Privo's scopes, and this gateway as the only audience.
func libraryClient(id string, redirects []string, resource string) *oauth2.DefaultClient {
	c := &oauth2.DefaultClient{ID: id, Public: true, RedirectURIs: redirects,
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
		Scopes:        []string{ScopeMCP, ScopeOffline},
	}
	if resource != "" {
		c.Audience = []string{resource}
	}
	return c
}

// expiry is when a row of this kind can be deleted: the time the library set
// on the session for it, or a fallback that is never shorter.
func (st *storage) expiry(r oauth2.Requester, kind oauth2.TokenType, fallback time.Duration) time.Time {
	if t := r.GetSession().GetExpiresAt(kind); !t.IsZero() {
		return t
	}
	return st.s.now().Add(fallback)
}

// ---- clients

// GetClient is the library's client lookup. At the authorization endpoint
// Privo has already looked the client up and matched the redirect address by
// its own rule, and the context names the one address that passed: the
// library then compares against exactly that. At the token and revocation
// endpoints the client is accepted by the policy alone.
func (st *storage) GetClient(ctx context.Context, id string) (oauth2.Client, error) {
	if accepted, ok := ctx.Value(redirectKey{}).(string); ok && accepted != "" {
		return libraryClient(id, []string{accepted}, st.s.resource), nil
	}
	if _, err := st.s.o.Clients.Accepted(ctx, id); err != nil {
		var na *NotAcceptedError
		if errors.As(err, &na) {
			return nil, oauth2.ErrNotFound
		}
		return nil, err
	}
	return libraryClient(id, nil, st.s.resource), nil
}

// ClientAssertionJWTValid and SetClientAssertionJWT belong to client
// authentication with signed assertions, which public clients do not use.
func (st *storage) ClientAssertionJWTValid(context.Context, string) error {
	return oauth2.ErrJTIKnown
}

func (st *storage) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return oauth2.ErrJTIKnown
}

// CreateClient, UpdateClient and DeleteClient are the library's own client
// registration, which Privo does not use: registration is Privo's handler,
// off by default (ADR 0022 point 13).
func (st *storage) CreateClient(context.Context, oauth2.Client) error { return errNoRegistration }
func (st *storage) UpdateClient(context.Context, string, oauth2.Client) error {
	return errNoRegistration
}
func (st *storage) DeleteClient(context.Context, string) error { return errNoRegistration }

var errNoRegistration = errors.New("oauthserver: clients are not managed through the library")

// ---- transactions

func (st *storage) BeginTX(ctx context.Context) (context.Context, error) {
	return st.s.o.Store.BeginOAuthTx(ctx)
}
func (st *storage) Commit(ctx context.Context) error   { return st.s.o.Store.CommitOAuthTx(ctx) }
func (st *storage) Rollback(ctx context.Context) error { return st.s.o.Store.RollbackOAuthTx(ctx) }

// ---- rows

func (st *storage) put(ctx context.Context, kind, signature string, r oauth2.Requester, expires time.Time) error {
	b, _, err := encode(r)
	if err != nil {
		return err
	}
	err = st.s.o.Store.PutOAuthToken(ctx, st.s.o.Org, store.OAuthToken{Kind: kind, Signature: signature, GrantID: r.GetID(), Request: b, ExpiresAt: expires}, st.s.now())
	if errors.Is(err, store.ErrNotFound) {
		return oauth2.ErrNotFound // the grant was ended meanwhile
	}
	return err
}

// get reads a row and its grant. A grant past its 90 days counts as gone.
func (st *storage) get(ctx context.Context, kind, signature string) (*oauth2.Request, store.OAuthToken, store.ClientGrant, error) {
	t, g, err := st.s.o.Store.OAuthTokenBySignature(ctx, st.s.o.Org, kind, signature)
	if errors.Is(err, store.ErrNotFound) {
		return nil, t, g, oauth2.ErrNotFound
	}
	if err != nil {
		return nil, t, g, err
	}
	if !st.s.now().Before(g.ExpiresAt) {
		return nil, t, g, oauth2.ErrNotFound
	}
	r, err := decode(t.Request)
	if err != nil {
		return nil, t, g, err
	}
	return r, t, g, nil
}

// ---- authorization codes

// CreateAuthorizeCodeSession is the first write of a flow: a signed-in member
// pressed Allow. It records the grant and its code.
func (st *storage) CreateAuthorizeCodeSession(ctx context.Context, signature string, r oauth2.Requester) error {
	c, ok := ctx.Value(consentKey{}).(Client)
	if !ok || c.ID != r.GetClient().GetID() {
		return errors.New("oauthserver: a code without a consent")
	}
	now := st.s.now()
	g := store.ClientGrant{ID: r.GetID(), UserID: r.GetSession().GetSubject(), ClientID: c.ID, ClientName: c.Name, ClientKind: c.Kind,
		Scope: joinScopes(r.GetGrantedScopes()), CreatedAt: now, ExpiresAt: now.Add(st.s.o.GrantTTL)}
	if err := st.s.o.Store.CreateClientGrant(ctx, st.s.o.Org, g); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return oauth2.ErrAccessDenied.WithHint("This member may not allow a client.")
		}
		return err
	}
	return st.put(ctx, store.OAuthCode, signature, r, st.expiry(r, oauth2.AuthorizeCode, st.s.o.CodeTTL))
}

func (st *storage) GetAuthorizeCodeSession(ctx context.Context, signature string, _ oauth2.Session) (oauth2.Requester, error) {
	r, t, _, err := st.get(ctx, store.OAuthCode, signature)
	if err != nil {
		return nil, err
	}
	if !t.Active {
		outcomeOf(ctx).codeReuse = true
		return r, oauth2.ErrInvalidatedAuthorizeCode
	}
	return r, nil
}

func (st *storage) InvalidateAuthorizeCodeSession(ctx context.Context, signature string) error {
	first, err := st.s.o.Store.DeactivateOAuthToken(ctx, st.s.o.Org, store.OAuthCode, signature)
	if err != nil {
		return err
	}
	if !first {
		outcomeOf(ctx).codeReuse = true
		return oauth2.ErrInvalidatedAuthorizeCode
	}
	return nil
}

// ---- PKCE

func (st *storage) CreatePKCERequestSession(ctx context.Context, signature string, r oauth2.Requester) error {
	return st.put(ctx, store.OAuthPKCE, signature, r, st.expiry(r, oauth2.AuthorizeCode, st.s.o.CodeTTL))
}

func (st *storage) GetPKCERequestSession(ctx context.Context, signature string, _ oauth2.Session) (oauth2.Requester, error) {
	r, _, _, err := st.get(ctx, store.OAuthPKCE, signature)
	return r, err
}

func (st *storage) DeletePKCERequestSession(ctx context.Context, signature string) error {
	return st.s.o.Store.DeleteOAuthToken(ctx, st.s.o.Org, store.OAuthPKCE, signature)
}

// ---- access tokens

func (st *storage) CreateAccessTokenSession(ctx context.Context, signature string, r oauth2.Requester) error {
	return st.put(ctx, store.OAuthAccess, signature, r, st.expiry(r, oauth2.AccessToken, st.s.o.AccessTTL))
}

func (st *storage) GetAccessTokenSession(ctx context.Context, signature string, _ oauth2.Session) (oauth2.Requester, error) {
	r, _, _, err := st.get(ctx, store.OAuthAccess, signature)
	return r, err
}

func (st *storage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	return st.s.o.Store.DeleteOAuthToken(ctx, st.s.o.Org, store.OAuthAccess, signature)
}

// ---- refresh tokens

func (st *storage) CreateRefreshTokenSession(ctx context.Context, signature, _ string, r oauth2.Requester) error {
	return st.put(ctx, store.OAuthRefresh, signature, r, st.expiry(r, oauth2.RefreshToken, st.s.o.RefreshTTL))
}

// GetRefreshTokenSession returns a rotated refresh token together with
// ErrInactiveToken. The library then ends the grant: there is no grace
// window (ADR 0022 point 9).
func (st *storage) GetRefreshTokenSession(ctx context.Context, signature string, _ oauth2.Session) (oauth2.Requester, error) {
	r, t, _, err := st.get(ctx, store.OAuthRefresh, signature)
	if err != nil {
		return nil, err
	}
	if !t.Active {
		outcomeOf(ctx).refreshReuse = true
		return r, oauth2.ErrInactiveToken
	}
	return r, nil
}

func (st *storage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	return st.s.o.Store.DeleteOAuthToken(ctx, st.s.o.Org, store.OAuthRefresh, signature)
}

// RotateRefreshToken uses a refresh token up. Of two refreshes with the same
// token at the same time one finds it used and is refused.
func (st *storage) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	err := st.s.o.Store.RotateRefreshToken(ctx, st.s.o.Org, requestID, signature, st.s.now())
	if errors.Is(err, store.ErrConflict) {
		return oauth2.ErrInactiveToken
	}
	return err
}

// RevokeRefreshToken ends the whole grant. The library calls it for a client
// that revokes its token, for a code used twice and for a rotated refresh
// token used again; in each case nothing of the grant may keep working.
func (st *storage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	g, err := st.s.o.Store.EndClientGrant(ctx, st.s.o.Org, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	o := outcomeOf(ctx)
	o.ended = append(o.ended, g)
	return nil
}

// RevokeAccessToken removes the access tokens of a grant.
func (st *storage) RevokeAccessToken(ctx context.Context, requestID string) error {
	return st.s.o.Store.DeleteAccessTokens(ctx, st.s.o.Org, requestID)
}

func joinScopes(scopes []string) string {
	out := ""
	for i, s := range scopes {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
