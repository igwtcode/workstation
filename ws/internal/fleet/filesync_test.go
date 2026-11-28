package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func TestResolveSyncPathsAndSensitivity(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "personal", "code", "workstation")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := &app.Context{
		HomeDir:  home,
		RepoRoot: repo,
		Host:     app.HostInfo{OS: "darwin", Arch: "arm64"},
	}

	t.Setenv("WS_OP_FILES", ".aws/config,.aws/credentials")
	t.Setenv("WS_SYNC_PATHS", ".zshrc")

	roots, warns, err := ResolveSyncPaths(ctx, true, true, nil)
	if err != nil {
		t.Fatalf("ResolveSyncPaths: %v (warns=%v)", err, warns)
	}
	got := strings.Join(roots, "|")
	for _, want := range []string{
		".ssh",
		".aws/config",
		".aws/credentials",
		"personal/code/workstation/mise.local.toml",
		"personal/code/workstation/.mise/tasks/local",
		".zshrc",
		".ws/backups/ws-backup-latest.enc",
		"~envfiles",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected root %q in %v", want, roots)
		}
	}

	t.Setenv("WS_SYNC_PATHS", "=.ssh/config,.aws/config")
	overrideRoots, _, err := ResolveSyncPaths(ctx, false, false, nil)
	if err != nil {
		t.Fatalf("ResolveSyncPaths(=override): %v", err)
	}
	if len(overrideRoots) != 2 || overrideRoots[0] != ".ssh/config" || overrideRoots[1] != ".aws/config" {
		t.Fatalf("expected exact '=' override roots [.ssh/config .aws/config], got %v", overrideRoots)
	}

	for _, bad := range []string{"../etc/passwd", "/etc/passwd", ".ws/ws.db", ".ws/db.key", "~"} {
		if _, _, err := ResolveSyncPaths(ctx, false, false, []string{bad}); err == nil {
			t.Fatalf("expected ResolveSyncPaths to reject %q", bad)
		}
	}

	sensitive := []string{
		".ssh/id_ed25519",
		".ssh/id_rsa",
		".ssh/custom.key",
		".aws/credentials",
		"work/code/app/.env",
		"work/code/app/.env.local",
		"personal/code/workstation/mise.local.toml",
		".ws/backups/ws-backup-latest.enc",
		".ws/db.key",
		"creds/service.pem",
	}
	for _, p := range sensitive {
		if !IsSensitiveSyncPath(p) {
			t.Fatalf("expected IsSensitiveSyncPath(%q) = true", p)
		}
	}

	safeConfigs := []string{
		".ssh/config",
		".ssh/known_hosts",
		".ssh/id_ed25519.pub",
		".aws/config",
		"work/code/app/.env.example",
		".zshrc",
	}
	for _, p := range safeConfigs {
		if IsSensitiveSyncPath(p) {
			t.Fatalf("expected IsSensitiveSyncPath(%q) = false", p)
		}
	}
}

