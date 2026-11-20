package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
	Roles     []string  `json:"roles"`
	Role      string    `json:"role"` // the strongest of Roles; kept for scripts for one release (ADR 0021)
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
		out = append(out, memberView{Person: u.Person, Name: u.DisplayName, Roles: u.Roles, Role: primaryRole(u.Roles), Login: u.Login, CreatedAt: u.CreatedAt, Removed: u.DisabledAt != nil})
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
	if elevated(u.Roles) && !p.Has(store.RoleOwner) {
		return u, errorf(http.StatusForbidden, "only owners can change owners and admins")
	}
	return u, nil
}

// elevated reports whether roles hold owner or admin, which only owners give
// or take (ADR 0021).
func elevated(roles []string) bool {
	return slices.Contains(roles, store.RoleOwner) || slices.Contains(roles, store.RoleAdmin)
}

// primaryRole is the strongest of roles, for the deprecated single "role" field.
func primaryRole(roles []string) string {
	for _, r := range []string{store.RoleOwner, store.RoleAdmin, store.RoleApprover, store.RoleAuditor, store.RoleMember} {
		if slices.Contains(roles, r) {
			return r
		}
	}
	return ""
}

// requestRoles reads a role set from a request: "roles", or the single "role"
// that scripts sent before ADR 0021, or def when neither is given.
func requestRoles(roles []string, role string, def ...string) ([]string, error) {
	if roles == nil && role != "" {
		roles = []string{role}
	}
	if roles == nil {
		roles = def
	}
	out, err := store.NormalizeRoles(roles)
	if err != nil {
		return nil, errorf(http.StatusUnprocessableEntity, "roles must be owner alone, admin alone, or any of member, approver and auditor")
	}
	return out, nil
}

func (a *API) patchMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := a.member(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Roles []string `json:"roles"`
		Role  string   `json:"role"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	roles, err := requestRoles(req.Roles, req.Role)
	if err != nil {
		return err
	}
	if elevated(roles) && !p.Has(store.RoleOwner) {
		return errorf(http.StatusForbidden, "only owners can make owners and admins")
	}
	if u.Has(store.RoleOwner) && !slices.Contains(roles, store.RoleOwner) {
		if n, err := a.activeOwners(r.Context(), p.OrgID); err != nil {
			return err
		} else if n <= 1 {
			return errorf(http.StatusConflict, "%q is the last owner", u.Person)
		}
	}
	// One event with the set before and after (ADR 0021).
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(),
		Action: "member.roles:" + u.Person + ":" + strings.Join(u.Roles, "+") + "->" + strings.Join(roles, "+"), Decision: "allow", Outcome: "ok"}
	roles, err = a.Store.SetUserRoles(r.Context(), p.OrgID, u.ID, roles, ev)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"person": u.Person, "roles": roles, "role": primaryRole(roles)})
	return nil
}

func (a *API) activeOwners(ctx context.Context, org store.OrgID) (int, error) {
	users, err := a.Store.ListUsers(ctx, org)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if u.Has(store.RoleOwner) && u.DisabledAt == nil {
			n++
		}
	}
	return n, nil
}

// removeMember ends a member's access: sessions, upstream grants, and every
// active token that carries their person.
func (a *API) removeMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	u, err := a.member(r, p)
	if err != nil {
		return err
	}
	if u.Has(store.RoleOwner) {
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
		// Bound tokens, and tokens made before F07 R-01 that carry the person.
		if t.UserID == u.ID || t.Person == u.Person {
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

// errNotSignedIn answers the "own token" routes for a principal with no member
// behind it: a control token, whatever person it names (F07 R-01).
var errNotSignedIn = errorf(http.StatusForbidden, "your own tokens are made on the Tokens page while signed in; this token belongs to no member")

func (a *API) listMyTokens(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if p.UserID == "" {
		return errNotSignedIn
	}
	toks, err := a.Store.ListTokens(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := []tokenView{}
	for _, t := range toks {
		if t.UserID == p.UserID && t.Kind == store.KindGateway {
			out = append(out, viewToken(t))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

// createMyToken gives a signed-in person a gateway token of their own for an
// AI client: bound to them (F07 R-01), with their person. It carries the
// member role whatever roles the person holds: it only uses the gateway, and
// rules read the person's current roles (ADR 0021).
func (a *API) createMyToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if p.UserID == "" || p.Person == "" {
		return errNotSignedIn
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	plain, rec, err := IssueToken(r.Context(), a.Store, p.OrgID, TokenSpec{Name: name, Role: store.RoleMember, Kind: store.KindGateway, Person: p.Person, UserID: p.UserID, TTL: a.TokenTTL,
		Event: store.AuditEvent{Actor: p.Name, ActorID: p.ActorID()}})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": rec.ID, "token": plain, "name": rec.Name, "role": rec.Role, "kind": rec.Kind,
		"person": rec.Person, "personal": true, "expires_at": rec.ExpiresAt})
	return nil
}

func (a *API) revokeMyToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	if p.UserID == "" {
		return errNotSignedIn
	}
	id := r.PathValue("id")
	t, err := a.Store.TokenByID(r.Context(), id)
	if err != nil || t.OrgID != p.OrgID || t.RevokedAt != nil || t.UserID != p.UserID || t.Kind != store.KindGateway {
		return errorf(http.StatusNotFound, "no active token %q of yours", id)
	}
	if err := a.Store.RevokeToken(r.Context(), p.OrgID, id); err != nil {
		return errorf(http.StatusNotFound, "no active token %q of yours", id)
	}
	a.adminEvent(r.Context(), p, "token.revoke:"+id, "")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	return nil
}
