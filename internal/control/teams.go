package control

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/catalog"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
)

// Teams, the catalog and each person's own tools (ADR 0020).

func (a *API) registerTeams(mux *http.ServeMux, wrap func(auth.Permission, handler) http.Handler) {
	mux.Handle("GET /api/v1/teams", wrap(auth.PermManageMembers, a.listTeams))
	mux.Handle("POST /api/v1/teams", wrap(auth.PermManageMembers, a.createTeam))
	mux.Handle("PATCH /api/v1/teams/{slug}", wrap(auth.PermManageMembers, a.patchTeam))
	mux.Handle("DELETE /api/v1/teams/{slug}", wrap(auth.PermManageMembers, a.deleteTeam))
	mux.Handle("POST /api/v1/teams/{slug}/members", wrap(auth.PermManageMembers, a.addTeamMember))
	mux.Handle("DELETE /api/v1/teams/{slug}/members/{person}", wrap(auth.PermManageMembers, a.removeTeamMember))
	mux.Handle("POST /api/v1/teams/{slug}/servers", wrap(auth.PermManageServers, a.addTeamServer))
	mux.Handle("DELETE /api/v1/teams/{slug}/servers/{alias}", wrap(auth.PermManageServers, a.removeTeamServer))
	mux.Handle("GET /api/v1/catalog", wrap(auth.PermManageServers, a.listCatalog))
	mux.Handle("GET /api/v1/me/tools", wrap(auth.PermUseGateway, a.myTools))
}

// event is an admin audit record for p; the store writes it with the change.
func event(p auth.Principal, action, server string) store.AuditEvent {
	return store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(), Server: server,
		Action: action, Decision: "allow", Outcome: "ok"}
}

type teamMemberView struct {
	Person string `json:"person"`
	Name   string `json:"name"`
}

type teamView struct {
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Profile     string           `json:"profile"`
	BuiltIn     bool             `json:"built_in,omitempty"`
	Members     []teamMemberView `json:"members"`
	Servers     []string         `json:"servers"`
}

const everyoneName = "Everyone"

func (a *API) listTeams(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	teams, err := a.Store.ListTeams(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	servers, err := a.Store.ListServers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	st, err := a.Store.ServerTeams(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	everyone := teamView{Slug: store.TeamEveryone, Name: everyoneName, Description: "Every member of the organisation.", BuiltIn: true,
		Members: []teamMemberView{}, Servers: []string{}}
	for _, s := range servers {
		if slices.Contains(st[s.ID], store.TeamEveryone) {
			everyone.Servers = append(everyone.Servers, s.Alias)
		}
	}
	out := []teamView{everyone}
	for _, t := range teams {
		v := teamView{Slug: t.Slug, Name: t.Name, Description: t.Description, Profile: t.Profile, Members: []teamMemberView{}, Servers: []string{}}
		for _, m := range t.Members {
			v.Members = append(v.Members, teamMemberView{Person: m.Person, Name: m.Name})
		}
		v.Servers = append(v.Servers, t.Servers...)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out, "profiles": catalog.Profiles()})
	return nil
}

// plainText reports whether s is one line of at most limit characters that a
// person can read: no control, format, line or paragraph separator,
// private-use or unassigned characters (they hide or reorder text in the app
// and the logs), no more than two combining marks in a row, and, unless empty
// is allowed, at least one letter or digit, so a name cannot look blank
// (F26 review R-07).
func plainText(s string, limit int, empty bool) bool {
	if utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) {
		return false
	}
	visible, marks := false, 0
	for _, r := range s {
		switch {
		case unicode.IsControl(r), unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co), !unicode.IsGraphic(r) && r != ' ':
			return false
		case unicode.Is(unicode.Mn, r):
			if marks++; marks > 2 {
				return false
			}
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !hangulFiller(r) {
				visible = true
			}
		}
		marks = 0
	}
	return visible || (empty && s == "")
}

