package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"privo.club/privo/internal/config"
	"privo.club/privo/internal/control"
	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/web"
)

const (
	adminUsage = "usage: privo admin setup-link [--person NAME]\n       privo admin invite --person NAME [--role admin|auditor|approver|member]"

	// setupLinkTTL is how long a setup link works. It is short on purpose: the
	// link makes whoever opens it the owner.
	setupLinkTTL = time.Hour

	// maxPersonLen is the longest person name a token can carry too.
	maxPersonLen = 128
)

// adminCmd runs `privo admin ...`, the commands that set up the admin site.
func adminCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	switch {
	case len(args) > 0 && args[0] == "setup-link":
		return setupLinkCmd(args[1:], stdout, stderr, getenv)
	case len(args) > 0 && args[0] == "invite":
		return inviteCmd(args[1:], stdout, stderr, getenv)
	}
	_, _ = fmt.Fprintln(stderr, adminUsage)
	return 2
}

// inviteCmd prints a one-time link that makes whoever signs in with it a member
// with the given person name and role (ADR 0017). Only its hash is stored.
func inviteCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	person, role := "", store.RoleMember
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if flag != "--person" && flag != "--role" {
			_, _ = fmt.Fprintf(stderr, "privo admin invite: unexpected argument %q\n%s\n", flag, adminUsage)
			return 2
		}
		if i++; i >= len(args) {
			_, _ = fmt.Fprintf(stderr, "privo admin invite: %s needs a value\n%s\n", flag, adminUsage)
			return 2
		}
		if flag == "--person" {
			person = strings.TrimSpace(args[i])
		} else {
			role = args[i]
		}
	}
	if !validPerson(person) {
		_, _ = fmt.Fprintf(stderr, "privo admin invite: --person is required: 1 to %d characters without control characters\n%s\n", maxPersonLen, adminUsage)
		return 2
	}
	if !store.ValidRole(role) || role == store.RoleOwner {
		_, _ = fmt.Fprintf(stderr, "privo admin invite: --role must be admin, auditor, approver or member\n%s\n", adminUsage)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	if cfg.PublicURL == nil || cfg.PublicURL.Path != "" {
		_, _ = fmt.Fprintln(stderr, "privo admin invite: PRIVO_PUBLIC_URL must be set, without a path: the link points at the admin site")
		return 2
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		st, err = store.OpenShared(ctx, cfg.DataDir) // the server is running
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: time.Now(), Actor: "cli", ActorID: "cli", Client: "cli", Decision: "allow", Outcome: "ok"}
	link, _, err := control.CreateInvite(ctx, st, org.ID, cfg.PublicURL, person, role, "cli", ev)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo admin invite: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, link)
	_, _ = fmt.Fprintf(stderr, "Give this link to %s. It works once, within %d hours, and makes the GitHub account they sign in with a %s. A new invite for the same person cancels this one.\n",
		person, int(control.InviteTTL.Hours()), role)
	return 0
}

// validPerson accepts the names tokens use for a person: text without control
// or format characters.
func validPerson(p string) bool {
	if p == "" || len(p) > maxPersonLen {
		return false
	}
	return !strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// setupLinkCmd prints a one-time URL that makes the first person to sign in
// with GitHub the owner. Only the hash of its secret is stored.
func setupLinkCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	person := ""
	for i := 0; i < len(args); i++ {
		if args[i] != "--person" {
			_, _ = fmt.Fprintf(stderr, "privo admin setup-link: unexpected argument %q\n%s\n", args[i], adminUsage)
			return 2
		}
		if i++; i >= len(args) {
			_, _ = fmt.Fprintf(stderr, "privo admin setup-link: --person needs a name\n%s\n", adminUsage)
			return 2
		}
		person = strings.TrimSpace(args[i])
		if !validPerson(person) {
			_, _ = fmt.Fprintf(stderr, "privo admin setup-link: --person must be 1 to %d characters without control characters\n%s\n", maxPersonLen, adminUsage)
			return 2
		}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	if cfg.PublicURL == nil {
		_, _ = fmt.Fprintln(stderr, "privo admin setup-link: PRIVO_PUBLIC_URL is not set. The link must point at the address people open Privo at, for example https://privo.example.com")
		return 2
	}
	if cfg.PublicURL.Path != "" {
		_, _ = fmt.Fprintln(stderr, "privo admin setup-link: PRIVO_PUBLIC_URL must be the address without a path; the admin site cannot run under a path prefix")
		return 2
	}
	webCfg, err := config.LoadWeb(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		st, err = store.OpenShared(ctx, cfg.DataDir) // the server is running
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	org, err := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	plain, hash, err := web.NewSetupToken()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	now := time.Now()
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: "cli", ActorID: "cli", Client: "cli", Action: "setup_link.create", Decision: "allow", Outcome: "ok"}
	err = st.CreateSetupLink(ctx, org.ID, store.NewSetupLink{ID: ids.New(), SecretHash: hash, Person: person, CreatedBy: "cli", Now: now, ExpiresAt: now.Add(setupLinkTTL)}, ev)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, strings.TrimSuffix(cfg.PublicURL.String(), "/")+"/admin/setup/"+plain)
	_, _ = fmt.Fprintln(stderr, "Open this link in your browser within 1 hour. It works once, and it makes the GitHub account you sign in with the owner. Making a new link cancels this one.")
	if missing := webCfg.Missing(cfg.PublicURL); len(missing) > 0 {
		_, _ = fmt.Fprintf(stderr, "Sign-in will not work until these are set and Privo is restarted: %s\n", strings.Join(missing, ", "))
	}
	return 0
}
