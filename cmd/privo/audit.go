package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"privo.club/privo/internal/audit"
	"privo.club/privo/internal/config"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/vault"
)

// auditCmd runs `privo audit verify`: it checks the org's audit chain (ADR
// 0006, 0028) with the keys derived from the master key, prints the report
// as JSON and exits 1 when it finds a problem. It works while the server runs.
func auditCmd(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) != 1 || args[0] != "verify" {
		_, _ = fmt.Fprintln(stderr, "usage: privo audit verify")
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", err)
		return 2
	}
	keys, err := vault.LoadKeyProvider(getenv)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 2
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		st, err = store.OpenShared(ctx, cfg.DataDir)
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
	trusted, err := audit.KeysFrom(keys, org.ID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 2
	}
	rep, err := audit.Verify(ctx, st, org.ID, trusted)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
	if !rep.OK {
		_, _ = fmt.Fprintf(stderr, "privo audit verify: %s\n", rep.Problem)
		return 1
	}
	return 0
}