// hangulFiller reports the Hangul fillers, letters that draw nothing.
func hangulFiller(r rune) bool { return r == 0x115F || r == 0x1160 || r == 0x3164 || r == 0xFFA0 }

// slugFrom makes a team slug out of a name: "Product managers" is
// "product-managers".
func slugFrom(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// nameTaken refuses a team name that reads like another team's, ignoring
// case, or like the built-in team's, so the access picker and a person's
// "Your teams" line cannot mislead (F26 review R-07). except is the team
// being renamed.
func (a *API) nameTaken(ctx context.Context, org store.OrgID, name, except string) error {
	if strings.EqualFold(name, everyoneName) {
		return errorf(http.StatusConflict, "%q is the name of the built-in team of every member", everyoneName)
	}
	teams, err := a.Store.ListTeams(ctx, org)
	if err != nil {
		return err
	}
	for _, t := range teams {
		if t.Slug != except && strings.EqualFold(t.Name, name) {
			return errorf(http.StatusConflict, "a team named %q already exists", t.Name)
		}
	}
	return nil
}

func validProfile(p string) bool {
	if p == "" {
		return true
	}
	if p == catalog.ProfileEveryone {
		return false
	}
	return slices.ContainsFunc(catalog.Profiles(), func(x catalog.Profile) bool { return x.ID == p })
}

func (a *API) createTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Profile     string `json:"profile"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	name, desc := strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if !plainText(name, 64, false) || !plainText(desc, 280, true) {
		return errorf(http.StatusUnprocessableEntity, "a team needs a name of at most 64 characters and a description of at most 280, on one line each")
	}
	slug := req.Slug
	if slug == "" {
		slug = slugFrom(name)
	}
	if slug == store.TeamEveryone {
		return errorf(http.StatusConflict, "%q is the built-in team of every member", store.TeamEveryone)
	}
	if !store.ValidTeamSlug(slug) {
		return errorf(http.StatusUnprocessableEntity, "the team's short name must be 2 to 32 lowercase letters, digits and dashes, starting with a letter")
	}
	if !validProfile(req.Profile) {
		return errorf(http.StatusUnprocessableEntity, "unknown profile %q", req.Profile)
	}
	if err := a.nameTaken(r.Context(), p.OrgID, name, ""); err != nil {
		return err
	}
	t := store.Team{Slug: slug, Name: name, Description: desc, Profile: req.Profile, CreatedAt: time.Now()}
	if err := a.Store.CreateTeam(r.Context(), p.OrgID, t, event(p, "team.create:"+slug, "")); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errorf(http.StatusConflict, "a team named %q already exists", slug)
		}
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"team": teamView{Slug: slug, Name: name, Description: desc, Profile: req.Profile,
		Members: []teamMemberView{}, Servers: []string{}}})
	return nil
}

// team finds the team named in the path; the built-in team is not one that
// can be changed.
func (a *API) team(r *http.Request, p auth.Principal) (store.Team, error) {
	slug := r.PathValue("slug")
	if slug == store.TeamEveryone {
		return store.Team{}, errorf(http.StatusConflict, "%q is built in: every member is in it and it cannot be changed", store.TeamEveryone)
	}
	t, err := a.Store.TeamBySlug(r.Context(), p.OrgID, slug)
	if errors.Is(err, store.ErrNotFound) {
		return t, errorf(http.StatusNotFound, "no team %q", slug)
	}
	return t, err
}

func (a *API) patchTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	t, err := a.team(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Name == nil && req.Description == nil {
		return errorf(http.StatusUnprocessableEntity, "nothing to change")
	}
	name, desc := t.Name, t.Description
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		desc = strings.TrimSpace(*req.Description)
	}
	if !plainText(name, 64, false) || !plainText(desc, 280, true) {
		return errorf(http.StatusUnprocessableEntity, "a team needs a name of at most 64 characters and a description of at most 280, on one line each")
	}
	if err := a.nameTaken(r.Context(), p.OrgID, name, t.Slug); err != nil {
		return err
	}
	if err := a.Store.UpdateTeam(r.Context(), p.OrgID, t.Slug, name, desc, event(p, "team.update:"+t.Slug, "")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": t.Slug, "name": name, "description": desc})
	return nil
}

func (a *API) deleteTeam(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	t, err := a.team(r, p)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteTeam(r.Context(), p.OrgID, t.Slug, event(p, "team.delete:"+t.Slug, "")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": t.Slug})
	return nil
}

// activeMember finds an active member by person.
func (a *API) activeMember(ctx context.Context, org store.OrgID, person string) (store.User, error) {
	u, err := a.Store.UserByPerson(ctx, org, person)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		return u, errorf(http.StatusNotFound, "no member %q", person)
	}
	return u, err
}

func (a *API) addTeamMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	t, err := a.team(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Person string `json:"person"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	u, err := a.activeMember(r.Context(), p.OrgID, req.Person)
	if err != nil {
		return err
	}
	if err := a.Store.AddTeamMember(r.Context(), p.OrgID, t.Slug, u.ID, time.Now(), event(p, "team.member.add:"+t.Slug+":"+u.Person, "")); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return errorf(http.StatusNotFound, "no member %q", req.Person)
		case errors.Is(err, store.ErrUnknownTeam):
			return errorf(http.StatusNotFound, "no team %q", t.Slug)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": t.Slug, "added": u.Person})
	return nil
}

