package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepoContractsRealRepo(t *testing.T) {
	root := repoRootForTest(t)
	issues := ValidateRepoContracts(root)
	if len(issues) != 0 {
		for _, is := range issues {
			t.Errorf("contract violation [%s]: %s (%s)", is.ID, is.Title, is.Detail)
		}
	}

	presets, err := LoadPresets(root)
	if err != nil {
		t.Fatalf("LoadPresets: %v", err)
	}
	arch, err := ResolvePreset(root, presets, "arch-desktop")
	if err != nil {
		t.Fatalf("ResolvePreset(arch-desktop): %v", err)
	}
	if !slices.Contains(arch.ConflictFiles, "pkg/arch-conflicts.txt") {
		t.Fatalf("arch-desktop missing pkg/arch-conflicts.txt: %v", arch.ConflictFiles)
	}
	for _, want := range []string{"jack", "yq", "dms-shell", "greetd-dms-greeter-bin", "noctalia-qs", "noctalia-shell"} {
		if !slices.Contains(arch.ConflictPackages, want) {
			t.Errorf("arch-desktop ConflictPackages missing %q: %v", want, arch.ConflictPackages)
		}
	}
	if slices.Contains(arch.OSHooks, "os/arch/conflicts.sh") {
		t.Errorf("arch-desktop os_hooks should not include os/arch/conflicts.sh: %v", arch.OSHooks)
	}
}

