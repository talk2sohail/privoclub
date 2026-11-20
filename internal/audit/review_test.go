package audit

import (
	"context"
	"strings"
	"testing"
	"time"

	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// The security review of F12 (2026-11-21): someone who can write to the
// database, but has no master key, must not be able to change history while
// verification still says intact.

func (e *chainEnv) sealAll(t *testing.T) {
	t.Helper()
	if _, err := e.seal.SealOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e *chainEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *chainEnv) mustFail(t *testing.T, what, want string) {
	t.Helper()
	r := e.verify(t)
	if r.OK {
		t.Fatalf("%s: verification says intact: %+v", what, r)
	}
	if !strings.Contains(r.Problem, want) {
		t.Fatalf("%s: problem %q, want it to mention %q", what, r.Problem, want)
	}
}

// Medium 1: deleting the oldest events is not retention unless a signed
// prune record says so, also with retention off.
func TestDeletingOldEventsWithoutASignedPruneIsCaught(t *testing.T) {
	e := newChainEnv(t)
	e.add(t, 6, nil) // two blocks of 3
	e.sealAll(t)
	e.exec(t, `DELETE FROM audit_events WHERE chain_seq <= 3`)
	e.mustFail(t, "the first block's events deleted", "block 1")

	e.exec(t, `DELETE FROM audit_events`)
	e.mustFail(t, "every event deleted", "block 1")
}

// Retention signs what it deletes; verification accepts exactly that.
func TestSignedPruneVerifies(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 6, nil)
	e.sealAll(t)
	cutoff := e.clock.Add(time.Minute)
	e.clock = e.clock.Add(2 * time.Minute)
	e.add(t, 3, nil)
	e.sealAll(t)
	n, err := e.seal.pruneBefore(ctx, cutoff)
	if err != nil || n != 6 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if r := e.verify(t); !r.OK || r.Pruned != 2 || r.Events != 3 {
		t.Fatalf("after a signed prune: %+v", r)
	}
	// Running it again deletes nothing and records nothing.
	if n, err := e.seal.pruneBefore(ctx, cutoff); err != nil || n != 0 {
		t.Fatalf("pruned again %d, %v", n, err)
	}
	ps, _ := e.st.AuditPrunes(ctx, e.org)
	if len(ps) != 1 || ps[0].ThroughSeq != 6 {
		t.Fatalf("prune records: %+v", ps)
	}
	// Moving the signed point forward by hand is caught.
	e.exec(t, `UPDATE audit_prunes SET through_seq = 9`)
	e.mustFail(t, "a prune record moved forward", "prune")
}

// A prune record signed with a key nobody trusts allows nothing.
func TestForgedPruneRecordIsCaught(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 6, nil)
	e.sealAll(t)
	other, _ := vault.NewKey()
	forger := &Sealer{Store: e.st, Keys: vault.StaticKeyProvider(other, nil), Org: e.org, Now: func() time.Time { return e.clock.Add(time.Hour) }}
	if _, err := forger.pruneBefore(ctx, e.clock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e.mustFail(t, "a prune signed by another key", "prune")
}

// Medium 2: an event that claims a place past the last block, or before the
// first, is never covered by a block; verification says so.
func TestEventOutsideTheBlocksIsCaught(t *testing.T) {
	for _, seq := range []int{0, -1, 9} {
		e := newChainEnv(t)
		e.add(t, 3, nil)
		e.sealAll(t)
		e.exec(t, `INSERT INTO audit_events (org_id, id, call_id, phase, ts, actor, actor_id, server, item, action, decision, chain_seq)
			VALUES (?, 'forged', 'cforged', 'intent', ?, 'mallory', 'tok-m', 'github', 'merge', 'tools/call', 'allow', ?)`,
			e.org, e.clock.Format(time.RFC3339Nano), seq)
		e.mustFail(t, "an event placed outside the blocks", "outside")
	}
}

// Medium 3: an event inserted now but dated before the last block was
// sealed is caught once sealed; one within the grace for batching is not.
func TestBackdatedEventIsCaught(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 3, nil)
	e.sealAll(t)
	sealed := e.clock
	e.clock = e.clock.Add(time.Hour)
	ok := store.AuditEvent{ID: "late-ok", CallID: "c-late-ok", Phase: "intent", Time: sealed.Add(-time.Minute), Actor: "ada", ActorID: "tok-ada", Action: "tools/call", Decision: "allow"}
	if err := e.st.InsertAudit(ctx, e.org, ok); err != nil {
		t.Fatal(err)
	}
	e.sealAll(t)
	if r := e.verify(t); !r.OK {
		t.Fatalf("an event within the grace: %+v", r)
	}
	back := store.AuditEvent{ID: "backdated", CallID: "c-back", Phase: "intent", Time: sealed.AddDate(0, -3, 0), Actor: "ada", ActorID: "tok-ada", Action: "tools/call", Decision: "allow"}
	if err := e.st.InsertAudit(ctx, e.org, back); err != nil {
		t.Fatal(err)
	}
	e.sealAll(t)
	e.mustFail(t, "an event dated months before the block before it", "dated before")
}