func TestDiffPreviewAndPushPullWithBackups(t *testing.T) {
	home := t.TempDir()
	ctx := &app.Context{
		HomeDir: home,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}

	writeLocal := func(rel, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}

	shaHex := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}

	writeLocal(".ssh/config", "Host peer\n  User alice\n  Port 2222\n", 0o644)
	writeLocal(".ssh/id_ed25519", "SAME-PRIVATE-KEY\n", 0o600)
	writeLocal(".aws/config", "[default]\nregion = eu-central-1\n", 0o644)
	writeLocal("work/code/app/.env", "API_KEY=old\n", 0o600)

	t.Setenv("WS_OP_FILES", ".aws/config")
	t.Setenv("WS_SYNC_PATHS", "")

	now := time.Unix(1760000100, 0)
	_ = os.Chtimes(filepath.Join(home, ".ssh", "config"), now, now)
	_ = os.Chtimes(filepath.Join(home, "work", "code", "app", ".env"), now.Add(-120*time.Second), now.Add(-120*time.Second))

	remoteSSHConfig := "Host peer\n  User alice\n"
	remoteEnv := "API_KEY=newer-from-remote\n"
	remoteKeyNew := "-----BEGIN OPENSSH PRIVATE KEY-----\nNEW\n"

	var sshCalls int
	var pushedBody string
	var pushedCmd string

	runner := funcExecRunner(func(stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
		sshCalls++
		if name != "ssh" || len(args) == 0 {
			return nil, nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		hasBatch := false
		hasAcceptNew := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-o" && args[i+1] == "BatchMode=yes" {
				hasBatch = true
			}
			if args[i] == "-o" && args[i+1] == "StrictHostKeyChecking=accept-new" {
				hasAcceptNew = true
			}
		}
		if !hasBatch || !hasAcceptNew {
			return nil, nil, fmt.Errorf("expected SSH args to include BatchMode=yes and StrictHostKeyChecking=accept-new, got %v", args)
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "hash_file()"):
			lines := []string{
				fmt.Sprintf("F\t.ssh/config\t%s\t%d\t%d\t644", shaHex(remoteSSHConfig), len(remoteSSHConfig), now.Add(-60*time.Second).Unix()),
				fmt.Sprintf("F\t.ssh/id_ed25519\t%s\t%d\t%d\t600", shaHex("SAME-PRIVATE-KEY\n"), len("SAME-PRIVATE-KEY\n"), now.Unix()),
				fmt.Sprintf("F\t.ssh/id_rsa\t%s\t%d\t%d\t600", shaHex(remoteKeyNew), len(remoteKeyNew), now.Unix()),
				fmt.Sprintf("F\twork/code/app/.env\t%s\t%d\t%d\t600", shaHex(remoteEnv), len(remoteEnv), now.Unix()),
			}
			return []byte(strings.Join(lines, "\n") + "\n"), nil, nil

		case strings.Contains(script, "head -c"):
			return []byte(remoteSSHConfig), nil, nil

		case strings.Contains(script, `cat > "$tmp"`):
			pushedCmd = script
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, nil, err
			}
			pushedBody = string(b)
			return []byte("OK\t~/.ssh/config.pre-ws-sync.20261126-120000\n"), nil, nil

		case strings.Contains(script, `cat -- "$p"`):
			return []byte(remoteEnv), nil, nil

		default:
			return nil, nil, fmt.Errorf("unexpected ssh script: %s", script)
		}
	})

	rep, err := DiffNodeFiles(ctx, "linux-box", "linux-box.tail.ts.net", FileSyncDiffOptions{
		IncludeEnv: true,
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("DiffNodeFiles: %v", err)
	}
	byPath := map[string]FileDiffEntry{}
	for _, e := range rep.Entries {
		byPath[e.RelPath] = e
	}
	if byPath[".ssh/config"].Status != "local-newer" {
		t.Fatalf("expected .ssh/config local-newer, got %+v", byPath[".ssh/config"])
	}
	if byPath[".ssh/id_ed25519"].Status != "same" || !byPath[".ssh/id_ed25519"].IsSecret {
		t.Fatalf("expected .ssh/id_ed25519 same & secret, got %+v", byPath[".ssh/id_ed25519"])
	}
	if byPath[".aws/config"].Status != "local-only" {
		t.Fatalf("expected .aws/config local-only, got %+v", byPath[".aws/config"])
	}
	if byPath[".ssh/id_rsa"].Status != "remote-only" || !byPath[".ssh/id_rsa"].IsSecret {
		t.Fatalf("expected .ssh/id_rsa remote-only & secret, got %+v", byPath[".ssh/id_rsa"])
	}
	if byPath["work/code/app/.env"].Status != "remote-newer" || !byPath["work/code/app/.env"].IsSecret {
		t.Fatalf("expected work/code/app/.env remote-newer & secret, got %+v", byPath["work/code/app/.env"])
	}

	callsBefore := sshCalls
	secPrev, err := PreviewFileTextDiff(ctx, "linux-box.tail.ts.net", ".ssh/id_ed25519", runner)
	if err != nil || !strings.HasPrefix(secPrev, "Redacted:") || sshCalls != callsBefore {
		t.Fatalf("expected secret preview redacted with zero SSH calls, got callsDelta=%d err=%v prev=%q", sshCalls-callsBefore, err, secPrev)
	}
	envPrev, err := PreviewFileTextDiff(ctx, "linux-box.tail.ts.net", "work/code/app/.env", runner)
	if err != nil || !strings.HasPrefix(envPrev, "Redacted:") || sshCalls != callsBefore {
		t.Fatalf("expected .env preview redacted with zero SSH calls, got callsDelta=%d err=%v prev=%q", sshCalls-callsBefore, err, envPrev)
	}

	cfgPrev, err := PreviewFileTextDiff(ctx, "linux-box.tail.ts.net", ".ssh/config", runner)
	if err != nil || !strings.Contains(cfgPrev, "-  Port 2222") || !strings.Contains(cfgPrev, "--- local/.ssh/config") {
		t.Fatalf("expected unified diff for .ssh/config, got err=%v\n%s", err, cfgPrev)
	}

	fixedNow := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	pushRes, err := SyncNodeFiles(ctx, "linux-box", "linux-box.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{".ssh/config"},
		Runner:    runner,
		Now:       fixedNow,
	})
	if err != nil || pushRes.Transferred != 1 || len(pushRes.BackupPaths) != 1 {
		t.Fatalf("SyncNodeFiles(push): err=%v res=%+v", err, pushRes)
	}
	if !strings.Contains(pushedBody, "Port 2222") || !strings.Contains(pushedCmd, ".pre-ws-sync.20261126-120000") {
		t.Fatalf("expected pushed bytes and backup command, got body=%q cmd=%s", pushedBody, pushedCmd)
	}

	pullRes, err := SyncNodeFiles(ctx, "linux-box", "linux-box.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{"work/code/app/.env"},
		Runner:    runner,
		Now:       fixedNow,
	})
	if err != nil || pullRes.Transferred != 1 || len(pullRes.BackupPaths) != 1 {
		t.Fatalf("SyncNodeFiles(pull): err=%v res=%+v", err, pullRes)
	}
	gotEnv, err := os.ReadFile(filepath.Join(home, "work", "code", "app", ".env"))
	if err != nil || string(gotEnv) != remoteEnv {
		t.Fatalf("expected pulled .env=%q, got %q err=%v", remoteEnv, string(gotEnv), err)
	}
	envStat, err := os.Stat(filepath.Join(home, "work", "code", "app", ".env"))
	if err != nil || envStat.Mode().Perm() != 0o600 {
		t.Fatalf("expected pulled .env mode 0600, got %v err=%v", envStat.Mode().Perm(), err)
	}
	bkPath := filepath.Join(home, "work", "code", "app", ".env.pre-ws-sync.20261126-120000")
	bkBytes, err := os.ReadFile(bkPath)
	if err != nil || string(bkBytes) != "API_KEY=old\n" {
		t.Fatalf("expected pre-sync backup %q to hold previous content, got %q err=%v", bkPath, string(bkBytes), err)
	}
	if bkStat, err := os.Stat(bkPath); err != nil || bkStat.Mode().Perm() != 0o600 {
		t.Fatalf("expected backup mode 0600, got %v err=%v", bkStat.Mode().Perm(), err)
	}

	hostKeyErr := formatFleetSSHError("probe remote files", "linux-box.tail.ts.net", errors.New("exit status 255"), []byte("REMOTE HOST IDENTIFICATION HAS CHANGED\nHost key verification failed."))
	if !strings.Contains(hostKeyErr.Error(), "ssh-keygen -R linux-box.tail.ts.net") {
		t.Fatalf("expected ssh-keygen -R guidance on host key error, got %v", hostKeyErr)
	}
}

