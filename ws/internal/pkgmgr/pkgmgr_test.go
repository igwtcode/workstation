package pkgmgr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

type recordedCmd struct {
	Cmd      []string
	Env      map[string]string
	Dir      string
	NeedsTTY bool
	Streamed bool
}

type mockRunner struct {
	mu       sync.Mutex
	bins     map[string]string
	handlers map[string]func(cmd []string, env map[string]string) (string, string, error)
	calls    []recordedCmd
}

func newMockRunner(bins ...string) *mockRunner {
	m := &mockRunner{
		bins:     map[string]string{},
		handlers: map[string]func([]string, map[string]string) (string, string, error){},
	}
	for _, b := range bins {
		m.bins[b] = "/usr/bin/" + b
	}
	return m
}

func (m *mockRunner) on(key string, fn func(cmd []string, env map[string]string) (string, string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[key] = fn
}

func (m *mockRunner) Run(cmd []string, env map[string]string, dir string, needsTTY bool) (string, string, error) {
	return m.run(cmd, env, dir, needsTTY, false, nil, nil)
}

func (m *mockRunner) RunStreaming(cmd []string, env map[string]string, dir string, needsTTY bool, stdout, stderr io.Writer) (string, error) {
	_, capturedErr, err := m.run(cmd, env, dir, needsTTY, true, stdout, stderr)
	return capturedErr, err
}

func (m *mockRunner) run(cmd []string, env map[string]string, dir string, needsTTY, streamed bool, stdoutW, stderrW io.Writer) (string, string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, recordedCmd{
		Cmd:      slices.Clone(cmd),
		Env:      env,
		Dir:      dir,
		NeedsTTY: needsTTY,
		Streamed: streamed,
	})
	joined := strings.Join(cmd, " ")
	fn, ok := m.handlers[joined]
	if !ok && len(cmd) > 0 {
		fn, ok = m.handlers[cmd[0]]
	}
	m.mu.Unlock()
	if ok && fn != nil {
		outStr, errStr, err := fn(cmd, env)
		if streamed {
			if stdoutW != nil && outStr != "" {
				_, _ = io.WriteString(stdoutW, outStr)
			}
			if stderrW != nil && errStr != "" {
				_, _ = io.WriteString(stderrW, errStr)
			}
		}
		return outStr, errStr, err
	}
	return "", "", nil
}

func (m *mockRunner) LookPath(bin string) (string, error) {
	if p, ok := m.bins[bin]; ok {
		return p, nil
	}
	return "", errors.New("not found: " + bin)
}

func makeSyntheticRepo(t *testing.T) (repoDir, homeDir string) {
	t.Helper()
	base := t.TempDir()
	repoDir = filepath.Join(base, "repo")
	homeDir = filepath.Join(base, "home")

	for _, d := range []string{
		filepath.Join(repoDir, "pkg"),
		filepath.Join(repoDir, "links"),
		filepath.Join(repoDir, "themes"),
		filepath.Join(repoDir, "os", "mac"),
		filepath.Join(repoDir, "os", "arch", "desktop"),
		homeDir,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write := func(rel, content string) {
		p := filepath.Join(repoDir, rel)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("themes/registry", "")
	write("links/common.links", "")
	write("links/mac.links", "")
	write("links/linux.links", "")
	write("links/server.links", "")
	write("pkg/server.txt", "bat\nfd\nripgrep\n")
	write("pkg/mac-brew.txt", "bat\nfd\nripgrep\nyq\n")
	write("pkg/mac-cask.txt", "ghostty\nclaude-code@latest\ncodex\n1password\n")
	write("pkg/arch.txt", "bat\nfd\ngo-yq\nhyprland\n")
	write("pkg/arch-conflicts.txt", "# replaced\njack\nnoctalia-shell\nyq\n")
	write("os/mac/defaults.sh", "#!/usr/bin/env bash\nexit 0\n")
	write("os/arch/postinstall.sh", "#!/usr/bin/env bash\nexit 0\n")

	write("presets.toml", `
[preset.base]
description = "Base"
links = ["links/common.links"]
packages = ["pkg/server.txt"]
secrets = "none"

[preset.mac-workstation]
description = "macOS workstation"
include = ["base"]
links = ["links/mac.links"]
packages = ["pkg/mac-brew.txt", "pkg/mac-cask.txt"]
extra_tools = ["claude-code", "codex", "gh-dash"]
os_hooks = ["os/mac/defaults.sh"]
secrets = "1password"

[preset.arch-desktop]
description = "Arch desktop"
include = ["base"]
links = ["links/linux.links"]
packages = ["pkg/arch.txt"]
conflicts = ["pkg/arch-conflicts.txt"]
os_hooks = ["os/arch/postinstall.sh"]
secrets = "1password"

[preset.ubuntu-runner]
description = "Ubuntu runner"
include = ["base"]
links = ["links/server.links"]
packages = ["pkg/server.txt"]
extra_tools = ["yq", "1password-cli"]
secrets = "none"
`)

	write("tools.toml", `
[tool.claude-code]
bin = "claude"
updates = "manager"
install = [
  { via = "cask", name = "claude-code@latest", os = "darwin" },
  { via = "pacman", name = "claude-code" },
  { via = "script", url = "https://example.invalid/install.sh", tofu = true },
]

[tool.codex]
bin = "codex"
updates = "manager"
install = [
  { via = "cask", name = "codex", os = "darwin" },
  { via = "pacman", name = "openai-codex" },
  { via = "mise", ref = "npm:@openai/codex" },
]

[tool.self-tool]
bin = "self-tool"
updates = "self"
conflicts = ["cask:old-self-cask"]
install = [
  { via = "cask", name = "self-cask-tool", os = "darwin" },
  { via = "script", url = "https://example.invalid/self-tool.sh", tofu = true },
]

[tool.fd]
bin = "fd"
updates = "manager"
install = [
  { via = "apt", name = "fd-find", shim = "fdfind:fd" },
  { via = "dnf", name = "fd-find" },
  { via = "brew", name = "fd" },
  { via = "pacman", name = "fd" },
]

[tool.bat]
bin = "bat"
updates = "manager"
install = [
  { via = "apt", name = "bat", shim = "batcat:bat" },
  { via = "brew", name = "bat" },
  { via = "pacman", name = "bat" },
]

[tool.yq]
bin = "yq"
updates = "manager"
install = [
  { via = "pacman", name = "go-yq" },
  { via = "brew", name = "yq" },
  { via = "mise", ref = "github:mikefarah/yq" },
]

[tool.gh-dash]
bin = "gh-dash"
updates = "manager"
install = [
  { via = "gh-ext", name = "dlvhdr/gh-dash" },
]
`)
	return repoDir, homeDir
}

func makeCtx(repoDir, homeDir, goos, distro string) *app.Context {
	return &app.Context{
		RepoRoot:  repoDir,
		HomeDir:   homeDir,
		WSHome:    filepath.Join(homeDir, ".ws"),
		ConfigDir: filepath.Join(homeDir, ".config"),
		DataDir:   filepath.Join(homeDir, ".local", "share"),
		StateDir:  filepath.Join(homeDir, ".local", "state"),
		CacheDir:  filepath.Join(homeDir, ".cache"),
		Host: app.HostInfo{
			OS:     goos,
			Distro: distro,
			Arch:   "arm64",
		},
		LocalEnv: map[string]string{},
	}
}

func TestAllNineProvidersBasicContracts(t *testing.T) {
	r := newMockRunner("brew", "pacman", "paru", "apt-get", "dnf", "mise", "gh", "bash")
	ctx := makeCtx("", t.TempDir(), "linux", "arch")
	provs := AllProviders(ctx, r, nil)

	expected := []string{"brew", "cask", "pacman", "paru", "apt", "dnf", "mise", "gh-ext", "script"}
	for _, name := range expected {
		p, ok := provs[name]
		if !ok {
			t.Fatalf("missing provider %q", name)
		}
		if p.Name() != name {
			t.Errorf("provider name = %q, want %q", p.Name(), name)
		}
		if !p.Available() {
			t.Errorf("provider %q should be available", name)
		}
		act := p.InstallAction(config.ResolvedTool{
			ID:      "demo",
			Bin:     "demo",
			Updates: "manager",
			Spec: config.InstallSpec{
				Via:  name,
				Name: "demo",
				Ref:  "github:example/demo",
				URL:  "https://example.invalid/demo.sh",
				TOFU: true,
			},
		})
		if act.Kind != engine.ActionInstall {
			t.Errorf("provider %q install kind = %q, want install", name, act.Kind)
		}
	}
}

func TestSelfUpdaterExcludedEvenUnderAllGreedy(t *testing.T) {
	tools := []config.ResolvedTool{
		{
			ID:        "claude-code",
			Bin:       "claude",
			Updates:   "self",
			Conflicts: []string{"cask:claude-code", "cask:claude-code@latest"},
			Spec:      config.InstallSpec{Via: "script", URL: "https://example.invalid/install.sh", TOFU: true},
		},
		{
			ID:      "codex",
			Bin:     "codex",
			Updates: "self",
			Spec:    config.InstallSpec{Via: "cask", Name: "codex"},
		},
		{
			ID:      "self-cli",
			Bin:     "self-cli",
			Updates: "self",
			Spec:    config.InstallSpec{Via: "brew", Name: "self-cli"},
		},
		{
			ID:      "arch-self",
			Bin:     "arch-self",
			Updates: "self",
			Spec:    config.InstallSpec{Via: "pacman", Name: "arch-self"},
		},
		{
			ID:      "apt-self",
			Bin:     "apt-self",
			Updates: "self",
			Spec:    config.InstallSpec{Via: "apt", Name: "apt-self"},
		},
	}

	r := newMockRunner("brew", "pacman", "paru", "apt-get", "apt")
	r.on("brew outdated --cask --greedy --quiet", func([]string, map[string]string) (string, string, error) {
		return "ghostty\ncodex\nclaude-code@latest\nclaude-code\nzed\n", "", nil
	})
	r.on("brew outdated --formula --quiet", func([]string, map[string]string) (string, string, error) {
		return "ripgrep\nself-cli\nbat\n", "", nil
	})
	r.on("apt list --upgradable", func([]string, map[string]string) (string, string, error) {
		return "Listing...\nripgrep/noble 14.1.1 amd64 [upgradable from: 14.1.0]\napt-self/noble 2.0 amd64 [upgradable from: 1.0]\n", "", nil
	})

	ctx := makeCtx("", t.TempDir(), "darwin", "macos")
	provs := AllProviders(ctx, r, tools)

	caskAct := provs["cask"].UpgradeAction(true)
	if caskAct == nil {
		t.Fatal("expected cask upgrade action for ghostty and zed")
	}
	joined := strings.Join(caskAct.Command, " ")
	for _, forbidden := range []string{"codex", "claude-code", "claude-code@latest"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("cask upgrade command %q must not contain %q", joined, forbidden)
		}
	}
	if !slices.Contains(caskAct.Command, "--greedy") {
		t.Errorf("cask upgrade command %q must preserve --greedy when all=true", joined)
	}
	if !slices.Contains(caskAct.Command, "ghostty") || !slices.Contains(caskAct.Command, "zed") {
		t.Errorf("expected ghostty and zed in cask upgrade command, got %v", caskAct.Command)
	}

	r.on("brew outdated --cask --quiet", func([]string, map[string]string) (string, string, error) {
		return "ghostty\ncodex\n", "", nil
	})
	caskNoGreedy := provs["cask"].UpgradeAction(false)
	if caskNoGreedy == nil || slices.Contains(caskNoGreedy.Command, "--greedy") {
		t.Errorf("expected no --greedy when all=false, got %+v", caskNoGreedy)
	}

	brewAct := provs["brew"].UpgradeAction(true)
	if brewAct == nil {
		t.Fatal("expected brew upgrade action")
	}
	if slices.Contains(brewAct.Command, "self-cli") {
		t.Errorf("brew upgrade command must exclude self-cli: %v", brewAct.Command)
	}
	if !slices.Contains(brewAct.Command, "ripgrep") {
		t.Errorf("expected ripgrep in brew upgrade command: %v", brewAct.Command)
	}

	pacAct := provs["pacman"].UpgradeAction(true)
	if pacAct == nil || !strings.Contains(strings.Join(pacAct.Command, " "), "--ignore arch-self") {
		t.Errorf("pacman upgrade must ignore arch-self, got %+v", pacAct)
	}
	paruAct := provs["paru"].UpgradeAction(true)
	if paruAct == nil || !strings.Contains(strings.Join(paruAct.Command, " "), "--ignore arch-self") {
		t.Errorf("paru upgrade must ignore arch-self, got %+v", paruAct)
	}

	aptAct := provs["apt"].UpgradeAction(true)
	if aptAct == nil {
		t.Fatal("expected apt upgrade action")
	}
	if slices.Contains(aptAct.Command, "apt-self") {
		t.Errorf("apt upgrade must exclude apt-self: %v", aptAct.Command)
	}
	if aptAct.Metadata["env.DEBIAN_FRONTEND"] != "noninteractive" {
		t.Errorf("apt upgrade missing DEBIAN_FRONTEND=noninteractive")
	}

	r.on("brew outdated --cask --greedy --quiet", func([]string, map[string]string) (string, string, error) {
		return "codex\nclaude-code@latest\n", "", nil
	})
	if act := provs["cask"].UpgradeAction(true); act != nil {
		t.Errorf("expected nil upgrade action when only self-updated casks are outdated, got %+v", act)
	}
}

func TestScriptSHA256VerificationAndTOFUNonInteractiveBlock(t *testing.T) {
	body := "#!/usr/bin/env bash\necho ok\n"
	sumBytes := sha256.Sum256([]byte(body))
	goodSum := hex.EncodeToString(sumBytes[:])
	badSum := strings.Repeat("0", 64)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newMockRunner("bash")
	ctx := makeCtx("", t.TempDir(), "linux", "ubuntu")
	ctx.Allow.Script = true
	spec := config.InstallSpec{Via: "script", URL: srv.URL + "/install.sh", SHA256: goodSum}
	gotSum, skipped, _, err := verifyAndRunScriptWithPrior(ctx, spec, "demo", "", srv.Client(), r)
	if err != nil || skipped {
		t.Fatalf("expected pinned script to succeed, got skipped=%v err=%v", skipped, err)
	}
	if gotSum != goodSum {
		t.Errorf("sha256 = %q, want %q", gotSum, goodSum)
	}
	if len(r.calls) != 1 || r.calls[0].Cmd[0] != "bash" || !strings.HasSuffix(r.calls[0].Cmd[1], ".sh") {
		t.Errorf("expected single bash <tmpfile> call, got %+v", r.calls)
	}
	if _, statErr := os.Stat(r.calls[0].Cmd[1]); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("expected temp script file to be removed after execution")
	}

	rIntBlocked := newMockRunner("bash")
	ctxIntBlocked := makeCtx("", t.TempDir(), "linux", "ubuntu")
	ctxIntBlocked.NonInteractive = false
	ctxIntBlocked.Allow.Script = false
	if _, skippedInt, warnInt, errInt := verifyAndRunScriptWithPrior(ctxIntBlocked, spec, "demo", "", srv.Client(), rIntBlocked); errInt != nil || !skippedInt || !strings.Contains(warnInt, "--allow script") {
		t.Fatalf("expected interactive pinned script without --allow script to be skipped, got skipped=%v warn=%q err=%v", skippedInt, warnInt, errInt)
	}
	if len(rIntBlocked.calls) != 0 {
		t.Errorf("interactive blocked script must never execute, got %+v", rIntBlocked.calls)
	}
	specTOFUInt := config.InstallSpec{Via: "script", URL: srv.URL + "/install.sh", TOFU: true}
	if _, skippedInt, warnInt, errInt := verifyAndRunScriptWithPrior(ctxIntBlocked, specTOFUInt, "claude-code", "", srv.Client(), rIntBlocked); errInt != nil || !skippedInt || !strings.Contains(warnInt, "--allow script") {
		t.Fatalf("expected interactive TOFU script without --allow script to be skipped, got skipped=%v warn=%q err=%v", skippedInt, warnInt, errInt)
	}
	if len(rIntBlocked.calls) != 0 {
		t.Errorf("interactive blocked TOFU script must never execute, got %+v", rIntBlocked.calls)
	}

	rBad := newMockRunner("bash")
	specBad := config.InstallSpec{Via: "script", URL: srv.URL + "/install.sh", SHA256: badSum}
	_, _, _, err = verifyAndRunScriptWithPrior(ctx, specBad, "demo", "", srv.Client(), rBad)
	if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected SHA256 mismatch error, got %v", err)
	}
	if len(rBad.calls) != 0 {
		t.Errorf("mismatched script must never execute, got calls: %+v", rBad.calls)
	}

	rBlocked := newMockRunner("bash")
	ctxNonInt := makeCtx("", t.TempDir(), "linux", "ubuntu")
	ctxNonInt.NonInteractive = true
	specTOFU := config.InstallSpec{Via: "script", URL: srv.URL + "/install.sh", TOFU: true}
	_, skipped, warn, err := verifyAndRunScriptWithPrior(ctxNonInt, specTOFU, "claude-code", "", srv.Client(), rBlocked)
	if err != nil {
		t.Fatalf("unexpected error on TOFU non-interactive skip: %v", err)
	}
	if !skipped || !strings.Contains(warn, "--allow script") {
		t.Fatalf("expected skip with --allow script warning, got skipped=%v warn=%q", skipped, warn)
	}
	if len(rBlocked.calls) != 0 {
		t.Errorf("blocked TOFU script must not execute, got %+v", rBlocked.calls)
	}

	rAllowed := newMockRunner("bash")
	ctxNonInt.Allow.Script = true
	gotSum, skipped, _, err = verifyAndRunScriptWithPrior(ctxNonInt, specTOFU, "claude-code", "", srv.Client(), rAllowed)
	if err != nil || skipped || gotSum != goodSum {
		t.Fatalf("expected TOFU script with --allow script to run, got sum=%q skipped=%v err=%v", gotSum, skipped, err)
	}
	if len(rAllowed.calls) != 1 {
		t.Errorf("expected 1 execution, got %d", len(rAllowed.calls))
	}

	p := AllProviders(ctx, r, nil)["script"]
	act := p.InstallAction(config.ResolvedTool{ID: "claude-code", Bin: "claude", Spec: specTOFU})
	if act.RequiresAllow != "script" {
		t.Errorf("script InstallAction RequiresAllow = %q, want script", act.RequiresAllow)
	}
}

