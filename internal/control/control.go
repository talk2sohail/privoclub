// Package control serves the versioned admin API under /api/v1.
// Every route authenticates the caller and checks a permission (ADR 0011).
// Responses never contain secrets.
package control

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/argcheck"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/oauthserver"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/scan"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/telemetry"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

// API is the admin API.
type API struct {
	Store *store.Store
	Keys  *keyring.Keyring
	// AuditKeys holds the master key the audit signing keys derive from
	// (ADR 0028); nil turns the integrity endpoints off.
	AuditKeys vault.KeyProvider
	Auth      *auth.Authenticator
	Upstream  *upstream.Manager
	Audit     *audit.Writer
	Approval  *approval.Service
	Egress    egress.Policy
	Logger    *slog.Logger
	// TokenTTL is the default personal token lifetime.
	TokenTTL time.Duration

	// Upstream OAuth (ADR 0017): discovery and registration, the grants, and
	// the public URL the callback and the links are built from. Without them
	// servers cannot use each person's own account.
	OAuth     *upstreamauth.Client
	Grants    *grants.Service
	PublicURL *url.URL
	// Privo's own authorization server (ADR 0022) and its client rules; nil
	// while PRIVO_OAUTH_SERVER is off.
	AuthServer *oauthserver.Server
	Clients    *oauthserver.Clients
	// Call statistics and the diagnostic log (ADR 0024). The endpoints answer
	// with empty data while retention is 0 (recording off).
	Telemetry              *telemetry.Recorder
	TelemetryRetentionDays int
	stats                  statCache
	in                     internal // the mux of in-process calls (Serve)
}

// ConnectWait bounds how long registration waits for the first connection.
const ConnectWait = 10 * time.Second

var aliasRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,15}$`)
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

// Routes mounts the API on mux.
func (a *API) Routes(mux *http.ServeMux) { a.register(mux, a.with) }

// register mounts every route on mux, each wrapped by wrap.
func (a *API) register(mux *http.ServeMux, wrap func(auth.Permission, handler) http.Handler) {
	a.registerMembers(mux, wrap)
	a.registerTeams(mux, wrap)
	a.registerOAuthClients(mux, wrap)
	mux.Handle("GET /api/v1/servers", wrap(auth.PermReadServers, a.listServers))
	mux.Handle("POST /api/v1/servers", wrap(auth.PermManageServers, a.createServer))
	mux.Handle("PATCH /api/v1/servers/{alias}", wrap(auth.PermManageServers, a.patchServer))
	mux.Handle("DELETE /api/v1/servers/{alias}", wrap(auth.PermManageServers, a.deleteServer))
	mux.Handle("GET /api/v1/servers/{alias}/pins", wrap(auth.PermManageServers, a.listPins))
	mux.Handle("GET /api/v1/servers/{alias}/connections", wrap(auth.PermManageServers, a.listConnections))
	mux.Handle("DELETE /api/v1/servers/{alias}/connections/{person}", wrap(auth.PermManageServers, a.deleteConnection))
	mux.Handle("GET /api/v1/connections", wrap(auth.PermManageServers, a.listAllConnections))
	mux.Handle("POST /api/v1/invites", wrap(auth.PermManageTokens, a.createInvite))
	mux.Handle("POST /api/v1/servers/{alias}/pins:accept", wrap(auth.PermManageServers, a.acceptPins))
	mux.Handle("POST /api/v1/servers/{alias}/pins:block", wrap(auth.PermManageServers, a.blockPins))
	mux.Handle("PATCH /api/v1/servers/{alias}/teams/{slug}", wrap(auth.PermManageServers, a.patchTeamTools))
	mux.Handle("GET /api/v1/tokens", wrap(auth.PermManageTokens, a.listTokens))
	mux.Handle("POST /api/v1/tokens", wrap(auth.PermManageTokens, a.createToken))
	mux.Handle("DELETE /api/v1/tokens/{id}", wrap(auth.PermManageTokens, a.revokeToken))
	mux.Handle("GET /api/v1/policies", wrap(auth.PermManagePolicy, a.listPolicies))
	mux.Handle("POST /api/v1/policies", wrap(auth.PermManagePolicy, a.createPolicy))
	mux.Handle("DELETE /api/v1/policies/{id}", wrap(auth.PermManagePolicy, a.deletePolicy))
	mux.Handle("GET /api/v1/policies/export", wrap(auth.PermManagePolicy, a.exportPolicies))
	mux.Handle("POST /api/v1/policies/import", wrap(auth.PermManagePolicy, a.importPolicies))
	mux.Handle("POST /api/v1/servers/{alias}/policy:preset", wrap(auth.PermManagePolicy, a.applyPreset))
	mux.Handle("PATCH /api/v1/settings/policy", wrap(auth.PermManagePolicy, a.patchPolicySettings))
	mux.Handle("GET /api/v1/approvals", wrap(auth.PermDecide, a.listApprovals))
	mux.Handle("GET /api/v1/approvals/{id}", wrap(auth.PermDecide, a.getApproval))
	mux.Handle("POST /api/v1/approvals/{id}/approve", wrap(auth.PermDecide, a.decide(true)))
	mux.Handle("POST /api/v1/approvals/{id}/deny", wrap(auth.PermDecide, a.decide(false)))
	mux.Handle("PATCH /api/v1/settings/approvals", wrap(auth.PermManagePolicy, a.patchApprovalSettings))
	mux.Handle("GET /api/v1/audit/events", wrap(auth.PermReadAudit, a.listAudit))
	mux.Handle("GET /api/v1/audit/export", wrap(auth.PermReadAudit, a.exportAudit))
	mux.Handle("GET /api/v1/audit/integrity", wrap(auth.PermReadAudit, a.auditIntegrity))
	mux.Handle("GET /api/v1/audit/checkpoints", wrap(auth.PermReadAudit, a.auditCheckpoints))
	mux.Handle("GET /api/v1/audit/keys", wrap(auth.PermReadAudit, a.auditKeys))
	mux.Handle("POST /api/v1/audit/erase", wrap(auth.PermManageAudit, a.eraseAudit))
	mux.Handle("PATCH /api/v1/settings/audit", wrap(auth.PermManageAudit, a.patchAuditSettings))
	a.telemetryRoutes(mux, wrap)
}

type handler func(w http.ResponseWriter, r *http.Request, p auth.Principal) error

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errorf(status int, format string, args ...any) error {
	return &httpError{status: status, msg: fmt.Sprintf(format, args...)}
}

func (a *API) with(perm auth.Permission, h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			return
		}
		p, _, err := a.Auth.Verify(r.Context(), strings.TrimSpace(bearer))
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			return
		}
		// Only control tokens reach the admin API. Gateway tokens live in AI
		// clients; refusing them here means an agent can never administer
		// Privo or approve its own calls.
		if p.Kind != store.KindControl {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "this is a gateway token; the admin API needs a control token"})
			return
		}
		a.run(perm, h, w, r, p)
	})
}

// run checks the permission and runs the handler as p, turning its error into
// a JSON answer. Token requests and in-process calls (Serve) share it.
func (a *API) run(perm auth.Permission, h handler, w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.Can(perm) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "your role cannot do this"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := h(w, r, p); err != nil {
		var he *httpError
		if errors.As(err, &he) {
			writeJSON(w, he.status, map[string]string{"error": he.msg})
			return
		}
		a.Logger.Error("admin api", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errorf(http.StatusBadRequest, "invalid JSON body: unexpected data after the object")
	}
	return nil
}

// decodeOptional is decode for a body that may be left out (empty, or
// whitespace only), as on approve and deny.
func decodeOptional(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return errorf(http.StatusBadRequest, "could not read the body")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	return decode(r, v)
}

func (a *API) adminEvent(ctx context.Context, p auth.Principal, action, server string) {
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(), Server: server, Action: action, Decision: "allow", Outcome: "ok"}
	if err := a.Audit.Admin(ctx, p.OrgID, ev); err != nil {
		a.Logger.Error("audit admin event", "action", action, "err", err)
	}
}

// ---- servers

type serverView struct {
	ID        string         `json:"id"`
	Alias     string         `json:"alias"`
	Transport string         `json:"transport"`
	Enabled   bool           `json:"enabled"`
	Status    string         `json:"status"`
	Error     string         `json:"error,omitempty"`
	Target    string         `json:"target"`
	Items     map[string]int `json:"items"`
	Pending   int            `json:"pending_review"`
	// Auth is "oauth" when each person uses their own account; ConnectURL is
	// where they connect it.
	Auth         string `json:"auth,omitempty"`
	Registration string `json:"registration,omitempty"`
	ConnectURL   string `json:"connect_url,omitempty"`
	// KeyHeader is the header each person's own key goes in (ADR 0023);
	// shown to admins only.
	KeyHeader string `json:"key_header,omitempty"`
	// Teams that reach the server (ADR 0020); shown to admins only, and
	// always to them, so an empty list ("nobody") is visible (F26 R-04).
	Teams *[]string `json:"teams,omitempty"`
	// TeamTools, TeamPrompts, TeamResources, TeamTemplates hold narrowed per-kind
	// lists per team (ADR 0026); admins only. A team on the server not present
	// in a map sees all items of that kind.
	TeamTools     map[string][]string `json:"team_tools,omitempty"`
	TeamPrompts   map[string][]string `json:"team_prompts,omitempty"`
	TeamResources map[string][]string `json:"team_resources,omitempty"`
	TeamTemplates map[string][]string `json:"team_templates,omitempty"`
	// For editing (ADR 0029), admins only: the static headers that are set
	// and the OAuth client entered by hand. Never a value or a secret.
	HeaderNames     []string `json:"header_names,omitempty"`
	ClientID        string   `json:"client_id,omitempty"`
	HasClientSecret bool     `json:"has_client_secret,omitempty"`
}

// view describes a server. Members and auditors see neither the full target
// (URLs often carry keys) nor connection errors (they can contain them).
// acc is the caller's access when they do not manage servers, nil otherwise.
func (a *API) view(ctx context.Context, p auth.Principal, s store.Server, teams []string, acc *store.Access) serverView {
	admin := p.Can(auth.PermManageServers)
	v := serverView{ID: s.ID, Alias: s.Alias, Transport: s.Transport, Enabled: s.Enabled, Status: upstream.StatusConnecting, Items: map[string]int{}}
	if admin {
		t := append([]string{}, teams...)
		v.Teams = &t
		v.HeaderNames = s.Config.HeaderNames
		if o := s.Config.OAuth; o != nil && (o.Registration == store.RegistrationAdmin || o.Registration == store.RegistrationManual) {
			v.ClientID, v.HasClientSecret = o.ClientID, o.HasClientSecret
		}
	}
	switch {
	case s.Transport == "stdio" && admin:
		v.Target = s.Config.Path
	case s.Transport != "stdio":
		v.Target = upstream.RedactURL(s.Config.URL)
	}
	if !s.Enabled {
		v.Status = upstream.StatusDisabled
	}
	if s.Config.PerPerson() {
		v.Auth, v.ConnectURL = s.Config.Auth, a.connectURL(s.Alias)
		if s.Config.OAuth != nil && admin {
			v.Registration = s.Config.OAuth.Registration
		}
		if s.Config.Key != nil && admin {
			v.KeyHeader = s.Config.Key.Header
		}
	}
	if st, ok := a.Upstream.State(s.ID); ok {
		v.Status = st.Status
		if admin {
			v.Error = st.Error
		}
		pins, _ := a.Store.PinStatuses(ctx, p.OrgID, s.ID)
		for _, it := range st.Items {
			// Others count only the items their teams let them see (ADR 0026),
			// so a narrowed team does not learn how many are hidden.
			if acc != nil && !acc.SeesItem(s.ID, it.Kind, it.Name) {
				continue
			}
			v.Items[it.Kind]++
			if admin && pins[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] == store.PinPending {
				v.Pending++
			}
		}
	}
	return v
}

// listServers lists the org's servers. Admins see every server; anyone else
// only the ones their teams reach (ADR 0020).
func (a *API) listServers(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	servers, err := a.Store.ListServers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	st, err := a.Store.ServerTeams(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	var acc store.Access
	admin := p.Can(auth.PermManageServers)
	if !admin {
		if acc, err = gateway.Access(r.Context(), a.Store, p); err != nil {
			return err
		}
	}
	var ts map[string]map[string]store.TeamToolset
	if admin {
		if ts, err = a.Store.TeamToolsets(r.Context(), p.OrgID); err != nil {
			return err
		}
	}
	out := make([]serverView, 0, len(servers))
	for _, s := range servers {
		if admin || acc.Reaches(s.ID) {
			var mine *store.Access
			if !admin {
				mine = &acc
			}
			v := a.view(r.Context(), p, s, st[s.ID], mine)
			if admin {
				for team, set := range ts[s.ID] {
					if set.Tools != nil {
						if v.TeamTools == nil {
							v.TeamTools = map[string][]string{}
						}
						v.TeamTools[team] = set.Tools
					}
					if set.Prompts != nil {
						if v.TeamPrompts == nil {
							v.TeamPrompts = map[string][]string{}
						}
						v.TeamPrompts[team] = set.Prompts
					}
					if set.Resources != nil {
						if v.TeamResources == nil {
							v.TeamResources = map[string][]string{}
						}
						v.TeamResources[team] = set.Resources
					}
					if set.Templates != nil {
						if v.TeamTemplates == nil {
							v.TeamTemplates = map[string][]string{}
						}
						v.TeamTemplates[team] = set.Templates
					}
				}
			}
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
	return nil
}

type toolsetFieldSpec struct {
	jsonKey   string
	kind      string
	auditKind string
	maxLen    int
	raw       json.RawMessage
}

func parseToolsetField(raw json.RawMessage, field string, maxLen int) (present bool, out []string, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false, nil, nil
	}
	if string(trimmed) == "null" {
		return true, nil, nil
	}
	var list []string
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return false, nil, errorf(http.StatusUnprocessableEntity, "%s must be null (all) or a list of names", field)
	}
	out = []string{}
	for _, name := range list {
		if name == "" || len(name) > maxLen || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false, nil, errorf(http.StatusUnprocessableEntity, "each %s entry must be 1 to %d characters without control characters", field, maxLen)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) > store.MaxTeamTools {
		return false, nil, errorf(http.StatusUnprocessableEntity, "at most %d entries per team in %s", store.MaxTeamTools, field)
	}
	sort.Strings(out)
	return true, out, nil
}

// patchTeamTools sets which of a server's tools, prompts, resources, and
// resource templates a team sees (ADR 0026): null for all, [names] for only those.
func (a *API) patchTeamTools(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	team := r.PathValue("slug")
	var req struct {
		Tools             json.RawMessage `json:"tools"`
		Prompts           json.RawMessage `json:"prompts"`
		Resources         json.RawMessage `json:"resources"`
		ResourceTemplates json.RawMessage `json:"resource_templates"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	specs := []toolsetFieldSpec{
		{jsonKey: "tools", kind: store.ItemKindTool, auditKind: "server.team_tools", maxLen: 128, raw: req.Tools},
		{jsonKey: "prompts", kind: store.ItemKindPrompt, auditKind: "server.team_prompts", maxLen: 128, raw: req.Prompts},
		{jsonKey: "resources", kind: store.ItemKindResource, auditKind: "server.team_resources", maxLen: 2048, raw: req.Resources},
		{jsonKey: "resource_templates", kind: store.ItemKindResourceTemplate, auditKind: "server.team_templates", maxLen: 2048, raw: req.ResourceTemplates},
	}
	updates := map[string][]string{}
	resp := map[string]any{"server": s.Alias, "team": team}
	var evs []store.AuditEvent
	now := time.Now()
	for _, sp := range specs {
		present, list, err := parseToolsetField(sp.raw, sp.jsonKey, sp.maxLen)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		updates[sp.kind] = list
		resp[sp.jsonKey] = list
		what := "all"
		if list != nil {
			what = strconv.Itoa(len(list))
		}
		evs = append(evs, store.AuditEvent{
			ID: ids.New(), CallID: ids.New(), Time: now, Actor: p.Name, ActorID: p.ActorID(), Server: s.Alias,
			Action: sp.auditKind + ":" + s.Alias + ":" + team + ":" + what, Decision: "allow", Outcome: "ok",
		})
	}
	if len(updates) == 0 {
		return errorf(http.StatusUnprocessableEntity, "provide at least one of tools, prompts, resources, or resource_templates")
	}
	if err := a.Store.SetTeamToolsets(r.Context(), p.OrgID, s.ID, team, updates, evs...); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "%q is not a team of %q", team, s.Alias)
		}
		return err
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

