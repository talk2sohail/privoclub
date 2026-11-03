package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/store"
)

// The admin pages (ADR 0018): plain server-rendered forms over the admin API,
// called in process as the signed-in person. Every rule, check and audit
// record is the API's own; these handlers only render and translate forms.

// API is the in-process admin API (control.API.Serve).
type API interface {
	Serve(ctx context.Context, p auth.Principal, method, path string, body any) (int, []byte)
}

var (
	personRe = regexp.MustCompile(`^[^\x00-\x1f\x7f/]{1,128}$`)
	idRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// uiFlashes are the only messages a result parameter can select.
var uiFlashes = map[string]flash{
	"added":       {"ok", "Server added."},
	"accepted":    {"ok", "Accepted. Those tools are now available to AI clients."},
	"enabled":     {"ok", "Server turned on."},
	"disabled":    {"ok", "Server turned off."},
	"deleted":     {"ok", "Server deleted."},
	"removed":     {"ok", "Removed."},
	"rule-added":  {"ok", "Rule added."},
	"rule-gone":   {"ok", "Rule deleted."},
	"default-set": {"ok", "Default saved."},
	"role-set":    {"ok", "Role changed."},
	"revoked":     {"ok", "Token revoked. It stops working within seconds."},
}

func (s *Server) uiEnabled() bool { return s.o.API != nil }

// apiError is the API's own message for a refused request.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// api calls the admin API as u and decodes the answer into out (if not nil).
func (s *Server) api(ctx context.Context, u store.User, method, path string, body, out any, want int) error {
	code, raw := s.o.API.Serve(ctx, s.principal(u), method, path, body)
	if code != want {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = http.StatusText(code)
		}
		return &apiError{status: code, msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// can reports whether a signed-in person's role allows perm.
func (s *Server) can(u store.User, perm auth.Permission) bool { return s.principal(u).Can(perm) }

// pageAuth gets the session and checks the permission for a page.
func (s *Server) pageAuth(w http.ResponseWriter, r *http.Request, perm auth.Permission) (store.Session, store.User, bool) {
	sess, u, ok := s.session(r)
	if !ok {
		redirect(w, "/admin/signin")
		return sess, u, false
	}
	if !s.can(u, perm) {
		s.pageFor(w, http.StatusForbidden, sess, u, "Not allowed", "Your role cannot use this page.")
		return sess, u, false
	}
	return sess, u, true
}

// formAuth is pageAuth for a form post: origin, CSRF, session, permission.
func (s *Server) formAuth(w http.ResponseWriter, r *http.Request, perm auth.Permission) (store.Session, store.User, bool) {
	sess, u, ok := s.guarded(w, r)
	if !ok {
		return sess, u, false
	}
	if !s.can(u, perm) {
		s.pageFor(w, http.StatusForbidden, sess, u, "Not allowed", "Your role cannot do this.")
		return sess, u, false
	}
	return sess, u, true
}

// failure renders a page builder's result with an error message.
func errFlash(err error) *flash {
	var ae *apiError
	if errors.As(err, &ae) {
		return &flash{Class: "warn", Text: ae.msg}
	}
	return &flash{Class: "warn", Text: "Something went wrong on the server. Try again."}
}

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) && ae.status >= 400 && ae.status < 500 {
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

func resultFlash(r *http.Request) *flash {
	if f, ok := uiFlashes[r.URL.Query().Get("result")]; ok {
		return &f
	}
	return nil
}

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// ---- servers

type serverRow struct {
	Alias, Transport, Status, Error, Target, Auth, ConnectURL string
	Enabled                                                   bool
	Tools, Prompts, Resources, Pending                        int
}

type apiServer struct {
	Alias      string         `json:"alias"`
	Transport  string         `json:"transport"`
	Enabled    bool           `json:"enabled"`
	Status     string         `json:"status"`
	Error      string         `json:"error"`
	Target     string         `json:"target"`
	Items      map[string]int `json:"items"`
	Pending    int            `json:"pending_review"`
	Auth       string         `json:"auth"`
	ConnectURL string         `json:"connect_url"`
}

func (a apiServer) row() serverRow {
	return serverRow{Alias: a.Alias, Transport: a.Transport, Status: a.Status, Error: a.Error, Target: a.Target, Auth: a.Auth, ConnectURL: a.ConnectURL,
		Enabled: a.Enabled, Tools: a.Items["tool"], Prompts: a.Items["prompt"], Resources: a.Items["resource"], Pending: a.Pending}
}

func (s *Server) listServers(ctx context.Context, u store.User) ([]serverRow, error) {
	var res struct {
		Servers []apiServer `json:"servers"`
	}
	if err := s.api(ctx, u, http.MethodGet, "/api/v1/servers", nil, &res, http.StatusOK); err != nil {
		return nil, err
	}
	out := make([]serverRow, 0, len(res.Servers))
	for _, a := range res.Servers {
		out = append(out, a.row())
	}
	return out, nil
}

func (s *Server) serversPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.pageAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	s.renderServers(w, r, sess, u, http.StatusOK, resultFlash(r), nil)
}

func (s *Server) renderServers(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, status int, f *flash, form url.Values) {
	list, err := s.listServers(r.Context(), u)
	if err != nil {
		f, status = errFlash(err), statusOf(err)
	}
	if form == nil {
		form = url.Values{"transport": {"http"}, "auth": {"none"}}
	}
	s.render(w, status, "servers", pageData{Title: "Servers", User: view(u), CSRF: sess.CSRF, Flash: f,
		Page: map[string]any{"Servers": list, "Form": formValues(form), "CanOAuth": s.connections()}})
}

func formValues(v url.Values) map[string]string {
	out := map[string]string{}
	for k := range v {
		if k != "csrf" && !strings.Contains(k, "secret") && k != "header_value" {
			out[k] = v.Get(k) // secrets are never echoed back into a page
		}
	}
	return out
}

func (s *Server) addServer(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	f := r.PostForm
	alias := strings.TrimSpace(f.Get("alias"))
	body := map[string]any{"alias": alias, "transport": f.Get("transport"), "url": strings.TrimSpace(f.Get("url"))}
	switch f.Get("auth") {
	case "header":
		name := strings.TrimSpace(f.Get("header_name"))
		if name == "" {
			name = "Authorization"
		}
		body["headers"] = map[string]string{name: f.Get("header_value")}
	case "oauth":
		o := map[string]any{}
		if id := strings.TrimSpace(f.Get("client_id")); id != "" {
			o["client_id"], o["client_secret"] = id, strings.TrimSpace(f.Get("client_secret"))
		}
		body["oauth"] = o
	}
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/servers", body, nil, http.StatusCreated); err != nil {
		s.renderServers(w, r, sess, u, statusOf(err), errFlash(err), f)
		return
	}
	redirect(w, "/admin/servers/"+url.PathEscape(alias)+"?result=added")
}

type pinRow struct {
	Kind, Name, Hash, Description, Status string
}

type connRow struct {
	Person, Scopes, Connected, LastUsed string
	Catalog                             bool
}

func (s *Server) serverPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.pageAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	s.renderServer(w, r, sess, u, http.StatusOK, resultFlash(r))
}

