package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// In-process calls for the admin site (ADR 0018). A page calls Serve with the
// signed-in person as principal; the request runs through the same routes,
// handlers, permission checks and audit as a token request. The internal mux
// is never mounted on a server, so it cannot be reached over HTTP.

// KindSession marks a principal that signed in on the admin site.
const KindSession = "session"

type principalKey struct{}

// internal holds the mux Serve dispatches to.
type internal struct {
	once sync.Once
	mux  *http.ServeMux
}

func (a *API) internalMux() *http.ServeMux {
	a.in.once.Do(func() {
		a.in.mux = http.NewServeMux()
		a.register(a.in.mux, a.withSession)
	})
	return a.in.mux
}

// withSession runs a handler as the principal Serve put in the context.
func (a *API) withSession(perm auth.Permission, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(auth.Principal)
		if !ok || p.Kind != KindSession || p.UserID == "" || p.OrgID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
			return
		}
		a.run(perm, h, w, r, p)
	})
}

// Serve runs one admin API request as a signed-in person and returns the
// status and the JSON body. body is marshalled as the request body when not nil.
func (a *API) Serve(ctx context.Context, p auth.Principal, method, path string, body any) (int, []byte) {
	if p.Kind != KindSession || p.UserID == "" {
		return http.StatusUnauthorized, []byte(`{"error":"not signed in"}`)
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return http.StatusBadRequest, []byte(`{"error":"bad request"}`)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r, err := http.NewRequestWithContext(context.WithValue(ctx, principalKey{}, p), method, "http://privo.internal"+path, rd)
	if err != nil {
		return http.StatusBadRequest, []byte(`{"error":"bad request"}`)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	a.internalMux().ServeHTTP(rec, r)
	return rec.status, rec.body.Bytes()
}

type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}

// ---- members and one's own tokens (ADR 0018)

func (a *API) registerMembers(mux *http.ServeMux, wrap func(auth.Permission, handler) http.Handler) {
	mux.Handle("GET /api/v1/members", wrap(auth.PermManageMembers, a.listMembers))
	mux.Handle("PATCH /api/v1/members/{person}", wrap(auth.PermManageMembers, a.patchMember))
	mux.Handle("POST /api/v1/members/{person}/remove", wrap(auth.PermManageMembers, a.removeMember))
	mux.Handle("GET /api/v1/me/tokens", wrap(auth.PermUseGateway, a.listMyTokens))
	mux.Handle("POST /api/v1/me/tokens", wrap(auth.PermUseGateway, a.createMyToken))
	mux.Handle("DELETE /api/v1/me/tokens/{id}", wrap(auth.PermUseGateway, a.revokeMyToken))
}

type memberView struct {
	Person    string    `json:"person"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Login     string    `json:"github_login"`
	CreatedAt time.Time `json:"created_at"`
	Removed   bool      `json:"removed"`
}

func (a *API) listMembers(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	users, err := a.Store.ListUsers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]memberView, 0, len(users))
	for _, u := range users {
		out = append(out, memberView{Person: u.Person, Name: u.DisplayName, Role: u.Role, Login: u.Login, CreatedAt: u.CreatedAt, Removed: u.DisabledAt != nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
	return nil
}

// member finds the active member named in the path and checks that p may
// change them: not themselves, and an owner or admin only by an owner.
func (a *API) member(r *http.Request, p auth.Principal) (store.User, error) {
	u, err := a.Store.UserByPerson(r.Context(), p.OrgID, r.PathValue("person"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		return u, errorf(http.StatusNotFound, "no member %q", r.PathValue("person"))
	}
	if err != nil {
		return u, err
	}
	if u.Person == p.Person {
		return u, errorf(http.StatusForbidden, "you cannot change your own membership; ask another owner")
	}
	if (u.Role == store.RoleOwner || u.Role == store.RoleAdmin) && p.Role != store.RoleOwner {
		return u, errorf(http.StatusForbidden, "only owners can change owners and admins")
	}
	return u, nil
}

func (a *API) patchMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := a.member(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if !store.ValidRole(req.Role) {
		return errorf(http.StatusUnprocessableEntity, "role must be owner, admin, approver, auditor or member")
	}
	if (req.Role == store.RoleOwner || req.Role == store.RoleAdmin) && p.Role != store.RoleOwner {
		return errorf(http.StatusForbidden, "only owners can make owners and admins")
	}
	if u.Role == store.RoleOwner && req.Role != store.RoleOwner {
		if n, err := a.activeOwners(r.Context(), p.OrgID); err != nil {
			return err
		} else if n <= 1 {
			return errorf(http.StatusConflict, "%q is the last owner", u.Person)
		}
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(),
		Action: "member.role:" + u.Person + ":" + req.Role, Decision: "allow", Outcome: "ok"}
	if err := a.Store.SetUserRole(r.Context(), p.OrgID, u.ID, req.Role, ev); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"person": u.Person, "role": req.Role})
	return nil
}

func (a *API) activeOwners(ctx context.Context, org store.OrgID) (int, error) {
	users, err := a.Store.ListUsers(ctx, org)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if u.Role == store.RoleOwner && u.DisabledAt == nil {
			n++
		}
	}
	return n, nil
}

// removeMember ends a member's access: sessions, upstream grants, every active
// token that belongs to them, and every token of nobody that names them.
func (a *API) removeMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := a.member(r, p)
	if err != nil {
		return err
	}
	if u.Role == store.RoleOwner {
		if n, err := a.activeOwners(r.Context(), p.OrgID); err != nil {
			return err
		} else if n <= 1 {
			return errorf(http.StatusConflict, "%q is the last owner", u.Person)
		}
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(),
		Action: "member.remove:" + u.Person, Decision: "allow", Outcome: "ok"}
	if err := a.Store.DisableUser(r.Context(), p.OrgID, u.ID, time.Now(), ev); err != nil {
		return err
	}
	toks, err := a.Store.ListTokens(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	revoked := 0
	for _, t := range toks {
		if t.UserID == u.ID || (t.UserID == "" && t.Person == u.Person) {
			if err := a.Store.RevokeToken(r.Context(), p.OrgID, t.ID); err == nil {
				revoked++
				a.adminEvent(r.Context(), p, "token.revoke:"+t.ID, "")
			}
		}
	}
	a.Upstream.CloseUser(u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"removed": u.Person, "tokens_revoked": revoked})
	return nil
}

func (a *API) listMyTokens(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	toks, err := a.Store.ListTokens(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := []tokenView{}
	for _, t := range toks {
		if mine(t, p) {
			out = append(out, viewToken(t))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

// mine reports whether a gateway token is p's to list and revoke: one that
// belongs to them, or a token of nobody that names them (made before F27, or
// by an administrator: they may always take a token with their name away).
// A token that belongs to another member is never theirs, whatever it names.
func mine(t store.APIToken, p auth.Principal) bool {
	if t.Kind != store.KindGateway {
		return false
	}
	if t.UserID != "" {
		return t.UserID == p.UserID
	}
	return p.Person != "" && t.Person == p.Person
}

// createMyToken gives a signed-in member a gateway token of their own for an
// AI client: their role, their name, and their user id, which is what makes
// calls with it use their teams and their accounts (F27). A caller that is no
// member (an API token, whatever person it names) cannot make one.
func (a *API) createMyToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if p.UserID == "" || p.Person == "" {
		return errorf(http.StatusForbidden, "sign in to Privo to make a token of your own; an API token cannot make one")
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	plain, rec, err := IssueToken(r.Context(), a.Store, p.OrgID, TokenSpec{Name: name, Role: p.Role, Kind: store.KindGateway, Person: p.Person, UserID: p.UserID, TTL: a.TokenTTL})
	if err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "token.create:gateway:"+rec.Role+":"+rec.Name, "")
	writeJSON(w, http.StatusCreated, map[string]any{"id": rec.ID, "token": plain, "name": rec.Name, "role": rec.Role, "kind": rec.Kind,
		"person": rec.Person, "bound": true, "expires_at": rec.ExpiresAt})
	return nil
}

func (a *API) revokeMyToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	t, err := a.Store.TokenByID(r.Context(), id)
	if err != nil || t.OrgID != p.OrgID || t.RevokedAt != nil || !mine(t, p) {
		return errorf(http.StatusNotFound, "no active token %q of yours", id)
	}
	if err := a.Store.RevokeToken(r.Context(), p.OrgID, id); err != nil {
		return errorf(http.StatusNotFound, "no active token %q of yours", id)
	}
	a.adminEvent(r.Context(), p, "token.revoke:"+id, "")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	return nil
}
