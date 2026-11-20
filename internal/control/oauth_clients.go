package control

import (
	"errors"
	"net/http"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/store"
)

// The admin API of Privo's own authorization server (F06, ADR 0022): which
// AI clients are connected, for a member their own and for an administrator
// everyone's; and which clients are accepted at all.
//
// With the authorization server off the lists are empty and say so; nothing
// can be approved.

func (a *API) registerOAuthClients(mux *http.ServeMux, wrap func(auth.Permission, handler) http.Handler) {
	mux.Handle("GET /api/v1/me/clients", wrap(auth.PermUseGateway, a.listMyClients))
	mux.Handle("DELETE /api/v1/me/clients/{id}", wrap(auth.PermUseGateway, a.revokeMyClient))
	mux.Handle("GET /api/v1/oauth/grants", wrap(auth.PermManageTokens, a.listClientGrants))
	mux.Handle("DELETE /api/v1/oauth/grants/{id}", wrap(auth.PermManageTokens, a.revokeClientGrant))
	mux.Handle("GET /api/v1/oauth/clients", wrap(auth.PermManageTokens, a.listOAuthClients))
	mux.Handle("POST /api/v1/oauth/clients", wrap(auth.PermManageTokens, a.approveOAuthClient))
	mux.Handle("POST /api/v1/oauth/clients:remove", wrap(auth.PermManageTokens, a.removeOAuthClient))
}

type clientGrantView struct {
	ID        string    `json:"id"`
	Client    string    `json:"client"`
	ClientID  string    `json:"client_id"`
	Kind      string    `json:"kind"`
	Person    string    `json:"person,omitempty"`
	Name      string    `json:"name,omitempty"`
	Allowed   time.Time `json:"allowed_at"`
	LastUsed  time.Time `json:"last_used_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func viewClientGrant(g store.ClientGrant, withMember bool) clientGrantView {
	v := clientGrantView{ID: g.ID, Client: g.ClientName, ClientID: g.ClientID, Kind: g.ClientKind, Allowed: g.CreatedAt, LastUsed: g.LastUsedAt, ExpiresAt: g.ExpiresAt}
	if v.Client == "" {
		v.Client = g.ClientID
	}
	if withMember {
		v.Person, v.Name = g.Person, g.DisplayName
	}
	return v
}

func (a *API) clientGrants(r *http.Request, p auth.Principal, userID string, withMember bool) ([]clientGrantView, error) {
	out := []clientGrantView{}
	if a.AuthServer == nil {
		return out, nil
	}
	gs, err := a.Store.ListClientGrants(r.Context(), p.OrgID, userID, time.Now())
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		out = append(out, viewClientGrant(g, withMember))
	}
	return out, nil
}

// listMyClients lists the AI clients the signed-in member allowed. A token
// has no clients of its own: only a person allows one.
func (a *API) listMyClients(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	out := []clientGrantView{}
	if p.UserID != "" {
		var err error
		if out, err = a.clientGrants(r, p, p.UserID, false); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out, "enabled": a.AuthServer != nil})
	return nil
}

func (a *API) revokeEvent(p auth.Principal, action string) store.AuditEvent {
	return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(), Action: action, Decision: "allow", Outcome: "ok"}
}

// revokeMyClient takes the access of one of the member's own clients away.
// Another member's client is "not found", as if it did not exist.
func (a *API) revokeMyClient(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	if p.UserID == "" {
		return errorf(http.StatusNotFound, "no connected client %q of yours", id)
	}
	if _, err := a.Store.RevokeClientGrant(r.Context(), p.OrgID, id, p.UserID, a.revokeEvent(p, "oauth.grant.revoke:"+id)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "no connected client %q of yours", id)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	return nil
}

func (a *API) listClientGrants(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	out, err := a.clientGrants(r, p, "", true)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out, "enabled": a.AuthServer != nil})
	return nil
}

func (a *API) revokeClientGrant(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	if _, err := a.Store.RevokeClientGrant(r.Context(), p.OrgID, id, "", a.revokeEvent(p, "oauth.grant.revoke:"+id)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "no connected client %q", id)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	return nil
}

type oauthClientView struct {
	ClientID     string     `json:"client_id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"` // known | approved | registered
	RedirectURIs []string   `json:"redirect_uris,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	AuthorizedAt *time.Time `json:"authorized_at,omitempty"`
	Grants       int        `json:"grants"`
}

