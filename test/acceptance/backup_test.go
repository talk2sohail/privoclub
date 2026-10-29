//go:build acceptance

package acceptance

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// masterKeyFileOf returns the PRIVO_MASTER_KEY_FILE an exec.Cmd would see: the last one wins.
func masterKeyFileOf(env []string) string {
	val := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PRIVO_MASTER_KEY_FILE="); ok {
			val = v
		}
	}
	return val
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// The restore drill (F15): back up a running gateway with the real binary,
// restore the file into a new data directory, start a gateway on it with the
// same master key, and find the server, the policy, the tokens and the audit
// history there. A backup holds no secret in the clear, and restoring over a
// running gateway is refused.
func TestBackupAndRestoreDrill(t *testing.T) {
	const secret = "drill-secret-value-7f3a9c1e"
	g := startGateway(t)
	g.registerEcho("echo", nil, map[string]string{"DRILL_SECRET": secret})
	g.acceptPins("echo")
	member := g.issue("ada")
	policy := idOfPolicy(t, g.api(http.MethodPost, "/api/v1/policies", g.owner,
		map[string]any{"server": "echo", "tool": "echo", "subject": "role:member", "action": "deny"}, http.StatusCreated))
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })
	if got := callEcho(t, s, "before"); got != "before" {
		t.Fatalf("call before the backup: %q", got)
	}
	wantCall(g, "echo", "echo", "tools/call", "allow", "ok")

	// Back up while the gateway runs and serves.
	file := filepath.Join(t.TempDir(), "drill.db")
	if out, err := g.command("backup", "--to", file).CombinedOutput(); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v, %v", info, err)
	}
	if got := callEcho(t, s, "during"); got != "during" {
		t.Fatalf("the gateway stopped serving after a backup: %q", got)
	}
	wantAdmin(g, "backup.create")

	// Nothing secret is in the file: not the credential, not a token, not the key.
	data, _ := os.ReadFile(file)
	keyFile := masterKeyFileOf(g.env)
	keyText, _ := os.ReadFile(keyFile)
	rawKey, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyText)))
	for name, needle := range map[string]string{"upstream credential": secret, "owner token": g.owner, "agent token": g.agent, "member token": member,
		"master key (base64)": strings.TrimSpace(string(keyText)), "master key (raw)": string(rawKey)} {
		if len(needle) == 0 || bytes.Contains(data, []byte(needle)) {
			t.Errorf("the backup contains the %s in the clear (or the check had nothing to look for)", name)
		}
	}

	// A running gateway holds the database: restore refuses and changes nothing.
	out, err := g.command("restore", "--from", file, "--force").CombinedOutput()
	if exitCode(err) != 1 || !strings.Contains(string(out), "running") {
		t.Fatalf("restore over a running gateway: exit %d, output %q, want exit 1 and a message that a gateway is running", exitCode(err), out)
	}
	if got := callEcho(t, s, "still"); got != "still" {
		t.Fatalf("the refused restore disturbed the gateway: %q", got)
	}

	// The drill: a new data directory, the same master key.
	g2 := newGateway(t, "PRIVO_MASTER_KEY_FILE="+keyFile)
	g2.owner, g2.agent = g.owner, g.agent
	if out, err := g2.command("restore", "--from", file).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	g2.start()

	g2.eventually("the restored server is up", 10*time.Second, func() bool { return g2.serverStatus("echo") == "up" })
	s2 := g2.connect(g.agent)
	g2.eventually("echo listed on the restored gateway", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s2), "echo__echo") })
	if got := callEcho(t, s2, "after"); got != "after" {
		t.Fatalf("call on the restored gateway: %q", got)
	}
	listed := g2.api(http.MethodGet, "/api/v1/policies", g2.owner, nil, http.StatusOK)["policies"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["id"] != policy {
		t.Fatalf("restored policies = %v, want the deny rule %s", listed, policy)
	}
	ms := g2.connect(member) // the member's token survived the restore, and so did the rule against members
	g2.eventually("member sees echo", 5*time.Second, func() bool { return slices.Contains(toolNames(t, ms), "echo__echo") })
	if text, isErr := call(t, ms, "echo__echo", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "Denied") {
		t.Fatalf("member call on the restored gateway: %q (error=%v), want a denial", text, isErr)
	}
	wantCall(g2, "echo", "echo", "tools/call", "allow", "ok") // history from before the backup
	wantAdmin(g2, "restore.apply")
	wantAdmin(g2, "policy.create:deny echo echo role:member")
}