// A block's seal time is signed: moving it back, to make room for a
// backdated event, breaks the block.
func TestSealTimeIsSigned(t *testing.T) {
	e := newChainEnv(t)
	e.add(t, 3, nil)
	e.sealAll(t)
	e.exec(t, `UPDATE audit_blocks SET sealed_at = ? WHERE seq = 1`, e.clock.AddDate(-1, 0, 0).Format(time.RFC3339Nano))
	e.mustFail(t, "a block's seal time changed", "block 1")
}

// Note: erasing an event not sealed yet keeps the digest of what was there.
func TestErasedUnsealedPayloadKeepsItsDigest(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 1, []byte(`{"secret":1}`))
	ev := store.AuditEvent{ID: "erase", CallID: "erase", Time: e.clock, Actor: "boss", ActorID: "user:boss", Action: "audit.erase:ada", Decision: "allow", Outcome: "ok"}
	if n, err := e.st.EraseAuditPayloads(ctx, e.org, store.Erasure{ActorIDs: []string{"tok-ada"}}, ev); err != nil || n != 1 {
		t.Fatalf("erased %d, %v", n, err)
	}
	e.sealAll(t)
	var digest string
	if err := e.db.QueryRow(`SELECT payload_sha256 FROM audit_events WHERE actor_id = 'tok-ada'`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != store.PayloadDigest([]byte(`{"secret":1}`)) {
		t.Fatalf("sealed digest %q is not the digest of what was erased", digest)
	}
	if r := e.verify(t); !r.OK {
		t.Fatalf("after erasing an unsealed event: %+v", r)
	}
}

// Note: blocks signed before a master key rotation keep verifying once the
// old key is gone, because the new key endorsed the old one during the
// rotation. An endorsement by a key nobody trusts counts for nothing.
func TestOldBlocksVerifyAfterKeyRotation(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 3, nil)
	e.sealAll(t)
	oldKey := e.keys.Current()
	newKey, _ := vault.NewKey()

	// Rotation: the new key is current, the old one previous.
	e.keys = vault.StaticKeyProvider(newKey, oldKey)
	e.seal.Keys = e.keys
	if err := e.seal.Endorse(ctx); err != nil {
		t.Fatal(err)
	}
	e.add(t, 3, nil)
	e.sealAll(t)

	// After the rotation only the new key is left.
	e.keys = vault.StaticKeyProvider(newKey, nil)
	if r := e.verify(t); !r.OK || r.Blocks != 2 {
		t.Fatalf("after the old key is gone: %+v", r)
	}

	// Without the endorsement the old block is signed by an unknown key.
	e.exec(t, `DELETE FROM audit_keys`)
	e.mustFail(t, "no endorsement", "not trusted")

	// An endorsement signed by a key nobody trusts is ignored.
	stranger, _ := vault.NewKey()
	rogue := &Sealer{Store: e.st, Keys: vault.StaticKeyProvider(stranger, oldKey), Org: e.org, Now: e.seal.Now}
	if err := rogue.Endorse(ctx); err != nil {
		t.Fatal(err)
	}
	e.mustFail(t, "an endorsement by a stranger", "not trusted")
}

// Note: retention keeps at least 30 days, whatever the setting says.
func TestRetentionKeepsAtLeastThirtyDays(t *testing.T) {
	ctx := context.Background()
	e := newChainEnv(t)
	e.add(t, 3, nil)
	e.sealAll(t)
	if err := e.st.SetSetting(ctx, e.org, SettingRetentionDays, "1"); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.AddDate(0, 0, 10)
	if n, err := e.seal.Prune(ctx); err != nil || n != 0 {
		t.Fatalf("10 days later, retention of 1 day deleted %d (%v); want nothing before 30 days", n, err)
	}
	e.clock = e.clock.AddDate(0, 0, 21)
	if n, err := e.seal.Prune(ctx); err != nil || n != 3 {
		t.Fatalf("31 days later: deleted %d, %v", n, err)
	}
	if r := e.verify(t); !r.OK || r.Pruned != 1 {
		t.Fatalf("after retention: %+v", r)
	}
}