func TestRemoteProbeScriptLocalExecutionMatchesIdenticalFiles(t *testing.T) {
	home := t.TempDir()
	ctx := &app.Context{
		HomeDir: home,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}

	writeLocal := func(rel, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeLocal(".ssh/config", "Host demo\n  User alice\n", 0o644)
	writeLocal(".ssh/id_ed25519", "PRIVATE-KEY-MATERIAL\n", 0o600)
	writeLocal(".aws/credentials", "[default]\naws_access_key_id=test\n", 0o600)

	t.Setenv("WS_OP_FILES", ".aws/credentials")
	t.Setenv("WS_SYNC_PATHS", "")

	runner := funcExecRunner(func(_ io.Reader, name string, args ...string) ([]byte, []byte, error) {
		if name != "ssh" || len(args) == 0 {
			return nil, nil, fmt.Errorf("unexpected command %s %v", name, args)
		}
		script := args[len(args)-1]
		cmd := exec.Command("/bin/sh", "-c", script)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.Bytes(), stderr.Bytes(), err
	})

	report, err := DiffNodeFiles(ctx, "linux-box", "linux-box.tail.ts.net", FileSyncDiffOptions{Runner: runner})
	if err != nil {
		t.Fatalf("DiffNodeFiles: %v", err)
	}
	if report.SameCount != 3 || report.DiffCount != 0 || report.LocalOnlyCount != 0 || report.RemoteOnlyCount != 0 {
		t.Fatalf("expected all 3 identical files to report SameCount=3 with 0 diffs/missing, got same=%d diff=%d local=%d remote=%d entries=%+v",
			report.SameCount, report.DiffCount, report.LocalOnlyCount, report.RemoteOnlyCount, report.Entries)
	}
}

func installFakeSSHOnPATH(t *testing.T, scriptBody string) {
	t.Helper()
	binDir := t.TempDir()
	sshPath := filepath.Join(binDir, "ssh")
	if err := os.WriteFile(sshPath, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRealOSExecRunnerSeparatesSSHStderrFromPulledContentAndPreview(t *testing.T) {
	localHome := t.TempDir()
	remoteHome := t.TempDir()

	writeFile := func(root, rel string, data []byte, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, mode); err != nil {
			t.Fatal(err)
		}
	}

	remoteKey := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nEXACT-KEY-BYTES\x00\x01\n-----END OPENSSH PRIVATE KEY-----\n")
	sharedCfg := []byte("Host peer\n  User dev\n")
	writeFile(remoteHome, ".ssh/id_ed25519", remoteKey, 0o600)
	writeFile(remoteHome, ".ssh/config", sharedCfg, 0o644)
	writeFile(localHome, ".ssh/config", sharedCfg, 0o644)

	t.Setenv("WS_TEST_REMOTE_HOME", remoteHome)
	installFakeSSHOnPATH(t, `#!/bin/sh
printf "Warning: Permanently added 'peer.tail.ts.net' (ED25519) to the list of known hosts.\n" >&2
printf "shell init stderr noise\n" >&2
for arg in "$@"; do
  last="$arg"
done
HOME="$WS_TEST_REMOTE_HOME" /bin/sh -c "$last"
`)

	ctx := &app.Context{
		HomeDir: localHome,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}

	pullRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{".ssh/id_ed25519"},
		Now:       time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC),
	})
	if err != nil || pullRes.Transferred != 1 {
		t.Fatalf("SyncNodeFiles(pull) with real osExecRunner: err=%v res=%+v", err, pullRes)
	}
	gotKey, err := os.ReadFile(filepath.Join(localHome, ".ssh", "id_ed25519"))
	if err != nil {
		t.Fatalf("read pulled key: %v", err)
	}
	if !bytes.Equal(gotKey, remoteKey) {
		t.Fatalf("pulled file corrupted by SSH stderr: got %q want %q", gotKey, remoteKey)
	}

	preview, err := PreviewFileTextDiff(ctx, "peer.tail.ts.net", ".ssh/config", nil)
	if err != nil {
		t.Fatalf("PreviewFileTextDiff: %v", err)
	}
	if !strings.Contains(preview, "byte-identical") || strings.Contains(preview, "Permanently added") {
		t.Fatalf("expected clean byte-identical preview without stderr warning, got %q", preview)
	}

	installFakeSSHOnPATH(t, `#!/bin/sh
printf 'PARTIAL_SECRET_FILE_BYTES_MUST_NOT_LEAK\n'
i=0
while [ "$i" -lt 120 ]; do
  printf 'ssh stderr failure detail line %03d 0123456789abcdef\n' "$i" >&2
  i=$((i + 1))
done
exit 1
`)
	failRes, failErr := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{".ssh/id_ed25519"},
	})
	if failErr == nil || len(failRes.Lines) == 0 {
		t.Fatalf("expected failed pull to return error and detail line, got err=%v res=%+v", failErr, failRes)
	}
	errDetail := strings.Join(failRes.Lines, "\n") + "\n" + failErr.Error()
	if strings.Contains(errDetail, "PARTIAL_SECRET_FILE_BYTES_MUST_NOT_LEAK") {
		t.Fatalf("pull error leaked partial stdout bytes: %s", errDetail)
	}
	if !strings.Contains(errDetail, "ssh stderr failure detail") {
		t.Fatalf("expected stderr context in error, got: %s", errDetail)
	}
	if !strings.Contains(errDetail, "... (truncated)") {
		t.Fatalf("expected long stderr to be bounded/truncated, got len=%d: %s", len(errDetail), errDetail)
	}
}

