package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWSConfigSaveLoadPrecedenceAndValidation(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "links"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "themes", "registry"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "links", "common.links"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mise.local.toml"), []byte("[env]\nWS_OP_VAULT = \"MiseLocalVault\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WS_OP_VAULT", "ProcessEnvVault")

	ctx, err := NewContext(Options{
		HomeDir:  home,
		RepoRoot: repo,
	})
	if err != nil {
		t.Fatalf("NewContext: %v", err)
	}

	eff := ctx.EffectiveConfig()
	if eff.TUI.StartTab != "overview" || !BoolVal(eff.TUI.AutoReload, false) || !BoolVal(eff.TUI.Mouse, false) || eff.TUI.AutoReloadSecs != 2 || eff.TUI.BorderedBar != "auto" {
		t.Fatalf("unexpected default EffectiveConfig: %+v", eff)
	}
	if got := ctx.LookupEnv("WS_OP_VAULT"); got != "ProcessEnvVault" {
		t.Fatalf("expected process env before override, got %q", got)
	}

	if err := ctx.SetConfigOverride("WS_OP_VAULT", "OverrideVault"); err != nil {
		t.Fatalf("SetConfigOverride(WS_OP_VAULT): %v", err)
	}
	if got := ctx.LookupEnv("WS_OP_VAULT"); got != "OverrideVault" {
		t.Fatalf("expected override to beat process env and mise.local, got %q", got)
	}
	if val, ok := ctx.LookupEnvExact("WS_OP_VAULT"); !ok || val != "OverrideVault" {
		t.Fatalf("expected LookupEnvExact override, got %q ok=%v", val, ok)
	}
	cfgInfo, err := os.Stat(ctx.WSConfigPath())
	if err != nil {
		t.Fatalf("expected %s created: %v", ctx.WSConfigPath(), err)
	}
	if cfgInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected config.toml mode 0600, got %o", cfgInfo.Mode().Perm())
	}

	if err := ctx.SetConfigOverride("tui.auto_reload", "false"); err != nil {
		t.Fatal(err)
	}
	if err := ctx.SetConfigOverride("tui.mouse", "false"); err != nil {
		t.Fatal(err)
	}
	if err := ctx.SetConfigOverride("tui.auto_reload_secs", "5"); err != nil {
		t.Fatal(err)
	}
	if err := ctx.SetConfigOverride("workspace.preset", "ubuntu-runner"); err != nil {
		t.Fatal(err)
	}
	ctx2, err := NewContext(Options{HomeDir: home, RepoRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	eff2 := ctx2.EffectiveConfig()
	if BoolVal(eff2.TUI.AutoReload, true) || BoolVal(eff2.TUI.Mouse, true) || eff2.TUI.AutoReloadSecs != 5 {
		t.Fatalf("reloaded TUI config mismatch: %+v", eff2.TUI)
	}
	if err := ctx2.ResetConfigOverride("tui.mouse"); err != nil {
		t.Fatalf("ResetConfigOverride(tui.mouse): %v", err)
	}
	if !BoolVal(ctx2.EffectiveConfig().TUI.Mouse, false) {
		t.Fatalf("expected ResetConfigOverride(tui.mouse) to restore default true")
	}
	if ctx2.EffectivePreset() != "ubuntu-runner" {
		t.Fatalf("expected workspace.preset override 'ubuntu-runner', got %q", ctx2.EffectivePreset())
	}

	ctxFlag := *ctx2
	ctxFlag.PresetOverride = "arch-desktop"
	if ctxFlag.EffectivePreset() != "arch-desktop" {
		t.Fatalf("expected PresetOverride CLI flag to win over config override, got %q", ctxFlag.EffectivePreset())
	}

	if err := ctx2.ResetConfigOverride("WS_OP_VAULT"); err != nil {
		t.Fatalf("ResetConfigOverride: %v", err)
	}
	if got := ctx2.LookupEnv("WS_OP_VAULT"); got != "ProcessEnvVault" {
		t.Fatalf("expected ResetConfigOverride to restore process env, got %q", got)
	}

	if err := ctx2.SetConfigOverride("path.ws_home", "/tmp/evil"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected readonly error for path.ws_home, got %v", err)
	}
	if err := ctx2.SetConfigOverride("tui.auto_reload_secs", "999"); err == nil {
		t.Fatalf("expected out-of-range auto_reload_secs rejected")
	}
	if err := ctx2.SetConfigOverride("tui.bordered_bar", "invalid"); err == nil {
		t.Fatalf("expected invalid enum value rejected")
	}
	for _, validTab := range []string{"overview", "projects", "packages", "env", "fleet", "workstation", "output", "system", "doctor", "settings", "mise", "secrets", "vault", "presets"} {
		if err := ctx2.SetConfigOverride("tui.start_tab", validTab); err != nil {
			t.Fatalf("expected tui.start_tab=%q accepted, got %v", validTab, err)
		}
	}
	if err := ctx2.SetConfigOverride("tui.start_tab", "bogus-tab"); err == nil {
		t.Fatalf("expected invalid tui.start_tab rejected")
	}
	if err := ctx2.SetConfigOverride("unknown.key", "1"); err == nil {
		t.Fatalf("expected unknown key rejected")
	}
}

func TestConfigFingerprintAndReloadRaceSafety(t *testing.T) {
	t.Setenv("WS_OP_VAULT", "")
	home := t.TempDir()
	repo := t.TempDir()
	_ = os.MkdirAll(filepath.Join(repo, "themes"), 0o755)
	_ = os.MkdirAll(filepath.Join(repo, "links"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "themes", "registry"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(repo, "links", "common.links"), []byte(""), 0o644)

	ctx, err := NewContext(Options{HomeDir: home, RepoRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	fpBefore, _ := ctx.ConfigFingerprint()

	external := &WSConfig{
		TUI: TUIConfig{Theme: "catppuccin-dark", BorderedBar: "compact"},
		Env: map[string]string{"WS_SYNC_PATHS": ".zshrc"},
	}
	if err := SaveWSConfig(ctx.WSConfigPath(), external); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mise.local.toml"), []byte("[env]\nWS_OP_VAULT = \"HotVault\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fpAfter, _ := ctx.ConfigFingerprint()
	if fpAfter == fpBefore {
		t.Fatalf("expected ConfigFingerprint to change after writing config.toml and mise.local.toml")
	}

	changed, err := ctx.ReloadConfig()
	if err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
	if !strings.Contains(strings.Join(changed, ","), "config.toml") || !strings.Contains(strings.Join(changed, ","), "mise.local.toml") {
		t.Fatalf("expected changed list to contain config.toml and mise.local.toml, got %v", changed)
	}
	if ctx.EffectiveConfig().TUI.Theme != "catppuccin-dark" || ctx.LookupEnv("WS_OP_VAULT") != "HotVault" {
		t.Fatalf("expected ReloadConfig values active immediately: theme=%q vault=%q", ctx.EffectiveConfig().TUI.Theme, ctx.LookupEnv("WS_OP_VAULT"))
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = ctx.EffectiveConfig()
				_ = ctx.LookupEnv("WS_OP_VAULT")
				_, _ = ctx.ReloadConfig()
			}
		}()
	}
	wg.Wait()
}