func TestMiseRejectsDeprecatedUbi(t *testing.T) {
	if err := ValidateMiseRef("github:mikefarah/yq"); err != nil {
		t.Errorf("github: ref should be valid: %v", err)
	}
	if err := ValidateMiseRef("aqua:cli/cli"); err != nil {
		t.Errorf("aqua: ref should be valid: %v", err)
	}
	if err := ValidateMiseRef("npm:@openai/codex"); err != nil {
		t.Errorf("npm: ref should be valid: %v", err)
	}
	if err := ValidateMiseRef("ubi:mikefarah/yq"); err == nil {
		t.Errorf("expected deprecated ubi: ref to be rejected")
	}
}

func TestPlanBootstrapConflictRemovalAndStranglerHooks(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	r := newMockRunner("brew", "gh", "bash")

	r.on("brew", func(cmd []string, _ map[string]string) (string, string, error) {
		if len(cmd) >= 2 && cmd[1] == "list" {
			return "", "", nil
		}
		return "", "", nil
	})
	r.on("gh extension list", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})

	macPlan, err := PlanBootstrapWithRunner(ctx, "mac-workstation", r)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac-workstation) failed: %v", err)
	}
	var sawClaudeCask, sawCodexCask, sawBrewUpdate bool
	hookIdx := -1
	for i, a := range macPlan.Actions {
		if a.ID == "brew:update" && slices.Equal(a.Command, []string{"brew", "update"}) {
			sawBrewUpdate = true
		}
		if a.ID == "install:cask:batch" && a.NeedsTTY {
			if slices.Contains(a.Command, "claude-code@latest") {
				sawClaudeCask = true
			}
			if slices.Contains(a.Command, "codex") {
				sawCodexCask = true
			}
		}
		if strings.HasPrefix(a.ID, "conflict:remove:") {
			t.Errorf("unexpected conflict removal for Homebrew mac-workstation: %+v", a)
		}
		if a.Kind == engine.ActionOSExec && a.Source == "os/mac/defaults.sh" {
			hookIdx = i
			if !a.NeedsTTY || a.RequiresAllow != "sudo" {
				t.Errorf("OS hook must have NeedsTTY=true and RequiresAllow=sudo, got %+v", a)
			}
		}
	}
	if !sawBrewUpdate || !sawClaudeCask || !sawCodexCask {
		t.Fatalf("expected brew update (%v) and batched Homebrew cask installs for claude-code@latest (%v) and codex (%v)", sawBrewUpdate, sawClaudeCask, sawCodexCask)
	}
	if hookIdx < 0 {
		t.Errorf("expected os/mac/defaults.sh OS hook in mac-workstation plan")
	}

	presetsRaw, _ := os.ReadFile(filepath.Join(repoDir, "presets.toml"))
	extraPreset := string(presetsRaw) + `
[preset.conflict-demo]
description = "Conflict test"
include = ["base"]
extra_tools = ["self-tool"]
secrets = "none"
`
	if err := os.WriteFile(filepath.Join(repoDir, "presets.toml"), []byte(extraPreset), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := newMockRunner("brew", "bash")
	r2.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/cask/old-self-cask\n", "", nil
	})
	plan2, err := PlanBootstrapWithRunner(ctx, "conflict-demo", r2)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(conflict-demo): %v", err)
	}
	removeIdx := -1
	installIdx := -1
	for i, a := range plan2.Actions {
		if a.Kind == engine.ActionRemove && a.Target == "cask:old-self-cask" {
			removeIdx = i
		}
		if a.Kind == engine.ActionInstall && strings.Contains(a.ID, "self-tool") {
			installIdx = i
		}
	}
	if removeIdx < 0 || installIdx < 0 || removeIdx >= installIdx {
		t.Fatalf("expected conflict removal (%d) before self-tool install (%d)", removeIdx, installIdx)
	}

	ctxSkip := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctxSkip.Allow.Script = true
	resSkip, err := ExecBootstrap(ctxSkip, plan2, r2)
	if err != nil {
		t.Fatalf("ExecBootstrap conflict skip failed: %v", err)
	}
	for _, changed := range resSkip.Changed {
		if strings.Contains(changed, "self-tool") {
			t.Errorf("self-tool must not install when conflict removal was skipped, changed=%v", resSkip.Changed)
		}
	}
}

func TestPlanAndExecBootstrapUbuntuRunnerShimsAndNoSecrets(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctx.Host.Headless = true
	ctx.Allow.Sudo = true

	fakeBinDir := filepath.Join(t.TempDir(), "sysbin")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fdfindBin := filepath.Join(fakeBinDir, "fdfind")
	batcatBin := filepath.Join(fakeBinDir, "batcat")
	_ = os.WriteFile(fdfindBin, []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(batcatBin, []byte("#!/bin/sh\n"), 0o755)

	r := newMockRunner("apt-get", "dpkg-query", "mise")
	r.bins["fdfind"] = fdfindBin
	r.bins["batcat"] = batcatBin
	r.on("dpkg-query", func([]string, map[string]string) (string, string, error) {
		return "deinstall ok config-files", "", nil
	})
	r.on("mise", func(cmd []string, _ map[string]string) (string, string, error) {
		if len(cmd) >= 2 && cmd[1] == "which" {
			return "", "", errors.New("not installed")
		}
		return "", "", nil
	})

	plan, err := PlanBootstrapWithRunner(ctx, "ubuntu-runner", r)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(ubuntu-runner) failed: %v", err)
	}

	hasSecretSkip := false
	for _, s := range plan.Skipped {
		if strings.Contains(s, "1password") && strings.Contains(s, "secrets = none") {
			hasSecretSkip = true
		}
	}
	if !hasSecretSkip {
		t.Errorf("expected 1password skip note in plan.Skipped, got %v", plan.Skipped)
	}

	var hasAptFD, hasMiseYQ, hasFDShim, hasBatShim bool
	for _, a := range plan.Actions {
		if a.ID == "install:apt:fd" && a.Metadata["env.DEBIAN_FRONTEND"] == "noninteractive" {
			hasAptFD = true
		}
		if a.ID == "install:mise:yq" && slices.Equal(a.Command, []string{"mise", "use", "-g", "github:mikefarah/yq"}) {
			hasMiseYQ = true
		}
		if a.ID == "shim:fd:fd" {
			hasFDShim = true
		}
		if a.ID == "shim:bat:bat" {
			hasBatShim = true
		}
		if strings.Contains(a.ID, "1password") {
			t.Errorf("1password must not be scheduled for ubuntu-runner: %+v", a)
		}
	}
	if !hasAptFD || !hasMiseYQ || !hasFDShim || !hasBatShim {
		t.Fatalf("missing expected ubuntu-runner steps: aptFD=%v miseYQ=%v fdShim=%v batShim=%v", hasAptFD, hasMiseYQ, hasFDShim, hasBatShim)
	}

	res, err := ExecBootstrap(ctx, plan, r)
	if err != nil {
		t.Fatalf("ExecBootstrap failed: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected applied actions > 0")
	}

	fdLink, err := os.Readlink(filepath.Join(homeDir, ".local", "bin", "fd"))
	if err != nil || fdLink != fdfindBin {
		t.Errorf("expected fd shim -> %s, got %q (err=%v)", fdfindBin, fdLink, err)
	}
	batLink, err := os.Readlink(filepath.Join(homeDir, ".local", "bin", "bat"))
	if err != nil || batLink != batcatBin {
		t.Errorf("expected bat shim -> %s, got %q (err=%v)", batcatBin, batLink, err)
	}
}

func TestBuildUpdateDigestDirtyRepoRiskAndSelfExclusion(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "cachyos")
	ctx.PresetOverride = "arch-desktop"

	r := newMockRunner("git", "paru", "pacman", "snapper")
	r.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return " M presets.toml\n", "", nil
	})
	r.on("pacman -Qm", func([]string, map[string]string) (string, string, error) {
		return "herdr-bin 0.1.0-1\n", "", nil
	})
	r.on("paru -Qu", func([]string, map[string]string) (string, string, error) {
		return strings.Join([]string{
			"hyprland 0.54.0-1 -> 0.55.0-1",
			"go 1.26.0-1 -> 1.27.4-1",
			"herdr-bin 0.1.0-1 -> 0.2.0-1",
			"ripgrep 14.1.0-1 -> 14.1.1-1",
		}, "\n") + "\n", "", nil
	})

	digest, err := BuildUpdateDigestWithRunner(ctx, true, r)
	if err != nil {
		t.Fatalf("BuildUpdateDigestWithRunner failed: %v", err)
	}
	if !digest.RepoDirty {
		t.Errorf("expected RepoDirty=true")
	}
	if strings.Contains(strings.ToLower(strings.Join(digest.Warnings, "\n")), "uncommitted") {
		t.Errorf("expected dirty repo status not duplicated into digest.Warnings, got %v", digest.Warnings)
	}
	if digest.SnapshotHint == "" {
		t.Errorf("expected non-empty snapshot hint on cachyos with snapper")
	}

	for _, c := range r.calls {
		if len(c.Cmd) > 0 && c.Cmd[0] == "git" && slices.Contains(c.Cmd, "pull") {
			t.Fatalf("BuildUpdateDigest must NEVER run git pull, saw: %v", c.Cmd)
		}
	}

	riskByName := map[string]engine.RiskLevel{}
	for _, it := range digest.Items {
		riskByName[it.Name] = it.Risk
	}
	for _, high := range []string{"hyprland", "go", "herdr-bin"} {
		if riskByName[high] != engine.RiskDangerous {
			t.Errorf("expected %q to be high risk (%s), got %q", high, engine.RiskDangerous, riskByName[high])
		}
	}
	if riskByName["ripgrep"] != engine.RiskMedium {
		t.Errorf("expected ripgrep to be medium risk, got %q", riskByName["ripgrep"])
	}
}

func TestBuildUpdateDigestBrewSkipsSelfUpdaters(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "mac-workstation"

	r := newMockRunner("git", "brew")
	r.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	brewQueries := 0
	r.on("brew outdated --json=v2 --greedy", func([]string, map[string]string) (string, string, error) {
		brewQueries++
		return `{
			"formulae": [
				{"name": "ripgrep", "installed_versions": ["14.1.0", "14.1.0_1"], "current_version": "14.1.1", "pinned": false},
				{"name": "openssl@3", "installed_versions": ["3.0.0"], "current_version": "3.1.0", "pinned": true}
			],
			"casks": [
				{"name": "self-cask-tool", "installed_versions": ["1.0.0"], "current_version": "1.1.0"},
				{"name": "ghostty", "installed_versions": ["1.0.0"], "current_version": "1.1.0"},
				{"name": "codex", "installed_versions": ["0.9.0"], "current_version": "0.10.0"}
			]
		}`, "", nil
	})

	digest, err := BuildUpdateDigestWithRunner(ctx, true, r)
	if err != nil {
		t.Fatalf("BuildUpdateDigestWithRunner failed: %v", err)
	}
	if brewQueries != 1 {
		t.Fatalf("expected exactly 1 brew outdated query (no duplicate polling), got %d", brewQueries)
	}
	for _, it := range digest.Items {
		if it.Name == "self-cask-tool" || it.Name == "openssl@3" {
			t.Errorf("excluded/pinned tool %q must not appear in digest items", it.Name)
		}
		if it.Name == "ripgrep" && it.Current != "14.1.0, 14.1.0_1" {
			t.Errorf("expected multi-version current, got %q", it.Current)
		}
	}
	if digest.Plan == nil || len(digest.Plan.Actions) < 3 {
		t.Fatalf("expected brew update + formula + cask actions, got %+v", digest.Plan)
	}
	if digest.Plan.Actions[0].ID != "update:brew-index" {
		t.Errorf("expected brew update index action first, got %+v", digest.Plan.Actions[0])
	}
	for _, act := range digest.Plan.Actions {
		cmdStr := strings.Join(act.Command, " ")
		if strings.Contains(cmdStr, "self-cask-tool") || strings.Contains(cmdStr, "openssl@3") {
			t.Errorf("plan upgrade command must never include self-updaters or pinned pkgs: %q", cmdStr)
		}
	}
}