func (s *Server) renderServer(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, status int, f *flash) {
	alias := r.PathValue("alias")
	list, err := s.listServers(r.Context(), u)
	if err != nil {
		s.pageFor(w, statusOf(err), sess, u, "Something went wrong", errFlash(err).Text)
		return
	}
	var srv *serverRow
	for i := range list {
		if list[i].Alias == alias {
			srv = &list[i]
		}
	}
	if srv == nil {
		s.pageFor(w, http.StatusNotFound, sess, u, "No such server", "There is no server with that name.")
		return
	}
	var pins struct {
		Pins []struct {
			Kind       string          `json:"kind"`
			Name       string          `json:"name"`
			Hash       string          `json:"hash"`
			Status     string          `json:"status"`
			Definition json.RawMessage `json:"definition"`
		} `json:"pins"`
	}
	var pending, accepted []pinRow
	if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/servers/"+url.PathEscape(alias)+"/pins", nil, &pins, http.StatusOK); err == nil {
		for _, p := range pins.Pins {
			var def struct {
				Description string `json:"description"`
				Title       string `json:"title"`
			}
			_ = json.Unmarshal(p.Definition, &def)
			row := pinRow{Kind: p.Kind, Name: p.Name, Hash: p.Hash, Description: firstLine(def.Description, 300), Status: p.Status}
			switch p.Status {
			case "pending":
				pending = append(pending, row)
			case "accepted":
				accepted = append(accepted, row)
			}
		}
	}
	var conns []connRow
	if srv.Auth == store.AuthOAuth {
		var res struct {
			Connections []struct {
				Person      string    `json:"person"`
				Scopes      string    `json:"scopes"`
				Catalog     bool      `json:"catalog"`
				ConnectedAt time.Time `json:"connected_at"`
				LastUsedAt  time.Time `json:"last_used_at"`
			} `json:"connections"`
		}
		if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/servers/"+url.PathEscape(alias)+"/connections", nil, &res, http.StatusOK); err == nil {
			for _, c := range res.Connections {
				conns = append(conns, connRow{Person: c.Person, Scopes: c.Scopes, Catalog: c.Catalog, Connected: when(c.ConnectedAt), LastUsed: when(c.LastUsedAt)})
			}
		}
	}
	s.render(w, status, "server", pageData{Title: srv.Alias, User: view(u), CSRF: sess.CSRF, Flash: f,
		Page: map[string]any{"S": srv, "Pending": pending, "Accepted": accepted, "Connections": conns}})
}