type createServerReq struct {
	Alias     string `json:"alias"`
	Transport string `json:"transport"`
	Command   *struct {
		Path   string   `json:"path"`
		SHA256 string   `json:"sha256"`
		Args   []string `json:"args"`
	} `json:"command"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Env     map[string]string `json:"env"`
	// OAuth makes every call run with the caller's own account (ADR 0017).
	OAuth *oauthReq `json:"oauth"`
	// PersonalKey makes every call run with the caller's own API key, which
	// each person enters once on the connect page (ADR 0023).
	PersonalKey *personalKeyReq `json:"personal_key"`
	// Teams that reach the server (ADR 0020); absent means everyone.
	Teams *[]string `json:"teams"`
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ctx := r.Context()
	var req createServerReq
	if err := decode(r, &req); err != nil {
		return err
	}
	if !aliasRe.MatchString(req.Alias) {
		return errorf(http.StatusUnprocessableEntity, "alias must match %s", aliasRe)
	}
	srv := store.Server{OrgID: p.OrgID, ID: ids.New(), Alias: req.Alias, Transport: req.Transport, Enabled: true, CreatedAt: time.Now()}
	if req.Teams != nil {
		teams, err := cleanTeams(*req.Teams)
		if err != nil {
			return err
		}
		srv.Teams = teams
		// Fast path before any outbound work (discovery, registration at the
		// provider); the store checks again inside its transaction (F26 R-06).
		for _, t := range teams {
			if t == store.TeamEveryone {
				continue
			}
			if _, err := a.Store.TeamBySlug(ctx, p.OrgID, t); errors.Is(err, store.ErrNotFound) {
				return errorf(http.StatusUnprocessableEntity, "there is no team %q", t)
			} else if err != nil {
				return err
			}
		}
	}
	secrets := map[string]string{}
	if req.OAuth != nil && req.Transport != "http" && req.Transport != "sse" {
		return errorf(http.StatusUnprocessableEntity, "only http and sse servers can use each person's own account (oauth)")
	}
	if req.OAuth != nil && len(req.Headers) > 0 {
		return errorf(http.StatusUnprocessableEntity, "a server that uses each person's own account (oauth) takes no static headers")
	}
	if req.PersonalKey != nil && req.OAuth != nil {
		return errorf(http.StatusUnprocessableEntity, "a server takes each person's own account (oauth) or each person's own key (personal_key), not both")
	}
	var key *store.ServerKey
	if req.PersonalKey != nil {
		if req.Transport != "http" && req.Transport != "sse" {
			return errorf(http.StatusUnprocessableEntity, "only http and sse servers can take each person's own key")
		}
		k, err := req.PersonalKey.check(req.Headers)
		if err != nil {
			return err
		}
		key = &k
	}
	switch req.Transport {
	case "stdio":
		if req.Command == nil || !strings.HasPrefix(req.Command.Path, "/") || !sha256Re.MatchString(req.Command.SHA256) {
			return errorf(http.StatusUnprocessableEntity, "stdio servers need command.path (absolute) and command.sha256 (hex)")
		}
		srv.Config = store.ServerConfig{Path: req.Command.Path, SHA256: req.Command.SHA256, Args: req.Command.Args}
		for k, v := range req.Env {
			if !nameRe.MatchString(k) || runner.ReservedEnv(k) {
				return errorf(http.StatusUnprocessableEntity, "env name %q is invalid or reserved", k)
			}
			secrets["env:"+k] = v
			srv.Config.EnvNames = append(srv.Config.EnvNames, k)
		}
	case "http", "sse":
		u, err := a.Egress.CheckURL(ctx, req.URL, nil)
		if err != nil {
			if errors.Is(err, egress.ErrBlocked) {
				return errorf(http.StatusUnprocessableEntity, "egress policy refused %s: %v", req.URL, err)
			}
			return errorf(http.StatusUnprocessableEntity, "%v", err)
		}
		srv.Config = store.ServerConfig{URL: u.String()}
		for k, v := range req.Headers {
			if !validHeaderName(k) {
				return errorf(http.StatusUnprocessableEntity, "header name %q is invalid or reserved", k)
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return errorf(http.StatusUnprocessableEntity, "header %q has an invalid value", k)
			}
			secrets["header:"+http.CanonicalHeaderKey(k)] = v
			srv.Config.HeaderNames = append(srv.Config.HeaderNames, http.CanonicalHeaderKey(k))
		}
	default:
		return errorf(http.StatusUnprocessableEntity, "transport must be stdio, http or sse")
	}
	sort.Strings(srv.Config.EnvNames)
	sort.Strings(srv.Config.HeaderNames)

	creds, err := a.seal(ctx, p.OrgID, srv.ID, secrets)
	if err != nil {
		return err
	}
	if key != nil {
		if a.Grants == nil || a.PublicURL == nil {
			return errorf(http.StatusUnprocessableEntity,
				"servers that take each person's own key need PRIVO_PUBLIC_URL and the admin site (GitHub sign-in) to be configured")
		}
		srv.Config.Auth, srv.Config.Key = store.AuthKey, key
	}
	if req.OAuth != nil {
		o, secret, err := a.configureOAuth(ctx, srv, req.OAuth)
		if err != nil {
			return err
		}
		srv.Config.Auth, srv.Config.OAuth = store.AuthOAuth, &o
		if secret != "" {
			c, err := a.Grants.SealClientSecret(ctx, p.OrgID, srv.ID, secret)
			if err != nil {
				return err
			}
			creds = append(creds, c)
		}
	}
	if err := a.Store.CreateServer(ctx, srv, creds); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return errorf(http.StatusConflict, "a server named %q already exists", req.Alias)
		}
		if errors.Is(err, store.ErrUnknownTeam) {
			return errorf(http.StatusUnprocessableEntity, "one of the teams %v does not exist", srv.Teams)
		}
		return err
	}
	if srv.Teams == nil {
		srv.Teams = []string{store.TeamEveryone}
	}
	a.adminEvent(ctx, p, "server.create", srv.Alias)
	a.adminEvent(ctx, p, "server.teams="+strings.Join(srv.Teams, ","), srv.Alias)
	if srv.Config.OAuth != nil {
		a.adminEvent(ctx, p, "server.oauth_register:"+srv.Alias+":"+srv.Config.OAuth.Registration, srv.Alias)
	}
	if srv.Config.Key != nil {
		a.adminEvent(ctx, p, "server.personal_key:"+srv.Alias+":"+srv.Config.Key.Header, srv.Alias)
	}
	if err := a.Upstream.Sync(ctx, p.OrgID); err != nil {
		return err
	}
	// Report the first connection result, so the admin sees the status and
	// how many items wait for review in the response.
	a.Upstream.WaitSettled(ctx, srv.ID, ConnectWait)
	writeJSON(w, http.StatusCreated, map[string]any{"server": a.view(ctx, p, srv, srv.Teams, nil)})
	return nil
}

func (a *API) seal(ctx context.Context, org store.OrgID, serverID string, secrets map[string]string) ([]store.Credential, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	version, dek, err := a.Keys.Current(ctx, org)
	if err != nil {
		return nil, err
	}
	var out []store.Credential
	for purpose, value := range secrets {
		c := store.Credential{ID: ids.New(), ServerID: serverID, Purpose: purpose, KeyVersion: version, RowVersion: 1}
		c.Ciphertext, err = vault.Seal(dek, []byte(value), vault.Binding{OrgID: string(org), CredentialID: c.ID, ServerID: serverID,
			Purpose: purpose, KeyVersion: version, RowVersion: 1})
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *API) server(r *http.Request, p auth.Principal) (store.Server, error) {
	s, err := a.Store.ServerByAlias(r.Context(), p.OrgID, r.PathValue("alias"))
	if errors.Is(err, store.ErrNotFound) {
		return s, errorf(http.StatusNotFound, "no server named %q", r.PathValue("alias"))
	}
	return s, err
}

func (a *API) patchServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Enabled *bool     `json:"enabled"`
		Teams   *[]string `json:"teams"`
		serverEdit
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Enabled == nil && req.Teams == nil && !req.any() {
		return errorf(http.StatusUnprocessableEntity, "nothing to change")
	}
	// Teams are checked before anything is written, so a refused request
	// changed nothing (security review of the third slice, L2).
	var teams []string
	if req.Teams != nil {
		if teams, err = cleanTeams(*req.Teams); err != nil {
			return err
		}
		for _, t := range teams {
			if t == store.TeamEveryone {
				continue
			}
			if _, err := a.Store.TeamBySlug(r.Context(), p.OrgID, t); errors.Is(err, store.ErrNotFound) {
				return errorf(http.StatusUnprocessableEntity, "there is no team %q", t)
			} else if err != nil {
				return err
			}
		}
	}
	var dropped []string
	if req.any() {
		if dropped, err = a.editServer(r.Context(), p, s, req.serverEdit); err != nil {
			return err
		}
	}
	if req.Teams != nil {
		if err := a.Store.SetServerTeams(r.Context(), p.OrgID, s.ID, teams, event(p, "server.teams="+strings.Join(teams, ","), s.Alias)); err != nil {
			if errors.Is(err, store.ErrUnknownTeam) {
				return errorf(http.StatusUnprocessableEntity, "one of the teams %v does not exist", teams)
			}
			return err
		}
	}
	if req.Enabled != nil {
		if err := a.Store.SetServerEnabled(r.Context(), p.OrgID, s.ID, *req.Enabled); err != nil {
			return err
		}
		a.adminEvent(r.Context(), p, "server.enabled="+strconv.FormatBool(*req.Enabled), s.Alias)
		if err := a.Upstream.Sync(r.Context(), p.OrgID); err != nil {
			return err
		}
		a.Upstream.WaitSettled(r.Context(), s.ID, ConnectWait)
	}
	s, _ = a.Store.ServerByAlias(r.Context(), p.OrgID, s.Alias)
	st, err := a.Store.ServerTeams(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := map[string]any{"server": a.view(r.Context(), p, s, st[s.ID], nil)}
	if len(dropped) > 0 {
		out["headers_dropped"] = dropped // the address changed; enter them again for the new one
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	// Everyone's grants go with the server; they are revoked at the provider
	// too, read now and sent once the server is gone (F07 R-04).
	revoke := a.serverRevocations(r.Context(), p.OrgID, s)
	if err := a.Store.DeleteServer(r.Context(), p.OrgID, s.ID); err != nil {
		return err
	}
	a.revokeLater(r.Context(), revoke)
	a.adminEvent(r.Context(), p, "server.delete", s.Alias)
	if err := a.Upstream.Sync(r.Context(), p.OrgID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": s.Alias})
	return nil
}

type pinRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Hash string `json:"hash"`
}

func (a *API) listPins(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	pins, err := a.Store.ListPins(r.Context(), p.OrgID, s.ID)
	if err != nil {
		return err
	}
	// What the server presents now; nil while it is not connected.
	var current map[store.PinKey]bool
	if st, ok := a.Upstream.State(s.ID); ok && len(st.Items) > 0 {
		current = map[store.PinKey]bool{}
		for _, it := range st.Items {
			current[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pins": pinViews(pins, current)})
	return nil
}

// pinViews describes a server's pins for the review (newest first, as the
// store lists them). serving marks an accepted definition the server presents
// now, which is what the gateway serves; it is left out while what the server
// presents is not known. previous is the definition a pending or blocked one
// would replace: the one served now, else the newest accepted one (review of
// control-plane batch 1).
func pinViews(pins []store.Pin, current map[store.PinKey]bool) []map[string]any {
	serving := func(pin store.Pin) bool {
		return pin.Status == store.PinAccepted && current[store.PinKey{Kind: pin.Kind, Name: pin.Name, Hash: pin.Hash}]
	}
	before := map[[2]string]store.Pin{}
	for _, pin := range pins {
		k := [2]string{pin.Kind, pin.Name}
		if pin.Status != store.PinAccepted {
			continue
		}
		if prev, seen := before[k]; !seen || (serving(pin) && !serving(prev)) {
			before[k] = pin
		}
	}
	out := make([]map[string]any, 0, len(pins))
	for _, pin := range pins {
		v := map[string]any{"kind": pin.Kind, "name": pin.Name, "hash": pin.Hash, "status": pin.Status,
			"definition": json.RawMessage(pin.Definition),
			// F11: signs of hidden instructions, for the reviewer (a hint, never a decision).
			"findings": findings(pin)}
		if current != nil && pin.Status == store.PinAccepted {
			v["serving"] = serving(pin)
		}
		if prev, ok := before[[2]string{pin.Kind, pin.Name}]; ok && pin.Status != store.PinAccepted {
			v["previous"] = map[string]any{"hash": prev.Hash, "definition": json.RawMessage(prev.Definition)}
		}
		out = append(out, v)
	}
	return out
}

// findings are the reviewer's hints on a definition: signs of hidden
// instructions (F11), and for a tool an input schema Privo cannot read, whose
// calls the gateway refuses (F02).
func findings(pin store.Pin) []scan.Finding {
	out := scan.Scan([]byte(pin.Definition))
	if pin.Kind != upstream.KindTool {
		return out
	}
	if problem := argcheck.DefinitionProblem([]byte(pin.Definition)); problem != "" {
		out = append(out, scan.Finding{Path: "inputSchema", Rule: "input-schema", Severity: "warn",
			Message: "Privo cannot read this input schema, so it cannot check arguments and refuses every call to this tool",
			Excerpt: scan.Reveal(problem)})
	}
	return out
}

// pinsAction names what a review decided: the count, then each definition as
// kind/name@hash-prefix (at most 20), so the trail says which ones.
func pinsAction(verb string, done []store.PinKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "pins.%s:%d", verb, len(done))
	for i, k := range done {
		if i == 20 {
			b.WriteString(",…")
			break
		}
		sep := ","
		if i == 0 {
			sep = ":"
		}
		b.WriteString(sep + k.Kind + "/" + k.Name + "@" + k.Hash[:min(len(k.Hash), 12)])
	}
	return b.String()
}

// acceptPins accepts exactly the reviewed definitions named in the request.
func (a *API) acceptPins(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return a.decidePins(w, r, p, "accept", a.Store.AcceptPins)
}

// blockPins blocks exactly the reviewed pending definitions named in the
// request (F11): they stay hidden and leave the review list.
func (a *API) blockPins(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	return a.decidePins(w, r, p, "block", a.Store.BlockPins)
}

func (a *API) decidePins(w http.ResponseWriter, r *http.Request, p auth.Principal, verb string,
	decide func(context.Context, store.OrgID, string, []store.PinKey) ([]store.PinKey, error)) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Pins []pinRef `json:"pins"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if len(req.Pins) == 0 || len(req.Pins) > 5000 {
		return errorf(http.StatusUnprocessableEntity, "list the reviewed pins (kind, name, hash) to %s", verb)
	}
	keys := make([]store.PinKey, 0, len(req.Pins))
	for _, pr := range req.Pins {
		keys = append(keys, store.PinKey{Kind: pr.Kind, Name: pr.Name, Hash: pr.Hash})
	}
	done, err := decide(r.Context(), p.OrgID, s.ID, keys)
	if err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, pinsAction(verb, done), s.Alias)
	writeJSON(w, http.StatusOK, map[string]any{verb + "ed": len(done), "requested": len(keys)})
	return nil
}