func (a *API) removeTeamMember(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	t, err := a.team(r, p)
	if err != nil {
		return err
	}
	u, err := a.Store.UserByPerson(r.Context(), p.OrgID, r.PathValue("person"))
	if errors.Is(err, store.ErrNotFound) {
		return errorf(http.StatusNotFound, "no member %q", r.PathValue("person"))
	}
	if err != nil {
		return err
	}
	if err := a.Store.RemoveTeamMember(r.Context(), p.OrgID, t.Slug, u.ID, event(p, "team.member.remove:"+t.Slug+":"+u.Person, "")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "%q is not in %q", u.Person, t.Slug)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": t.Slug, "removed": u.Person})
	return nil
}

// teamOrEveryone is the team named in the path for server access, where the
// built-in team may be named.
func (a *API) teamOrEveryone(r *http.Request, p auth.Principal) (string, error) {
	if r.PathValue("slug") == store.TeamEveryone {
		return store.TeamEveryone, nil
	}
	t, err := a.team(r, p)
	return t.Slug, err
}

func (a *API) addTeamServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	slug, err := a.teamOrEveryone(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Alias string `json:"alias"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	s, err := a.Store.ServerByAlias(r.Context(), p.OrgID, req.Alias)
	if errors.Is(err, store.ErrNotFound) {
		return errorf(http.StatusNotFound, "no server named %q", req.Alias)
	}
	if err != nil {
		return err
	}
	if err := a.Store.AddServerTeam(r.Context(), p.OrgID, s.ID, slug, event(p, "server.team.add:"+slug, s.Alias)); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return errorf(http.StatusNotFound, "no server named %q", s.Alias)
		case errors.Is(err, store.ErrUnknownTeam):
			return errorf(http.StatusNotFound, "no team %q", slug)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": slug, "server": s.Alias})
	return nil
}

func (a *API) removeTeamServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	slug, err := a.teamOrEveryone(r, p)
	if err != nil {
		return err
	}
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	if err := a.Store.RemoveServerTeam(r.Context(), p.OrgID, s.ID, slug, event(p, "server.team.remove:"+slug, s.Alias)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "team %q has no access to %q", slug, s.Alias)
		}
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"team": slug, "server": s.Alias})
	return nil
}

// cleanTeams checks a list of team names from a request: each "everyone" or
// a well-formed slug, no repeats. Whether the teams exist is the store's to
// say, inside the transaction.
func cleanTeams(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t != store.TeamEveryone && !store.ValidTeamSlug(t) {
			return nil, errorf(http.StatusUnprocessableEntity, "%q is not a team name", t)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ---- the catalog

type catalogEntryView struct {
	catalog.Entry
	Installed string `json:"installed,omitempty"` // the alias of a server already added with this URL
}

func (a *API) listCatalog(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	servers, err := a.Store.ListServers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	byURL := map[string]string{}
	for _, s := range servers {
		if s.Config.URL != "" {
			byURL[s.Config.URL] = s.Alias
		}
	}
	entries := catalog.Entries()
	out := make([]catalogEntryView, 0, len(entries))
	for _, e := range entries {
		out = append(out, catalogEntryView{Entry: e, Installed: byURL[e.URL]})
	}
	callback := ""
	if a.PublicURL != nil {
		callback = upstreamauth.CallbackURL(a.PublicURL)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": catalog.Profiles(), "entries": out, "callback_url": callback,
		"oauth_available": a.OAuth != nil && a.Grants != nil && a.PublicURL != nil})
	return nil
}

// ---- one's own tools

type myServerView struct {
	Alias       string `json:"alias"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Tools       int    `json:"tools"`
	// OAuth: each person connects their own account; Connected says whether
	// this person did.
	Auth       string `json:"auth,omitempty"`
	Connected  bool   `json:"connected,omitempty"`
	ConnectURL string `json:"connect_url,omitempty"`
}

