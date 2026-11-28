package pkgmgr

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "lib", "common.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("lib/common.sh not found")
		}
		dir = parent
	}
}

func declinedExitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected the probe to exit non-zero, got err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "probe: declined") {
		t.Fatalf("expected the decline warning, got %q", out)
	}
	return exitErr.ExitCode()
}

func hookEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "WS_YES=") || strings.HasPrefix(kv, "WS_NON_INTERACTIVE=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func TestDeclinedExitHelperSeparatesPromptFromMissingConsent(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	body := "source " + filepath.Join(repoRootForTest(t), "lib", "common.sh") + "\nfalse || declined_exit probe\n"
	if err := os.WriteFile(probe, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	noTTY := exec.Command(bash, probe)
	noTTY.Env = hookEnv()
	if got := declinedExitCode(t, noTTY); got != HookDeclinedExitCode {
		t.Fatalf("decline without a TTY exited %d, want %d", got, HookDeclinedExitCode)
	}

	nonInteractive := exec.Command(bash, probe)
	nonInteractive.Env = hookEnv("WS_NON_INTERACTIVE=1")
	if got := declinedExitCode(t, nonInteractive); got != HookDeclinedExitCode {
		t.Fatalf("decline under WS_NON_INTERACTIVE exited %d, want %d", got, HookDeclinedExitCode)
	}

	scriptBin, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script not available to allocate a TTY")
	}
	var withTTY *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		withTTY = exec.Command(scriptBin, "-q", "/dev/null", bash, probe)
	case "linux":
		withTTY = exec.Command(scriptBin, "-qec", bash+" "+probe, "/dev/null")
	default:
		t.Skipf("no TTY harness for %s", runtime.GOOS)
	}
	withTTY.Env = hookEnv()
	if got := declinedExitCode(t, withTTY); got != HookUserDeclinedExitCode {
		t.Fatalf("decline at a TTY prompt exited %d, want %d", got, HookUserDeclinedExitCode)
	}

	withTTYNonInteractive := exec.Command(withTTY.Args[0], withTTY.Args[1:]...)
	withTTYNonInteractive.Env = hookEnv("WS_NON_INTERACTIVE=1")
	if got := declinedExitCode(t, withTTYNonInteractive); got != HookDeclinedExitCode {
		t.Fatalf("decline under WS_NON_INTERACTIVE at a TTY exited %d, want %d", got, HookDeclinedExitCode)
	}
}

func TestConfirmDeclinesWithoutTTYThroughDeclinedExit(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	probe := filepath.Join(t.TempDir(), "probe.sh")
	body := "source " + filepath.Join(repoRootForTest(t), "lib", "common.sh") + "\nif ! confirm \"probe: continue?\"; then\n  declined_exit probe\nfi\necho accepted\n"
	if err := os.WriteFile(probe, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, extra := range [][]string{nil, {"WS_YES=0"}, {"WS_YES=0", "WS_NON_INTERACTIVE=0"}, {"WS_NON_INTERACTIVE=1"}} {
		cmd := exec.Command(bash, probe)
		cmd.Env = hookEnv(extra...)
		if got := declinedExitCode(t, cmd); got != HookDeclinedExitCode {
			t.Fatalf("confirm without a TTY (env %v) exited %d, want %d", extra, got, HookDeclinedExitCode)
		}
	}

	yes := exec.Command(bash, probe)
	yes.Env = hookEnv("WS_YES=1")
	out, err := yes.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "accepted") {
		t.Fatalf("confirm under WS_YES=1 should accept, got err=%v out=%q", err, out)
	}
}