// The likeliest operator mistake after a restore: the wrong master key. The
// gateway must say so and stop, not start and fail on every credential later.
func TestRestoreWithTheWrongKeyRefusesToStart(t *testing.T) {
	g := startGateway(t)
	g.registerEcho("echo", nil, map[string]string{"X": "y"})
	g.acceptPins("echo")
	file := filepath.Join(t.TempDir(), "wrongkey.db")
	if out, err := g.command("backup", "--to", file).CombinedOutput(); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	other := newGateway(t) // a different master key
	if out, err := other.command("restore", "--from", file).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	cmd := other.command("serve")
	out, err := runWithin(cmd, 15*time.Second)
	if err == nil {
		t.Fatal("a gateway started on a database its master key cannot open")
	}
	if exitCode(err) != 1 {
		t.Errorf("exit code %d, want 1 (output %q)", exitCode(err), out)
	}
	text := string(out)
	if !strings.Contains(text, "master key") || !strings.Contains(text, "cannot unwrap") {
		t.Errorf("the message does not name the problem: %q", text)
	}
	keyText, _ := os.ReadFile(masterKeyFileOf(other.env))
	if k := strings.TrimSpace(string(keyText)); k == "" || strings.Contains(text, k) {
		t.Errorf("the master key appears in the output (or could not be read)")
	}
}

// runWithin runs cmd and returns its combined output; a process still running
// at the deadline is killed and reported as an error.
func runWithin(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	var buf logBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return []byte(buf.String()), err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		killed := <-done
		return []byte(buf.String()), errors.Join(errors.New("still running at the deadline"), killed)
	}
}

// The built-in schedule (PRIVO_BACKUP_INTERVAL): a running gateway writes
// snapshots into <data>/backups on its own, keeps only the newest few, audits
// each one, and the newest restores into a working gateway.
func TestScheduledBackupsKeepTheNewestAndRestore(t *testing.T) {
	g := startGateway(t, "PRIVO_BACKUP_INTERVAL=1s", "PRIVO_BACKUP_KEEP=2")
	g.registerEcho("echo", nil, map[string]string{"X": "y"})
	g.acceptPins("echo")
	s := g.connect(g.agent)
	g.eventually("echo listed", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s), "echo__echo") })

	countBackups := func() int {
		n := 0
		for _, a := range adminActions(g) {
			if a == "backup.create" {
				n++
			}
		}
		return n
	}
	g.eventually("three scheduled backups", 20*time.Second, func() bool { return countBackups() >= 3 })
	if got := callEcho(t, s, "while backing up"); got != "while backing up" {
		t.Fatalf("the gateway stopped serving while backing up: %q", got)
	}

	// Stop first so the directory stops changing, then inspect it.
	keyFile := masterKeyFileOf(g.env)
	g.stop()
	dir := filepath.Join(g.dataDir, "backups")
	files, err := filepath.Glob(filepath.Join(dir, "privo-*.db"))
	if err != nil || len(files) != 2 {
		t.Fatalf("backups kept = %v (%v), want exactly the newest 2 of at least 3", files, err)
	}
	if stray, _ := filepath.Glob(filepath.Join(dir, "*.partial")); len(stray) != 0 {
		t.Errorf("partial backups left behind: %v", stray)
	}
	slices.Sort(files)
	newest := files[len(files)-1]
	if info, err := os.Stat(newest); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode: %v, %v, want 0600", info, err)
	}

	// The newest snapshot restores into a new data directory and serves.
	g2 := newGateway(t, "PRIVO_MASTER_KEY_FILE="+keyFile)
	g2.owner, g2.agent = g.owner, g.agent
	if out, err := g2.command("restore", "--from", newest).CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	g2.start()
	g2.eventually("the restored server is up", 10*time.Second, func() bool { return g2.serverStatus("echo") == "up" })
	s2 := g2.connect(g2.agent)
	g2.eventually("echo listed on the restored gateway", 5*time.Second, func() bool { return slices.Contains(toolNames(t, s2), "echo__echo") })
	if got := callEcho(t, s2, "restored"); got != "restored" {
		t.Fatalf("call on the restored gateway: %q", got)
	}
}

// With the schedule off (the default) a gateway writes no backups by itself.
func TestNoScheduledBackupsByDefault(t *testing.T) {
	g := startGateway(t)
	time.Sleep(1500 * time.Millisecond)
	if files, _ := filepath.Glob(filepath.Join(g.dataDir, "backups", "*")); len(files) != 0 {
		t.Fatalf("backups written with no schedule configured: %v", files)
	}
	for _, a := range adminActions(g) {
		if a == "backup.create" {
			t.Fatalf("a backup was audited with no schedule configured")
		}
	}
}
