package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"privo.club/privo/internal/control"
	"privo.club/privo/internal/store"
	"privo.club/privo/internal/token"
)

// installBootstrap installs the operator's bootstrap token on first boot (F24).
// It never logs the token, only its public id. A malformed token is an error;
// a token that is not needed or was already used is a warning that tells the
// operator to remove the variable.
func installBootstrap(ctx context.Context, st *store.Store, org store.OrgID, plain string, logger *slog.Logger) error {
	res, rec, err := control.InstallBootstrapToken(ctx, st, org, plain, time.Now())
	if err != nil {
		if errors.Is(err, token.ErrMalformed) {
			return errors.New("PRIVO_BOOTSTRAP_TOKEN is not a valid Privo token")
		}
		return fmt.Errorf("installing the bootstrap token: %w", err)
	}
	switch res {
	case store.BootstrapInstalled:
		logger.Info("bootstrap owner token installed from PRIVO_BOOTSTRAP_TOKEN: sign in with it, create your own owner token, revoke this one, then remove the variable",
			"token_id", rec.ID, "expires", rec.ExpiresAt.UTC().Format(time.RFC3339))
	case store.BootstrapConsumed:
		logger.Warn("PRIVO_BOOTSTRAP_TOKEN is set but a bootstrap token was already installed here and will not be installed again: remove the variable")
	case store.BootstrapNotNeeded:
		logger.Warn("PRIVO_BOOTSTRAP_TOKEN is set but this organisation already has control tokens, so it was ignored: remove the variable")
	}
	return nil
}

// tokenGenerate prints a fresh, well-formed token. It reads no configuration
// and opens no database, so the operator can run it anywhere (a laptop, or
// `docker run`) before the first deploy and keep the secret out of every log.
func tokenGenerate(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: privo token generate")
		return 2
	}
	tok, err := token.New()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "privo: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, tok.Plain)
	_, _ = fmt.Fprintf(stderr, "Token id %s. Store the token now; it cannot be shown again.\n"+
		"Set it as PRIVO_BOOTSTRAP_TOKEN (or put it in a file named by PRIVO_BOOTSTRAP_TOKEN_FILE) for the first boot only.\n"+
		"It becomes an owner control token that expires 24 hours after install, and is installed once.\n", tok.ID)
	return 0
}
