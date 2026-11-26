package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"privo.club/privo/internal/githubauth"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
)

// notFoundSetup is the one answer for every setup link that cannot be used:
// unknown, malformed, expired, used or replaced look the same.
func (s *Server) notFoundSetup(w http.ResponseWriter) {
	s.message(w, http.StatusNotFound, "This setup link does not work",
		"It may have been used already, it may have expired, or a newer one may have replaced it.",
		"Ask for a new one by running “privo admin setup-link” on the server.")
}

// setupPage shows the page behind a setup link. It never writes anything.
func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if !validSetupToken(token) {
		s.notFoundSetup(w)
		return
	}
	l, err := s.o.Store.SetupLink(r.Context(), s.o.Org, HashSecret(token), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: look up setup link", "err", err)
		}
		s.notFoundSetup(w)
		return
	}
	s.render(w, http.StatusOK, "setup", pageData{Title: "Set up Privo", SetupToken: token, Expires: l.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")})
}

// start sends the browser to GitHub. It is a POST so a page cannot start
// sign-ins for people by embedding a link; the origin check keeps other sites
// from posting it. Neither stops a script, so start writes nothing: the
// sign-in in flight goes into a sealed cookie (ADR 0016 amendment 1).
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if !s.form(w, r) {
		return
	}
	in := signIn{Purpose: purposeSignIn}
	if _, setup := r.PostForm["setup_token"]; setup {
		token := r.PostForm.Get("setup_token")
		if !validSetupToken(token) {
			s.notFoundSetup(w)
			return
		}
		l, err := s.o.Store.SetupLink(r.Context(), s.o.Org, HashSecret(token), s.now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("web: look up setup link", "err", err)
			}
			s.notFoundSetup(w)
			return
		}
		in.Purpose, in.Link = purposeSetup, l.ID
	} else if _, inv := r.PostForm["invite_token"]; inv {
		token := r.PostForm.Get("invite_token")
		if !validInviteToken(token) {
			s.notFoundInvite(w)
			return
		}
		inv, err := s.o.Store.Invite(r.Context(), s.o.Org, HashSecret(token), s.now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("web: look up invite", "err", err)
			}
			s.notFoundInvite(w)
			return
		}
		in.Purpose, in.Link = purposeInvite, inv.ID
	} else if next := r.PostForm.Get("return_to"); s.returnOK(next) {
		in.ReturnTo = next
	}

	state, err1 := randomSecret()
	verifier, err2 := githubauth.NewVerifier()
	if err := errors.Join(err1, err2); err != nil {
		s.log.Error("web: random", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	in.Verifier, in.Expires = verifier, s.now().Add(StateTTL).Unix()
	cookie, err := s.sealSignIn(r.Context(), state, in)
	if err != nil {
		s.log.Error("web: seal the sign-in cookie", "err", err)
		s.message(w, http.StatusInternalServerError, "Could not start sign-in", "Try again.")
		return
	}
	s.setCookie(w, s.signInCookie, cookie, int(StateTTL.Seconds()))
	redirect(w, s.o.GitHub.AuthorizeURL(s.callback, state, githubauth.Challenge(verifier)))
}

func (s *Server) signInFailed(w http.ResponseWriter, status int, lines ...string) {
	s.messageLink(w, status, "Sign-in did not complete", "/admin/signin", "Start again", lines...)
}

// callbackHandler finishes a sign-in. The browser must bring the state it was
// sent to GitHub with and the cookie that was sealed for that state. The
// purpose, the link being claimed and the PKCE verifier come from the cookie
// alone, never from the request.
func (s *Server) callbackHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	const notHere = "This sign-in was not started in this browser, or it has expired."
	var (
		in signIn
		ok bool
	)
	if c, err := r.Cookie(s.signInCookie); err == nil {
		in, ok = s.openSignIn(r.Context(), state, c.Value)
	}
	if !ok {
		// The cookie is left as it is: a request that does not belong to the
		// sign-in in this browser (a link planted on another site, say) must
		// not be able to cancel it (F05 R-03).
		s.signInFailed(w, http.StatusBadRequest, notHere)
		return
	}
	s.clearCookie(w, s.signInCookie)
	if !s.finished.first(state, s.now(), StateTTL) {
		s.signInFailed(w, http.StatusBadRequest, notHere)
		return
	}
	if q.Get("error") != "" || code == "" || len(code) > maxCodeLen {
		s.signInFailed(w, http.StatusBadRequest, "GitHub did not complete the sign-in.")
		return
	}

	ghCtx := store.WithOrg(r.Context(), s.o.Org)
	token, err := s.o.GitHub.Exchange(ghCtx, code, s.callback, in.Verifier)
	if err != nil {
		s.githubFailed(w, "exchange the code", err)
		return
	}
	prof, err := s.o.GitHub.User(ghCtx, token)
	if err != nil {
		s.githubFailed(w, "read the profile", err)
		return
	}
	// F05 A: only members of the configured organisations sign in, also
	// with a valid invite or setup link.
	if len(s.o.GitHub.OrgIDs) > 0 {
		orgs, err := s.o.GitHub.Orgs(ghCtx, token)
		if errors.Is(err, githubauth.ErrOrgsHidden) {
			s.notInOrg(w, r, prof) // not shown is not a member, and it is audited
			return
		}
		if err != nil {
			s.githubFailed(w, "read the organisations", err)
			return
		}
		if !s.o.GitHub.InOrgs(orgs) {
			s.notInOrg(w, r, prof)
			return
		}
	}

	switch in.Purpose {
	case purposeSetup:
		s.finishSetup(w, r, in, prof)
	case purposeInvite:
		s.finishInvite(w, r, in, prof)
	case purposeSignIn:
		s.finishSignIn(w, r, in, prof)
	default: // openSignIn accepts no other
		s.signInFailed(w, http.StatusBadRequest, notHere)
	}
}