func TestScheduleRenderInstallStatusRemoveDarwinAndLinux(t *testing.T) {

	macCtx := makeCtx("", t.TempDir(), "darwin", "macos")
	macSpec, err := RenderScheduleFiles(macCtx, 12)
	if err != nil {
		t.Fatalf("RenderScheduleFiles(darwin) failed: %v", err)
	}
	if macSpec.Backend != "launchd" || len(macSpec.Files) != 1 {
		t.Fatalf("unexpected mac spec: %+v", macSpec)
	}
	if !strings.HasSuffix(macSpec.Files[0].Path, filepath.Join("Library", "LaunchAgents", "dev.workstation.ws-digest.plist")) {
		t.Errorf("unexpected plist path: %s", macSpec.Files[0].Path)
	}
	plist := macSpec.Files[0].Content
	if !strings.Contains(plist, "<string>--digest</string>") || !strings.Contains(plist, "<string>--json</string>") {
		t.Errorf("plist must run ws update --digest --json: %s", plist)
	}
	if strings.Contains(plist, "git pull") || strings.Contains(plist, "apply") {
		t.Errorf("plist must NEVER run git pull or apply: %s", plist)
	}

	rMac := newMockRunner("launchctl")
	st, err := InstallScheduleWithOptions(macCtx, ScheduleOptions{IntervalHours: 12}, rMac)
	if err != nil || !st.Installed || !st.Active {
		t.Fatalf("InstallScheduleWithOptions(darwin) failed: st=%+v err=%v", st, err)
	}
	gotSt, err := GetScheduleStatus(macCtx, rMac)
	if err != nil || !gotSt.Installed || !gotSt.Active || gotSt.IntervalHours != 12 {
		t.Fatalf("GetScheduleStatus(darwin) unexpected: %+v err=%v", gotSt, err)
	}
	if err := RemoveSchedule(macCtx, rMac); err != nil {
		t.Fatalf("RemoveSchedule(darwin) failed: %v", err)
	}
	afterSt, _ := GetScheduleStatus(macCtx, rMac)
	if afterSt.Installed {
		t.Errorf("expected schedule uninstalled after RemoveSchedule")
	}

	linCtx := makeCtx("", t.TempDir(), "linux", "arch")
	linSpec, err := RenderScheduleFiles(linCtx, 6)
	if err != nil {
		t.Fatalf("RenderScheduleFiles(linux) failed: %v", err)
	}
	if linSpec.Backend != "systemd" || len(linSpec.Files) != 2 {
		t.Fatalf("unexpected linux spec: %+v", linSpec)
	}
	svc := linSpec.Files[0].Content
	if !strings.Contains(svc, "ws update --digest --json") {
		t.Errorf("systemd service must run ws update --digest --json: %s", svc)
	}
	if strings.Contains(svc, "git pull") || strings.Contains(svc, "apply") {
		t.Errorf("systemd service must NEVER run git pull or apply: %s", svc)
	}

	rLin := newMockRunner("systemctl")
	rLin.on("systemctl --user is-active ws-digest.timer", func([]string, map[string]string) (string, string, error) {
		return "active\n", "", nil
	})
	stLin, err := InstallScheduleWithOptions(linCtx, ScheduleOptions{IntervalHours: 6}, rLin)
	if err != nil || !stLin.Installed || !stLin.Active {
		t.Fatalf("InstallScheduleWithOptions(linux) failed: st=%+v err=%v", stLin, err)
	}
	gotLin, err := GetScheduleStatus(linCtx, rLin)
	if err != nil || !gotLin.Installed || !gotLin.Active || gotLin.IntervalHours != 6 {
		t.Fatalf("GetScheduleStatus(linux) unexpected: %+v err=%v", gotLin, err)
	}
	if err := RemoveSchedule(linCtx, rLin); err != nil {
		t.Fatalf("RemoveSchedule(linux) failed: %v", err)
	}
	afterLin, _ := GetScheduleStatus(linCtx, rLin)
	if afterLin.Installed {
		t.Errorf("expected linux units removed after RemoveSchedule")
	}
}

func TestConsentGatesFailClosedInteractiveAndNonInteractive(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	plan := &engine.Plan{
		Verb:  "bootstrap",
		Title: "Consent gate test",
		Actions: []engine.Action{
			{
				ID:            "install:apt:fd",
				Kind:          engine.ActionInstall,
				Target:        "fd-find",
				RequiresAllow: "sudo",
				Command:       []string{"sudo", "-E", "apt-get", "install", "-y", "fd-find"},
			},
			{
				ID:            "conflict:remove:cask:claude-code",
				Kind:          engine.ActionRemove,
				Target:        "cask:claude-code",
				RequiresAllow: "remove",
				Command:       []string{"brew", "uninstall", "--cask", "claude-code"},
			},
		},
	}

	for _, nonInt := range []bool{false, true} {
		ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
		ctx.NonInteractive = nonInt
		r := newMockRunner("sudo", "brew")
		res, err := ExecBootstrap(ctx, plan, r)
		if err != nil {
			t.Fatalf("ExecBootstrap (nonInt=%v) unexpected err: %v", nonInt, err)
		}
		if res.Applied != 0 || res.Skipped != 2 || len(r.calls) != 0 {
			t.Fatalf("nonInt=%v: expected 0 applied / 2 skipped / 0 calls, got res=%+v calls=%+v", nonInt, res, r.calls)
		}
	}

	ctxOK := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctxOK.Allow.Sudo = true
	ctxOK.Allow.Remove = true
	rOK := newMockRunner("sudo", "brew")
	resOK, err := ExecBootstrap(ctxOK, plan, rOK)
	if err != nil || resOK.Applied != 2 || len(rOK.calls) != 2 {
		t.Fatalf("expected 2 applied with allows, got res=%+v calls=%+v err=%v", resOK, rOK.calls, err)
	}
}

func TestExecBootstrapRejectsForgedPlans(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.Allow, _ = app.ParseAllowStrict("all")

	if _, err := ExecBootstrap(ctx, &engine.Plan{Verb: "apply", Title: "wrong"}, newMockRunner()); err == nil {
		t.Errorf("expected ExecBootstrap to reject Verb=apply")
	}
	if _, err := ExecBootstrap(ctx, &engine.Plan{Verb: "clean", Title: "wrong"}, newMockRunner()); err == nil {
		t.Errorf("expected ExecBootstrap to reject Verb=clean")
	}

	forgedActions := []engine.Action{
		{ID: "bad:bin", Kind: engine.ActionInstall, Command: []string{"curl", "https://example.invalid"}},
		{ID: "bad:sudo", Kind: engine.ActionInstall, Command: []string{"sudo", "rm", "-rf", "/"}},
		{ID: "bad:bash-install", Kind: engine.ActionInstall, Command: []string{"bash", "-c", "echo pwn"}},
		{ID: "bad:os-outside", Kind: engine.ActionOSExec, Target: "/tmp/evil.sh", Command: []string{"bash", "/tmp/evil.sh"}},
		{ID: "bad:os-traversal", Kind: engine.ActionOSExec, Command: []string{"bash", filepath.Join(repoDir, "os", "..", "presets.toml")}},
		{ID: "bad:os-dashc", Kind: engine.ActionOSExec, Command: []string{"bash", "-c", "echo pwn"}},
	}
	for _, act := range forgedActions {
		r := newMockRunner("curl", "sudo", "bash")
		if _, err := ExecBootstrap(ctx, &engine.Plan{Verb: "bootstrap", Actions: []engine.Action{act}}, r); err == nil {
			t.Errorf("expected forged action %s to be rejected", act.ID)
		}
		if len(r.calls) != 0 {
			t.Errorf("forged action %s executed commands: %+v", act.ID, r.calls)
		}
	}

	hookPath := filepath.Join(repoDir, "os", "mac", "defaults.sh")
	rGood := newMockRunner("bash")
	res, err := ExecBootstrap(ctx, &engine.Plan{
		Verb: "bootstrap",
		Actions: []engine.Action{
			{
				ID:            "os_exec:os/mac/defaults.sh",
				Kind:          engine.ActionOSExec,
				Target:        hookPath,
				RequiresAllow: "sudo",
				Command:       []string{"bash", hookPath},
			},
		},
	}, rGood)
	if err != nil || res.Applied != 1 {
		t.Fatalf("expected valid os/ hook to succeed, got res=%+v err=%v", res, err)
	}
}

func TestTOFULedgerMismatchAndHTTPSRedirectRefusal(t *testing.T) {
	currentBody := "#!/usr/bin/env bash\necho v1\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-http" {
			http.Redirect(w, r, "http://example.invalid/evil.sh", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(currentBody))
	}))
	defer srv.Close()

	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctx.Allow.Script = true

	plan := &engine.Plan{
		Verb:  "bootstrap",
		Title: "TOFU test",
		Actions: []engine.Action{
			{
				ID:            "install:script:claude-code",
				Kind:          engine.ActionInstall,
				Target:        "claude-code",
				RequiresAllow: "script",
				Metadata: map[string]string{
					"provider": "script",
					"tool":     "claude-code",
					"url":      srv.URL + "/install.sh",
					"tofu":     "true",
					"updates":  "self",
				},
			},
		},
	}

	r1 := newMockRunner("bash")
	res1, err := ExecBootstrapWithHTTP(ctx, plan, r1, srv.Client())
	if err != nil || res1.Applied != 1 {
		t.Fatalf("initial TOFU install failed: res=%+v err=%v", res1, err)
	}

	currentBody = "#!/usr/bin/env bash\necho tampered-v2\n"
	r2 := newMockRunner("bash")
	_, err = ExecBootstrapWithHTTP(ctx, plan, r2, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "TOFU SHA256 mismatch") {
		t.Fatalf("expected TOFU SHA256 mismatch error, got %v", err)
	}
	if len(r2.calls) != 0 {
		t.Fatalf("tampered TOFU script must never execute, got calls: %+v", r2.calls)
	}

	if _, _, err := DownloadAndVerifyScript(srv.URL+"/redirect-http", "", srv.Client()); err == nil || !strings.Contains(err.Error(), "non-https") {
		t.Fatalf("expected redirect to non-https URL to be refused, got %v", err)
	}
}

func TestMiseWorkDirJSONParsingAndCrossProviderSelfExclusion(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "mac-workstation"

	miseOnlyTools := []config.ResolvedTool{
		{
			ID:      "codex",
			Bin:     "codex",
			Updates: "self",
			Spec:    config.InstallSpec{Via: "mise", Ref: "npm:@openai/codex"},
		},
	}
	selfMap := SelfUpdatedByProvider(miseOnlyTools)
	if !slices.Contains(selfMap["cask"], "codex") {
		t.Fatalf("expected codex excluded in cask provider regardless of resolved provider, got %v", selfMap["cask"])
	}

	rErr := newMockRunner("brew")
	rErr.on("brew outdated --cask --quiet", func([]string, map[string]string) (string, string, error) {
		return "", "network error", errors.New("exit 1")
	})
	rErr.on("brew outdated --cask --greedy --quiet", func([]string, map[string]string) (string, string, error) {
		return "", "network error", errors.New("exit 1")
	})
	provsErr := AllProviders(ctx, rErr, miseOnlyTools)
	if act := provsErr["cask"].UpgradeAction(false); act != nil {
		t.Fatalf("expected nil on brew outdated error with self-updaters (all=false), got %+v", act)
	}
	if act := provsErr["cask"].UpgradeAction(true); act != nil {
		t.Fatalf("expected nil on brew outdated error with self-updaters (all=true), got %+v", act)
	}

	rMise := newMockRunner("mise")
	rMise.on("mise outdated --json", func([]string, map[string]string) (string, string, error) {
		return `{"yq":{"name":"yq","requested":"latest","current":"4.44.0","latest":"4.45.0"},"self-tool":{"name":"self-tool","requested":"latest","current":"0.1.0","latest":"0.2.0"}}`, "", nil
	})
	digest, err := BuildUpdateDigestWithRunner(ctx, false, rMise)
	if err != nil {
		t.Fatalf("BuildUpdateDigestWithRunner(mise json): %v", err)
	}
	if digest.Plan == nil || digest.Plan.Verb != "update" || digest.Plan.Host == "" {
		t.Fatalf("expected digest plan with Verb=update and Host, got %+v", digest.Plan)
	}
	foundMiseCall := false
	for _, c := range rMise.calls {
		if len(c.Cmd) >= 2 && c.Cmd[0] == "mise" && c.Cmd[1] == "outdated" {
			foundMiseCall = true
			if c.Dir != repoDir {
				t.Errorf("mise outdated workDir = %q, want %q", c.Dir, repoDir)
			}
		}
	}
	if !foundMiseCall {
		t.Fatalf("expected mise outdated call")
	}
	if len(digest.Items) != 1 || digest.Items[0].Name != "yq" {
		t.Fatalf("expected only yq after excluding self-tool, got %+v", digest.Items)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "tools.toml"), []byte("[tool.broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildUpdateDigestWithRunner(ctx, false, newMockRunner("brew")); err == nil {
		t.Fatalf("expected BuildUpdateDigestWithRunner to propagate tools.toml parse error")
	}
}

func TestScheduleAtomicOutputAndDefaultInterval(t *testing.T) {
	macCtx := makeCtx("", t.TempDir(), "darwin", "macos")
	macSpec, err := RenderScheduleFiles(macCtx, 0)
	if err != nil {
		t.Fatalf("RenderScheduleFiles(darwin, 0): %v", err)
	}
	if macSpec.IntervalHours != 12 {
		t.Fatalf("default darwin interval = %d, want 12", macSpec.IntervalHours)
	}
	plist := macSpec.Files[0].Content
	wantOut := macCtx.WSDigestPath()
	if !strings.Contains(plist, "<string>--output</string>") || !strings.Contains(plist, xmlEscape(wantOut)) {
		t.Errorf("darwin plist missing --output %s:\n%s", wantOut, plist)
	}
	if strings.Contains(plist, "<key>StandardOutPath</key>") {
		t.Errorf("darwin plist must not redirect StandardOutPath to digest.json:\n%s", plist)
	}

	linCtx := makeCtx("", t.TempDir(), "linux", "arch")
	linSpec, err := RenderScheduleFiles(linCtx, 0)
	if err != nil {
		t.Fatalf("RenderScheduleFiles(linux, 0): %v", err)
	}
	if linSpec.IntervalHours != 12 {
		t.Fatalf("default linux interval = %d, want 12", linSpec.IntervalHours)
	}
	svc := linSpec.Files[0].Content
	if !strings.Contains(svc, "--output "+linCtx.WSDigestPath()) {
		t.Errorf("linux service missing --output flag:\n%s", svc)
	}
	if strings.Contains(svc, "StandardOutput=truncate") {
		t.Errorf("linux service must not use StandardOutput=truncate:\n%s", svc)
	}
}