// ---- tokens

type tokenView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Kind   string `json:"kind"`
	Person string `json:"person"`
	// Bound says the token belongs to a member, who made it for themselves:
	// only such a token uses that member's teams and accounts (F27).
	Bound     bool      `json:"bound"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func viewToken(t store.APIToken) tokenView {
	return tokenView{ID: t.ID, Name: t.Name, Role: t.Role, Kind: t.Kind, Person: t.Person, Bound: t.UserID != "", CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt}
}

func (a *API) listTokens(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	toks, err := a.Store.ListTokens(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]tokenView, 0, len(toks))
	for _, t := range toks {
		out = append(out, viewToken(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
	return nil
}

// TokenSpec describes a token to issue. Person is a label. UserID makes the
// token belong to that member, with their teams and accounts: only a signed-in
// member sets it, for themselves (F27). Everything else issues a token of
// nobody, whatever person it names.
type TokenSpec struct {
	Name, Role, Kind, Person string
	UserID                   string
	TTL                      time.Duration
	// Event names who issues the token (Actor, ActorID, Client); IssueToken
	// fills in the rest and writes it with the token, in one transaction
	// (F24 second review R-02).
	Event store.AuditEvent
}

// IssueToken creates a token and returns its plain text once. Shared with the CLI.
func IssueToken(ctx context.Context, s *store.Store, org store.OrgID, spec TokenSpec) (string, store.APIToken, error) {
	name, role, ttl := spec.Name, spec.Role, spec.TTL
	if !store.ValidRole(role) {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "unknown role %q", role)
	}
	if !store.ValidKind(spec.Kind) {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "kind must be gateway or control")
	}
	if spec.Kind == store.KindGateway && role == store.RoleApprover {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "approvers decide with control tokens; they get no gateway token")
	}
	if spec.Kind == store.KindControl && role == store.RoleMember {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "members only use the gateway; issue a gateway token")
	}
	person := strings.TrimSpace(spec.Person)
	if person == "" {
		person = name
	}
	if len(person) > 128 {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "person is too long")
	}
	if strings.TrimSpace(name) == "" || len(name) > 64 {
		return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity, "name must be 1 to 64 characters")
	}
	tok, err := token.New()
	if err != nil {
		return "", store.APIToken{}, err
	}
	now := time.Now()
	rec := store.APIToken{OrgID: org, ID: tok.ID, Name: name, Role: role, Kind: spec.Kind, Person: person, UserID: spec.UserID, SecretHash: tok.SecretHash,
		CreatedAt: now, ExpiresAt: now.Add(ttl)}
	ev := spec.Event
	ev.ID, ev.CallID, ev.Time = ids.New(), ids.New(), now
	ev.Action, ev.Decision, ev.Outcome = "token.create:"+rec.Kind+":"+rec.Role+":"+rec.Name, "allow", "ok"
	if err := s.CreateTokenAudited(ctx, rec, ev); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "", rec, errorf(http.StatusConflict, "an active token named %q already exists", name)
		}
		if errors.Is(err, store.ErrNotFound) {
			return "", rec, errorf(http.StatusForbidden, "only an active member can make a token of their own")
		}
		return "", rec, err
	}
	return tok.Plain, rec, nil
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		Kind    string `json:"kind"`
		Person  string `json:"person"`
		Confirm bool   `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Role == "" {
		req.Role = store.RoleMember
	}
	if req.Kind == "" {
		req.Kind = store.KindGateway
	}
	elevated := req.Role == store.RoleOwner || req.Role == store.RoleAdmin
	if elevated && !p.Has(store.RoleOwner) {
		return errorf(http.StatusForbidden, "only owners can issue owner or admin tokens")
	}
	ev := store.AuditEvent{Actor: p.Name, ActorID: p.ActorID()}
	// F14 R-07: in the browser, an owner or admin control token is a
	// deliberate step: it does what the role does, without signing in, until
	// it expires. The audit marks it as made in the browser.
	if elevated && req.Kind == store.KindControl && p.Kind == KindSession {
		if !req.Confirm {
			return errorf(http.StatusUnprocessableEntity,
				"an %s control token can do everything an %s can, without signing in, for %s; send confirm: true to create it", req.Role, req.Role, plainDuration(a.TokenTTL))
		}
		ev.Client = "web"
	}
	// No UserID: a token an administrator issues never belongs to a member,
	// whatever person it names, so it cannot use anyone's accounts or teams.
	plain, rec, err := IssueToken(r.Context(), a.Store, p.OrgID, TokenSpec{Name: req.Name, Role: req.Role, Kind: req.Kind, Person: req.Person, TTL: a.TokenTTL,
		Event: ev})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": rec.ID, "token": plain, "name": rec.Name, "role": rec.Role, "kind": rec.Kind,
		"person": rec.Person, "bound": false, "expires_at": rec.ExpiresAt})
	return nil
}

