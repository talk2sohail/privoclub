package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/oidcauth"
	"privo.club/privo/internal/store"
)

// Sign-in providers (ADR 0034): GitHub (ADR 0016) and one OpenID Connect
// provider. Either may be configured, or both. Who a person is follows from
// the provider and its subject alone; nothing is matched by email or name.

// Provider kinds, as the routes and the sealed cookie name them.
const (
	kindGitHub = "github"
	kindOIDC   = "oidc"
)

// identity is who a provider says signed in.
type identity struct {
	Provider string // the store's key: store.ProviderGitHub or the OIDC issuer
	Kind     string // kindGitHub or kindOIDC
	Subject  string
	Login    string // display snapshot: a GitHub login, an email
	Name     string // display name
	person   string // the person name a setup claim suggests
}

// ref names the identity in the audit's actor id, short and without the
// issuer.
func (i identity) ref() string { return i.Kind + ":" + i.Subject }

// item names the identity in full in the audit's item: the provider (the
// issuer, for OIDC) and the subject, so a sign-in can be traced to the
// provider's account (ADR 0034, residual risks; review L-3).
func (i identity) item() string { return auditItem(i.Provider, i.Subject) }

func auditItem(provider, subject string) string {
	return cleanText(provider, 200) + "#" + cleanText(subject, 255)
}

func (i identity) display() string {
	if i.Name != "" {
		return i.Name
	}
	return i.Login
}

func githubIdentity(p githubauth.Profile) identity {
	return identity{Provider: store.ProviderGitHub, Kind: kindGitHub, Subject: p.Subject(), Login: p.Login, Name: displayName(p),
		person: strings.ToLower(p.Login)}
}

func (s *Server) oidcIdentity(id oidcauth.Identity) identity {
	login := cleanText(id.Email, 254)
	if login == "" {
		login = cleanText(id.Username, 254)
	}
	if login == "" {
		login = id.Subject
	}
	hint := id.Username
	if local, _, ok := strings.Cut(id.Email, "@"); ok && local != "" {
		hint = local
	}
	return identity{Provider: s.o.OIDC.Issuer, Kind: kindOIDC, Subject: id.Subject, Login: login, Name: cleanText(id.Name, 100),
		person: personHint(hint)}
}

// personHint makes a person name from what a provider says: lower case,
// letters, digits, dots, dashes and underscores only, at most 64; "owner"
// when nothing is left.
func personHint(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() >= 64 {
			break
		}
		if r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "owner"
	}
	return b.String()
}

// cleanText drops control and format characters and cuts to n bytes: claims
// are shown on pages and written to the audit.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	for len(s) > n {
		s = s[:n]
		for len(s) > 0 && !utf8Start(s[len(s)-1:]) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func utf8Start(b string) bool { return len(b) > 0 && b[0]&0xC0 != 0x80 }

// label is a provider's name on pages.
func (s *Server) label(kind string) string {
	if kind == kindOIDC && s.o.OIDC != nil {
		return s.o.OIDCName
	}
	return "GitHub"
}

// labelFor names a stored identity's provider; one that is no longer
// configured says so.
func (s *Server) labelFor(provider string) string {
	switch {
	case provider == store.ProviderGitHub:
		return "GitHub"
	case s.o.OIDC != nil && provider == s.o.OIDC.Issuer:
		return s.o.OIDCName
	}
	return "a provider no longer set up (" + cleanText(provider, 80) + ")"
}

// usableProviders are the providers set up now, as identities name them.
func (s *Server) usableProviders() []string {
	var out []string
	if s.o.GitHub != nil {
		out = append(out, store.ProviderGitHub)
	}
	if s.o.OIDC != nil {
		out = append(out, s.o.OIDC.Issuer)
	}
	return out
}

// withButtons returns the provider buttons and, once discovery has run,
// sets the page's CSP again: wrap set it before the OIDC provider's sign-in
// address was known (review I-3).
func (s *Server) withButtons(w http.ResponseWriter, r *http.Request) []button {
	b := s.buttons(r.Context())
	w.Header().Set("Content-Security-Policy", s.policy())
	return b
}