func TestSymlinkParentEscapeRefusedLocalAndRemote(t *testing.T) {
	localHome := t.TempDir()
	remoteHome := t.TempDir()
	outsideDir := t.TempDir()

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("OUTSIDE-ORIGINAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideNote := filepath.Join(outsideDir, "note.txt")
	if err := os.WriteFile(outsideNote, []byte("OUTSIDE-NOTE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outsideDir, filepath.Join(localHome, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(remoteHome, "rem_escape")); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(remoteHome, "escape"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteHome, "escape", "secret.txt"), []byte("FROM-REMOTE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(localHome, "rem_escape", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localHome, "rem_escape", "secret.txt"), []byte("OVERWRITE-FROM-LOCAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localHome, "rem_escape", "sub", "new.txt"), []byte("NEW-FROM-LOCAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_TEST_REMOTE_HOME", remoteHome)
	installFakeSSHOnPATH(t, `#!/bin/sh
for arg in "$@"; do
  last="$arg"
done
HOME="$WS_TEST_REMOTE_HOME" /bin/sh -c "$last"
`)

	ctx := &app.Context{
		HomeDir: localHome,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	pullLocalRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{"escape/secret.txt", "escape/nested/new.txt"},
		Now:       fixedNow,
	})
	if err == nil || pullLocalRes.Transferred != 0 || pullLocalRes.Failed != 2 {
		t.Fatalf("expected local symlink parent pull to be refused, got err=%v res=%+v", err, pullLocalRes)
	}
	if got, rerr := os.ReadFile(outsideFile); rerr != nil || string(got) != "OUTSIDE-ORIGINAL\n" {
		t.Fatalf("outside file mutated by local pull: got %q err=%v", string(got), rerr)
	}
	if entries, rerr := os.ReadDir(outsideDir); rerr != nil || len(entries) != 2 {
		t.Fatalf("unexpected backup or directory created in outsideDir after local pull: entries=%v err=%v", entries, rerr)
	}

	pushLocalRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{"escape/secret.txt"},
		Now:       fixedNow,
	})
	if err == nil || pushLocalRes.Transferred != 0 || pushLocalRes.Failed != 1 {
		t.Fatalf("expected local symlink parent push to be refused, got err=%v res=%+v", err, pushLocalRes)
	}
	if got, rerr := os.ReadFile(filepath.Join(remoteHome, "escape", "secret.txt")); rerr != nil || string(got) != "FROM-REMOTE\n" {
		t.Fatalf("remote file mutated by refused local push: got %q err=%v", string(got), rerr)
	}

	pullRemRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{"rem_escape/secret.txt"},
		Now:       fixedNow,
	})
	if err == nil || pullRemRes.Transferred != 0 || pullRemRes.Failed != 1 {
		t.Fatalf("expected remote symlink parent pull to be refused, got err=%v res=%+v", err, pullRemRes)
	}
	if got, rerr := os.ReadFile(filepath.Join(localHome, "rem_escape", "secret.txt")); rerr != nil || string(got) != "OVERWRITE-FROM-LOCAL\n" {
		t.Fatalf("local file mutated by refused remote pull: got %q err=%v", string(got), rerr)
	}

	pushRemRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{"rem_escape/secret.txt", "rem_escape/sub/new.txt"},
		Now:       fixedNow,
	})
	if err == nil || pushRemRes.Transferred != 0 || pushRemRes.Failed != 2 {
		t.Fatalf("expected remote symlink parent push to be refused, got err=%v res=%+v", err, pushRemRes)
	}
	if got, rerr := os.ReadFile(outsideFile); rerr != nil || string(got) != "OUTSIDE-ORIGINAL\n" {
		t.Fatalf("outside file mutated by remote push: got %q err=%v", string(got), rerr)
	}
	if entries, rerr := os.ReadDir(outsideDir); rerr != nil || len(entries) != 2 {
		t.Fatalf("unexpected backup or directory created in outsideDir after remote push: entries=%v err=%v", entries, rerr)
	}

	if prev, err := PreviewFileTextDiff(ctx, "peer.tail.ts.net", "escape/note.txt", nil); err == nil {
		t.Fatalf("expected PreviewFileTextDiff to refuse local symlink parent, got %q", prev)
	}
	if prev, err := PreviewFileTextDiff(ctx, "peer.tail.ts.net", "rem_escape/note.txt", nil); err == nil {
		t.Fatalf("expected PreviewFileTextDiff to refuse remote symlink parent, got %q", prev)
	}

	diffRep, err := DiffNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncDiffOptions{
		ExtraPaths: []string{"escape/secret.txt", "rem_escape/secret.txt"},
	})
	if err != nil {
		t.Fatalf("DiffNodeFiles: %v", err)
	}
	for _, e := range diffRep.Entries {
		if e.RelPath == "escape/secret.txt" && e.Local.Exists {
			t.Fatalf("DiffNodeFiles should not read local file through symlink parent: %+v", e)
		}
		if e.RelPath == "rem_escape/secret.txt" && e.Remote.Exists {
			t.Fatalf("DiffNodeFiles should not read remote file through symlink parent: %+v", e)
		}
	}
}