func (a *API) revokeToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	target, err := a.Store.TokenByID(r.Context(), id)
	if err != nil || target.OrgID != p.OrgID || target.RevokedAt != nil {
		return errorf(http.StatusNotFound, "no active token %q", id)
	}
	if (target.Role == store.RoleOwner || target.Role == store.RoleAdmin) && !p.Has(store.RoleOwner) && target.ID != p.TokenID {
		return errorf(http.StatusForbidden, "only owners can revoke owner or admin tokens")
	}
	// A member's own token carries member (ADR 0021); it is protected by its
	// person's roles, as it was by its own role before.
	if target.UserID != "" && target.UserID != p.UserID && !p.Has(store.RoleOwner) {
		u, err := a.Store.UserByID(r.Context(), p.OrgID, target.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil && elevated(u.Roles) {
			return errorf(http.StatusForbidden, "only owners can revoke an owner's or an admin's tokens")
		}
	}
	if err := a.Store.RevokeToken(r.Context(), p.OrgID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "no active token %q", id)
		}
		return err
	}
	a.adminEvent(r.Context(), p, "token.revoke:"+id, "")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
	return nil
}

// ---- audit

// listAudit serves the audit log: the calls through the gateway by default
// (?kind=calls), or with ?kind=admin what the control API did and the
// bootstrap install, which are separate rows (R-09, independent review
// 2026-10-29).

