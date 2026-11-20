package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// F12: privo audit verify checks the chain with the master key, and finds a
// row changed in the database.
func TestAuditVerifyCommand(t *testing.T) {
	kek, _ := vault.NewKey()
	getenv, dir := adminEnv(t, map[string]string{"PRIVO_MASTER_KEY": base64.StdEncoding.EncodeToString(kek)})
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	org, _ := st.EnsureOrg(ctx, DefaultOrgSlug, "Default")
	for i, id := range []string{"e1", "e2", "e3"} {
		ev := store.AuditEvent{ID: id, CallID: id, Phase: "intent", Time: time.Now().Add(time.Duration(i) * time.Second), Actor: "a", Action: "tools/call", Decision: "allow"}
		if err := st.InsertAudit(ctx, org.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&audit.Sealer{Store: st, Keys: vault.StaticKeyProvider(kek, nil), Org: org.ID}).SealOnce(ctx); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "verify"}, &out, &errb, getenv); code != 0 || !strings.Contains(out.String(), `"ok": true`) || !strings.Contains(out.String(), `"events": 3`) {
		t.Fatalf("verify: %d %s %s", code, out.String(), errb.String())
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "privo.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE audit_events SET decision = 'deny' WHERE id = 'e2'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out.Reset()
	errb.Reset()
	if code := run([]string{"audit", "verify"}, &out, &errb, getenv); code != 1 || !strings.Contains(errb.String(), "was changed") {
		t.Fatalf("after a change: %d %s %s", code, out.String(), errb.String())
	}
	// Another master key trusts none of the blocks.
	other, _ := vault.NewKey()
	getenv2, _ := adminEnv(t, map[string]string{"PRIVO_DATA_DIR": dir, "PRIVO_MASTER_KEY": base64.StdEncoding.EncodeToString(other)})
	if code := run([]string{"audit", "verify"}, &out, &errb, getenv2); code != 1 {
		t.Fatalf("another master key: %d", code)
	}
	if code := run([]string{"audit", "nope"}, &out, &errb, getenv); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}
}