// button is one way to sign in, on the sign-in, setup and invite pages.
type button struct{ Action, Label string }

// buttons lists the configured providers, GitHub first.
func (s *Server) buttons(ctx context.Context) []button {
	var out []button
	if s.o.GitHub != nil {
		out = append(out, button{"/auth/github/start", "Continue with GitHub"})
	}
	if s.o.OIDC != nil {
		// Discovery now, so the page's CSP names the provider's sign-in
		// address (form-action); a failure leaves the button, which then
		// says the provider cannot be reached.
		_ = s.o.OIDC.Ready(ctx)
		out = append(out, button{"/auth/oidc/start", "Continue with " + s.o.OIDCName})
	}
	return out
}

// startOIDC sends the browser to the OIDC provider (ADR 0034 point 3).
func (s *Server) startOIDC(w http.ResponseWriter, r *http.Request) { s.begin(w, r, kindOIDC) }

// startGitHub sends the browser to GitHub (ADR 0016).
func (s *Server) startGitHub(w http.ResponseWriter, r *http.Request) { s.begin(w, r, kindGitHub) }

// oidcCallback finishes an OIDC sign-in. As with GitHub, the purpose, the
// link being claimed, the verifier and the nonce come from the sealed cookie
// alone, and the identity from the ID token's issuer and subject alone.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	in, code, ok := s.callbackStart(w, r, kindOIDC)
	if !ok {
		return
	}
	id, err := s.o.OIDC.Exchange(r.Context(), code, s.oidcCallbackURL, in.Verifier, in.Nonce)
	switch {
	case errors.Is(err, oidcauth.ErrNotAllowed):
		s.notAllowed(w, r, id)
		return
	case errors.Is(err, oidcauth.ErrRejected):
		s.log.Warn("web: OIDC sign-in refused", "err", err)
		s.signInFailed(w, http.StatusBadRequest, s.label(kindOIDC)+" did not accept the sign-in.")
		return
	case err != nil:
		s.providerDown(r.Context(), kindOIDC, err)
		s.signInFailed(w, http.StatusBadGateway, "Privo could not reach "+s.label(kindOIDC)+". Try again in a moment.")
		return
	}
	s.finish(w, r, in, s.oidcIdentity(id))
}

// notAllowed answers someone the domain limit refuses (ADR 0034 point 6).
// It is audited like a refused stranger, under the same bound.
func (s *Server) notAllowed(w http.ResponseWriter, r *http.Request, id oidcauth.Identity) {
	record, skipped := s.denials.record(kindOIDC+":"+id.Subject, false, s.now())
	s.deniedSummary(r.Context(), skipped)
	if record {
		ev := s.event("signin.refused:not_allowed", "unknown", kindOIDC+":"+cleanText(id.Subject, 255), "deny", "denied")
		if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
			s.log.Error("web: audit", "action", "signin.refused:not_allowed", "err", err)
		}
	}
	s.message(w, http.StatusForbidden, "This account cannot sign in here",
		"This Privo accepts only accounts of its own organisation. Sign in with your work account.")
}

// providerDown records that a provider could not be reached, for Health.
func (s *Server) providerDown(ctx context.Context, kind string, err error) {
	s.log.Warn("web: sign-in provider unreachable", "provider", kind, "err", err)
	if s.o.Diagnose != nil {
		s.o.Diagnose(ctx, s.label(kind)+" sign-in could not be reached")
	}
}

