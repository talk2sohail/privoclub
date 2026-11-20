package audit

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// ADR 0006, 0025: sealing, signing and verification.

type chainEnv struct {
	st    *store.Store
	db    *sql.DB // a second connection, to change rows behind the store's back
	org   store.OrgID
	keys  vault.KeyProvider
	seal  *Sealer
	clock time.Time
}

func newChainEnv(t *testing.T) *chainEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, "default", "Default")
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "privo.db")+"?"+q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	kek, _ := vault.NewKey()
	e := &chainEnv{st: st, db: db, org: org.ID, keys: vault.StaticKeyProvider(kek, nil), clock: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	e.seal = &Sealer{Store: st, Keys: e.keys, Org: org.ID, BatchSize: 3, Now: func() time.Time { return e.clock }}
	return e
}

var evSeq int

func (e *chainEnv) add(t *testing.T, n int, payload []byte) {
	t.Helper()
	for i := 0; i < n; i++ {
		evSeq++
		e.clock = e.clock.Add(time.Second)
		ev := store.AuditEvent{ID: fmt.Sprintf("ev%04d", evSeq), CallID: fmt.Sprintf("c%04d", evSeq), Phase: "intent", Time: e.clock, Actor: "ada-laptop",
			ActorID: "tok-ada", Server: "github", Item: "merge", Action: "tools/call", Decision: "allow", Payload: payload}
		if err := e.st.InsertAudit(context.Background(), e.org, ev); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *chainEnv) verify(t *testing.T) Report {
	t.Helper()
	trusted, err := KeysFrom(e.keys, e.org)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Verify(context.Background(), e.st, e.org, trusted)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSigningKeyIsDerived(t *testing.T) {
	k1, _ := vault.NewKey()
	k2, _ := vault.NewKey()
	a1, id1, _ := SigningKey(k1, "org-a")
	a1again, id1again, _ := SigningKey(k1, "org-a")
	b1, idb, _ := SigningKey(k1, "org-b")
	a2, id2, _ := SigningKey(k2, "org-a")
	if !a1.Equal(a1again) || id1 != id1again || len(id1) != 16 {
		t.Fatal("the same master key and org give another key")
	}
	if a1.Equal(b1) || id1 == idb || a1.Equal(a2) || id1 == id2 {
		t.Fatal("orgs or master keys share a signing key")
	}
	if _, _, err := SigningKey(nil, "org-a"); err == nil {
		t.Fatal("a key from nothing")
	}
	trusted, _ := KeysFrom(vault.StaticKeyProvider(k2, k1), "org-a")
	if len(trusted) != 2 || trusted[id1] == nil || trusted[id2] == nil {
		t.Fatalf("trusted during a rotation: %v", trusted)
	}
}

func TestSealAndVerify(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	if r := e.verify(t); !r.OK || r.Blocks != 0 {
		t.Fatalf("empty chain: %+v", r)
	}
	e.add(t, 7, []byte("sealed-args"))
	if r := e.verify(t); !r.OK || r.Unsealed != 7 {
		t.Fatalf("before sealing: %+v", r)
	}
	n, err := e.seal.SealOnce(ctx)
	if err != nil || n != 7 {
		t.Fatalf("SealOnce = %d, %v", n, err)
	}
	if n, _ := e.seal.SealOnce(ctx); n != 0 {
		t.Fatal("sealed twice")
	}
	r := e.verify(t)
	if !r.OK || r.Blocks != 3 || r.Events != 7 || r.Unsealed != 0 {
		t.Fatalf("after sealing: %+v", r)
	}
	// Later events continue the chain.
	e.add(t, 2, nil)
	if _, err := e.seal.SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.verify(t); !r.OK || r.Blocks != 4 || r.Events != 9 {
		t.Fatalf("continued: %+v", r)
	}
}

// Every way of rewriting history that the chain covers is found.
func TestVerifyFindsTampering(t *testing.T) {
	cases := map[string]struct {
		sql  string
		want string
	}{
		"a changed field":     {`UPDATE audit_events SET decision = 'deny' WHERE id = 'ev%s'`, "was changed"},
		"a changed actor":     {`UPDATE audit_events SET actor = 'someone' WHERE id = 'ev%s'`, "was changed"},
		"a deleted event":     {`DELETE FROM audit_events WHERE id = 'ev%s'`, "missing"},
		"changed arguments":   {`UPDATE audit_events SET payload = x'00' WHERE id = 'ev%s'`, "arguments were changed"},
		"a changed digest":    {`UPDATE audit_events SET payload_sha256 = 'ab', payload = NULL WHERE id = 'ev%s'`, "was changed"},
		"a reordered event":   {`UPDATE audit_events SET chain_seq = chain_seq + 100 WHERE id = 'ev%s'`, "missing"},
		"a deleted block":     {`DELETE FROM audit_blocks WHERE seq = 1 AND '%s' <> ''`, "missing"},
		"a forged signature":  {`UPDATE audit_blocks SET signature = 'AAAA' WHERE seq = 2 AND '%s' <> ''`, "bad signature"},
		"an untrusted key":    {`UPDATE audit_blocks SET key_id = 'feedfacefeedface' WHERE seq = 2 AND '%s' <> ''`, "not trusted"},
		"a broken link":       {`UPDATE audit_blocks SET prev_hash = 'ab' WHERE seq = 2 AND '%s' <> ''`, "does not follow"},
		"a block hash edited": {`UPDATE audit_blocks SET hash = 'ab' || substr(hash, 3) WHERE seq = 1 AND '%s' <> ''`, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newChainEnv(t)
			e.add(t, 6, []byte("args"))
			if _, err := e.seal.SealOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			first := fmt.Sprintf("%04d", evSeq-4) // an event in the middle
			if _, err := e.db.Exec(fmt.Sprintf(c.sql, first)); err != nil {
				t.Fatal(err)
			}
			r := e.verify(t)
			if r.OK || r.Problem == "" || !strings.Contains(r.Problem, c.want) {
				t.Fatalf("not found: %+v", r)
			}
		})
	}
}

// A block signed by a key the verifier does not trust fails, even when the
// forger re-signs it consistently with their own key.
func TestVerifyRefusesAnotherKey(t *testing.T) {
	e := newChainEnv(t)
	e.add(t, 3, nil)
	other, _ := vault.NewKey()
	forger := &Sealer{Store: e.st, Keys: vault.StaticKeyProvider(other, nil), Org: e.org, BatchSize: 3}
	if _, err := forger.SealOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := e.verify(t); r.OK || !strings.Contains(r.Problem, "not trusted") {
		t.Fatalf("a foreign key: %+v", r)
	}
}

func TestVerifyAfterRetentionAndErasure(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 6, []byte("old-args")) // two blocks of 3
	if _, err := e.seal.SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cutoff := e.clock.Add(time.Minute)
	e.clock = e.clock.Add(2 * time.Minute)
	e.add(t, 4, []byte("new-args"))
	if _, err := e.seal.SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	e.add(t, 2, nil) // unsealed: never pruned
	n, err := e.st.PruneAudit(ctx, e.org, cutoff)
	if err != nil || n != 6 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	r := e.verify(t)
	if !r.OK || r.Pruned != 2 || r.Events != 4 || r.Unsealed != 2 {
		t.Fatalf("after retention: %+v", r)
	}
	// Erasing the arguments keeps the chain valid, sealed or not.
	ev := store.AuditEvent{ID: "erase", CallID: "erase", Time: e.clock, Actor: "boss", ActorID: "user:boss", Action: "audit.erase", Decision: "allow", Outcome: "ok"}
	if n, err := e.st.EraseAuditPayloads(ctx, e.org, []string{"tok-ada"}, ev); err != nil || n != 4 {
		t.Fatalf("erased %d, %v", n, err)
	}
	if _, err := e.seal.SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.verify(t); !r.OK || r.Unsealed != 0 {
		t.Fatalf("after erasure: %+v", r)
	}
	var left int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE length(payload) > 0`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d payloads left", left)
	}
	// Retention reaching into kept events is caught: delete one more.
	if _, err := e.db.Exec(`DELETE FROM audit_events WHERE chain_seq = (SELECT MIN(chain_seq) FROM audit_events WHERE chain_seq IS NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if r := e.verify(t); r.OK {
		t.Fatalf("a deleted kept event was not found: %+v", r)
	}
}
