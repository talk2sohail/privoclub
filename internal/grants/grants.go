// Package grants keeps each person's upstream OAuth grants usable (ADR 0017):
// it seals tokens into the vault, hands out access tokens, refreshes them once
// per grant at a time, and disconnects.
//
// Tokens are only ever decrypted in memory here and returned to the transport
// that adds them to requests to the server's own origin. Nothing in this
// package logs a token, and errors never contain one.
package grants

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

// Skew is how long before expiry an access token is refreshed.
const Skew = 30 * time.Second

// refreshTimeout bounds one refresh. It runs without the caller's context, so
// a caller that goes away cannot leave a rotated refresh token unsaved.
const refreshTimeout = 20 * time.Second

// ErrNotConnected means the person has no usable grant for the server: never
// connected, disconnected, or the authorization server refused the grant.
var ErrNotConnected = errors.New("grants: no connected account")

// ErrServerOff is returned for a server an admin turned off: its provider is
// asked for nothing, and the grants are kept for when it is on again.
var ErrServerOff = errors.New("grants: the server is turned off")

// ErrKeyRefused means the server refused the person's own API key (ADR
// 0023). The key is kept: a 401 can be the server's mistake, and the person
// replaces the key on the connect page.
var ErrKeyRefused = errors.New("grants: the server refused the person's API key")

// maxKeyLen bounds an API key.
const maxKeyLen = 4096

