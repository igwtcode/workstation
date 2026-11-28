package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

const testGruvboxDark = `name = "gruvbox-dark"
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

const testNordDark = `name = "nord-dark"
mode = "dark"
bg = "#2e3440"
bg_alt = "#3b4252"
fg = "#eceff4"
fg_dim = "#4c566a"
accent = "#88c0d0"
selection_bg = "#434c5e"
selection_fg = "#eceff4"
cursor = "#d8dee9"
border = "#4c566a"
color0 = "#3b4252"
color1 = "#bf616a"
color2 = "#a3be8c"
color3 = "#ebcb8b"
color4 = "#81a1c1"
color5 = "#b48ead"
color6 = "#88c0d0"
color7 = "#e5e9f0"
color8 = "#4c566a"
color9 = "#bf616a"
color10 = "#a3be8c"
color11 = "#ebcb8b"
color12 = "#81a1c1"
color13 = "#b48ead"
color14 = "#8fbcbb"
color15 = "#eceff4"
`

type syntheticFixture struct {
	repo string
	home string
	ctx  *app.Context
}

func newSyntheticFixture(t *testing.T) syntheticFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")

	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	mustWrite("themes/gruvbox/dark.toml", testGruvboxDark)
	mustWrite("themes/nord/dark.toml", testNordDark)

	hookMarker := filepath.Join(root, "hook-count.log")
	mustWrite("themes/registry", strings.Join([]string{
		"# test registry",
		"config/fzf/colors-template.sh config/fzf/colors.sh -",
		"config/tmux/colors-template.conf config/tmux/colors.conf echo hit >> " + hookMarker,
		"config/herdr/config-template.toml config/herdr/config.toml -",
		"config/herdr-app/colors-template.json config/herdr-app/colors.json -",
	}, "\n")+"\n")

	mustWrite("config/fzf/colors-template.sh", "export FZF_BG={{bg}}\nexport FZF_BG_HEX={{bg_strip}}\n")
	mustWrite("config/tmux/colors-template.conf", "set -g status-bg '{{bg}}'\nset -g status-fg '{{fg}}'\n")
	mustWrite("config/herdr/config-template.toml", "accent = \"{{accent}}\"\n")
	mustWrite("config/herdr-app/colors-template.json", "{\"bg\": \"{{bg}}\"}\n")
	mustWrite("config/zsh/zshenv", "export FOO=bar\n")

	mustWrite("links/common.links", strings.Join([]string{
		"config/zsh/zshenv ~/.zshenv",
		"config/tmux ~/.config/tmux",
		"config/fzf ~/.config/fzf",
	}, "\n")+"\n")

	mustWrite("pkg/base.txt", "git\n")

	mustWrite("presets.toml", strings.Join([]string{
		"[preset.base]",
		`description = "Base test preset"`,
		`links = ["links/common.links"]`,
		`packages = ["pkg/base.txt"]`,
		`extra_tools = ["claude-code", "mytool"]`,
		`secrets = "none"`,
		"",
		"[preset.linux-only]",
		`description = "Linux only preset"`,
		`include = ["base"]`,
		`when = { os = "linux", distro = ["arch"] }`,
	}, "\n")+"\n")

	mustWrite("tools.toml", strings.Join([]string{
		"[tool.claude-code]",
		`bin = "claude"`,
		`updates = "self"`,
		`conflicts = ["cask:claude-code", "cask:claude-code@latest"]`,
		`install = [{ via = "script", url = "https://example.invalid/install.sh", tofu = true }]`,
		"",
		"[tool.mytool]",
		`bin = "mytool"`,
		`updates = "manager"`,
		`install = [{ via = "brew", name = "mytool" }]`,
	}, "\n")+"\n")

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}

	ctx := &app.Context{
		RepoRoot:  repo,
		HomeDir:   home,
		WSHome:    filepath.Join(home, ".ws"),
		ConfigDir: filepath.Join(home, ".config"),
		DataDir:   filepath.Join(home, ".local", "share"),
		StateDir:  filepath.Join(home, ".local", "state"),
		CacheDir:  filepath.Join(home, ".cache"),
		Host: app.HostInfo{
			OS:            "darwin",
			Distro:        "macos",
			Arch:          "arm64",
			DefaultPreset: "base",
		},
		LocalEnv: map[string]string{},
	}

	return syntheticFixture{repo: repo, home: home, ctx: ctx}
}

func TestRenderTemplateStringAndHash(t *testing.T) {
	f := newSyntheticFixture(t)
	pal, err := config.LoadPalette(f.repo, "gruvbox-dark")
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}

	got, err := RenderTemplateString("bg={{bg}} strip={{bg_strip}} name={{name}}\n\n", pal)
	if err != nil {
		t.Fatalf("RenderTemplateString: %v", err)
	}
	want := "bg=#282828 strip=282828 name=gruvbox-dark\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	manual := config.Palette{Tokens: map[string]string{"bg": "#112233"}}
	gotManual, err := RenderTemplateString("{{bg_strip}}", manual)
	if err != nil || gotManual != "112233\n" {
		t.Fatalf("manual _strip: got %q, err=%v", gotManual, err)
	}

	if _, err := RenderTemplateString("bad={{nope}}", pal); err == nil {
		t.Fatalf("expected error on unknown token")
	}
	if _, err := RenderTemplateString("bad={{bg", pal); err == nil {
		t.Fatalf("expected error on unterminated {{")
	}
	if _, err := RenderTemplateString("bad={{}}", pal); err == nil {
		t.Fatalf("expected error on empty token")
	}

	if h := SHA256Hex([]byte("hello\n")); h != "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03" {
		t.Fatalf("unexpected SHA256Hex: %s", h)
	}
}

func TestUnifiedDiff(t *testing.T) {
	if d := UnifiedDiff("a", "b", "same\n", "same\n"); d != "" {
		t.Fatalf("expected empty diff for equal text, got %q", d)
	}

	oldText := "line1\nline2\nline3\nline4\n"
	newText := "line1\nline2-mod\nline3\nline4\nline5\n"
	diff := UnifiedDiff("old.txt", "new.txt", oldText, newText)
	for _, want := range []string{"--- old.txt", "+++ new.txt", "-line2", "+line2-mod", "+line5"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q:\n%s", want, diff)
		}
	}

	newFileDiff := UnifiedDiff("/dev/null", "out.txt", "", "alpha\nbeta\n")
	if !strings.Contains(newFileDiff, "+alpha\n+beta\n") {
		t.Fatalf("unexpected new file diff:\n%s", newFileDiff)
	}
}

func TestCheckTemplatesStates(t *testing.T) {
	f := newSyntheticFixture(t)

	statuses, err := CheckTemplates(f.ctx, "gruvbox-dark")
	if err != nil {
		t.Fatalf("CheckTemplates initial: %v", err)
	}
	if len(statuses) != 4 {
		t.Fatalf("expected 4 statuses, got %d", len(statuses))
	}
	for _, st := range statuses {
		if st.State != "missing" {
			t.Fatalf("expected missing for %s, got %s", st.App, st.State)
		}
	}

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply: %v", err)
	}
	if _, err := ExecApply(f.ctx, plan); err != nil {
		t.Fatalf("ExecApply: %v", err)
	}

	statuses, err = CheckTemplates(f.ctx, "gruvbox-dark")
	if err != nil {
		t.Fatalf("CheckTemplates after apply: %v", err)
	}
	for _, st := range statuses {
		if st.State != "ok" {
			t.Fatalf("expected ok for %s, got %s", st.App, st.State)
		}
	}

	fzfOut := filepath.Join(f.repo, "config/fzf/colors.sh")
	if err := os.WriteFile(fzfOut, []byte("# hand edited\n"), 0o644); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	statuses, err = CheckTemplates(f.ctx, "nord-dark")
	if err != nil {
		t.Fatalf("CheckTemplates nord-dark: %v", err)
	}
	byApp := map[string]TemplateStatus{}
	for _, st := range statuses {
		byApp[st.App] = st
	}
	if byApp["fzf"].State != "modified" {
		t.Fatalf("expected fzf modified, got %s", byApp["fzf"].State)
	}
	if byApp["tmux"].State != "stale" {
		t.Fatalf("expected tmux stale, got %s", byApp["tmux"].State)
	}
}

func TestLinksPlanAndApply(t *testing.T) {
	f := newSyntheticFixture(t)
	presets, err := config.LoadPresets(f.repo)
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	resolved, err := config.ResolvePreset(f.repo, presets, "base")
	if err != nil {
		t.Fatalf("ResolvePreset: %v", err)
	}

	zshenv := filepath.Join(f.home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("old zshenv\n"), 0o600); err != nil {
		t.Fatalf("write zshenv: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(f.home, ".config"), 0o755); err != nil {
		t.Fatalf("mkdir .config: %v", err)
	}
	if err := os.Symlink("/nonexistent/other", filepath.Join(f.home, ".config", "tmux")); err != nil {
		t.Fatalf("symlink tmux: %v", err)
	}
	if err := os.Symlink(filepath.Join(f.repo, "config/fzf"), filepath.Join(f.home, ".config", "fzf")); err != nil {
		t.Fatalf("symlink fzf: %v", err)
	}

	entries, err := config.LoadLinksForPreset(f.ctx.RepoRoot, resolved)
	if err != nil {
		t.Fatalf("LoadLinksForPreset: %v", err)
	}
	plan, skipped, err := planLinkEntriesWithPlanned(f.ctx, resolved.ID, entries, nil)
	if err != nil {
		t.Fatalf("planLinkEntriesWithPlanned: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != "~/.config/fzf" {
		t.Fatalf("unexpected skipped: %v", skipped)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("expected 2 link actions, got %d", len(plan.Actions))
	}

	byTarget := map[string]Action{}
	for _, a := range plan.Actions {
		byTarget[a.Target] = a
	}
	if byTarget[zshenv].RequiresAllow != "replace" {
		t.Fatalf("expected zshenv RequiresAllow=replace, got %q", byTarget[zshenv].RequiresAllow)
	}
	if byTarget[zshenv].Metadata["state"] != "replaced" {
		t.Fatalf("expected zshenv state=replaced, got %q", byTarget[zshenv].Metadata["state"])
	}
	tmuxTarget := filepath.Join(f.home, ".config", "tmux")
	if byTarget[tmuxTarget].Metadata["state"] != "relinked" {
		t.Fatalf("expected tmux state=relinked, got %q", byTarget[tmuxTarget].Metadata["state"])
	}

	var tx app.UndoTx
	if _, err := ApplySymlink(filepath.Join(f.repo, "config/zsh/zshenv"), zshenv, false, &tx); err == nil {
		t.Fatalf("expected refusal when allowReplace=false")
	}

	state, err := ApplySymlink(filepath.Join(f.repo, "config/zsh/zshenv"), zshenv, true, &tx)
	if err != nil {
		t.Fatalf("ApplySymlink replace: %v", err)
	}
	if state != "replaced" {
		t.Fatalf("got state %q, want replaced", state)
	}
	if len(tx.Ops) != 1 || tx.Ops[0].Kind != "restore_backup" || tx.Ops[0].BackupPath != zshenv+".pre-ws" {
		t.Fatalf("unexpected undo op: %+v", tx.Ops)
	}
	backupRaw, err := os.ReadFile(zshenv + ".pre-ws")
	if err != nil || string(backupRaw) != "old zshenv\n" {
		t.Fatalf("backup content mismatch: %q, err=%v", string(backupRaw), err)
	}
}

func TestPlanApplyExecDryRunAllowAndUndo(t *testing.T) {
	f := newSyntheticFixture(t)
	for _, dir := range []string{
		filepath.Join(f.repo, "config", "tmux", "plugins", "tpm"),
		filepath.Join(f.ctx.DataDir, "gh", "extensions", "gh-dash"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WS_EXCLUDE_PATHS", "go")
	hookLog := filepath.Join(filepath.Dir(f.repo), "hook-count.log")

	zshenv := filepath.Join(f.home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write zshenv: %v", err)
	}

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply: %v", err)
	}

	var renders, links, hooks int
	for _, a := range plan.Actions {
		switch a.Kind {
		case ActionRender:
			renders++
		case ActionSymlink:
			links++
		case ActionHook:
			hooks++
		}
	}
	if renders != 4 || links != 3 || hooks != 1 {
		t.Fatalf("unexpected counts renders=%d links=%d hooks=%d", renders, links, hooks)
	}

	f.ctx.DryRun = true
	dryRes, err := ExecApply(f.ctx, plan)
	if err != nil {
		t.Fatalf("ExecApply dry-run: %v", err)
	}
	if dryRes.Applied != 0 {
		t.Fatalf("dry-run applied=%d, want 0", dryRes.Applied)
	}
	if _, err := os.Stat(hookLog); !os.IsNotExist(err) {
		t.Fatalf("hook ran during dry-run")
	}
	if _, err := os.Stat(filepath.Join(f.repo, "config/fzf/colors.sh")); !os.IsNotExist(err) {
		t.Fatalf("render wrote file during dry-run")
	}
	f.ctx.DryRun = false

	f.ctx.NonInteractive = true
	blockedPlan := *plan
	blockedPlan.Actions = nil
	blockedPlan.Theme = ""
	blockedPlan.Preset = ""
	for _, a := range plan.Actions {
		if a.Kind == ActionSymlink && a.Target == zshenv {
			blockedPlan.Actions = append(blockedPlan.Actions, a)
		}
	}
	blockedRes, err := ExecApply(f.ctx, &blockedPlan)
	if err != nil {
		t.Fatalf("expected consent skip, not error, without Allow.Replace: %v", err)
	}
	if blockedRes.ConsentSkipped != 1 || blockedRes.Applied != 0 || !slices.Equal(blockedRes.MissingAllows, []string{"replace"}) {
		t.Fatalf("expected one replace consent skip, got %+v", blockedRes)
	}
	if !strings.Contains(strings.Join(blockedRes.Warnings, "\n"), "kept existing") {
		t.Fatalf("expected kept existing warning, got %v", blockedRes.Warnings)
	}
	if raw, rerr := os.ReadFile(zshenv); rerr != nil || string(raw) != "original\n" {
		t.Fatalf("blocked apply touched existing zshenv: %q err=%v", string(raw), rerr)
	}

	f.ctx.Allow.Replace = true
	res, err := ExecApply(f.ctx, plan)
	if err != nil {
		t.Fatalf("ExecApply with Allow.Replace: %v", err)
	}
	if res.Applied != 8 || res.UndoID == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	hookBytes, err := os.ReadFile(hookLog)
	if err != nil || strings.Count(string(hookBytes), "hit") != 1 {
		t.Fatalf("expected hook to run once, got %q err=%v", string(hookBytes), err)
	}

	plan2, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply idempotent: %v", err)
	}
	if len(plan2.Actions) != 0 {
		t.Fatalf("expected 0 actions on idempotent PlanApply, got %d: %+v", len(plan2.Actions), plan2.Actions)
	}

	ledger, err := app.LoadLedger(f.ctx.WSBaseDir())
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	if ledger.ActiveTheme != "gruvbox-dark" || ledger.ActivePreset != "base" {
		t.Fatalf("unexpected ledger active state: theme=%q preset=%q", ledger.ActiveTheme, ledger.ActivePreset)
	}
	if len(ledger.UndoLog) != 1 || len(ledger.RunJournal) != 1 {
		t.Fatalf("unexpected undo/journal lengths: %d / %d", len(ledger.UndoLog), len(ledger.RunJournal))
	}

	planNord, err := PlanApply(f.ctx, "", "nord-dark")
	if err != nil {
		t.Fatalf("PlanApply nord-dark: %v", err)
	}
	if _, err := ExecApply(f.ctx, planNord); err != nil {
		t.Fatalf("ExecApply nord-dark: %v", err)
	}
	fzfContent, _ := os.ReadFile(filepath.Join(f.repo, "config/fzf/colors.sh"))
	if !strings.Contains(string(fzfContent), "#2e3440") {
		t.Fatalf("expected nord bg in fzf output, got %q", string(fzfContent))
	}

	if msg, err := UndoLast(f.ctx); err != nil {
		t.Fatalf("UndoLast nord-dark: %v", err)
	} else if !strings.Contains(msg, "undid") {
		t.Fatalf("unexpected undo msg: %q", msg)
	}
	fzfContent, _ = os.ReadFile(filepath.Join(f.repo, "config/fzf/colors.sh"))
	if !strings.Contains(string(fzfContent), "#282828") {
		t.Fatalf("expected gruvbox bg after undo, got %q", string(fzfContent))
	}
	ledgerAfterNordUndo, err := app.LoadLedger(f.ctx.WSBaseDir())
	if err != nil || ledgerAfterNordUndo.ActiveTheme != "gruvbox-dark" {
		t.Fatalf("expected ActiveTheme restored to gruvbox-dark after undo, got %q (err=%v)", ledgerAfterNordUndo.ActiveTheme, err)
	}

	if _, err := UndoLast(f.ctx); err != nil {
		t.Fatalf("UndoLast initial: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "config/fzf/colors.sh")); !os.IsNotExist(err) {
		t.Fatalf("expected fzf output removed on undo")
	}
	fi, err := os.Lstat(zshenv)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected ~/.zshenv restored as regular file, err=%v mode=%v", err, fi)
	}
	restored, _ := os.ReadFile(zshenv)
	if string(restored) != "original\n" {
		t.Fatalf("restored ~/.zshenv mismatch: %q", string(restored))
	}
	if _, err := os.Lstat(zshenv + ".pre-ws"); !os.IsNotExist(err) {
		t.Fatalf("expected .pre-ws backup moved back")
	}
	if _, err := UndoLast(f.ctx); err == nil {
		t.Fatalf("expected error on empty undo log")
	}
}

func TestPlanApplyOnlyApp(t *testing.T) {
	f := newSyntheticFixture(t)
	plan, err := PlanApply(f.ctx, "tmux", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply --only tmux: %v", err)
	}

	if len(plan.Actions) != 3 {
		t.Fatalf("expected 3 actions for --only tmux, got %d: %+v", len(plan.Actions), plan.Actions)
	}
	if _, err := PlanApply(f.ctx, "nonexistent-app", "gruvbox-dark"); err == nil {
		t.Fatalf("expected error for unknown --only app")
	}
}

func TestEditResolveAndExecAfterSave(t *testing.T) {
	f := newSyntheticFixture(t)
	hookLog := filepath.Join(filepath.Dir(f.repo), "hook-count.log")

	e, err := ResolveAppTemplate(f.repo, "herdr")
	if err != nil || e.App != "herdr" {
		t.Fatalf("exact match herdr failed: %+v, %v", e, err)
	}

	e, err = ResolveAppTemplate(f.repo, "colors-template.conf")
	if err != nil || e.App != "tmux" {
		t.Fatalf("basename match failed: %+v, %v", e, err)
	}

	e, err = ResolveAppTemplate(f.repo, "tmu")
	if err != nil || e.App != "tmux" {
		t.Fatalf("partial match failed: %+v, %v", e, err)
	}

	if _, err := ResolveAppTemplate(f.repo, "herd"); err == nil {
		t.Fatalf("expected ambiguous match error for 'herd'")
	}

	res1, err := ExecEditAfterSave(f.ctx, "tmux")
	if err != nil {
		t.Fatalf("ExecEditAfterSave 1: %v", err)
	}
	if res1.Applied != 2 {
		t.Fatalf("expected 2 actions (render+hook), got %d", res1.Applied)
	}

	if _, err := os.Stat(filepath.Join(f.repo, "config/fzf/colors.sh")); !os.IsNotExist(err) {
		t.Fatalf("ExecEditAfterSave unexpectedly rendered another app")
	}

	res2, err := ExecEditAfterSave(f.ctx, "tmux")
	if err != nil {
		t.Fatalf("ExecEditAfterSave 2: %v", err)
	}
	if res2.Applied != 0 {
		t.Fatalf("expected 0 applied on unchanged template, got %d", res2.Applied)
	}
	hookBytes, _ := os.ReadFile(hookLog)
	if strings.Count(string(hookBytes), "hit") != 1 {
		t.Fatalf("hook should have run exactly once, log=%q", string(hookBytes))
	}

	tmplPath := filepath.Join(f.repo, "config/tmux/colors-template.conf")
	if err := os.WriteFile(tmplPath, []byte("set -g status-bg '{{accent}}'\n"), 0o644); err != nil {
		t.Fatalf("edit template: %v", err)
	}
	entry, _ := ResolveAppTemplate(f.repo, "tmux")
	plan, err := EditAppPlan(f.ctx, entry, "")
	if err != nil {
		t.Fatalf("EditAppPlan: %v", err)
	}
	if len(plan.Actions) != 2 || !strings.Contains(plan.Actions[0].Diff, "+set -g status-bg '#fe8019'") {
		t.Fatalf("unexpected edit plan: %+v", plan.Actions)
	}
	if _, err := ExecEditAfterSave(f.ctx, "tmux"); err != nil {
		t.Fatalf("ExecEditAfterSave 3: %v", err)
	}
	hookBytes, _ = os.ReadFile(hookLog)
	if strings.Count(string(hookBytes), "hit") != 2 {
		t.Fatalf("expected hook to run twice, log=%q", string(hookBytes))
	}
}

func TestRunDoctorChecks(t *testing.T) {
	f := newSyntheticFixture(t)

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	mkExec := func(dir, name string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	mkExec(binDir, "mytool")
	mkExec(binDir, "claude")
	t.Setenv("PATH", binDir)

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply: %v", err)
	}
	if _, err := ExecApply(f.ctx, plan); err != nil {
		t.Fatalf("ExecApply: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(f.home, "personal"), 0o755); err != nil {
		t.Fatalf("mkdir zone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.home, "personal", "mise.toml"), []byte("[env]\n"), 0o644); err != nil {
		t.Fatalf("write zone mise.toml: %v", err)
	}
	f.ctx.LocalEnv["WS_ZONES"] = "personal|Demo User|demo@example.com\n"
	t.Setenv("WS_ZONES", "")

	cleanReport, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor clean: %v", err)
	}
	if cleanReport.LinksOK != 3 || cleanReport.LinksTotal != 3 {
		t.Fatalf("expected 3/3 links ok, got %d/%d", cleanReport.LinksOK, cleanReport.LinksTotal)
	}
	if cleanReport.TemplatesOK != 4 || cleanReport.TemplatesTotal != 4 {
		t.Fatalf("expected 4/4 templates ok, got %d/%d", cleanReport.TemplatesOK, cleanReport.TemplatesTotal)
	}
	if len(cleanReport.Findings) != 0 {
		t.Fatalf("expected 0 findings on healthy fixture, got %+v", cleanReport.Findings)
	}

	_ = os.Remove(filepath.Join(f.home, ".zshenv"))
	_ = os.Symlink(filepath.Join(f.home, "does-not-exist"), filepath.Join(f.home, ".zshenv"))

	otherDir := filepath.Join(t.TempDir(), "other-tmux")
	_ = os.MkdirAll(otherDir, 0o755)
	_ = os.Remove(filepath.Join(f.home, ".config", "tmux"))
	_ = os.Symlink(otherDir, filepath.Join(f.home, ".config", "tmux"))

	_ = os.WriteFile(filepath.Join(f.repo, "config/fzf/colors.sh"), []byte("# edited\n"), 0o644)

	_ = os.WriteFile(filepath.Join(f.home, ".zshenv.pre-ws"), []byte("old\n"), 0o644)

	brewBin := filepath.Join(t.TempDir(), "opt", "homebrew", "bin")
	mkExec(brewBin, "claude")
	t.Setenv("PATH", brewBin+string(os.PathListSeparator)+binDir)

	f.ctx.LocalEnv["WS_ZONES"] = "personal|Demo User|demo@example.com\nwork|Demo Work|work@example.com\n"

	report, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor degraded: %v", err)
	}

	gotIDs := map[string]Finding{}
	for _, finding := range report.Findings {
		gotIDs[finding.ID] = finding
	}

	wantFindings := []string{
		"link:broken:~/.zshenv",
		"link:drifted:~/.config/tmux",
		"template:modified:fzf",
		"backup:~/.zshenv.pre-ws",
		"path:self-updater-conflict:claude",
		"path:shadow:claude",
		"zone:missing:work",
	}
	for _, id := range wantFindings {
		if _, ok := gotIDs[id]; !ok {
			t.Errorf("missing expected finding %q; got %v", id, keysOf(gotIDs))
		}
	}
	if gotIDs["link:broken:~/.zshenv"].Severity != SeverityError || gotIDs["link:broken:~/.zshenv"].FixPlan == nil {
		t.Errorf("expected broken link to be error with FixPlan, got %+v", gotIDs["link:broken:~/.zshenv"])
	}
	if gotIDs["template:modified:fzf"].FixPlan == nil {
		t.Errorf("expected modified template to have FixPlan")
	}

	ledger, _ := app.LoadLedger(f.ctx.WSBaseDir())
	ledger.ActiveTheme = "missing-dark"
	ledger.ActivePreset = "no-such-preset"
	_ = app.SaveLedger(f.ctx.WSBaseDir(), ledger)

	badReport, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor invalid theme/preset: %v", err)
	}
	badIDs := map[string]Finding{}
	for _, finding := range badReport.Findings {
		badIDs[finding.ID] = finding
	}
	if _, ok := badIDs["preset:invalid"]; !ok {
		t.Errorf("expected preset:invalid finding, got %v", keysOf(badIDs))
	}
	if _, ok := badIDs["theme:invalid"]; !ok {
		t.Errorf("expected theme:invalid finding, got %v", keysOf(badIDs))
	}
}

func keysOf(m map[string]Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestExecApplyRejectsForgedPlans(t *testing.T) {
	f := newSyntheticFixture(t)
	outside := filepath.Join(t.TempDir(), "escape.txt")

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply: %v", err)
	}
	if plan.Verb != "apply" || plan.Host == "" {
		t.Fatalf("expected PlanApply to populate Verb=apply and Host, got verb=%q host=%q", plan.Verb, plan.Host)
	}

	if _, err := ExecApply(f.ctx, &Plan{Verb: "clean", Title: "wrong verb"}); err == nil {
		t.Errorf("expected ExecApply to reject plan with Verb=clean")
	}

	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Actions: []Action{
			{ID: "rm", Kind: ActionRemove, Target: filepath.Join(f.home, "victim")},
		},
	}); err == nil {
		t.Errorf("expected ExecApply to refuse ActionRemove")
	}

	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Actions: []Action{
			{ID: "exec", Kind: ActionOSExec, Command: []string{"bash", "-c", "true"}},
		},
	}); err == nil {
		t.Errorf("expected ExecApply to refuse ActionOSExec")
	}

	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Actions: []Action{
			{
				ID:       "render:escape",
				Kind:     ActionRender,
				Target:   outside,
				Metadata: map[string]string{"content": "pwn\n"},
			},
		},
	}); err == nil {
		t.Errorf("expected ExecApply to refuse render target outside RepoRoot/HomeDir")
	}

	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Actions: []Action{
			{
				ID:     "link:escape",
				Kind:   ActionSymlink,
				Source: filepath.Join(f.repo, "config/zsh/zshenv"),
				Target: outside,
			},
		},
	}); err == nil {
		t.Errorf("expected ExecApply to refuse symlink target outside HomeDir")
	}

	outsideDir := t.TempDir()
	evilParent := filepath.Join(f.home, "escape-dir")
	if err := os.Symlink(outsideDir, evilParent); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Actions: []Action{
			{
				ID:     "link:symlink-parent-escape",
				Kind:   ActionSymlink,
				Source: filepath.Join(f.repo, "config/zsh/zshenv"),
				Target: filepath.Join(evilParent, "escaped-link"),
			},
		},
	}); err == nil {
		t.Errorf("expected ExecApply to refuse symlink target through escaped parent symlink")
	}
}

func TestUndoSafetyPartialApplyAndPartialMetadata(t *testing.T) {
	f := newSyntheticFixture(t)

	fullPlan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply full: %v", err)
	}
	if _, err := ExecApply(f.ctx, fullPlan); err != nil {
		t.Fatalf("ExecApply full: %v", err)
	}

	onlyPlan, err := PlanApply(f.ctx, "tmux", "nord-dark")
	if err != nil {
		t.Fatalf("PlanApply --only tmux: %v", err)
	}
	if onlyPlan.Metadata["partial"] != "true" {
		t.Fatalf("expected partial=true metadata on --only plan, got %+v", onlyPlan.Metadata)
	}
	if _, err := ExecApply(f.ctx, onlyPlan); err != nil {
		t.Fatalf("ExecApply --only tmux: %v", err)
	}
	ledger, err := app.LoadLedger(f.ctx.WSBaseDir())
	if err != nil {
		t.Fatal(err)
	}
	if ledger.ActiveTheme != "gruvbox-dark" {
		t.Fatalf("partial --only apply overwrote ActiveTheme to %q, want gruvbox-dark", ledger.ActiveTheme)
	}

	linkTarget := filepath.Join(f.home, ".config", "app-file")
	if err := os.MkdirAll(filepath.Dir(linkTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkTarget, []byte("user real file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledger.UndoLog = append(ledger.UndoLog, app.UndoTx{
		ID:        "tx-real-file-guard",
		Summary:   "test create_symlink guard",
		PrevTheme: "gruvbox-dark",
		Ops: []app.UndoOp{
			{Kind: "create_symlink", Target: linkTarget},
		},
	})
	if err := app.SaveLedger(f.ctx.WSBaseDir(), ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoLast(f.ctx); err == nil {
		t.Fatalf("expected UndoLast to refuse deleting non-symlink target")
	}
	if got, err := os.ReadFile(linkTarget); err != nil || string(got) != "user real file\n" {
		t.Fatalf("real file must remain intact after refused undo, got %q err=%v", string(got), err)
	}

	ledger, _ = app.LoadLedger(f.ctx.WSBaseDir())
	ledger.UndoLog[len(ledger.UndoLog)-1] = app.UndoTx{
		ID:        "tx-replace-guard",
		Summary:   "test replace_symlink guard",
		PrevTheme: "gruvbox-dark",
		Ops: []app.UndoOp{
			{Kind: "replace_symlink", Target: linkTarget, PrevLinkTarget: "/some/old/target"},
		},
	}
	if err := app.SaveLedger(f.ctx.WSBaseDir(), ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoLast(f.ctx); err == nil {
		t.Fatalf("expected UndoLast to refuse overwriting non-symlink on replace_symlink")
	}

	missingBackupLink := filepath.Join(f.home, ".missing-backup-target")
	if err := os.Symlink(filepath.Join(f.repo, "config/zsh/zshenv"), missingBackupLink); err != nil {
		t.Fatal(err)
	}
	ledger, _ = app.LoadLedger(f.ctx.WSBaseDir())
	ledger.UndoLog[len(ledger.UndoLog)-1] = app.UndoTx{
		ID:        "tx-missing-backup",
		Summary:   "test restore_backup guard",
		PrevTheme: "gruvbox-dark",
		Ops: []app.UndoOp{
			{
				Kind:       "restore_backup",
				Target:     missingBackupLink,
				BackupPath: missingBackupLink + ".pre-ws",
			},
		},
	}
	if err := app.SaveLedger(f.ctx.WSBaseDir(), ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoLast(f.ctx); err == nil {
		t.Fatalf("expected UndoLast to fail when backup file is missing")
	}
	if _, err := os.Lstat(missingBackupLink); err != nil {
		t.Fatalf("symlink target was removed even though backup was missing: %v", err)
	}

	f2 := newSyntheticFixture(t)

	zshenv := filepath.Join(f2.home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zshenv+".pre-ws", []byte("existing-backup\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f2.ctx.Allow.Replace = true
	partialFailPlan, err := PlanApply(f2.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply f2: %v", err)
	}
	failRes, err := ExecApply(f2.ctx, partialFailPlan)
	if err == nil {
		t.Fatalf("expected ExecApply to fail on pre-existing .pre-ws backup")
	}
	if failRes == nil || failRes.UndoID == "" {
		t.Fatalf("expected partial failure to record UndoID, got %+v", failRes)
	}
	if _, statErr := os.Stat(filepath.Join(f2.repo, "config/fzf/colors.sh")); statErr != nil {
		t.Fatalf("expected render output to exist before undo: %v", statErr)
	}
	if _, err := UndoLast(f2.ctx); err != nil {
		t.Fatalf("UndoLast after partial apply failure: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f2.repo, "config/fzf/colors.sh")); !os.IsNotExist(statErr) {
		t.Fatalf("expected UndoLast to remove partially rendered fzf output")
	}
}

func TestFreshClonePlanAndApplyWithGeneratedLinkTarget(t *testing.T) {
	f := newSyntheticFixture(t)

	commonLinks := filepath.Join(f.repo, "links", "common.links")
	raw, err := os.ReadFile(commonLinks)
	if err != nil {
		t.Fatal(err)
	}
	updatedLinks := string(raw) + "config/herdr-app/colors.json ~/.config/herdr-app/colors.json\n"
	if err := os.WriteFile(commonLinks, []byte(updatedLinks), 0o644); err != nil {
		t.Fatal(err)
	}

	genOut := filepath.Join(f.repo, "config/herdr-app/colors.json")
	if _, err := os.Lstat(genOut); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist before apply", genOut)
	}

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("fresh-clone PlanApply failed: %v", err)
	}

	f.ctx.DryRun = true
	if _, err := ExecApply(f.ctx, plan); err != nil {
		t.Fatalf("fresh-clone ExecApply dry-run failed: %v", err)
	}
	f.ctx.DryRun = false

	if _, err := ExecApply(f.ctx, plan); err != nil {
		t.Fatalf("fresh-clone ExecApply failed: %v", err)
	}
	dest, err := os.Readlink(filepath.Join(f.home, ".config", "herdr-app", "colors.json"))
	if err != nil || dest != genOut {
		t.Fatalf("expected symlink -> %s, got %q err=%v", genOut, dest, err)
	}

	if err := os.WriteFile(commonLinks, []byte(updatedLinks+"config/missing/file.conf ~/.config/missing.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanApply(f.ctx, "", "gruvbox-dark"); err == nil {
		t.Fatalf("expected PlanApply to fail on genuinely missing link source")
	}
}

func TestExecApplyHookStripsZoneEnvAndHexLowercaseParity(t *testing.T) {
	f := newSyntheticFixture(t)
	envDump := filepath.Join(t.TempDir(), "hook-env.txt")
	t.Setenv("WS_ZONE", "work")
	t.Setenv("AWS_PROFILE", "work-prod")
	t.Setenv("GH_CONFIG_DIR", "/tmp/work-gh")

	hookPlan := &Plan{
		Verb:  "apply",
		Title: "test zone env pinning",
		Host:  FormatPlanHost(f.ctx),
		Actions: []Action{
			{
				ID:      "hook:envcheck",
				Kind:    ActionHook,
				Target:  "envcheck",
				Command: []string{"bash", "-c", `printf '%s|%s|%s' "${WS_ZONE:-}" "${AWS_PROFILE:-}" "${GH_CONFIG_DIR:-}" > ` + envDump},
			},
		},
	}
	if _, err := ExecApply(f.ctx, hookPlan); err == nil {
		t.Fatalf("expected custom hook without Allow.Script to be rejected")
	}
	f.ctx.Allow.Script = true
	if _, err := ExecApply(f.ctx, hookPlan); err != nil {
		t.Fatalf("ExecApply hook: %v", err)
	}
	got, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("read env dump: %v", err)
	}
	if string(got) != "||" {
		t.Fatalf("expected zone env stripped in apply hook, got %q", string(got))
	}

	upperPalette := strings.Replace(testGruvboxDark, `bg = "#282828"`, `bg = "#AABBCC"`, 1)
	if err := os.WriteFile(filepath.Join(f.repo, "themes/gruvbox/dark.toml"), []byte(upperPalette), 0o644); err != nil {
		t.Fatal(err)
	}
	pal, err := config.LoadPalette(f.repo, "gruvbox-dark")
	if err != nil {
		t.Fatalf("LoadPalette: %v", err)
	}
	if pal.Tokens["bg_strip"] != "" {
		t.Fatalf("expected uppercase hex not to derive bg_strip, got %q", pal.Tokens["bg_strip"])
	}
	if _, err := RenderTemplateString("{{bg_strip}}", pal); err == nil {
		t.Fatalf("expected RenderTemplateString to reject bg_strip for uppercase hex")
	}
}

func TestPlanApplyRespectsWSExcludePaths(t *testing.T) {
	f := newSyntheticFixture(t)

	for _, appName := range []string{"ghostty", "kitty"} {
		tmplDir := filepath.Join(f.repo, "config", appName)
		if err := os.MkdirAll(tmplDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmplDir, "colors.tmpl"), []byte("bg={{bg}}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmplDir, "colors.conf"), []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	regRaw, err := os.ReadFile(filepath.Join(f.repo, "themes", "registry"))
	if err != nil {
		t.Fatal(err)
	}
	regUpdated := string(regRaw) + "\nconfig/ghostty/colors.tmpl config/ghostty/colors.conf -\nconfig/kitty/colors.tmpl config/kitty/colors.conf -\n"
	if err := os.WriteFile(filepath.Join(f.repo, "themes", "registry"), []byte(regUpdated), 0o644); err != nil {
		t.Fatal(err)
	}
	linksRaw, err := os.ReadFile(filepath.Join(f.repo, "links", "common.links"))
	if err != nil {
		t.Fatal(err)
	}
	linksUpdated := string(linksRaw) + "config/ghostty/colors.conf ~/.config/ghostty/colors.conf\nconfig/kitty/colors.conf ~/.config/kitty/colors.conf\n"
	if err := os.WriteFile(filepath.Join(f.repo, "links", "common.links"), []byte(linksUpdated), 0o644); err != nil {
		t.Fatal(err)
	}

	f.ctx.LocalEnv["WS_EXCLUDE_PATHS"] = "~/.config/ghostty,kitty"
	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply with WS_EXCLUDE_PATHS: %v", err)
	}
	for _, a := range plan.Actions {
		if strings.Contains(a.Target, "ghostty") || strings.Contains(a.Target, "kitty") {
			t.Fatalf("excluded app appeared in plan actions: %+v", a)
		}
	}
	skippedAll := strings.Join(plan.Skipped, "\n")
	if !strings.Contains(skippedAll, "ghostty") || !strings.Contains(skippedAll, "kitty") {
		t.Fatalf("expected skipped entries for ghostty and kitty, got: %v", plan.Skipped)
	}
}

func TestPhase2SecurityAndUndoRegression(t *testing.T) {
	f := newSyntheticFixture(t)
	t.Setenv("WS_EXCLUDE_PATHS", "go")

	if _, err := ExecApply(f.ctx, &Plan{Verb: "apply", Title: "no host"}); err == nil {
		t.Errorf("expected ExecApply to reject empty Host")
	}
	if _, err := ExecApply(f.ctx, &Plan{Verb: "apply", Host: "linux/amd64:other", Title: "wrong host"}); err == nil {
		t.Errorf("expected ExecApply to reject mismatched Host")
	}

	homeTarget := filepath.Join(f.home, ".zshrc")
	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Host: FormatPlanHost(f.ctx),
		Actions: []Action{{
			ID:     "render:forged-home",
			Kind:   ActionRender,
			Target: homeTarget,
		}},
	}); err == nil {
		t.Errorf("expected ExecApply to reject ActionRender targeting HomeDir")
	}

	outsideFile := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	escapedSrc := filepath.Join(f.repo, "config", "fzf", "escape-leaf")
	if err := os.Symlink(outsideFile, escapedSrc); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecApply(f.ctx, &Plan{
		Verb: "apply",
		Host: FormatPlanHost(f.ctx),
		Actions: []Action{{
			ID:     "link:escape-leaf",
			Kind:   ActionSymlink,
			Source: escapedSrc,
			Target: filepath.Join(f.home, ".escape-target"),
		}},
	}); err == nil {
		t.Errorf("expected ExecApply to reject symlink leaf escaping RepoRoot")
	}

	symTarget := filepath.Join(f.home, ".config", "sym-target.conf")
	_ = os.MkdirAll(filepath.Dir(symTarget), 0o755)
	_ = os.Symlink(outsideFile, symTarget)
	if err := writeFileWithUndo(symTarget, []byte("overwrite\n"), 0o644, nil); err == nil {
		t.Errorf("expected writeFileWithUndo to refuse symlink target")
	}

	createdLink := filepath.Join(f.home, ".partial-link")
	_ = os.Symlink(filepath.Join(f.repo, "links", "common.links"), createdLink)
	missingBackup := filepath.Join(f.home, ".missing.pre-ws")
	ledger, err := app.LoadLedger(f.ctx.WSBaseDir())
	if err != nil {
		t.Fatal(err)
	}
	ledger.UndoLog = append(ledger.UndoLog, app.UndoTx{
		ID:      "tx-partial",
		Summary: "partial failure test",
		Ops: []app.UndoOp{
			{Kind: "restore_backup", Target: filepath.Join(f.home, ".does-not-exist"), BackupPath: missingBackup},
			{Kind: "create_symlink", Target: createdLink, PrevExists: false},
		},
	})
	if err := app.SaveLedger(f.ctx.WSBaseDir(), ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := UndoLast(f.ctx); err == nil {
		t.Fatalf("expected UndoLast to fail on missing backup op[0]")
	}
	if _, err := os.Lstat(createdLink); !os.IsNotExist(err) {
		t.Errorf("expected op[1] create_symlink to be undone, err=%v", err)
	}
	reloaded, err := app.LoadLedger(f.ctx.WSBaseDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.UndoLog) != 1 || len(reloaded.UndoLog[0].Ops) != 1 {
		t.Fatalf("expected remaining UndoLog[0].Ops length 1 after partial failure, got %+v", reloaded.UndoLog)
	}
}

func TestPhase2SyncHooksAndDoctorParity(t *testing.T) {
	f := newSyntheticFixture(t)
	t.Setenv("WS_EXCLUDE_PATHS", "")

	stubBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubBin, 0o755); err != nil {
		t.Fatal(err)
	}
	goStub := filepath.Join(stubBin, "go")
	goScript := `#!/bin/sh