// firstLine shortens upstream text for a table cell. It stays text: the
// template escapes it.
func firstLine(s string, limit int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

func (s *Server) acceptTools(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	var pins []map[string]string
	for _, v := range r.PostForm["pin"] {
		parts := strings.SplitN(v, "|", 3)
		if len(parts) == 3 {
			pins = append(pins, map[string]string{"kind": parts[0], "hash": parts[1], "name": parts[2]})
		}
	}
	alias := r.PathValue("alias")
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/servers/"+url.PathEscape(alias)+"/pins:accept", map[string]any{"pins": pins}, nil, http.StatusOK); err != nil {
		s.renderServer(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/servers/"+url.PathEscape(alias)+"?result=accepted")
}

func (s *Server) setServerEnabled(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	on := r.PostForm.Get("enabled") == "true"
	alias := r.PathValue("alias")
	if err := s.api(r.Context(), u, http.MethodPatch, "/api/v1/servers/"+url.PathEscape(alias), map[string]any{"enabled": on}, nil, http.StatusOK); err != nil {
		s.renderServer(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	result := "disabled"
	if on {
		result = "enabled"
	}
	redirect(w, "/admin/servers/"+url.PathEscape(alias)+"?result="+result)
}

func (s *Server) deleteServer(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	alias := r.PathValue("alias")
	if r.PostForm.Get("confirm") != alias {
		s.renderServer(w, r, sess, u, http.StatusUnprocessableEntity, &flash{Class: "warn", Text: "To delete the server, type its name in the box."})
		return
	}
	if err := s.api(r.Context(), u, http.MethodDelete, "/api/v1/servers/"+url.PathEscape(alias), nil, nil, http.StatusOK); err != nil {
		s.renderServer(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/servers?result=deleted")
}

func (s *Server) removeConnection(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageServers)
	if !ok {
		return
	}
	alias, person := r.PathValue("alias"), r.PathValue("person")
	if err := s.api(r.Context(), u, http.MethodDelete, "/api/v1/servers/"+url.PathEscape(alias)+"/connections/"+url.PathEscape(person), nil, nil, http.StatusOK); err != nil {
		s.renderServer(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/servers/"+url.PathEscape(alias)+"?result=removed")
}

// ---- rules

type ruleRow struct {
	ID, Server, Tool, Subject, Action, Note, CreatedBy, Expires string
}

func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.pageAuth(w, r, auth.PermManagePolicy)
	if !ok {
		return
	}
	s.renderRules(w, r, sess, u, http.StatusOK, resultFlash(r))
}

func (s *Server) renderRules(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, status int, f *flash) {
	var res struct {
		Policies []struct {
			ID        string     `json:"id"`
			Server    string     `json:"server"`
			Tool      string     `json:"tool"`
			Subject   string     `json:"subject"`
			Action    string     `json:"action"`
			Note      string     `json:"note"`
			CreatedBy string     `json:"created_by"`
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"policies"`
		Default string `json:"default"`
	}
	if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/policies", nil, &res, http.StatusOK); err != nil {
		f, status = errFlash(err), statusOf(err)
	}
	rows := make([]ruleRow, 0, len(res.Policies))
	for _, p := range res.Policies {
		row := ruleRow{ID: p.ID, Server: p.Server, Tool: p.Tool, Subject: subjectLabel(p.Subject), Action: p.Action, Note: p.Note, CreatedBy: p.CreatedBy}
		if p.ExpiresAt != nil {
			row.Expires = when(*p.ExpiresAt)
		}
		rows = append(rows, row)
	}
	servers, _ := s.listServers(r.Context(), u)
	s.render(w, status, "rules", pageData{Title: "Rules", User: view(u), CSRF: sess.CSRF, Flash: f,
		Page: map[string]any{"Rules": rows, "Default": res.Default, "Servers": servers}})
}

func subjectLabel(sub string) string {
	switch {
	case sub == "*":
		return "everyone"
	case strings.HasPrefix(sub, "role:"):
		return strings.TrimPrefix(sub, "role:") + "s"
	case strings.HasPrefix(sub, "token:"):
		return "token " + strings.TrimPrefix(sub, "token:")
	}
	return sub
}

func (s *Server) addRule(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManagePolicy)
	if !ok {
		return
	}
	f := r.PostForm
	tool := strings.TrimSpace(f.Get("tool"))
	if tool == "" {
		tool = "*"
	}
	body := map[string]any{"server": f.Get("server"), "tool": tool, "subject": f.Get("subject"), "action": f.Get("action"), "note": strings.TrimSpace(f.Get("note"))}
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/policies", body, nil, http.StatusCreated); err != nil {
		s.renderRules(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/rules?result=rule-added")
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManagePolicy)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		redirect(w, "/admin/rules")
		return
	}
	if err := s.api(r.Context(), u, http.MethodDelete, "/api/v1/policies/"+id, nil, nil, http.StatusOK); err != nil {
		s.renderRules(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/rules?result=rule-gone")
}

func (s *Server) setDefaultRule(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManagePolicy)
	if !ok {
		return
	}
	if err := s.api(r.Context(), u, http.MethodPatch, "/api/v1/settings/policy", map[string]any{"default": r.PostForm.Get("default")}, nil, http.StatusOK); err != nil {
		s.renderRules(w, r, sess, u, statusOf(err), errFlash(err))
		return
	}
	redirect(w, "/admin/rules?result=default-set")
}

// ---- people

type memberRow struct {
	Person, Name, Role, Login, Joined string
	Removed, Me                       bool
}

func (s *Server) peoplePage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.pageAuth(w, r, auth.PermManageMembers)
	if !ok {
		return
	}
	s.renderPeople(w, r, sess, u, http.StatusOK, resultFlash(r), nil)
}

func (s *Server) renderPeople(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, status int, f *flash, extra map[string]any) {
	var res struct {
		Members []struct {
			Person    string    `json:"person"`
			Name      string    `json:"name"`
			Role      string    `json:"role"`
			Login     string    `json:"github_login"`
			CreatedAt time.Time `json:"created_at"`
			Removed   bool      `json:"removed"`
		} `json:"members"`
	}
	if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/members", nil, &res, http.StatusOK); err != nil {
		f, status = errFlash(err), statusOf(err)
	}
	rows := make([]memberRow, 0, len(res.Members))
	for _, m := range res.Members {
		rows = append(rows, memberRow{Person: m.Person, Name: m.Name, Role: m.Role, Login: m.Login, Joined: when(m.CreatedAt), Removed: m.Removed, Me: m.Person == u.Person})
	}
	page := map[string]any{"Members": rows, "Roles": inviteRoles(u), "AllRoles": changeRoles(u)}
	for k, v := range extra {
		page[k] = v
	}
	s.render(w, status, "people", pageData{Title: "People", User: view(u), CSRF: sess.CSRF, Flash: f, Page: page})
}

// inviteRoles are the roles u may invite with; changeRoles the ones u may set.
func inviteRoles(u store.User) []string {
	if u.Role == store.RoleOwner {
		return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor, store.RoleAdmin}
	}
	return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor}
}

func changeRoles(u store.User) []string {
	if u.Role == store.RoleOwner {
		return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor, store.RoleAdmin, store.RoleOwner}
	}
	return []string{store.RoleMember, store.RoleApprover, store.RoleAuditor}
}

func (s *Server) invitePerson(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageMembers)
	if !ok {
		return
	}
	var res struct {
		Invite struct {
			Person    string    `json:"person"`
			Role      string    `json:"role"`
			URL       string    `json:"url"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"invite"`
	}
	body := map[string]any{"person": strings.TrimSpace(r.PostForm.Get("person")), "role": r.PostForm.Get("role")}
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/invites", body, &res, http.StatusCreated); err != nil {
		s.renderPeople(w, r, sess, u, statusOf(err), errFlash(err), nil)
		return
	}
	// The link is shown once, on this answer only (no redirect, no-store).
	s.renderPeople(w, r, sess, u, http.StatusOK, &flash{Class: "ok", Text: "Invite created. Send the link below to " + res.Invite.Person + "."},
		map[string]any{"Invite": map[string]string{"Person": res.Invite.Person, "Role": res.Invite.Role, "URL": res.Invite.URL, "Expires": when(res.Invite.ExpiresAt)}})
}

func (s *Server) setRole(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageMembers)
	if !ok {
		return
	}
	person := r.PathValue("person")
	if !personRe.MatchString(person) {
		redirect(w, "/admin/people")
		return
	}
	if err := s.api(r.Context(), u, http.MethodPatch, "/api/v1/members/"+url.PathEscape(person), map[string]any{"role": r.PostForm.Get("role")}, nil, http.StatusOK); err != nil {
		s.renderPeople(w, r, sess, u, statusOf(err), errFlash(err), nil)
		return
	}
	redirect(w, "/admin/people?result=role-set")
}

func (s *Server) removePerson(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageMembers)
	if !ok {
		return
	}
	person := r.PathValue("person")
	if !personRe.MatchString(person) {
		redirect(w, "/admin/people")
		return
	}
	if r.PostForm.Get("confirm") != "yes" {
		s.renderPeople(w, r, sess, u, http.StatusUnprocessableEntity, &flash{Class: "warn", Text: "Tick the box to confirm removing " + person + "."}, nil)
		return
	}
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/members/"+url.PathEscape(person)+"/remove", nil, nil, http.StatusOK); err != nil {
		s.renderPeople(w, r, sess, u, statusOf(err), errFlash(err), nil)
		return
	}
	redirect(w, "/admin/people?result=removed")
}

// ---- tokens

type tokenRow struct {
	ID, Name, Kind, Role, Person, Created, Expires string
}

type apiToken struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Person    string    `json:"person"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (t apiToken) row() tokenRow {
	return tokenRow{ID: t.ID, Name: t.Name, Kind: t.Kind, Role: t.Role, Person: t.Person, Created: when(t.CreatedAt), Expires: when(t.ExpiresAt)}
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.session(r)
	if !ok {
		redirect(w, "/admin/signin")
		return
	}
	if !s.can(u, auth.PermUseGateway) && !s.can(u, auth.PermManageTokens) {
		s.pageFor(w, http.StatusForbidden, sess, u, "Not allowed", "Your role does not use tokens.")
		return
	}
	s.renderTokens(w, r, sess, u, http.StatusOK, resultFlash(r), nil)
}

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, status int, f *flash, created *apiToken) {
	page := map[string]any{"MCPURL": s.origin + "/mcp", "UsesGateway": s.can(u, auth.PermUseGateway), "ManagesTokens": s.can(u, auth.PermManageTokens),
		"Roles": []string{store.RoleOwner, store.RoleAdmin, store.RoleAuditor, store.RoleApprover}}
	if s.can(u, auth.PermUseGateway) {
		var mine struct {
			Tokens []apiToken `json:"tokens"`
		}
		if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/me/tokens", nil, &mine, http.StatusOK); err == nil {
			rows := []tokenRow{}
			for _, t := range mine.Tokens {
				rows = append(rows, t.row())
			}
			page["Mine"] = rows
		}
	}
	if s.can(u, auth.PermManageTokens) {
		var all struct {
			Tokens []apiToken `json:"tokens"`
		}
		if err := s.api(r.Context(), u, http.MethodGet, "/api/v1/tokens", nil, &all, http.StatusOK); err == nil {
			rows := []tokenRow{}
			for _, t := range all.Tokens {
				rows = append(rows, t.row())
			}
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].Person < rows[j].Person })
			page["All"] = rows
		}
	}
	if created != nil {
		page["Created"] = map[string]string{"Name": created.Name, "Kind": created.Kind, "Token": created.Token, "Expires": when(created.ExpiresAt),
			"Command": fmt.Sprintf(`claude mcp add --transport http privo %s/mcp --header "Authorization: Bearer %s"`, s.origin, created.Token)}
	}
	s.render(w, status, "tokens", pageData{Title: "Tokens", User: view(u), CSRF: sess.CSRF, Flash: f, Page: page})
}

func (s *Server) createMyToken(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermUseGateway)
	if !ok {
		return
	}
	var t apiToken
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": strings.TrimSpace(r.PostForm.Get("name"))}, &t, http.StatusCreated); err != nil {
		s.renderTokens(w, r, sess, u, statusOf(err), errFlash(err), nil)
		return
	}
	s.renderTokens(w, r, sess, u, http.StatusOK, &flash{Class: "ok", Text: "Token created. Copy it now: it is shown only once."}, &t)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.formAuth(w, r, auth.PermManageTokens)
	if !ok {
		return
	}
	f := r.PostForm
	body := map[string]any{"name": strings.TrimSpace(f.Get("name")), "role": f.Get("role"), "kind": "control", "person": strings.TrimSpace(f.Get("person"))}
	var t apiToken
	if err := s.api(r.Context(), u, http.MethodPost, "/api/v1/tokens", body, &t, http.StatusCreated); err != nil {
		s.renderTokens(w, r, sess, u, statusOf(err), errFlash(err), nil)
		return
	}
	s.renderTokens(w, r, sess, u, http.StatusOK, &flash{Class: "ok", Text: "API token created. Copy it now: it is shown only once."}, &t)
}

func (s *Server) revokeToken(mine bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		perm, path := auth.PermManageTokens, "/api/v1/tokens/"
		if mine {
			perm, path = auth.PermUseGateway, "/api/v1/me/tokens/"
		}
		sess, u, ok := s.formAuth(w, r, perm)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if !idRe.MatchString(id) {
			redirect(w, "/admin/tokens")
			return
		}
		if err := s.api(r.Context(), u, http.MethodDelete, path+id, nil, nil, http.StatusOK); err != nil {
			s.renderTokens(w, r, sess, u, statusOf(err), errFlash(err), nil)
			return
		}
		redirect(w, "/admin/tokens?result=revoked")
	}
}

// ---- activity

// shortDecision drops the approval id: "approved:<id>" reads as "approved".
func shortDecision(d string) string {
	if before, _, ok := strings.Cut(d, ":"); ok && before == "approved" {
		return before
	}
	return d
}

// shortApprover drops the id from "name (user <id>)" or "name (token <id>)".
func shortApprover(a string) string {
	if i := strings.Index(a, " ("); i > 0 {
		return a[:i]
	}
	return a
}

type eventRow struct {
	Time, Actor, Server, Item, Action, Decision, Outcome, ApprovedBy string
}

func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	sess, u, ok := s.pageAuth(w, r, auth.PermReadAudit)
	if !ok {
		return
	}
	admin := r.URL.Query().Get("view") == "admin"
	path := "/api/v1/audit/events?limit=100"
	if admin {
		path += "&kind=admin"
	}
	var res struct {
		Events []struct {
			Time       time.Time `json:"time"`
			Actor      string    `json:"actor"`
			Server     string    `json:"server"`
			Item       string    `json:"item"`
			Action     string    `json:"action"`
			Decision   string    `json:"decision"`
			Outcome    string    `json:"outcome"`
			ApprovedBy string    `json:"approved_by"`
		} `json:"events"`
	}
	var f *flash
	status := http.StatusOK
	if err := s.api(r.Context(), u, http.MethodGet, path, nil, &res, http.StatusOK); err != nil {
		f, status = errFlash(err), statusOf(err)
	}
	rows := make([]eventRow, 0, len(res.Events))
	for _, e := range res.Events {
		rows = append(rows, eventRow{Time: when(e.Time), Actor: e.Actor, Server: e.Server, Item: e.Item, Action: e.Action,
			Decision: shortDecision(e.Decision), Outcome: strings.ReplaceAll(e.Outcome, "_", " "), ApprovedBy: shortApprover(e.ApprovedBy)})
	}
	s.render(w, status, "activity", pageData{Title: "Activity", User: view(u), CSRF: sess.CSRF, Flash: f, Page: map[string]any{"Events": rows, "Admin": admin}})
}