// ValidKey reports whether a pasted API key can be sent in a header: printable
// ASCII without spaces, at most 4096 characters.
func ValidKey(key string) bool {
	if key == "" || len(key) > maxKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// Store is the store subset the service uses.
type Store interface {
	Grant(ctx context.Context, org store.OrgID, serverID, userID string) (store.Grant, error)
	CatalogGrant(ctx context.Context, org store.OrgID, serverID string) (store.Grant, error)
	PassCatalog(ctx context.Context, org store.OrgID, serverID, userID string) (bool, error)
	SaveGrant(ctx context.Context, org store.OrgID, g store.Grant, catalogOK bool, ev store.AuditEvent) (store.Grant, error)
	UpdateGrantToken(ctx context.Context, org store.OrgID, serverID, userID string, from uint64, c store.Credential, now time.Time) error
	DeleteGrant(ctx context.Context, org store.OrgID, serverID, userID string, ev store.AuditEvent) error
	TouchGrant(ctx context.Context, org store.OrgID, serverID, userID string, now time.Time) error
	ServerCredentials(ctx context.Context, org store.OrgID, serverID string) ([]store.Credential, error)
	UpdateServerOAuth(ctx context.Context, org store.OrgID, serverID string, o store.ServerOAuth, secret *store.Credential) error
	ServerByID(ctx context.Context, org store.OrgID, id string) (store.Server, error)
}

// Service is safe for concurrent use.
type Service struct {
	Store     Store
	Keys      *keyring.Keyring
	Auth      *upstreamauth.Client
	PublicURL *url.URL
	Now       func() time.Time
	Logger    *slog.Logger
	// Diagnostics receives grant events for the diagnostic log (ADR 0024);
	// nil records none.
	Diagnostics *telemetry.Recorder

	mu      sync.Mutex
	flights map[string]*flight
	// unsaved holds tokens the provider issued that could not be saved
	// (F07 R-06): with rotating refresh tokens, losing them loses the grant.
	unsaved map[string]unsavedToken
}

// unsavedToken is a sealed credential waiting to be saved over the stored
// row version from.
type unsavedToken struct {
	from uint64
	c    store.Credential
}

// saveAttempts and saveBackoff bound the retries of saving a refreshed token.
const saveAttempts = 4

var saveBackoff = 25 * time.Millisecond

type flight struct {
	done chan struct{}
	tok  string
	err  error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Endpoint returns what token requests need for an oauth server, with the
// client secret decrypted when there is one.
func (s *Service) Endpoint(ctx context.Context, org store.OrgID, srv store.Server) (upstreamauth.Endpoint, error) {
	o := srv.Config.OAuth
	if srv.Config.Auth != store.AuthOAuth || o == nil {
		return upstreamauth.Endpoint{}, errors.New("grants: the server does not use OAuth")
	}
	e := upstreamauth.Endpoint{AuthorizationEndpoint: o.AuthorizationEndpoint, TokenEndpoint: o.TokenEndpoint,
		RevocationEndpoint: o.RevocationEndpoint, Resource: o.Resource, ClientID: o.ClientID, AuthMethod: o.TokenAuthMethod}
	if o.Registration == store.RegistrationCIMD {
		if s.PublicURL == nil {
			return upstreamauth.Endpoint{}, errors.New("grants: a client ID metadata document needs the public URL")
		}
		e.ClientID = upstreamauth.ClientMetadataURL(s.PublicURL)
	}
	if e.AuthMethod == "" {
		e.AuthMethod = "none"
	}
	if !o.HasClientSecret {
		return e, nil
	}
	creds, err := s.Store.ServerCredentials(ctx, org, srv.ID)
	if err != nil {
		return upstreamauth.Endpoint{}, err
	}
	for _, c := range creds {
		if c.Purpose != store.PurposeOAuthClient || c.OwnerID != "" {
			continue
		}
		pt, err := s.open(ctx, org, c)
		if err != nil {
			return upstreamauth.Endpoint{}, fmt.Errorf("grants: client secret: %w", err)
		}
		e.ClientSecret = string(pt)
		return e, nil
	}
	return upstreamauth.Endpoint{}, errors.New("grants: the server's client secret is missing")
}

// SealClientSecret encrypts a client secret for UpdateServerOAuth or CreateServer.
func (s *Service) SealClientSecret(ctx context.Context, org store.OrgID, serverID, secret string) (store.Credential, error) {
	return s.seal(ctx, org, store.Credential{ID: ids.New(), ServerID: serverID, Purpose: store.PurposeOAuthClient, RowVersion: 1}, []byte(secret))
}

// Reregister registers Privo again with a server's authorization server, for a
// server that was registered dynamically and whose registration the
// authorization server no longer accepts (invalid_client). Grants made with
// the old client keep working only if the authorization server still honours
// them; a person whose refresh fails connects again.
func (s *Service) Reregister(ctx context.Context, org store.OrgID, srv store.Server) error {
	if srv.Config.OAuth == nil || srv.Config.OAuth.Registration != store.RegistrationDynamic || srv.Config.OAuth.RegistrationEndpoint == "" {
		return errors.New("grants: the server was not registered dynamically")
	}
	if s.PublicURL == nil {
		return errors.New("grants: registering needs the public URL")
	}
	reg, err := s.Auth.Register(ctx, srv.Config.OAuth.RegistrationEndpoint, s.PublicURL)
	if err != nil {
		return err
	}
	o := *srv.Config.OAuth
	o.ClientID, o.TokenAuthMethod, o.HasClientSecret = reg.ClientID, reg.AuthMethod, reg.ClientSecret != ""
	var secret *store.Credential
	if reg.ClientSecret != "" {
		c, err := s.SealClientSecret(ctx, org, srv.ID, reg.ClientSecret)
		if err != nil {
			return err
		}
		secret = &c
	}
	return s.Store.UpdateServerOAuth(ctx, org, srv.ID, o, secret)
}

// Save stores a person's new grant, sealed to that person and server. catalogOK
// says the user manages servers, so their grant may list the catalog.
func (s *Service) Save(ctx context.Context, org store.OrgID, srv store.Server, user store.User, tok upstreamauth.Token, catalogOK bool, ev store.AuditEvent) (store.Grant, error) {
	b, err := json.Marshal(tok) //nolint:gosec // the tokens are marshalled only to be sealed in the vault
	if err != nil {
		return store.Grant{}, err
	}
	c, err := s.seal(ctx, org, store.Credential{ID: ids.New(), ServerID: srv.ID, OwnerID: user.ID, Purpose: store.PurposeOAuthGrant, RowVersion: 1}, b)
	if err != nil {
		return store.Grant{}, err
	}
	return s.Store.SaveGrant(ctx, org, store.Grant{ServerID: srv.ID, UserID: user.ID, Scopes: tok.Scope, ConnectedAt: s.now(), Credential: c}, catalogOK, ev)
}

// SaveKey stores a person's own API key for a server (ADR 0023), sealed to
// that person and server like an OAuth grant, replacing a key they saved
// before. catalogOK says the user manages servers, so their key may list the
// catalog.
func (s *Service) SaveKey(ctx context.Context, org store.OrgID, srv store.Server, user store.User, key string, catalogOK bool, ev store.AuditEvent) (store.Grant, error) {
	if srv.Config.Auth != store.AuthKey {
		return store.Grant{}, errors.New("grants: the server does not take personal keys")
	}
	if !ValidKey(key) {
		return store.Grant{}, errors.New("grants: an API key is printable characters without spaces, at most 4096")
	}
	b, err := json.Marshal(upstreamauth.Token{AccessToken: key}) //nolint:gosec // marshalled only to be sealed in the vault
	if err != nil {
		return store.Grant{}, err
	}
	c, err := s.seal(ctx, org, store.Credential{ID: ids.New(), ServerID: srv.ID, OwnerID: user.ID, Purpose: store.PurposeKeyGrant, RowVersion: 1}, b)
	if err != nil {
		return store.Grant{}, err
	}
	return s.Store.SaveGrant(ctx, org, store.Grant{ServerID: srv.ID, UserID: user.ID, ConnectedAt: s.now(), Credential: c}, catalogOK, ev)
}

// AccessToken returns a usable access token of the person's grant, refreshing
// it when it is about to expire. ErrNotConnected when there is no grant.
func (s *Service) AccessToken(ctx context.Context, org store.OrgID, srv store.Server, userID string) (string, error) {
	g, err := s.Store.Grant(ctx, org, srv.ID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", err
	}
	return s.usable(ctx, org, srv, g)
}

// CatalogAccessToken is AccessToken for the server's catalog grant; it also
// returns whose grant that is.
func (s *Service) CatalogAccessToken(ctx context.Context, org store.OrgID, srv store.Server) (userID, token string, err error) {
	g, err := s.Store.CatalogGrant(ctx, org, srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	token, err = s.usable(ctx, org, srv, g)
	return g.UserID, token, err
}

func (s *Service) usable(ctx context.Context, org store.OrgID, srv store.Server, g store.Grant) (string, error) {
	// A key is used only for a server that takes keys, and an OAuth grant
	// only for one that signs people in. Changing a server's sign-in method
	// is planned; until the stored credentials follow, a mismatch reads as not
	// connected (review of 2026-11-15, U-07).
	if (srv.Config.Auth == store.AuthKey) != (g.Credential.Purpose == store.PurposeKeyGrant) {
		return "", ErrNotConnected
	}
	tok, err := s.token(ctx, org, g)
	if err != nil {
		return "", err
	}
	now := s.now()
	if !tok.Expired(now, Skew) {
		if err := s.Store.TouchGrant(ctx, org, srv.ID, g.UserID, now); err != nil {
			s.logger().Warn("recording a grant's use failed", "server", srv.Alias, "err", err)
		}
		return tok.AccessToken, nil
	}
	return s.refresh(ctx, org, srv, g.UserID, tok.AccessToken)
}

// PassCatalog hands a server's catalog to another manager's grant after the
// upstream refused userID's key (store.PassCatalog).
func (s *Service) PassCatalog(ctx context.Context, org store.OrgID, srv store.Server, userID string) (bool, error) {
	moved, err := s.Store.PassCatalog(ctx, org, srv.ID, userID)
	if moved {
		s.logger().Warn("the server refused the key the tool list was read with; another administrator's key reads it now", "server", srv.Alias)
	}
	return moved, err
}

// RefreshAfterReject refreshes the person's grant after the server refused the
// access token rejected (401 invalid_token). If another request already
// refreshed it, the newer token is returned without asking again.
func (s *Service) RefreshAfterReject(ctx context.Context, org store.OrgID, srv store.Server, userID, rejected string) (string, error) {
	return s.refresh(ctx, org, srv, userID, rejected)
}

// refresh replaces the token stale. At most one refresh per grant runs in this
// process; others wait for it. Across replicas the row version decides.
func (s *Service) refresh(ctx context.Context, org store.OrgID, srv store.Server, userID, stale string) (string, error) {
	key := string(org) + "\x00" + srv.ID + "\x00" + userID
	s.mu.Lock()
	if s.flights == nil {
		s.flights = map[string]*flight{}
	}
	if f, ok := s.flights[key]; ok {
		s.mu.Unlock()
		select {
		case <-f.done:
			return f.tok, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	s.flights[key] = f
	s.mu.Unlock()

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	f.tok, f.err = s.doRefresh(rctx, org, srv, userID, stale)
	cancel()

	s.mu.Lock()
	delete(s.flights, key)
	s.mu.Unlock()
	close(f.done)
	return f.tok, f.err
}

// current reads the server as it is stored now. A caller may hold it as it
// was at startup (the upstream manager does), and a re-registration changes
// the client id under it: a token request with the old id is answered
// invalid_grant and the grant would be deleted (F07 R-02).
func (s *Service) current(ctx context.Context, org store.OrgID, srv store.Server) (store.Server, error) {
	cur, err := s.Store.ServerByID(ctx, org, srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Server{}, ErrNotConnected
	}
	return cur, err
}

func (s *Service) doRefresh(ctx context.Context, org store.OrgID, srv store.Server, userID, stale string) (string, error) {
	srv, err := s.current(ctx, org, srv)
	if err != nil {
		return "", err
	}
	if !srv.Enabled {
		return "", ErrServerOff
	}
	g, err := s.Store.Grant(ctx, org, srv.ID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNotConnected
	}
	if err != nil {
		return "", err
	}
	now := s.now()
	key := string(org) + "\x00" + srv.ID + "\x00" + userID
	stored := g.Credential.RowVersion
	// A pair an earlier refresh could not save is newer than the stored one:
	// save it now, and use it either way (F07 R-06).
	s.mu.Lock()
	p, held := s.unsaved[key]
	s.mu.Unlock()
	if held {
		// The same credential at the same version: a reconnection makes a new
		// credential, whose version may well be the same number.
		if p.from == stored && p.c.ID == g.Credential.ID {
			switch err := s.saveToken(ctx, org, srv.ID, userID, stored, p.c, now); {
			case err == nil:
				s.forgetUnsaved(key)
				stored = p.c.RowVersion
				g.Credential = p.c
			case errors.Is(err, store.ErrNotFound):
				// The grant went meanwhile (removed, disconnected, or the
				// server's connections ended): nothing of it is used.
				s.forgetUnsaved(key)
				return "", ErrNotConnected
			case errors.Is(err, store.ErrConflict):
				s.forgetUnsaved(key) // another refresh saved a newer pair: theirs is read below
				if g, err = s.Store.Grant(ctx, org, srv.ID, userID); err != nil {
					return "", ErrNotConnected
				}
				stored = g.Credential.RowVersion
			default:
				g.Credential = p.c // still unsaved: use it, save it next time
			}
		} else {
			s.forgetUnsaved(key) // the person connected again meanwhile
		}
	}
	tok, err := s.token(ctx, org, g)
	if err != nil {
		return "", err
	}
	if tok.AccessToken != stale && !tok.Expired(now, Skew) {
		return tok.AccessToken, nil // refreshed meanwhile
	}
	if srv.Config.Auth == store.AuthKey || g.Credential.Purpose == store.PurposeKeyGrant {
		// A key cannot be refreshed. The person saved a new one meanwhile, or
		// the server refused the one there is.
		return "", ErrKeyRefused
	}
	e, err := s.Endpoint(ctx, org, srv)
	if err != nil {
		return "", err
	}
	next, err := s.Auth.Refresh(ctx, e, tok.RefreshToken, now)
	if errors.Is(err, upstreamauth.ErrInvalidGrant) {
		ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: "system", Server: srv.Alias,
			Action: "upstream.grant_lost:" + srv.Alias, Decision: "allow", Outcome: "error"}
		if derr := s.Store.DeleteGrant(ctx, org, srv.ID, userID, ev); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			s.logger().Error("deleting a refused grant failed", "server", srv.Alias, "err", derr)
		}
		s.Diagnostics.Diagnose(org, store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryGrant, Server: srv.Alias, UserID: userID,
			Message: "the authorization server refused a member's grant; they must connect again"})
		return "", ErrNotConnected
	}
	if err != nil {
		// The error can name the token endpoint; the log entry is a fixed text.
		s.Diagnostics.Diagnose(org, store.Diagnostic{Level: store.LevelWarn, Category: store.CategoryGrant, Server: srv.Alias, UserID: userID,
			Message: "refreshing a member's grant failed"})
		return "", fmt.Errorf("refreshing the %s grant: %w", srv.Alias, err)
	}
	if next.Scope == "" {
		next.Scope = tok.Scope
	}
	b, err := json.Marshal(next) //nolint:gosec // the tokens are marshalled only to be sealed in the vault
	if err != nil {
		return "", err
	}
	c := g.Credential
	c.RowVersion = stored + 1
	c, err = s.seal(ctx, org, c, b)
	if err != nil {
		return "", err
	}
	switch err := s.saveToken(ctx, org, srv.ID, userID, stored, c, now); {
	case err == nil:
		s.forgetUnsaved(key)
		return next.AccessToken, nil
	case errors.Is(err, store.ErrNotFound):
		return "", ErrNotConnected
	case errors.Is(err, store.ErrConflict):
		// Another replica refreshed (or the person reconnected) first: use theirs.
		g, err := s.Store.Grant(ctx, org, srv.ID, userID)
		if err != nil {
			return "", ErrNotConnected
		}
		t, err := s.token(ctx, org, g)
		if err != nil || t.Expired(now, 0) {
			return "", errors.New("refreshing the grant raced with another refresh")
		}
		return t.AccessToken, nil
	default:
		// The provider may have rotated the refresh token: the pair is kept
		// and saved on the next refresh, and this call goes on with it.
		s.mu.Lock()
		if s.unsaved == nil {
			s.unsaved = map[string]unsavedToken{}
		}
		s.unsaved[key] = unsavedToken{from: stored, c: c}
		s.mu.Unlock()
		s.logger().Error("saving a refreshed grant failed; it is kept and saved on the next refresh", "server", srv.Alias, "err", err)
		return next.AccessToken, nil
	}
}

// saveToken saves a refreshed credential, retrying a few times: the tokens in
// it are already issued and may be the only valid ones (F07 R-06). A lost
// race or a deleted grant is not retried.
func (s *Service) saveToken(ctx context.Context, org store.OrgID, serverID, userID string, from uint64, c store.Credential, now time.Time) error {
	wait := saveBackoff
	var err error
	for range saveAttempts {
		err = s.Store.UpdateGrantToken(ctx, org, serverID, userID, from, c, now)
		if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return err
		}
		wait *= 2
	}
	return err
}

func (s *Service) forgetUnsaved(key string) {
	s.mu.Lock()
	delete(s.unsaved, key)
	s.mu.Unlock()
}

// Disconnect revokes the person's grant at the authorization server when it
// offers revocation (best effort) and deletes it with the audit record ev.
func (s *Service) Disconnect(ctx context.Context, org store.OrgID, srv store.Server, userID string, ev store.AuditEvent) error {
	s.forgetUnsaved(string(org) + "\x00" + srv.ID + "\x00" + userID)
	g, err := s.Store.Grant(ctx, org, srv.ID, userID)
	if err != nil {
		return err
	}
	if cur, err := s.current(ctx, org, srv); err == nil {
		srv = cur
	}
	if tok, err := s.token(ctx, org, g); err == nil {
		if e, err := s.Endpoint(ctx, org, srv); err == nil {
			t, hint := tok.RefreshToken, "refresh_token"
			if t == "" {
				t, hint = tok.AccessToken, "access_token"
			}
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := s.Auth.Revoke(rctx, e, t, hint); err != nil {
				s.logger().Warn("revoking an upstream grant failed; deleting it anyway", "server", srv.Alias, "err", err)
			}
			cancel()
		}
	}
	return s.Store.DeleteGrant(ctx, org, srv.ID, userID, ev)
}

func (s *Service) token(ctx context.Context, org store.OrgID, g store.Grant) (upstreamauth.Token, error) {
	pt, err := s.open(ctx, org, g.Credential)
	if err != nil {
		return upstreamauth.Token{}, fmt.Errorf("grants: opening the grant: %w", err)
	}
	var t upstreamauth.Token
	if err := json.Unmarshal(pt, &t); err != nil || t.AccessToken == "" {
		return upstreamauth.Token{}, errors.New("grants: the stored grant is unreadable")
	}
	return t, nil
}

func binding(org store.OrgID, c store.Credential) vault.Binding {
	return vault.Binding{OrgID: string(org), CredentialID: c.ID, OwnerID: c.OwnerID, ServerID: c.ServerID,
		Purpose: c.Purpose, KeyVersion: c.KeyVersion, RowVersion: c.RowVersion}
}

func (s *Service) open(ctx context.Context, org store.OrgID, c store.Credential) ([]byte, error) {
	dek, err := s.Keys.DEK(ctx, org, c.KeyVersion)
	if err != nil {
		return nil, err
	}
	return vault.Open(dek, c.Ciphertext, binding(org, c))
}

func (s *Service) seal(ctx context.Context, org store.OrgID, c store.Credential, plaintext []byte) (store.Credential, error) {
	version, dek, err := s.Keys.Current(ctx, org)
	if err != nil {
		return store.Credential{}, err
	}
	c.KeyVersion = version
	c.Ciphertext, err = vault.Seal(dek, plaintext, binding(org, c))
	return c, err
}