mode_file="${XDG_CONFIG_HOME}/go/telemetry/mode"
if [ "$1" = "telemetry" ] && [ "$2" = "off" ]; then
  mkdir -p "$(dirname "$mode_file")"
  printf "off\n" > "$mode_file"
  exit 0
fi
if [ "$1" = "telemetry" ]; then
  if [ -f "$mode_file" ]; then
    cat "$mode_file"
  else
    echo "local"
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(goStub, []byte(goScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+"/usr/bin:/bin")

	codexDir := filepath.Join(f.home, ".codex")
	codexInlineDir := filepath.Join(f.home, ".codex-inline")
	_ = os.MkdirAll(codexDir, 0o700)
	_ = os.MkdirAll(codexInlineDir, 0o700)
	codexCfg := filepath.Join(codexDir, "config.toml")
	codexInlineCfg := filepath.Join(codexInlineDir, "config.toml")
	if err := os.WriteFile(codexCfg, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexInlineCfg, []byte("analytics.enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	awsDir := filepath.Join(f.home, ".aws")
	_ = os.MkdirAll(awsDir, 0o700)
	if err := os.WriteFile(filepath.Join(awsDir, "work-credentials"), []byte("[default]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.ctx.LocalEnv["WS_AWS_DEFAULT_ZONE"] = "work"

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply sync hooks: %v", err)
	}
	var gotGo, gotCodex, gotAWS bool
	for _, a := range plan.Actions {
		switch {
		case a.ID == "sync:go-telemetry":
			gotGo = true
		case strings.HasPrefix(a.ID, "sync:codex-analytics:"):
			gotCodex = true
		case a.ID == "sync:aws-default-creds":
			gotAWS = true
		}
	}
	if !gotGo || !gotCodex || !gotAWS {
		t.Fatalf("expected go=%v codex=%v aws=%v in plan actions: %+v", gotGo, gotCodex, gotAWS, plan.Actions)
	}
	if !strings.Contains(strings.Join(plan.Warnings, "\n"), "inline/dotted analytics key") {
		t.Fatalf("expected inline/dotted analytics skip warning, got %v", plan.Warnings)
	}

	f.ctx.Allow.Replace = true
	res, err := ExecApply(f.ctx, plan)
	if err != nil {
		t.Fatalf("ExecApply sync hooks: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected sync hook actions to apply")
	}

	codexUpdated, _ := os.ReadFile(codexCfg)
	if !strings.Contains(string(codexUpdated), "[analytics]") || !strings.Contains(string(codexUpdated), "enabled = false") {
		t.Fatalf("codex config missing [analytics] enabled = false:\n%s", string(codexUpdated))
	}
	if _, err := os.Stat(codexCfg + ".pre-ws"); err != nil {
		t.Fatalf("expected codex .pre-ws backup: %v", err)
	}
	awsDest, err := os.Readlink(filepath.Join(awsDir, "credentials"))
	if err != nil || awsDest != "work-credentials" {
		t.Fatalf("expected relative link work-credentials, got %q err=%v", awsDest, err)
	}

	plan2, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply idempotent sync: %v", err)
	}
	for _, a := range plan2.Actions {
		if a.RequiresAllow == "script" && !f.ctx.Allow.Script {
			continue
		}
		if strings.HasPrefix(a.ID, "sync:") {
			t.Fatalf("unexpected non-idempotent sync action on 2nd plan: %+v", a)
		}
	}

	rep, err := RunDoctor(f.ctx)
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	var sawCodexBackup bool
	for _, fd := range rep.Findings {
		if strings.HasPrefix(fd.ID, "backup:") && strings.Contains(fd.ID, ".codex/config.toml.pre-ws") {
			sawCodexBackup = true
		}
		if fd.ID == "sync:go-telemetry" || fd.ID == "sync:aws-default-creds" {
			t.Errorf("unexpected sync finding after apply: %+v", fd)
		}
	}
	if !sawCodexBackup {
		t.Errorf("expected Doctor to report .codex/config.toml.pre-ws backup, got %+v", rep.Findings)
	}

	if _, err := UndoLast(f.ctx); err != nil {
		t.Fatalf("UndoLast sync hooks: %v", err)
	}
	codexRestored, _ := os.ReadFile(codexCfg)
	if string(codexRestored) != "model = \"gpt-5\"\n" {
		t.Errorf("expected codex config restored on undo, got %q", string(codexRestored))
	}
	if _, err := os.Lstat(filepath.Join(awsDir, "credentials")); !os.IsNotExist(err) {
		t.Errorf("expected ~/.aws/credentials removed on undo, err=%v", err)
	}
}

func TestExecApplyRefusesTamperedPlanWithoutAllowScript(t *testing.T) {
	f := newSyntheticFixture(t)
	f.ctx.Allow = app.AllowSet{}
	planPath := filepath.Join(t.TempDir(), "plan.json")
	marker := filepath.Join(t.TempDir(), "pwned")

	tampered := &Plan{
		Verb:  "apply",
		Host:  FormatPlanHost(f.ctx),
		Title: "tampered tpm hook",
		Actions: []Action{
			{
				ID:      "sync:tmux-tpm",
				Kind:    ActionHook,
				Target:  filepath.Join(f.ctx.ConfigDir, "tmux", "plugins", "tpm"),
				Summary: "install tpm",
				Risk:    RiskSafe,
				Command: []string{"bash", "-c", "touch " + shellSingleQuote(marker) + " # https://github.com/tmux-plugins/tpm"},
			},
		},
	}
	if err := tampered.Save(planPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecApply(f.ctx, loaded); err == nil {
		t.Fatalf("expected tampered sync:tmux-tpm plan to be refused without --allow script")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("tampered command executed without --allow script")
	}

	tpmDir := filepath.Join(f.ctx.ConfigDir, "tmux", "plugins", "tpm")
	strippedTPM := &Plan{
		Verb:  "apply",
		Host:  FormatPlanHost(f.ctx),
		Title: "stripped tpm consent",
		Actions: []Action{
			{
				ID:            "sync:tmux-tpm",
				Kind:          ActionHook,
				Target:        tpmDir,
				Summary:       "install tmux plugin manager (tpm)",
				Risk:          RiskMedium,
				RequiresAllow: "",
				Command:       tmuxTPMInstallCommand(tpmDir),
			},
		},
	}
	if err := strippedTPM.Save(planPath); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadPlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := ExecApply(f.ctx, loaded); err != nil || res.ConsentSkipped != 1 || res.Applied != 0 || !strings.Contains(strings.Join(res.Warnings, "\n"), "--allow script") {
		t.Fatalf("expected stripped sync:tmux-tpm plan to be consent-skipped for --allow script, got res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(tpmDir); !os.IsNotExist(err) {
		t.Fatalf("consent-skipped tpm hook ran, err=%v", err)
	}
	f.ctx.DryRun = true
	dryRes, err := ExecApply(f.ctx, loaded)
	f.ctx.DryRun = false
	if err != nil {
		t.Fatalf("dry-run stripped sync:tmux-tpm: %v", err)
	}
	if !strings.Contains(strings.Join(dryRes.Warnings, "\n"), "--allow script") {
		t.Fatalf("expected dry-run warning requiring --allow script, got %v", dryRes.Warnings)
	}

	strippedGH := &Plan{
		Verb:  "apply",
		Host:  FormatPlanHost(f.ctx),
		Title: "stripped gh-dash consent",
		Actions: []Action{
			{
				ID:            "sync:gh-dash",
				Kind:          ActionHook,
				Target:        "gh-dash",
				Summary:       "install gh-dash extension",
				Risk:          RiskSafe,
				RequiresAllow: "",
				Command:       []string{"gh", "extension", "install", "dlvhdr/gh-dash"},
			},
		},
	}
	if res, err := ExecApply(f.ctx, strippedGH); err != nil || res.ConsentSkipped != 1 || !slices.Equal(res.MissingAllows, []string{"script"}) {
		t.Fatalf("expected stripped sync:gh-dash plan to be consent-skipped for --allow script, got res=%+v err=%v", res, err)
	}

	existingTarget := filepath.Join(f.home, ".existing-real-file")
	if err := os.WriteFile(existingTarget, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	renderOut := filepath.Join(f.repo, "config", "fzf", "colors.sh")
	strippedReplace := &Plan{
		Verb:  "apply",
		Host:  FormatPlanHost(f.ctx),
		Theme: "gruvbox-dark",
		Title: "stripped replace consent",
		Actions: []Action{
			{
				ID:     "render:config/fzf/colors.sh",
				Kind:   ActionRender,
				Target: renderOut,
				Metadata: map[string]string{
					"theme": "gruvbox-dark",
				},
			},
			{
				ID:            "link:~/.existing-real-file",
				Kind:          ActionSymlink,
				Source:        filepath.Join(f.repo, "config", "zsh", "zshenv"),
				Target:        existingTarget,
				RequiresAllow: "",
			},
		},
	}
	replRes, err := ExecApply(f.ctx, strippedReplace)
	if err != nil {
		t.Fatalf("expected stripped replace symlink to be skipped, not fail: %v", err)
	}
	if replRes.Applied != 1 || replRes.ConsentSkipped != 1 || !slices.Equal(replRes.MissingAllows, []string{"replace"}) {
		t.Fatalf("expected approved render applied and replace skipped, got %+v", replRes)
	}
	if !strings.Contains(strings.Join(replRes.Warnings, "\n"), "kept existing ~/.existing-real-file (requires --allow replace)") {
		t.Fatalf("expected kept existing warning, got %v", replRes.Warnings)
	}
	if _, err := os.Stat(renderOut); err != nil {
		t.Fatalf("expected approved render to run despite the skipped replace, err=%v", err)
	}
	if fi, err := os.Lstat(existingTarget); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected existing file kept, err=%v", err)
	}
}

func TestUndoSkipsDriftedSymlinksUnlessAllowReplace(t *testing.T) {
	f := newSyntheticFixture(t)

	srcA := filepath.Join(f.repo, "config", "zsh", "zshenv")
	srcB := filepath.Join(f.repo, "config", "fzf", "colors.sh")
	if err := os.WriteFile(srcB, []byte("# fzf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	customTarget := filepath.Join(f.home, "my-custom-config")
	if err := os.WriteFile(customTarget, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origLinkTarget := filepath.Join(f.home, "old-target")
	if err := os.WriteFile(origLinkTarget, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	createdLink := filepath.Join(f.home, ".created-link")
	replacedLink := filepath.Join(f.home, ".replaced-link")
	backedUpLink := filepath.Join(f.home, ".backed-up-link")
	untouchedLink := filepath.Join(f.home, ".untouched-link")

	if err := os.Symlink(origLinkTarget, replacedLink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backedUpLink, []byte("original-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.ctx.Allow.Replace = true
	plan := &Plan{
		Verb:  "apply",
		Host:  FormatPlanHost(f.ctx),
		Title: "symlink drift test",
		Actions: []Action{
			{ID: "link:created", Kind: ActionSymlink, Source: srcA, Target: createdLink},
			{ID: "link:replaced", Kind: ActionSymlink, Source: srcA, Target: replacedLink},
			{ID: "link:backedup", Kind: ActionSymlink, Source: srcA, Target: backedUpLink, RequiresAllow: "replace"},
			{ID: "link:untouched", Kind: ActionSymlink, Source: srcB, Target: untouchedLink},
		},
	}
	if _, err := ExecApply(f.ctx, plan); err != nil {
		t.Fatalf("ExecApply: %v", err)
	}

	for _, p := range []string{createdLink, replacedLink, backedUpLink} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(customTarget, p); err != nil {
			t.Fatal(err)
		}
	}

	f.ctx.Allow.Replace = false
	msg, err := UndoLast(f.ctx)
	if err != nil {
		t.Fatalf("UndoLast without --allow replace should skip drifted links, got err=%v", err)
	}
	if !strings.Contains(msg, "skipped 3 drifted") || !strings.Contains(msg, ".created-link") || !strings.Contains(msg, ".replaced-link") || !strings.Contains(msg, ".backed-up-link") {
		t.Fatalf("expected UndoLast report to mention skipped drifted links, got %q", msg)
	}
	if _, err := os.Lstat(untouchedLink); !os.IsNotExist(err) {
		t.Fatalf("expected untouched link to be removed during undo, err=%v", err)
	}
	for _, p := range []string{createdLink, replacedLink, backedUpLink} {
		got, rerr := os.Readlink(p)
		if rerr != nil || got != customTarget {
			t.Fatalf("expected drifted symlink %s to stay pointing to %s, got %q err=%v", p, customTarget, got, rerr)
		}
	}
	if _, err := os.Stat(backedUpLink + ".pre-ws"); err != nil {
		t.Fatalf("expected .pre-ws backup preserved while drifted restore_backup was skipped: %v", err)
	}

	f.ctx.Allow.Replace = true
	msg2, err := UndoLast(f.ctx)
	if err != nil {
		t.Fatalf("UndoLast with --allow replace: %v", err)
	}
	if strings.Contains(msg2, "skipped") {
		t.Fatalf("expected no skipped entries with --allow replace, got %q", msg2)
	}
	if _, err := os.Lstat(createdLink); !os.IsNotExist(err) {
		t.Fatalf("expected createdLink removed with --allow replace, err=%v", err)
	}
	if got, err := os.Readlink(replacedLink); err != nil || got != origLinkTarget {
		t.Fatalf("expected replacedLink restored to %s, got %q err=%v", origLinkTarget, got, err)
	}
	if raw, err := os.ReadFile(backedUpLink); err != nil || string(raw) != "original-file\n" {
		t.Fatalf("expected backedUpLink restored from backup, got %q err=%v", string(raw), err)
	}
}

func TestDiscoverCodexConfigPathsReadsZoneMiseToml(t *testing.T) {
	f := newSyntheticFixture(t)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("WS_ZONES", "")

	idDir := filepath.Join(f.repo, "config", "identity")
	if err := os.MkdirAll(idDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idDir, "work.mise.toml"), []byte("[env]\nCODEX_HOME = \"{{env.HOME}}/.config/codex-work\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	workZoneDir := filepath.Join(f.home, "work")
	if err := os.MkdirAll(workZoneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workZoneDir, "mise.toml"), []byte("[env]\nWS_ZONE = \"work\"\nCODEX_HOME = \"{{env.HOME}}/.config/codex-work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workCodexCfg := filepath.Join(f.home, ".config", "codex-work", "config.toml")
	if err := os.MkdirAll(filepath.Dir(workCodexCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workCodexCfg, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.ctx.LocalEnv["WS_ZONES"] = "lab|Lab|lab@example.invalid"
	labZoneDir := filepath.Join(f.home, "lab")
	if err := os.MkdirAll(labZoneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(labZoneDir, "mise.toml"), []byte("[env]\nCODEX_HOME = \"{{config_root}}/.codex-lab\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	labCodexCfg := filepath.Join(labZoneDir, ".codex-lab", "config.toml")
	if err := os.MkdirAll(filepath.Dir(labCodexCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(labCodexCfg, []byte("model = \"gpt-5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := discoverCodexConfigPaths(f.ctx)
	if !slices.Contains(got, workCodexCfg) {
		t.Fatalf("expected zone work codex config %q in %v", workCodexCfg, got)
	}
	if !slices.Contains(got, labCodexCfg) {
		t.Fatalf("expected zone lab codex config %q in %v", labCodexCfg, got)
	}
}

func TestPlanApplyAlwaysPlansScriptHooksAndProbesDisableMiseAutoInstall(t *testing.T) {
	f := newSyntheticFixture(t)
	t.Setenv("WS_EXCLUDE_PATHS", "")
	t.Setenv("MISE_NOT_FOUND_AUTO_INSTALL", "1")
	t.Setenv("MISE_EXEC_AUTO_INSTALL", "1")
	f.ctx.Allow = app.AllowSet{}

	stubBin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(stubBin, 0o755); err != nil {
		t.Fatal(err)
	}
	probeLog := filepath.Join(t.TempDir(), "probe.log")
	callLog := filepath.Join(t.TempDir(), "calls.log")
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stubBin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("go", `if [ "$1" = "telemetry" ] && [ -z "$2" ]; then
  printf '%s|%s\n' "$MISE_NOT_FOUND_AUTO_INSTALL" "$MISE_EXEC_AUTO_INSTALL" >> `+shellSingleQuote(probeLog)+`
  echo local
fi
exit 0
`)
	stub("gh", `echo "gh $*" >> `+shellSingleQuote(callLog)+`
exit 0
`)
	stub("git", `echo "git $*" >> `+shellSingleQuote(callLog)+`
exit 0
`)
	t.Setenv("PATH", stubBin+string(os.PathListSeparator)+"/usr/bin:/bin")
	if err := os.MkdirAll(filepath.Join(f.ctx.ConfigDir, "tmux"), 0o755); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanApply(f.ctx, "", "gruvbox-dark")
	if err != nil {
		t.Fatalf("PlanApply: %v", err)
	}
	planned := map[string]bool{}
	for _, a := range plan.Actions {
		planned[a.ID] = true
	}
	if !planned["sync:tmux-tpm"] || !planned["sync:gh-dash"] {
		t.Fatalf("expected tpm and gh-dash planned without --allow script, got %v", planned)
	}

	probes, err := os.ReadFile(probeLog)
	if err != nil {
		t.Fatalf("go telemetry probe did not run: %v", err)
	}
	if strings.TrimSpace(string(probes)) != "0|0" {
		t.Fatalf("expected probe env MISE_NOT_FOUND_AUTO_INSTALL=0 MISE_EXEC_AUTO_INSTALL=0, got %q", string(probes))
	}

	res, err := ExecApply(f.ctx, plan)
	if err != nil {
		t.Fatalf("ExecApply: %v", err)
	}
	if !slices.Contains(res.MissingAllows, "script") || res.ConsentSkipped < 2 {
		t.Fatalf("expected script hooks consent-skipped, got %+v", res)
	}
	calls, _ := os.ReadFile(callLog)
	if strings.Contains(string(calls), "extension install") || strings.Contains(string(calls), "clone") {
		t.Fatalf("consent-skipped script hooks ran: %q", string(calls))
	}
}
