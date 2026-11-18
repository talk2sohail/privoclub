// Package control serves the versioned admin API under /api/v1.
// Every route authenticates the caller and checks a permission (ADR 0011).
// Responses never contain secrets.
package control

import (
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
	"sort"
	"strconv"
	"strings"
	"time"

	"privo.club/privo/internal/approval"
	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/gateway"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/policy"
	"privo.club/privo/internal/runner"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
	"privo.club/privo/internal/upstream"
	"privo.club/privo/internal/upstreamauth"
	"privo.club/privo/internal/vault"
)

// API is the admin API.
type API struct {
	Store    *store.Store
	Keys     *keyring.Keyring
	Auth     *auth.Authenticator
	Upstream *upstream.Manager
	Audit    *audit.Writer
	Approval *approval.Service
	Egress   egress.Policy
	Logger   *slog.Logger
	// TokenTTL is the default personal token lifetime.
	TokenTTL time.Duration

	// Upstream OAuth (ADR 0017): discovery and registration, the grants, and
	// the public URL the callback and the links are built from. Without them
	// servers cannot use each person's own account.
	OAuth     *upstreamauth.Client
	Grants    *grants.Service
	PublicURL *url.URL
	in        internal // the mux of in-process calls (Serve)
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
	mux.Handle("GET /api/v1/servers", wrap(auth.PermReadServers, a.listServers))
	mux.Handle("POST /api/v1/servers", wrap(auth.PermManageServers, a.createServer))
	mux.Handle("PATCH /api/v1/servers/{alias}", wrap(auth.PermManageServers, a.patchServer))
	mux.Handle("DELETE /api/v1/servers/{alias}", wrap(auth.PermManageServers, a.deleteServer))
	mux.Handle("GET /api/v1/servers/{alias}/pins", wrap(auth.PermManageServers, a.listPins))
	mux.Handle("GET /api/v1/servers/{alias}/connections", wrap(auth.PermManageServers, a.listConnections))
	mux.Handle("DELETE /api/v1/servers/{alias}/connections/{person}", wrap(auth.PermManageServers, a.deleteConnection))
	mux.Handle("POST /api/v1/invites", wrap(auth.PermManageTokens, a.createInvite))
	mux.Handle("POST /api/v1/servers/{alias}/pins:accept", wrap(auth.PermManageServers, a.acceptPins))
	mux.Handle("GET /api/v1/tokens", wrap(auth.PermManageTokens, a.listTokens))
	mux.Handle("POST /api/v1/tokens", wrap(auth.PermManageTokens, a.createToken))
	mux.Handle("DELETE /api/v1/tokens/{id}", wrap(auth.PermManageTokens, a.revokeToken))
	mux.Handle("GET /api/v1/policies", wrap(auth.PermManagePolicy, a.listPolicies))
	mux.Handle("POST /api/v1/policies", wrap(auth.PermManagePolicy, a.createPolicy))
	mux.Handle("DELETE /api/v1/policies/{id}", wrap(auth.PermManagePolicy, a.deletePolicy))
	mux.Handle("POST /api/v1/servers/{alias}/policy:preset", wrap(auth.PermManagePolicy, a.applyPreset))
	mux.Handle("PATCH /api/v1/settings/policy", wrap(auth.PermManagePolicy, a.patchPolicySettings))
	mux.Handle("GET /api/v1/approvals", wrap(auth.PermDecide, a.listApprovals))
	mux.Handle("GET /api/v1/approvals/{id}", wrap(auth.PermDecide, a.getApproval))
	mux.Handle("POST /api/v1/approvals/{id}/approve", wrap(auth.PermDecide, a.decide(true)))
	mux.Handle("POST /api/v1/approvals/{id}/deny", wrap(auth.PermDecide, a.decide(false)))
	mux.Handle("PATCH /api/v1/settings/approvals", wrap(auth.PermManagePolicy, a.patchApprovalSettings))
	mux.Handle("GET /api/v1/audit/events", wrap(auth.PermReadAudit, a.listAudit))
	mux.Handle("PATCH /api/v1/settings/audit", wrap(auth.PermManageAudit, a.patchAuditSettings))
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
	// Teams that reach the server (ADR 0020); shown to admins only, and
	// always to them, so an empty list ("nobody") is visible (F26 R-04).
	Teams *[]string `json:"teams,omitempty"`
}