func TestExecBootstrapCorruptLedgerFails(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ledgerPath := app.LedgerPath(ctx.WSBaseDir())
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	const corrupt = "{not-valid-json"
	if err := os.WriteFile(ledgerPath, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	r := newMockRunner("brew")
	plan := &engine.Plan{
		Verb: "bootstrap",
		Actions: []engine.Action{
			{ID: "install:brew:bat", Kind: engine.ActionInstall, Command: []string{"brew", "install", "bat"}},
		},
	}
	if _, err := ExecBootstrap(ctx, plan, r); err == nil {
		t.Fatalf("expected ExecBootstrap to fail on corrupt ledger")
	}
	if len(r.calls) != 0 {
		t.Fatalf("ExecBootstrap must not execute actions when ledger is corrupt")
	}
	raw, _ := os.ReadFile(ledgerPath)
	if string(raw) != corrupt {
		t.Fatalf("corrupt ledger was overwritten: got %q", string(raw))
	}
}

type mockExitError int

func (e mockExitError) Error() string { return "exit status" }
func (e mockExitError) ExitCode() int { return int(e) }

func TestPhase3PkgmgrRegressions(t *testing.T) {

	rBrew := newMockRunner("brew")
	rBrew.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/core/bat\nanomalyco/tap/hex\nother/tap/foo\n", "", nil
	})
	rBrew.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/cask/claude-code@latest\nhomebrew/cask/codex\n", "", nil
	})
	provs := AllProviders(makeCtx("", t.TempDir(), "darwin", "macos"), rBrew, nil)
	if !provs["brew"].IsInstalled(config.ResolvedTool{ID: "bat", Spec: config.InstallSpec{Via: "brew", Name: "bat"}}) {
		t.Errorf("expected homebrew/core/bat to match bat")
	}
	if !provs["brew"].IsInstalled(config.ResolvedTool{ID: "hex", Spec: config.InstallSpec{Via: "brew", Name: "anomalyco/tap/hex"}}) {
		t.Errorf("expected anomalyco/tap/hex to match")
	}
	if provs["brew"].IsInstalled(config.ResolvedTool{ID: "foo", Spec: config.InstallSpec{Via: "brew", Name: "my/tap/foo"}}) {
		t.Errorf("my/tap/foo must NOT match other/tap/foo")
	}
	if !provs["cask"].IsInstalled(config.ResolvedTool{ID: "claude-code", Spec: config.InstallSpec{Via: "cask", Name: "claude-code@latest"}}) {
		t.Errorf("expected claude-code@latest to match")
	}

	repoDir, homeDir := makeSyntheticRepo(t)
	_ = os.WriteFile(filepath.Join(repoDir, "pkg", "mac-brew.txt"), []byte("anomalyco/tap/hex\n"), 0o644)
	ctxMac := makeCtx(repoDir, homeDir, "darwin", "macos")
	rTap := newMockRunner("brew")
	rTap.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rTap.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	tapPlan, err := PlanBootstrapWithRunner(ctxMac, "mac-workstation", rTap)
	if err != nil {
		t.Fatal(err)
	}
	var tapIdx, trustIdx, hexIdx int = -1, -1, -1
	for i, a := range tapPlan.Actions {
		if a.ID == "tap:brew:anomalyco/tap" {
			tapIdx = i
		}
		if a.ID == "trust:brew:anomalyco/tap/hex" {
			trustIdx = i
		}
		if strings.HasPrefix(a.ID, "install:brew:") && slices.Contains(a.Command, "anomalyco/tap/hex") {
			hexIdx = i
		}
	}
	if tapIdx < 0 || trustIdx < 0 || hexIdx < 0 || !(tapIdx < trustIdx && trustIdx < hexIdx) {
		t.Fatalf("expected tap (%d) < trust (%d) < install (%d)", tapIdx, trustIdx, hexIdx)
	}

	for _, badCmd := range [][]string{
		{"sudo", "-E", "apt-get", "update", "-o", "APT::Update::Pre-Invoke::=/bin/sh"},
		{"sudo", "apt-get", "install", "--option=DPkg::Pre-Invoke::=evil", "fd"},
		{"brew", "tap-new", "evil/tap"},
		{"mise", "exec", "--", "sh"},
	} {
		if err := validateBootstrapAction(ctxMac, engine.Action{ID: "bad", Kind: engine.ActionInstall, Command: badCmd}); err == nil {
			t.Errorf("expected validateBootstrapAction to reject %v", badCmd)
		}
	}

	rUpd := newMockRunner("git", "brew", "rustup", "tldr", "gh", "tailscale", "tailscaled")
	rUpd.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rUpd.on("tailscale version", func([]string, map[string]string) (string, string, error) {
		return "1.80.0\n  daemon: 1.78.0\n", "", nil
	})
	fullUpd, err := PlanUpdateWithRunner(ctxMac, UpdateOptions{All: true, PkgOnly: false}, rUpd)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[string]bool{}
	for _, a := range fullUpd.Plan.Actions {
		gotIDs[a.ID] = true
	}
	for _, want := range []string{"update:repo-pull", "update:brew-index", "update:brew-autoremove", "update:brew-cleanup", "update:rustup", "update:tldr", "update:gh-ext"} {
		if !gotIDs[want] {
			t.Errorf("missing PlanUpdate action %q; got %v", want, gotIDs)
		}
	}
	if !strings.Contains(strings.Join(fullUpd.Plan.Warnings, "\n"), "tailscaled daemon") {
		t.Errorf("expected stale tailscaled warning on macOS, got %v", fullUpd.Plan.Warnings)
	}
	pkgOnly, err := PlanUpdateWithRunner(ctxMac, UpdateOptions{All: true, PkgOnly: true}, rUpd)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range pkgOnly.Plan.Actions {
		if a.ID == "update:repo-pull" || a.ID == "update:brew-cleanup" || a.ID == "update:rustup" {
			t.Errorf("PkgOnly must omit %q, got action %+v", a.ID, a)
		}
	}

	ctxLin := makeCtx(repoDir, homeDir, "linux", "fedora")
	rDnf := newMockRunner("dnf")
	rDnf.on("dnf check-update -q", func([]string, map[string]string) (string, string, error) {
		return "fd-find.x86_64 9.0.0-1 fedora\nObsoleting Packages\nold-pkg.x86_64 1.0 fedora\n", "", mockExitError(100)
	})
	dnfDig, err := BuildUpdateDigestWithRunner(ctxLin, false, rDnf)
	if err != nil || len(dnfDig.Items) != 1 || dnfDig.Items[0].Name != "fd-find" {
		t.Fatalf("unexpected dnf digest on exit 100: %+v err=%v", dnfDig.Items, err)
	}

	if got := systemdQuoteArg("/tmp/100%$HOME dir"); got != `"/tmp/100%%$$HOME dir"` {
		t.Errorf("systemdQuoteArg = %q", got)
	}
	spec, err := RenderScheduleFiles(ctxMac, 12)
	if err != nil || !strings.Contains(spec.Files[0].Content, "<string>--repo</string>") || !strings.Contains(spec.Files[0].Content, "<key>WS_DIR</key>") {
		t.Fatalf("expected --repo and WS_DIR in launchd schedule: err=%v\n%s", err, spec.Files[0].Content)
	}
}

func TestExecUpdateParallelPhasesAndNonFatalWarnings(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "mac-workstation"

	r := newMockRunner("git", "brew", "rustup", "tldr", "gh")
	r.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	r.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "bat\nfd\nripgrep\nyq\n", "", nil
	})
	r.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "ghostty\nclaude-code@latest\ncodex\n1password\n", "", nil
	})
	r.on("brew outdated --json=v2 --greedy", func([]string, map[string]string) (string, string, error) {
		return `{"formulae":[{"name":"bat","installed_versions":["0.24.0"],"current_version":"0.25.0","pinned":false}],"casks":[]}`, "", nil
	})
	r.on("brew outdated --formula --quiet", func([]string, map[string]string) (string, string, error) {
		return "bat\nself-tool\n", "", nil
	})
	r.on("brew outdated --cask --greedy --quiet", func([]string, map[string]string) (string, string, error) {
		return "self-cask-tool\n", "", nil
	})
	r.on("gh extension list", func([]string, map[string]string) (string, string, error) {
		return "dlvhdr/gh-dash\n", "", nil
	})
	r.on("rustup update", func([]string, map[string]string) (string, string, error) {
		return "", "mirror timeout", errors.New("exit 1")
	})

	digest, err := PlanUpdateWithRunner(ctx, UpdateOptions{All: true}, r)
	if err != nil {
		t.Fatalf("PlanUpdateWithRunner failed: %v", err)
	}
	res, err := ExecUpdate(ctx, digest, r)
	if err != nil {
		t.Fatalf("expected ExecUpdate to tolerate continue_on_error rustup failure, got err=%v (warnings=%v)", err, res.Warnings)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "update:rustup") {
		t.Fatalf("expected rustup warning recorded, got %v", res.Warnings)
	}

	idxOf := func(sub string) int {
		for i, c := range r.calls {
			if strings.Contains(strings.Join(c.Cmd, " "), sub) {
				return i
			}
		}
		return -1
	}
	pullIdx := idxOf("pull --ff-only")
	brewIdx := idxOf("brew update")
	refreshFormulaIdx := -1
	upgIdx := -1
	for i, c := range r.calls {
		joined := strings.Join(c.Cmd, " ")
		if joined == "brew outdated --formula --quiet" {
			refreshFormulaIdx = i
		}
		if strings.HasPrefix(joined, "brew upgrade --formula") {
			upgIdx = i
			if slices.Contains(c.Cmd, "self-tool") {
				t.Fatalf("self-updated formula self-tool leaked into upgrade command: %v", c.Cmd)
			}
		}
	}
	autoRmIdx := idxOf("brew autoremove")
	cleanIdx := idxOf("brew cleanup --prune=all")
	tldrIdx := idxOf("tldr --update")

	if pullIdx < 0 || brewIdx < 0 || refreshFormulaIdx < 0 || upgIdx < 0 || autoRmIdx < 0 || cleanIdx < 0 || tldrIdx < 0 {
		t.Fatalf("missing expected update commands in calls: %+v", r.calls)
	}
	if pullIdx > upgIdx || brewIdx > refreshFormulaIdx || refreshFormulaIdx > upgIdx {
		t.Fatalf("expected Phase 1 (pull=%d, brew-update=%d) < post-update refresh (%d) < upgrade (%d)", pullIdx, brewIdx, refreshFormulaIdx, upgIdx)
	}
	if upgIdx >= autoRmIdx || autoRmIdx >= cleanIdx {
		t.Fatalf("expected Phase 2 sequential order upgrade (%d) < brew autoremove (%d) < brew cleanup (%d)", upgIdx, autoRmIdx, cleanIdx)
	}
	if cleanIdx > tldrIdx {
		t.Fatalf("expected Phase 2 cleanup (%d) strictly before Phase 3 parallel tools (tldr=%d)", cleanIdx, tldrIdx)
	}

	rEmptyPre := newMockRunner("git", "brew", "tldr")
	rEmptyPre.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rEmptyPre.on("brew outdated --json=v2", func([]string, map[string]string) (string, string, error) {
		return `{"formulae":[],"casks":[]}`, "", nil
	})
	rEmptyPre.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "bat\nfd\nripgrep\nyq\n", "", nil
	})
	rEmptyPre.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "ghostty\nclaude-code@latest\ncodex\n1password\n", "", nil
	})
	emptyPlan, err := PlanUpdateWithRunner(ctx, UpdateOptions{All: false}, rEmptyPre)
	if err != nil {
		t.Fatalf("PlanUpdateWithRunner (empty pre-outdated): %v", err)
	}
	hasUpgradeBrew := false
	for _, a := range emptyPlan.Plan.Actions {
		if a.ID == "upgrade:brew" && a.Metadata["refresh_before_run"] == "true" {
			hasUpgradeBrew = true
		}
	}
	if !hasUpgradeBrew {
		t.Fatalf("expected full update plan to schedule upgrade:brew with refresh_before_run even when pre-update digest was empty: %+v", emptyPlan.Plan.Actions)
	}
}

func TestDetectUntrackedAndAddRemovePackageList(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "mac-workstation"

	r := newMockRunner("brew")
	r.on("brew list --formula --installed-on-request --full-name", func([]string, map[string]string) (string, string, error) {
		return "bat\njq\nhomebrew/core/fd\n", "", nil
	})
	r.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/cask/ghostty\nhomebrew/cask/raycast\n", "", nil
	})

	untracked, err := DetectUntrackedPackages(ctx, "mac-workstation", r)
	if err != nil {
		t.Fatalf("DetectUntrackedPackages failed: %v", err)
	}
	gotNames := map[string]string{}
	for _, u := range untracked {
		gotNames[u.Provider+":"+u.Name] = u.SuggestedFile
	}
	if gotNames["brew:jq"] != "pkg/mac-brew.txt" || gotNames["cask:homebrew/cask/raycast"] != "pkg/mac-cask.txt" {
		t.Fatalf("unexpected untracked packages: %+v", untracked)
	}
	if _, exists := gotNames["brew:bat"]; exists {
		t.Fatalf("managed package 'bat' must not appear untracked: %+v", untracked)
	}

	added, err := AddPackageToList(repoDir, "pkg/mac-brew.txt", "jq")
	if err != nil || !added {
		t.Fatalf("AddPackageToList(jq) failed: added=%v err=%v", added, err)
	}
	addedAgain, err := AddPackageToList(repoDir, "pkg/mac-brew.txt", "jq")
	if err != nil || addedAgain {
		t.Fatalf("expected idempotent second AddPackageToList, added=%v err=%v", addedAgain, err)
	}
	foundFile, ok := FindPackageListFile(repoDir, "jq", nil)
	if !ok || foundFile != "pkg/mac-brew.txt" {
		t.Fatalf("FindPackageListFile(jq) = %q, %v", foundFile, ok)
	}
	removed, err := RemovePackageFromList(repoDir, "pkg/mac-brew.txt", "jq")
	if err != nil || !removed {
		t.Fatalf("RemovePackageFromList(jq) failed: removed=%v err=%v", removed, err)
	}
	if _, err := AddPackageToList(repoDir, "../escape.txt", "jq"); err == nil {
		t.Fatalf("expected traversal package list path to fail")
	}
}

func TestSearchAndInspectPackagesMultiProvider(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctxMac := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctxMac.PresetOverride = "mac-workstation"

	rMac := newMockRunner("brew", "mise")
	rMac.on("brew desc --search -- rip", func([]string, map[string]string) (string, string, error) {
		return "==> Formulae\nripgrep ✔: Search tool like grep and The Silver Searcher\nripgrep-all: Wrapper around ripgrep\n\n==> Casks\ncmux: Ghostty-based terminal\n", "", nil
	})
	rMac.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/core/ripgrep\n", "", nil
	})
	rMac.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rMac.on("mise registry", func([]string, map[string]string) (string, string, error) {
		return "ripgrep aqua:BurntSushi/ripgrep cargo:ripgrep\n", "", nil
	})
	rMac.on("mise ls --installed --json", func([]string, map[string]string) (string, string, error) {
		return "{}", "", nil
	})

	results, err := SearchPackagesWithRunner(ctxMac, "rip", SearchOptions{}, rMac)
	if err != nil {
		t.Fatalf("SearchPackagesWithRunner(mac): %v", err)
	}
	if len(results) < 4 {
		t.Fatalf("expected >=4 search results, got %+v", results)
	}
	foundRG := false
	for _, r := range results {
		if r.Provider == "brew" && r.Name == "ripgrep" {
			foundRG = true
			if !r.Installed || !r.Tracked {
				t.Fatalf("expected ripgrep marked installed=true & tracked=true, got %+v", r)
			}
		}
	}
	if !foundRG {
		t.Fatalf("missing brew ripgrep in results: %+v", results)
	}

	rFallback := newMockRunner("brew")
	rFallback.on("brew desc --search --formula -- foo", func([]string, map[string]string) (string, string, error) {
		return "", "no index", errors.New("exit 1")
	})
	rFallback.on("brew search --formula -- foo", func([]string, map[string]string) (string, string, error) {
		return "foo-cli\nbar-foo\n", "", nil
	})
	fbRes, err := SearchPackagesWithRunner(ctxMac, "foo", SearchOptions{Provider: "brew"}, rFallback)
	if err != nil || len(fbRes) != 2 || fbRes[0].Name != "foo-cli" {
		t.Fatalf("brew search fallback failed: res=%+v err=%v", fbRes, err)
	}

	ctxArch := makeCtx(repoDir, homeDir, "linux", "arch")
	rArch := newMockRunner("paru", "pacman")
	rArch.on("paru --color=never -Ss -- rip", func([]string, map[string]string) (string, string, error) {
		return "extra/ripgrep 14.1.1-1 [installed]\n    Line-oriented search tool\naur/ripgrep-all-bin 0.10.6-1 (+12 0.4)\n    Search inside PDFs and archives\n", "", nil
	})
	rArch.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "ripgrep\n", "", nil
	})
	archRes, err := SearchPackagesWithRunner(ctxArch, "rip", SearchOptions{Provider: "paru"}, rArch)
	if err != nil || len(archRes) != 2 {
		t.Fatalf("arch search failed: res=%+v err=%v", archRes, err)
	}
	if archRes[0].Name != "ripgrep" || archRes[0].Repo != "extra" || !archRes[0].Installed {
		t.Fatalf("unexpected official Arch search row: %+v", archRes[0])
	}
	if archRes[1].Name != "ripgrep-all-bin" || archRes[1].Repo != "aur" || archRes[1].Provider != "paru" {
		t.Fatalf("unexpected AUR search row: %+v", archRes[1])
	}

	rArch.on("paru --color=never -Qi -- ripgrep", func([]string, map[string]string) (string, string, error) {
		return "", "not installed", errors.New("exit 1")
	})
	rArch.on("paru --color=never -Si -- ripgrep", func([]string, map[string]string) (string, string, error) {
		return "Repository      : extra\nName            : ripgrep\nVersion         : 14.1.1-1\nDescription     : Search tool\nURL             : https://example.invalid\nLicenses        : MIT  Unlicense\nDepends On      : gcc-libs  pcre2\nOptional Deps   : foo: extra\n                  bar: more\nInstalled Size  : 4.91 MiB\n", "", nil
	})
	info, err := GetPackageInfoWithRunner(ctxArch, "ripgrep", "paru", rArch)
	if err != nil {
		t.Fatalf("GetPackageInfoWithRunner(arch): %v", err)
	}
	if info.Repo != "extra" || info.Version != "14.1.1-1" || len(info.Dependencies) != 2 || len(info.OptionalDeps) != 2 {
		t.Fatalf("unexpected Arch PackageInfo: %+v", info)
	}

	if _, err := GetPackageInfoWithRunner(ctxArch, "--malicious", "paru", rArch); err == nil {
		t.Fatalf("expected invalid leading-dash package name rejection")
	}
	if _, err := SearchPackagesWithRunner(ctxArch, "   ", SearchOptions{}, rArch); err == nil {
		t.Fatalf("expected empty query rejection")
	}
}