func TestStrictTOMLRejectsUnknownKeys(t *testing.T) {
	tmp := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmp, "presets.toml"), []byte("ws_min_version = \"0.1.0\"\nunknown_top = true\n\n[preset.base]\ndescription = \"base\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresets(tmp); err == nil {
		t.Fatalf("expected LoadPresets to reject unknown top-level key")
	}

	if err := os.WriteFile(filepath.Join(tmp, "presets.toml"), []byte("[preset.base]\ndescription = \"base\"\nbogus_key = 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresets(tmp); err == nil {
		t.Fatalf("expected LoadPresets to reject unknown preset field")
	}

	if err := os.WriteFile(filepath.Join(tmp, "tools.toml"), []byte("[tool.demo]\nbin = \"demo\"\nunknown_tool_key = \"nope\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTools(tmp); err == nil {
		t.Fatalf("expected LoadTools to reject unknown tool field")
	}

	if err := os.WriteFile(filepath.Join(tmp, "tools.toml"), []byte("[tool.demo]\nbin = \"demo\"\ninstall = [{ via = \"brew\", bogus = true }]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTools(tmp); err == nil {
		t.Fatalf("expected LoadTools to reject unknown install field")
	}

	palDir := filepath.Join(tmp, "themes", "demo")
	if err := os.MkdirAll(palDir, 0o755); err != nil {
		t.Fatal(err)
	}
	badPalette := strings.Join([]string{
		`name = "demo-dark"`,
		`mode = "dark"`,
		`bg = "#111111"`,
		`bg_alt = "#222222"`,
		`fg = "#eeeeee"`,
		`fg_dim = "#888888"`,
		`accent = "#ff8800"`,
		`selection_bg = "#333333"`,
		`selection_fg = "#ffffff"`,
		`cursor = "#ffffff"`,
		`border = "#444444"`,
		`color0 = "#000000"`,
		`color1 = "#111111"`,
		`color2 = "#222222"`,
		`color3 = "#333333"`,
		`color4 = "#444444"`,
		`color5 = "#555555"`,
		`color6 = "#666666"`,
		`color7 = "#777777"`,
		`color8 = "#888888"`,
		`color9 = "#999999"`,
		`color10 = "#aaaaaa"`,
		`color11 = "#bbbbbb"`,
		`color12 = "#cccccc"`,
		`color13 = "#dddddd"`,
		`color14 = "#eeeeee"`,
		`color15 = "#ffffff"`,
		`extra_unknown_token = "#123456"`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(palDir, "dark.toml"), []byte(badPalette), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPalette(tmp, "demo-dark"); err == nil {
		t.Fatalf("expected LoadPalette to reject unknown key")
	}
}

func TestWSMinVersionEnforcement(t *testing.T) {
	tmp := t.TempDir()
	presetsRaw := "ws_min_version = \"0.2.0\"\n\n[preset.base]\ndescription = \"base\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "presets.toml"), []byte(presetsRaw), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, allowed := range []string{"", "dev", "a1b2c3d", "a1b2c3d-dirty", "0.2.0", "v0.2.0", "v0.2.0-dirty", "v0.2.0-4-g1234567", "v0.2.1", "1.0.0"} {
		if _, err := LoadPresetsWithVersion(tmp, allowed); err != nil {
			t.Errorf("expected version %q to pass ws_min_version 0.2.0, got: %v", allowed, err)
		}
	}

	for _, refused := range []string{"0.1.9", "v0.1.0", "0.0.1", "v0.2.0-rc.1"} {
		_, err := LoadPresetsWithVersion(tmp, refused)
		if err == nil {
			t.Errorf("expected older version %q to be refused for ws_min_version 0.2.0", refused)
			continue
		}
		if !strings.Contains(err.Error(), "0.2.0") || !strings.Contains(err.Error(), refused) {
			t.Errorf("expected clear version message for %q, got: %v", refused, err)
		}
	}

	badPresets := "ws_min_version = \"not-a-semver\"\n\n[preset.base]\ndescription = \"base\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "presets.toml"), []byte(badPresets), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPresetsWithVersion(tmp, "dev"); err == nil {
		t.Fatalf("expected malformed ws_min_version to fail even on dev builds")
	}
}

func TestValidateRepoContractsCatchesViolations(t *testing.T) {
	tmp := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("presets.toml", strings.Join([]string{
		`ws_min_version = "0.1.0"`,
		`[preset.base]`,
		`description = "test"`,
		`links = ["links/common.links"]`,
		`packages = ["pkg/base.txt"]`,
		`os_hooks = ["os/missing.sh", "os/bad.sh"]`,
	}, "\n")+"\n")
	mustWrite("tools.toml", "[tool.demo]\nbin = \"demo\"\n")
	mustWrite("os/bad.sh", "#!/usr/bin/env bash\nif true; then\n")
	mustWrite("links/common.links", "config/missing-file ~/.missing\n")
	mustWrite("pkg/base.txt", "git\n")
	mustWrite("themes/registry", "config/demo/colors-template.sh config/demo/colors.sh -\n")
	mustWrite("config/demo/colors-template.sh", "echo {{nonexistent_token}}\n")
	mustWrite("themes/demo/dark.toml", strings.Join([]string{
		`name = "demo-dark"`,
		`mode = "dark"`,
		`bg = "#111111"`,
		`bg_alt = "#222222"`,
		`fg = "#eeeeee"`,
		`fg_dim = "#888888"`,
		`accent = "#ff8800"`,
		`selection_bg = "#333333"`,
		`selection_fg = "#ffffff"`,
		`cursor = "#ffffff"`,
		`border = "#444444"`,
		`color0 = "#000000"`,
		`color1 = "#111111"`,
		`color2 = "#222222"`,
		`color3 = "#333333"`,
		`color4 = "#444444"`,
		`color5 = "#555555"`,
		`color6 = "#666666"`,
		`color7 = "#777777"`,
		`color8 = "#888888"`,
		`color9 = "#999999"`,
		`color10 = "#aaaaaa"`,
		`color11 = "#bbbbbb"`,
		`color12 = "#cccccc"`,
		`color13 = "#dddddd"`,
		`color14 = "#eeeeee"`,
		`color15 = "#ffffff"`,
	}, "\n")+"\n")

	issues := ValidateRepoContracts(tmp)
	ids := map[string]bool{}
	for _, is := range issues {
		ids[is.ID] = true
	}
	for _, want := range []string{
		"contract:os-hook-missing:os/missing.sh",
		"contract:os-hook-syntax:os/bad.sh",
		"contract:link-source:links/common.links:config/missing-file",
		"contract:template-render:config/demo/colors-template.sh:demo-dark",
	} {
		if !ids[want] {
			t.Errorf("missing expected contract issue %q; got %+v", want, issues)
		}
	}
}