// githubFailed answers a failed call to GitHub. What GitHub said is never
// repeated: a refusal is a refusal, and anything else is "could not reach".
func (s *Server) githubFailed(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, githubauth.ErrRejected) {
		s.signInFailed(w, http.StatusBadRequest, "GitHub did not accept the sign-in.")
		return
	}
	s.log.Warn("web: GitHub failed", "step", what, "err", err)
	s.signInFailed(w, http.StatusBadGateway, "Privo could not reach GitHub. Try again in a moment.")
}

func displayName(p githubauth.Profile) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Login
}

// notInOrg answers a GitHub account outside the organisations sign-in is
// limited to (F05 A). It is audited like a refused stranger, with the same
// bound on how many rows a stranger can make Privo write.
func (s *Server) notInOrg(w http.ResponseWriter, r *http.Request, p githubauth.Profile) {
	record, skipped := s.denials.record(p.Subject(), false, s.now())
	s.deniedSummary(r.Context(), skipped)
	if record {
		ev := s.event("signin.refused:not_in_org", p.Login, "github:"+p.Subject(), "deny", "denied")
		if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
			s.log.Error("web: audit", "action", "signin.refused:not_in_org", "err", err)
		}
	}
	s.message(w, http.StatusForbidden, "This GitHub account cannot sign in",
		"This Privo accepts only members of its GitHub organisation. If you are one, check that the organisation lets you share your membership with Privo's GitHub app.")
}