// userOf is the member behind p, or "" when there is none: the signed-in
// person or the member whose own token p is, never a person name (F07 R-01).
func (a *API) userOf(_ context.Context, p auth.Principal) (string, error) {
	return p.UserID, nil
}

// myTools is what the start page shows a person: their teams, the servers
// they reach with the tools they can use, and which accounts they still need
// to connect. It never shows servers they do not reach.
func (a *API) myTools(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	acc, err := gateway.Access(ctx, a.Store, p)
	if err != nil {
		return err
	}
	teams, err := a.Store.ListTeams(ctx, p.OrgID)
	if err != nil {
		return err
	}
	names := map[string]string{store.TeamEveryone: everyoneName}
	for _, t := range teams {
		names[t.Slug] = t.Name
	}
	myTeams := []map[string]string{}
	for _, slug := range acc.Teams {
		myTeams = append(myTeams, map[string]string{"slug": slug, "name": names[slug]})
	}
	uid, err := a.userOf(ctx, p)
	if err != nil {
		return err
	}
	connected := map[string]bool{}
	if uid != "" {
		grants, err := a.Store.ListGrants(ctx, p.OrgID, uid)
		if err != nil {
			return err
		}
		for _, g := range grants {
			connected[g.ServerID] = true
		}
	}
	byURL := map[string]catalog.Entry{}
	for _, e := range catalog.Entries() {
		byURL[e.URL] = e
	}
	servers, err := a.Store.ListServers(ctx, p.OrgID)
	if err != nil {
		return err
	}
	out := []myServerView{}
	for _, s := range servers {
		if !s.Enabled || !acc.Reaches(s.ID) {
			continue
		}
		v := myServerView{Alias: s.Alias, Name: s.Alias, Status: upstream.StatusConnecting}
		if e, ok := byURL[s.Config.URL]; ok {
			v.Name, v.Description = e.Name, e.Description
		}
		if st, ok := a.Upstream.State(s.ID); ok {
			v.Status = st.Status
			pins, err := a.Store.PinStatuses(ctx, p.OrgID, s.ID)
			if err != nil {
				return err
			}
			for _, it := range st.Items {
				if it.Kind == upstream.KindTool && acc.SeesTool(s.ID, it.Name) && pins[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] == store.PinAccepted {
					v.Tools++
				}
			}
		}
		if s.Config.Auth == store.AuthOAuth {
			v.Auth, v.Connected, v.ConnectURL = store.AuthOAuth, connected[s.ID], a.connectURL(s.Alias)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": myTeams, "servers": out})
	return nil
}