func TestFleetSyncPreservesPrivateModesAndParentDirModes(t *testing.T) {
	localHome := t.TempDir()
	remoteHome := t.TempDir()

	writeFile := func(root, rel, content string, dirMode, fileMode os.FileMode) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), dirMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(full), dirMode); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, fileMode); err != nil {
			t.Fatal(err)
		}
	}

	for _, sensitive := range []string{
		"production.env",
		"app/staging.env",
		".envrc",
		"app/.env.prod",
	} {
		if !IsSensitiveSyncPath(sensitive) {
			t.Fatalf("expected IsSensitiveSyncPath(%q) = true", sensitive)
		}
	}
	for _, nonSensitive := range []string{
		".env.example",
		"example.env",
		"app/prod.example.env",
		"app/sample.env",
	} {
		if IsSensitiveSyncPath(nonSensitive) {
			t.Fatalf("expected IsSensitiveSyncPath(%q) = false", nonSensitive)
		}
	}

	writeFile(localHome, "sub/production.env", "SECRET=top-secret\n", 0o750, 0o600)
	writeFile(localHome, "priv/notes.txt", "private notes\n", 0o700, 0o600)
	writeFile(localHome, ".ssh/config", "Host peer\n  User dev\n", 0o700, 0o644)

	if err := os.MkdirAll(filepath.Join(remoteHome, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(remoteHome, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remoteHome, "priv"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(remoteHome, "priv"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remoteHome, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(remoteHome, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_TEST_REMOTE_HOME", remoteHome)
	installFakeSSHOnPATH(t, `#!/bin/sh
for arg in "$@"; do
  last="$arg"
done
HOME="$WS_TEST_REMOTE_HOME" /bin/sh -c "$last"
`)

	ctx := &app.Context{
		HomeDir: localHome,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	pushRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{"sub/production.env", "priv/notes.txt", ".ssh/config"},
		Now:       fixedNow,
	})
	if err != nil || pushRes.Transferred != 3 {
		t.Fatalf("SyncNodeFiles(push): err=%v res=%+v", err, pushRes)
	}

	checkPerm := func(p string, want os.FileMode) {
		t.Helper()
		fi, serr := os.Stat(p)
		if serr != nil {
			t.Fatalf("stat %s: %v", p, serr)
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("mode(%s) = %04o, want %04o", p, fi.Mode().Perm(), want)
		}
	}

	checkPerm(filepath.Join(remoteHome, "sub", "production.env"), 0o600)
	checkPerm(filepath.Join(remoteHome, "priv", "notes.txt"), 0o600)
	checkPerm(filepath.Join(remoteHome, ".ssh", "config"), 0o644)
	checkPerm(filepath.Join(remoteHome, ".ssh"), 0o700)
	checkPerm(filepath.Join(remoteHome, "priv"), 0o700)
	checkPerm(filepath.Join(remoteHome, "sub"), 0o750)

	writeFile(remoteHome, "pullsub/production.env", "REMOTE_SECRET=1\n", 0o750, 0o644)
	writeFile(remoteHome, "pullpriv/private.conf", "token=abc\n", 0o700, 0o600)
	if err := os.MkdirAll(filepath.Join(localHome, "pullpriv"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(localHome, "pullpriv"), 0o750); err != nil {
		t.Fatal(err)
	}

	pullRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{"pullsub/production.env", "pullpriv/private.conf"},
		Now:       fixedNow,
	})
	if err != nil || pullRes.Transferred != 2 {
		t.Fatalf("SyncNodeFiles(pull): err=%v res=%+v", err, pullRes)
	}

	checkPerm(filepath.Join(localHome, "pullsub", "production.env"), 0o600)
	checkPerm(filepath.Join(localHome, "pullpriv", "private.conf"), 0o600)
	checkPerm(filepath.Join(localHome, "pullpriv"), 0o750)

	var capturedScript string
	mockRunner := funcExecRunner(func(_ io.Reader, _ string, args ...string) ([]byte, []byte, error) {
		capturedScript = args[len(args)-1]
		return []byte("OK\t\n"), nil, nil
	})
	if _, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{"priv/notes.txt"},
		Runner:    mockRunner,
		Now:       fixedNow,
	}); err != nil {
		t.Fatalf("mock push: %v", err)
	}
	if !strings.Contains(capturedScript, "umask 077") || !strings.Contains(capturedScript, `chmod 0600 "$tmp" && cat > "$tmp"`) {
		t.Fatalf("expected temp file to be created 0600 before streaming, script:\n%s", capturedScript)
	}
}