// denied is the single answer for an account that is not a member: a stranger
// and a disabled member see the same page.
//
// A refusal is the one thing a person who is not a member can make Privo
// write, and anyone with a GitHub account can be refused as often as they
// like. So the audit gets one row per account in ten minutes and at most
// maxDenials rows in ten minutes over all strangers; a disabled member is
// always recorded. What is left out is counted, and the count is written to
// the audit as one row when the window ends (F05 R-02; review of 2026-11-15,
// S-06).
func (s *Server) denied(w http.ResponseWriter, r *http.Request, p githubauth.Profile, member bool) {
	record, skipped := s.denials.record(p.Subject(), member, s.now())
	s.deniedSummary(r.Context(), skipped)
	if record {
		ev := s.event("signin.denied", p.Login, "github:"+p.Subject(), "deny", "denied")
		if err := s.o.Audit.Admin(r.Context(), s.o.Org, ev); err != nil {
			s.log.Error("web: audit", "action", "signin.denied", "err", err)
		}
	}
	s.message(w, http.StatusForbidden, "This GitHub account cannot sign in",
		"It is not a member of this Privo. Ask an owner to add you.")
}

// deniedSummary writes how many refused sign-ins a window left out of the
// audit, as one row, when there were any.
func (s *Server) deniedSummary(ctx context.Context, n int) {
	if n <= 0 {
		return
	}
	s.log.Warn("web: refused sign-ins left out of the audit in the last ten minutes", "count", n)
	ev := s.event(fmt.Sprintf("signin.denied.skipped:%d", n), "system", "system", "deny", "denied")
	if err := s.o.Audit.Admin(context.WithoutCancel(ctx), s.o.Org, ev); err != nil {
		s.log.Error("web: audit", "action", "signin.denied.skipped", "err", err)
	}
}

func (s *Server) finishSignIn(w http.ResponseWriter, r *http.Request, in signIn, p githubauth.Profile) {
	u, err := s.o.Store.UserByIdentity(r.Context(), s.o.Org, store.ProviderGitHub, p.Subject())
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.DisabledAt != nil) {
		s.denied(w, r, p, err == nil)
		return
	}
	if err != nil {
		s.log.Error("web: look up member", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	secret, sess, err := s.newSession(u.ID)
	if err == nil {
		err = s.o.Store.CreateSession(r.Context(), s.o.Org, sess, s.event("signin", u.Person, "user:"+u.ID, "allow", "ok"))
	}
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	to := s.landing(u)
	if s.returnOK(in.ReturnTo) {
		to = in.ReturnTo
	}
	redirect(w, to)
}

func (s *Server) finishSetup(w http.ResponseWriter, r *http.Request, in signIn, p githubauth.Profile) {
	secret, sess, err := s.newSession("")
	if err != nil {
		s.log.Error("web: start session", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	userID := ids.New()
	sess.UserID = userID
	u, err := s.o.Store.ClaimSetup(r.Context(), s.o.Org, store.Claim{
		LinkID: in.Link, UserID: userID, Person: strings.ToLower(p.Login), Subject: p.Subject(), Login: p.Login,
		DisplayName: displayName(p), Session: sess, Now: s.now(),
	}, s.event("setup.claim", strings.ToLower(p.Login), "github:"+p.Subject(), "allow", "ok"))
	switch {
	case errors.Is(err, store.ErrSetupUnavailable):
		s.notFoundSetup(w)
		return
	case errors.Is(err, store.ErrIdentityExists):
		s.messageLink(w, http.StatusConflict, "This GitHub account is already a member", "/admin/signin", "Sign in instead",
			"The setup link has not been used up. It still works for another account.")
		return
	case errors.Is(err, store.ErrConflict):
		s.message(w, http.StatusConflict, "That person name is already taken",
			"Another member already uses the name “"+strings.ToLower(p.Login)+"”. The setup link has not been used up.",
			"Ask for a new link that names the person: privo admin setup-link --person NAME")
		return
	case err != nil:
		s.log.Error("web: claim setup link", "err", err)
		s.signInFailed(w, http.StatusInternalServerError, "Something went wrong on the server.")
		return
	}
	s.setCookie(w, s.sessionCookie, secret, int(s.o.MaxAge.Seconds()))
	redirect(w, s.landing(u))
}
