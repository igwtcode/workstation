package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/envmgr"
	"github.com/igwtcode/workstation/ws/internal/fleet"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

type testFixture struct {
	repo string
	home string
}

func setupFixture(t *testing.T) testFixture {
	t.Helper()
	t.Setenv("WS_ZONE", "")
	t.Setenv("WS_DB_KEY", "")

	repo := filepath.Join(t.TempDir(), "repo")
	home := filepath.Join(t.TempDir(), "home")

	mustWrite := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	mustWrite(filepath.Join(repo, "presets.toml"), `
[preset.base]
description = "Minimal baseline"
links = ["links/common.links"]
packages = ["pkg/server.txt"]
secrets = "none"
tags = ["core", "cli"]

[preset.ubuntu-runner]
description = "Headless Ubuntu runner"
include = ["base"]
exclude = ["desktop", "gui"]
when = { os = "linux", distro = ["ubuntu"], headless = true }
links = ["links/server.links"]
packages = ["pkg/server.txt"]
extra_tools = ["yq"]
secrets = "none"
tags = ["core", "cli", "headless"]
`, 0o644)

	mustWrite(filepath.Join(repo, "tools.toml"), `
[tool.yq]
bin = "yq"
description = "YAML processor"
updates = "manager"

[[tool.yq.install]]
via = "brew"
name = "yq"

[[tool.yq.install]]
via = "mise"
ref = "aqua:mikefarah/yq"
`, 0o644)

	mustWrite(filepath.Join(repo, "pkg", "server.txt"), "git\ncurl\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "common.links"), "config/tmux ~/.config/tmux\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "server.links"), "config/demo/app.conf ~/.config/demo/app.conf\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "demo", "app.conf"), "enabled = true\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "tmux", "colors-template.conf"), "set -g status-bg '{{bg}}'\nset -g status-fg '{{accent}}'\n", 0o644)
	mustWrite(filepath.Join(repo, "themes", "registry"), "config/tmux/colors-template.conf config/tmux/colors.conf -\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "tmux", "plugins", "tpm", "tpm"), "", 0o755)
	mustWrite(filepath.Join(home, ".local", "share", "gh", "extensions", "gh-dash", "gh-dash"), "", 0o755)
	mustWrite(filepath.Join(home, "Library", "Application Support", "go", "telemetry", "mode"), "off", 0o644)
	mustWrite(filepath.Join(home, ".config", "go", "telemetry", "mode"), "off", 0o644)

	paletteTOML := func(name, mode, bg, accent string) string {
		return `name = "` + name + `"
mode = "` + mode + `"
bg = "` + bg + `"
bg_alt = "#3c3836"
fg = "#ebdbb2"
fg_dim = "#a89984"
accent = "` + accent + `"
selection_bg = "#504945"
selection_fg = "#fbf1c7"
cursor = "#ebdbb2"
border = "#665c54"
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
	}
	mustWrite(filepath.Join(repo, "themes", "gruvbox", "dark.toml"), paletteTOML("Gruvbox Dark", "dark", "#282828", "#fe8019"), 0o644)
	mustWrite(filepath.Join(repo, "themes", "catppuccin", "dark.toml"), paletteTOML("Catppuccin Mocha", "dark", "#1e1e2e", "#cba6f7"), 0o644)

	mustWrite(filepath.Join(repo, ".mise", "tasks", "local", "hello"), "#!/usr/bin/env bash\nprintf '%s|%s|%s\n' \"$WS_REPO\" \"$WS_THEME\" \"$WS_THEME_ACCENT\"\n", 0o755)

	mustWrite(filepath.Join(home, ".cache", "bat", "cache.bin"), strings.Repeat("x", 1024), 0o644)
	mustWrite(filepath.Join(home, ".zsh_history"), ": 1700000100:2;git status\n: 1700000200:1;ws doctor --json\n", 0o644)
	mustWrite(filepath.Join(home, ".bash_history"), "#1700000300\necho hello world\n", 0o644)
	mustWrite(filepath.Join(home, "work", "mise.toml"), "[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\n", 0o600)

	return testFixture{repo: repo, home: home}
}

func execWS(t *testing.T, fx testFixture, args ...string) (string, error) {
	t.Helper()
	return execWSWithStdin(t, fx, "", args...)
}

func execWSWithStdin(t *testing.T, fx testFixture, stdin string, args ...string) (string, error) {
	t.Helper()
	opts := &GlobalOpts{
		Repo:    fx.repo,
		Home:    fx.home,
		Preset:  "ubuntu-runner",
		Version: "test",
	}
	cmd := NewRootCmd(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDoctorAndBareNonTTY(t *testing.T) {
	fx := setupFixture(t)

	out, err := execWS(t, fx, "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor --json failed: %v\n%s", err, out)
	}
	var rep engine.DoctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal doctor JSON: %v\n%s", err, out)
	}
	if rep.Preset != "ubuntu-runner" {
		t.Fatalf("expected preset ubuntu-runner, got %q", rep.Preset)
	}
	if rep.TemplatesTotal != 1 || rep.LinksTotal != 2 {
		t.Fatalf("unexpected totals: templates=%d links=%d", rep.TemplatesTotal, rep.LinksTotal)
	}

	bareOut, err := execWS(t, fx)
	if err != nil {
		t.Fatalf("bare ws non-TTY failed: %v\n%s", err, bareOut)
	}
	for _, want := range []string{"Workstation", "Workflow", "Secrets & State", "Packages & Fleet"} {
		if !strings.Contains(bareOut, want) {
			t.Fatalf("expected bare non-TTY ws to print help containing %q, got:\n%s", want, bareOut)
		}
	}
}

func TestPresetAndThemeCommands(t *testing.T) {
	fx := setupFixture(t)

	listOut, err := execWS(t, fx, "preset", "list")
	if err != nil {
		t.Fatalf("preset list failed: %v", err)
	}
	if !strings.Contains(listOut, "ubuntu-runner") || !strings.Contains(listOut, "base") {
		t.Fatalf("unexpected preset list output:\n%s", listOut)
	}

	showOut, err := execWS(t, fx, "preset", "show", "ubuntu-runner")
	if err != nil {
		t.Fatalf("preset show failed: %v", err)
	}
	if !strings.Contains(showOut, "base -> ubuntu-runner") {
		t.Fatalf("expected inheritance chain in preset show:\n%s", showOut)
	}

	if _, err := execWS(t, fx, "preset", "use", "base"); err != nil {
		t.Fatalf("preset use failed: %v", err)
	}
	ledger, err := app.LoadLedger(filepath.Join(fx.home, ".ws"))
	if err != nil || ledger.ActivePreset != "base" {
		t.Fatalf("expected ledger activePreset=base, got %q (err=%v)", ledger.ActivePreset, err)
	}

	themeOut, err := execWS(t, fx, "theme", "--list")
	if err != nil {
		t.Fatalf("theme --list failed: %v", err)
	}
	if !strings.Contains(themeOut, "gruvbox-dark") || !strings.Contains(themeOut, "catppuccin-dark") {
		t.Fatalf("unexpected theme --list output:\n%s", themeOut)
	}
}

func TestApplyIdempotencySavePlanAndUndo(t *testing.T) {
	fx := setupFixture(t)

	dryOut, err := execWS(t, fx, "apply", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("apply --dry-run failed: %v\n%s", err, dryOut)
	}
	var dryPayload applyJSONOutput
	if err := json.Unmarshal([]byte(dryOut), &dryPayload); err != nil {
		t.Fatalf("unmarshal dry-run apply JSON: %v", err)
	}
	if len(dryPayload.Actions) == 0 {
		t.Fatalf("expected planned actions on first apply --dry-run")
	}

	planPath := filepath.Join(fx.home, "saved-plan.json")
	if _, err := execWS(t, fx, "apply", "--save-plan", planPath); err != nil {
		t.Fatalf("apply --save-plan failed: %v", err)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("saved plan missing: %v", err)
	}

	if out, err := execWS(t, fx, "apply", "--plan", planPath); err != nil {
		t.Fatalf("apply --plan failed: %v\n%s", err, out)
	}

	secondDryOut, err := execWS(t, fx, "apply", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("2nd apply --dry-run failed: %v\n%s", err, secondDryOut)
	}
	var secondPayload applyJSONOutput
	if err := json.Unmarshal([]byte(secondDryOut), &secondPayload); err != nil {
		t.Fatalf("unmarshal 2nd apply JSON: %v", err)
	}
	if len(secondPayload.Actions) != 0 {
		t.Fatalf("expected 0 actions on 2nd apply (idempotency), got %d:\n%s", len(secondPayload.Actions), secondDryOut)
	}

	if out, err := execWS(t, fx, "theme", "catppuccin-dark", "--non-interactive"); err != nil {
		t.Fatalf("theme catppuccin-dark failed: %v\n%s", err, out)
	}
	rendered, err := os.ReadFile(filepath.Join(fx.repo, "config", "tmux", "colors.conf"))
	if err != nil || !strings.Contains(string(rendered), "#1e1e2e") {
		t.Fatalf("expected catppuccin-dark rendered output, got %q (err=%v)", string(rendered), err)
	}

	undoOut, err := execWS(t, fx, "undo")
	if err != nil {
		t.Fatalf("undo failed: %v\n%s", err, undoOut)
	}
	if !strings.Contains(undoOut, "undid") {
		t.Fatalf("expected undo summary, got %q", undoOut)
	}
	reverted, err := os.ReadFile(filepath.Join(fx.repo, "config", "tmux", "colors.conf"))
	if err != nil || !strings.Contains(string(reverted), "#282828") {
		t.Fatalf("expected gruvbox-dark restored after undo, got %q (err=%v)", string(reverted), err)
	}
}

func TestCleanSafetyAndDryRun(t *testing.T) {
	fx := setupFixture(t)
	batCache := filepath.Join(fx.home, ".cache", "bat", "cache.bin")

	bareOut, err := execWS(t, fx, "clean", "--json")
	if err != nil {
		t.Fatalf("bare clean --json failed: %v\n%s", err, bareOut)
	}
	var barePayload struct {
		DryRun bool `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(bareOut), &barePayload); err != nil {
		t.Fatalf("unmarshal bare clean JSON: %v\n%s", err, bareOut)
	}
	if !barePayload.DryRun {
		t.Fatalf("expected bare ws clean to default to dry-run, got:\n%s", bareOut)
	}
	if !strings.Contains(bareOut, "safe:cli-bat") {
		t.Fatalf("expected safe:cli-bat candidate in bare clean:\n%s", bareOut)
	}
	if _, err := os.Stat(batCache); err != nil {
		t.Fatalf("bare ws clean mutated cache file without --execute: %v", err)
	}

	out, err := execWS(t, fx, "clean", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("clean --dry-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "safe:cli-bat") {
		t.Fatalf("expected safe:cli-bat candidate in clean --dry-run:\n%s", out)
	}
	if _, err := os.Stat(batCache); err != nil {
		t.Fatalf("clean --dry-run mutated cache file: %v", err)
	}

	execOut, err := execWS(t, fx, "clean", "--execute", "--tier", "safe", "--non-interactive", "--json")
	if err != nil {
		t.Fatalf("clean --execute --tier safe failed: %v\n%s", err, execOut)
	}
	if _, err := os.Stat(batCache); !os.IsNotExist(err) {
		t.Fatalf("expected clean --execute to remove %s, stat err=%v", batCache, err)
	}

	avdFile := filepath.Join(fx.home, ".android", "avd", "pixel.img")
	if err := os.MkdirAll(filepath.Dir(avdFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avdFile, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := execWS(t, fx, "clean", "--execute", "--tier", "dangerous", "--non-interactive", "--allow", "remove"); err == nil {
		t.Fatalf("expected dangerous clean tier to be blocked under --non-interactive")
	}
}

func TestWhoamiGenScheduleOSEnvInsightsAndPlugins(t *testing.T) {
	fx := setupFixture(t)

	whoamiOut, err := execWS(t, fx, "whoami", "--json")
	if err != nil {
		t.Fatalf("whoami --json failed: %v", err)
	}
	var whoamiMap map[string]any
	if err := json.Unmarshal([]byte(whoamiOut), &whoamiMap); err != nil {
		t.Fatalf("unmarshal whoami JSON: %v", err)
	}

	for _, kind := range []string{"hex", "base64", "uuid"} {
		out, err := execWS(t, fx, "gen", kind)
		if err != nil {
			t.Fatalf("gen %s failed: %v", kind, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("gen %s returned empty output", kind)
		}
	}

	schedOut, err := execWS(t, fx, "schedule", "print")
	if err != nil {
		t.Fatalf("schedule print failed: %v", err)
	}
	hasDigestCmd := strings.Contains(schedOut, "update --digest --json") ||
		(strings.Contains(schedOut, "<string>update</string>") &&
			strings.Contains(schedOut, "<string>--digest</string>") &&
			strings.Contains(schedOut, "<string>--json</string>"))
	if !hasDigestCmd {
		t.Fatalf("schedule print missing digest-only command:\n%s", schedOut)
	}

	osEnvOut, err := execWS(t, fx, "os-env", "print")
	if err != nil {
		t.Fatalf("os-env print failed: %v", err)
	}
	if !strings.Contains(osEnvOut, "XDG_CONFIG_HOME") {
		t.Fatalf("os-env print missing XDG_CONFIG_HOME:\n%s", osEnvOut)
	}

	editorScript := filepath.Join(t.TempDir(), "fake-editor.sh")
	if err := os.WriteFile(editorScript, []byte("#!/usr/bin/env bash\nprintf '# edited\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editorScript)
	editOut, err := execWS(t, fx, "edit", "tmux")
	if err != nil {
		t.Fatalf("edit tmux failed: %v\n%s", err, editOut)
	}
	if !strings.Contains(editOut, "+# edited") {
		t.Fatalf("expected unified diff in edit output:\n%s", editOut)
	}

	histOut, err := execWS(t, fx, "hist", "search", "doctor")
	if err != nil {
		t.Fatalf("hist search failed: %v", err)
	}
	if !strings.Contains(histOut, "ws doctor --json") {
		t.Fatalf("expected matching history command:\n%s", histOut)
	}

	runOut, err := execWS(t, fx, "run", "--zone", "work", "--dry-run", "--", "env")
	if err != nil {
		t.Fatalf("run --zone --dry-run failed: %v", err)
	}
	if !strings.Contains(runOut, "zone=work") {
		t.Fatalf("unexpected run output: %s", runOut)
	}

	pluginListOut, err := execWS(t, fx, "plugin", "list")
	if err != nil {
		t.Fatalf("plugin list failed: %v", err)
	}
	if !strings.Contains(pluginListOut, "hello") {
		t.Fatalf("expected local plugin hello in plugin list:\n%s", pluginListOut)
	}

	pluginRunOut, err := execWS(t, fx, "hello")
	if err != nil {
		t.Fatalf("plugin dispatch failed: %v\n%s", err, pluginRunOut)
	}
	if !strings.Contains(pluginRunOut, fx.repo+"|gruvbox-dark|#fe8019") {
		t.Fatalf("unexpected plugin env output: %q", pluginRunOut)
	}

	vmDryOut, err := execWS(t, fx, "test", "--in", "ubuntu", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("test --in ubuntu --dry-run --json failed: %v", err)
	}
	if !strings.Contains(vmDryOut, `"preset": "ubuntu-runner"`) {
		t.Fatalf("unexpected test dry-run JSON:\n%s", vmDryOut)
	}
}

func TestTUIBackendSmoke(t *testing.T) {
	fx := setupFixture(t)

	stubBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubBin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubBin, "tailscale"), []byte("#!/bin/sh\nprintf '{\"Self\":{\"HostName\":\"local-dev\",\"TailscaleIPs\":[\"100.64.0.1\"],\"OS\":\"linux\",\"Arch\":\"arm64\",\"Online\":true}}\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
	})
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	b := NewTUIBackend(ctx)
	defer func() { _ = b.Close() }()

	badge, err := b.HostSummary()
	if err != nil || badge.Preset != "ubuntu-runner" {
		t.Fatalf("HostSummary: badge=%+v err=%v", badge, err)
	}

	if err := b.ScanEnv(); err != nil {
		t.Fatalf("ScanEnv: %v", err)
	}
	envVars, err := b.LoadEnvVars("", "")
	if err != nil || len(envVars) == 0 {
		t.Fatalf("LoadEnvVars: %+v err=%v", envVars, err)
	}
	detail, err := b.LoadEnvDetail("WS_ZONE", "")
	if err != nil || detail.VarKey != "WS_ZONE" {
		t.Fatalf("LoadEnvDetail: %+v err=%v", detail, err)
	}

	if _, err := b.LoadRepos(true, ""); err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if _, err := b.LoadGHItems(false, ""); err != nil {
		t.Fatalf("LoadGHItems: %v", err)
	}

	nodes, err := b.LoadFleetNodes()
	if err != nil || len(nodes) == 0 {
		t.Fatalf("LoadFleetNodes: %+v err=%v", nodes, err)
	}
	if joinCmd, err := b.TailscaleJoinCmd("dev-box", []string{"tag:dev"}); err != nil || !strings.Contains(joinCmd, "tailscale up") {
		t.Fatalf("TailscaleJoinCmd: %q err=%v", joinCmd, err)
	}
	if prev, err := b.PreviewFleetSync(tui.FleetSyncReq{Node: "dev-box", Preset: "ubuntu-runner", Arch: "arm64", NoZones: true}); err != nil || len(prev.Steps) == 0 {
		t.Fatalf("PreviewFleetSync: %+v err=%v", prev, err)
	}

	dryCtx := *b.ctx
	dryCtx.DryRun = true
	dryB := NewTUIBackend(&dryCtx)
	defer func() { _ = dryB.Close() }()
	if msg, err := dryB.ExecFleetSync(tui.FleetSyncReq{Node: "dev-box", Preset: "ubuntu-runner", Arch: "arm64", DryRun: false}); err != nil || !strings.Contains(msg, "[dry-run]") {
		t.Fatalf("expected global DryRun to hold in ExecFleetSync, got msg=%q err=%v", msg, err)
	}
	if _, err := dryB.RunCleanup(nil, false); err != nil {
		t.Fatalf("dryB RunCleanup with global DryRun failed: %v", err)
	}

	presets, err := b.ListPresets()
	if err != nil || len(presets) < 2 {
		t.Fatalf("ListPresets: len=%d err=%v", len(presets), err)
	}
	tools, err := b.ResolvePreset("ubuntu-runner")
	if err != nil || len(tools) == 0 {
		t.Fatalf("ResolvePreset: len=%d err=%v", len(tools), err)
	}
	themes, err := b.ListThemes()
	if err != nil || len(themes) != 2 {
		t.Fatalf("ListThemes: %v err=%v", themes, err)
	}
	pal, err := b.LoadPalette("catppuccin-dark")
	if err != nil || pal.Color("bg", "") != "#1e1e2e" {
		t.Fatalf("LoadPalette: %+v err=%v", pal, err)
	}
	if _, err := b.ApplyTheme("catppuccin-dark"); err != nil {
		t.Fatalf("ApplyTheme: %v", err)
	}
	active, err := b.ActiveTheme()
	if err != nil || active != "catppuccin-dark" {
		t.Fatalf("ActiveTheme: %q err=%v", active, err)
	}
	tpls, err := b.TemplateStatuses()
	if err != nil || len(tpls) != 1 || tpls[0].Status != "ok" {
		t.Fatalf("TemplateStatuses: %+v err=%v", tpls, err)
	}
	t.Setenv("EDITOR", "true")
	edCmd, err := b.EditAppCmd("tmux")
	if err != nil || edCmd == nil || len(edCmd.Args) < 2 {
		t.Fatalf("EditAppCmd: cmd=%v err=%v", edCmd, err)
	}
	cleanItems, err := b.ScanCleanup()
	if err != nil || len(cleanItems) == 0 {
		t.Fatalf("ScanCleanup: %+v err=%v", cleanItems, err)
	}
	if execCmd, err := b.CleanupExecCmd([]string{cleanItems[0].ID}); err != nil || execCmd != nil {
		t.Fatalf("CleanupExecCmd non-sudo: cmd=%v err=%v", execCmd, err)
	}
	cleanRes, err := b.RunCleanup([]string{cleanItems[0].ID}, true)
	if err != nil || cleanRes == nil {
		t.Fatalf("RunCleanup dry-run: res=%+v err=%v", cleanRes, err)
	}

	doc, err := b.LoadDoctor()
	if err != nil || doc.TemplatesTotal != 1 {
		t.Fatalf("LoadDoctor: doc=%+v err=%v", doc, err)
	}
	dbSt, err := b.LoadDBStatus()
	if err != nil || dbSt.SchemaVersion != store.SchemaVersion || dbSt.HasKey {
		t.Fatalf("LoadDBStatus (no key): %+v err=%v", dbSt, err)
	}
	if _, err := b.UndoLast(); err != nil {
		t.Fatalf("UndoLast: %v", err)
	}
}

func TestBuildContextNonTTYAutoNonInteractive(t *testing.T) {
	fx := setupFixture(t)

	defaultOpts := &GlobalOpts{Repo: fx.repo, Home: fx.home, Preset: "ubuntu-runner"}
	ctx, err := defaultOpts.buildContext()
	if err != nil {
		t.Fatalf("buildContext: %v", err)
	}
	if !isCharDevice(os.Stdin) && !ctx.NonInteractive {
		t.Fatalf("expected non-TTY stdin to default NonInteractive=true")
	}

	forcedOpts := &GlobalOpts{Repo: fx.repo, Home: fx.home, Preset: "ubuntu-runner", ForceInteractive: true}
	forcedCtx, err := forcedOpts.buildContext()
	if err != nil {
		t.Fatalf("buildContext ForceInteractive: %v", err)
	}
	if forcedCtx.NonInteractive {
		t.Fatalf("expected ForceInteractive=true to keep NonInteractive=false")
	}
}

func TestUpdateDigestAtomicOutput(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("PATH", t.TempDir())

	outPath := filepath.Join(fx.home, ".ws", "cache", "digest.json")
	for run := 1; run <= 2; run++ {
		stdout, err := execWS(t, fx, "update", "--digest", "--json", "--output", outPath)
		if err != nil {
			t.Fatalf("run %d: update --digest --json --output failed: %v\n%s", run, err, stdout)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("run %d: expected empty stdout when --output is set, got %q", run, stdout)
		}
		raw, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("run %d: read output file: %v", run, err)
		}
		var digest pkgmgr.UpdateDigest
		if err := json.Unmarshal(raw, &digest); err != nil {
			t.Fatalf("run %d: output file is not valid single JSON object: %v\n%s", run, err, string(raw))
		}
		if digest.Preset != "ubuntu-runner" {
			t.Fatalf("run %d: expected preset ubuntu-runner, got %q", run, digest.Preset)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(outPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".ws-out-") {
			t.Fatalf("leftover temp file after atomic write: %s", e.Name())
		}
	}
}

func TestMacCaskAdjustmentAndDigestRecovery(t *testing.T) {
	fx := setupFixture(t)

	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "mac-brew.txt"), []byte("ripgrep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "mac-cask.txt"), []byte("ghostty\nfirefox\nfont-geist-mono-nerd-font\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolved := &config.ResolvedPreset{
		ID:           "mac-workstation",
		PackageFiles: []string{"pkg/mac-brew.txt", "pkg/mac-cask.txt"},
		PackageNames: []string{"ripgrep", "ghostty", "firefox", "font-geist-mono-nerd-font"},
	}
	macHost := app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64"}
	rawTools := config.ResolveTools(resolved, map[string]config.ToolSpec{}, macHost)
	adjusted := pkgmgr.RefineToolsForPreset(&app.Context{RepoRoot: fx.repo, Host: macHost}, resolved, rawTools, nil)

	gotVia := map[string]string{}
	for _, tl := range adjusted {
		gotVia[tl.ID] = tl.Spec.Via
	}
	for _, caskName := range []string{"ghostty", "firefox", "font-geist-mono-nerd-font"} {
		if gotVia[caskName] != "cask" {
			t.Fatalf("expected %s to use cask provider on macOS, got %q", caskName, gotVia[caskName])
		}
	}
	if gotVia["ripgrep"] != "brew" {
		t.Fatalf("expected formula ripgrep to keep brew provider, got %q", gotVia["ripgrep"])
	}

	wsHome := filepath.Join(fx.home, ".ws")
	if d, warn := loadDigest(wsHome); d != nil || warn != "" {
		t.Fatalf("expected nil/empty for missing digest, got d=%+v warn=%q", d, warn)
	}

	digestPath := filepath.Join(wsHome, "cache", "digest.json")
	if err := os.MkdirAll(filepath.Dir(digestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	rec1 := `{"preset":"ubuntu-runner","items":[{"name":"a","provider":"brew","risk":"safe"}]}` + "\n"
	rec2 := `{"preset":"ubuntu-runner","items":[{"name":"a","provider":"brew","risk":"safe"},{"name":"b","provider":"brew","risk":"safe"}]}` + "\n"
	if err := os.WriteFile(digestPath, []byte(rec1+rec2), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, warn := loadDigest(wsHome); d == nil || len(d.Items) != 2 || warn == "" {
		t.Fatalf("expected multi-record recovery with warning, got d=%+v warn=%q", d, warn)
	}

	if err := os.WriteFile(digestPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, warn := loadDigest(wsHome); d != nil || warn == "" {
		t.Fatalf("expected warning for corrupt digest.json, got d=%+v warn=%q", d, warn)
	}

	ctx, err := app.NewContext(app.Options{RepoRoot: fx.repo, HomeDir: fx.home, PresetOverride: "ubuntu-runner", NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	b := NewTUIBackend(ctx)
	defer func() { _ = b.Close() }()
	doc, err := b.LoadDoctor()
	if err != nil {
		t.Fatal(err)
	}
	foundDigestWarn := false
	for _, f := range doc.Findings {
		if f.ID == "digest:corrupt" {
			foundDigestWarn = true
			break
		}
	}
	if !foundDigestWarn {
		t.Fatalf("expected Doctor to surface digest:corrupt warning finding, got %+v", doc.Findings)
	}
}

func TestFreshCloneApplyDryRun(t *testing.T) {
	fx := setupFixture(t)

	starshipTmpl := filepath.Join(fx.repo, "config", "starship", "config-template.toml")
	if err := os.MkdirAll(filepath.Dir(starshipTmpl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(starshipTmpl, []byte("palette = '{{name}}'\nformat = '{{accent}}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	regPath := filepath.Join(fx.repo, "themes", "registry")
	regRaw, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	regRaw = append(regRaw, []byte("config/starship/config-template.toml config/starship/config.toml -\n")...)
	if err := os.WriteFile(regPath, regRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	linksPath := filepath.Join(fx.repo, "links", "common.links")
	linksRaw, err := os.ReadFile(linksPath)
	if err != nil {
		t.Fatal(err)
	}
	linksRaw = append(linksRaw, []byte("config/starship ~/.config/starship\n")...)
	if err := os.WriteFile(linksPath, linksRaw, 0o644); err != nil {
		t.Fatal(err)
	}

	tmuxOut := filepath.Join(fx.repo, "config", "tmux", "colors.conf")
	starshipOut := filepath.Join(fx.repo, "config", "starship", "config.toml")
	for _, p := range []string{tmuxOut, starshipOut, filepath.Join(fx.home, ".config", "tmux"), filepath.Join(fx.home, ".config", "starship")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("expected fresh clone path %s to be absent initially", p)
		}
	}

	dryOut, err := execWS(t, fx, "apply", "--dry-run", "--non-interactive", "--json")
	if err != nil {
		t.Fatalf("fresh-clone apply --dry-run failed: %v\n%s", err, dryOut)
	}
	var payload applyJSONOutput
	if err := json.Unmarshal([]byte(dryOut), &payload); err != nil {
		t.Fatalf("unmarshal fresh-clone apply JSON: %v\n%s", err, dryOut)
	}
	renderCount, linkCount := 0, 0
	for _, act := range payload.Actions {
		switch act.Kind {
		case engine.ActionRender:
			renderCount++
		case engine.ActionSymlink:
			linkCount++
		}
	}
	if renderCount != 2 || linkCount != 3 {
		t.Fatalf("expected 2 renders and 3 symlinks on fresh-clone dry-run, got renders=%d links=%d", renderCount, linkCount)
	}
	for _, p := range []string{tmuxOut, starshipOut, filepath.Join(fx.home, ".config", "tmux"), filepath.Join(fx.home, ".config", "starship")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("fresh-clone dry-run mutated %s", p)
		}
	}

	if out, err := execWS(t, fx, "apply", "--non-interactive"); err != nil {
		t.Fatalf("fresh-clone apply failed: %v\n%s", err, out)
	}
	reDryOut, err := execWS(t, fx, "apply", "--dry-run", "--non-interactive", "--json")
	if err != nil {
		t.Fatalf("2nd apply --dry-run failed: %v\n%s", err, reDryOut)
	}
	var secondPayload applyJSONOutput
	if err := json.Unmarshal([]byte(reDryOut), &secondPayload); err != nil {
		t.Fatalf("unmarshal 2nd apply JSON: %v", err)
	}
	if len(secondPayload.Actions) != 0 {
		t.Fatalf("expected 0 actions after fresh-clone apply, got %d", len(secondPayload.Actions))
	}
}

func TestDBKeyStatusBackupDiffGatedAndRestore(t *testing.T) {
	fx := setupFixture(t)

	envFile := filepath.Join(fx.home, "work", "code", "svc", ".env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	origEnv := "# project secret\nSERVICE_TOKEN=sk-backup-roundtrip-123456\n"
	if err := os.WriteFile(envFile, []byte(origEnv), 0o600); err != nil {
		t.Fatal(err)
	}

	initOut, err := execWS(t, fx, "db", "key", "init", "--json")
	if err != nil {
		t.Fatalf("db key init failed: %v\n%s", err, initOut)
	}
	var keyResp struct {
		HasKey bool            `json:"hasKey"`
		Source store.KeySource `json:"source"`
	}
	if err := json.Unmarshal([]byte(initOut), &keyResp); err != nil || !keyResp.HasKey {
		t.Fatalf("unexpected db key init JSON: %s (err=%v)", initOut, err)
	}

	if out, err := execWS(t, fx, "env", "scan", "--json"); err != nil {
		t.Fatalf("env scan failed: %v\n%s", err, out)
	}

	statusOut, err := execWS(t, fx, "db", "status", "--json")
	if err != nil {
		t.Fatalf("db status --json failed: %v\n%s", err, statusOut)
	}
	var st dbStatusOutput
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil {
		t.Fatalf("unmarshal db status: %v\n%s", err, statusOut)
	}
	if !st.HasKey || !st.DirtySinceBackup || st.TableCounts["secret_blobs"] == 0 {
		t.Fatalf("unexpected db status before backup: %+v", st)
	}

	firstOut, err := execWS(t, fx, "db", "backup", "--if-changed", "--json")
	if err != nil {
		t.Fatalf("1st db backup failed: %v\n%s", err, firstOut)
	}
	var bk1 store.BackupResult
	if err := json.Unmarshal([]byte(firstOut), &bk1); err != nil {
		t.Fatalf("unmarshal 1st backup: %v\n%s", err, firstOut)
	}
	if !bk1.Changed || bk1.Skipped || bk1.LocalPath == "" {
		t.Fatalf("expected 1st backup changed=true skipped=false, got %+v", bk1)
	}

	secondOut, err := execWS(t, fx, "db", "backup", "--if-changed", "--json")
	if err != nil {
		t.Fatalf("2nd db backup failed: %v\n%s", err, secondOut)
	}
	var bk2 store.BackupResult
	if err := json.Unmarshal([]byte(secondOut), &bk2); err != nil {
		t.Fatalf("unmarshal 2nd backup: %v\n%s", err, secondOut)
	}
	if bk2.Changed || !bk2.Skipped {
		t.Fatalf("expected 2nd backup unchanged (changed=false skipped=true), got %+v", bk2)
	}

	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	restorePreviewOut, err := execWS(t, fx, "db", "restore", "--file", bk1.LocalPath, "--write-files", "--json")
	if err != nil {
		t.Fatalf("db restore preview failed: %v\n%s", err, restorePreviewOut)
	}
	var restPreview store.RestoreResult
	if err := json.Unmarshal([]byte(restorePreviewOut), &restPreview); err != nil || !restPreview.DryRun {
		t.Fatalf("expected dry-run db restore preview by default, got err=%v out=%s", err, restorePreviewOut)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("expected bare db restore not to write files before --execute, stat err=%v", err)
	}
	restoreOut, err := execWS(t, fx, "db", "restore", "--file", bk1.LocalPath, "--write-files", "--execute", "--json")
	if err != nil {
		t.Fatalf("db restore --execute failed: %v\n%s", err, restoreOut)
	}
	var rest store.RestoreResult
	if err := json.Unmarshal([]byte(restoreOut), &rest); err != nil {
		t.Fatalf("unmarshal restore: %v\n%s", err, restoreOut)
	}
	if len(rest.FilesWritten) == 0 {
		t.Fatalf("expected restored env file in FilesWritten, got %+v", rest)
	}
	gotEnv, err := os.ReadFile(envFile)
	if err != nil || string(gotEnv) != origEnv {
		t.Fatalf("restored .env mismatch: got %q err=%v", string(gotEnv), err)
	}
	fi, err := os.Stat(envFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected restored .env perm 0600, got %04o err=%v", fi.Mode().Perm(), err)
	}
}

func TestEnvScanProvenanceImpactRotateAndUndo(t *testing.T) {
	fx := setupFixture(t)

	if out, err := execWS(t, fx, "db", "key", "init"); err != nil {
		t.Fatalf("db key init: %v\n%s", err, out)
	}

	zshenv := filepath.Join(fx.home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("export API_TOKEN=\"sk-global-default-0000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envA := filepath.Join(fx.home, "work", "code", "svc-a", ".env")
	envB := filepath.Join(fx.home, "work", "code", "svc-b", ".env")
	for p, body := range map[string]string{
		envA: "# keep svc-a comment\nAPI_TOKEN=sk-shared-secret-111111\nOTHER_VAR=keep\n",
		envB: "# keep svc-b comment\nAPI_TOKEN=sk-shared-secret-111111\n",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scanOut, err := execWS(t, fx, "env", "scan", "--json")
	if err != nil {
		t.Fatalf("env scan failed: %v\n%s", err, scanOut)
	}
	var scanRes envmgr.ScanResult
	if err := json.Unmarshal([]byte(scanOut), &scanRes); err != nil || scanRes.EntriesCount < 3 {
		t.Fatalf("unexpected scan result: %s (err=%v)", scanOut, err)
	}

	provOut, err := execWS(t, fx, "env", "provenance", "API_TOKEN", "--dir", filepath.Dir(envA), "--json")
	if err != nil {
		t.Fatalf("env provenance failed: %v\n%s", err, provOut)
	}
	var prov envmgr.ProvenanceReport
	if err := json.Unmarshal([]byte(provOut), &prov); err != nil {
		t.Fatalf("unmarshal provenance: %v\n%s", err, provOut)
	}
	if len(prov.Layers) < 2 || prov.Fingerprint == "" {
		t.Fatalf("expected >=2 precedence layers and fingerprint, got %+v", prov)
	}

	impOut, err := execWS(t, fx, "env", "impact", prov.Fingerprint, "--json")
	if err != nil {
		t.Fatalf("env impact failed: %v\n%s", err, impOut)
	}
	var impacts []envmgr.EnvEntry
	if err := json.Unmarshal([]byte(impOut), &impacts); err != nil || len(impacts) != 2 {
		t.Fatalf("expected 2 impact matches for shared secret fingerprint, got len=%d out=%s err=%v", len(impacts), impOut, err)
	}

	dryRotOut, err := execWSWithStdin(t, fx, "sk-new-secret-999999\n", "env", "rotate", "API_TOKEN", "--value-stdin", "--json")
	if err != nil {
		t.Fatalf("dry-run rotate failed: %v\n%s", err, dryRotOut)
	}
	rawA, _ := os.ReadFile(envA)
	if !strings.Contains(string(rawA), "sk-shared-secret-111111") {
		t.Fatalf("dry-run rotate mutated %s: %s", envA, string(rawA))
	}

	if _, err := execWSWithStdin(t, fx, "sk-new-secret-999999\n", "env", "rotate", "API_TOKEN", "--value-stdin", "--execute", "--json"); err == nil {
		t.Fatalf("expected non-interactive env rotate --execute without --allow replace to fail closed")
	}

	rotOut, err := execWSWithStdin(t, fx, "sk-new-secret-999999\n", "env", "rotate", "API_TOKEN", "--value-stdin", "--execute", "--allow", "replace", "--json")
	if err != nil {
		t.Fatalf("env rotate --execute failed: %v\n%s", err, rotOut)
	}
	var rotPayload struct {
		DryRun bool                 `json:"dryRun"`
		Result *envmgr.RotateResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(rotOut), &rotPayload); err != nil || rotPayload.DryRun || rotPayload.Result == nil {
		t.Fatalf("unexpected rotate JSON: %s (err=%v)", rotOut, err)
	}
	if len(rotPayload.Result.UpdatedFiles) != 2 || rotPayload.Result.TxID == "" {
		t.Fatalf("expected 2 rotated files and txId, got %+v", rotPayload.Result)
	}

	gotA, err := os.ReadFile(envA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotA), "# keep svc-a comment") || !strings.Contains(string(gotA), "OTHER_VAR=keep") || !strings.Contains(string(gotA), "sk-new-secret-999999") {
		t.Fatalf("rotated envA lost comments or value:\n%s", string(gotA))
	}
	if fi, err := os.Stat(envA); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 on rotated envA, got %04o err=%v", fi.Mode().Perm(), err)
	}

	undoPreview, err := execWS(t, fx, "env", "undo", rotPayload.Result.TxID, "--json")
	if err != nil {
		t.Fatalf("env undo preview failed: %v\n%s", err, undoPreview)
	}
	if stillRotated, rerr := os.ReadFile(envA); rerr != nil || !strings.Contains(string(stillRotated), "sk-new-secret-999999") {
		t.Fatalf("expected bare env undo not to mutate files before --execute, got:\n%s (err=%v)", string(stillRotated), rerr)
	}

	if _, err := execWS(t, fx, "env", "undo", rotPayload.Result.TxID, "--execute", "--json"); err == nil {
		t.Fatalf("expected non-interactive env undo --execute without --allow replace to fail closed")
	}

	undoOut, err := execWS(t, fx, "env", "undo", rotPayload.Result.TxID, "--execute", "--allow", "replace", "--json")
	if err != nil {
		t.Fatalf("env undo failed: %v\n%s", err, undoOut)
	}
	revertedA, err := os.ReadFile(envA)
	if err != nil || !strings.Contains(string(revertedA), "sk-shared-secret-111111") {
		t.Fatalf("expected env undo to restore original secret, got:\n%s (err=%v)", string(revertedA), err)
	}
}

func TestEnvRotateDryRunDoesNotCreateOrMutateDB(t *testing.T) {
	fx := setupFixture(t)
	envA := filepath.Join(fx.home, "work", "code", "svc-a", ".env")
	if err := os.MkdirAll(filepath.Dir(envA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envA, []byte("DRY_TOKEN=sk-dryrun-secret-11112222\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	wsDir := filepath.Join(fx.home, ".ws")
	dbPath := filepath.Join(wsDir, "ws.db")

	dryOut, err := execWSWithStdin(t, fx, "sk-replacement-9999\n", "env", "rotate", "DRY_TOKEN", "--dry-run", "--value-stdin", "--json")
	if err != nil {
		t.Fatalf("ws env rotate --dry-run (no DB): %v\n%s", err, dryOut)
	}
	var dryPayload struct {
		DryRun bool               `json:"dryRun"`
		Plan   *envmgr.RotatePlan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(dryOut), &dryPayload); err != nil || !dryPayload.DryRun || dryPayload.Plan == nil || len(dryPayload.Plan.Targets) != 1 {
		t.Fatalf("unexpected dry-run rotate output: %s (err=%v)", dryOut, err)
	}
	if _, statErr := os.Lstat(dbPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry-run rotate must not create ws.db, stat err=%v", statErr)
	}
	if _, statErr := os.Lstat(wsDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry-run rotate must not create ~/.ws directory, stat err=%v", statErr)
	}

	if out, err := execWS(t, fx, "db", "key", "init"); err != nil {
		t.Fatalf("db key init: %v\n%s", err, out)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO env_entries(var_key, zone, source_kind, source_path, line_num, precedence, is_active, masked_value, fingerprint, is_secret, updated_at)
		 VALUES('STALE_SENTINEL', 'work', 'dotenv', '~/work/code/stale/.env', 1, 50, 1, 'mask', 'fp:stale', 1, 1)`,
	); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.SetMeta(envLastScanMetaKey, "1"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	dryOut2, err := execWSWithStdin(t, fx, "sk-replacement-8888\n", "env", "rotate", "DRY_TOKEN", "--dry-run", "--value-stdin", "--json")
	if err != nil {
		t.Fatalf("ws env rotate --dry-run (existing DB): %v\n%s", err, dryOut2)
	}
	var dryPayload2 struct {
		DryRun bool               `json:"dryRun"`
		Plan   *envmgr.RotatePlan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(dryOut2), &dryPayload2); err != nil || !dryPayload2.DryRun || dryPayload2.Plan == nil || len(dryPayload2.Plan.Targets) != 1 {
		t.Fatalf("expected dry-run rotate with existing DB to scan in memory and find DRY_TOKEN: %s (err=%v)", dryOut2, err)
	}

	dbCheck, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen ws.db: %v", err)
	}
	defer dbCheck.Close()

	var cnt int
	var onlyKey string
	if err := dbCheck.SQL().QueryRow(`SELECT COUNT(*), COALESCE(MIN(var_key), '') FROM env_entries`).Scan(&cnt, &onlyKey); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 || onlyKey != "STALE_SENTINEL" {
		t.Fatalf("dry-run rotate mutated env_entries: count=%d key=%q", cnt, onlyKey)
	}
	var blobCnt int
	if err := dbCheck.SQL().QueryRow(`SELECT COUNT(*) FROM secret_blobs`).Scan(&blobCnt); err != nil {
		t.Fatal(err)
	}
	if blobCnt != 0 {
		t.Fatalf("dry-run rotate stored %d secret_blobs, want 0", blobCnt)
	}
	if ts, ok, err := dbCheck.GetMeta(envLastScanMetaKey); err != nil || !ok || ts != "1" {
		t.Fatalf("dry-run rotate mutated %s meta: ts=%q ok=%v err=%v", envLastScanMetaKey, ts, ok, err)
	}
}

func TestRepoListLocalsAndCloneDryRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	fx := setupFixture(t)

	repoDir := filepath.Join(fx.home, "work", "code", "demo-svc")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repoDir
		c.Env = shell.MergeEnv(os.Environ(), []string{
			"HOME=" + fx.home,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Sample",
			"GIT_AUTHOR_EMAIL=sample@example.invalid",
			"GIT_COMMITTER_NAME=Sample",
			"GIT_COMMITTER_EMAIL=sample@example.invalid",
		})
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	runGit("init")
	runGit("remote", "add", "origin", "https://github.com/example-org/demo-svc.git")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(repoDir, ".env"), []byte("APP_SECRET=sk-demo-repo-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := execWS(t, fx, "db", "key", "init"); err != nil {
		t.Fatalf("db key init: %v\n%s", err, out)
	}
	if out, err := execWS(t, fx, "env", "scan"); err != nil {
		t.Fatalf("env scan: %v\n%s", err, out)
	}

	listOut, err := execWS(t, fx, "repo", "list", "--json")
	if err != nil {
		t.Fatalf("repo list --json failed: %v\n%s", err, listOut)
	}
	var repos []projects.RepoRecord
	if err := json.Unmarshal([]byte(listOut), &repos); err != nil || len(repos) != 1 {
		t.Fatalf("expected 1 discovered repo, got %s (err=%v)", listOut, err)
	}
	if repos[0].Name != "demo-svc" || repos[0].Zone != "work" || repos[0].Forge != "github" {
		t.Fatalf("unexpected repo record: %+v", repos[0])
	}
	if len(repos[0].UntrackedLocals) != 1 || !repos[0].UntrackedLocals[0].BackedUp {
		t.Fatalf("expected backed-up .env local manifest, got %+v", repos[0].UntrackedLocals)
	}

	localsOut, err := execWS(t, fx, "repo", "locals", "demo-svc")
	if err != nil {
		t.Fatalf("repo locals failed: %v\n%s", err, localsOut)
	}
	if !strings.Contains(localsOut, ".env") || !strings.Contains(localsOut, "ws.db") {
		t.Fatalf("unexpected repo locals output:\n%s", localsOut)
	}

	cloneOut, err := execWS(t, fx, "repo", "clone", "example-org/sample-app", "--zone", "work", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("repo clone --dry-run failed: %v\n%s", err, cloneOut)
	}
	var cloneResp struct {
		DryRun bool                `json:"dryRun"`
		Plan   *projects.ClonePlan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(cloneOut), &cloneResp); err != nil || !cloneResp.DryRun || cloneResp.Plan == nil {
		t.Fatalf("unexpected clone JSON: %s (err=%v)", cloneOut, err)
	}
	if cloneResp.Plan.Forge != "github" || !strings.HasSuffix(cloneResp.Plan.DestDir, filepath.Join("work", "code", "sample-app")) {
		t.Fatalf("unexpected clone plan: %+v", cloneResp.Plan)
	}
}

func TestFleetSyncDryRun(t *testing.T) {
	fx := setupFixture(t)

	if _, err := execWS(t, fx, "fleet", "sync", "edge-vm", "--no-zones", "--push-env", "--preset", "ubuntu-runner", "--arch", "arm64", "--dry-run"); err == nil {
		t.Fatalf("expected fleet sync --push-env without --repos to fail")
	}

	syncOut, err := execWS(t, fx, "fleet", "sync", "edge-vm",
		"--no-zones",
		"--push-env",
		"--repos", "sample-app",
		"--preset", "ubuntu-runner",
		"--arch", "arm64",
		"--dry-run",
		"--json",
	)
	if err != nil {
		t.Fatalf("fleet sync --dry-run failed: %v\n%s", err, syncOut)
	}
	var syncResp struct {
		DryRun   bool            `json:"dryRun"`
		Executed bool            `json:"executed"`
		Plan     *fleet.SyncPlan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(syncOut), &syncResp); err != nil || !syncResp.DryRun || syncResp.Executed || syncResp.Plan == nil {
		t.Fatalf("unexpected fleet sync JSON: %s (err=%v)", syncOut, err)
	}
	if len(syncResp.Plan.Steps) < 3 || len(syncResp.Plan.SkippedConfigs) == 0 || len(syncResp.Plan.ManualChecklist) == 0 {
		t.Fatalf("incomplete fleet sync plan: %+v", syncResp.Plan)
	}
}

func TestSchedulePrintWithDBBackup(t *testing.T) {
	fx := setupFixture(t)

	out, err := execWS(t, fx, "schedule", "print", "--with-db-backup", "--json")
	if err != nil {
		t.Fatalf("schedule print --with-db-backup failed: %v\n%s", err, out)
	}
	var spec pkgmgr.ScheduleSpec
	if err := json.Unmarshal([]byte(out), &spec); err != nil {
		t.Fatalf("unmarshal schedule spec: %v\n%s", err, out)
	}
	if !spec.WithDBBackup || len(spec.Files) < 2 {
		t.Fatalf("expected auxiliary db-backup schedule files, got %+v", spec)
	}
	var joined strings.Builder
	for _, f := range spec.Files {
		joined.WriteString(f.Content)
	}
	allContent := joined.String()
	hasBackup := strings.Contains(allContent, "db backup --if-changed") ||
		(strings.Contains(allContent, "<string>db</string>") &&
			strings.Contains(allContent, "<string>backup</string>") &&
			strings.Contains(allContent, "<string>--if-changed</string>"))
	if !hasBackup {
		t.Fatalf("expected ws db backup --if-changed in schedule files:\n%s", allContent)
	}
	for _, forbidden := range []string{"apply", "git pull", "rotate"} {
		if strings.Contains(allContent, forbidden) {
			t.Fatalf("schedule must never contain %q:\n%s", forbidden, allContent)
		}
	}
}

func TestBatchedPackageCheckAndTUIResolvePreset(t *testing.T) {
	fx := setupFixture(t)

	presetsPath := filepath.Join(fx.repo, "presets.toml")
	rawPresets, err := os.ReadFile(presetsPath)
	if err != nil {
		t.Fatal(err)
	}
	macPreset := `
[preset.mac-workstation]
description = "macOS test preset"
links = ["links/common.links"]
packages = ["pkg/mac-brew.txt", "pkg/mac-cask.txt"]
secrets = "none"
`
	if err := os.WriteFile(presetsPath, append(rawPresets, []byte(macPreset)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "mac-brew.txt"), []byte("ripgrep\ncoreutils\nmissing-formula\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "mac-cask.txt"), []byte("ghostty\nzed\norbstack\nfont-geist-mono-nerd-font\nmissing-cask\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	fakeBrew := `#!/bin/sh
if [ "$1" = "list" ] && [ "$2" = "--formula" ]; then
  printf 'ripgrep\ncoreutils\n'
  exit 0
fi
if [ "$1" = "list" ] && [ "$2" = "--cask" ]; then
  printf 'ghostty\nzed\norbstack\nfont-geist-mono-nerd-font\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(fakeBin, "brew"), []byte(fakeBrew), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin)

	macCtx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "mac-workstation",
	})
	if err != nil {
		t.Fatal(err)
	}
	macCtx.Host = app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64", PkgManagers: []string{"brew"}}

	b := NewTUIBackend(macCtx)
	defer func() { _ = b.Close() }()
	rows, err := b.ResolvePreset("mac-workstation")
	if err != nil {
		t.Fatalf("ResolvePreset(mac-workstation) failed: %v", err)
	}

	statusByID := map[string]string{}
	for _, r := range rows {
		statusByID[r.ID] = r.Status
	}
	for _, wantInstalled := range []string{"ripgrep", "coreutils", "ghostty", "zed", "orbstack", "font-geist-mono-nerd-font"} {
		if statusByID[wantInstalled] != "installed" {
			t.Fatalf("expected %s installed via batched brew check, got %q (all=%+v)", wantInstalled, statusByID[wantInstalled], statusByID)
		}
	}
	for _, wantMissing := range []string{"missing-formula", "missing-cask"} {
		if statusByID[wantMissing] != "missing" {
			t.Fatalf("expected %s missing, got %q", wantMissing, statusByID[wantMissing])
		}
	}

	if runtime.GOOS == "darwin" {
		checkOut, err := execWS(t, fx, "--preset", "mac-workstation", "pkg", "check", "--json")
		if err != nil {
			t.Fatalf("pkg check --json failed: %v\n%s", err, checkOut)
		}
		var checkPayload struct {
			Installed int `json:"installed"`
			Total     int `json:"total"`
			Tools     []struct {
				ID        string `json:"id"`
				Installed bool   `json:"installed"`
			} `json:"tools"`
		}
		if err := json.Unmarshal([]byte(checkOut), &checkPayload); err != nil {
			t.Fatalf("unmarshal pkg check: %v\n%s", err, checkOut)
		}
		if checkPayload.Installed != 6 || checkPayload.Total != 8 {
			t.Fatalf("expected 6/8 installed from synthetic brew, got %d/%d (%s)", checkPayload.Installed, checkPayload.Total, checkOut)
		}
	}

	systemBash := config.ResolvedTool{ID: "bash", Bin: "bash", Updates: "manager", Spec: config.InstallSpec{Via: "brew", Name: "bash"}}
	sysFakeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sysFakeDir, "bash"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysFakeDir, "brew"), []byte(fakeBrew), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sysFakeDir)
	brewOnlyDet := newPkgInstallDetector(&app.Context{
		RepoRoot: fx.repo,
		HomeDir:  fx.home,
		Host:     app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64"},
	}, []config.ResolvedTool{systemBash})
	if inst, st := brewOnlyDet.checkStatus(systemBash); inst || st != "missing" {
		t.Fatalf("expected brew bash to report missing when absent from brew list --formula (despite /bin/bash on PATH), got %v %q", inst, st)
	}

	fakeLinuxBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeLinuxBin, "pacman"), []byte("#!/bin/sh\nif [ \"$1\" = \"-Qq\" ]; then printf 'ripgrep\\nfd\\n'; exit 0; fi\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeLinuxBin, "dpkg-query"), []byte("#!/bin/sh\nprintf 'install ok installed\\tgit\\ndeinstall ok config-files\\tdpkg-purged\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeLinuxBin)

	linuxCtx := *macCtx
	linuxCtx.Host = app.HostInfo{OS: "linux", Distro: "arch", Arch: "amd64"}
	linuxTools := []config.ResolvedTool{
		{ID: "ripgrep", Bin: "rg", Updates: "manager", Spec: config.InstallSpec{Via: "pacman", Name: "ripgrep"}},
		{ID: "missing-arch", Bin: "missing-arch", Updates: "manager", Spec: config.InstallSpec{Via: "pacman", Name: "missing-arch"}},
		{ID: "git", Bin: "git", Updates: "manager", Spec: config.InstallSpec{Via: "apt", Name: "git"}},
		{ID: "dpkg-purged", Bin: "dpkg-purged", Updates: "manager", Spec: config.InstallSpec{Via: "apt", Name: "dpkg-purged"}},
	}
	det := newPkgInstallDetector(&linuxCtx, linuxTools)
	if inst, st := det.checkStatus(linuxTools[0]); !inst || st != "installed" {
		t.Fatalf("expected pacman ripgrep installed, got %v %q", inst, st)
	}
	if inst, st := det.checkStatus(linuxTools[1]); inst || st != "missing" {
		t.Fatalf("expected pacman missing-arch missing, got %v %q", inst, st)
	}
	if inst, st := det.checkStatus(linuxTools[2]); !inst || st != "installed" {
		t.Fatalf("expected apt git installed via dpkg-query, got %v %q", inst, st)
	}
	if inst, st := det.checkStatus(linuxTools[3]); inst || st != "missing" {
		t.Fatalf("expected deinstall config-files dpkg-purged missing, got %v %q", inst, st)
	}
}

func TestPickerTTYDetectionWhenStdoutPiped(t *testing.T) {
	fx := setupFixture(t)
	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
	})
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	ctx.NonInteractive = false
	ctx.JSON = false

	cmd := NewRootCmd(&GlobalOpts{Repo: fx.repo, Home: fx.home})
	if got := isPickerInteractiveTTYWith(cmd, ctx, func() bool { return true }); !got {
		t.Fatalf("expected picker interactive=true when stdout is os.Stdout (even if piped) and hasTTY is true")
	}
	if got := isPickerInteractiveTTYWith(cmd, ctx, func() bool { return false }); got {
		t.Fatalf("expected picker interactive=false when hasTTY returns false")
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() {
		_ = pr.Close()
		_ = pw.Close()
	}()
	if ui.IsTTY(pw) {
		t.Fatalf("expected os.Pipe writer not to be a TTY")
	}
	if picker.VisibleRows(24) != 14 || picker.VisibleRows(10) != 6 {
		t.Fatalf("unexpected VisibleRows bounds: h=24 -> %d, h=10 -> %d", picker.VisibleRows(24), picker.VisibleRows(10))
	}

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if got := isPickerInteractiveTTYWith(cmd, ctx, func() bool { return true }); got {
		t.Fatalf("expected picker interactive=false when cmd.SetOut(&buf) overrides stdout")
	}

	cmdDefaultOut := NewRootCmd(&GlobalOpts{Repo: fx.repo, Home: fx.home})
	nonIntCtx := *ctx
	nonIntCtx.NonInteractive = true
	if got := isPickerInteractiveTTYWith(cmdDefaultOut, &nonIntCtx, func() bool { return true }); got {
		t.Fatalf("expected picker interactive=false when NonInteractive=true")
	}

	jsonCtx := *ctx
	jsonCtx.JSON = true
	if got := isPickerInteractiveTTYWith(cmdDefaultOut, &jsonCtx, func() bool { return true }); got {
		t.Fatalf("expected picker interactive=false when JSON=true")
	}
}

func TestRootHelpGroupsAndNoColorZeroANSI(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	helpOut, err := execWS(t, fx, "--help")
	if err != nil {
		t.Fatalf("ws --help failed: %v\n%s", err, helpOut)
	}
	if strings.Contains(helpOut, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in ws --help under NO_COLOR=1:\n%q", helpOut)
	}
	for _, want := range []string{"Workstation", "Workflow", "Secrets & State", "Packages & Fleet", "Preview convention:", "tui", "repo", "aws", "env", "hist", "fleet", "--tsv"} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("expected %q in ws --help output:\n%s", want, helpOut)
		}
	}
	for _, hidden := range []string{"--plan", "--save-plan", "\n  edit ", "\n  run ", "\n  schedule ", "\n  gen ", "\n  test ", "\n  android ", "\n  os-env ", "\n  plugin "} {
		if strings.Contains(helpOut, hidden) {
			t.Fatalf("expected %q hidden from ws --help output:\n%s", hidden, helpOut)
		}
	}

	cs := ui.FangScheme(nil)
	if cs.Title != (ui.FangScheme(nil)).Title {
		t.Fatalf("unexpected unstable FangScheme")
	}

	out, err := execWS(t, fx, "preset", "list")
	if err != nil {
		t.Fatalf("preset list failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in preset list under NO_COLOR=1:\n%q", out)
	}
}

func TestEnsureEnvIndexedTTLStaleness(t *testing.T) {
	fx := setupFixture(t)

	envPath := filepath.Join(fx.home, "work", "code", "ttl-svc", ".env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("TTL_ALPHA=first-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
		NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	db, kr, err := openStoreAndKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	if err := ensureEnvIndexedAt(ctx, db, kr, false, t0); err != nil {
		t.Fatalf("initial ensureEnvIndexedAt: %v", err)
	}
	rawTS, ok, err := db.GetMeta(envLastScanMetaKey)
	if err != nil || !ok || rawTS != strconv.FormatInt(t0.Unix(), 10) {
		t.Fatalf("unexpected env_last_scan_at after initial scan: raw=%q ok=%v err=%v", rawTS, ok, err)
	}
	entries, err := envmgr.FindEntriesForZone(db, "TTL_ALPHA", "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected TTL_ALPHA indexed initially, got %+v err=%v", entries, err)
	}

	if err := os.WriteFile(envPath, []byte("TTL_ALPHA=first-value\nTTL_BETA=second-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tFresh := t0.Add(5 * time.Minute)
	if err := ensureEnvIndexedAt(ctx, db, kr, false, tFresh); err != nil {
		t.Fatalf("fresh ensureEnvIndexedAt: %v", err)
	}
	if beta, err := envmgr.FindEntriesForZone(db, "TTL_BETA", ""); err != nil || len(beta) != 0 {
		t.Fatalf("expected fresh TTL cache to skip rescan, found beta=%+v err=%v", beta, err)
	}

	tStale := t0.Add(11 * time.Minute)
	if err := ensureEnvIndexedAt(ctx, db, kr, false, tStale); err != nil {
		t.Fatalf("stale ensureEnvIndexedAt: %v", err)
	}
	if beta, err := envmgr.FindEntriesForZone(db, "TTL_BETA", ""); err != nil || len(beta) != 1 {
		t.Fatalf("expected stale TTL cache to re-scan TTL_BETA, got %+v err=%v", beta, err)
	}
	if gotTS, _, _ := db.GetMeta(envLastScanMetaKey); gotTS != strconv.FormatInt(tStale.Unix(), 10) {
		t.Fatalf("expected updated env_last_scan_at=%d, got %q", tStale.Unix(), gotTS)
	}

	if err := os.WriteFile(envPath, []byte("TTL_ALPHA=first-value\nTTL_BETA=second-value\nTTL_GAMMA=third-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tForced := tStale.Add(1 * time.Minute)
	if err := ensureEnvIndexedAt(ctx, db, kr, true, tForced); err != nil {
		t.Fatalf("forced ensureEnvIndexedAt: %v", err)
	}
	if gamma, err := envmgr.FindEntriesForZone(db, "TTL_GAMMA", ""); err != nil || len(gamma) != 1 {
		t.Fatalf("expected forceScan to immediately index TTL_GAMMA, got %+v err=%v", gamma, err)
	}
}

func TestEnvFilesCommandAndTUIBackendFiltering(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("WS_ZONES", "work|Example User|work@example.invalid\npersonal|Example User|personal@example.invalid")

	workEnv := filepath.Join(fx.home, "work", "code", "svc-work", ".env")
	if err := os.MkdirAll(filepath.Dir(workEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workEnv, []byte("WORK_API_KEY=sk-work-secret-123456\nWORK_MODE=staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	persMise := filepath.Join(fx.home, "personal", "mise.toml")
	if err := os.MkdirAll(filepath.Dir(persMise), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(persMise, []byte("[env]\nWS_ZONE = \"personal\"\nWS_CODE_ROOT = \"{{env.HOME}}/personal/code\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	persEnv := filepath.Join(fx.home, "personal", "code", "app-pers", ".env")
	if err := os.MkdirAll(filepath.Dir(persEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(persEnv, []byte("SHARED_KEY=sk-personal-secret-999999\nPERSONAL_ONLY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workEnv, []byte("SHARED_KEY=sk-work-secret-123456\nWORK_MODE=staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	allJSON, err := execWS(t, fx, "env", "files", "--scan", "--json")
	if err != nil {
		t.Fatalf("ws env files --json failed: %v\n%s", err, allJSON)
	}
	var allFiles []tui.EnvFileRow
	if err := json.Unmarshal([]byte(allJSON), &allFiles); err != nil {
		t.Fatalf("unmarshal env files JSON: %v\n%s", err, allJSON)
	}
	if len(allFiles) < 2 {
		t.Fatalf("expected >=2 indexed env files across zones, got %+v", allFiles)
	}
	var foundWorkEnv bool
	for _, f := range allFiles {
		if strings.HasSuffix(f.Path, filepath.Join("svc-work", ".env")) {
			foundWorkEnv = true
			if f.Zone != "work" || f.VarCount != 2 || f.SecretCount != 2 {
				t.Fatalf("unexpected svc-work file counts: %+v", f)
			}
		}
	}
	if !foundWorkEnv {
		t.Fatalf("expected svc-work/.env in env files output: %+v", allFiles)
	}

	workOnlyJSON, err := execWS(t, fx, "env", "files", "--zone", "work", "--json")
	if err != nil {
		t.Fatalf("ws env files --zone work --json failed: %v\n%s", err, workOnlyJSON)
	}
	var workFiles []tui.EnvFileRow
	if err := json.Unmarshal([]byte(workOnlyJSON), &workFiles); err != nil {
		t.Fatalf("unmarshal work env files JSON: %v", err)
	}
	if len(workFiles) == 0 {
		t.Fatalf("expected work zone env files")
	}
	for _, f := range workFiles {
		if f.Zone != "work" {
			t.Fatalf("expected only zone=work files, got %+v", f)
		}
	}

	tableOut, err := execWS(t, fx, "env", "files")
	if err != nil {
		t.Fatalf("ws env files failed: %v\n%s", err, tableOut)
	}
	if strings.Contains(tableOut, "\x1b") {
		t.Fatalf("expected zero ANSI in ws env files under NO_COLOR=1:\n%q", tableOut)
	}
	for _, col := range []string{"PATH", "KIND", "ZONE", "VARS", "SECRETS", "STATE"} {
		if !strings.Contains(tableOut, col) {
			t.Fatalf("expected header column %q in env files table:\n%s", col, tableOut)
		}
	}

	tsvOut, err := execWS(t, fx, "env", "files", "--tsv")
	if err != nil {
		t.Fatalf("ws env files --tsv failed: %v\n%s", err, tsvOut)
	}
	if strings.Contains(tsvOut, "\x1b") || strings.Contains(tsvOut, "SECRETS\t") || !strings.Contains(tsvOut, "\t") {
		t.Fatalf("unexpected ws env files --tsv output:\n%s", tsvOut)
	}

	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
		NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := NewTUIBackend(ctx)
	defer func() { _ = b.Close() }()

	persVars, err := b.LoadEnvVars("", "personal")
	if err != nil {
		t.Fatalf("LoadEnvVars personal: %v", err)
	}
	var sawSharedInPersonal, sawWorkModeInPersonal bool
	for _, v := range persVars {
		if v.VarKey == "SHARED_KEY" && v.Zone == "personal" {
			sawSharedInPersonal = true
		}
		if v.VarKey == "WORK_MODE" {
			sawWorkModeInPersonal = true
		}
	}
	if !sawSharedInPersonal || sawWorkModeInPersonal {
		t.Fatalf("expected zone-scoped LoadEnvVars(personal) to include SHARED_KEY and exclude WORK_MODE, got %+v", persVars)
	}

	tuiFiles, err := b.LoadEnvFiles("personal")
	if err != nil || len(tuiFiles) == 0 {
		t.Fatalf("LoadEnvFiles(personal): %+v err=%v", tuiFiles, err)
	}
	for _, f := range tuiFiles {
		if f.Zone != "personal" {
			t.Fatalf("LoadEnvFiles(personal) leaked non-personal row: %+v", f)
		}
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := b.LoadFleetNodes(); err == nil {
		t.Fatalf("expected LoadFleetNodes to return error when tailscale CLI is missing and no saved nodes exist")
	}
}

func TestAWSMetadataListAndPickQuery(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	awsCfg := filepath.Join(fx.home, "work", ".aws", "config")
	if err := os.MkdirAll(filepath.Dir(awsCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(awsCfg, []byte("[default]\nregion = us-east-1\nsso_account_id = 111122223333\n\n[profile prod-role]\nregion = eu-central-1\nrole_arn = arn:aws:iam::444455556666:role/Admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workMise := filepath.Join(fx.home, "work", "mise.toml")
	if err := os.WriteFile(workMise, []byte("[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\nAWS_CONFIG_FILE = \"{{env.HOME}}/work/.aws/config\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	listOut, err := execWS(t, fx, "aws", "list")
	if err != nil {
		t.Fatalf("ws aws list failed: %v\n%s", err, listOut)
	}
	for _, want := range []string{"ZONE", "PROFILE", "REGION", "ACCOUNT", "us-east-1", "111122223333", "prod-role", "eu-central-1", "444455556666"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("expected %q in ws aws list output:\n%s", want, listOut)
		}
	}

	pickOut, err := execWS(t, fx, "aws", "pick", "prod")
	if err != nil {
		t.Fatalf("ws aws pick prod failed: %v\n%s", err, pickOut)
	}
	if strings.TrimSpace(pickOut) != "prod-role" {
		t.Fatalf("expected ws aws pick prod -> prod-role, got %q", pickOut)
	}
}

func TestWave2CLIZoneAndSecretsIntegration(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("WS_ZONES", "work|Example User|work@example.invalid\npersonal|Example User|personal@example.invalid")

	workMise := filepath.Join(fx.home, "work", "mise.toml")
	if err := os.MkdirAll(filepath.Dir(workMise), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workMise, []byte("[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\nCLAUDE_CONFIG_DIR = \"{{env.HOME}}/work/.claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	persMise := filepath.Join(fx.home, "personal", "mise.toml")
	if err := os.MkdirAll(filepath.Dir(persMise), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(persMise, []byte("[env]\nWS_ZONE = \"personal\"\nWS_CODE_ROOT = \"{{env.HOME}}/personal/code\"\nCLAUDE_CONFIG_DIR = \"{{env.HOME}}/personal/.claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	secretProd := filepath.Join(fx.home, "personal", "code", "genesis", ".secrets", "prod.env")
	if err := os.MkdirAll(filepath.Dir(secretProd), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretProd, []byte("DB_URL=postgres://personal-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workSecret := filepath.Join(fx.home, "work", "code", "svc", ".env")
	if err := os.MkdirAll(filepath.Dir(workSecret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workSecret, []byte("DB_URL=postgres://work-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := execWS(t, fx, "env", "scan"); err != nil {
		t.Fatalf("ws env scan: %v", err)
	}
	listJSON, err := execWS(t, fx, "env", "list", "--json")
	if err != nil {
		t.Fatalf("ws env list --json: %v", err)
	}
	var dbZones []string
	var summaries []envmgr.EnvSummary
	if err := json.Unmarshal([]byte(listJSON), &summaries); err != nil {
		t.Fatalf("unmarshal env list: %v", err)
	}
	for _, s := range summaries {
		if s.VarKey == "DB_URL" {
			dbZones = append(dbZones, s.Zone)
		}
	}
	if len(dbZones) != 2 {
		t.Fatalf("expected DB_URL in both zones without collapsing, got zones=%v", dbZones)
	}

	findWork, err := execWS(t, fx, "env", "find", "--zone", "work", "DB_URL", "--json")
	if err != nil {
		t.Fatalf("ws env find --zone work: %v", err)
	}
	if strings.Contains(findWork, "genesis/.secrets/prod.env") || !strings.Contains(findWork, "svc/.env") {
		t.Fatalf("unexpected zone-filtered env find output: %s", findWork)
	}

	for _, repoDir := range []string{
		filepath.Join(fx.home, "personal", "code", "genesis"),
		filepath.Join(fx.home, "work", "code", "svc"),
	} {
		if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoWorkOut, err := execWS(t, fx, "repo", "list", "--zone", "work", "--json")
	if err != nil {
		t.Fatalf("ws repo list --zone work: %v\n%s", err, repoWorkOut)
	}
	if !strings.Contains(repoWorkOut, "svc") || strings.Contains(repoWorkOut, "genesis") {
		t.Fatalf("unexpected zone-filtered repo list: %s", repoWorkOut)
	}

	if err := os.WriteFile(filepath.Join(fx.home, "work", "code", "svc", ".git", "HEAD"), []byte("ref: refs/heads/feat/live-head\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pickWorkOut, err := execWS(t, fx, "repo", "pick", "--zone", "work", "--json")
	if err != nil {
		t.Fatalf("ws repo pick --zone work --json: %v\n%s", err, pickWorkOut)
	}
	if !strings.Contains(pickWorkOut, "svc") || !strings.Contains(pickWorkOut, "feat/live-head") || strings.Contains(pickWorkOut, "genesis") {
		t.Fatalf("unexpected fast ws repo pick output: %s", pickWorkOut)
	}
}

func TestTUIBackendK9sAdditions(t *testing.T) {
	fx := setupFixture(t)

	presets, err := config.LoadPresets(fx.repo)
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	rp, err := config.ResolvePreset(fx.repo, presets, "ubuntu-runner")
	if err != nil {
		t.Fatalf("ResolvePreset(ubuntu-runner): %v", err)
	}
	if orig := rp.PackageOrigins["git"]; orig.PresetID != "base" || orig.SourceFile != "pkg/server.txt" {
		t.Fatalf("expected git PackageOrigin base/pkg/server.txt, got %+v", orig)
	}
	if orig := rp.PackageOrigins["yq"]; orig.PresetID != "ubuntu-runner" || orig.SourceFile != "extra_tools" {
		t.Fatalf("expected yq PackageOrigin ubuntu-runner/extra_tools, got %+v", orig)
	}

	histContent := ": 1732200000:2;git status -sb\n: 1732200010:0;ws doctor --json\n"
	if err := os.WriteFile(filepath.Join(fx.home, ".zsh_history"), []byte(histContent), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fx.home)

	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
		DryRun:         true,
	})
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}
	b := NewTUIBackend(ctx)
	defer func() { _ = b.Close() }()

	rows, err := b.ResolvePreset("ubuntu-runner")
	if err != nil || len(rows) == 0 {
		t.Fatalf("ResolvePreset: len=%d err=%v", len(rows), err)
	}
	var yqRow *tui.PresetToolRow
	for i := range rows {
		if rows[i].ID == "yq" {
			yqRow = &rows[i]
			break
		}
	}
	if yqRow == nil {
		t.Fatalf("expected resolved tool yq in ubuntu-runner rows: %+v", rows)
	}
	if yqRow.OriginPreset != "ubuntu-runner" || yqRow.OriginFile != "extra_tools" {
		t.Fatalf("unexpected yq origin metadata: %+v", yqRow)
	}

	histRows, histSrc, err := b.LoadHistory("", 50)
	if err != nil || len(histRows) == 0 || histSrc == "" {
		t.Fatalf("LoadHistory: rows=%d src=%q err=%v", len(histRows), histSrc, err)
	}

	pullRes, err := b.PullSelectedRepos([]string{filepath.Join(fx.home, "nonexistent-repo")})
	if err != nil {
		t.Fatalf("PullSelectedRepos: %v", err)
	}
	_ = pullRes

	actRes, err := b.ExecPackageAction("update", []tui.PresetToolRow{
		{ID: "git", PackageName: "git", Provider: "brew", Updates: "manager"},
		{ID: "claude-code", PackageName: "claude-code", Provider: "script", Updates: "self"},
	})
	if err != nil {
		t.Fatalf("ExecPackageAction dry-run: %v", err)
	}
	if !strings.Contains(actRes.Output, "[dry-run] $ brew upgrade git") || !strings.Contains(actRes.Output, "updates=self; manages its own updates") {
		t.Fatalf("unexpected ExecPackageAction dry-run output:\n%s", actRes.Output)
	}

	if res, err := b.RunWSCommand([]string{"tui"}); err == nil || !res.IsErr {
		t.Fatalf("expected RunWSCommand(tui) to fail cleanly, got res=%+v err=%v", res, err)
	}
	safeRes, err := b.RunWSCommand([]string{"theme", "list"})
	if err != nil || safeRes.ExitCode != 0 || !strings.Contains(safeRes.Output, "catppuccin-dark") {
		t.Fatalf("expected RunWSCommand(theme list) to succeed in-process, got res=%+v err=%v", safeRes, err)
	}
}

func TestPhase6CLIHardeningAndParity(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", fx.home)

	if _, err := execWS(t, fx, "preset", "list", "--json", "--tsv"); err == nil {
		t.Fatalf("expected --json + --tsv to be rejected")
	}
	if _, err := execWS(t, fx, "doctor", "--plan", filepath.Join(fx.home, "missing.json")); err == nil {
		t.Fatalf("expected --plan on doctor to be rejected")
	}
	outdatedTSV, err := execWS(t, fx, "pkg", "outdated", "--tsv")
	if err != nil {
		t.Fatalf("pkg outdated --tsv: %v\n%s", err, outdatedTSV)
	}
	if strings.Contains(outdatedTSV, "\x1b") {
		t.Fatalf("expected zero ANSI in pkg outdated --tsv: %q", outdatedTSV)
	}

	if _, err := execWS(t, fx, "tui", "bogus-tab"); err == nil {
		t.Fatalf("expected unknown tui tab to be rejected")
	}
	if _, err := execWS(t, fx, "tui", "env"); err == nil {
		t.Fatalf("expected non-interactive tui to be rejected")
	}

	t.Setenv("EDITOR", "false")
	if out, err := execWS(t, fx, "edit", "tmux", "--dry-run", "--json"); err != nil || !strings.Contains(out, `"dryRun": true`) {
		t.Fatalf("edit --dry-run failed: err=%v out=%s", err, out)
	}

	themePlanPath := filepath.Join(fx.home, "theme-plan.json")
	if out, err := execWS(t, fx, "theme", "catppuccin-dark", "--save-plan", themePlanPath); err != nil {
		t.Fatalf("theme --save-plan failed: %v\n%s", err, out)
	}
	if out, err := execWS(t, fx, "theme", "--plan", themePlanPath, "--dry-run", "--json"); err != nil || !strings.Contains(out, "catppuccin-dark") {
		t.Fatalf("theme --plan failed: err=%v out=%s", err, out)
	}
	if out, err := execWS(t, fx, "theme", "set", "catppuccin-dark", "--app", "tmux", "--dry-run", "--json"); err != nil || !strings.Contains(out, "tmux") {
		t.Fatalf("theme set --app tmux --dry-run failed: err=%v out=%s", err, out)
	}

	det := &pkgInstallDetector{ctx: &app.Context{Host: app.HostInfo{OS: "darwin"}}, hasBrewFormulae: true, brewFormulae: map[string]bool{}}
	if inst, status := det.checkStatus(config.ResolvedTool{ID: "zsh", Bin: "sh", Spec: config.InstallSpec{Via: "brew", Name: "zsh"}}); !inst || status != "installed" {
		t.Fatalf("expected macOS zsh on PATH to report installed even when absent from brew formulae, got inst=%v status=%q", inst, status)
	}

	t.Setenv("WS_ZONES", "work|Example User|work@example.invalid")
	seedDir := filepath.Join(fx.repo, "config", "identity")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "work.mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symTarget := filepath.Join(fx.home, "work", "mise.toml")
	if err := os.MkdirAll(filepath.Dir(symTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(symTarget)
	if err := os.Symlink(filepath.Join(seedDir, "work.mise.toml"), symTarget); err != nil {
		t.Fatal(err)
	}
	if out, err := execWS(t, fx, "zone", "fix-symlinks", "work", "--allow", "replace"); err != nil {
		t.Fatalf("zone fix-symlinks: %v\n%s", err, out)
	}
	if fi, lerr := os.Lstat(symTarget); lerr != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected symlink %s converted to regular file, got mode=%v err=%v", symTarget, fi.Mode(), lerr)
	}
	flatProj := filepath.Join(fx.home, "code", "legacy-svc")
	if err := os.MkdirAll(flatProj, 0o755); err != nil {
		t.Fatal(err)
	}
	migPreviewOut, err := execWS(t, fx, "zone", "migrate", "work", "--all", "--json")
	if err != nil {
		t.Fatalf("zone migrate preview: %v\n%s", err, migPreviewOut)
	}
	if _, serr := os.Stat(flatProj); serr != nil {
		t.Fatalf("expected bare zone migrate not to move directory without --execute: %v", serr)
	}
	migOut, err := execWS(t, fx, "zone", "migrate", "work", "--all", "--execute", "--json")
	if err != nil {
		t.Fatalf("zone migrate --all --execute: %v\n%s", err, migOut)
	}
	if _, serr := os.Stat(filepath.Join(fx.home, "work", "code", "legacy-svc")); serr != nil {
		t.Fatalf("expected legacy-svc moved into ~/work/code: %v", serr)
	}

	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "android.txt"), []byte("platform-tools\nsystem-images;android-35;google_apis;arm64-v8a\nsystem-images;android-35;google_apis;x86_64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	andOut, err := execWS(t, fx, "android", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("android --dry-run --json: %v\n%s", err, andOut)
	}
	if !strings.Contains(andOut, `"platform-tools"`) {
		t.Fatalf("expected platform-tools in android output: %s", andOut)
	}

	stubBin := t.TempDir()
	sshStub := filepath.Join(stubBin, "ssh")
	if err := os.WriteFile(sshStub, []byte("#!/bin/sh\necho 'noisy ssh banner stdout'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pluginScript := filepath.Join(stubBin, "ws-customexit")
	if err := os.WriteFile(pluginScript, []byte("#!/bin/sh\nif [ \"$1\" = \"--custom-flag\" ]; then exit 7; fi\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+origPath)

	tagsOut, err := execWS(t, fx, "fleet", "tags", "dev-vm", "tag:dev", "--execute", "--json")
	if err != nil {
		t.Fatalf("fleet tags --execute --json: %v\n%s", err, tagsOut)
	}
	var tagsPayload map[string]any
	if uerr := json.Unmarshal([]byte(tagsOut), &tagsPayload); uerr != nil || !strings.Contains(tagsOut, "noisy ssh banner stdout") {
		t.Fatalf("expected valid JSON capturing ssh stdout, got err=%v out=%s", uerr, tagsOut)
	}

	t.Setenv("WS_DB_KEY", "")
	_ = os.Remove(filepath.Join(fx.home, ".ws", "db.key"))
	if _, err := execWS(t, fx, "fleet", "sync", "dev-vm", "--repos", "sample-app", "--push-env", "--execute"); err == nil {
		t.Fatalf("expected fleet sync --push-env --execute without ws.db/key to fail loudly")
	}

	rootCmd := NewRootCmd(&GlobalOpts{Repo: fx.repo, Home: fx.home})
	handled, perr := TryPreDispatchPlugin(t.Context(), rootCmd, &GlobalOpts{Repo: fx.repo, Home: fx.home}, []string{"customexit", "--custom-flag"})
	if !handled || ExitCode(perr) != 7 {
		t.Fatalf("expected pre-dispatch plugin exit code 7, got handled=%v code=%d err=%v", handled, ExitCode(perr), perr)
	}

	ctx, err := app.NewContext(app.Options{
		RepoRoot:       fx.repo,
		HomeDir:        fx.home,
		PresetOverride: "ubuntu-runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := NewTUIBackend(ctx)
	if res, err := b.RunWSCommand([]string{"--dry-run", "tui"}); err == nil || !res.IsErr {
		t.Fatalf("expected RunWSCommand(--dry-run tui) to fail closed, got res=%+v err=%v", res, err)
	}
	if _, err := b.TemplateStatuses(); err != nil {
		t.Fatalf("TemplateStatuses: %v", err)
	}
	if _, err := b.FinishAppEdit("tmux"); err != nil {
		t.Fatalf("FinishAppEdit: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := b.LoadDBStatus(); err == nil {
		t.Fatalf("expected LoadDBStatus after Close to fail cleanly")
	}
}

func TestMiseCLICommands(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("HOME", fx.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fx.home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(fx.home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(fx.home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(fx.home, ".cache"))
	t.Setenv("WS_HOME", "")
	t.Setenv("WS_CODE_ROOT", "")
	t.Setenv("MISE_TRUSTED_CONFIG_PATHS", "")
	t.Setenv("WS_ZONES", "work|Example User|work@example.invalid")

	otherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherDir, "mise.toml"), []byte("[tools]\npython = \"3.12\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(otherDir)

	globalMise := filepath.Join(fx.home, ".config", "mise", "config.toml")
	if err := os.MkdirAll(filepath.Dir(globalMise), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalMise, []byte("[tools]\ngo = \"1.24\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projMise := filepath.Join(fx.home, "work", "code", "sample-app", "mise.local.toml")
	if err := os.MkdirAll(filepath.Dir(projMise), 0o755); err != nil {
		t.Fatal(err)
	}
	const secretSentinel = "sk-never-export-this-value"
	if err := os.WriteFile(projMise, []byte("[tools]\nnode = \"20\"\n\n[env]\nSUPER_SECRET_API_KEY = \""+secretSentinel+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgOut, err := execWS(t, fx, "mise", "configs", "--no-outdated", "--json")
	if err != nil {
		t.Fatalf("ws mise configs: %v\n%s", err, cfgOut)
	}
	if !strings.Contains(cfgOut, "SUPER_SECRET_API_KEY") || !strings.Contains(cfgOut, "sample-app") {
		t.Fatalf("expected mise configs JSON to list project config and env key name:\n%s", cfgOut)
	}
	if strings.Contains(cfgOut, secretSentinel) {
		t.Fatalf("SECURITY VIOLATION: secret value leaked into ws mise configs output")
	}

	listOut, err := execWS(t, fx, "mise", "list", "--no-outdated", "--json")
	if err != nil {
		t.Fatalf("ws mise list: %v\n%s", err, listOut)
	}
	var listPayload map[string]any
	if jErr := json.Unmarshal([]byte(listOut), &listPayload); jErr != nil {
		t.Fatalf("invalid mise list JSON: %v\n%s", jErr, listOut)
	}
	if strings.Contains(listOut, secretSentinel) {
		t.Fatalf("SECURITY VIOLATION: secret value leaked into ws mise list output")
	}
}

func TestMiseCLICommandsWithoutWSZones(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("HOME", fx.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fx.home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(fx.home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(fx.home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(fx.home, ".cache"))
	t.Setenv("WS_HOME", "")
	t.Setenv("WS_CODE_ROOT", "")
	t.Setenv("WS_ZONES", "")
	t.Setenv("MISE_TRUSTED_CONFIG_PATHS", "")

	otherDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherDir, "mise.toml"), []byte("[tools]\npython = \"3.12\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(otherDir)

	globalMise := filepath.Join(fx.home, ".config", "mise", "config.toml")
	if err := os.MkdirAll(filepath.Dir(globalMise), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalMise, []byte("[tools]\ngo = \"1.24\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	projMise := filepath.Join(fx.home, "work", "code", "sample-app", "mise.local.toml")
	if err := os.MkdirAll(filepath.Dir(projMise), 0o755); err != nil {
		t.Fatal(err)
	}
	const secretSentinel = "sk-never-export-no-zones-value"
	if err := os.WriteFile(projMise, []byte("[tools]\nnode = \"20\"\n\n[env]\nNO_ZONE_ENV_SECRET = \""+secretSentinel+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgOut, err := execWS(t, fx, "mise", "configs", "--no-outdated", "--json")
	if err != nil {
		t.Fatalf("ws mise configs without WS_ZONES: %v\n%s", err, cfgOut)
	}
	if !strings.Contains(cfgOut, "work (zone)") || !strings.Contains(cfgOut, "sample-app") || !strings.Contains(cfgOut, "NO_ZONE_ENV_SECRET") {
		t.Fatalf("expected global, zone, and project configs without WS_ZONES:\n%s", cfgOut)
	}
	if strings.Contains(cfgOut, secretSentinel) {
		t.Fatalf("SECURITY VIOLATION: secret value leaked into ws mise configs output")
	}
}

func TestFleetDiffAndSyncFilesCLI(t *testing.T) {
	fx := setupFixture(t)

	helpOut, err := execWS(t, fx, "fleet", "--help")
	if err != nil {
		t.Fatalf("ws fleet --help: %v\n%s", err, helpOut)
	}
	for _, want := range []string{"diff", "sync-files"} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("expected ws fleet --help to include %q:\n%s", want, helpOut)
		}
	}

	if _, err := execWS(t, fx, "fleet", "sync-files", "dev-vm", ".ssh/config"); err == nil {
		t.Fatalf("expected sync-files without --push/--pull to fail")
	}
	if _, err := execWS(t, fx, "fleet", "sync-files", "dev-vm", "--push", "--pull", ".ssh/config"); err == nil {
		t.Fatalf("expected sync-files with both --push and --pull to fail")
	}
	if _, err := execWS(t, fx, "fleet", "sync-files", "dev-vm", "--push"); err == nil {
		t.Fatalf("expected sync-files without paths or --all-diff to fail")
	}

	if err := os.MkdirAll(filepath.Join(fx.home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.home, ".ssh", "config"), []byte("Host demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execWS(t, fx, "fleet", "sync-files", "dev-vm", "--target", "dev-vm.tail.ts.net", "--push", ".ssh/config", "--json")
	if err != nil {
		t.Fatalf("ws fleet sync-files dry-run: %v\n%s", err, out)
	}
	var payload fleet.FileSyncResult
	if jerr := json.Unmarshal([]byte(out), &payload); jerr != nil {
		t.Fatalf("invalid sync-files JSON: %v\n%s", jerr, out)
	}
	if !payload.DryRun || payload.Transferred != 1 || payload.Direction != "push" {
		t.Fatalf("unexpected dry-run sync-files payload: %+v", payload)
	}
}

func TestConfigCLICommands(t *testing.T) {
	fx := setupFixture(t)

	helpOut, err := execWS(t, fx, "config", "--help")
	if err != nil || !strings.Contains(helpOut, "show") || !strings.Contains(helpOut, "set") || !strings.Contains(helpOut, "unset") {
		t.Fatalf("ws config --help failed: err=%v out=%s", err, helpOut)
	}

	if out, err := execWS(t, fx, "config", "set", "WS_OP_VAULT", "TeamVault", "--json"); err != nil || !strings.Contains(out, `"TeamVault"`) {
		t.Fatalf("ws config set failed: err=%v out=%s", err, out)
	}

	showOut, err := execWS(t, fx, "config", "show", "--json")
	if err != nil {
		t.Fatalf("ws config show --json: %v\n%s", err, showOut)
	}
	var showPayload struct {
		ConfigPath string `json:"configPath"`
		Settings   []struct {
			Key        string `json:"key"`
			Value      string `json:"value"`
			Source     string `json:"source"`
			Overridden bool   `json:"overridden"`
		} `json:"settings"`
	}
	if jerr := json.Unmarshal([]byte(showOut), &showPayload); jerr != nil {
		t.Fatalf("invalid config show JSON: %v\n%s", jerr, showOut)
	}
	foundVault := false
	for _, s := range showPayload.Settings {
		if s.Key == "WS_OP_VAULT" {
			foundVault = true
			if s.Value != "TeamVault" || s.Source != "override" || !s.Overridden {
				t.Fatalf("expected WS_OP_VAULT override in config show, got %+v", s)
			}
		}
	}
	if !foundVault {
		t.Fatalf("missing WS_OP_VAULT in ws config show output")
	}

	if _, err := execWS(t, fx, "config", "unset", "WS_OP_VAULT", "--json"); err != nil {
		t.Fatalf("ws config unset failed: %v", err)
	}
	if _, err := execWS(t, fx, "tui", "settings"); err == nil {
		t.Fatalf("expected non-interactive ws tui settings to fail cleanly rather than invalid tab error")
	}
}

func TestIsCancelledErrorAndExitCode(t *testing.T) {
	cancelErrs := []struct {
		name string
		err  error
	}{
		{name: "picker.ErrCancelled", err: picker.ErrCancelled},
		{name: "wrapped picker.ErrCancelled", err: fmt.Errorf("wrapped: %w", picker.ErrCancelled)},
		{name: "huh.ErrUserAborted", err: huh.ErrUserAborted},
		{name: "context.Canceled", err: context.Canceled},
	}

	for _, tc := range cancelErrs {
		if !IsCancelledError(tc.err) {
			t.Fatalf("expected IsCancelledError(%s) == true", tc.name)
		}
		if got := ExitCode(tc.err); got != 130 {
			t.Fatalf("expected ExitCode(%s) == 130, got %d", tc.name, got)
		}
	}

	if IsCancelledError(nil) {
		t.Fatalf("expected IsCancelledError(nil) == false")
	}
	if got := ExitCode(nil); got != 0 {
		t.Fatalf("expected ExitCode(nil) == 0, got %d", got)
	}

	normalErr := errors.New("boom")
	if IsCancelledError(normalErr) {
		t.Fatalf("expected IsCancelledError(normalErr) == false")
	}
	if got := ExitCode(normalErr); got != 1 {
		t.Fatalf("expected ExitCode(normalErr) == 1, got %d", got)
	}

	custom := &ExitCodeError{Code: 42, Err: errors.New("custom exit")}
	if got := ExitCode(custom); got != 42 {
		t.Fatalf("expected custom ExitCodeError == 42, got %d", got)
	}
}

func TestCLIUXPolishWhoamiCodexTUIHelpAndGHSubcommand(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CODEX_HOME", filepath.Join(fx.home, ".config", "codex-work"))

	helpOut, err := execWS(t, fx, "--help")
	if err != nil {
		t.Fatalf("ws --help failed: %v\n%s", err, helpOut)
	}
	if !strings.Contains(helpOut, "tui") || !strings.Contains(helpOut, "gh") {
		t.Fatalf("expected 'tui' and 'gh' in ws --help:\n%s", helpOut)
	}
	if strings.Contains(helpOut, "[projects|prs|issues|gh|mise|system") {
		t.Fatalf("expected decluttered tui usage in ws --help, got verbose tab list:\n%s", helpOut)
	}

	for _, cmdName := range []string{"repo", "doctor", "hist", "gh"} {
		out, err := execWS(t, fx, cmdName, "--help")
		if err != nil {
			t.Fatalf("ws %s --help failed: %v\n%s", cmdName, err, out)
		}
		if !strings.Contains(out, "--tui") {
			t.Fatalf("expected --tui flag on ws %s --help:\n%s", cmdName, out)
		}
	}
	histHelp, err := execWS(t, fx, "hist", "--help")
	if err != nil {
		t.Fatalf("ws hist --help failed: %v", err)
	}
	if strings.Contains(histHelp, "Filter unified command history by substring") {
		t.Fatalf("expected 'hist search' hidden from ws hist --help:\n%s", histHelp)
	}

	whoOut, err := execWS(t, fx, "whoami")
	if err != nil {
		t.Fatalf("ws whoami failed: %v\n%s", err, whoOut)
	}
	if !strings.Contains(whoOut, "Codex Home:") {
		t.Fatalf("expected 'Codex Home:' in ws whoami output:\n%s", whoOut)
	}

	ghHelp, err := execWS(t, fx, "gh", "--help")
	if err != nil {
		t.Fatalf("ws gh --help failed: %v\n%s", ghHelp, ghHelp)
	}
	for _, flag := range []string{"--repo", "--state", "--zone", "--all", "--list"} {
		if !strings.Contains(ghHelp, flag) {
			t.Fatalf("expected flag %q in ws gh --help:\n%s", flag, ghHelp)
		}
	}
}

func TestPkgUntrackedAddUntrackCLI(t *testing.T) {
	fx := setupFixture(t)

	helpOut, err := execWS(t, fx, "pkg", "untracked", "--help")
	if err != nil || !strings.Contains(helpOut, "--preset") {
		t.Fatalf("ws pkg untracked --help failed: err=%v out=%s", err, helpOut)
	}

	addOut, err := execWS(t, fx, "pkg", "add", "--no-install", "--file", "pkg/server.txt", "hyperfine", "--json")
	if err != nil || !strings.Contains(addOut, "hyperfine") {
		t.Fatalf("ws pkg add --no-install failed: err=%v out=%s", err, addOut)
	}
	raw, err := os.ReadFile(filepath.Join(fx.repo, "pkg", "server.txt"))
	if err != nil || !strings.Contains(string(raw), "hyperfine") {
		t.Fatalf("expected hyperfine in pkg/server.txt, err=%v raw=%q", err, string(raw))
	}

	rmOut, err := execWS(t, fx, "pkg", "untrack", "hyperfine", "--json")
	if err != nil || !strings.Contains(rmOut, `"removed": true`) {
		t.Fatalf("ws pkg untrack failed: err=%v out=%s", err, rmOut)
	}
	rawAfter, err := os.ReadFile(filepath.Join(fx.repo, "pkg", "server.txt"))
	if err != nil || strings.Contains(string(rawAfter), "hyperfine") {
		t.Fatalf("expected hyperfine removed from pkg/server.txt, err=%v raw=%q", err, string(rawAfter))
	}
}

func TestPkgSearchInfoInstallRemoveCLI(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	for _, sub := range []string{"search", "info", "install", "remove"} {
		out, err := execWS(t, fx, "pkg", sub, "--help")
		if err != nil || strings.TrimSpace(out) == "" {
			t.Fatalf("ws pkg %s --help failed: err=%v out=%s", sub, err, out)
		}
	}

	if _, err := execWS(t, fx, "pkg", "search", "--non-interactive"); err == nil {
		t.Fatalf("expected non-interactive 'ws pkg search' with no args to fail")
	}
	if _, err := execWS(t, fx, "pkg", "info", "--non-interactive"); err == nil {
		t.Fatalf("expected non-interactive 'ws pkg info' with no args to fail")
	}

	instOut, err := execWS(t, fx, "pkg", "install", "--dry-run", "--provider", "brew", "--track", "--file", "pkg/server.txt", "fd", "--json")
	if err != nil || !strings.Contains(instOut, "brew install fd") {
		t.Fatalf("ws pkg install --dry-run failed: err=%v out=%s", err, instOut)
	}

	if _, err := execWS(t, fx, "pkg", "remove", "--non-interactive", "--provider", "brew", "fd"); err == nil || !strings.Contains(err.Error(), "--allow remove") {
		t.Fatalf("expected non-interactive 'ws pkg remove' without --allow remove to fail, got err=%v", err)
	}
	if _, err := execWS(t, fx, "pkg", "remove", "--json", "--provider", "brew", "fd"); err == nil || !strings.Contains(err.Error(), "--allow remove") {
		t.Fatalf("expected --json remove without --allow remove to refuse instead of prompting, got %v", err)
	}
	rmOut, err := execWS(t, fx, "pkg", "remove", "--dry-run", "--provider", "brew", "fd", "--json")
	if err != nil || !strings.Contains(rmOut, `"dryRun": true`) || !strings.Contains(rmOut, "brew uninstall fd") {
		t.Fatalf("ws pkg remove --dry-run failed: err=%v out=%s", err, rmOut)
	}
}

func TestUpdateDryRunDedupsDirtyRepoWarningAndShowsFinalize(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")

	stubBin := t.TempDir()
	gitStub := `#!/bin/sh
if [ "$1" = "-C" ] && [ "$3" = "status" ]; then
  echo " M presets.toml"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(stubBin, "git"), []byte(gitStub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubBin)

	out, err := execWS(t, fx, "update", "-n")
	if err != nil {
		t.Fatalf("ws update -n failed: %v\n%s", err, out)
	}
	if got := strings.Count(strings.ToLower(out), "uncommitted"); got != 1 {
		t.Fatalf("expected 'uncommitted' notice exactly once in dry-run output, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "[finalize] Re-sync dotfile symlinks and rendered theme templates (ws apply)") {
		t.Fatalf("expected finalize ws apply step in dry-run output:\n%s", out)
	}
}

func TestUpdateLiveOutputAndPureJSON(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", fx.home)

	if err := os.WriteFile(filepath.Join(fx.repo, "pkg", "server.txt"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.repo, "presets.toml"), []byte(`
[preset.base]
description = "Minimal baseline"
links = ["links/common.links"]
packages = ["pkg/server.txt"]
secrets = "none"
tags = ["core", "cli"]

[preset.ubuntu-runner]
description = "Headless Ubuntu runner"
include = ["base"]
exclude = ["desktop", "gui"]
when = { os = "linux", distro = ["ubuntu"], headless = true }
links = ["links/server.links"]
packages = ["pkg/server.txt"]
secrets = "none"
tags = ["core", "cli", "headless"]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	stubBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubBin, "git"), []byte("#!/bin/sh\nif [ \"$1\" = \"-C\" ] && [ \"$3\" = \"status\" ]; then echo \" M presets.toml\"; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubBin, "tldr"), []byte("#!/bin/sh\necho 'tldr: cache refreshed'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubBin)

	liveOut, err := execWS(t, fx, "--preset", "ubuntu-runner", "update")
	if err != nil {
		t.Fatalf("ws update failed: %v\n%s", err, liveOut)
	}
	if strings.Contains(liveOut, "\x1b") {
		t.Fatalf("expected zero ANSI escapes in non-TTY ws update output:\n%s", liveOut)
	}
	for _, want := range []string{
		"INFO  Checking workstation updates",
		"preset=ubuntu-runner",
		"INFO  Update digest",
		"User tools",
		"› Update tldr page cache",
		"│ tldr: cache refreshed",
		"✓ Update tldr page cache",
		"Finalize",
		"Re-sync dotfile symlinks and rendered theme templates (ws apply)",
		"DONE  Update complete",
	} {
		if !strings.Contains(liveOut, want) {
			t.Fatalf("expected %q in live ws update output:\n%s", want, liveOut)
		}
	}

	jsonOut, err := execWS(t, fx, "--preset", "ubuntu-runner", "update", "--json")
	if err != nil {
		t.Fatalf("ws update --json failed: %v\n%s", err, jsonOut)
	}
	if strings.Contains(jsonOut, "INFO") || strings.Contains(jsonOut, "DONE") || strings.Contains(jsonOut, "Checking workstation updates") {
		t.Fatalf("ws update --json polluted stdout with progress text:\n%s", jsonOut)
	}
	var parsed map[string]any
	if jerr := json.Unmarshal([]byte(jsonOut), &parsed); jerr != nil {
		t.Fatalf("ws update --json emitted invalid JSON: %v\n%s", jerr, jsonOut)
	}
}

func TestUpdateConsentSkippedExitCodeAndInteractivePrompt(t *testing.T) {
	fx := setupFixture(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", fx.home)

	conflictsPath := filepath.Join(fx.repo, "os", "arch", "conflicts.sh")
	if err := os.MkdirAll(filepath.Dir(conflictsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictsPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan := &engine.Plan{
		Verb:   "update",
		Title:  "Arch consent test",
		Preset: "arch-desktop",
		Actions: []engine.Action{
			{
				ID:            "os_exec:os/arch/conflicts.sh",
				Kind:          engine.ActionOSExec,
				Target:        conflictsPath,
				Source:        "os/arch/conflicts.sh",
				Summary:       "Run OS hook os/arch/conflicts.sh",
				Risk:          engine.RiskMedium,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"bash", conflictsPath},
				Metadata:      map[string]string{"phase": "os-pkg", "hook": "os/arch/conflicts.sh"},
			},
			{
				ID:            "upgrade:paru",
				Kind:          engine.ActionUpgrade,
				Target:        "paru",
				Summary:       "Upgrade official and AUR packages via paru",
				Risk:          engine.RiskDangerous,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"paru", "-Syu", "--noconfirm"},
				Metadata:      map[string]string{"phase": "os-pkg", "provider": "paru"},
			},
			{
				ID:            "update:paccache-keep",
				Kind:          engine.ActionUpgrade,
				Target:        "paccache",
				Summary:       "Prune pacman package cache (keep 2 latest versions)",
				Risk:          engine.RiskSafe,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"sudo", "paccache", "-rk2"},
				Metadata:      map[string]string{"phase": "os-pkg", "provider": "pacman"},
			},
		},
	}
	planPath := filepath.Join(t.TempDir(), "arch-update.json")
	if err := plan.Save(planPath); err != nil {
		t.Fatal(err)
	}

	stubBin := t.TempDir()
	for _, bin := range []string{"paru", "sudo", "bash"} {
		if err := os.WriteFile(filepath.Join(stubBin, bin), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", stubBin)

	outSkip, err := execWS(t, fx, "update", "--plan", planPath)
	if err == nil || ExitCode(err) == 0 {
		t.Fatalf("expected non-zero exit when sudo steps are skipped, got err=%v\n%s", err, outSkip)
	}
	if !strings.Contains(outSkip, "3 steps skipped, rerun with --allow sudo") {
		t.Fatalf("expected clear skipped-steps rerun hint in output:\n%s", outSkip)
	}
	if strings.Contains(outSkip, "Update complete") {
		t.Fatalf("must not print Update complete when steps were skipped:\n%s", outSkip)
	}

	origConfirm := confirmAllowScope
	t.Cleanup(func() { confirmAllowScope = origConfirm })

	var prompted []string
	confirmAllowScope = func(_ *cobra.Command, _ *app.Context, scope string, _ []string) (bool, error) {
		prompted = append(prompted, scope)
		return false, nil
	}
	var declineBuf bytes.Buffer
	declineOpts := &GlobalOpts{Repo: fx.repo, Home: fx.home, ForceInteractive: true}
	declineCmd := NewRootCmd(declineOpts)
	declineCmd.SetOut(&declineBuf)
	declineCmd.SetErr(&declineBuf)
	declineCmd.SetArgs([]string{"update", "--plan", planPath})
	errDecline := declineCmd.Execute()
	if errDecline == nil || ExitCode(errDecline) == 0 {
		t.Fatalf("expected non-zero exit when user declines sudo prompt, got err=%v\n%s", errDecline, declineBuf.String())
	}
	if !slices.Equal(prompted, []string{"sudo"}) {
		t.Fatalf("expected exactly one prompt for sudo, got %v", prompted)
	}
	if !strings.Contains(declineBuf.String(), "3 steps skipped, rerun with --allow sudo") {
		t.Fatalf("expected skipped-steps message on declined prompt:\n%s", declineBuf.String())
	}

	prompted = nil
	confirmAllowScope = func(_ *cobra.Command, _ *app.Context, scope string, _ []string) (bool, error) {
		prompted = append(prompted, scope)
		return true, nil
	}
	var allowBuf bytes.Buffer
	allowOpts := &GlobalOpts{Repo: fx.repo, Home: fx.home, ForceInteractive: true}
	allowCmd := NewRootCmd(allowOpts)
	allowCmd.SetOut(&allowBuf)
	allowCmd.SetErr(&allowBuf)
	allowCmd.SetArgs([]string{"update", "--plan", planPath})
	if errAllow := allowCmd.Execute(); errAllow != nil {
		t.Fatalf("expected approved sudo update to succeed, got err=%v\n%s", errAllow, allowBuf.String())
	}
	if !slices.Equal(prompted, []string{"sudo"}) {
		t.Fatalf("expected exactly one prompt for sudo on approve, got %v", prompted)
	}
	if !strings.Contains(allowBuf.String(), "Update complete") {
		t.Fatalf("expected Update complete on approved run:\n%s", allowBuf.String())
	}
}

func TestTUIBackendPkgAndUpdateExecCmds(t *testing.T) {
	fx := setupFixture(t)
	ctx, err := app.NewContext(app.Options{RepoRoot: fx.repo, HomeDir: fx.home})
	if err != nil {
		t.Fatal(err)
	}
	b := NewTUIBackend(ctx)
	defer func() { _ = b.Close() }()

	updCmd, err := b.UpdateAllExecCmd(true)
	if err != nil || updCmd == nil {
		t.Fatalf("UpdateAllExecCmd(true): cmd=%v err=%v", updCmd, err)
	}
	if !slices.Contains(updCmd.Args, "--allow") || !slices.Contains(updCmd.Args, "sudo") ||
		!slices.Contains(updCmd.Args, "update") || !slices.Contains(updCmd.Args, "--all") {
		t.Fatalf("unexpected UpdateAllExecCmd args: %v", updCmd.Args)
	}

	rmCmd, err := b.PackageActionExecCmd("remove", []tui.PresetToolRow{
		{ID: "bat", PackageName: "bat", Provider: "paru", Updates: "manager"},
	})
	if err != nil || rmCmd == nil {
		t.Fatalf("PackageActionExecCmd(remove): cmd=%v err=%v", rmCmd, err)
	}
	if !slices.Contains(rmCmd.Args, "remove,sudo") || !slices.Contains(rmCmd.Args, "pkg") ||
		!slices.Contains(rmCmd.Args, "remove") || !slices.Contains(rmCmd.Args, "bat") {
		t.Fatalf("unexpected PackageActionExecCmd(remove) args: %v", rmCmd.Args)
	}

	multiCmd, err := b.PackageActionExecCmd("update", []tui.PresetToolRow{
		{ID: "bat", PackageName: "bat", Provider: "brew", Updates: "manager"},
		{ID: "ghostty", PackageName: "ghostty", Provider: "cask", Updates: "manager"},
	})
	if err != nil || multiCmd == nil || len(multiCmd.Args) < 3 || multiCmd.Args[0] != "sh" {
		t.Fatalf("expected multi-provider PackageActionExecCmd to chain via sh -c, got %v err=%v", multiCmd, err)
	}
	if !strings.Contains(multiCmd.Args[2], "&&") || !strings.Contains(multiCmd.Args[2], "'brew'") || !strings.Contains(multiCmd.Args[2], "'cask'") {
		t.Fatalf("unexpected multi-provider shell script: %s", multiCmd.Args[2])
	}

	selfCmd, err := b.PackageActionExecCmd("update", []tui.PresetToolRow{
		{ID: "claude-code", PackageName: "claude-code", Provider: "cask", Updates: "self"},
	})
	if err != nil || selfCmd != nil {
		t.Fatalf("expected self-updated package to return nil ExecCmd, got cmd=%v err=%v", selfCmd, err)
	}

	addCmd, err := b.AddAndTrackPackageExecCmd("ripgrep", "paru", "pkg/arch.txt", true)
	if err != nil || addCmd == nil {
		t.Fatalf("AddAndTrackPackageExecCmd(install=true): cmd=%v err=%v", addCmd, err)
	}
	if !slices.Contains(addCmd.Args, "--track") || !slices.Contains(addCmd.Args, "pkg/arch.txt") || !slices.Contains(addCmd.Args, "ripgrep") {
		t.Fatalf("unexpected AddAndTrackPackageExecCmd args: %v", addCmd.Args)
	}
	trackOnlyCmd, err := b.AddAndTrackPackageExecCmd("ripgrep", "paru", "pkg/arch.txt", false)
	if err != nil || trackOnlyCmd != nil {
		t.Fatalf("expected track-only AddAndTrackPackageExecCmd to return nil, got cmd=%v err=%v", trackOnlyCmd, err)
	}

	ctxDry := *ctx
	ctxDry.DryRun = true
	bDry := NewTUIBackend(&ctxDry)
	defer func() { _ = bDry.Close() }()
	if c, err := bDry.UpdateAllExecCmd(true); err != nil || c != nil {
		t.Fatalf("expected dry-run UpdateAllExecCmd to return nil, got cmd=%v err=%v", c, err)
	}
	if c, err := bDry.PackageActionExecCmd("install", []tui.PresetToolRow{{ID: "bat", Provider: "paru"}}); err != nil || c != nil {
		t.Fatalf("expected dry-run PackageActionExecCmd to return nil, got cmd=%v err=%v", c, err)
	}
}