// listOAuthClients shows which clients may ask a member for access: the
// policy, Privo's built-in list, and what is accepted by a row.
func (a *API) listOAuthClients(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	out := map[string]any{"enabled": a.AuthServer != nil, "policy": "", "registration": false, "clients": []oauthClientView{}}
	if a.AuthServer == nil || a.Clients == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	list := []oauthClientView{}
	if a.Clients.Policy != oauthserver.PolicyApproved {
		known := a.Clients.Known
		if known == nil {
			known = oauthserver.Known
		}
		for _, k := range known {
			list = append(list, oauthClientView{ClientID: k.URL, Name: k.Name, Kind: store.ClientKnown})
		}
	}
	stored, err := a.Store.ListOAuthClients(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	for _, c := range stored {
		created := c.CreatedAt
		list = append(list, oauthClientView{ClientID: c.ClientID, Name: c.Name, Kind: c.Kind, RedirectURIs: c.RedirectURIs, CreatedAt: &created, AuthorizedAt: c.AuthorizedAt, Grants: c.Grants})
	}
	out["policy"], out["registration"], out["clients"] = a.Clients.Policy, a.AuthServer.Registration(), list
	writeJSON(w, http.StatusOK, out)
	return nil
}

// approveOAuthClient accepts a client by its metadata document. The document
// is fetched and checked now, so what is approved is something that works,
// and the answer shows where it would send people.
func (a *API) approveOAuthClient(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var in struct {
		ClientID string `json:"client_id"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if a.AuthServer == nil || a.Clients == nil {
		return errorf(http.StatusConflict, "signing in with OAuth is not switched on (PRIVO_OAUTH_SERVER)")
	}
	doc, err := a.Clients.Document(r.Context(), in.ClientID)
	if err != nil {
		var na *oauthserver.NotAcceptedError
		if errors.As(err, &na) {
			return errorf(http.StatusBadRequest, "%s", na.Reason)
		}
		return err
	}
	name := doc.Name
	if name == "" {
		name = doc.Host()
	}
	ev := a.revokeEvent(p, "oauth.client.approve:"+doc.ID)
	ev.Client = name
	c := store.OAuthClient{ClientID: doc.ID, Kind: store.ClientApproved, Name: name, CreatedAt: time.Now(), CreatedBy: p.UserID}
	if err := a.Store.SaveOAuthClient(r.Context(), p.OrgID, c, 0, &ev); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errorf(http.StatusConflict, "this client is already accepted")
		}
		return err
	}
	created := c.CreatedAt
	writeJSON(w, http.StatusCreated, oauthClientView{ClientID: c.ClientID, Name: name, Kind: c.Kind, RedirectURIs: doc.RedirectURIs, CreatedAt: &created})
	return nil
}

// removeOAuthClient takes a client's acceptance away and ends every grant it
// has. For a client on the built-in list only the grants end: the list is
// part of Privo, and the policy "approved" is how to switch it off.
func (a *API) removeOAuthClient(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var in struct {
		ClientID string `json:"client_id"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.ClientID == "" {
		return errorf(http.StatusBadRequest, "client_id is required")
	}
	ev := a.revokeEvent(p, "oauth.client.remove:"+in.ClientID)
	ended, err := a.Store.DeleteOAuthClient(r.Context(), p.OrgID, in.ClientID, ev)
	removed := err == nil
	if errors.Is(err, store.ErrNotFound) {
		ended, err = a.Store.EndClientGrantsOfClient(r.Context(), p.OrgID, in.ClientID, ev)
	}
	if err != nil {
		return err
	}
	if !removed && ended == 0 {
		return errorf(http.StatusNotFound, "no such client, and no connection of it")
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "ended": ended})
	return nil
}