func (a *API) openPayload(ctx context.Context, org store.OrgID, callID string, payload []byte) (json.RawMessage, error) {
	version := binary.BigEndian.Uint32(payload[:4])
	dek, err := a.Keys.DEK(ctx, org, version)
	if err != nil {
		return nil, err
	}
	pt, err := vault.Open(dek, payload[4:], gateway.AuditBinding(org, callID, version))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(pt), nil
}

func (a *API) patchAuditSettings(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		RecordArguments *bool `json:"record_arguments"`
		RecordResults   *bool `json:"record_results"`
		RetentionDays   *int  `json:"retention_days"` // 0 keeps events for ever (F12)
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	set := func(key string, v *bool) error {
		if v == nil {
			return nil
		}
		a.adminEvent(ctx, p, key+"="+strconv.FormatBool(*v), "")
		return a.Store.SetSetting(ctx, p.OrgID, key, strconv.FormatBool(*v))
	}
	if err := set(gateway.SettingRecordArguments, req.RecordArguments); err != nil {
		return err
	}
	if err := set(gateway.SettingRecordResults, req.RecordResults); err != nil {
		return err
	}
	if d := req.RetentionDays; d != nil {
		if *d < 0 || (*d > 0 && *d < audit.MinRetentionDays) || *d > audit.MaxRetentionDays {
			return errorf(http.StatusUnprocessableEntity, "retention_days must be 0 (keep for ever) or %d to %d", audit.MinRetentionDays, audit.MaxRetentionDays)
		}
		a.adminEvent(ctx, p, audit.SettingRetentionDays+"="+strconv.Itoa(*d), "")
		if err := a.Store.SetSetting(ctx, p.OrgID, audit.SettingRetentionDays, strconv.Itoa(*d)); err != nil {
			return err
		}
	}
	args, _ := a.Store.Setting(ctx, p.OrgID, gateway.SettingRecordArguments, "false")
	res, _ := a.Store.Setting(ctx, p.OrgID, gateway.SettingRecordResults, "false")
	days, _ := a.Store.Setting(ctx, p.OrgID, audit.SettingRetentionDays, "0")
	n, _ := strconv.Atoi(days)
	writeJSON(w, http.StatusOK, map[string]any{"record_arguments": args == "true", "record_results": res == "true", "retention_days": n})
	return nil
}

// reservedHeaders are hop-by-hop, transport or MCP protocol headers that
// stored credentials must never override.
var reservedHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Content-Type": true, "Transfer-Encoding": true, "Connection": true,
	"Keep-Alive": true, "Te": true, "Trailer": true, "Upgrade": true, "Accept": true,
}

// validHeaderName checks the RFC 9110 token grammar and the reserved list.
func validHeaderName(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !isAlnum && strings.IndexByte("!#$%&'*+-.^_`|~", c) < 0 {
			return false
		}
	}
	canon := http.CanonicalHeaderKey(k)
	if reservedHeaders[canon] || strings.HasPrefix(canon, "Proxy-") || strings.HasPrefix(canon, "Mcp-") || strings.HasPrefix(canon, "Sec-") {
		return false
	}
	return true
}

// ---- policies (F09)

type policyView struct {
	ID        string     `json:"id"`
	Server    string     `json:"server"`
	Tool      string     `json:"tool"`
	Subject   string     `json:"subject"`
	Action    string     `json:"action"`
	Priority  int        `json:"priority"`
	Note      string     `json:"note,omitempty"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// SubjectPerson names the member of a "user:" rule (F10 grants).
	SubjectPerson string `json:"subject_person,omitempty"`
	// Conditions on the call's arguments (ADR 0027).
	Conditions []policy.Condition `json:"conditions,omitempty"`
	// GrantOf is the approval a grant was made from (F10).
	GrantOf string `json:"grant_of,omitempty"`
}

func viewPolicy(p store.Policy) policyView {
	cs, _ := policy.RuleConditions(p)
	return policyView{ID: p.ID, Server: p.Server, Tool: p.Tool, Subject: p.Subject, Action: p.Action, Priority: p.Priority,
		Note: p.Note, CreatedBy: p.CreatedBy, ExpiresAt: p.ExpiresAt, Conditions: cs, GrantOf: p.GrantOf}
}

func (a *API) listPolicies(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ps, err := a.Store.ListPolicies(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	users, err := a.Store.ListUsers(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	person := map[string]string{}
	for _, u := range users {
		person[u.ID] = u.Person
	}
	out := make([]policyView, 0, len(ps))
	for _, pol := range ps {
		v := viewPolicy(pol)
		if id, ok := strings.CutPrefix(pol.Subject, "user:"); ok {
			v.SubjectPerson = person[id]
		}
		out = append(out, v)
	}
	def, _ := a.Store.Setting(r.Context(), p.OrgID, gateway.SettingPolicyDefault, store.ActionAllow)
	writeJSON(w, http.StatusOK, map[string]any{"policies": out, "default": def})
	return nil
}

func (a *API) createPolicy(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req ruleInput
	if err := decode(r, &req); err != nil {
		return err
	}
	pol, err := req.policy(p, time.Now())
	if err != nil {
		return err
	}
	if err := a.Store.CreatePolicies(r.Context(), p.OrgID, pol); err != nil {
		return ruleStoreError(err, pol)
	}
	a.adminEvent(r.Context(), p, fmt.Sprintf("policy.create:%s %s %s %s", pol.Action, pol.Server, pol.Tool, pol.Subject), pol.Server)
	writeJSON(w, http.StatusCreated, map[string]any{"policy": viewPolicy(pol)})
	return nil
}

// ruleStoreError turns the store's refusal of a rule into an answer.
func ruleStoreError(err error, pol store.Policy) error {
	switch {
	case errors.Is(err, store.ErrUnknownTeam):
		return errorf(http.StatusUnprocessableEntity, "there is no team %q", strings.TrimPrefix(pol.Subject, "team:"))
	case errors.Is(err, store.ErrUnknownUser):
		return errorf(http.StatusUnprocessableEntity, "there is no member %q", strings.TrimPrefix(pol.Subject, "user:"))
	}
	return err
}

func (a *API) deletePolicy(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	id := r.PathValue("id")
	if err := a.Store.DeletePolicy(r.Context(), p.OrgID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "no policy %q", id)
		}
		return err
	}
	a.adminEvent(r.Context(), p, "policy.delete:"+id, "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	return nil
}

// applyPreset turns a server's reviewed tools into per-tool rules.
func (a *API) applyPreset(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	var req struct {
		Preset string `json:"preset"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	st, ok := a.Upstream.State(s.ID)
	if !ok || st.Status != upstream.StatusUp {
		return errorf(http.StatusConflict, "server %q must be connected to apply a preset", s.Alias)
	}
	pins, err := a.Store.PinStatuses(r.Context(), p.OrgID, s.ID)
	if err != nil {
		return err
	}
	var tools []policy.PresetTool
	for _, it := range st.Items {
		if it.Kind == upstream.KindTool && pins[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] == store.PinAccepted {
			tools = append(tools, policy.PresetTool{Tool: it.Tool, Hash: it.Hash})
		}
	}
	if len(tools) == 0 {
		return errorf(http.StatusConflict, "server %q has no reviewed tools yet", s.Alias)
	}
	rules, err := policy.PresetRules(req.Preset, s.Alias, tools)
	if err != nil {
		return errorf(http.StatusUnprocessableEntity, "%v", err)
	}
	now := time.Now()
	out := make([]policyView, 0, len(rules))
	for i := range rules {
		rules[i].ID, rules[i].CreatedBy, rules[i].CreatedAt = ids.New(), p.Name, now
		out = append(out, viewPolicy(rules[i]))
	}
	if err := a.Store.CreatePolicies(r.Context(), p.OrgID, rules...); err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "policy.preset:"+req.Preset, s.Alias)
	writeJSON(w, http.StatusCreated, map[string]any{"policies": out})
	return nil
}

