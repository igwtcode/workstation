package shell

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func newTestContext(t *testing.T, osName string) *app.Context {
	t.Helper()
	home := t.TempDir()
	repo := t.TempDir()
	return &app.Context{
		RepoRoot:  repo,
		HomeDir:   home,
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		Host: app.HostInfo{
			OS:   osName,
			Arch: "arm64",
		},
		LocalEnv: map[string]string{},
	}
}

func TestOSEnvRenderAndApply(t *testing.T) {
	linuxCtx := newTestContext(t, "linux")
	art, err := RenderOSEnv(linuxCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(art.Path, filepath.Join("environment.d", "ws-xdg.conf")) {
		t.Fatalf("unexpected linux os-env path: %q", art.Path)
	}
	for _, key := range []string{"XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "XDG_STATE_HOME=", "XDG_CACHE_HOME="} {
		if !strings.Contains(art.Content, key) {
			t.Fatalf("missing %q in linux os-env", key)
		}
	}
	linuxCtx.DryRun = true
	if err := ApplyOSEnv(linuxCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(art.Path); err == nil {
		t.Fatalf("dry-run should not write %s", art.Path)
	}
	linuxCtx.DryRun = false
	if err := ApplyOSEnv(linuxCtx); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(art.Path)
	if err != nil || string(written) != art.Content {
		t.Fatalf("applied linux os-env mismatch: err=%v", err)
	}

	macCtx := newTestContext(t, "darwin")
	macArt, err := RenderOSEnv(macCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(macArt.Path, filepath.Join("Library", "LaunchAgents", "dev.workstation.ws-env.plist")) {
		t.Fatalf("unexpected mac os-env path: %q", macArt.Path)
	}
	if !strings.Contains(macArt.Content, "launchctl setenv XDG_CONFIG_HOME") {
		t.Fatalf("missing launchctl setenv in mac plist")
	}
	dec := xml.NewDecoder(strings.NewReader(macArt.Content))
	for {
		if _, err := dec.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("invalid plist XML: %v", err)
		}
	}
	if err := ApplyOSEnv(macCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(macArt.Path); err != nil {
		t.Fatalf("expected mac plist written at %s: %v", macArt.Path, err)
	}
}

func TestZonesParseApplyReadAndWhoami(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "")

	t.Setenv("PATH", t.TempDir())

	seedDir := filepath.Join(ctx.RepoRoot, "config", "identity")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workSeed := `[env]
WS_ZONE = "work"
WS_CODE_ROOT = "{{env.HOME}}/work/code"
GH_CONFIG_DIR = "{{env.HOME}}/.config/gh-work"
CLAUDE_CONFIG_DIR = "{{env.HOME}}/.config/claude-work"
AWS_CONFIG_FILE = "{{env.HOME}}/.aws/work-config"
AWS_SHARED_CREDENTIALS_FILE = "{{env.HOME}}/.aws/work-credentials"
KUBECONFIG = "{{env.HOME}}/.kube/work-config"
`
	personalSeed := `[env]
WS_ZONE = "personal"
WS_CODE_ROOT = "{{env.HOME}}/personal/code"
GH_CONFIG_DIR = "{{env.HOME}}/.config/gh-personal"
CLAUDE_CONFIG_DIR = "{{env.HOME}}/.config/claude-personal"
CODEX_HOME = "{{ env.HOME }}/.codex"
AWS_CONFIG_FILE = "{{env.HOME}}/.aws/personal-config"
AWS_SHARED_CREDENTIALS_FILE = "{{env.HOME}}/.aws/personal-credentials"
KUBECONFIG = "{{env.HOME}}/.kube/personal-config"
`
	if err := os.WriteFile(filepath.Join(seedDir, "work.mise.toml"), []byte(workSeed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "personal.mise.toml"), []byte(personalSeed), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx.LocalEnv["WS_ZONES"] = `
# sample zones
work|Sample Worker|worker@example.invalid|ssh-ed25519 AAAATEST|work-dev
personal|Sample User|user@example.invalid
`
	specs, err := ParseZones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Name != "work" || specs[0].AWSProfile != "work-dev" || specs[1].Name != "personal" {
		t.Fatalf("unexpected specs: %+v", specs)
	}

	actions, err := ApplyZones(ctx, "")
	if err != nil {
		t.Fatalf("ApplyZones failed: %v", err)
	}
	if len(actions) == 0 {
		t.Fatalf("expected scaffold actions")
	}

	workCfg := filepath.Join(ctx.HomeDir, "work", "mise.toml")
	customLine := "\nCUSTOM_LOCAL_VAR = \"keep-me\"\n"
	orig, err := os.ReadFile(workCfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workCfg, append(orig, []byte(customLine)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyZones(ctx, "work"); err != nil {
		t.Fatalf("re-ApplyZones failed: %v", err)
	}
	envMap, err := ReadZoneMiseEnv(filepath.Join(ctx.HomeDir, "work"))
	if err != nil {
		t.Fatal(err)
	}
	if envMap["CUSTOM_LOCAL_VAR"] != "keep-me" {
		t.Fatalf("expected existing mise.toml edits preserved, got %+v", envMap)
	}
	if envMap["WS_CODE_ROOT"] != filepath.Join(ctx.HomeDir, "work", "code") {
		t.Fatalf("expected expanded WS_CODE_ROOT, got %q", envMap["WS_CODE_ROOT"])
	}
	if envMap["AWS_PROFILE"] != "work-dev" {
		t.Fatalf("expected AWS_PROFILE from mise.local.toml, got %q", envMap["AWS_PROFILE"])
	}

	personalEnv, err := ReadZoneMiseEnv(filepath.Join(ctx.HomeDir, "personal"))
	if err != nil {
		t.Fatal(err)
	}
	if personalEnv["CODEX_HOME"] != filepath.Join(ctx.HomeDir, ".codex") {
		t.Fatalf("expected spaced {{ env.HOME }} expansion, got %q", personalEnv["CODEX_HOME"])
	}

	gitCfgRaw, err := os.ReadFile(filepath.Join(ctx.HomeDir, "work", ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gitCfgRaw), "format = ssh") || !strings.Contains(string(gitCfgRaw), "gpgsign = true") {
		t.Fatalf("expected ssh signing config in work .gitconfig:\n%s", string(gitCfgRaw))
	}

	statuses, err := ZoneStatuses(ctx)
	if err != nil || len(statuses) != 2 {
		t.Fatalf("ZoneStatuses: err=%v len=%d", err, len(statuses))
	}
	if !statuses[0].RootExists || !statuses[0].ConfigExists || !statuses[0].GitConfigExists {
		t.Fatalf("expected scaffolded zone status: %+v", statuses[0])
	}

	workSub := filepath.Join(ctx.HomeDir, "work", "code", "service")
	rep, err := Whoami(ctx, workSub)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ActiveZone != "work" || rep.ZoneMismatch || rep.GitEmail != "worker@example.invalid" || rep.AWSProfile != "work-dev" {
		t.Fatalf("unexpected whoami report: %+v", rep)
	}

	t.Setenv("WS_ZONE", "personal")
	mismatch, err := Whoami(ctx, workSub)
	if err != nil {
		t.Fatal(err)
	}
	if !mismatch.ZoneMismatch || mismatch.PathZone != "work" || mismatch.EnvZone != "personal" {
		t.Fatalf("expected zone mismatch detection, got %+v", mismatch)
	}

	zoneEnv, err := ResolveZoneEnv(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(zoneEnv, "WS_ZONE=work") || !slices.Contains(zoneEnv, "AWS_PROFILE=work-dev") {
		t.Fatalf("unexpected ResolveZoneEnv: %+v", zoneEnv)
	}
	merged := MergeEnv([]string{"WS_ZONE=personal", "KEEP=1"}, zoneEnv)
	if !slices.Contains(merged, "WS_ZONE=work") || slices.Contains(merged, "WS_ZONE=personal") || !slices.Contains(merged, "KEEP=1") {
		t.Fatalf("unexpected MergeEnv result: %+v", merged)
	}
}

func TestParseZonesErrors(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_ZONES", "")

	for _, bad := range []string{
		"broken|onlytwo",
		"../escape|User|u@example.invalid",
		".hidden|User|u@example.invalid",
		"work|A|a@example.invalid\nwork|B|b@example.invalid",
	} {
		ctx.LocalEnv["WS_ZONES"] = bad
		if _, err := ParseZones(ctx); err == nil {
			t.Fatalf("expected error for malformed WS_ZONES %q", bad)
		}
	}
}

func TestDiscoverReposAndAWSProfiles(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "work")

	for _, z := range []string{"personal", "work"} {
		zroot := filepath.Join(ctx.HomeDir, z)
		if err := os.MkdirAll(filepath.Join(zroot, "code"), 0o755); err != nil {
			t.Fatal(err)
		}
		miseContent := "[env]\n" +
			"WS_ZONE = \"" + z + "\"\n" +
			"WS_CODE_ROOT = \"{{env.HOME}}/" + z + "/code\"\n" +
			"AWS_CONFIG_FILE = \"{{env.HOME}}/.aws/" + z + "-config\"\n" +
			"AWS_SHARED_CREDENTIALS_FILE = \"{{env.HOME}}/.aws/" + z + "-credentials\"\n"
		if err := os.WriteFile(filepath.Join(zroot, "mise.toml"), []byte(miseContent), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, p := range []string{
		filepath.Join(ctx.HomeDir, "work", "code", "api", ".git"),
		filepath.Join(ctx.HomeDir, "personal", "code", "org", "app", ".git"),
		filepath.Join(ctx.HomeDir, "personal", "code", "a", "b", "c", "too-deep", ".git"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	repos, err := DiscoverRepos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos within depth 4, got %+v", repos)
	}
	if repos[0].Zone != "work" || repos[0].Name != "api" {
		t.Fatalf("expected active zone work first, got %+v", repos[0])
	}
	if repos[1].Zone != "personal" || repos[1].Name != "app" {
		t.Fatalf("expected personal/app second, got %+v", repos[1])
	}

	awsDir := filepath.Join(ctx.HomeDir, ".aws")
	if err := os.MkdirAll(awsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	workCfg := "[default]\nregion = us-east-1\n[profile prod]\nregion = eu-west-1\n[sso-session corp]\nsso_region = us-east-1\n"
	workCred := "[staging]\naws_access_key_id = X\n"
	personalCfg := "[profile sandbox]\nregion = us-west-2\n"
	if err := os.WriteFile(filepath.Join(awsDir, "work-config"), []byte(workCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awsDir, "work-credentials"), []byte(workCred), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awsDir, "personal-config"), []byte(personalCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := DiscoverAWSProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(profiles))
	for _, p := range profiles {
		gotNames = append(gotNames, p.Zone+":"+p.Name)
	}
	wantNames := []string{"work:default", "work:prod", "work:staging", "personal:sandbox"}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("unexpected AWS profiles: got %v want %v", gotNames, wantNames)
	}
}

func TestPullReposConcurrent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := newTestContext(t, "linux")
	scanDir := filepath.Join(ctx.HomeDir, "code")
	remoteDir := filepath.Join(ctx.HomeDir, "remote.git")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = MergeEnv(os.Environ(), []string{
			"HOME=" + ctx.HomeDir,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Sample",
			"GIT_AUTHOR_EMAIL=sample@example.invalid",
			"GIT_COMMITTER_NAME=Sample",
			"GIT_COMMITTER_EMAIL=sample@example.invalid",
		})
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
		}
	}

	runGit(ctx.HomeDir, "init", "--bare", remoteDir)

	repoUpdated := filepath.Join(scanDir, "repo-updated")
	runGit(scanDir, "clone", remoteDir, repoUpdated)
	if err := os.WriteFile(filepath.Join(repoUpdated, "README.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoUpdated, "add", "README.md")
	runGit(repoUpdated, "commit", "-m", "init")
	runGit(repoUpdated, "push", "-u", "origin", "HEAD")

	repoUpToDate := filepath.Join(scanDir, "repo-uptodate")
	runGit(scanDir, "clone", remoteDir, repoUpToDate)
	if err := os.WriteFile(filepath.Join(repoUpToDate, "README.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoUpToDate, "add", "README.md")
	runGit(repoUpToDate, "commit", "-m", "v2")
	runGit(repoUpToDate, "push")

	repoDirty := filepath.Join(scanDir, "repo-dirty")
	runGit(scanDir, "clone", remoteDir, repoDirty)
	if err := os.WriteFile(filepath.Join(repoDirty, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repoLocal := filepath.Join(scanDir, "repo-local")
	if err := os.MkdirAll(repoLocal, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(repoLocal, "init")
	if err := os.WriteFile(filepath.Join(repoLocal, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoLocal, "add", "a.txt")
	runGit(repoLocal, "commit", "-m", "local")

	summary, err := PullRepos(ctx, scanDir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || summary.Updated != 1 || summary.UpToDate != 1 || summary.Dirty != 1 || summary.NoUpstream != 1 || summary.Failed != 0 {
		t.Fatalf("unexpected pull summary: %+v", summary)
	}
}

func TestGenSecret(t *testing.T) {
	hexVal, err := GenSecret("hex")
	if err != nil {
		t.Fatal(err)
	}
	if len(hexVal) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(hexVal))
	}
	if _, err := hex.DecodeString(hexVal); err != nil {
		t.Fatalf("invalid hex output: %v", err)
	}

	b64Val, err := GenSecret("base64")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64Val)
	if err != nil || len(raw) != 60 {
		t.Fatalf("expected 60 decoded base64 bytes, got len=%d err=%v", len(raw), err)
	}

	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	u1, err := GenSecret("uuid")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := GenSecret("uuid")
	if err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(u1) || u1 == u2 {
		t.Fatalf("unexpected uuid values: %q vs %q", u1, u2)
	}

	if _, err := GenSecret("invalid"); err == nil {
		t.Fatalf("expected error for invalid secret kind")
	}
}

func TestVaultOpGuards(t *testing.T) {
	ctx := newTestContext(t, "linux")
	ctx.DryRun = true
	if err := VaultOpWithOptions(ctx, "load", "ssh", nil, VaultOptions{}); err == nil {
		t.Fatalf("expected VaultOpWithOptions to refuse in DryRun")
	}

	ctx.DryRun = false
	t.Setenv("PATH", t.TempDir())
	if err := VaultOpWithOptions(ctx, "load", "ssh", nil, VaultOptions{}); err == nil || !strings.Contains(err.Error(), "op") {
		t.Fatalf("expected missing op error, got %v", err)
	}
	if err := VaultOpWithOptions(ctx, "load", "badscope", nil, VaultOptions{}); err == nil {
		t.Fatalf("expected invalid scope error")
	}
	if err := VaultOpWithOptions(ctx, "badmode", "ssh", nil, VaultOptions{}); err == nil {
		t.Fatalf("expected invalid mode error")
	}
}

func TestDiscoverAndFindPlugins(t *testing.T) {
	ctx := newTestContext(t, "linux")
	binDir := t.TempDir()
	localTasksDir := filepath.Join(ctx.RepoRoot, ".mise", "tasks", "local")
	if err := os.MkdirAll(localTasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(binDir, "ws-alpha"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "ws-nonexec"), []byte("noop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localTasksDir, "beta"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(localTasksDir, "alpha"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)
	plugins, err := DiscoverPlugins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %+v", plugins)
	}
	if plugins[0].Name != "alpha" || plugins[0].Source != "path" {
		t.Fatalf("expected alpha from path, got %+v", plugins[0])
	}
	if plugins[1].Name != "beta" || plugins[1].Source != "local-task" {
		t.Fatalf("expected beta from local-task, got %+v", plugins[1])
	}

	found, err := FindPlugin(ctx, "beta")
	if err != nil || found.Source != "local-task" {
		t.Fatalf("FindPlugin(beta) failed: %+v %v", found, err)
	}
	if _, err := FindPlugin(ctx, "missing"); err == nil {
		t.Fatalf("expected error for missing plugin")
	}
}

func TestZipUnzipModePreservationAndHardening(t *testing.T) {
	srcRoot := t.TempDir()
	mustWrite := func(rel, content string, mode os.FileMode) {
		t.Helper()
		full := filepath.Join(srcRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, mode); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(".ssh/id_ed25519", "PRIVATE-KEY\n", 0o600)
	mustWrite(".ssh/id_ed25519.pub", "ssh-ed25519 AAAA\n", 0o644)
	mustWrite(".mise/tasks/local/deploy", "#!/bin/sh\necho ok\n", 0o755)
	mustWrite("notes.txt", "hello\n", 0o664)
	mustWrite("tool.sh", "#!/bin/sh\n", 0o777)

	zipPath := filepath.Join(t.TempDir(), "vault.zip")
	if err := zipRelativePaths(srcRoot, []string{".ssh", ".mise/tasks/local", "notes.txt", "tool.sh"}, zipPath); err != nil {
		t.Fatalf("zipRelativePaths: %v", err)
	}

	dstRoot := t.TempDir()
	if err := unzipArchiveWithPolicy(zipPath, dstRoot, true); err != nil {
		t.Fatalf("unzipArchiveWithPolicy: %v", err)
	}

	checkPerm := func(rel string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(dstRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Fatalf("%s perm = %04o, want %04o", rel, got, want)
		}
	}
	checkPerm(".ssh/id_ed25519", 0o600)
	checkPerm(".ssh/id_ed25519.pub", 0o644)
	checkPerm(".mise/tasks/local/deploy", 0o755)
	checkPerm("notes.txt", 0o644)
	checkPerm("tool.sh", 0o755)

	legacyZip := filepath.Join(t.TempDir(), "legacy.zip")
	f, err := os.Create(legacyZip)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range [][2]string{
		{".ssh/id_rsa", "LEGACY-KEY"},
		{".ssh/id_rsa.pub", "ssh-rsa AAAA"},
		{".mise/tasks/local/helper", "#!/bin/sh"},
		{"mise.local.toml", "[env]\nFOO = \"bar\"\n"},
	} {
		w, err := zw.Create(entry[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	legacyDst := t.TempDir()
	if err := unzipArchiveWithPolicy(legacyZip, legacyDst, true); err != nil {
		t.Fatalf("unzipArchiveWithPolicy legacy: %v", err)
	}
	checkLegacy := func(rel string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(filepath.Join(legacyDst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Fatalf("legacy %s perm = %04o, want %04o", rel, got, want)
		}
	}
	checkLegacy(".ssh/id_rsa", 0o600)
	checkLegacy(".ssh/id_rsa.pub", 0o644)
	checkLegacy(".mise/tasks/local/helper", 0o755)
	checkLegacy("mise.local.toml", 0o600)
}

func TestVaultPullAllowReplaceAndUnsetVaultWarning(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_OP_VAULT", "")

	sshDir := filepath.Join(ctx.HomeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VaultOpWithOptions(ctx, "pull", "ssh", nil, VaultOptions{}); err == nil || !strings.Contains(err.Error(), "requires --allow replace") {
		t.Fatalf("expected ssh pull without --allow replace to fail, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(ctx.RepoRoot, "mise.local.toml"), []byte("[env]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VaultOpWithOptions(ctx, "pull", "secrets", nil, VaultOptions{}); err == nil || !strings.Contains(err.Error(), "requires --allow replace") {
		t.Fatalf("expected secrets pull without --allow replace to fail, got %v", err)
	}

	codeDir := filepath.Join(ctx.HomeDir, "work", "code")
	if err := os.MkdirAll(filepath.Join(codeDir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codeDir, "svc", ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VaultOpWithOptions(ctx, "pull", "envfiles", []string{codeDir}, VaultOptions{}); err == nil || !strings.Contains(err.Error(), "requires --allow replace") {
		t.Fatalf("expected envfiles pull without --allow replace to fail, got %v", err)
	}

	zipSrc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(zipSrc, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zipSrc, ".ssh", "id_ed25519"), []byte("NEW-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zipSrc, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 NEW\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mockZip := filepath.Join(t.TempDir(), "ssh.zip")
	if err := zipRelativePaths(zipSrc, []string{".ssh"}, mockZip); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	mockOp := "#!/bin/sh\ncase \"$1\" in\n  whoami) exit 0 ;;\n  document)\n    case \"$2\" in\n      list) printf '[{\"id\":\"doc1\",\"title\":\"~/.ssh.zip\"}]\\n' ;;\n      get) cp \"$MOCK_SSH_ZIP\" \"$5\" ;;\n    esac\n    ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "op"), []byte(mockOp), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_SSH_ZIP", mockZip)
	t.Setenv("PATH", binDir+":/usr/bin:/bin")

	ctx.Allow.Replace = true
	var warnBuf bytes.Buffer
	if err := VaultOpWithOptions(ctx, "pull", "ssh", nil, VaultOptions{WarnOut: &warnBuf}); err != nil {
		t.Fatalf("VaultOpWithOptions pull ssh failed: %v", err)
	}
	if !strings.Contains(warnBuf.String(), "WS_OP_VAULT") {
		t.Fatalf("expected warning about unset WS_OP_VAULT, got %q", warnBuf.String())
	}
	gotKey, err := os.ReadFile(filepath.Join(sshDir, "id_ed25519"))
	if err != nil || string(gotKey) != "NEW-KEY\n" {
		t.Fatalf("expected overwritten key, got %q (err=%v)", string(gotKey), err)
	}
	if fi, err := os.Stat(filepath.Join(sshDir, "id_ed25519")); err != nil {
		t.Fatalf("stat pulled private key: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 on pulled private key, got %04o", fi.Mode().Perm())
	}
}

func TestPullReposDryRunStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := newTestContext(t, "linux")
	scanDir := filepath.Join(ctx.HomeDir, "code")
	remoteDir := filepath.Join(ctx.HomeDir, "remote.git")
	if err := os.MkdirAll(scanDir, 0o755); err != nil {
		t.Fatal(err)
	}

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = MergeEnv(os.Environ(), []string{
			"HOME=" + ctx.HomeDir,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Sample",
			"GIT_AUTHOR_EMAIL=sample@example.invalid",
			"GIT_COMMITTER_NAME=Sample",
			"GIT_COMMITTER_EMAIL=sample@example.invalid",
		})
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}

	runGit(ctx.HomeDir, "init", "--bare", remoteDir)
	repoDir := filepath.Join(scanDir, "demo")
	runGit(scanDir, "clone", remoteDir, repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "README.md")
	runGit(repoDir, "commit", "-m", "init")
	runGit(repoDir, "push", "-u", "origin", "HEAD")

	ctx.DryRun = true
	summary, err := PullRepos(ctx, scanDir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 1 || summary.DryRun != 1 || summary.UpToDate != 0 || summary.Items[0].Status != PullDryRun {
		t.Fatalf("expected dry-run status in DryRun mode, got %+v", summary)
	}
}

func TestReplaceZoneEnvAndGitConfigQuoting(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"KEEP=1",
		"WS_ZONE=work",
		"WS_CODE_ROOT=/home/sample/work/code",
		"GH_CONFIG_DIR=/home/sample/.config/gh-work",
		"CLAUDE_CONFIG_DIR=/home/sample/.config/claude-work",
		"CODEX_HOME=/home/sample/.codex-work",
		"AWS_CONFIG_FILE=/home/sample/.aws/work-config",
		"AWS_SHARED_CREDENTIALS_FILE=/home/sample/.aws/work-creds",
		"AWS_PROFILE=work-prod",
		"KUBECONFIG=/home/sample/.kube/work-config",
	}
	overlay := []string{
		"WS_ZONE=personal",
		"WS_CODE_ROOT=/home/sample/personal/code",
	}
	got := ReplaceZoneEnv(base, overlay)
	envMap := map[string]string{}
	for _, entry := range got {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			envMap[k] = v
		}
	}
	if envMap["WS_ZONE"] != "personal" || envMap["WS_CODE_ROOT"] != "/home/sample/personal/code" {
		t.Fatalf("expected overlay zone vars applied, got %+v", envMap)
	}
	if envMap["KEEP"] != "1" || envMap["PATH"] != "/usr/bin" {
		t.Fatalf("expected non-zone vars preserved, got %+v", envMap)
	}
	for _, leaked := range []string{
		"GH_CONFIG_DIR", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
		"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "AWS_PROFILE", "KUBECONFIG",
	} {
		if v, exists := envMap[leaked]; exists {
			t.Fatalf("expected old zone var %s to be stripped, leaked %q", leaked, v)
		}
	}

	spec := ZoneSpec{
		Name:       "work",
		GitName:    `Sample "Dev" #1`,
		GitEmail:   "dev@example.invalid",
		SigningKey: "ssh-ed25519 AAAA key #comment",
	}
	rendered := renderZoneGitConfig(spec)
	if !strings.Contains(rendered, `name = "Sample \"Dev\" #1"`) {
		t.Fatalf("expected safely quoted git name, got:\n%s", rendered)
	}
	tmpCfg := filepath.Join(t.TempDir(), ".gitconfig")
	if err := os.WriteFile(tmpCfg, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	gotName, gotEmail, gotKey := parseGitUserConfig(tmpCfg)
	if gotName != spec.GitName || gotEmail != spec.GitEmail || gotKey != spec.SigningKey {
		t.Fatalf("gitconfig round-trip mismatch: got (%q,%q,%q) want (%q,%q,%q)",
			gotName, gotEmail, gotKey, spec.GitName, spec.GitEmail, spec.SigningKey)
	}

	ctx := newTestContext(t, "linux")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WS_ZONES", "")
	ctx.LocalEnv["WS_ZONES"] = "work|Sample Worker|worker@example.invalid"
	workGitCfg := filepath.Join(ctx.HomeDir, "work", ".gitconfig")
	if err := os.MkdirAll(filepath.Dir(workGitCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	handWritten := "[user]\n\tname = Custom Hand Written\n"
	if err := os.WriteFile(workGitCfg, []byte(handWritten), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err := ApplyZones(ctx, "work")
	if err != nil {
		t.Fatalf("ApplyZones: %v", err)
	}
	after, err := os.ReadFile(workGitCfg)
	if err != nil || string(after) != handWritten {
		t.Fatalf("expected hand-written .gitconfig preserved, got %q (actions=%v)", string(after), actions)
	}
}

func TestPhase3ShellRegressions(t *testing.T) {

	ctx := newTestContext(t, "linux")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WS_ZONES", "")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("GH_HOST", "github.enterprise.invalid")
	ctx.LocalEnv["WS_ZONES"] = "work|Work|work@example.invalid\npersonal|Pers|p@example.invalid\n"
	workDir := filepath.Join(ctx.HomeDir, "work")
	_ = os.MkdirAll(filepath.Join(workDir, "code"), 0o755)
	_ = os.WriteFile(filepath.Join(workDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nCUSTOM_ZONE_SECRET = \"secret\"\n"), 0o600)
	t.Setenv("CUSTOM_ZONE_SECRET", "leaked")
	t.Setenv("WS_ZONE", "personal")

	who, err := Whoami(ctx, workDir)
	if err != nil || !who.ZoneMismatch {
		t.Fatalf("expected ZoneMismatch, got %+v err=%v", who, err)
	}
	if who.AWSProfile != "" || who.CodexHome != "" {
		t.Fatalf("expected Whoami to suppress os.Getenv fallback on ZoneMismatch, got %+v", who)
	}
	if got := ReplaceZoneEnvForContext(ctx, []string{"AWS_REGION=us-east-1", "GH_HOST=ent", "CUSTOM_ZONE_SECRET=leaked", "KEEP=1"}, []string{"WS_ZONE=personal"}); slices.Contains(got, "AWS_REGION=us-east-1") || slices.Contains(got, "GH_HOST=ent") || slices.Contains(got, "CUSTOM_ZONE_SECRET=leaked") || !slices.Contains(got, "KEEP=1") {
		t.Fatalf("ReplaceZoneEnvForContext leaked zone/prefix env: %v", got)
	}

	_ = os.MkdirAll(filepath.Join(ctx.HomeDir, ".aws"), 0o700)
	escapeTarget := filepath.Join(t.TempDir(), "should-not-create")
	danglingCreds := filepath.Join(ctx.HomeDir, ".aws", "work-dangling-creds")
	_ = os.Symlink(escapeTarget, danglingCreds)
	_ = os.WriteFile(filepath.Join(workDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nAWS_SHARED_CREDENTIALS_FILE = \"{{config_root}}/../.aws/work-dangling-creds\"\n"), 0o600)
	if _, err := ApplyZones(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(escapeTarget); !os.IsNotExist(err) {
		t.Fatalf("ApplyZones wrote through dangling symlink!")
	}

	symMise := filepath.Join(ctx.HomeDir, "personal", "mise.toml")
	_ = os.MkdirAll(filepath.Dir(symMise), 0o755)
	seedTarget := filepath.Join(ctx.RepoRoot, "personal.seed.toml")
	_ = os.WriteFile(seedTarget, []byte("[env]\nWS_ZONE = \"personal\"\n"), 0o600)
	_ = os.Symlink(seedTarget, symMise)
	if _, err := FixZoneSymlinks(ctx, "personal"); err == nil {
		t.Fatalf("expected FixZoneSymlinks without Allow.Replace to fail")
	}
	ctx.Allow.Replace = true
	if _, err := FixZoneSymlinks(ctx, "personal"); err != nil {
		t.Fatalf("FixZoneSymlinks failed: %v", err)
	}
	if fi, _ := os.Lstat(symMise); fi == nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected real file after FixZoneSymlinks, got %+v", fi)
	}

	srcCode := filepath.Join(ctx.HomeDir, "code")
	_ = os.MkdirAll(filepath.Join(srcCode, "app1"), 0o755)
	_ = os.MkdirAll(filepath.Join(srcCode, "skipme"), 0o755)
	_ = os.MkdirAll(filepath.Join(ctx.HomeDir, "work", "code", "skipme"), 0o755)
	rep, err := MigrateZoneDirs(ctx, MigrateZoneOptions{Zone: "work", All: true})
	if err != nil {
		t.Fatalf("MigrateZoneDirs: %v", err)
	}
	if rep.Moved != 1 || rep.Skipped != 1 {
		t.Fatalf("expected Moved=1 Skipped=1, got %+v", rep)
	}
	ctx.RepoRoot = filepath.Join(srcCode, "ws-home")
	_ = os.MkdirAll(ctx.RepoRoot, 0o755)
	if _, err := MigrateZoneDirs(ctx, MigrateZoneOptions{Zone: "work", All: true}); err == nil {
		t.Fatalf("expected MigrateZoneDirs to block moving directory containing RepoRoot")
	}

	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_ZONE", "personal")
	if err := VaultOpWithOptions(ctx, "load", "files", []string{"~/../../etc/passwd"}, VaultOptions{}); err == nil || !strings.Contains(err.Error(), "escapes home directory") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if gotZone := zoneForPath(ctx, filepath.Join(ctx.HomeDir, "work", "code")); gotZone != "work" {
		t.Fatalf("zoneForPath(work/code) = %q, want work", gotZone)
	}

	noZoneCtx := newTestContext(t, "linux")
	_ = os.MkdirAll(filepath.Join(noZoneCtx.HomeDir, ".aws"), 0o700)
	_ = os.WriteFile(filepath.Join(noZoneCtx.HomeDir, ".aws", "config"), []byte("[default]\n[profile dev]\n"), 0o600)
	profs, err := DiscoverAWSProfiles(noZoneCtx)
	if err != nil || len(profs) != 2 || profs[0].Zone != "default" {
		t.Fatalf("unexpected non-zone AWS profiles: %+v err=%v", profs, err)
	}
	noZoneCtx.ConfigDir = "/tmp/my config/dir"
	art, err := RenderOSEnv(noZoneCtx)
	if err != nil || !strings.Contains(art.Content, `XDG_CONFIG_HOME="/tmp/my config/dir"`) {
		t.Fatalf("expected quoted environment.d value, got %q err=%v", art.Content, err)
	}
	noZoneCtx.ConfigDir = "/tmp/bad\nline"
	if _, err := RenderOSEnv(noZoneCtx); err == nil {
		t.Fatalf("expected RenderOSEnv to reject newline in env value")
	}
}

func TestDiscoverVaultItems(t *testing.T) {
	ctx := newTestContext(t, "linux")
	wsBase := ctx.WSBaseDir()
	if err := os.MkdirAll(wsBase, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx.WSDBPath(), []byte("sqlite-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx.WSKeyPath(), []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ctx.HomeDir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, ".ssh", "config"), []byte("Host demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.RepoRoot, "mise.local.toml"), []byte("[env]\nFOO = \"bar\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ctx.HomeDir, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, ".aws", "credentials"), []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workCode := filepath.Join(ctx.HomeDir, "work", "code", "app")
	if err := os.MkdirAll(workCode, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.HomeDir, "work", "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workCode, ".env"), []byte("TOKEN=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_ZONES", "work")
	t.Setenv("WS_OP_FILES", ".aws/credentials")
	t.Setenv("PATH", t.TempDir())

	inv := DiscoverVaultItems(ctx)
	byID := map[string]VaultItem{}
	for _, it := range inv.Items {
		byID[it.ID] = it
	}

	for _, want := range []string{"ws-db", "ws-db-key", "ssh", "secrets", "file:.aws/credentials", "envfiles:work"} {
		it, ok := byID[want]
		if !ok {
			t.Fatalf("missing vault inventory item %q in %+v", want, inv.Items)
		}
		if !it.ExistsLocal || it.FileCount < 1 {
			t.Fatalf("expected item %q to report ExistsLocal=true FileCount>=1, got %+v", want, it)
		}
	}
	if byID["ws-db"].DocTitle != "ws-db-backup.enc" || byID["ws-db-key"].DocTitle != "ws-db.key" {
		t.Fatalf("unexpected doc titles: db=%q key=%q", byID["ws-db"].DocTitle, byID["ws-db-key"].DocTitle)
	}
	if byID["envfiles:work"].DocTitle != "workstation-envfiles-work.zip" {
		t.Fatalf("unexpected envfiles doc title: %q", byID["envfiles:work"].DocTitle)
	}
}

func TestVaultSSHLoadBacksUpReplacedFiles(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_OP_VAULT", "Personal")

	sshDir := filepath.Join(ctx.HomeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	origKeyPath := filepath.Join(sshDir, "id_ed25519")
	untouchedCfg := filepath.Join(sshDir, "config")
	if err := os.WriteFile(origKeyPath, []byte("OLD-PRIVATE-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untouchedCfg, []byte("Host keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	zipSrc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(zipSrc, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zipSrc, ".ssh", "id_ed25519"), []byte("NEW-PRIVATE-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zipSrc, ".ssh", "new_only"), []byte("BRAND-NEW\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mockZip := filepath.Join(t.TempDir(), "ssh.zip")
	if err := zipRelativePaths(zipSrc, []string{".ssh"}, mockZip); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	mockOp := "#!/bin/sh\ncase \"$1\" in\n  whoami) exit 0 ;;\n  document)\n    case \"$2\" in\n      list) printf '[{\"id\":\"doc1\",\"title\":\"~/.ssh.zip\"}]\\n' ;;\n      get) cp \"$MOCK_SSH_ZIP\" \"$5\" ;;\n    esac\n    ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "op"), []byte(mockOp), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_SSH_ZIP", mockZip)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")

	ctx.Allow.Replace = true
	var warnBuf bytes.Buffer
	if err := VaultOpWithOptions(ctx, "load", "ssh", nil, VaultOptions{WarnOut: &warnBuf}); err != nil {
		t.Fatalf("VaultOpWithOptions load ssh: %v", err)
	}

	gotNew, err := os.ReadFile(origKeyPath)
	if err != nil || string(gotNew) != "NEW-PRIVATE-KEY\n" {
		t.Fatalf("expected replaced key content, got %q err=%v", string(gotNew), err)
	}

	backups, err := filepath.Glob(filepath.Join(sshDir, "id_ed25519.*.pre-ws-vault"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected 1 timestamped .pre-ws-vault backup for id_ed25519, got %v err=%v", backups, err)
	}
	gotBackup, err := os.ReadFile(backups[0])
	if err != nil || string(gotBackup) != "OLD-PRIVATE-KEY\n" {
		t.Fatalf("backup content mismatch: got %q err=%v", string(gotBackup), err)
	}
	bfi, err := os.Stat(backups[0])
	if err != nil || bfi.Mode().Perm() != 0o600 {
		t.Fatalf("expected backup mode 0600, got %v err=%v", bfi, err)
	}
	if !strings.Contains(warnBuf.String(), filepath.Base(backups[0])) {
		t.Fatalf("expected output to report backup path %q, got %q", backups[0], warnBuf.String())
	}

	if extra, _ := filepath.Glob(filepath.Join(sshDir, "new_only.*.pre-ws-vault")); len(extra) != 0 {
		t.Fatalf("did not expect backup for newly created file, got %v", extra)
	}
	if extra, _ := filepath.Glob(filepath.Join(sshDir, "config.*.pre-ws-vault")); len(extra) != 0 {
		t.Fatalf("did not expect backup for untouched file, got %v", extra)
	}
}

func TestMigrateZoneDirsFallsBackOnEXDEV(t *testing.T) {
	ctx := newTestContext(t, "linux")
	t.Setenv("WS_ZONES", "")
	ctx.LocalEnv["WS_ZONES"] = "work|Work|work@example.invalid"

	srcRoot := filepath.Join(ctx.HomeDir, "code")
	projSrc := filepath.Join(srcRoot, "proj")
	subDir := filepath.Join(projSrc, "bin")
	roDir := filepath.Join(projSrc, "readonly")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projSrc, "README.md"), []byte("hello exdev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "run.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roDir, "secret.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(projSrc, "readme-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("does-not-exist", filepath.Join(projSrc, "dangling-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = makeTreeWritable(srcRoot)
		_ = makeTreeWritable(filepath.Join(ctx.HomeDir, "work"))
	})

	prevRename := renameDirFn
	renameDirFn = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { renameDirFn = prevRename })

	if _, err := MigrateZoneDirs(ctx, MigrateZoneOptions{Zone: "work"}); err == nil {
		t.Fatal("expected consent gate to still refuse migration without explicit entries or --all")
	}

	rep, err := MigrateZoneDirs(ctx, MigrateZoneOptions{Zone: "work", Entries: []string{"proj"}})
	if err != nil {
		t.Fatalf("MigrateZoneDirs EXDEV fallback: %v", err)
	}
	if rep.Moved != 1 {
		t.Fatalf("expected Moved=1, got %+v", rep)
	}
	if _, err := os.Lstat(projSrc); !os.IsNotExist(err) {
		t.Fatalf("expected source %s removed after verified EXDEV move, err=%v", projSrc, err)
	}

	projDst := filepath.Join(ctx.HomeDir, "work", "code", "proj")
	if got, err := os.ReadFile(filepath.Join(projDst, "README.md")); err != nil || string(got) != "hello exdev\n" {
		t.Fatalf("README.md mismatch: got %q err=%v", string(got), err)
	}
	if fi, err := os.Stat(filepath.Join(projDst, "bin", "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("expected 0755 preserved on run.sh, got %v err=%v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(projDst, "readonly")); err != nil || fi.Mode().Perm() != 0o555 {
		t.Fatalf("expected 0555 preserved on readonly dir, got %v err=%v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(projDst, "readonly", "secret.txt")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 preserved on secret.txt, got %v err=%v", fi, err)
	}
	if lt, err := os.Readlink(filepath.Join(projDst, "readme-link")); err != nil || lt != "README.md" {
		t.Fatalf("expected relative symlink preserved, got %q err=%v", lt, err)
	}
	if lt, err := os.Readlink(filepath.Join(projDst, "dangling-link")); err != nil || lt != "does-not-exist" {
		t.Fatalf("expected dangling symlink preserved, got %q err=%v", lt, err)
	}
}
