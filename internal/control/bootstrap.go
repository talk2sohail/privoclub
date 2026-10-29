package control

import (
	"context"
	"time"

	"privo.club/privo/internal/ids"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

// Bootstrap token constants (F24). The token exists so a deployment with no
// shell, such as the distroless image on Coolify, can get its first owner
// token from an environment variable. It is a stopgap until the first-boot
// setup link of F05.
const (
	// BootstrapTokenName is the name the installed token carries.
	BootstrapTokenName = "bootstrap"
	// BootstrapTokenTTL is how long the token lasts after install.
	BootstrapTokenTTL = 24 * time.Hour
	// BootstrapActor is the audit actor for the install.
	BootstrapActor = "system"
)

// InstallBootstrapToken installs plain, a token the operator generated with
// `privo token generate`, as the org's first owner control token. Only the hash
// of its secret is stored. It installs at most once per org and only when the
// org has never had a control token; the result says which case applied.
//
// A malformed token returns token.ErrMalformed, and one whose secret is too
// regular to be random (R-10) returns token.ErrWeak; neither repeats the input.
func InstallBootstrapToken(ctx context.Context, s *store.Store, org store.OrgID, plain string, now time.Time) (store.BootstrapResult, store.APIToken, error) {
	id, secret, err := token.Parse(plain)
	if err != nil {
		return 0, store.APIToken{}, token.ErrMalformed
	}
	if token.Weak(secret) {
		return 0, store.APIToken{}, token.ErrWeak
	}
	rec := store.APIToken{OrgID: org, ID: id, Name: BootstrapTokenName, Role: store.RoleOwner, Kind: store.KindControl, Person: BootstrapTokenName,
		SecretHash: token.HashSecret(secret), CreatedAt: now, ExpiresAt: now.Add(BootstrapTokenTTL)}
	ev := store.AuditEvent{ID: ids.New(), CallID: ids.New(), Time: now, Actor: BootstrapActor, Action: "bootstrap.token.install:" + id,
		Decision: "allow", Outcome: "ok"}
	res, err := s.InstallBootstrapToken(ctx, rec, ev)
	return res, rec, err
}
