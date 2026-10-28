package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalChecksDigestAndScrubsEnv(t *testing.T) {
	path, err := filepath.Abs("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := FileDigest(path)
	if err != nil {
		t.Skip("no /bin/sh:", err)
	}
	t.Setenv("PRIVO_SHOULD_NOT_LEAK", "x")

	cmd, err := Local{}.Command(context.Background(), Spec{Path: path, SHA256: digest, Args: []string{"-c", "env"}, Env: map[string]string{"SECRET": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	env := string(out)
	if !strings.Contains(env, "SECRET=s") {
		t.Fatalf("server env missing: %s", env)
	}
	if strings.Contains(env, "PRIVO_SHOULD_NOT_LEAK") {
		t.Fatal("gateway environment leaked to the server")
	}

	for _, bad := range []string{"LD_PRELOAD", "ld_library_path", "PATH", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PRIVO_MASTER_KEY"} {
		if _, err := (Local{}).Command(context.Background(), Spec{Path: path, SHA256: digest, Env: map[string]string{bad: "x"}}); !errors.Is(err, ErrReservedEnv) {
			t.Errorf("%s accepted: %v", bad, err)
		}
	}
	if _, err := (Local{}).Command(context.Background(), Spec{Path: path, SHA256: strings.Repeat("0", 64)}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("wrong digest: %v", err)
	}
	if _, err := (Local{}).Command(context.Background(), Spec{Path: "sh", SHA256: digest}); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := (Local{}).Command(context.Background(), Spec{Path: filepath.Join(t.TempDir(), "missing"), SHA256: digest}); err == nil {
		t.Fatal("missing file accepted")
	}
	_ = os.Getenv
}