func TestBrewProviderTreatsSystemZshInstalledOnDarwin(t *testing.T) {
	binDir := t.TempDir()
	zshStub := filepath.Join(binDir, "zsh")
	if err := os.WriteFile(zshStub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	r := newMockRunner("brew", "zsh")
	r.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/core/bat\n", "", nil
	})
	macCtx := makeCtx("", t.TempDir(), "darwin", "macos")
	provs := AllProviders(macCtx, r, nil)
	if !provs["brew"].IsInstalled(config.ResolvedTool{
		ID:   "zsh",
		Bin:  "zsh",
		Spec: config.InstallSpec{Via: "brew", Name: "zsh"},
	}) {
		t.Fatalf("expected macOS brewProvider to treat zsh on PATH as installed")
	}
}

func TestExecUpdateWithOptionsEventsAndStreaming(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "mac-workstation"

	setupBrewMock := func(dirtyRepo bool) (*mockRunner, *UpdateDigest) {
		r := newMockRunner("git", "brew", "rustup", "tldr", "gh")
		r.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
			if dirtyRepo {
				return " M presets.toml\n", "", nil
			}
			return "", "", nil
		})
		r.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
			return "bat\nfd\nripgrep\nyq\n", "", nil
		})
		r.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
			return "ghostty\nclaude-code@latest\ncodex\n1password\n", "", nil
		})
		r.on("brew outdated --json=v2 --greedy", func([]string, map[string]string) (string, string, error) {
			return `{"formulae":[{"name":"bat","installed_versions":["0.24.0"],"current_version":"0.25.0","pinned":false}],"casks":[]}`, "", nil
		})
		r.on("brew outdated --formula --quiet", func([]string, map[string]string) (string, string, error) {
			return "bat\n", "", nil
		})
		r.on("brew outdated --cask --greedy --quiet", func([]string, map[string]string) (string, string, error) {
			return "", "", nil
		})
		r.on("brew update", func([]string, map[string]string) (string, string, error) {
			return "Updated 1 tap.\n", "", nil
		})
		r.on("brew upgrade --formula bat", func([]string, map[string]string) (string, string, error) {
			return "==> Upgrading bat\n", "", nil
		})
		r.on("gh extension list", func([]string, map[string]string) (string, string, error) {
			return "dlvhdr/gh-dash\n", "", nil
		})
		digest, err := PlanUpdateWithRunner(ctx, UpdateOptions{All: true}, r)
		if err != nil {
			t.Fatalf("PlanUpdateWithRunner failed: %v", err)
		}
		return r, digest
	}

	r1, dig1 := setupBrewMock(false)
	var events []UpdateEvent
	var outBuf, errBuf bytes.Buffer
	res1, err := ExecUpdateWithOptions(ctx, dig1, r1, ExecUpdateOptions{
		StreamOut: &outBuf,
		StreamErr: &errBuf,
		OnEvent: func(ev UpdateEvent) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatalf("ExecUpdateWithOptions failed: %v (warnings=%v)", err, res1.Warnings)
	}
	var phases []string
	for _, ev := range events {
		if ev.Status == "phase" {
			phases = append(phases, ev.Phase)
		}
	}
	wantPhases := []string{"prep", "os-pkg", "post-parallel", "finalize"}
	if !slices.Equal(phases, wantPhases) {
		t.Fatalf("phase order = %v, want %v", phases, wantPhases)
	}
	if !strings.Contains(outBuf.String(), "==> Upgrading bat") {
		t.Fatalf("expected streamed Phase 2 brew upgrade stdout, got:\n%s", outBuf.String())
	}
	if strings.Contains(outBuf.String(), "Updated 1 tap.") {
		t.Fatalf("parallel Phase 1 brew update must not interleave stdout into stream:\n%s", outBuf.String())
	}

	callStreamed := map[string]bool{}
	for _, c := range r1.calls {
		callStreamed[strings.Join(c.Cmd, " ")] = c.Streamed
	}
	if !callStreamed["brew upgrade --formula bat"] || !callStreamed["brew autoremove"] || !callStreamed["brew cleanup --prune=all"] {
		t.Fatalf("expected Phase 2 actions to use RunStreaming, got calls=%+v", callStreamed)
	}
	if callStreamed["brew update"] || callStreamed["rustup update"] || callStreamed["tldr --update"] {
		t.Fatalf("expected multi-action parallel phases not to stream, got calls=%+v", callStreamed)
	}

	r2, dig2 := setupBrewMock(true)
	var outBuf2 bytes.Buffer
	if _, err := ExecUpdateWithOptions(ctx, dig2, r2, ExecUpdateOptions{StreamOut: &outBuf2}); err != nil {
		t.Fatalf("ExecUpdateWithOptions (single prep) failed: %v", err)
	}
	if !strings.Contains(outBuf2.String(), "Updated 1 tap.") {
		t.Fatalf("expected single-action Phase 1 brew update to stream live, got:\n%s", outBuf2.String())
	}
}