// view describes a server. Members and auditors see neither the full target
// (URLs often carry keys) nor connection errors (they can contain them).
func (a *API) view(ctx context.Context, p auth.Principal, s store.Server, teams []string) serverView {
	admin := p.Can(auth.PermManageServers)
	v := serverView{ID: s.ID, Alias: s.Alias, Transport: s.Transport, Enabled: s.Enabled, Status: upstream.StatusConnecting, Items: map[string]int{}}
	if admin {
		t := append([]string{}, teams...)
		v.Teams = &t
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
	if s.Config.Auth == store.AuthOAuth {
		v.Auth, v.ConnectURL = store.AuthOAuth, a.connectURL(s.Alias)
		if s.Config.OAuth != nil && admin {
			v.Registration = s.Config.OAuth.Registration
		}
	}
	if st, ok := a.Upstream.State(s.ID); ok {
		v.Status = st.Status
		if admin {
			v.Error = st.Error
		}
		pins, _ := a.Store.PinStatuses(ctx, p.OrgID, s.ID)
		for _, it := range st.Items {
			v.Items[it.Kind]++
			if pins[store.PinKey{Kind: it.Kind, Name: it.Name, Hash: it.Hash}] == store.PinPending {
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
	out := make([]serverView, 0, len(servers))
	for _, s := range servers {
		if admin || acc.Reaches(s.ID) {
			out = append(out, a.view(r.Context(), p, s, st[s.ID]))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
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
	if err := a.Upstream.Sync(ctx, p.OrgID); err != nil {
		return err
	}
	// Report the first connection result, so the admin sees the status and
	// how many items wait for review in the response.
	a.Upstream.WaitSettled(ctx, srv.ID, ConnectWait)
	writeJSON(w, http.StatusCreated, map[string]any{"server": a.view(ctx, p, srv, srv.Teams)})
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
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Enabled == nil && req.Teams == nil {
		return errorf(http.StatusUnprocessableEntity, "nothing to change")
	}
	if req.Teams != nil {
		teams, err := cleanTeams(*req.Teams)
		if err != nil {
			return err
		}
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
	writeJSON(w, http.StatusOK, map[string]any{"server": a.view(r.Context(), p, s, st[s.ID])})
	return nil
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	s, err := a.server(r, p)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteServer(r.Context(), p.OrgID, s.ID); err != nil {
		return err
	}
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
	out := make([]map[string]any, 0, len(pins))
	for _, pin := range pins {
		out = append(out, map[string]any{"kind": pin.Kind, "name": pin.Name, "hash": pin.Hash, "status": pin.Status,
			"definition": json.RawMessage(pin.Definition)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pins": out})
	return nil
}

// acceptPins accepts exactly the reviewed definitions named in the request.
func (a *API) acceptPins(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
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
		return errorf(http.StatusUnprocessableEntity, "list the reviewed pins (kind, name, hash) to accept")
	}
	keys := make([]store.PinKey, 0, len(req.Pins))
	for _, pr := range req.Pins {
		keys = append(keys, store.PinKey{Kind: pr.Kind, Name: pr.Name, Hash: pr.Hash})
	}
	accepted, err := a.Store.AcceptPins(r.Context(), p.OrgID, s.ID, keys)
	if err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, fmt.Sprintf("pins.accept:%d", len(accepted)), s.Alias)
	writeJSON(w, http.StatusOK, map[string]any{"accepted": len(accepted), "requested": len(keys)})
	return nil
}

// ---- tokens

type tokenView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Person    string    `json:"person"`
	Personal  bool      `json:"personal"` // a member's own token (F07 R-01)
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
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

// TokenSpec describes a token to issue.
type TokenSpec struct {
	Name, Role, Kind, Person string
	// UserID binds the token to a member: only for a member's own token,
	// made while they are signed in (POST /api/v1/me/tokens). Every other
	// token is bound to no one and may not act as a member (F07 R-01).
	UserID string
	TTL    time.Duration
}

func viewToken(t store.APIToken) tokenView {
	return tokenView{ID: t.ID, Name: t.Name, Role: t.Role, Kind: t.Kind, Person: t.Person, Personal: t.UserID != "", CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt}
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
	// F07 R-01: a token that is not a member's own must not carry a member's
	// name. Nothing finds a member by it any more; refusing keeps the audit
	// and the token list honest about who a token is.
	if spec.UserID == "" {
		switch _, err := s.UserByPerson(ctx, org, person); {
		case err == nil:
			return "", store.APIToken{}, errorf(http.StatusUnprocessableEntity,
				"%q is a member of this organisation; members make their own tokens on the Tokens page while signed in. Name a service or script that is not a member", person)
		case !errors.Is(err, store.ErrNotFound):
			return "", store.APIToken{}, err
		}
	}
	tok, err := token.New()
	if err != nil {
		return "", store.APIToken{}, err
	}
	now := time.Now()
	rec := store.APIToken{OrgID: org, ID: tok.ID, Name: name, Role: role, Kind: spec.Kind, Person: person, UserID: spec.UserID, SecretHash: tok.SecretHash,
		CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := s.CreateToken(ctx, rec); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "", rec, errorf(http.StatusConflict, "an active token named %q already exists", name)
		}
		return "", rec, err
	}
	return tok.Plain, rec, nil
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Name   string `json:"name"`
		Role   string `json:"role"`
		Kind   string `json:"kind"`
		Person string `json:"person"`
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
	if (req.Role == store.RoleOwner || req.Role == store.RoleAdmin) && !p.Has(store.RoleOwner) {
		return errorf(http.StatusForbidden, "only owners can issue owner or admin tokens")
	}
	plain, rec, err := IssueToken(r.Context(), a.Store, p.OrgID, TokenSpec{Name: req.Name, Role: req.Role, Kind: req.Kind, Person: req.Person, TTL: a.TokenTTL})
	if err != nil {
		return err
	}
	a.adminEvent(r.Context(), p, "token.create:"+rec.Kind+":"+rec.Role+":"+rec.Name, "")
	writeJSON(w, http.StatusCreated, map[string]any{"id": rec.ID, "token": plain, "name": rec.Name, "role": rec.Role, "kind": rec.Kind,
		"person": rec.Person, "personal": false, "expires_at": rec.ExpiresAt})
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
func (a *API) listAudit(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	switch kind := r.URL.Query().Get("kind"); kind {
	case "", "calls":
	case "admin":
		return a.listAdminAudit(w, r, p, limit)
	default:
		return errorf(http.StatusBadRequest, "kind must be calls or admin")
	}
	calls, err := a.Store.RecentCalls(r.Context(), p.OrgID, limit)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		e := map[string]any{
			"id": c.Intent.CallID, "time": c.Intent.Time, "actor": c.Intent.Actor, "actor_id": c.Intent.ActorID, "server": c.Intent.Server,
			"item": c.Intent.Item, "action": c.Intent.Action, "decision": c.Intent.Decision, "outcome": "pending",
		}
		if c.Intent.Action == "tools/call" {
			e["tool"] = c.Intent.Item
		}
		if c.Intent.ApprovedBy != "" {
			e["approved_by"] = c.Intent.ApprovedBy
		}
		if c.Intent.Account != "" {
			e["account"] = c.Intent.Account
		}
		if c.Completion != nil {
			e["outcome"], e["latency_ms"] = c.Completion.Outcome, c.Completion.LatencyMS
		}
		if len(c.Intent.Payload) > 4 {
			if args, err := a.openPayload(r.Context(), p.OrgID, c.Intent.CallID, c.Intent.Payload); err == nil {
				e["arguments"] = args
			} else {
				e["arguments_error"] = "cannot decrypt"
			}
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
	return nil
}

func (a *API) listAdminAudit(w http.ResponseWriter, r *http.Request, p auth.Principal, limit int) error {
	events, err := a.Store.RecentAdminEvents(r.Context(), p.OrgID, limit)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		e := map[string]any{"id": ev.ID, "time": ev.Time, "actor": ev.Actor, "actor_id": ev.ActorID, "action": ev.Action, "decision": ev.Decision, "outcome": ev.Outcome}
		if ev.Server != "" {
			e["server"] = ev.Server
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
	return nil
}

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
	args, _ := a.Store.Setting(ctx, p.OrgID, gateway.SettingRecordArguments, "false")
	res, _ := a.Store.Setting(ctx, p.OrgID, gateway.SettingRecordResults, "false")
	writeJSON(w, http.StatusOK, map[string]any{"record_arguments": args == "true", "record_results": res == "true"})
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
}

func viewPolicy(p store.Policy) policyView {
	return policyView{ID: p.ID, Server: p.Server, Tool: p.Tool, Subject: p.Subject, Action: p.Action, Priority: p.Priority,
		Note: p.Note, CreatedBy: p.CreatedBy, ExpiresAt: p.ExpiresAt}
}

func (a *API) listPolicies(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	ps, err := a.Store.ListPolicies(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	out := make([]policyView, 0, len(ps))
	for _, pol := range ps {
		out = append(out, viewPolicy(pol))
	}
	def, _ := a.Store.Setting(r.Context(), p.OrgID, gateway.SettingPolicyDefault, store.ActionAllow)
	writeJSON(w, http.StatusOK, map[string]any{"policies": out, "default": def})
	return nil
}

func (a *API) createPolicy(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
	var req struct {
		Server    string `json:"server"`
		Tool      string `json:"tool"`
		Subject   string `json:"subject"`
		Action    string `json:"action"`
		Priority  int    `json:"priority"`
		Note      string `json:"note"`
		ExpiresIn string `json:"expires_in"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if req.Subject == "" {
		req.Subject = "*"
	}
	if req.Tool == "" {
		req.Tool = "*"
	}
	switch {
	case req.Server != "*" && !aliasRe.MatchString(req.Server):
		return errorf(http.StatusUnprocessableEntity, "server must be a server alias or *")
	case !policy.ValidGlob(req.Tool):
		return errorf(http.StatusUnprocessableEntity, "tool must be a name or glob pattern")
	case !policy.ValidSubject(req.Subject):
		return errorf(http.StatusUnprocessableEntity, "subject must be *, role:<role>, team:<slug> or token:<id>")
	case !policy.ValidAction(req.Action):
		return errorf(http.StatusUnprocessableEntity, "action must be allow, ask or deny")
	case req.Priority < -1000 || req.Priority > 1000:
		return errorf(http.StatusUnprocessableEntity, "priority must be between -1000 and 1000")
	case len(req.Note) > 500:
		return errorf(http.StatusUnprocessableEntity, "note is too long")
	}
	pol := store.Policy{ID: ids.New(), Server: req.Server, Tool: req.Tool, Subject: req.Subject, Action: req.Action,
		Priority: req.Priority, Note: req.Note, CreatedBy: p.Name, CreatedAt: time.Now()}
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 || d > 90*24*time.Hour {
			return errorf(http.StatusUnprocessableEntity, "expires_in must be a duration up to 2160h")
		}
		exp := time.Now().Add(d)
		pol.ExpiresAt = &exp
	}
	if err := a.Store.CreatePolicies(r.Context(), p.OrgID, pol); err != nil {
		if errors.Is(err, store.ErrUnknownTeam) {
			return errorf(http.StatusUnprocessableEntity, "there is no team %q", strings.TrimPrefix(pol.Subject, "team:"))
		}
		return err
	}
	a.adminEvent(r.Context(), p, fmt.Sprintf("policy.create:%s %s %s %s", pol.Action, pol.Server, pol.Tool, pol.Subject), pol.Server)
	writeJSON(w, http.StatusCreated, map[string]any{"policy": viewPolicy(pol)})
	return nil
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
// decide them (ADR 0021: a member who also approves sees their own calls).
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
	status := r.URL.Query().Get("status")
	if status != "" && status != store.ApprovalPending && status != store.ApprovalApproved && status != store.ApprovalDenied &&
		status != store.ApprovalConsumed && status != store.ApprovalWithdrawn {
		return errorf(http.StatusUnprocessableEntity, "unknown status %q", status)
	}
	list, err := a.Store.ListApprovals(r.Context(), p.OrgID, status, 100)
	if err != nil {
		return err
	}
	allowSelf, err := a.Store.Setting(r.Context(), p.OrgID, approval.SettingAllowSelf, "false")
	if err != nil {
		return err
	}
	now := time.Now()
	out := make([]map[string]any, 0, len(list))
	for _, ap := range list {
		if status == store.ApprovalPending && ap.EffectiveStatus(now) != store.ApprovalPending {
			continue
		}
		out = append(out, markOwn(a.approvalView(r.Context(), p.OrgID, ap, true), ap, p, allowSelf == "true"))
	}
	a.adminEvent(r.Context(), p, "approvals.view:"+strconv.Itoa(len(out)), "")
	writeJSON(w, http.StatusOK, map[string]any{"approvals": out})
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

func (a *API) decide(approve bool) handler {
	return func(w http.ResponseWriter, r *http.Request, p auth.Principal) error {
		id := r.PathValue("id")
		err := a.Approval.Decide(r.Context(), p.OrgID, id, approval.Decider{TokenID: p.TokenID, UserID: p.UserID, Name: p.Name, Person: p.Person}, approve)
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
		ap, _ := a.Store.GetApproval(r.Context(), p.OrgID, id)
		writeJSON(w, http.StatusOK, map[string]any{"approval": a.approvalView(r.Context(), p.OrgID, ap, false)})
		return nil
	}
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
