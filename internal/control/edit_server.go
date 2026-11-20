package control

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"privo.club/privo/internal/auth"
	"privo.club/privo/internal/egress"
	"privo.club/privo/internal/grants"
	"privo.club/privo/internal/store"
)

// Editing a server (ADR 0029): its address, its static headers and, for an
// OAuth server whose client was entered by hand, its client. What was given
// for the old address (each person's grant or key, static header values,
// accepted definitions) never follows a new one.

// serverEdit is the part of PATCH /api/v1/servers/{alias} that edits.
type serverEdit struct {
	URL         *string           `json:"url"`
	Headers     map[string]string `json:"headers"` // "" removes a header
	OAuthClient *struct {
		ClientID     string  `json:"client_id"`
		ClientSecret *string `json:"client_secret"` // omitted keeps it, "" removes it
	} `json:"oauth_client"`
}

func (e serverEdit) any() bool { return e.URL != nil || e.Headers != nil || e.OAuthClient != nil }

// editServer applies an edit and returns the headers it dropped because the
// address changed and they were not given again.
func (a *API) editServer(ctx context.Context, p auth.Principal, s store.Server, req serverEdit) ([]string, error) {
	cfg := s.Config
	cfg.HeaderNames = slices.Clone(s.Config.HeaderNames)
	var edit store.ServerEdit
	var evs []store.AuditEvent
	var dropped []string
	given := map[string]string{} // canonical header name -> value
	for k, v := range req.Headers {
		if !validHeaderName(k) {
			return nil, errorf(http.StatusUnprocessableEntity, "header name %q is invalid or reserved", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, errorf(http.StatusUnprocessableEntity, "header %q has an invalid value", k)
		}
		given[http.CanonicalHeaderKey(k)] = v
	}
	static := s.Transport == "http" || s.Transport == "sse"

	if req.URL != nil {
		if !static {
			return nil, errorf(http.StatusUnprocessableEntity, "only http and sse servers have an address; a stdio server is removed and added again")
		}
		u, err := a.Egress.CheckURL(ctx, *req.URL, nil)
		if err != nil {
			if errors.Is(err, egress.ErrBlocked) {
				return nil, errorf(http.StatusUnprocessableEntity, "egress policy refused %s: %v", *req.URL, err)
			}
			return nil, errorf(http.StatusUnprocessableEntity, "%v", err)
		}
		if u.String() != cfg.URL {
			if cfg.Auth == store.AuthOAuth {
				return nil, errorf(http.StatusUnprocessableEntity,
					"%s uses each person's own account, set up for its current address; to use another address, remove it and add it again", s.Alias)
			}
			cfg.URL = u.String()
			// Nothing given for the old address goes to the new one.
			edit.EndConnections, edit.ReviewAgain, edit.NewAddress = true, true, true
			for _, name := range cfg.HeaderNames {
				if v, ok := given[name]; !ok || v == "" {
					dropped = append(dropped, name)
				}
			}
			evs = append(evs, event(p, "server.url", s.Alias))
		}
	}

	if req.Headers != nil {
		if !static {
			return nil, errorf(http.StatusUnprocessableEntity, "a stdio server takes env, not headers")
		}
		if cfg.PerPerson() {
			return nil, errorf(http.StatusUnprocessableEntity, "%s uses each person's own account or key and takes no static headers", s.Alias)
		}
		if len(given) == 0 {
			return nil, errorf(http.StatusUnprocessableEntity, "headers is empty: name a header to set, or give it \"\" to remove it")
		}
		secrets := map[string]string{}
		var changes []string
		for _, name := range sortedKeys(given) {
			v := given[name]
			had := slices.Contains(cfg.HeaderNames, name)
			switch {
			case v == "" && !had:
				return nil, errorf(http.StatusUnprocessableEntity, "%s has no header %s to remove", s.Alias, name)
			case v == "":
				changes = append(changes, "-"+name)
			case had:
				secrets["header:"+name] = v
				changes = append(changes, name)
			default:
				secrets["header:"+name] = v
				changes = append(changes, "+"+name)
			}
		}
		creds, err := a.seal(ctx, p.OrgID, s.ID, secrets)
		if err != nil {
			return nil, err
		}
		edit.SetCreds = append(edit.SetCreds, creds...)
		// Removals first in the event only for readability: +added, replaced, -removed.
		slices.SortStableFunc(changes, func(x, y string) int { return headerChangeRank(x) - headerChangeRank(y) })
		evs = append(evs, event(p, "server.headers:"+strings.Join(changes, ","), s.Alias))
	}
	for _, name := range dropped {
		if !slices.Contains(evsHeaderChanges(evs), "-"+name) {
			evs = append(evs, event(p, "server.headers:-"+name, s.Alias))
		}
	}
	// The final set of header names: what stays, what was set; never what was
	// removed or dropped.
	removed := map[string]bool{}
	for _, name := range dropped {
		removed[name] = true
	}
	for name, v := range given {
		if v == "" {
			removed[name] = true
		} else if !slices.Contains(cfg.HeaderNames, name) {
			cfg.HeaderNames = append(cfg.HeaderNames, name)
		}
	}
	cfg.HeaderNames = slices.DeleteFunc(cfg.HeaderNames, func(n string) bool { return removed[n] })
	slices.Sort(cfg.HeaderNames)
	for name := range removed {
		edit.RemoveCreds = append(edit.RemoveCreds, "header:"+name)
	}
	slices.Sort(edit.RemoveCreds)

	if req.OAuthClient != nil {
		if cfg.OAuth == nil {
			return nil, errorf(http.StatusUnprocessableEntity, "%s does not use each person's own account, so it has no OAuth client", s.Alias)
		}
		if r := cfg.OAuth.Registration; r != store.RegistrationAdmin && r != store.RegistrationManual {
			return nil, errorf(http.StatusUnprocessableEntity, "Privo registered its own client with %s's sign-in; there is no client to enter", s.Alias)
		}
		c := req.OAuthClient
		secret := ""
		if c.ClientSecret != nil {
			secret = *c.ClientSecret
		}
		if c.ClientID == "" || !printableASCII(c.ClientID, 255) || !printableASCII(secret, 512) {
			return nil, errorf(http.StatusUnprocessableEntity, "oauth_client.client_id is needed, and it and client_secret must be printable without spaces")
		}
		// F07 R-05 on this path too: a secret goes only to an authorization
		// server the admin named, never to one found from the MCP server.
		if secret != "" && !cfg.OAuth.IssuerNamed && cfg.OAuth.Registration != store.RegistrationManual {
			return nil, errorf(http.StatusUnprocessableEntity,
				"%s's sign-in was found from the server, not named by you, so it takes no client secret; to use one, delete the server and add it again with oauth.issuer", s.Alias)
		}
		o := *cfg.OAuth
		if c.ClientID != o.ClientID {
			// Refresh tokens belong to the client that got them.
			o.ClientID, edit.EndConnections = c.ClientID, true
		}
		if c.ClientSecret != nil {
			edit.RemoveCreds = append(edit.RemoveCreds, store.PurposeOAuthClient)
			o.HasClientSecret, o.TokenAuthMethod = false, "none"
			if secret != "" {
				sc, err := a.Grants.SealClientSecret(ctx, p.OrgID, s.ID, secret)
				if err != nil {
					return nil, err
				}
				edit.SetCreds = append(edit.SetCreds, sc)
				o.HasClientSecret, o.TokenAuthMethod = true, cfg.OAuth.TokenAuthMethod
				if o.TokenAuthMethod == "" || o.TokenAuthMethod == "none" {
					o.TokenAuthMethod = "client_secret_basic"
				}
			}
		}
		cfg.OAuth = &o
		evs = append(evs, event(p, "server.oauth_client", s.Alias))
	}

	if len(evs) == 0 {
		return nil, nil // the same address again: nothing changes
	}
	edit.Config, edit.Version = cfg, s.UpdatedAt
	// Connections an edit ends are revoked at the provider with the settings
	// they were given under, read before the edit (F07 R-04).
	var revoke []grants.Revocation
	if edit.EndConnections {
		revoke = a.serverRevocations(ctx, p.OrgID, s)
	}
	if _, err := a.Store.EditServer(ctx, p.OrgID, s.ID, edit, evs); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, errorf(http.StatusConflict, "%s changed since this edit was started; reload it and edit again", s.Alias)
		}
		return nil, err
	}
	a.revokeLater(ctx, revoke)
	// The connection restarts with the new settings (Sync sees updated_at);
	// pooled per-person sessions close with it.
	if err := a.Upstream.Sync(ctx, p.OrgID); err != nil {
		return nil, err
	}
	a.Upstream.WaitSettled(ctx, s.ID, ConnectWait)
	return dropped, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func headerChangeRank(c string) int {
	switch c[0] {
	case '+':
		return 0
	case '-':
		return 2
	}
	return 1
}

func evsHeaderChanges(evs []store.AuditEvent) []string {
	var out []string
	for _, e := range evs {
		if rest, ok := strings.CutPrefix(e.Action, "server.headers:"); ok {
			out = append(out, strings.Split(rest, ",")...)
		}
	}
	return out
}