func (a *API) patchPolicySettings(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Default string `json:"default"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if !policy.ValidAction(req.Default) {
		return errorf(http.StatusUnprocessableEntity, "default must be allow, ask or deny")
	}
	if err := a.Store.SetSetting(r.Context(), p.OrgID, gateway.SettingPolicyDefault, req.Default); err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "policy.default="+req.Default, "")
	writeJSON(w, http.StatusOK, map[string]any{"default": req.Default})
	return nil
}

// ---- approvals (F10)

func (a *API) approvalView(ctx context.Context, org store.OrgID, ap store.Approval, withArgs bool) map[string]any {
	status := ap.EffectiveStatus(time.Now())
	v := map[string]any{
		"id": ap.ID, "status": status, "requester": ap.RequesterName, "requester_id": ap.RequesterID, "requester_person": ap.RequesterPerson,
		"server": ap.ServerAlias, "tool": ap.Tool, "reason": ap.Reason, "created_at": ap.CreatedAt, "expires_at": ap.ExpiresAt,
		"decided_by": ap.DecidedBy, "decided_at": ap.DecidedAt,
	}
	// Approvers see the full arguments while a decision or the call is still
	// open (ADR 0005); afterwards they are purged and never shown.
	if withArgs && (status == store.ApprovalPending || status == store.ApprovalApproved) {
		if args, err := a.Approval.Arguments(ctx, org, ap); err == nil {
			v["arguments"] = args
		} else {
			v["arguments_error"] = "cannot decrypt"
		}
	}
	return v
}

// markOwn tells a person which requests are their own, and why they cannot
// decide them (ADR 0025: a member who also approves sees their own calls).
// Decide refuses them in any case.
func markOwn(v map[string]any, ap store.Approval, p auth.Principal, allowSelf bool) map[string]any {
	if approval.Own(approval.Decider{TokenID: p.TokenID, UserID: p.UserID, Name: p.Name, Person: p.Person}, ap) {
		v["own"] = true
		if !allowSelf {
			v["decide_blocked"] = "This is your own request. Another approver decides it."
		}
	}
	return v
}

func (a *API) listApprovals(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	q := r.URL.Query()
	status := q.Get("status")
	allowSelf, err := a.Store.Setting(r.Context(), p.OrgID, approval.SettingAllowSelf, "false")
	if err != nil {
		return err
	}
	out := []map[string]any{}
	resp := map[string]any{}
	switch status {
	case store.ViewPending, store.ViewHistory:
		// Pages, newest first (F10): waiting requests with their arguments,
		// or the history without them (they are purged once decided).
		limit, _ := strconv.Atoi(q.Get("limit"))
		page, err := a.Store.ListApprovalsPage(r.Context(), p.OrgID, status, q.Get("before"), limit, time.Now())
		if errors.Is(err, store.ErrBadCursor) {
			return errorf(http.StatusBadRequest, "before must be the next value of a previous page")
		}
		if err != nil {
			return err
		}
		for _, ap := range page.Approvals {
			out = append(out, markOwn(a.approvalView(r.Context(), p.OrgID, ap, status == store.ViewPending), ap, p, allowSelf == "true"))
		}
		resp["next"] = page.Next
	case "", store.ApprovalApproved, store.ApprovalDenied, store.ApprovalConsumed, store.ApprovalWithdrawn:
		list, err := a.Store.ListApprovals(r.Context(), p.OrgID, status, 100)
		if err != nil {
			return err
		}
		for _, ap := range list {
			out = append(out, markOwn(a.approvalView(r.Context(), p.OrgID, ap, true), ap, p, allowSelf == "true"))
		}
	default:
		return errorf(http.StatusUnprocessableEntity, "unknown status %q", status)
	}
	a.adminEvent(r.Context(), p, "approvals.view:"+strconv.Itoa(len(out)), "")
	resp["approvals"] = out
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (a *API) getApproval(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ap, err := a.Store.GetApproval(r.Context(), p.OrgID, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return errorf(http.StatusNotFound, "no approval %q", r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "approvals.view:"+ap.ID, "")
	writeJSON(w, http.StatusOK, map[string]any{"approval": a.approvalView(r.Context(), p.OrgID, ap, true)})
	return nil
}

// grantLengths are the lengths a scoped grant may have (F10, ADR 0005).
var grantLengths = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "8h": 8 * time.Hour}

func (a *API) decide(approve bool) handler {
	return func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		id := r.PathValue("id")
		var req struct {
			Grant string `json:"grant"` // also allow the tool for the requester for this long
		}
		if err := decodeOptional(r, &req); err != nil {
			return err
		}
		grant, ok := grantLengths[req.Grant]
		switch {
		case req.Grant != "" && !approve:
			return errorf(http.StatusUnprocessableEntity, "only an approval grants anything")
		case req.Grant != "" && !ok:
			return errorf(http.StatusUnprocessableEntity, "grant must be 15m, 1h or 8h")
		}
		before, err := a.Store.GetApproval(r.Context(), p.OrgID, id)
		if errors.Is(err, store.ErrNotFound) {
			return errorf(http.StatusNotFound, "no approval %q", id)
		}
		if err != nil {
			return err
		}
		decider := approval.Decider{TokenID: p.TokenID, UserID: p.UserID, Name: p.Name, Person: p.Person}
		var planned store.Policy
		if grant > 0 {
			// Nobody allows a tool for themselves, even where they may approve
			// their own calls: a grant takes the person out of every later call.
			if approval.Own(decider, before) {
				return errorf(http.StatusForbidden, "you cannot allow a tool for yourself; approve this call alone")
			}
			if planned, err = a.planGrant(r.Context(), p, before, grant); err != nil {
				return err
			}
		}
		err = a.Approval.Decide(r.Context(), p.OrgID, id, decider, approve)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return errorf(http.StatusNotFound, "no approval %q", id)
		case errors.Is(err, approval.ErrSelfApproval):
			// A refused attempt to decide one's own request is worth a record.
			ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: p.Name, ActorID: p.ActorID(),
				Action: "approval." + map[bool]string{true: "approve", false: "deny"}[approve] + ":" + id, Decision: "deny", Outcome: "refused"}
			if aerr := a.Audit.Admin(r.Context(), p.OrgID, ev); aerr != nil {
				a.Logger.Error("audit admin event", "action", ev.Action, "err", aerr)
			}
			return errorf(http.StatusForbidden, "%v", err)
		case errors.Is(err, approval.ErrNotPending):
			return errorf(http.StatusConflict, "%v", err)
		case err != nil:
			return err
		}
		verb := "deny"
		if approve {
			verb = "approve"
		}
		a.adminEvent(r.Context(), p, "approval."+verb+":"+id, "")
		resp := map[string]any{}
		if grant > 0 {
			if err := a.Store.CreatePolicies(r.Context(), p.OrgID, planned); err != nil {
				a.Logger.Error("saving a grant after an approval", "approval", id, "err", err)
				return errorf(http.StatusInternalServerError, "the call is approved, but allowing the tool for longer failed; add a rule on Rules instead")
			}
			a.adminEvent(r.Context(), p, fmt.Sprintf("policy.grant:%s:%s:%s/%s:%s", planned.ID, planned.Subject, planned.Server, before.Tool, grant), planned.Server)
			resp["grant"] = viewPolicy(planned)
		}
		ap, _ := a.Store.GetApproval(r.Context(), p.OrgID, id)
		resp["approval"] = a.approvalView(r.Context(), p.OrgID, ap, false)
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
}

// planGrant builds the rule that allows an approved call's tool for its
// requester for d (F10, ADR 0005 scoped grants): one server, the exact tool,
// the reviewed definition that was asked about, the requester's member (or
// their token when it belongs to no member). The rules are evaluated again for
// the requester now: a grant is made only when the call would still ask. It
// is marked with the approval (GrantOf), so it turns an ask into allow and
// never a deny, whatever the deny's rank or conditions (ADR 0027).
func (a *API) planGrant(ctx context.Context, p auth.Principal, ap store.Approval, d time.Duration) (store.Policy, error) {
	refuse := func(why string) (store.Policy, error) {
		return store.Policy{}, errorf(http.StatusConflict, "%s Approve this call without allowing the tool for longer.", why)
	}
	tok, err := a.Store.TokenByID(ctx, ap.RequesterID)
	if err != nil || tok.OrgID != p.OrgID || tok.RevokedAt != nil {
		return refuse("The token that asked is gone.")
	}
	requester := auth.Principal{OrgID: p.OrgID, TokenID: tok.ID, Name: tok.Name, Roles: []string{tok.Role}, Kind: tok.Kind, Person: tok.Person, UserID: tok.UserID}
	acc, err := gateway.Access(ctx, a.Store, requester)
	if err != nil {
		return store.Policy{}, err
	}
	if !acc.SeesTool(ap.ServerID, ap.Tool) || (tok.UserID != "" && !auth.RolesCan(acc.Roles, auth.PermUseGateway)) {
		return refuse("The person who asked no longer sees this tool.")
	}
	pin, err := a.Store.PinDefinition(ctx, p.OrgID, ap.ServerID, store.PinKey{Kind: upstream.KindTool, Name: ap.Tool, Hash: ap.PinHash})
	if errors.Is(err, store.ErrNotFound) || (err == nil && pin.Status != store.PinAccepted) {
		return refuse("The tool's definition is no longer the accepted one.")
	}
	if err != nil {
		return store.Policy{}, err
	}
	var def mcp.Tool
	_ = json.Unmarshal([]byte(pin.Definition), &def) // annotations only tighten; none is the safe reading
	rules, err := a.Store.ListPolicies(ctx, p.OrgID)
	if err != nil {
		return store.Policy{}, err
	}
	dflt, err := a.Store.Setting(ctx, p.OrgID, gateway.SettingPolicyDefault, store.ActionAllow)
	if err != nil {
		return store.Policy{}, err
	}
	// The approved call's own arguments, for rules with conditions (ADR 0027).
	args, err := a.Approval.Arguments(ctx, p.OrgID, ap)
	if err != nil {
		return refuse("The call's arguments can no longer be read.")
	}
	now := time.Now()
	dec := policy.Evaluate(rules, policy.Request{Roles: gateway.CallerRoles(requester, acc), TokenID: tok.ID, UserID: tok.UserID, Teams: acc.Teams,
		Server: ap.ServerAlias, Tool: ap.Tool, PinHash: ap.PinHash, Annotations: def.Annotations, Args: args}, dflt, now)
	if dec.Action != store.ActionAsk {
		return refuse("The rules changed while this request waited: this call would now be " + map[string]string{store.ActionAllow: "allowed", store.ActionDeny: "denied"}[dec.Action] + ".")
	}
	// The grant covers asks up to the rank of the one that asks now; the
	// default's ask is below every rule (ADR 0027, security review 2026-11-20).
	covers := -1000
	for _, r := range rules {
		if r.ID == dec.RuleID {
			covers = r.Priority
		}
	}
	subject := "token:" + tok.ID
	if tok.UserID != "" {
		subject = "user:" + tok.UserID
	}
	exp := now.Add(d)
	return store.Policy{ID: ids.New(), Server: ap.ServerAlias, Tool: policy.ExactTool(ap.Tool), Subject: subject, Action: store.ActionAllow,
		GrantOf: ap.ID, Priority: covers, PinHash: ap.PinHash, Note: fmt.Sprintf("Allowed for %s by %s from approval %s", ap.RequesterPerson, p.Name, ap.ID),
		CreatedBy: p.Name, CreatedAt: now, ExpiresAt: &exp}, nil
}

func (a *API) patchApprovalSettings(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		AllowSelf *bool `json:"allow_self"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.AllowSelf == nil {
		return errorf(http.StatusUnprocessableEntity, "nothing to change")
	}
	if err := a.Store.SetSetting(r.Context(), p.OrgID, approval.SettingAllowSelf, strconv.FormatBool(*req.AllowSelf)); err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "approvals.allow_self="+strconv.FormatBool(*req.AllowSelf), "")
	writeJSON(w, http.StatusOK, map[string]any{"allow_self": *req.AllowSelf})
	return nil
}

