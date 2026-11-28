package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/spf13/cobra"
)

func setupBootstrapFixture(t *testing.T) testFixture {
	t.Helper()
	t.Setenv("WS_ZONE", "")
	t.Setenv("WS_ZONES", "")
	t.Setenv("WS_OP_FILES", "")
	t.Setenv("WS_OP_VAULT", "")
	t.Setenv("WS_PROFILE", "")
	t.Setenv("WS_DB_KEY", "")
	t.Setenv("NO_COLOR", "1")

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

[preset.dev-cli]
description = "Dev CLI"
include = ["base"]
packages = ["pkg/server.txt"]
secrets = "1password"
tags = ["core", "cli", "dev"]

[preset.mac-workstation]
description = "macOS workstation"
include = ["dev-cli"]
links = ["links/mac.links"]
packages = ["pkg/server.txt"]
secrets = "1password"
tags = ["core", "cli", "dev", "desktop", "mac"]

[preset.arch-desktop]
description = "Arch desktop"
include = ["dev-cli"]
links = ["links/linux.links"]
packages = ["pkg/server.txt"]
secrets = "1password"
tags = ["core", "cli", "dev", "desktop", "arch"]

[preset.debian-server]
description = "Debian server"
include = ["base"]
links = ["links/server.links"]
packages = ["pkg/server.txt"]
secrets = "none"
tags = ["core", "cli", "headless", "server"]

[preset.ubuntu-runner]
description = "Ubuntu runner"
include = ["base"]
links = ["links/server.links"]
packages = ["pkg/server.txt"]
secrets = "none"
tags = ["core", "cli", "headless", "runner"]
`, 0o644)

	mustWrite(filepath.Join(repo, "tools.toml"), "", 0o644)
	mustWrite(filepath.Join(repo, "pkg", "server.txt"), "git\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "common.links"), "config/tmux ~/.config/tmux\nconfig/bat ~/.config/bat\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "mac.links"), "config/demo/mac.conf ~/.config/demo/mac.conf\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "linux.links"), "config/demo/linux.conf ~/.config/demo/linux.conf\n", 0o644)
	mustWrite(filepath.Join(repo, "links", "server.links"), "config/demo/server.conf ~/.config/demo/server.conf\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "demo", "mac.conf"), "mac = true\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "demo", "linux.conf"), "linux = true\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "demo", "server.conf"), "server = true\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "bat", "config"), "--theme=ws\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "tmux", "colors-template.conf"), "set -g status-bg '{{bg}}'\nset -g status-fg '{{accent}}'\n", 0o644)
	mustWrite(filepath.Join(repo, "config", "identity", "work.mise.toml"), "[env]\nWS_ZONE = \"work\"\nWS_CODE_ROOT = \"{{env.HOME}}/work/code\"\nGH_CONFIG_DIR = \"{{env.HOME}}/.config/gh-work\"\n", 0o600)
	mustWrite(filepath.Join(repo, "themes", "registry"), "config/tmux/colors-template.conf config/tmux/colors.conf -\n", 0o644)
	mustWrite(filepath.Join(repo, "mise.toml"), "[tools]\n", 0o644)

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

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	return testFixture{repo: repo, home: home}
}

func withBootstrapDeps(t *testing.T, deps bootstrapDeps) {
	t.Helper()
	orig := defaultBootstrapDeps
	defaultBootstrapDeps = deps
	t.Cleanup(func() {
		defaultBootstrapDeps = orig
	})
}

func execBootstrap(t *testing.T, fx testFixture, forceInteractive bool, args ...string) (string, error) {
	t.Helper()
	opts := &GlobalOpts{
		Repo:             fx.repo,
		Home:             fx.home,
		ForceInteractive: forceInteractive,
		Version:          "test",
	}
	cmd := NewRootCmd(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"bootstrap"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func writeTestZip(t *testing.T, dest string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapPhaseOrderPerProfile(t *testing.T) {
	cases := []struct {
		profile    string
		wantPreset string
		wantOrder  []string
	}{
		{
			profile:    "mac",
			wantPreset: "mac-workstation",
			wantOrder: []string{
				"packages:plan:mac-workstation",
				"packages:exec",
				"apply:plan:hooks=true",
				"apply:exec",
				"mise-trust:",
				"op-whoami",
				"vault:secrets",
				"mise-trust:mise.local.toml",
				"zones",
				"op-whoami",
				"vault:ssh",
				"vault:files:all=true",
				"theme:plan:gruvbox-dark",
				"theme:exec",
			},
		},
		{
			profile:    "arch",
			wantPreset: "arch-desktop",
			wantOrder: []string{
				"packages:plan:arch-desktop",
				"packages:exec",
				"apply:plan:hooks=true",
				"apply:exec",
				"mise-trust:",
				"op-whoami",
				"vault:secrets",
				"mise-trust:mise.local.toml",
				"zones",
				"op-whoami",
				"vault:ssh",
				"vault:files:all=true",
				"theme:plan:gruvbox-dark",
				"theme:exec",
			},
		},
		{
			profile:    "server",
			wantPreset: "debian-server",
			wantOrder: []string{
				"packages:plan:debian-server",
				"packages:exec",
				"apply:plan:hooks=true",
				"apply:exec",
				"mise-trust:",
				"theme:plan:gruvbox-dark",
				"theme:exec",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			fx := setupBootstrapFixture(t)
			var events []string

			withBootstrapDeps(t, bootstrapDeps{
				planPackages: func(ctx *app.Context, presetID string) (*engine.Plan, error) {
					events = append(events, "packages:plan:"+presetID)
					return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
				},
				execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
					events = append(events, "packages:exec")
					return &engine.ApplyResult{Applied: 1}, nil
				},
				planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
					if opts.ThemeOverride != "" {
						events = append(events, "theme:plan:"+opts.ThemeOverride)
						return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "theme"}, nil
					}
					events = append(events, fmt.Sprintf("apply:plan:hooks=%v", opts.AllPostSyncHooks))
					return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Title: "apply"}, nil
				},
				execApply: func(_ *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
					if plan.Theme != "" {
						events = append(events, "theme:exec")
					} else {
						events = append(events, "apply:exec")
					}
					return &engine.ApplyResult{Applied: 1}, nil
				},
				trustMise: func(_ *app.Context, configPath string) error {
					label := ""
					if configPath != "" {
						label = filepath.Base(configPath)
					}
					events = append(events, "mise-trust:"+label)
					return nil
				},
				opSignedIn: func(_ context.Context) bool {
					events = append(events, "op-whoami")
					return true
				},
				confirmOnePassword: func(_ *cobra.Command, _ *app.Context) (bool, error) {
					t.Fatalf("unexpected 1Password prompt under --yes")
					return false, nil
				},
				confirmAllow: func(_ *cobra.Command, _ *app.Context, _ string, _ []string) (bool, error) {
					t.Fatalf("unexpected allow prompt under --yes")
					return false, nil
				},
				vaultPull: func(ctx *app.Context, scope string, _ []string, opts shell.VaultOptions) error {
					if scope == "files" {
						events = append(events, fmt.Sprintf("vault:files:all=%v", opts.All))
					} else {
						events = append(events, "vault:"+scope)
					}
					if scope == "secrets" {
						ml := filepath.Join(ctx.RepoRoot, "mise.local.toml")
						if err := os.WriteFile(ml, []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\nWS_OP_FILES = \"~/.aws/config\"\n"), 0o600); err != nil {
							return err
						}
					}
					return nil
				},
				applyZones: func(ctx *app.Context) ([]string, error) {
					if strings.TrimSpace(ctx.LookupEnv("WS_ZONES")) == "" {
						t.Fatalf("expected WS_ZONES reloaded in-process before zones phase")
					}
					events = append(events, "zones")
					return []string{"created ~/work/"}, nil
				},
				isTTY: func(_ *cobra.Command, _ *app.Context) bool { return false },
			})

			out, err := execBootstrap(t, fx, false, "--profile", tc.profile, "--yes", "--json")
			if err != nil {
				t.Fatalf("bootstrap --profile %s failed: %v\n%s", tc.profile, err, out)
			}

			if !slices.Equal(events, tc.wantOrder) {
				t.Fatalf("phase order mismatch for %s:\ngot:  %v\nwant: %v", tc.profile, events, tc.wantOrder)
			}

			var summary bootstrapSummaryOutput
			if err := json.Unmarshal([]byte(out), &summary); err != nil {
				t.Fatalf("unmarshal summary JSON: %v\n%s", err, out)
			}
			if summary.Profile != tc.profile {
				t.Fatalf("summary.Profile = %q, want %q", summary.Profile, tc.profile)
			}
			if summary.Preset != tc.wantPreset {
				t.Fatalf("summary.Preset = %q, want %q", summary.Preset, tc.wantPreset)
			}
			if len(summary.Phases) != 7 {
				t.Fatalf("expected 7 phases in summary, got %d", len(summary.Phases))
			}
			if tc.profile == "server" {
				for _, idx := range []int{3, 4, 5} {
					if summary.Phases[idx].Status != bootstrapPhaseSkipped || summary.Phases[idx].Reason != "server profile" {
						t.Fatalf("expected phase %s skipped for server profile, got %+v", summary.Phases[idx].ID, summary.Phases[idx])
					}
				}
				for _, idx := range []int{0, 1, 2, 6} {
					if summary.Phases[idx].Status != bootstrapPhaseDone {
						t.Fatalf("expected phase %s done for server profile, got %+v", summary.Phases[idx].ID, summary.Phases[idx])
					}
				}
			} else {
				for _, ph := range summary.Phases {
					if ph.Status != bootstrapPhaseDone {
						t.Fatalf("expected phase %s done for %s, got %+v", ph.ID, tc.profile, ph)
					}
				}
			}
		})
	}
}

func TestBootstrapYesSkipsAllPromptsAndInteractivePromptsMissingScopes(t *testing.T) {
	t.Run("yes_skips_prompts_and_grants_all_allows", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		if err := os.MkdirAll(filepath.Join(fx.home, ".ssh"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fx.home, ".aws-existing"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		allowPromptCalls := 0
		opPromptCalls := 0
		var seenAllows app.AllowSet

		withBootstrapDeps(t, bootstrapDeps{
			planPackages: func(ctx *app.Context, presetID string) (*engine.Plan, error) {
				seenAllows = ctx.Allow
				return &engine.Plan{
					Verb:   "bootstrap",
					Preset: presetID,
					Title:  "pkg",
					Actions: []engine.Action{
						{ID: "os_exec:os/mac/defaults.sh", Kind: engine.ActionOSExec, RequiresAllow: "sudo", Command: []string{"bash", "os/mac/defaults.sh"}},
						{ID: "conflict:remove:brew:old", Kind: engine.ActionRemove, RequiresAllow: "remove"},
					},
				}, nil
			},
			execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{Applied: 2}, nil
			},
			planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
				if opts.ThemeOverride != "" {
					return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "theme"}, nil
				}
				return &engine.Plan{
					Verb:  "apply",
					Host:  engine.FormatPlanHost(ctx),
					Title: "apply",
					Actions: []engine.Action{
						{ID: "sync:tmux-tpm", Kind: engine.ActionHook, RequiresAllow: "script"},
						{ID: "sync:gh-dash", Kind: engine.ActionHook, RequiresAllow: "script"},
					},
				}, nil
			},
			execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{Applied: 2}, nil
			},
			trustMise:  func(_ *app.Context, _ string) error { return nil },
			opSignedIn: func(_ context.Context) bool { return true },
			confirmOnePassword: func(_ *cobra.Command, _ *app.Context) (bool, error) {
				opPromptCalls++
				return true, nil
			},
			confirmAllow: func(_ *cobra.Command, _ *app.Context, _ string, _ []string) (bool, error) {
				allowPromptCalls++
				return true, nil
			},
			vaultPull: func(ctx *app.Context, scope string, _ []string, _ shell.VaultOptions) error {
				if scope == "secrets" {
					return os.WriteFile(filepath.Join(ctx.RepoRoot, "mise.local.toml"), []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\nWS_OP_FILES = \"~/.aws-existing\"\n"), 0o600)
				}
				return nil
			},
			applyZones: func(_ *app.Context) ([]string, error) { return nil, nil },
			isTTY:      func(_ *cobra.Command, _ *app.Context) bool { return true },
		})

		out, err := execBootstrap(t, fx, true, "--profile", "mac", "--yes")
		if err != nil {
			t.Fatalf("bootstrap --yes failed: %v\n%s", err, out)
		}
		if allowPromptCalls != 0 || opPromptCalls != 0 {
			t.Fatalf("expected 0 prompts under --yes, got allow=%d op=%d", allowPromptCalls, opPromptCalls)
		}
		if !seenAllows.Replace || !seenAllows.Sudo || !seenAllows.Remove || !seenAllows.Script || !seenAllows.Yes {
			t.Fatalf("expected --yes to grant all allow scopes and Yes, got %+v", seenAllows)
		}
	})

	t.Run("without_yes_prompts_missing_scopes_and_onepassword_on_tty", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		if err := os.MkdirAll(filepath.Join(fx.home, ".ssh"), 0o700); err != nil {
			t.Fatal(err)
		}
		var promptedScopes []string
		opPrompted := 0

		withBootstrapDeps(t, bootstrapDeps{
			planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
				return &engine.Plan{
					Verb:   "bootstrap",
					Preset: presetID,
					Title:  "pkg",
					Actions: []engine.Action{
						{ID: "os_exec:os/mac/defaults.sh", Kind: engine.ActionOSExec, RequiresAllow: "sudo", Command: []string{"bash", "os/mac/defaults.sh"}},
					},
				}, nil
			},
			execPackages: func(ctx *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				if !ctx.Allow.Sudo {
					t.Fatalf("expected sudo granted after prompt")
				}
				return &engine.ApplyResult{Applied: 1}, nil
			},
			planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
				if opts.ThemeOverride != "" {
					return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "theme"}, nil
				}
				return &engine.Plan{
					Verb:  "apply",
					Host:  engine.FormatPlanHost(ctx),
					Title: "apply",
					Actions: []engine.Action{
						{ID: "sync:tmux-tpm", Kind: engine.ActionHook, RequiresAllow: "script"},
					},
				}, nil
			},
			execApply: func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
				if plan.Theme == "" && !ctx.Allow.Script {
					t.Fatalf("expected script granted after prompt")
				}
				return &engine.ApplyResult{Applied: 1}, nil
			},
			trustMise:  func(_ *app.Context, _ string) error { return nil },
			opSignedIn: func(_ context.Context) bool { return true },
			confirmOnePassword: func(_ *cobra.Command, _ *app.Context) (bool, error) {
				opPrompted++
				return true, nil
			},
			confirmAllow: func(_ *cobra.Command, _ *app.Context, scope string, _ []string) (bool, error) {
				promptedScopes = append(promptedScopes, scope)
				return true, nil
			},
			vaultPull: func(ctx *app.Context, scope string, _ []string, _ shell.VaultOptions) error {
				if scope == "secrets" {
					return os.WriteFile(filepath.Join(ctx.RepoRoot, "mise.local.toml"), []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\n"), 0o600)
				}
				if scope == "ssh" && !ctx.Allow.Replace {
					t.Fatalf("expected replace granted after prompt for existing ~/.ssh")
				}
				return nil
			},
			applyZones: func(_ *app.Context) ([]string, error) { return nil, nil },
			isTTY:      func(_ *cobra.Command, _ *app.Context) bool { return true },
		})

		out, err := execBootstrap(t, fx, true, "--profile", "mac")
		if err != nil {
			t.Fatalf("interactive bootstrap failed: %v\n%s", err, out)
		}
		if opPrompted != 1 {
			t.Fatalf("expected 1Password ready prompt once, got %d", opPrompted)
		}
		for _, wantScope := range []string{"sudo", "script", "replace"} {
			if !slices.Contains(promptedScopes, wantScope) {
				t.Fatalf("expected %q scope prompt, got %v", wantScope, promptedScopes)
			}
		}
	})
}

func TestBootstrapYesIsExplicitNotInferredFromScopes(t *testing.T) {
	allScopes := []engine.Action{
		{ID: "os_exec:os/arch/snapshots.sh", Kind: engine.ActionOSExec, RequiresAllow: "sudo"},
		{ID: "conflict:remove:paru:old", Kind: engine.ActionRemove, RequiresAllow: "remove"},
		{ID: "replace:conf", Kind: engine.ActionSymlink, RequiresAllow: "replace"},
		{ID: "script:hook", Kind: engine.ActionHook, RequiresAllow: "script"},
	}
	run := func(t *testing.T, args ...string) app.AllowSet {
		t.Helper()
		fx := setupBootstrapFixture(t)
		var seen app.AllowSet
		withBootstrapDeps(t, bootstrapDeps{
			planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
				return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg", Actions: allScopes}, nil
			},
			execPackages: func(ctx *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				seen = ctx.Allow
				return &engine.ApplyResult{Applied: len(allScopes)}, nil
			},
			confirmAllow: func(_ *cobra.Command, _ *app.Context, _ string, _ []string) (bool, error) {
				return true, nil
			},
			isTTY: func(_ *cobra.Command, _ *app.Context) bool { return true },
		})
		if out, err := execBootstrap(t, fx, true, append([]string{"--profile", "server", "--packages-only"}, args...)...); err != nil {
			t.Fatalf("bootstrap %v failed: %v\n%s", args, err, out)
		}
		return seen
	}

	t.Run("prompt_grants_of_every_scope", func(t *testing.T) {
		got := run(t)
		if !got.Replace || !got.Sudo || !got.Remove || !got.Script || got.Yes {
			t.Fatalf("expected every scope granted at prompts without Yes, got %+v", got)
		}
	})
	t.Run("listed_scopes", func(t *testing.T) {
		if got := run(t, "--allow", "replace,sudo,remove,script"); got.Yes {
			t.Fatalf("expected listing every scope not to set Yes, got %+v", got)
		}
	})
	t.Run("allow_all", func(t *testing.T) {
		if got := run(t, "--allow", "all"); !got.Yes {
			t.Fatalf("expected --allow all to set Yes, got %+v", got)
		}
	})
	t.Run("yes", func(t *testing.T) {
		if got := run(t, "--yes"); !got.Yes {
			t.Fatalf("expected --yes to set Yes, got %+v", got)
		}
	})
}

func TestBootstrapOnePasswordGateWithAndWithoutSignedInOp(t *testing.T) {
	noopBaseDeps := func(opOK bool, isTTY bool, opPromptAnswer bool, onVault func(ctx *app.Context, scope string) error, onZones func() error) bootstrapDeps {
		return bootstrapDeps{
			planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
				return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
			},
			execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{}, nil
			},
			planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
				return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "apply"}, nil
			},
			execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{}, nil
			},
			trustMise:  func(_ *app.Context, _ string) error { return nil },
			opSignedIn: func(_ context.Context) bool { return opOK },
			confirmOnePassword: func(_ *cobra.Command, _ *app.Context) (bool, error) {
				return opPromptAnswer, nil
			},
			confirmAllow: func(_ *cobra.Command, _ *app.Context, _ string, _ []string) (bool, error) {
				return true, nil
			},
			vaultPull: func(ctx *app.Context, scope string, _ []string, _ shell.VaultOptions) error {
				if onVault != nil {
					return onVault(ctx, scope)
				}
				return nil
			},
			applyZones: func(_ *app.Context) ([]string, error) {
				if onZones != nil {
					return nil, onZones()
				}
				return nil, nil
			},
			isTTY: func(_ *cobra.Command, _ *app.Context) bool { return isTTY },
		}
	}

	t.Run("yes_without_signed_in_op_warns_and_skips", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		vaultCalls := 0
		zoneCalls := 0
		withBootstrapDeps(t, noopBaseDeps(false, false, false,
			func(_ *app.Context, _ string) error { vaultCalls++; return nil },
			func() error { zoneCalls++; return nil },
		))

		out, err := execBootstrap(t, fx, false, "--profile", "mac", "--yes", "--json")
		if err != nil {
			t.Fatalf("bootstrap should still succeed when op is not signed in: %v\n%s", err, out)
		}
		if vaultCalls != 0 || zoneCalls != 0 {
			t.Fatalf("expected 0 vault/zone calls without signed-in op, got vault=%d zone=%d", vaultCalls, zoneCalls)
		}
		var summary bootstrapSummaryOutput
		if err := json.Unmarshal([]byte(out), &summary); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if summary.Phases[3].Status != bootstrapPhaseSkipped || !strings.Contains(summary.Phases[3].Reason, "not signed in") {
			t.Fatalf("expected phase 4 skipped (not signed in), got %+v", summary.Phases[3])
		}
		if summary.Phases[4].Status != bootstrapPhaseSkipped || summary.Phases[5].Status != bootstrapPhaseSkipped {
			t.Fatalf("expected phases 5 and 6 skipped, got %+v %+v", summary.Phases[4], summary.Phases[5])
		}
		if summary.Phases[6].Status != bootstrapPhaseDone {
			t.Fatalf("expected phase 7 theme done, got %+v", summary.Phases[6])
		}
		if len(summary.Warnings) == 0 || !strings.Contains(strings.Join(summary.Warnings, "\n"), "1Password not signed in") {
			t.Fatalf("expected clear warning about 1Password not signed in, got %v", summary.Warnings)
		}
	})

	t.Run("yes_with_signed_in_op_pulls_reloads_and_applies", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		var pulledScopes []string
		zoneCalls := 0
		withBootstrapDeps(t, noopBaseDeps(true, false, false,
			func(ctx *app.Context, scope string) error {
				pulledScopes = append(pulledScopes, scope)
				if scope == "secrets" {
					return os.WriteFile(filepath.Join(ctx.RepoRoot, "mise.local.toml"), []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\nWS_OP_FILES = \"~/.aws/config\"\n"), 0o600)
				}
				return nil
			},
			func() error { zoneCalls++; return nil },
		))

		out, err := execBootstrap(t, fx, false, "--profile", "mac", "--yes", "--json")
		if err != nil {
			t.Fatalf("bootstrap failed: %v\n%s", err, out)
		}
		if !slices.Equal(pulledScopes, []string{"secrets", "ssh", "files"}) {
			t.Fatalf("expected vault pulls [secrets ssh files], got %v", pulledScopes)
		}
		if zoneCalls != 1 {
			t.Fatalf("expected 1 zone apply call, got %d", zoneCalls)
		}
	})

	t.Run("tty_user_declines_onepassword_ready", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		vaultCalls := 0
		withBootstrapDeps(t, noopBaseDeps(true, true, false,
			func(_ *app.Context, _ string) error { vaultCalls++; return nil },
			nil,
		))

		out, err := execBootstrap(t, fx, true, "--profile", "mac", "--json")
		if err != nil {
			t.Fatalf("bootstrap failed: %v\n%s", err, out)
		}
		if vaultCalls != 0 {
			t.Fatalf("expected 0 vault calls when user declined 1Password ready, got %d", vaultCalls)
		}
		var summary bootstrapSummaryOutput
		if err := json.Unmarshal([]byte(out), &summary); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if summary.Phases[3].Status != bootstrapPhaseSkipped || !strings.Contains(summary.Phases[3].Reason, "1Password not ready") {
			t.Fatalf("expected phase 4 skipped (1Password not ready), got %+v", summary.Phases[3])
		}
	})

	t.Run("zones_already_configured_skips_secrets_pull_but_runs_zones_and_restores", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		if err := os.WriteFile(filepath.Join(fx.repo, "mise.local.toml"), []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\nWS_OP_FILES = \"~/.aws/config\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var pulledScopes []string
		zoneCalls := 0
		withBootstrapDeps(t, noopBaseDeps(true, false, false,
			func(_ *app.Context, scope string) error { pulledScopes = append(pulledScopes, scope); return nil },
			func() error { zoneCalls++; return nil },
		))

		out, err := execBootstrap(t, fx, false, "--profile", "mac", "--yes", "--json")
		if err != nil {
			t.Fatalf("bootstrap failed: %v\n%s", err, out)
		}
		var summary bootstrapSummaryOutput
		if err := json.Unmarshal([]byte(out), &summary); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if summary.Phases[3].Status != bootstrapPhaseSkipped || !strings.Contains(summary.Phases[3].Reason, "already configured") {
			t.Fatalf("expected phase 4 skipped (already configured), got %+v", summary.Phases[3])
		}
		if zoneCalls != 1 {
			t.Fatalf("expected zone apply to run, got %d", zoneCalls)
		}
		if !slices.Equal(pulledScopes, []string{"ssh", "files"}) {
			t.Fatalf("expected ssh and files restores without secrets pull, got %v", pulledScopes)
		}
	})
}

func TestBootstrapDryRunMakesNoChanges(t *testing.T) {
	fx := setupBootstrapFixture(t)

	marker := filepath.Join(t.TempDir(), "mutated-marker")
	fakeBin := t.TempDir()
	for _, binName := range []string{"bat", "git", "gh", "mise", "op"} {
		script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"extension\" ] && [ \"$2\" = \"list\" ]; then exit 0; fi\ntouch %q\nexit 0\n", marker)
		if err := os.WriteFile(filepath.Join(fakeBin, binName), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fakeBin)

	deps := newDefaultBootstrapDeps()
	deps.planPackages = func(_ *app.Context, presetID string) (*engine.Plan, error) {
		return &engine.Plan{
			Verb:   "bootstrap",
			Preset: presetID,
			Title:  "Bootstrap preset " + presetID,
			Actions: []engine.Action{
				{ID: "install:brew:ripgrep", Kind: engine.ActionInstall, Summary: "Install ripgrep"},
			},
		}, nil
	}
	deps.execPackages = func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
		if !ctx.DryRun {
			t.Fatalf("expected ctx.DryRun=true in execPackages")
		}
		return &engine.ApplyResult{Skipped: len(plan.Actions)}, nil
	}
	withBootstrapDeps(t, deps)

	out, err := execBootstrap(t, fx, false, "--profile", "mac", "--dry-run")
	if err != nil {
		t.Fatalf("bootstrap --dry-run failed: %v\n%s", err, out)
	}

	for _, want := range []string{
		"1. Packages and OS hooks",
		"Install ripgrep",
		"2. Links and templates",
		"sync:bat-cache",
		"install tmux plugin manager (tpm)",
		"install gh-dash extension",
		"3. Trust repo mise config",
		"4. 1Password secrets",
		"5. Identity zones",
		"6. Vault restores",
		"7. Theme",
		"gruvbox-dark",
		"Bootstrap summary",
		"dry-run complete",
	} {
		if !strings.Contains(out, want) && !(want == "sync:bat-cache" && strings.Contains(out, "rebuild bat cache")) {
			t.Fatalf("expected dry-run output to mention %q, got:\n%s", want, out)
		}
	}

	for _, untouched := range []string{
		marker,
		filepath.Join(fx.repo, "config", "tmux", "colors.conf"),
		filepath.Join(fx.home, ".config", "tmux"),
		filepath.Join(fx.home, ".ws", "ledger.json"),
		filepath.Join(fx.home, ".ws", "theme"),
		filepath.Join(fx.home, "work"),
	} {
		if _, err := os.Lstat(untouched); !os.IsNotExist(err) {
			t.Fatalf("dry-run unexpectedly created or mutated %s (err=%v)", untouched, err)
		}
	}
}

func TestBootstrapSummaryRequiredFailureAndFlags(t *testing.T) {
	t.Run("required_phase_failure_exits_nonzero_and_prints_summary", func(t *testing.T) {
		fx := setupBootstrapFixture(t)
		withBootstrapDeps(t, bootstrapDeps{
			planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
				return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
			},
			execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{Applied: 1}, nil
			},
			planApply: func(ctx *app.Context, _ engine.PlanApplyOptions) (*engine.Plan, error) {
				return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Title: "apply"}, nil
			},
			execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{Applied: 1}, nil
			},
			trustMise: func(_ *app.Context, _ string) error {
				return errors.New("mise trust boom")
			},
			isTTY: func(_ *cobra.Command, _ *app.Context) bool { return false },
		})

		out, err := execBootstrap(t, fx, false, "--profile", "server", "--yes")
		if err == nil {
			t.Fatalf("expected non-zero error when required phase mise-trust fails")
		}
		if ExitCode(err) != 1 {
			t.Fatalf("expected exit code 1, got %d", ExitCode(err))
		}
		for _, want := range []string{
			"Bootstrap summary",
			"Packages and OS hooks",
			"Links and templates",
			"Trust repo mise config",
			"mise trust boom",
			"aborted after mise-trust failed",
			"Bootstrap failed",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("expected %q in failure summary output:\n%s", want, out)
			}
		}
	})

	t.Run("packages_only_full_alias_and_help", func(t *testing.T) {
		fx := setupBootstrapFixture(t)

		helpOut, err := execBootstrap(t, fx, false, "--help")
		if err != nil {
			t.Fatalf("bootstrap --help failed: %v", err)
		}
		for _, want := range []string{"--profile", "--yes", "--packages-only", "--plan", "--save-plan"} {
			if !strings.Contains(helpOut, want) {
				t.Fatalf("expected %q in bootstrap --help:\n%s", want, helpOut)
			}
		}
		if strings.Contains(helpOut, "--full") {
			t.Fatalf("expected --full hidden from bootstrap --help:\n%s", helpOut)
		}

		if _, err := execBootstrap(t, fx, false, "--profile", "bogus"); err == nil || ExitCode(err) != 2 {
			t.Fatalf("expected exit code 2 for invalid --profile, got err=%v code=%d", err, ExitCode(err))
		}
		if _, err := execBootstrap(t, fx, false, "--full", "--packages-only"); err == nil || ExitCode(err) != 2 {
			t.Fatalf("expected exit code 2 for --full + --packages-only, got err=%v code=%d", err, ExitCode(err))
		}

		applyCalled := false
		withBootstrapDeps(t, bootstrapDeps{
			planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
				return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
			},
			execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{Applied: 1}, nil
			},
			planApply: func(_ *app.Context, _ engine.PlanApplyOptions) (*engine.Plan, error) {
				applyCalled = true
				return &engine.Plan{}, nil
			},
			execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
				return &engine.ApplyResult{}, nil
			},
			trustMise: func(_ *app.Context, _ string) error { return nil },
			isTTY:     func(_ *cobra.Command, _ *app.Context) bool { return false },
		})

		if out, err := execBootstrap(t, fx, false, "--profile", "server", "--packages-only", "--yes"); err != nil {
			t.Fatalf("--packages-only failed: %v\n%s", err, out)
		}
		if applyCalled {
			t.Fatalf("expected --packages-only not to run apply phase")
		}

		if out, err := execBootstrap(t, fx, false, "--profile", "server", "--full", "--yes"); err != nil {
			t.Fatalf("hidden --full alias failed: %v\n%s", err, out)
		}
		if !applyCalled {
			t.Fatalf("expected --full alias to run full bootstrap including apply")
		}
	})
}

func TestBootstrapEndToEndHooksZonesVaultMissingDocAndTheme(t *testing.T) {
	fx := setupBootstrapFixture(t)

	logFile := filepath.Join(t.TempDir(), "invocations.log")
	vaultDir := t.TempDir()
	writeTestZip(t, filepath.Join(vaultDir, "workstation-secrets.zip"), map[string]string{
		"mise.local.toml": "[env]\nWS_OP_VAULT = \"Personal\"\nWS_ZONES = \"work|Dev User|dev@example.com\"\nWS_OP_FILES = \"~/.existing-doc\\n~/.missing-doc\"\n",
	})
	writeTestZip(t, filepath.Join(vaultDir, "ssh.zip"), map[string]string{
		".ssh/config": "Host example\n  User dev\n",
	})
	if err := os.WriteFile(filepath.Join(vaultDir, "existing-doc"), []byte("restored-content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	writeStub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeStub("bat", fmt.Sprintf("echo \"bat $*\" >> %q\nexit 0\n", logFile))
	writeStub("gh", fmt.Sprintf(`
if [ "$1" = "extension" ] && [ "$2" = "list" ]; then
  exit 0
fi
echo "gh $*" >> %q
exit 0
`, logFile))
	writeStub("git", fmt.Sprintf(`
echo "git $*" >> %q
if [ "$1" = "clone" ]; then
  dest=""
  for arg in "$@"; do dest="$arg"; done
  mkdir -p "$dest/bin"
  cat <<'EOF' > "$dest/bin/install_plugins"
#!/bin/sh
echo "tpm install_plugins" >> %q
EOF
  chmod +x "$dest/bin/install_plugins"
fi
exit 0
`, logFile, logFile))
	writeStub("mise", fmt.Sprintf(`
echo "mise $*" >> %q
if [ "$1" = "trust" ] && [ "$2" = "--show" ]; then
  exit 1
fi
exit 0
`, logFile))
	writeStub("op", fmt.Sprintf(`
if [ "$1" = "whoami" ]; then
  echo '{"email":"dev@example.com"}'
  exit 0
fi
if [ "$1" = "document" ] && [ "$2" = "list" ]; then
  cat <<'JSON'
[
  {"id":"id-secrets","title":"workstation-secrets.zip"},
  {"id":"id-ssh","title":"~/.ssh.zip"},
  {"id":"id-existing","title":"~/.existing-doc"}
]
JSON
  exit 0
fi
if [ "$1" = "document" ] && [ "$2" = "get" ]; then
  id="$3"
  out=""
  shift 3
  while [ $# -gt 0 ]; do
    if [ "$1" = "--out-file" ]; then out="$2"; shift 2; continue; fi
    shift
  done
  case "$id" in
    id-secrets) cp %q "$out" ;;
    id-ssh) cp %q "$out" ;;
    id-existing) cp %q "$out" ;;
    *) exit 1 ;;
  esac
  exit 0
