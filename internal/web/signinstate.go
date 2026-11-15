package web

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// A sign-in in flight lives in the browser, not on the server (ADR 0016
// amendment 1). Starting a sign-in needs no session, so anything it wrote
// could be filled by anyone: the table of states it used to write had a cap,
// and filling the cap locked everyone out of signing in (F05 R-01). Now the
// start writes nothing. What the callback needs travels in a cookie that only
// this Privo can read or make.

// Purposes of a sign-in in flight.
const (
	purposeSignIn = "signin"
	purposeSetup  = "setup"  // claiming a setup link (ADR 0016)
	purposeInvite = "invite" // claiming an invite (ADR 0017)
)

// maxSealedCookie bounds what is decoded from the cookie. A sealed sign-in is
// about 250 characters.
const maxSealedCookie = 1024

// maxFinished bounds the callbacks a process remembers (see finished).
const maxFinished = 8192

// Keys gives the organisation's data key (the keyring, ADR 0003). The website
// seals the sign-in cookie with it, so the cookie also opens after a restart
// and on another replica that shares the database.
type Keys interface {
	Current(ctx context.Context, org store.OrgID) (uint32, []byte, error)
	DEK(ctx context.Context, org store.OrgID, version uint32) ([]byte, error)
}

// signIn is what the callback needs to finish a sign-in that start began.
type signIn struct {
	Purpose string `json:"p"`
	// Link is the id of the setup link or invite being claimed, by purpose.
	Link     string `json:"l,omitempty"`
	Verifier string `json:"v"` // PKCE code verifier
	// ReturnTo is a local path to go back to after signing in; only fixed
	// shapes are accepted, when it is written and again when it is used.
	ReturnTo string `json:"r,omitempty"`
	Expires  int64  `json:"e"` // Unix seconds
}

// signInBinding ties a sealed sign-in to this organisation, to the key
// version and to the state value sent to GitHub. The state is part of the key
// derivation and of the associated data, so a cookie opens only together with
// the state it was made for: the callback of one sign-in cannot be finished
// with the cookie of another, and each cookie has a key of its own.
func signInBinding(org store.OrgID, state string, keyVersion uint32) vault.Binding {
	return vault.Binding{OrgID: string(org), CredentialID: "signin:" + state, Purpose: "web.signin", KeyVersion: keyVersion, RowVersion: 1}
}

// sealSignIn returns the cookie value for a sign-in started with state.
func (s *Server) sealSignIn(ctx context.Context, state string, in signIn) (string, error) {
	version, dek, err := s.o.Keys.Current(ctx, s.o.Org)
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sealed, err := vault.Seal(dek, plain, signInBinding(s.o.Org, state, version))
	if err != nil {
		return "", err
	}
	out := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(sealed)), version)
	return base64.RawURLEncoding.EncodeToString(append(out, sealed...)), nil
}

// openSignIn reads a sign-in cookie for the state a callback brought. It is
// false for anything that is not a cookie this Privo made for exactly that
// state and that has not expired; it never says which.
func (s *Server) openSignIn(ctx context.Context, state, cookie string) (signIn, bool) {
	if !validSecret(state) || cookie == "" || len(cookie) > maxSealedCookie {
		return signIn{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil || len(raw) <= 4 {
		return signIn{}, false
	}
	version := binary.BigEndian.Uint32(raw)
	dek, err := s.o.Keys.DEK(ctx, s.o.Org, version)
	if err != nil {
		// An unknown key version is a cookie Privo did not make.
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("web: key for the sign-in cookie", "err", err)
		}
		return signIn{}, false
	}
	plain, err := vault.Open(dek, raw[4:], signInBinding(s.o.Org, state, version))
	if err != nil {
		return signIn{}, false
	}
	var in signIn
	if err := json.Unmarshal(plain, &in); err != nil {
		return signIn{}, false
	}
	switch in.Purpose {
	case purposeSignIn, purposeSetup, purposeInvite:
	default:
		return signIn{}, false
	}
	if in.Verifier == "" || s.now().Unix() >= in.Expires {
		return signIn{}, false
	}
	return in, true
}

// finished remembers the sign-ins whose callback this process has taken, so
// the same callback is refused a second time while its cookie would still
// open. It is the second line, not the first: an authorization code works
// once at GitHub, the browser's cookie is cleared, and the member, setup link
// or invite is checked again on every use. It is kept in memory on purpose (a
// row would be a write for someone who is not signed in), so it does not
// reach another replica or outlive a restart, and when it is full the oldest
// entry goes.
type finished struct {
	mu    sync.Mutex
	until map[string]time.Time
	order []string // states in the order they were added
}

// first records state and reports whether this is the first time it is seen.
// An entry is kept for ttl, the longest a cookie for it can still open.
func (f *finished) first(state string, now time.Time, ttl time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.until == nil {
		f.until = map[string]time.Time{}
	}
	if until, seen := f.until[state]; seen && now.Before(until) {
		return false
	}
	// Entries are added in time order with one ttl, so the oldest is in front:
	// drop what has expired, and the oldest one when there is no room.
	drop := func() {
		delete(f.until, f.order[0])
		f.order = f.order[1:]
	}
	for len(f.order) > 0 && !now.Before(f.until[f.order[0]]) {
		drop()
	}
	if len(f.order) >= maxFinished {
		drop()
	}
	f.until[state] = now.Add(ttl)
	f.order = append(f.order, state)
	return true
}