// plainDuration says a lifetime in whole days, or hours under two days.
func plainDuration(d time.Duration) string {
	if h := int(d.Round(time.Hour) / time.Hour); h < 48 {
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	return fmt.Sprintf("%d days", int(d.Round(24*time.Hour)/(24*time.Hour)))
}

// serverRevocations reads, before a server's grants are deleted, what to
// revoke at its provider (F07 R-04). Nothing without the grants service.
func (a *API) serverRevocations(ctx context.Context, org store.OrgID, s store.Server) []grants.Revocation {
	if a.Grants == nil || !s.Config.PerPerson() {
		return nil
	}
	gs, err := a.Store.ListServerGrants(ctx, org, s.ID)
	if err != nil {
		return nil
	}
	return a.Grants.Revocations(ctx, org, s, gs)
}

// userRevocations reads, before a member's grants are deleted, what to
// revoke at each provider (F07 R-04).
func (a *API) userRevocations(ctx context.Context, org store.OrgID, userID string) []grants.Revocation {
	if a.Grants == nil {
		return nil
	}
	gs, err := a.Store.ListGrants(ctx, org, userID)
	if err != nil {
		return nil
	}
	var out []grants.Revocation
	for _, g := range gs {
		s, err := a.Store.ServerByID(ctx, org, g.ServerID)
		if err != nil {
			continue
		}
		out = append(out, a.Grants.Revocations(ctx, org, s, []store.Grant{g})...)
	}
	return out
}

func (a *API) revokeLater(ctx context.Context, rs []grants.Revocation) {
	if a.Grants != nil {
		a.Grants.RevokeLater(ctx, rs)
	}
}

// unusedAfter is how long a connection may go unused before the list marks it.
const unusedAfter = 90 * 24 * time.Hour

// listAllConnections lists everyone's connections across servers (F07 A):
// who connected what, of which kind, when, and whether it went unused. It
// never holds a token. Filters: person, server; a cursor: after.
func (a *API) listAllConnections(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	gs, next, err := a.Store.ListAllGrants(r.Context(), p.OrgID, store.GrantQuery{Person: q.Get("person"), Server: q.Get("server"), After: q.Get("after"), Limit: limit})
	if errors.Is(err, store.ErrBadGrantQuery) {
		return errorf(http.StatusBadRequest, "after must be the next value of a previous page")
	}
	if err != nil {
		return err
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		kind := "oauth"
		if g.Kind == store.PurposeKeyGrant {
			kind = "key"
		}
		out = append(out, map[string]any{"server": g.Alias, "person": g.Person, "kind": kind, "scopes": g.Scopes, "catalog": g.Catalog,
			"connected_at": g.ConnectedAt, "last_used_at": g.LastUsedAt, "refreshed_at": g.RefreshedAt,
			"unused": now.Sub(g.LastUsedAt) > unusedAfter})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out, "next": next})
	return nil
}