func TestInsideHomeSymlinkParentAllowedLocalAndRemote(t *testing.T) {
	localHome := t.TempDir()
	remoteHome := t.TempDir()
	outsideDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("OUTSIDE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	localRepoDir := filepath.Join(localHome, "workstation", "config", "myapp")
	remoteRepoDir := filepath.Join(remoteHome, "workstation", "config", "myapp")
	for _, d := range []string{
		localRepoDir,
		remoteRepoDir,
		filepath.Join(localHome, ".config"),
		filepath.Join(remoteHome, ".config"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(localRepoDir, "settings.conf"), []byte("theme = dark\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRepoDir, "settings.conf"), []byte("theme = light\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(localRepoDir, filepath.Join(localHome, ".config", "myapp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(remoteRepoDir, filepath.Join(remoteHome, ".config", "myapp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(localRepoDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(remoteRepoDir, "escape")); err != nil {
		t.Fatal(err)
	}

	guard := remoteSafePathGuard(".config/myapp/settings.conf")
	if !strings.Contains(guard, "cd -P") || !strings.Contains(guard, "pwd -P") || strings.Contains(guard, "readlink") {
		t.Fatalf("expected portable cd -P / pwd -P guard without readlink, got: %s", guard)
	}

	t.Setenv("WS_TEST_REMOTE_HOME", remoteHome)
	installFakeSSHOnPATH(t, `#!/bin/sh
for arg in "$@"; do
  last="$arg"
done
HOME="$WS_TEST_REMOTE_HOME" /bin/sh -c "$last"
`)

	ctx := &app.Context{
		HomeDir: localHome,
		Host:    app.HostInfo{OS: "darwin", Arch: "arm64"},
	}
	fixedNow := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)

	rep, err := DiffNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncDiffOptions{
		ExtraPaths: []string{".config/myapp/settings.conf"},
	})
	if err != nil {
		t.Fatalf("DiffNodeFiles inside-home symlink parent: %v", err)
	}
	if len(rep.Entries) != 1 || !rep.Entries[0].Local.Exists || !rep.Entries[0].Remote.Exists {
		t.Fatalf("expected both local and remote files found through inside-home symlink parent, got %+v", rep.Entries)
	}

	prev, err := PreviewFileTextDiff(ctx, "peer.tail.ts.net", ".config/myapp/settings.conf", nil)
	if err != nil || !strings.Contains(prev, "-theme = dark") || !strings.Contains(prev, "+theme = light") {
		t.Fatalf("expected diff preview through inside-home symlink parent, got err=%v prev=%q", err, prev)
	}

	pushRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{".config/myapp/settings.conf"},
		Now:       fixedNow,
	})
	if err != nil || pushRes.Transferred != 1 {
		t.Fatalf("push through inside-home symlink parent failed: err=%v res=%+v", err, pushRes)
	}
	if got, rerr := os.ReadFile(filepath.Join(remoteRepoDir, "settings.conf")); rerr != nil || string(got) != "theme = dark\n" {
		t.Fatalf("expected remote file updated via inside-home symlink parent, got %q err=%v", string(got), rerr)
	}

	if err := os.WriteFile(filepath.Join(remoteRepoDir, "settings.conf"), []byte("theme = nord\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pullRes, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{".config/myapp/settings.conf"},
		Now:       fixedNow,
	})
	if err != nil || pullRes.Transferred != 1 {
		t.Fatalf("pull through inside-home symlink parent failed: err=%v res=%+v", err, pullRes)
	}
	if got, rerr := os.ReadFile(filepath.Join(localRepoDir, "settings.conf")); rerr != nil || string(got) != "theme = nord\n" {
		t.Fatalf("expected local file updated via inside-home symlink parent, got %q err=%v", string(got), rerr)
	}

	if _, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "pull",
		RelPaths:  []string{".config/myapp/escape/secret.txt"},
		Now:       fixedNow,
	}); err == nil {
		t.Fatalf("expected nested outside-home symlink parent escape to be refused")
	}
	if _, err := SyncNodeFiles(ctx, "peer", "peer.tail.ts.net", FileSyncOptions{
		Direction: "push",
		RelPaths:  []string{".config/myapp/escape/secret.txt"},
		Now:       fixedNow,
	}); err == nil {
		t.Fatalf("expected nested outside-home symlink parent push escape to be refused")
	}
}
