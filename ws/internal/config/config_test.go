package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
)

func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRepoPresetsToolsLinksThemes(t *testing.T) {
	root := repoRootForTest(t)

	presets, err := LoadPresets(root)
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	for _, name := range []string{"base", "dev-cli", "mac-workstation", "arch-desktop", "cachyos-desktop", "ubuntu-runner", "debian-server", "server"} {
		res, err := ResolvePreset(root, presets, name)
		if err != nil {
			t.Fatalf("ResolvePreset(%s): %v", name, err)
		}
		if len(res.PackageNames) == 0 {
			t.Fatalf("preset %s resolved 0 packages", name)
		}
	}

	macPreset, err := ResolvePreset(root, presets, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(macPreset.PackageNames, "claude-code@latest") {
		t.Fatalf("mac-workstation should keep claude-code@latest cask from mac-cask.txt")
	}
	if !slices.Contains(macPreset.PackageNames, "claude-code") {
		t.Fatalf("mac-workstation should include claude-code from dev-cli")
	}
	if slices.Contains(macPreset.PackageNames, "zsh") {
		t.Fatalf("mac-workstation should exclude zsh (macOS ships /bin/zsh)")
	}
	if slices.Contains(macPreset.PackageNames, "atuin") {
		t.Fatalf("mac-workstation should not include untracked atuin")
	}

	tools, err := LoadTools(root)
	if err != nil {
		t.Fatalf("LoadTools: %v", err)
	}
	resolved := ResolveTools(macPreset, tools, app.HostInfo{OS: "darwin", Distro: "macos", Arch: "arm64", PkgManagers: []string{"brew"}})
	var claudeCount, codexCount int
	var foundGhostty, foundAntigravity, foundHex bool
	for _, rt := range resolved {
		if rt.Spec.Via == "cask" && rt.Spec.Name == "claude-code@latest" {
			claudeCount++
			if rt.Updates != "manager" || len(rt.Conflicts) != 0 {
				t.Fatalf("unexpected claude-code resolution: %+v", rt)
			}
		}
		if rt.Spec.Via == "cask" && rt.Spec.Name == "codex" {
			codexCount++
			if rt.Updates != "manager" {
				t.Fatalf("unexpected codex resolution: %+v", rt)
			}
		}
		switch rt.ID {
		case "ghostty":
			foundGhostty = true
			if rt.Spec.Via != "cask" {
				t.Fatalf("ghostty should resolve as cask, got %+v", rt.Spec)
			}
		case "antigravity-cli":
			foundAntigravity = true
			if rt.Spec.Via != "cask" {
				t.Fatalf("antigravity-cli should resolve as cask, got %+v", rt.Spec)
			}
		case "anomalyco/tap/hex":
			foundHex = true
			if rt.Spec.Via != "cask" {
				t.Fatalf("anomalyco/tap/hex should resolve as cask, got %+v", rt.Spec)
			}
		}
	}
	if claudeCount != 1 {
		t.Fatalf("expected exactly 1 deduplicated claude-code@latest cask entry, got %d", claudeCount)
	}
	if codexCount != 1 {
		t.Fatalf("expected exactly 1 codex cask entry, got %d", codexCount)
	}
	if !foundGhostty || !foundAntigravity || !foundHex {
		t.Fatalf("missing expected mac casks: ghostty=%v antigravity=%v hex=%v", foundGhostty, foundAntigravity, foundHex)
	}

	links, err := LoadLinksForPreset(root, macPreset)
	if err != nil {
		t.Fatalf("LoadLinksForPreset: %v", err)
	}
	if len(links) < 10 {
		t.Fatalf("expected >= 10 mac links, got %d", len(links))
	}

	themes, err := ListThemes(root)
	if err != nil || len(themes) == 0 {
		t.Fatalf("ListThemes: %v (%v)", err, themes)
	}
	for _, tid := range themes {
		pal, err := LoadPalette(root, tid)
		if err != nil {
			t.Fatalf("LoadPalette(%s): %v", tid, err)
		}
		if pal.Tokens["bg_strip"] == "" {
			t.Fatalf("palette %s missing derived bg_strip", tid)
		}
	}

	reg, err := LoadThemeRegistry(root)
	if err != nil || len(reg) == 0 {
		t.Fatalf("LoadThemeRegistry: %v", err)
	}
}

func TestLoadPaletteTraversalAndTabRegistryParsing(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPalette(tmp, "../../etc-dark"); err == nil {
		t.Fatalf("expected traversal theme id to be rejected")
	}
	if _, err := LoadPalette(tmp, "gruvbox-custom"); err == nil {
		t.Fatalf("expected non-dark/light mode to be rejected")
	}

	regContent := strings.Join([]string{
		"config/fzf/colors-template.sh\tconfig/fzf/colors.sh\t-",
		"config/bat/themes/colors-template.tmTheme\tconfig/bat/themes/colors.tmTheme\tbat cache --build",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes", "registry"), []byte(regContent), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadThemeRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadThemeRegistry tab lines: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].PostHook != "" {
		t.Fatalf("expected empty PostHook for '-' column, got %q", entries[0].PostHook)
	}
	if entries[1].PostHook != "bat cache --build" {
		t.Fatalf("unexpected PostHook for bat: %q", entries[1].PostHook)
	}
}
