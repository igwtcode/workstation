package pkgmgr

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/engine"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	defer func() {
		os.Stdout = orig
	}()
	fn()
	os.Stdout = orig
	_ = w.Close()
	return <-done
}

func TestExecBootstrapJSONKeepsHookOutputOffStdout(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	t.Setenv("WS_YES", "1")
	t.Setenv("WS_NON_INTERACTIVE", "0")
	repoDir, homeDir := makeSyntheticRepo(t)
	envOut := filepath.Join(t.TempDir(), "hook-env")
	hookRel := filepath.Join("os", "mac", "json-probe.sh")
	body := "echo hook-stdout-leak\nprintf '%s %s' \"$WS_YES\" \"$WS_NON_INTERACTIVE\" > \"" + envOut + "\"\n"
	if err := os.WriteFile(filepath.Join(repoDir, hookRel), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.JSON = true
	ctx.Allow.Sudo = true
	act := osHookAction(ctx, "mac-workstation", hookRel)
	if !act.NeedsTTY {
		t.Fatal("expected the OS hook action to need a TTY")
	}
	plan := &engine.Plan{Actions: []engine.Action{act}}

	var res *engine.ApplyResult
	var execErr error
	out := captureStdout(t, func() {
		res, execErr = ExecBootstrap(ctx, plan, ExecRunner{})
		if execErr == nil {
			_ = json.NewEncoder(os.Stdout).Encode(res)
		}
	})
	if execErr != nil {
		t.Fatalf("ExecBootstrap: %v", execErr)
	}
	if strings.Contains(out, "hook-stdout-leak") {
		t.Fatalf("hook output leaked to stdout under --json: %q", out)
	}
	var decoded engine.ApplyResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("stdout is not pure JSON under --json: %v: %q", err, out)
	}
	got, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	if string(got) != "0 1" {
		t.Fatalf("hook env WS_YES/WS_NON_INTERACTIVE = %q, want %q", got, "0 1")
	}
}

func TestOSHookEnvOverridesAmbientConsent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("WS_YES", "1")
	t.Setenv("WS_NON_INTERACTIVE", "1")
	repoDir, homeDir := makeSyntheticRepo(t)
	probe := []string{"sh", "-c", `printf '%s %s' "$WS_YES" "$WS_NON_INTERACTIVE"`}

	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	out, _, err := ExecRunner{}.Run(probe, osHookEnv(ctx, nil), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "0 0" {
		t.Fatalf("interactive hook env WS_YES/WS_NON_INTERACTIVE = %q, want %q", out, "0 0")
	}

	ctx.Allow.Yes = true
	ctx.NonInteractive = true
	out, _, err = ExecRunner{}.Run(probe, osHookEnv(ctx, map[string]string{"WS_YES": "0"}), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out != "1 1" {
		t.Fatalf("consented hook env WS_YES/WS_NON_INTERACTIVE = %q, want %q", out, "1 1")
	}
}