func TestExecUpdatePreservesPostApplyUndoTransaction(t *testing.T) {
	t.Setenv("WS_EXCLUDE_PATHS", "go,codex,aws,tmux,gh-dash,bat")
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctx.PresetOverride = "base"

	if err := os.MkdirAll(filepath.Join(repoDir, "themes", "gruvbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	pal := `name = "gruvbox-dark"
mode = "dark"
bg = "#282828"
bg_alt = "#3c3836"
fg = "#ebdbb2"
fg_dim = "#928374"
accent = "#fe8019"
selection_bg = "#665c54"
selection_fg = "#ebdbb2"
cursor = "#ebdbb2"
border = "#504945"
color0 = "#282828"
color1 = "#cc241d"
color2 = "#98971a"
color3 = "#d79921"
color4 = "#458588"
color5 = "#b16286"
color6 = "#689d6a"
color7 = "#a89984"
color8 = "#928374"
color9 = "#fb4934"
color10 = "#b8bb26"
color11 = "#fabd2f"
color12 = "#83a598"
color13 = "#d3869b"
color14 = "#8ec07c"
color15 = "#ebdbb2"
`
	if err := os.WriteFile(filepath.Join(repoDir, "themes", "gruvbox", "dark.toml"), []byte(pal), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "config", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "config", "a", "rc"), []byte("a=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "links", "common.links"), []byte("config/a/rc ~/.a-rc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initPlan, err := engine.PlanApply(ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("initial PlanApply: %v", err)
	}
	initRes, err := engine.ExecApply(ctx, initPlan)
	if err != nil {
		t.Fatalf("initial ExecApply: %v", err)
	}
	if initRes.UndoID == "" {
		t.Fatalf("expected initial ExecApply to record UndoID")
	}

	if err := os.MkdirAll(filepath.Join(repoDir, "config", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "config", "b", "rc"), []byte("b=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "links", "common.links"), []byte("config/a/rc ~/.a-rc\nconfig/b/rc ~/.b-rc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newMockRunner("brew")
	digest := &UpdateDigest{
		Plan: &engine.Plan{
			Verb:   "update",
			Title:  "Update packages",
			Preset: "base",
			Metadata: map[string]string{
				"post_apply": "true",
			},
			Actions: []engine.Action{
				{
					ID:       "update:brew-index",
					Kind:     engine.ActionUpgrade,
					Target:   "brew",
					Summary:  "Update Homebrew index",
					Command:  []string{"brew", "update"},
					Metadata: map[string]string{"phase": "prep", "provider": "brew"},
				},
			},
		},
	}

	if _, err := ExecUpdate(ctx, digest, r); err != nil {
		t.Fatalf("ExecUpdate: %v", err)
	}

	linkA := filepath.Join(homeDir, ".a-rc")
	linkB := filepath.Join(homeDir, ".b-rc")
	if _, err := os.Lstat(linkB); err != nil {
		t.Fatalf("expected post-update apply to create %s: %v", linkB, err)
	}

	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if len(ledger.UndoLog) != 2 {
		t.Fatalf("expected 2 UndoLog transactions after update+apply, got %d: %+v", len(ledger.UndoLog), ledger.UndoLog)
	}
	if ledger.ManagedLinks[linkB] == "" {
		t.Fatalf("expected post-update ManagedLinks to preserve %s, got %+v", linkB, ledger.ManagedLinks)
	}

	if _, err := engine.UndoLast(ctx); err != nil {
		t.Fatalf("UndoLast: %v", err)
	}
	if _, err := os.Lstat(linkB); !os.IsNotExist(err) {
		t.Fatalf("expected UndoLast to remove update's link %s, err=%v", linkB, err)
	}
	if _, err := os.Lstat(linkA); err != nil {
		t.Fatalf("expected earlier transaction's link %s to remain intact: %v", linkA, err)
	}
}

func TestArchUpdatePlanOrderAndBatchedInstalledCheck(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "arch")
	ctx.PresetOverride = "arch-desktop"

	r := newMockRunner("git", "paru", "pacman", "paccache", "bash")
	r.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	r.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nnoctalia-shell\nripgrep\n", "", nil
	})
	r.on("paru -Qu", func([]string, map[string]string) (string, string, error) {
		return "hyprland 0.54.0-1 -> 0.55.0-1\n", "", nil
	})

	digest, err := PlanUpdateWithRunner(ctx, UpdateOptions{All: true}, r)
	if err != nil {
		t.Fatalf("PlanUpdateWithRunner(arch) failed: %v", err)
	}

	qqCalls := 0
	qiCalls := 0
	for _, c := range r.calls {
		joined := strings.Join(c.Cmd, " ")
		if joined == "pacman -Qq" {
			qqCalls++
		}
		if len(c.Cmd) >= 2 && (c.Cmd[0] == "pacman" || c.Cmd[0] == "paru") && c.Cmd[1] == "-Qi" {
			qiCalls++
		}
	}
	if qqCalls < 1 || qiCalls != 0 {
		t.Fatalf("expected batched pacman -Qq>=1 and -Qi=0, got qq=%d qi=%d", qqCalls, qiCalls)
	}

	var osPhase []engine.Action
	for _, a := range digest.Plan.Actions {
		if a.Metadata["phase"] == "os-pkg" {
			osPhase = append(osPhase, a)
		}
		if strings.Contains(a.ID, "postinstall.sh") || strings.Contains(a.ID, "conflicts.sh") {
			t.Fatalf("OS hook must not be scheduled in update plan: %+v", a)
		}
	}
	if len(osPhase) != 6 {
		t.Fatalf("expected 6 Arch os-pkg actions (2 conflict removals, -Syu, batched -S, paccache -rk2, paccache -ruk0), got %d: %+v", len(osPhase), osPhase)
	}

	if osPhase[0].ID != "conflict:remove:pacman:jack" || osPhase[0].Kind != engine.ActionRemove || !osPhase[0].NeedsTTY || osPhase[0].RequiresAllow != "remove,sudo" {
		t.Fatalf("expected jack conflict removal first with NeedsTTY+remove,sudo, got %+v", osPhase[0])
	}
	if !slices.Equal(osPhase[0].Command, []string{"sudo", "pacman", "-Rdd", "--noconfirm", "jack"}) {
		t.Fatalf("unexpected jack removal cmd: %v", osPhase[0].Command)
	}
	if osPhase[1].ID != "conflict:remove:pacman:noctalia-shell" || osPhase[1].Kind != engine.ActionRemove || !osPhase[1].NeedsTTY || osPhase[1].RequiresAllow != "remove,sudo" {
		t.Fatalf("expected noctalia-shell conflict removal second with NeedsTTY+remove,sudo, got %+v", osPhase[1])
	}
	if !slices.Equal(osPhase[1].Command, []string{"sudo", "pacman", "-Rdd", "--noconfirm", "noctalia-shell"}) {
		t.Fatalf("unexpected noctalia-shell removal cmd: %v", osPhase[1].Command)
	}

	if osPhase[2].ID != "upgrade:paru" || !slices.Equal(osPhase[2].Command[:3], []string{"paru", "-Syu", "--noconfirm"}) {
		t.Fatalf("expected paru -Syu third, got %+v", osPhase[2])
	}

	batchAct := osPhase[3]
	if batchAct.ID != "install:paru:batch" || !batchAct.NeedsTTY || batchAct.RequiresAllow != "sudo" {
		t.Fatalf("expected batched paru install fourth, got %+v", batchAct)
	}
	wantBatchCmd := []string{"paru", "-S", "--needed", "--noconfirm", "fd", "go-yq", "hyprland"}
	if !slices.Equal(batchAct.Command, wantBatchCmd) {
		t.Fatalf("batched install cmd = %v, want %v", batchAct.Command, wantBatchCmd)
	}

	if !slices.Equal(osPhase[4].Command, []string{"sudo", "paccache", "-rk2"}) || osPhase[4].RequiresAllow != "sudo" || !osPhase[4].NeedsTTY {
		t.Fatalf("expected sudo paccache -rk2 fifth, got %+v", osPhase[4])
	}
	if !slices.Equal(osPhase[5].Command, []string{"sudo", "paccache", "-ruk0"}) || osPhase[5].RequiresAllow != "sudo" || !osPhase[5].NeedsTTY {
		t.Fatalf("expected sudo paccache -ruk0 sixth, got %+v", osPhase[5])
	}

	rNoAllow := newMockRunner("git", "paru", "pacman", "paccache", "bash")
	resSkip, err := ExecUpdate(ctx, digest, rNoAllow)
	if err != nil {
		t.Fatalf("ExecUpdate without allow failed: %v", err)
	}
	if resSkip.ConsentSkipped != 6 || !slices.Contains(resSkip.MissingAllows, "sudo") || !slices.Contains(resSkip.MissingAllows, "remove") {
		t.Fatalf("expected ConsentSkipped=6 MissingAllows containing remove+sudo, got skipped=%d missing=%v", resSkip.ConsentSkipped, resSkip.MissingAllows)
	}

	ctxSudoOnly := *ctx
	ctxSudoOnly.Allow.Sudo = true
	rSudoOnly := newMockRunner("git", "paru", "pacman", "paccache", "bash")
	resSudoOnly, err := ExecUpdate(&ctxSudoOnly, digest, rSudoOnly)
	if err != nil {
		t.Fatalf("ExecUpdate with sudo only failed: %v", err)
	}
	if resSudoOnly.ConsentSkipped != 2 || !slices.Equal(resSudoOnly.MissingAllows, []string{"remove"}) {
		t.Fatalf("expected conflict removals to stay gated on remove consent, got skipped=%d missing=%v", resSudoOnly.ConsentSkipped, resSudoOnly.MissingAllows)
	}

	ctxAllow := *ctx
	ctxAllow.Allow.Sudo = true
	ctxAllow.Allow.Remove = true
	rAllow := newMockRunner("git", "paru", "pacman", "paccache", "bash")
	resOK, err := ExecUpdate(&ctxAllow, digest, rAllow)
	if err != nil {
		t.Fatalf("ExecUpdate with remove+sudo failed: %v", err)
	}
	if resOK.ConsentSkipped != 0 {
		t.Fatalf("expected ConsentSkipped=0 with --allow remove,sudo, got %d", resOK.ConsentSkipped)
	}
	var execOrder []string
	for _, c := range rAllow.calls {
		if len(c.Cmd) > 0 && (c.Cmd[0] == "bash" || c.Cmd[0] == "paru" || c.Cmd[0] == "sudo") {
			execOrder = append(execOrder, strings.Join(c.Cmd, " "))
		}
	}
	wantOrder := []string{
		"sudo pacman -Rdd --noconfirm jack",
		"sudo pacman -Rdd --noconfirm noctalia-shell",
		"paru -Syu --noconfirm --ignore self-cask-tool --ignore self-tool",
		"paru -S --needed --noconfirm fd go-yq hyprland",
		"sudo paccache -rk2",
		"sudo paccache -ruk0",
	}
	if !slices.Equal(execOrder, wantOrder) {
		t.Fatalf("Arch exec order =\n%v\nwant:\n%v", execOrder, wantOrder)
	}

	rClean := newMockRunner("git", "paru", "pacman", "paccache", "bash")
	rClean.on("git -C "+repoDir+" status --porcelain", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rClean.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\nripgrep\n", "", nil
	})
	rClean.on("paru -Qu", func([]string, map[string]string) (string, string, error) {
		return "hyprland 0.54.0-1 -> 0.55.0-1\n", "", nil
	})
	cleanDigest, err := PlanUpdateWithRunner(ctx, UpdateOptions{All: true}, rClean)
	if err != nil {
		t.Fatalf("PlanUpdateWithRunner(arch clean) failed: %v", err)
	}
	for _, a := range cleanDigest.Plan.Actions {
		if a.Kind == engine.ActionRemove {
			t.Fatalf("expected no removal actions when no conflict packages are installed, got %+v", a)
		}
	}
}

func TestArchBootstrapPlanBatchesWithSyuAndFallback(t *testing.T) {
	assertNoPartialUpgrade := func(t *testing.T, plan *engine.Plan) {
		t.Helper()
		for _, a := range plan.Actions {
			if a.ID == "bootstrap:paru:deps" {
				continue
			}
			if len(a.Command) >= 2 && a.Command[0] == "paru" && a.Command[1] == "-S" {
				t.Fatalf("unexpected partial-upgrade paru -S without -Syu: %v", a.Command)
			}
			if len(a.Command) >= 3 && a.Command[0] == "sudo" && a.Command[1] == "pacman" && a.Command[2] == "-S" {
				t.Fatalf("unexpected partial-upgrade pacman -S without -Syu: %v", a.Command)
			}
			if len(a.Command) >= 2 && a.Command[0] == "pacman" && a.Command[1] == "-S" {
				t.Fatalf("unexpected partial-upgrade pacman -S without -Syu: %v", a.Command)
			}
		}
	}

	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "arch")
	ctx.PresetOverride = "arch-desktop"

	rParu := newMockRunner("paru", "pacman", "bash")
	rParu.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nnoctalia-shell\nripgrep\n", "", nil
	})
	planParu, err := PlanBootstrapWithRunner(ctx, "arch-desktop", rParu)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch paru) failed: %v", err)
	}
	assertNoPartialUpgrade(t, planParu)

	wantParuCmds := [][]string{
		{"sudo", "pacman", "-Rdd", "--noconfirm", "jack"},
		{"sudo", "pacman", "-Rdd", "--noconfirm", "noctalia-shell"},
		{"paru", "-Syu", "--needed", "--noconfirm", "fd", "go-yq", "hyprland"},
		{"bash", filepath.Join(repoDir, "os", "arch", "postinstall.sh")},
	}
	if len(planParu.Actions) != len(wantParuCmds) {
		t.Fatalf("paru bootstrap action count = %d, want %d: %+v", len(planParu.Actions), len(wantParuCmds), planParu.Actions)
	}
	for i, want := range wantParuCmds {
		if !slices.Equal(planParu.Actions[i].Command, want) {
			t.Fatalf("paru bootstrap action[%d] cmd = %v, want %v", i, planParu.Actions[i].Command, want)
		}
	}

	rPacman := newMockRunner("pacman", "bash")
	rPacman.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nnoctalia-shell\nripgrep\n", "", nil
	})
	planPacman, err := PlanBootstrapWithRunner(ctx, "arch-desktop", rPacman)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch paru bootstrap) failed: %v", err)
	}
	assertNoPartialUpgrade(t, planPacman)

	wantParuBootIDs := []string{
		"bootstrap:paru:deps",
		"bootstrap:paru:build",
		"conflict:remove:pacman:jack",
		"conflict:remove:pacman:noctalia-shell",
		"install:paru:batch",
		"os_exec:os/arch/postinstall.sh",
	}
	if len(planPacman.Actions) != len(wantParuBootIDs) {
		t.Fatalf("paru-absent bootstrap action count = %d, want %d: %+v", len(planPacman.Actions), len(wantParuBootIDs), planPacman.Actions)
	}
	for i, wantID := range wantParuBootIDs {
		if planPacman.Actions[i].ID != wantID {
			t.Fatalf("paru-absent action[%d] ID = %q, want %q", i, planPacman.Actions[i].ID, wantID)
		}
	}
	if !slices.Equal(planPacman.Actions[0].Command, []string{"sudo", "pacman", "-S", "--needed", "--noconfirm", "base-devel", "git"}) {
		t.Fatalf("paru deps cmd = %v", planPacman.Actions[0].Command)
	}
	if planPacman.Actions[1].Metadata["provider"] != "paru-bootstrap" || len(planPacman.Actions[1].Command) != 0 {
		t.Fatalf("unexpected paru build action: %+v", planPacman.Actions[1])
	}
	if !slices.Equal(planPacman.Actions[4].Command, []string{"paru", "-Syu", "--needed", "--noconfirm", "fd", "go-yq", "hyprland"}) {
		t.Fatalf("paru batch cmd = %v", planPacman.Actions[4].Command)
	}

	toolsRaw, _ := os.ReadFile(filepath.Join(repoDir, "tools.toml"))
	extraTools := string(toolsRaw) + `
[tool.arch-replaced]
bin = "arch-replaced"
updates = "manager"
conflicts = ["pacman:legacy-pkg"]
install = [
  { via = "pacman", name = "arch-replaced" },
]
`
	if err := os.WriteFile(filepath.Join(repoDir, "tools.toml"), []byte(extraTools), 0o644); err != nil {
		t.Fatal(err)
	}
	presetsRaw, _ := os.ReadFile(filepath.Join(repoDir, "presets.toml"))
	extraPreset := string(presetsRaw) + `
[preset.arch-conflict-tool]
description = "Arch tool conflict test"
include = ["arch-desktop"]
extra_tools = ["arch-replaced"]
secrets = "none"
`
	if err := os.WriteFile(filepath.Join(repoDir, "presets.toml"), []byte(extraPreset), 0o644); err != nil {
		t.Fatal(err)
	}

	rConflict := newMockRunner("paru", "pacman", "bash")
	rConflict.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nlegacy-pkg\nripgrep\n", "", nil
	})
	planConflict, err := PlanBootstrapWithRunner(ctx, "arch-conflict-tool", rConflict)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch-conflict-tool) failed: %v", err)
	}
	assertNoPartialUpgrade(t, planConflict)

	wantConflictCmds := [][]string{
		{"sudo", "pacman", "-Rdd", "--noconfirm", "jack"},
		{"sudo", "pacman", "-Rns", "--noconfirm", "legacy-pkg"},
		{"paru", "-Syu", "--needed", "--noconfirm", "fd", "go-yq", "hyprland"},
		{"paru", "-Syu", "--needed", "--noconfirm", "arch-replaced"},
		{"bash", filepath.Join(repoDir, "os", "arch", "postinstall.sh")},
	}
	if len(planConflict.Actions) != len(wantConflictCmds) {
		t.Fatalf("conflict-tool bootstrap action count = %d, want %d: %+v", len(planConflict.Actions), len(wantConflictCmds), planConflict.Actions)
	}
	for i, want := range wantConflictCmds {
		if !slices.Equal(planConflict.Actions[i].Command, want) {
			t.Fatalf("conflict-tool bootstrap action[%d] cmd = %v, want %v", i, planConflict.Actions[i].Command, want)
		}
	}

	ctxSudoOnly := *ctx
	ctxSudoOnly.Allow.Sudo = true
	rExecSkip := newMockRunner("paru", "pacman", "bash")
	resSkip, err := ExecBootstrap(&ctxSudoOnly, planConflict, rExecSkip)
	if err != nil {
		t.Fatalf("ExecBootstrap(sudo-only) failed: %v", err)
	}
	for _, id := range resSkip.Changed {
		if strings.Contains(id, "arch-replaced") {
			t.Fatalf("expected arch-replaced to be skipped when its conflict removal was skipped, changed=%v", resSkip.Changed)
		}
	}
	if !slices.Contains(resSkip.Changed, "install:paru:batch") {
		t.Fatalf("expected batched install to run even when per-tool conflict was skipped, changed=%v", resSkip.Changed)
	}
}

func TestBootstrapExactArgvOrderMac(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "mac-brew.txt"), []byte("bat\nfd\nripgrep\nyq\nhashicorp/tap/terraform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "mac-cask.txt"), []byte("ghostty\nclaude-code@latest\ncodex\n1password\nanomalyco/tap/hex\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "os", "mac", "postinstall.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	presetsRaw, err := os.ReadFile(filepath.Join(repoDir, "presets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	updatedPresets := strings.Replace(
		string(presetsRaw),
		`os_hooks = ["os/mac/defaults.sh"]`,
		`os_hooks = ["os/mac/postinstall.sh", "os/mac/defaults.sh"]`,
		1,
	)
	if err := os.WriteFile(filepath.Join(repoDir, "presets.toml"), []byte(updatedPresets), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := makeCtx(repoDir, homeDir, "darwin", "macos")
	r := newMockRunner("brew", "gh", "bash")
	r.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/core/bat\n", "", nil
	})
	r.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/cask/ghostty\n", "", nil
	})
	r.on("gh extension list", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})

	plan, err := PlanBootstrapWithRunner(ctx, "mac-workstation", r)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac-workstation): %v", err)
	}

	wantCmds := [][]string{
		{"brew", "update"},
		{"brew", "tap", "hashicorp/tap"},
		{"brew", "trust", "--formula", "hashicorp/tap/terraform"},
		{"brew", "install", "--formula", "fd", "ripgrep", "yq", "hashicorp/tap/terraform"},
		{"brew", "tap", "anomalyco/tap"},
		{"brew", "trust", "--cask", "anomalyco/tap/hex"},
		{"brew", "install", "--cask", "claude-code@latest", "codex", "1password", "anomalyco/tap/hex"},
		{"gh", "extension", "install", "dlvhdr/gh-dash"},
		{"bash", filepath.Join(repoDir, "os", "mac", "postinstall.sh")},
		{"bash", filepath.Join(repoDir, "os", "mac", "defaults.sh")},
	}
	if len(plan.Actions) != len(wantCmds) {
		t.Fatalf("mac plan action count = %d, want %d: %+v", len(plan.Actions), len(wantCmds), plan.Actions)
	}
	for i, want := range wantCmds {
		act := plan.Actions[i]
		if !slices.Equal(act.Command, want) {
			t.Fatalf("mac action[%d] cmd = %v, want %v", i, act.Command, want)
		}
		if len(want) > 0 && want[0] == "brew" {
			if act.Metadata["env.HOMEBREW_NO_ASK"] != "1" {
				t.Errorf("mac action[%d] (%s) missing HOMEBREW_NO_ASK=1", i, act.ID)
			}
		}
	}
	if !plan.Actions[6].NeedsTTY {
		t.Errorf("expected cask batch install to set NeedsTTY=true")
	}
	if plan.Actions[8].RequiresAllow != "sudo" || plan.Actions[9].RequiresAllow != "sudo" {
		t.Errorf("expected mac OS hooks to require sudo consent")
	}

	rInstalled := newMockRunner("brew", "gh", "bash")
	rInstalled.on("brew list --formula --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/core/bat\nhomebrew/core/fd\nhomebrew/core/ripgrep\nhomebrew/core/yq\nhashicorp/tap/terraform\n", "", nil
	})
	rInstalled.on("brew list --cask --full-name", func([]string, map[string]string) (string, string, error) {
		return "homebrew/cask/ghostty\nhomebrew/cask/claude-code@latest\nhomebrew/cask/codex\nhomebrew/cask/1password\nanomalyco/tap/hex\n", "", nil
	})
	rInstalled.on("gh extension list", func([]string, map[string]string) (string, string, error) {
		return "dlvhdr/gh-dash\n", "", nil
	})
	idempPlan, err := PlanBootstrapWithRunner(ctx, "mac-workstation", rInstalled)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac idempotent): %v", err)
	}
	wantIdemp := [][]string{
		{"bash", filepath.Join(repoDir, "os", "mac", "postinstall.sh")},
		{"bash", filepath.Join(repoDir, "os", "mac", "defaults.sh")},
	}
	if len(idempPlan.Actions) != len(wantIdemp) {
		t.Fatalf("mac idempotent action count = %d, want %d: %+v", len(idempPlan.Actions), len(wantIdemp), idempPlan.Actions)
	}
	for i, want := range wantIdemp {
		if !slices.Equal(idempPlan.Actions[i].Command, want) {
			t.Fatalf("mac idempotent action[%d] = %v, want %v", i, idempPlan.Actions[i].Command, want)
		}
	}

	ctxNonInt := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctxNonInt.NonInteractive = true
	ctxNonInt.Allow.Sudo = true
	nonIntPlan, err := PlanBootstrapWithRunner(ctxNonInt, "mac-workstation", rInstalled)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac non-interactive): %v", err)
	}
	rExec := newMockRunner("bash")
	if _, err := ExecBootstrap(ctxNonInt, nonIntPlan, rExec); err != nil {
		t.Fatalf("ExecBootstrap(mac non-interactive): %v", err)
	}
	if len(rExec.calls) != 2 || rExec.calls[0].NeedsTTY || rExec.calls[0].Env["WS_NON_INTERACTIVE"] != "1" {
		t.Fatalf("expected non-interactive hook env and NeedsTTY=false, got %+v", rExec.calls)
	}
	if rExec.calls[0].Env["WS_YES"] != "0" {
		t.Fatalf("sudo-only consent must not pass WS_YES to OS hooks, got %+v", rExec.calls[0].Env)
	}

	ctxScopes := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctxScopes.NonInteractive = true
	ctxScopes.Allow = app.AllowSet{Replace: true, Sudo: true, Remove: true, Script: true}
	scopesPlan, err := PlanBootstrapWithRunner(ctxScopes, "mac-workstation", rInstalled)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac per-scope grants): %v", err)
	}
	if _, ok := scopesPlan.Actions[0].Metadata["env.WS_YES"]; ok {
		t.Fatalf("per-scope grants of every scope must not plan WS_YES, got %+v", scopesPlan.Actions[0].Metadata)
	}
	rScopes := newMockRunner("bash")
	if _, err := ExecBootstrap(ctxScopes, scopesPlan, rScopes); err != nil {
		t.Fatalf("ExecBootstrap(mac per-scope grants): %v", err)
	}
	if rScopes.calls[0].Env["WS_YES"] != "0" {
		t.Fatalf("per-scope grants of every scope must not pass WS_YES to OS hooks, got %+v", rScopes.calls[0].Env)
	}

	ctxYes := makeCtx(repoDir, homeDir, "darwin", "macos")
	ctxYes.NonInteractive = true
	ctxYes.Allow, err = app.ParseAllowStrict("all")
	if err != nil {
		t.Fatal(err)
	}
	yesPlan, err := PlanBootstrapWithRunner(ctxYes, "mac-workstation", rInstalled)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(mac --yes): %v", err)
	}
	if yesPlan.Actions[0].Metadata["env.WS_YES"] != "1" {
		t.Fatalf("expected full consent to plan WS_YES=1 for OS hooks, got %+v", yesPlan.Actions[0].Metadata)
	}
	rYes := newMockRunner("bash")
	if _, err := ExecBootstrap(ctxYes, yesPlan, rYes); err != nil {
		t.Fatalf("ExecBootstrap(mac --yes): %v", err)
	}
	if len(rYes.calls) != 2 || rYes.calls[0].Env["WS_YES"] != "1" || rYes.calls[0].Env["WS_NON_INTERACTIVE"] != "1" {
		t.Fatalf("expected WS_YES=1 and WS_NON_INTERACTIVE=1 in OS hook env, got %+v", rYes.calls)
	}

	rStale := newMockRunner("bash")
	if _, err := ExecBootstrap(ctxNonInt, yesPlan, rStale); err != nil {
		t.Fatalf("ExecBootstrap(saved --yes plan, sudo only): %v", err)
	}
	if rStale.calls[0].Env["WS_YES"] != "0" {
		t.Fatalf("WS_YES from a saved plan must not survive without full consent, got %+v", rStale.calls[0].Env)
	}
}

func TestBootstrapExactArgvOrderArchFlatpakAndParuBootstrap(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "arch.txt"), []byte("bat\nfd\ngo-yq\nhyprland\nflatpak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := makeCtx(repoDir, homeDir, "linux", "arch")
	ctx.PresetOverride = "arch-desktop"

	rParu := newMockRunner("paru", "pacman", "bash")
	rParu.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nnoctalia-shell\nripgrep\n", "", nil
	})
	planParu, err := PlanBootstrapWithRunner(ctx, "arch-desktop", rParu)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch paru+flatpak): %v", err)
	}
	wantParu := [][]string{
		{"sudo", "pacman", "-Rdd", "--noconfirm", "jack"},
		{"sudo", "pacman", "-Rdd", "--noconfirm", "noctalia-shell"},
		{"paru", "-Syu", "--needed", "--noconfirm", "fd", "go-yq", "hyprland", "flatpak"},
		{"flatpak", "remote-add", "--if-not-exists", "flathub", flathubRepoURL},
		{"flatpak", "install", "-y", "--noninteractive", "flathub", "org.gtk.Gtk3theme.adw-gtk3", "org.gtk.Gtk3theme.adw-gtk3-dark"},
		{"bash", filepath.Join(repoDir, "os", "arch", "postinstall.sh")},
	}
	if len(planParu.Actions) != len(wantParu) {
		t.Fatalf("arch paru plan count = %d, want %d: %+v", len(planParu.Actions), len(wantParu), planParu.Actions)
	}
	for i, want := range wantParu {
		if !slices.Equal(planParu.Actions[i].Command, want) {
			t.Fatalf("arch paru action[%d] = %v, want %v", i, planParu.Actions[i].Command, want)
		}
	}

	rDone := newMockRunner("paru", "pacman", "flatpak", "bash")
	rDone.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\nfd\ngo-yq\nhyprland\nflatpak\nripgrep\n", "", nil
	})
	rDone.on("flatpak remotes --columns=name", func([]string, map[string]string) (string, string, error) {
		return "flathub\n", "", nil
	})
	rDone.on("flatpak list --runtime --columns=application", func([]string, map[string]string) (string, string, error) {
		return "org.gtk.Gtk3theme.adw-gtk3\norg.gtk.Gtk3theme.adw-gtk3-dark\n", "", nil
	})
	idempPlan, err := PlanBootstrapWithRunner(ctx, "arch-desktop", rDone)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch idempotent): %v", err)
	}
	if len(idempPlan.Actions) != 1 || !slices.Equal(idempPlan.Actions[0].Command, []string{"bash", filepath.Join(repoDir, "os", "arch", "postinstall.sh")}) {
		t.Fatalf("expected only OS hook on idempotent arch re-run, got %+v", idempPlan.Actions)
	}

	rNoParu := newMockRunner("pacman", "git", "makepkg", "bash")
	rNoParu.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
		return "bat\njack\nripgrep\n", "", nil
	})
	planNoParu, err := PlanBootstrapWithRunner(ctx, "arch-desktop", rNoParu)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(arch no paru): %v", err)
	}
	wantIDs := []string{
		"bootstrap:paru:deps",
		"bootstrap:paru:build",
		"conflict:remove:pacman:jack",
		"install:paru:batch",
		"flatpak:remote:flathub",
		"install:flatpak:adw-gtk3",
		"os_exec:os/arch/postinstall.sh",
	}
	if len(planNoParu.Actions) != len(wantIDs) {
		t.Fatalf("arch no-paru action count = %d, want %d: %+v", len(planNoParu.Actions), len(wantIDs), planNoParu.Actions)
	}
	for i, id := range wantIDs {
		if planNoParu.Actions[i].ID != id {
			t.Fatalf("arch no-paru action[%d] ID = %q, want %q", i, planNoParu.Actions[i].ID, id)
		}
	}

	rNoAllow := newMockRunner("sudo", "git", "makepkg", "paru", "flatpak", "bash")
	resNoAllow, err := ExecBootstrap(ctx, planNoParu, rNoAllow)
	if err != nil {
		t.Fatalf("ExecBootstrap(no allow): %v", err)
	}
	if !slices.Contains(resNoAllow.MissingAllows, "sudo") || !slices.Contains(resNoAllow.MissingAllows, "remove") {
		t.Fatalf("expected sudo+remove consent skips, got %+v", resNoAllow)
	}
	for _, c := range rNoAllow.calls {
		if c.Cmd[0] == "sudo" || c.Cmd[0] == "git" || c.Cmd[0] == "makepkg" || c.Cmd[0] == "paru" {
			t.Fatalf("unexpected execution without sudo consent: %v", c.Cmd)
		}
	}

	origEUID := currentEUID
	t.Cleanup(func() { currentEUID = origEUID })

	currentEUID = func() int { return 0 }
	ctxAllow := *ctx
	ctxAllow.Allow.Sudo = true
	ctxAllow.Allow.Remove = true
	rRoot := newMockRunner("sudo", "git", "makepkg", "paru", "flatpak", "bash")
	if _, err := ExecBootstrap(&ctxAllow, planNoParu, rRoot); err == nil || !strings.Contains(err.Error(), "never root") {
		t.Fatalf("expected paru bootstrap to refuse root EUID, got err=%v", err)
	}
	for _, c := range rRoot.calls {
		if c.Cmd[0] == "git" || c.Cmd[0] == "makepkg" {
			t.Fatalf("root paru bootstrap must never invoke git/makepkg, saw %v", c.Cmd)
		}
	}

	currentEUID = func() int { return 1000 }
	rRun := newMockRunner("sudo", "git", "makepkg", "paru", "flatpak", "bash")
	resRun, err := ExecBootstrap(&ctxAllow, planNoParu, rRun)
	if err != nil {
		t.Fatalf("ExecBootstrap(paru bootstrap non-root): %v", err)
	}
	if resRun.Applied != len(wantIDs) {
		t.Fatalf("applied = %d, want %d", resRun.Applied, len(wantIDs))
	}
	if len(rRun.calls) != 8 {
		t.Fatalf("expected 8 executed commands (deps, git clone, makepkg, conflict, paru, flathub, themes, hook), got %d: %+v", len(rRun.calls), rRun.calls)
	}
	if !slices.Equal(rRun.calls[0].Cmd, []string{"sudo", "pacman", "-S", "--needed", "--noconfirm", "base-devel", "git"}) {
		t.Fatalf("call[0] = %v", rRun.calls[0].Cmd)
	}
	if len(rRun.calls[1].Cmd) != 5 || !slices.Equal(rRun.calls[1].Cmd[:4], []string{"git", "clone", "--depth=1", paruAURURL}) {
		t.Fatalf("call[1] = %v", rRun.calls[1].Cmd)
	}
	buildDir := rRun.calls[1].Cmd[4]
	if !slices.Equal(rRun.calls[2].Cmd, []string{"makepkg", "-si", "--noconfirm"}) || rRun.calls[2].Dir != buildDir {
		t.Fatalf("call[2] = %v dir=%q, want makepkg -si --noconfirm in %q", rRun.calls[2].Cmd, rRun.calls[2].Dir, buildDir)
	}
	if _, statErr := os.Stat(filepath.Dir(buildDir)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected temp dir %s to be cleaned up, statErr=%v", filepath.Dir(buildDir), statErr)
	}
	if !slices.Equal(rRun.calls[3].Cmd, []string{"sudo", "pacman", "-Rdd", "--noconfirm", "jack"}) {
		t.Fatalf("call[3] = %v", rRun.calls[3].Cmd)
	}
	if !slices.Equal(rRun.calls[4].Cmd, []string{"paru", "-Syu", "--needed", "--noconfirm", "fd", "go-yq", "hyprland", "flatpak"}) {
		t.Fatalf("call[4] = %v", rRun.calls[4].Cmd)
	}
	if !slices.Equal(rRun.calls[5].Cmd, []string{"flatpak", "remote-add", "--if-not-exists", "flathub", flathubRepoURL}) {
		t.Fatalf("call[5] = %v", rRun.calls[5].Cmd)
	}
	if !slices.Equal(rRun.calls[6].Cmd, []string{"flatpak", "install", "-y", "--noninteractive", "flathub", "org.gtk.Gtk3theme.adw-gtk3", "org.gtk.Gtk3theme.adw-gtk3-dark"}) {
		t.Fatalf("call[6] = %v", rRun.calls[6].Cmd)
	}

	if err := validateBootstrapAction(ctx, engine.Action{
		ID:       "bad:paru-url",
		Kind:     engine.ActionInstall,
		Metadata: map[string]string{"provider": "paru-bootstrap", "url": "https://evil.invalid/paru.git"},
	}); err == nil {
		t.Fatalf("expected forged paru-bootstrap URL to be rejected")
	}
	if err := validateBootstrapAction(ctx, engine.Action{
		ID:       "bad:paru-cmd",
		Kind:     engine.ActionInstall,
		Command:  []string{"bash", "-c", "id"},
		Metadata: map[string]string{"provider": "paru-bootstrap", "url": paruAURURL},
	}); err == nil {
		t.Fatalf("expected paru-bootstrap with raw Command to be rejected")
	}
}

func TestBootstrapExactArgvOrderServerAndSkipMissing(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "server.txt"), []byte("bat\nfd\ngum\nripgrep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	presetsRaw, err := os.ReadFile(filepath.Join(repoDir, "presets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	serverPreset := string(presetsRaw) + `
[preset.server]
description = "Generic headless server"
include = ["base"]
links = ["links/server.links"]
packages = ["pkg/server.txt"]
secrets = "none"
`
	if err := os.WriteFile(filepath.Join(repoDir, "presets.toml"), []byte(serverPreset), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBinDir := filepath.Join(t.TempDir(), "sysbin")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	batcatBin := filepath.Join(fakeBinDir, "batcat")
	if err := os.WriteFile(batcatBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctxApt := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctxApt.Host.Headless = true
	ctxApt.Allow.Sudo = true
	rApt := newMockRunner("apt-get", "dpkg-query", "mise")
	rApt.bins["batcat"] = batcatBin
	rApt.on("dpkg-query", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rApt.on("mise", func(cmd []string, _ map[string]string) (string, string, error) {
		if len(cmd) >= 2 && cmd[1] == "which" {
			return "", "", errors.New("not installed")
		}
		return "", "", nil
	})

	aptPlan, err := PlanBootstrapWithRunner(ctxApt, "ubuntu-runner", rApt)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(ubuntu-runner): %v", err)
	}
	wantAptCmds := [][]string{
		{"sudo", "-E", "apt-get", "update", "-qq"},
		{"sudo", "-E", "apt-get", "install", "-y", "bat"},
		nil,
		{"sudo", "-E", "apt-get", "install", "-y", "fd-find"},
		nil,
		{"sudo", "-E", "apt-get", "install", "-y", "gum"},
		{"sudo", "-E", "apt-get", "install", "-y", "ripgrep"},
		{"mise", "use", "-g", "github:mikefarah/yq"},
	}
	if len(aptPlan.Actions) != len(wantAptCmds) {
		t.Fatalf("ubuntu-runner action count = %d, want %d: %+v", len(aptPlan.Actions), len(wantAptCmds), aptPlan.Actions)
	}
	for i, want := range wantAptCmds {
		if want == nil {
			if aptPlan.Actions[i].Kind != engine.ActionSymlink {
				t.Fatalf("ubuntu-runner action[%d] kind = %q, want symlink", i, aptPlan.Actions[i].Kind)
			}
			continue
		}
		if !slices.Equal(aptPlan.Actions[i].Command, want) {
			t.Fatalf("ubuntu-runner action[%d] cmd = %v, want %v", i, aptPlan.Actions[i].Command, want)
		}
	}

	rAptExec := newMockRunner("sudo", "mise")
	rAptExec.bins["batcat"] = batcatBin
	rAptExec.on("sudo -E apt-get install -y gum", func([]string, map[string]string) (string, string, error) {
		return "", "E: Unable to locate package gum", errors.New("exit 100")
	})
	rAptExec.on("sudo -E apt-get install -y fd-find", func([]string, map[string]string) (string, string, error) {
		return "", "E: Unable to locate package fd-find", errors.New("exit 100")
	})
	resApt, err := ExecBootstrap(ctxApt, aptPlan, rAptExec)
	if err != nil {
		t.Fatalf("ExecBootstrap(server skip-missing) must not fail on missing distro package, got err=%v", err)
	}
	if resApt.Skipped != 3 || resApt.Applied != 5 {
		t.Fatalf("expected 5 applied and 3 skipped (gum, fd-find, fd shim), got res=%+v", resApt)
	}
	joinedWarns := strings.Join(resApt.Warnings, "\n")
	if !strings.Contains(joinedWarns, "skipping gum") || !strings.Contains(joinedWarns, "skipping fd-find") {
		t.Fatalf("expected skip-missing warnings for gum and fd-find, got %v", resApt.Warnings)
	}
	if !slices.Contains(resApt.Changed, "install:apt:ripgrep") || !slices.Contains(resApt.Changed, "install:mise:yq") || !slices.Contains(resApt.Changed, "shim:bat:bat") {
		t.Fatalf("expected remaining packages and bat shim to succeed, changed=%v", resApt.Changed)
	}

	rAptUpdateFail := newMockRunner("sudo", "mise")
	rAptUpdateFail.on("sudo -E apt-get update -qq", func([]string, map[string]string) (string, string, error) {
		return "", "network down", errors.New("exit 100")
	})
	if _, err := ExecBootstrap(ctxApt, aptPlan, rAptUpdateFail); err == nil {
		t.Fatalf("expected apt-get update failure to fail bootstrap")
	}

	ctxArchSrv := makeCtx(repoDir, homeDir, "linux", "arch")
	ctxArchSrv.Host.Headless = true
	ctxArchSrv.Allow.Sudo = true
	for _, bins := range [][]string{{"pacman"}, {"pacman", "paru"}} {
		rArchSrv := newMockRunner(bins...)
		rArchSrv.on("pacman -Qq", func([]string, map[string]string) (string, string, error) {
			return "bat\n", "", nil
		})
		archSrvPlan, err := PlanBootstrapWithRunner(ctxArchSrv, "server", rArchSrv)
		if err != nil {
			t.Fatalf("PlanBootstrapWithRunner(arch server bins=%v): %v", bins, err)
		}
		wantArchSrv := [][]string{
			{"sudo", "pacman", "-Syu", "--noconfirm"},
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "fd"},
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "gum"},
			{"sudo", "pacman", "-S", "--needed", "--noconfirm", "ripgrep"},
		}
		if len(archSrvPlan.Actions) != len(wantArchSrv) {
			t.Fatalf("arch server (bins=%v) action count = %d, want %d: %+v", bins, len(archSrvPlan.Actions), len(wantArchSrv), archSrvPlan.Actions)
		}
		for i, want := range wantArchSrv {
			if !slices.Equal(archSrvPlan.Actions[i].Command, want) {
				t.Fatalf("arch server (bins=%v) action[%d] = %v, want %v", bins, i, archSrvPlan.Actions[i].Command, want)
			}
		}

		rArchSrvExec := newMockRunner("sudo")
		rArchSrvExec.on("sudo pacman -S --needed --noconfirm gum", func([]string, map[string]string) (string, string, error) {
			return "", "error: target not found: gum", errors.New("exit 1")
		})
		resArchSrv, err := ExecBootstrap(ctxArchSrv, archSrvPlan, rArchSrvExec)
		if err != nil {
			t.Fatalf("ExecBootstrap(arch server skip-missing): %v", err)
		}
		if resArchSrv.Applied != 3 || resArchSrv.Skipped != 1 {
			t.Fatalf("expected 3 applied and 1 skipped on arch server, got %+v", resArchSrv)
		}
	}
}

func TestRealRepoBootstrapParityPresets(t *testing.T) {
	realRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	presets, err := config.LoadPresets(realRoot)
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	serverPkgs, err := config.ParsePackageList(filepath.Join(realRoot, "pkg", "server.txt"))
	if err != nil {
		t.Fatalf("ParsePackageList(pkg/server.txt): %v", err)
	}
	for _, pid := range []string{"server", "ubuntu-runner", "debian-server"} {
		resolved, err := config.ResolvePreset(realRoot, presets, pid)
		if err != nil {
			t.Fatalf("ResolvePreset(%s): %v", pid, err)
		}
		for _, pkg := range serverPkgs {
			if !slices.Contains(resolved.PackageNames, pkg) {
				t.Errorf("preset %s missing pkg/server.txt entry %q", pid, pkg)
			}
		}
	}

	macResolved, err := config.ResolvePreset(realRoot, presets, "mac-workstation")
	if err != nil {
		t.Fatalf("ResolvePreset(mac-workstation): %v", err)
	}
	wantMacHooks := []string{"os/mac/postinstall.sh", "os/mac/defaults.sh"}
	if !slices.Equal(macResolved.OSHooks, wantMacHooks) {
		t.Fatalf("mac-workstation os_hooks = %v, want %v", macResolved.OSHooks, wantMacHooks)
	}

	homeDir := t.TempDir()
	ctxUbuntu := makeCtx(realRoot, homeDir, "linux", "ubuntu")
	ctxUbuntu.Host.Headless = true
	rUbuntu := newMockRunner("apt-get", "dpkg-query", "mise")
	rUbuntu.on("dpkg-query", func([]string, map[string]string) (string, string, error) {
		return "", "", nil
	})
	rUbuntu.on("mise", func(cmd []string, _ map[string]string) (string, string, error) {
		if len(cmd) >= 2 && cmd[1] == "which" {
			return "", "", errors.New("not installed")
		}
		return "", "", nil
	})
	ubPlan, err := PlanBootstrapWithRunner(ctxUbuntu, "ubuntu-runner", rUbuntu)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(real ubuntu-runner): %v", err)
	}
	foundFDFind := false
	for _, a := range ubPlan.Actions {
		if a.ID == "install:apt:fd" && slices.Equal(a.Command, []string{"sudo", "-E", "apt-get", "install", "-y", "fd-find"}) && a.Metadata["skip_if_missing"] == "true" {
			foundFDFind = true
		}
	}
	if !foundFDFind {
		t.Fatalf("expected fd -> fd-find remap with skip_if_missing on real ubuntu-runner")
	}
}

func TestExecRunnerProbeDisablesMiseAutoInstall(t *testing.T) {
	t.Setenv("MISE_AUTO_INSTALL", "1")
	t.Setenv("MISE_NOT_FOUND_AUTO_INSTALL", "1")
	t.Setenv("MISE_EXEC_AUTO_INSTALL", "1")
	script := `printf '%s|%s|%s' "$MISE_AUTO_INSTALL" "$MISE_NOT_FOUND_AUTO_INSTALL" "$MISE_EXEC_AUTO_INSTALL"`
	out, _, err := ExecRunner{Probe: true}.Run([]string{"sh", "-c", script}, nil, "", false)
	if err != nil || out != "0|0|0" {
		t.Fatalf("probe runner env = %q err=%v, want 0|0|0", out, err)
	}
	out, _, err = ExecRunner{}.Run([]string{"sh", "-c", script}, nil, "", false)
	if err != nil || out != "1|1|1" {
		t.Fatalf("action runner env = %q err=%v, want inherited 1|1|1", out, err)
	}
	out, _, err = TimedExecRunner{}.Run([]string{"sh", "-c", script}, nil, "", false)
	if err != nil || out != "0|0|0" {
		t.Fatalf("timed probe runner env = %q err=%v, want 0|0|0", out, err)
	}
}

func TestExecBootstrapUbuntuWithoutSudoSkipsDependentShims(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctx.Host.Headless = true
	ctx.NonInteractive = true

	r := newMockRunner("apt-get", "dpkg-query", "mise")
	r.on("dpkg-query", func([]string, map[string]string) (string, string, error) {
		return "deinstall ok config-files", "", nil
	})
	r.on("mise", func(cmd []string, _ map[string]string) (string, string, error) {
		if len(cmd) >= 2 && cmd[1] == "which" {
			return "", "", errors.New("not installed")
		}
		return "", "", nil
	})

	plan, err := PlanBootstrapWithRunner(ctx, "ubuntu-runner", r)
	if err != nil {
		t.Fatalf("PlanBootstrapWithRunner(ubuntu-runner) failed: %v", err)
	}
	var shimIDs []string
	for _, a := range plan.Actions {
		if a.Kind == engine.ActionSymlink {
			shimIDs = append(shimIDs, a.ID)
		}
	}
	if !slices.Contains(shimIDs, "shim:fd:fd") || !slices.Contains(shimIDs, "shim:bat:bat") {
		t.Fatalf("expected fd and bat shims planned, got %v", shimIDs)
	}

	res, err := ExecBootstrap(ctx, plan, r)
	if err != nil {
		t.Fatalf("ExecBootstrap without sudo must not hard-error, got %v", err)
	}
	if res.ConsentSkipped == 0 || !slices.Contains(res.MissingAllows, "sudo") {
		t.Fatalf("expected consent skips citing sudo, got skipped=%d missing=%v", res.ConsentSkipped, res.MissingAllows)
	}
	for _, id := range shimIDs {
		if slices.Contains(res.Changed, id) {
			t.Errorf("shim %s must be skipped when its install was skipped, changed=%v", id, res.Changed)
		}
		found := false
		for _, w := range res.Warnings {
			if strings.HasPrefix(w, "skipped "+id+":") && strings.Contains(w, "--allow sudo") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected consent warning for %s, got %v", id, res.Warnings)
		}
	}
	if _, lerr := os.Lstat(filepath.Join(homeDir, ".local", "bin", "fd")); !os.IsNotExist(lerr) {
		t.Errorf("expected no fd shim created, err=%v", lerr)
	}
}

func hookExitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("expected exit %d to fail", code)
	}
	return err
}

func TestHookDeclinedMapsExit75ToConsentSkip(t *testing.T) {
	if hookExitCode(hookExitError(t, HookDeclinedExitCode)) != HookDeclinedExitCode {
		t.Fatalf("expected exit %d to be read back", HookDeclinedExitCode)
	}
	if hookExitCode(errors.New("boom")) != -1 || hookExitCode(nil) != -1 {
		t.Fatalf("expected non-exit errors to read as -1")
	}

	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "arch")
	ctx.Allow.Sudo = true
	hookRel := "os/arch/postinstall.sh"
	hookPath := filepath.Join(repoDir, hookRel)
	plan := &engine.Plan{
		Verb: "bootstrap",
		Actions: []engine.Action{
			{
				ID:            "os_exec:" + hookRel,
				Kind:          engine.ActionOSExec,
				Target:        hookPath,
				Source:        hookRel,
				RequiresAllow: "sudo",
				Command:       []string{"bash", hookPath},
				Metadata:      map[string]string{"hook": hookRel},
			},
		},
	}

	declined := hookExitError(t, HookDeclinedExitCode)
	r := newMockRunner("bash")
	r.on("bash", func([]string, map[string]string) (string, string, error) { return "", "", declined })
	res, err := ExecBootstrap(ctx, plan, r)
	if err != nil {
		t.Fatalf("declined hook must not fail the run, got %v", err)
	}
	if res.Applied != 0 || res.Skipped != 1 || res.ConsentSkipped != 1 || res.Declined != 1 || len(res.MissingAllows) != 0 {
		t.Fatalf("expected one declined consent skip, got %+v", res)
	}
	if !slices.Contains(res.Warnings, "declined "+hookRel+" (rerun with --yes to accept)") {
		t.Fatalf("expected decline warning, got %v", res.Warnings)
	}

	failed := hookExitError(t, 1)
	rFail := newMockRunner("bash")
	rFail.on("bash", func([]string, map[string]string) (string, string, error) { return "", "", failed })
	if _, err := ExecBootstrap(ctx, plan, rFail); err == nil {
		t.Fatalf("expected a non-75 hook exit to fail the run")
	}
}

func osExecPlan(act engine.Action) *engine.Plan {
	act.Kind = engine.ActionOSExec
	act.RequiresAllow = "sudo"
	return &engine.Plan{Verb: "bootstrap", Actions: []engine.Action{act}}
}

func failingRunner(t *testing.T, bin string, code int) *mockRunner {
	t.Helper()
	exitErr := hookExitError(t, code)
	r := newMockRunner(bin)
	r.on(bin, func([]string, map[string]string) (string, string, error) { return "", "", exitErr })
	return r
}

func TestHookUserDeclineIsPlainSkip(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "arch")
	ctx.Allow.Sudo = true
	hookRel := "os/arch/snapshots.sh"
	hookPath := filepath.Join(repoDir, hookRel)
	plan := osExecPlan(engine.Action{ID: "os_exec:" + hookRel, Target: hookPath, Command: []string{"bash", hookPath}, Metadata: map[string]string{"hook": hookRel}})

	res, err := ExecBootstrap(ctx, plan, failingRunner(t, "bash", HookUserDeclinedExitCode))
	if err != nil {
		t.Fatalf("a hook declined at its prompt must not fail the run, got %v", err)
	}
	if res.Applied != 0 || res.Skipped != 1 || res.ConsentSkipped != 0 || res.Declined != 0 || len(res.Changed) != 0 {
		t.Fatalf("expected a plain skip without consent accounting, got %+v", res)
	}
	if !slices.Contains(res.Warnings, "skipped "+hookRel+" (declined at prompt)") {
		t.Fatalf("expected declined-at-prompt warning, got %v", res.Warnings)
	}
}

func TestDeclineExitCodesOnlyApplyToHooks(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctx.Allow.Sudo = true
	script := filepath.Join(repoDir, "os", "linux", "helper.sh")
	cases := []struct {
		name string
		bin  string
		act  engine.Action
	}{
		{"apt_update", "sudo", engine.Action{ID: "apt:update", Target: "apt-get", Command: []string{"sudo", "-E", "apt-get", "update", "-qq"}, Metadata: map[string]string{"provider": "apt"}}},
		{"os_script_without_hook", "bash", engine.Action{ID: "os_exec:helper", Target: script, Command: []string{"bash", script}}},
	}
	for _, tc := range cases {
		for _, code := range []int{HookDeclinedExitCode, HookUserDeclinedExitCode} {
			t.Run(fmt.Sprintf("%s_%d", tc.name, code), func(t *testing.T) {
				res, err := ExecBootstrap(ctx, osExecPlan(tc.act), failingRunner(t, tc.bin, code))
				if err == nil || !strings.Contains(err.Error(), tc.act.ID+" failed") {
					t.Fatalf("exit %d from a non-hook OS exec action must fail the run, got err=%v res=%+v", code, err, res)
				}
				if res.Skipped != 0 || res.ConsentSkipped != 0 || res.Declined != 0 {
					t.Fatalf("exit %d from a non-hook OS exec action must not be skipped, got %+v", code, res)
				}
			})
		}
	}
}

func TestExecUpdateWithoutSudoSkipsDependentShim(t *testing.T) {
	repoDir, homeDir := makeSyntheticRepo(t)
	ctx := makeCtx(repoDir, homeDir, "linux", "ubuntu")
	ctx.NonInteractive = true

	r := newMockRunner("apt-get")
	digest := &UpdateDigest{
		Plan: &engine.Plan{
			Verb:   "update",
			Title:  "Update packages",
			Preset: "ubuntu-runner",
			Actions: []engine.Action{
				{
					ID:            "install:apt:fd",
					Kind:          engine.ActionInstall,
					Target:        "fd-find",
					Summary:       "Install fd-find",
					RequiresAllow: "sudo",
					Command:       []string{"sudo", "apt-get", "install", "-y", "fd-find"},
					Metadata:      map[string]string{"provider": "apt", "tool": "fd", "pkg": "fd-find"},
				},
				{
					ID:       "shim:fd:fd",
					Kind:     engine.ActionSymlink,
					Target:   filepath.Join(homeDir, ".local", "bin", "fd"),
					Source:   "fdfind",
					Summary:  "Create shim fd",
					Metadata: map[string]string{"shim": "fdfind:fd", "tool": "fd"},
				},
			},
		},
	}

	res, err := ExecUpdate(ctx, digest, r)
	if err != nil {
		t.Fatalf("ExecUpdate without sudo must not hard-error, got %v", err)
	}
	if res.ConsentSkipped != 2 || !slices.Contains(res.MissingAllows, "sudo") {
		t.Fatalf("expected install and shim consent-skipped citing sudo, got skipped=%d missing=%v", res.ConsentSkipped, res.MissingAllows)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "skipped shim:fd:fd:") && strings.Contains(w, "--allow sudo") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected consent warning for shim:fd:fd, got %v", res.Warnings)
	}
	if _, lerr := os.Lstat(filepath.Join(homeDir, ".local", "bin", "fd")); !os.IsNotExist(lerr) {
		t.Fatalf("expected no fd shim created, err=%v", lerr)
	}
}