// finishLink binds the identity to the member who started linking (ADR 0034
// point 7). The callback must arrive in the same, still valid session as the
// start: the sealed cookie names both, and neither comes from the request.
func (s *Server) finishLink(w http.ResponseWriter, r *http.Request, in signIn, id identity) {
	sess, u, ok := s.lookup(r)
	if !ok || sess.ID != in.Session || u.ID != in.User {
		s.message(w, http.StatusForbidden, "Linking did not complete",
			"Linking must finish in the same signed-in browser that started it. Sign in and try again.")
		return
	}
	ev := s.event("identity.link:"+id.Kind, u.Person, "user:"+u.ID, "allow", "ok")
	ev.Item = id.item()
	err := s.o.Store.LinkIdentity(r.Context(), s.o.Org, u.ID, store.Identity{Provider: id.Provider, Subject: id.Subject, Login: id.Login, CreatedAt: s.now()}, ev)
	switch {
	case errors.Is(err, store.ErrIdentityExists):
		s.pageFor(w, http.StatusConflict, sess, u, "That account is already in use",
			"This "+s.label(id.Kind)+" account already signs someone in to this Privo, perhaps you. Nothing was changed.")
		return
	case err != nil:
		s.log.Error("web: link identity", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	redirect(w, "/admin/account?result=linked")
}

// ---- the app's account page: a member's own sign-ins

type identityView struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	Login     string `json:"login"`
	CreatedAt string `json:"created_at"`
}

type linkable struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Linked bool   `json:"linked"`
}

// identitiesAPI lists the signed-in member's own sign-ins and the providers
// they can link.
func (s *Server) identitiesAPI(w http.ResponseWriter, r *http.Request) {
	u, ok := s.appAuth(w, r)
	if !ok {
		return
	}
	ids, err := s.o.Store.Identities(r.Context(), s.o.Org, u.ID)
	if err != nil {
		s.log.Error("web: list identities", "err", err)
		apiError(w, http.StatusInternalServerError, "could not read your sign-ins")
		return
	}
	views := make([]identityView, 0, len(ids))
	has := map[string]bool{}
	for _, i := range ids {
		views = append(views, identityView{Provider: i.Provider, Name: s.labelFor(i.Provider), Subject: i.Subject, Login: i.Login,
			CreatedAt: i.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")})
		has[i.Provider] = true
	}
	var can []linkable
	if s.o.GitHub != nil {
		can = append(can, linkable{Kind: kindGitHub, Name: "GitHub", Action: "/auth/github/start", Linked: has[store.ProviderGitHub]})
	}
	if s.o.OIDC != nil {
		can = append(can, linkable{Kind: kindOIDC, Name: s.o.OIDCName, Action: "/auth/oidc/start", Linked: has[s.o.OIDC.Issuer]})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"identities": views, "providers": can})
}

// unlinkAPI removes one of the signed-in member's own sign-ins, never the
// last (ADR 0034 point 7).
func (s *Server) unlinkAPI(w http.ResponseWriter, r *http.Request) {
	u, ok := s.appAuth(w, r)
	if !ok {
		return
	}
	sess, _, ok := s.lookup(r)
	if !ok {
		apiError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Subject  string `json:"subject"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Provider == "" || body.Subject == "" {
		apiError(w, http.StatusBadRequest, "name the provider and subject of the sign-in to remove")
		return
	}
	kind := kindOIDC
	if body.Provider == store.ProviderGitHub {
		kind = kindGitHub
	}
	ev := s.event("identity.unlink:"+kind, u.Person, "user:"+u.ID, "allow", "ok")
	ev.Item = auditItem(body.Provider, body.Subject)
	// Only providers set up now count as a way in; the member's other
	// sessions end with the identity (review L-4).
	err := s.o.Store.UnlinkIdentity(r.Context(), s.o.Org, u.ID, body.Provider, body.Subject, s.usableProviders(), sess.ID, ev)
	switch {
	case errors.Is(err, store.ErrLastIdentity):
		apiError(w, http.StatusConflict, "this is your only way to sign in; link another before removing it")
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "you have no such sign-in")
	case err != nil:
		s.log.Error("web: unlink identity", "err", err)
		apiError(w, http.StatusInternalServerError, "could not remove the sign-in")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