fi
exit 1
`, filepath.Join(vaultDir, "workstation-secrets.zip"), filepath.Join(vaultDir, "ssh.zip"), filepath.Join(vaultDir, "existing-doc")))

	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := os.MkdirAll(filepath.Join(fx.home, ".ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.home, ".ws", "theme"), []byte("catppuccin-dark\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps := newDefaultBootstrapDeps()
	deps.planPackages = func(_ *app.Context, presetID string) (*engine.Plan, error) {
		return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
	}
	deps.execPackages = func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
		return &engine.ApplyResult{}, nil
	}
	withBootstrapDeps(t, deps)

	out, err := execBootstrap(t, fx, false, "--profile", "mac", "--yes")
	if err != nil {
		t.Fatalf("end-to-end bootstrap failed: %v\n%s", err, out)
	}

	invRaw, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read invocations log: %v", err)
	}
	inv := string(invRaw)
	for _, want := range []string{
		"bat cache --build",
		"tpm install_plugins",
		"gh extension install dlvhdr/gh-dash",
		"mise trust",
	} {
		if !strings.Contains(inv, want) {
			t.Fatalf("expected %q in tool invocations, got:\n%s", want, inv)
		}
	}

	if _, err := os.Stat(filepath.Join(fx.home, "work", ".gitconfig")); err != nil {
		t.Fatalf("expected work zone .gitconfig created after in-process WS_ZONES reload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fx.home, ".ssh", "config")); err != nil {
		t.Fatalf("expected ~/.ssh/config restored: %v", err)
	}
	restoredDoc, err := os.ReadFile(filepath.Join(fx.home, ".existing-doc"))
	if err != nil || string(restoredDoc) != "restored-content\n" {
		t.Fatalf("expected ~/.existing-doc restored, got %q err=%v", string(restoredDoc), err)
	}
	if !strings.Contains(out, "skip ~/.missing-doc") {
		t.Fatalf("expected warning for missing vault document ~/.missing-doc, got:\n%s", out)
	}

	renderedColors, err := os.ReadFile(filepath.Join(fx.repo, "config", "tmux", "colors.conf"))
	if err != nil || !strings.Contains(string(renderedColors), "#1e1e2e") {
		t.Fatalf("expected active theme catppuccin-dark preserved, got %q err=%v", string(renderedColors), err)
	}
	activeThemeFile, err := os.ReadFile(filepath.Join(fx.home, ".ws", "theme"))
	if err != nil || strings.TrimSpace(string(activeThemeFile)) != "catppuccin-dark" {
		t.Fatalf("expected ~/.ws/theme=catppuccin-dark, got %q err=%v", string(activeThemeFile), err)
	}
}

func TestBootstrapRestoresRespectReplaceConsentAndDefaultTheme(t *testing.T) {
	fx := setupBootstrapFixture(t)
	if err := os.WriteFile(filepath.Join(fx.repo, "mise.local.toml"), []byte("[env]\nWS_ZONES = \"work|Dev|dev@example.com\"\nWS_OP_FILES = \"~/.kept-file\\n~/.fresh-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keptPath := filepath.Join(fx.home, ".kept-file")
	if err := os.WriteFile(keptPath, []byte("local-original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var pulledFilesArgs [][]string
	var pulledScopes []string
	withBootstrapDeps(t, bootstrapDeps{
		planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
			return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
		},
		execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{}, nil
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return engine.PlanApplyWithConfig(ctx, opts)
		},
		execApply: func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
			return engine.ExecApply(ctx, plan)
		},
		trustMise:  func(_ *app.Context, _ string) error { return nil },
		opSignedIn: func(_ context.Context) bool { return true },
		confirmAllow: func(_ *cobra.Command, _ *app.Context, scope string, _ []string) (bool, error) {
			return scope != "replace", nil
		},
		vaultPull: func(_ *app.Context, scope string, args []string, _ shell.VaultOptions) error {
			pulledScopes = append(pulledScopes, scope)
			if scope == "files" {
				pulledFilesArgs = append(pulledFilesArgs, slices.Clone(args))
			}
			return nil
		},
		applyZones: func(_ *app.Context) ([]string, error) { return nil, nil },
		isTTY:      func(_ *cobra.Command, _ *app.Context) bool { return true },
	})

	fakeBin := t.TempDir()
	t.Setenv("PATH", fakeBin)

	out, err := execBootstrap(t, fx, true, "--profile", "mac")
	if err != nil {
		t.Fatalf("bootstrap failed: %v\n%s", err, out)
	}
	if !slices.Equal(pulledScopes, []string{"ssh", "files"}) {
		t.Fatalf("expected ssh and files pull, got %v", pulledScopes)
	}
	if len(pulledFilesArgs) != 1 || !slices.Equal(pulledFilesArgs[0], []string{"~/.fresh-file"}) {
		t.Fatalf("expected only non-existing ~/.fresh-file pulled without replace consent, got %v", pulledFilesArgs)
	}
	gotKept, err := os.ReadFile(keptPath)
	if err != nil || string(gotKept) != "local-original\n" {
		t.Fatalf("expected existing local file untouched, got %q err=%v", string(gotKept), err)
	}
	activeThemeFile, err := os.ReadFile(filepath.Join(fx.home, ".ws", "theme"))
	if err != nil || strings.TrimSpace(string(activeThemeFile)) != "gruvbox-dark" {
		t.Fatalf("expected default theme gruvbox-dark when unset, got %q err=%v", string(activeThemeFile), err)
	}
}

func TestPresetForServerProfileByDistro(t *testing.T) {
	cases := map[string]string{"ubuntu": "ubuntu-runner", "debian": "debian-server", "arch": "server", "fedora": "server", "": "server"}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for distro, want := range cases {
		ctx := &app.Context{RepoRoot: root}
		ctx.Host.Distro = distro
		if got := presetForProfile(ctx, "server"); got != want {
			t.Errorf("distro %q: preset %q, want %q", distro, got, want)
		}
	}
}

func TestBootstrapConsentSkipContinuesRemainingPhases(t *testing.T) {
	fx := setupBootstrapFixture(t)
	var events []string
	withBootstrapDeps(t, bootstrapDeps{
		planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
			return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
		},
		execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			events = append(events, "packages:exec")
			return &engine.ApplyResult{Applied: 2, Skipped: 1, ConsentSkipped: 1, MissingAllows: []string{"sudo"}}, nil
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "apply"}, nil
		},
		execApply: func(_ *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
			if plan.Theme != "" {
				events = append(events, "theme:exec")
				return &engine.ApplyResult{Applied: 1}, nil
			}
			events = append(events, "apply:exec")
			return &engine.ApplyResult{Applied: 3, Skipped: 1, ConsentSkipped: 1, MissingAllows: []string{"replace"}, Warnings: []string{"kept existing ~/.zshenv (requires --allow replace)"}}, nil
		},
		trustMise: func(_ *app.Context, _ string) error {
			events = append(events, "mise-trust")
			return nil
		},
		isTTY: func(_ *cobra.Command, _ *app.Context) bool { return false },
	})

	out, err := execBootstrap(t, fx, false, "--profile", "server", "--json")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("expected exit code 1 after consent skips, got err=%v code=%d\n%s", err, ExitCode(err), out)
	}
	if !strings.Contains(err.Error(), "2 steps skipped") || !strings.Contains(err.Error(), "sudo") || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("expected aggregated consent skip error, got %v", err)
	}
	if want := []string{"packages:exec", "apply:exec", "mise-trust", "theme:exec"}; !slices.Equal(events, want) {
		t.Fatalf("expected every phase to run after a consent skip:\ngot:  %v\nwant: %v", events, want)
	}
	var summary bootstrapSummaryOutput
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("unmarshal summary JSON: %v\n%s", err, out)
	}
	if summary.Skipped != 2 {
		t.Fatalf("summary consentSkipped = %d, want 2", summary.Skipped)
	}
	wantStatus := map[string]bootstrapPhaseStatus{
		"packages":   bootstrapPhasePartial,
		"apply":      bootstrapPhasePartial,
		"mise-trust": bootstrapPhaseDone,
		"theme":      bootstrapPhaseDone,
	}
	for _, ph := range summary.Phases {
		if want, ok := wantStatus[ph.ID]; ok && ph.Status != want {
			t.Fatalf("phase %s status = %s, want %s (%+v)", ph.ID, ph.Status, want, ph)
		}
	}

	events = nil
	textOut, err := execBootstrap(t, fx, false, "--profile", "server")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("expected exit code 1 in text mode, got err=%v", err)
	}
	for _, want := range []string{"Bootstrap summary", "Bootstrap finished with skipped steps", "kept existing ~/.zshenv"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("expected %q in output:\n%s", want, textOut)
		}
	}
	if strings.Contains(textOut, "Bootstrap failed") {
		t.Fatalf("consent skips must not be reported as a failed bootstrap:\n%s", textOut)
	}
}

func TestBootstrapRefusesProfileForOtherHostOS(t *testing.T) {
	fx := setupBootstrapFixture(t)
	ctx := &app.Context{RepoRoot: fx.repo}
	linux := app.HostInfo{OS: "linux", Distro: "debian"}
	arch := app.HostInfo{OS: "linux", Distro: "endeavouros", DistroLike: []string{"arch"}}
	mac := app.HostInfo{OS: "darwin", Distro: "macos"}
	cases := []struct {
		host     app.HostInfo
		profile  string
		preset   string
		mismatch bool
	}{
		{mac, "mac", "mac-workstation", false},
		{mac, "arch", "arch-desktop", true},
		{mac, "server", "debian-server", true},
		{arch, "arch", "arch-desktop", false},
		{arch, "mac", "mac-workstation", true},
		{linux, "server", "debian-server", false},
		{linux, "arch", "arch-desktop", true},
	}
	for _, tc := range cases {
		got := bootstrapHostMismatch(ctx, tc.host, tc.profile, tc.preset)
		if (got != "") != tc.mismatch {
			t.Fatalf("bootstrapHostMismatch(%s on %s/%s) = %q, want mismatch=%v", tc.profile, tc.host.OS, tc.host.Distro, got, tc.mismatch)
		}
	}

	planned := false
	withBootstrapDeps(t, bootstrapDeps{
		planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
			planned = true
			return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
		},
		execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{}, nil
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "apply"}, nil
		},
		execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{}, nil
		},
		trustMise: func(_ *app.Context, _ string) error { return nil },
		isTTY:     func(_ *cobra.Command, _ *app.Context) bool { return false },
		hostMismatch: func(ctx *app.Context, profile, presetID string) string {
			return bootstrapHostMismatch(ctx, linux, profile, presetID)
		},
	})

	out, err := execBootstrap(t, fx, false, "--profile", "mac", "--yes")
	if err == nil || !strings.Contains(err.Error(), "profile mac needs a macOS host") {
		t.Fatalf("expected real run of --profile mac on linux to be refused, got err=%v\n%s", err, out)
	}
	if planned {
		t.Fatalf("refused bootstrap must not plan packages")
	}

	out, err = execBootstrap(t, fx, false, "--profile", "mac", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run of a mismatched profile should still plan: %v\n%s", err, out)
	}
	if !planned || !strings.Contains(out, "profile mac needs a macOS host") {
		t.Fatalf("expected dry-run plan with a host mismatch warning, planned=%v\n%s", planned, out)
	}
}

func TestBootstrapDeclinedHookIsPartialAndHintsYes(t *testing.T) {
	fx := setupBootstrapFixture(t)
	withBootstrapDeps(t, bootstrapDeps{
		planPackages: func(_ *app.Context, presetID string) (*engine.Plan, error) {
			return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg"}, nil
		},
		execPackages: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{Applied: 2, Skipped: 1, ConsentSkipped: 1, Declined: 1, Warnings: []string{"declined os/arch/snapshots.sh (rerun with --yes to accept)"}}, nil
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "apply"}, nil
		},
		execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{Applied: 1}, nil
		},
		trustMise: func(_ *app.Context, _ string) error { return nil },
		isTTY:     func(_ *cobra.Command, _ *app.Context) bool { return false },
	})

	out, err := execBootstrap(t, fx, false, "--profile", "server", "--json")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("expected exit code 1 after a declined hook, got err=%v code=%d\n%s", err, ExitCode(err), out)
	}
	if got := err.Error(); got != "1 step skipped, rerun with --yes to accept declined hooks" {
		t.Fatalf("unexpected declined-hook error: %q", got)
	}
	var summary bootstrapSummaryOutput
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("unmarshal summary JSON: %v\n%s", err, out)
	}
	for _, ph := range summary.Phases {
		if ph.ID == "packages" && ph.Status != bootstrapPhasePartial {
			t.Fatalf("packages phase status = %s, want partial (%+v)", ph.Status, ph)
		}
	}
}

type exitCodeRunner struct{ err error }

func (r exitCodeRunner) Run([]string, map[string]string, string, bool) (string, string, error) {
	return "", "", r.err
}

func (exitCodeRunner) LookPath(bin string) (string, error) { return "/usr/bin/" + bin, nil }

func TestBootstrapHookDeclinedAtPromptKeepsRunGreen(t *testing.T) {
	fx := setupBootstrapFixture(t)
	exitErr := exec.Command("sh", "-c", fmt.Sprintf("exit %d", pkgmgr.HookUserDeclinedExitCode)).Run()
	if exitErr == nil {
		t.Fatal("expected a non-zero exit")
	}
	hookRel := "os/arch/snapshots.sh"
	var pkgRes *engine.ApplyResult
	withBootstrapDeps(t, bootstrapDeps{
		planPackages: func(ctx *app.Context, presetID string) (*engine.Plan, error) {
			hookPath := filepath.Join(ctx.RepoRoot, hookRel)
			return &engine.Plan{Verb: "bootstrap", Preset: presetID, Title: "pkg", Actions: []engine.Action{{
				ID:            "os_exec:" + hookRel,
				Kind:          engine.ActionOSExec,
				Target:        hookPath,
				Source:        hookRel,
				RequiresAllow: "sudo",
				Command:       []string{"bash", hookPath},
				Metadata:      map[string]string{"hook": hookRel},
			}}}, nil
		},
		execPackages: func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
			res, err := pkgmgr.ExecBootstrap(ctx, plan, exitCodeRunner{err: exitErr})
			pkgRes = res
			return res, err
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return &engine.Plan{Verb: "apply", Host: engine.FormatPlanHost(ctx), Theme: opts.ThemeOverride, Title: "apply"}, nil
		},
		execApply: func(_ *app.Context, _ *engine.Plan) (*engine.ApplyResult, error) {
			return &engine.ApplyResult{}, nil
		},
		trustMise: func(_ *app.Context, _ string) error { return nil },
		isTTY:     func(_ *cobra.Command, _ *app.Context) bool { return false },
	})

	out, err := execBootstrap(t, fx, false, "--profile", "server", "--allow", "sudo", "--json")
	if err != nil {
		t.Fatalf("expected exit 0 after a hook declined at its prompt, got err=%v code=%d\n%s", err, ExitCode(err), out)
	}
	if pkgRes == nil || pkgRes.Applied != 0 || pkgRes.Skipped != 1 || pkgRes.ConsentSkipped != 0 || pkgRes.Declined != 0 {
		t.Fatalf("expected a plain skip, got %+v", pkgRes)
	}
	if !slices.Contains(pkgRes.Warnings, "skipped "+hookRel+" (declined at prompt)") {
		t.Fatalf("expected declined-at-prompt warning, got %v", pkgRes.Warnings)
	}
	var summary bootstrapSummaryOutput
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("unmarshal summary JSON: %v\n%s", err, out)
	}
	if summary.Skipped != 0 || summary.Error != "" {
		t.Fatalf("expected no consent skips or error in summary, got %+v", summary)
	}
	for _, ph := range summary.Phases {
		if ph.ID == "packages" && ph.Status != bootstrapPhaseDone {
			t.Fatalf("packages phase status = %s, want done (%+v)", ph.Status, ph)
		}
	}
}

func TestConsentSkippedErrorHints(t *testing.T) {
	for _, tc := range []struct {
		res  engine.ApplyResult
		want string
	}{
		{engine.ApplyResult{ConsentSkipped: 2, MissingAllows: []string{"sudo"}}, "2 steps skipped, rerun with --allow sudo"},
		{engine.ApplyResult{ConsentSkipped: 1, Declined: 1}, "1 step skipped, rerun with --yes to accept declined hooks"},
		{engine.ApplyResult{ConsentSkipped: 3, Declined: 1, MissingAllows: []string{"sudo", "remove"}}, "3 steps skipped, rerun with --allow sudo,remove (declined hooks need --yes)"},
		{engine.ApplyResult{ConsentSkipped: 1}, "1 step skipped due to missing consent"},
	} {
		err := consentSkippedError(&tc.res)
		if err == nil || err.Error() != tc.want || ExitCode(err) != 1 {
			t.Errorf("consentSkippedError(%+v) = %v, want %q", tc.res, err, tc.want)
		}
	}
	if consentSkippedError(&engine.ApplyResult{}) != nil {
		t.Errorf("expected nil error without consent skips")
	}
}
