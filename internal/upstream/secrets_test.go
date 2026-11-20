package upstream

import (
	"context"
	"testing"
	"time"

	"privo.club/privo/internal/keyring"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// Only the headers and env the configuration names are sent: a credential
// left over from an earlier configuration goes nowhere (ADR 0029, security
// review of the third slice, M1).
func TestSecretsSendOnlyWhatTheConfigurationNames(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	org, _ := st.EnsureOrg(ctx, "default", "Default")
	kek, _ := vault.NewKey()
	kr := keyring.New(st, vault.StaticKeyProvider(kek, nil))
	if err := kr.EnsureOrgKey(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	version, dek, err := kr.Current(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(id, purpose, value string) store.Credential {
		c := store.Credential{ID: id, ServerID: "srv", Purpose: purpose, KeyVersion: version, RowVersion: 1}
		c.Ciphertext, err = vault.Seal(dek, []byte(value), vault.Binding{OrgID: string(org.ID), CredentialID: id, ServerID: "srv", Purpose: purpose, KeyVersion: version, RowVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	srv := store.Server{OrgID: org.ID, ID: "srv", Alias: "kit", Transport: "http", Enabled: true, CreatedAt: time.Now(),
		Config: store.ServerConfig{URL: "https://kit.example/mcp", HeaderNames: []string{"Authorization"}, EnvNames: nil}}
	if err := st.CreateServer(ctx, srv, []store.Credential{
		seal("c1", "header:Authorization", "Bearer named"),
		seal("c2", "header:X-Old-Key", "for-the-old-host"),
		seal("c3", "env:STRAY", "stray"),
	}); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Store: st, Keys: kr, HTTPClient: loopClient(), RetryBase: time.Hour})
	t.Cleanup(m.Close)
	env, headers, err := m.secrets(ctx, &entry{org: org.ID, srv: srv})
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 || headers["Authorization"] != "Bearer named" || len(env) != 0 {
		t.Fatalf("headers %v, env %v", headers, env)
	}
}
