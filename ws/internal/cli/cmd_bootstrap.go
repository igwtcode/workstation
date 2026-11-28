package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/opcli"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

type bootstrapPhaseStatus string

const (
	bootstrapPhaseDone    bootstrapPhaseStatus = "done"
	bootstrapPhaseSkipped bootstrapPhaseStatus = "skipped"
	bootstrapPhaseFailed  bootstrapPhaseStatus = "failed"
	bootstrapPhasePartial bootstrapPhaseStatus = "partial"
)

type bootstrapPhaseResult struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Required bool                 `json:"required"`
	Status   bootstrapPhaseStatus `json:"status"`
	Reason   string               `json:"reason,omitempty"`
	Detail   string               `json:"detail,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
}

type bootstrapSummaryOutput struct {
	Profile  string                 `json:"profile"`
	Preset   string                 `json:"preset"`
	Theme    string                 `json:"theme,omitempty"`
	DryRun   bool                   `json:"dryRun"`
	Phases   []bootstrapPhaseResult `json:"phases"`
	Warnings []string               `json:"warnings,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Skipped  int                    `json:"consentSkipped,omitempty"`
}

type bootstrapDeps struct {
	planPackages       func(ctx *app.Context, presetID string) (*engine.Plan, error)
	execPackages       func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error)
	planApply          func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error)
	execApply          func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error)
	trustMise          func(ctx *app.Context, configPath string) error
	opSignedIn         func(ctx context.Context) bool
	confirmOnePassword func(cmd *cobra.Command, ctx *app.Context) (bool, error)
	confirmAllow       func(cmd *cobra.Command, ctx *app.Context, scope string, actions []string) (bool, error)
	vaultPull          func(ctx *app.Context, scope string, args []string, opts shell.VaultOptions) error
	applyZones         func(ctx *app.Context) ([]string, error)
	isTTY              func(cmd *cobra.Command, ctx *app.Context) bool
	hostMismatch       func(ctx *app.Context, profile, presetID string) string
}

var defaultBootstrapDeps = newDefaultBootstrapDeps()

func newDefaultBootstrapDeps() bootstrapDeps {
	return bootstrapDeps{
		planPackages: func(ctx *app.Context, presetID string) (*engine.Plan, error) {
			return pkgmgr.PlanBootstrap(ctx, presetID)
		},
		execPackages: func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
			return pkgmgr.ExecBootstrap(ctx, plan, nil)
		},
		planApply: func(ctx *app.Context, opts engine.PlanApplyOptions) (*engine.Plan, error) {
			return engine.PlanApplyWithConfig(ctx, opts)
		},
		execApply: func(ctx *app.Context, plan *engine.Plan) (*engine.ApplyResult, error) {
			return engine.ExecApply(ctx, plan)
		},
		trustMise:          defaultTrustMise,
		opSignedIn:         defaultOpSignedIn,
		confirmOnePassword: defaultConfirmOnePassword,
		confirmAllow: func(cmd *cobra.Command, ctx *app.Context, scope string, actions []string) (bool, error) {
			return confirmAllowScope(cmd, ctx, scope, actions)
		},
		vaultPull: func(ctx *app.Context, scope string, args []string, opts shell.VaultOptions) error {
			return shell.VaultOpWithOptions(ctx, "pull", scope, args, opts)
		},
		applyZones: func(ctx *app.Context) ([]string, error) {
			return shell.ApplyZones(ctx, "")
		},
		isTTY: isInteractiveTTY,
		hostMismatch: func(ctx *app.Context, profile, presetID string) string {
			return bootstrapHostMismatch(ctx, ctx.Host, profile, presetID)
		},
	}
}

func defaultOpSignedIn(ctx context.Context) bool {
	_, ok := opcli.IsSignedIn(ctx)
	return ok
}

func defaultConfirmOnePassword(cmd *cobra.Command, ctx *app.Context) (bool, error) {
	if !isInteractiveTTY(cmd, ctx) {
		return false, nil
	}
	var ready bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("1Password ready?").
			Affirmative("Yes").
			Negative("Skip").
			Value(&ready),
	)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
	if err := form.Run(); err != nil {
		return false, err
	}
	return ready, nil
}

func findMiseBin(ctx *app.Context) (string, error) {
	if p, err := exec.LookPath("mise"); err == nil {
		return p, nil
	}
	if ctx != nil && ctx.HomeDir != "" {
		cand := filepath.Join(ctx.HomeDir, ".local", "bin", "mise")
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("mise not found on PATH")
}

func bootstrapCmdEnv(ctx *app.Context) []string {
	base := app.PinnedMachineEnv(os.Environ())
	if ctx == nil {
		return base
	}
	overrides := map[string]string{}
	if ctx.HomeDir != "" {
		overrides["HOME"] = ctx.HomeDir
	}
	if ctx.ConfigDir != "" {
		overrides["XDG_CONFIG_HOME"] = ctx.ConfigDir
	}
	if ctx.DataDir != "" {
		overrides["XDG_DATA_HOME"] = ctx.DataDir
	}
	if ctx.StateDir != "" {
		overrides["XDG_STATE_HOME"] = ctx.StateDir
	}
	if ctx.CacheDir != "" {
		overrides["XDG_CACHE_HOME"] = ctx.CacheDir
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if ok {
			if _, drop := overrides[k]; drop {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

func defaultTrustMise(ctx *app.Context, configPath string) error {
	miseBin, err := findMiseBin(ctx)
	if err != nil {
		return err
	}
	args := []string{"trust"}
	if strings.TrimSpace(configPath) != "" {
		args = append(args, configPath)
	}
	c := exec.Command(miseBin, args...)
	if ctx != nil && ctx.RepoRoot != "" {
		c.Dir = ctx.RepoRoot
	}
	c.Env = bootstrapCmdEnv(ctx)
	out, err := c.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("mise trust failed: %w: %s", err, msg)
		}
		return fmt.Errorf("mise trust failed: %w", err)
	}
	return nil
}

func newBootstrapCmd(opts *GlobalOpts) *cobra.Command {
	var (
		profileFlag  string
		yesFlag      bool
		packagesOnly bool
		fullFlag     bool
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap machine packages, dotfiles, zones, and theme",
		Long:  "Bootstrap this machine: install packages and OS hooks, sync links and templates, trust repo mise config, pull 1Password secrets and apply identity zones and restores (desktop profiles), and render the theme.\n\nRe-running is safe. Pass --yes to approve all consent scopes without prompting, --packages-only for packages and OS hooks only, or -n/--dry-run to preview the full plan.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fullFlag && packagesOnly {
				return &UsageError{Err: fmt.Errorf("--full and --packages-only are mutually exclusive")}
			}
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			profile, presetID, err := resolveBootstrapProfile(cmd, ctx, profileFlag)
			if err != nil {
				return err
			}
			ctx.PresetOverride = presetID
			if yesFlag {
				ctx.Allow = app.AllowSet{
					Replace: true,
					Sudo:    true,
					Remove:  true,
					Script:  true,
					Yes:     true,
				}
			}
			deps := defaultBootstrapDeps
			if deps.hostMismatch != nil {
				if msg := deps.hostMismatch(ctx, profile, presetID); msg != "" {
					if !ctx.DryRun {
						return &UsageError{Err: fmt.Errorf("%s; refusing to bootstrap (use -n/--dry-run to preview)", msg)}
					}
					if ctx.JSON {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", msg)
					} else {
						ui.NewLogger(cmd.OutOrStdout(), loadWriterRoles(ctx, cmd.OutOrStdout())).Warn(msg + " (dry-run preview only)")
					}
				}
			}
			if packagesOnly {
				return runBootstrapPackagesOnly(cmd, opts, ctx, presetID, yesFlag, deps)
			}
			return runBootstrapFull(cmd, opts, ctx, profile, presetID, yesFlag, deps)
		},
	}

	cmd.Flags().StringVarP(&profileFlag, "profile", "p", "", "Machine profile override: mac, arch, or server (default: detected)")
	cmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "Approve all consent scopes and skip confirmation prompts")
	cmd.Flags().BoolVar(&packagesOnly, "packages-only", false, "Install packages and OS hooks only")
	cmd.Flags().BoolVar(&fullFlag, "full", false, "Run full machine bootstrap (default)")
	_ = cmd.Flags().MarkHidden("full")
	addPlanFlags(cmd, opts)
	_ = cmd.RegisterFlagCompletionFunc("profile", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"mac", "arch", "server"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func validateBootstrapProfile(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "mac", "arch", "server":
		return p, nil
	default:
		return "", &UsageError{Err: fmt.Errorf("unknown profile %q (expected mac, arch, or server)", raw)}
	}
}

func bootstrapHostMismatch(ctx *app.Context, host app.HostInfo, profile, presetID string) string {
	hostProfile := detectProfileFromHost(host)
	switch profile {
	case "mac":
		if host.OS != "darwin" {
			return fmt.Sprintf("profile mac needs a macOS host, this host is %s", bootstrapHostLabel(host))
		}
	case "arch":
		if hostProfile != "arch" {
			return fmt.Sprintf("profile arch needs an Arch-based Linux host, this host is %s", bootstrapHostLabel(host))
		}
	case "server":
		if host.OS != "linux" {
			return fmt.Sprintf("profile server needs a Linux host, this host is %s", bootstrapHostLabel(host))
		}
	}
	if ctx != nil && ctx.RepoRoot != "" && presetID != "" {
		if presets, err := config.LoadPresets(ctx.RepoRoot); err == nil {
			if want := strings.TrimSpace(presets[presetID].When.OS); want != "" && want != host.OS {
				return fmt.Sprintf("preset %s targets %s, this host is %s", presetID, want, bootstrapHostLabel(host))
			}
		}
	}
	return ""
}

func bootstrapHostLabel(host app.HostInfo) string {
	if host.Distro != "" && host.Distro != host.OS {
		return host.OS + "/" + host.Distro
	}
	if host.OS == "" {
		return "unknown"
	}
	return host.OS
}

func detectProfileFromHost(host app.HostInfo) string {
	if host.OS == "darwin" {
		return "mac"
	}
	if host.Distro == "arch" || host.Distro == "cachyos" || slices.Contains(host.DistroLike, "arch") {
		return "arch"
	}
	return "server"
}

func inferProfileFromPreset(ctx *app.Context, presetID string) string {
	if ctx != nil && ctx.RepoRoot != "" && presetID != "" {
		if presets, err := config.LoadPresets(ctx.RepoRoot); err == nil {
			tags := presets[presetID].Tags
			if resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, presetID); err == nil {
				if resolved.Secrets == "none" || slices.Contains(tags, "server") || slices.Contains(tags, "headless") {
					return "server"
				}
				if slices.Contains(tags, "mac") || presetID == "mac-workstation" {
					return "mac"
				}
				if slices.Contains(tags, "arch") || presetID == "arch-desktop" || presetID == "cachyos-desktop" {
					return "arch"
				}
			}
		}
	}
	if ctx != nil {
		return detectProfileFromHost(ctx.Host)
	}
	return "server"
}

func presetForProfile(ctx *app.Context, profile string) string {
	var presets map[string]config.Preset
	if ctx != nil && ctx.RepoRoot != "" {
		presets, _ = config.LoadPresets(ctx.RepoRoot)
	}
	pick := func(candidates ...string) string {
		if len(presets) > 0 {
			for _, c := range candidates {
				if _, ok := presets[c]; ok {
					return c
				}
			}
		}
		if len(candidates) > 0 {
			return candidates[0]
		}
		return ""
	}
	switch profile {
	case "mac":
		return pick("mac-workstation", "dev-cli", "base")
	case "arch":
		if ctx != nil && ctx.Host.Distro == "cachyos" {
			return pick("cachyos-desktop", "arch-desktop", "dev-cli", "base")
		}
		return pick("arch-desktop", "cachyos-desktop", "dev-cli", "base")
	default:
		distro := ""
		if ctx != nil {
			distro = ctx.Host.Distro
		}
		switch distro {
		case "ubuntu":
			return pick("ubuntu-runner", "debian-server", "server", "base")
		case "debian":
			return pick("debian-server", "server", "base")
		}
		return pick("server", "debian-server", "base")
	}
}

func resolveBootstrapProfile(cmd *cobra.Command, ctx *app.Context, profileFlag string) (string, string, error) {
	presetFlagChanged := cmd != nil && cmd.Flags().Changed("preset")
	rawProfile := strings.TrimSpace(profileFlag)

	if rawProfile != "" {
		profile, err := validateBootstrapProfile(rawProfile)
		if err != nil {
			return "", "", err
		}
		if presetFlagChanged && ctx.PresetOverride != "" {
			return profile, ctx.PresetOverride, nil
		}
		return profile, presetForProfile(ctx, profile), nil
	}

	if ctx.PresetOverride != "" {
		return inferProfileFromPreset(ctx, ctx.PresetOverride), ctx.PresetOverride, nil
	}

	if envProfile := strings.TrimSpace(ctx.LookupEnv("WS_PROFILE")); envProfile != "" {
		profile, err := validateBootstrapProfile(envProfile)
		if err != nil {
			return "", "", err
		}
		return profile, presetForProfile(ctx, profile), nil
	}

	profile := detectProfileFromHost(ctx.Host)
	presetID := ctx.EffectivePreset()
	if presetID == "" {
		presetID = presetForProfile(ctx, profile)
	}
	return profile, presetID, nil
}

func promptBootstrapAllows(cmd *cobra.Command, ctx *app.Context, plan *engine.Plan, yesFlag bool, deps bootstrapDeps) error {
	if ctx == nil || ctx.DryRun || yesFlag || ctx.NonInteractive || ctx.JSON || plan == nil {
		return nil
	}
	order, byScope := pkgmgr.MissingPlanScopes(ctx.Allow, plan)
	if len(order) == 0 {
		return nil
	}
	for _, scope := range order {
		ok, err := deps.confirmAllow(cmd, ctx, scope, byScope[scope])
		if err != nil {
			return err
		}
		if ok {
			grantAllowScope(&ctx.Allow, scope)
		}
	}
	return nil
}

func runBootstrapPackagesOnly(cmd *cobra.Command, opts *GlobalOpts, ctx *app.Context, presetID string, yesFlag bool, deps bootstrapDeps) error {
	outW := cmd.OutOrStdout()
	roles := loadWriterRoles(ctx, outW)
	var (
		plan *engine.Plan
		err  error
	)
	if opts.PlanPath != "" {
		plan, err = engine.LoadPlan(opts.PlanPath)
	} else if !ctx.JSON {
		err = ui.Spin(outW, roles, fmt.Sprintf("Planning bootstrap (%s)", presetID), func() error {
			var perr error
			plan, perr = deps.planPackages(ctx, presetID)
			return perr
		})
	} else {
		plan, err = deps.planPackages(ctx, presetID)
	}
	if err != nil {
		return err
	}
	if opts.SavePlanPath != "" {
		if err := plan.Save(opts.SavePlanPath); err != nil {
			return err
		}
		if ctx.JSON {
			return writeJSON(outW, plan)
		}
		fmt.Fprintf(outW, "Saved bootstrap plan (%d actions) to %s\n", len(plan.Actions), opts.SavePlanPath)
		return nil
	}
	if err := promptBootstrapAllows(cmd, ctx, plan, yesFlag, deps); err != nil {
		return err
	}
	res, err := deps.execPackages(ctx, plan)
	if err != nil {
		return err
	}
	skipErr := consentSkippedError(res)
	if ctx.JSON {
		if werr := writeJSON(outW, applyJSONOutput{Plan: plan, Result: res}); werr != nil {
			return werr
		}
		return skipErr
	}
	printPlanSummary(outW, roles, plan, ctx.DryRun)
	if !ctx.DryRun {
		printApplyResult(outW, roles, res, false)
		if skipErr != nil {
			ui.NewLogger(outW, roles).Warn(skipErr.Error())
		}
	}
	return skipErr
}

type bootstrapPhaseSpec struct {
	id       string
	name     string
	required bool
}

var bootstrapPhasesOrder = []bootstrapPhaseSpec{
	{id: "packages", name: "Packages and OS hooks", required: true},
	{id: "apply", name: "Links and templates", required: true},
	{id: "mise-trust", name: "Trust repo mise config", required: true},
	{id: "onepassword", name: "1Password secrets", required: false},
	{id: "zones", name: "Identity zones", required: false},
	{id: "restores", name: "Vault restores", required: false},
	{id: "theme", name: "Theme", required: true},
}

func runBootstrapFull(cmd *cobra.Command, opts *GlobalOpts, ctx *app.Context, profile, presetID string, yesFlag bool, deps bootstrapDeps) error {
	outW := cmd.OutOrStdout()
	errW := cmd.ErrOrStderr()
	roles := loadWriterRoles(ctx, outW)
	log := ui.NewLogger(outW, roles)
	isDesktop := profile != "server"

	results := make([]bootstrapPhaseResult, len(bootstrapPhasesOrder))
	for i, sp := range bootstrapPhasesOrder {
		results[i] = bootstrapPhaseResult{
			ID:       sp.id,
			Name:     sp.name,
			Required: sp.required,
			Status:   bootstrapPhaseSkipped,
		}
	}
	var allWarnings []string
	addWarn := func(idx int, msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" {
			return
		}
		results[idx].Warnings = append(results[idx].Warnings, msg)
		allWarnings = append(allWarnings, msg)
		if !ctx.JSON {
			log.Warn(msg)
		}
	}

	consentTotal := &engine.ApplyResult{}
	recordPhaseConsent := func(idx int, res *engine.ApplyResult) bool {
		if res == nil || res.ConsentSkipped <= 0 {
			return false
		}
		consentTotal.ConsentSkipped += res.ConsentSkipped
		consentTotal.Declined += res.Declined
		for _, sc := range res.MissingAllows {
			if !slices.Contains(consentTotal.MissingAllows, sc) {
				consentTotal.MissingAllows = append(consentTotal.MissingAllows, sc)
			}
		}
		if skipErr := consentSkippedError(res); skipErr != nil {
			addWarn(idx, skipErr.Error())
			results[idx].Status = bootstrapPhasePartial
			results[idx].Reason = skipErr.Error()
		}
		return true
	}

	var activeTheme string
	finish := func(failedIdx int, runErr error) error {
		if failedIdx >= 0 {
			for i := failedIdx + 1; i < len(results); i++ {
				if results[i].Reason == "" {
					results[i].Status = bootstrapPhaseSkipped
					results[i].Reason = fmt.Sprintf("aborted after %s failed", results[failedIdx].ID)
				}
			}
		}
		summary := bootstrapSummaryOutput{
			Profile:  profile,
			Preset:   presetID,
			Theme:    activeTheme,
			DryRun:   ctx.DryRun,
			Phases:   results,
			Warnings: allWarnings,
			Skipped:  consentTotal.ConsentSkipped,
		}
		if runErr == nil {
			runErr = consentSkippedError(consentTotal)
		}
		if runErr != nil {
			summary.Error = runErr.Error()
		}
		if ctx.JSON {
			if werr := writeJSON(outW, summary); werr != nil {
				return werr
			}
			return runErr
		}
		printBootstrapSummary(outW, roles, summary)
		return runErr
	}

	if !ctx.JSON {
		if ctx.DryRun {
			log.Info("Bootstrap plan", "profile", profile, "preset", presetID, "mode", "dry-run")
		} else {
			log.Info("Bootstrap starting", "profile", profile, "preset", presetID)
		}
	}

	var (
		pkgPlan *engine.Plan
		err     error
	)
	if !ctx.JSON {
		log.Section("1. Packages and OS hooks")
	}
	if opts.PlanPath != "" {
		pkgPlan, err = engine.LoadPlan(opts.PlanPath)
	} else if !ctx.JSON {
		err = ui.Spin(outW, roles, fmt.Sprintf("Planning packages (%s)", presetID), func() error {
			var perr error
			pkgPlan, perr = deps.planPackages(ctx, presetID)
			return perr
		})
	} else {
		pkgPlan, err = deps.planPackages(ctx, presetID)
	}
	if err != nil {
		results[0].Status = bootstrapPhaseFailed
		results[0].Reason = err.Error()
		return finish(0, err)
	}
	if opts.SavePlanPath != "" {
		if err := pkgPlan.Save(opts.SavePlanPath); err != nil {
			return err
		}
		if ctx.JSON {
			return writeJSON(outW, pkgPlan)
		}
		fmt.Fprintf(outW, "Saved bootstrap plan (%d actions) to %s\n", len(pkgPlan.Actions), opts.SavePlanPath)
		return nil
	}
	if err := promptBootstrapAllows(cmd, ctx, pkgPlan, yesFlag, deps); err != nil {
		if IsCancelledError(err) {
			return err
		}
		results[0].Status = bootstrapPhaseFailed
		results[0].Reason = err.Error()
		return finish(0, err)
	}
	pkgRes, err := deps.execPackages(ctx, pkgPlan)
	if err != nil {
		results[0].Status = bootstrapPhaseFailed
		results[0].Reason = err.Error()
		return finish(0, err)
	}
	if !ctx.JSON {
		printPlanSummary(outW, roles, pkgPlan, ctx.DryRun)
		if !ctx.DryRun {
			printApplyResult(outW, roles, pkgRes, false)
		}
	}
	if !ctx.DryRun {
		results[0].Status = bootstrapPhaseDone
		results[0].Detail = fmt.Sprintf("applied=%d skipped=%d", pkgRes.Applied, pkgRes.Skipped)
		recordPhaseConsent(0, pkgRes)
	} else {
		results[0].Status = bootstrapPhaseDone
		results[0].Detail = fmt.Sprintf("dry-run: %d action(s)", len(pkgPlan.Actions))
	}

	if !ctx.JSON {
		log.Section("2. Links and templates")
	}
	applyPlan, err := deps.planApply(ctx, engine.PlanApplyOptions{AllPostSyncHooks: true})
	if err != nil {
		results[1].Status = bootstrapPhaseFailed
		results[1].Reason = err.Error()
		return finish(1, err)
	}
	if err := promptBootstrapAllows(cmd, ctx, applyPlan, yesFlag, deps); err != nil {
		if IsCancelledError(err) {
			return err
		}
		results[1].Status = bootstrapPhaseFailed
		results[1].Reason = err.Error()
		return finish(1, err)
	}
	applyRes, err := deps.execApply(ctx, applyPlan)
	if err != nil {
		results[1].Status = bootstrapPhaseFailed
		results[1].Reason = err.Error()
		return finish(1, err)
	}
	if !ctx.JSON {
		printPlanSummary(outW, roles, applyPlan, ctx.DryRun)
		if !ctx.DryRun {
			printApplyResult(outW, roles, applyRes, false)
		}
	}
	if !ctx.DryRun {
		results[1].Status = bootstrapPhaseDone
		results[1].Detail = fmt.Sprintf("applied=%d skipped=%d", applyRes.Applied, applyRes.Skipped)
		recordPhaseConsent(1, applyRes)
	} else {
		results[1].Status = bootstrapPhaseDone
		results[1].Detail = fmt.Sprintf("dry-run: %d action(s)", len(applyPlan.Actions))
	}

	if !ctx.JSON {
		log.Section("3. Trust repo mise config")
	}
	if ctx.DryRun {
		if !ctx.JSON {
			log.PlanItem("mise", fmt.Sprintf("mise trust in %s", ctx.CollapseHome(ctx.RepoRoot)))
		}
		results[2].Status = bootstrapPhaseDone
		results[2].Detail = "dry-run"
	} else {
		if err := deps.trustMise(ctx, ""); err != nil {
			results[2].Status = bootstrapPhaseFailed
			results[2].Reason = err.Error()
			return finish(2, err)
		}
		if !ctx.JSON {
			log.Done("Trusted repo mise config", "repo", ctx.CollapseHome(ctx.RepoRoot))
		}
		results[2].Status = bootstrapPhaseDone
	}

	if !isDesktop {
		results[3].Status = bootstrapPhaseSkipped
		results[3].Reason = "server profile"
		results[4].Status = bootstrapPhaseSkipped
		results[4].Reason = "server profile"
		results[5].Status = bootstrapPhaseSkipped
		results[5].Reason = "server profile"
	} else {
		if !ctx.JSON {
			log.Section("4. 1Password secrets")
		}
		if hasBootstrapZones(ctx) {
			results[3].Status = bootstrapPhaseSkipped
			results[3].Reason = "WS_ZONES already configured"
			if !ctx.JSON {
				log.Skip("1Password secrets pull skipped", "reason", results[3].Reason)
			}
		} else if ctx.DryRun {
			if !ctx.JSON {
				log.PlanItem("1password", "check 1Password gate, run ws vault secrets pull, mise trust, and reload WS_ZONES")
			}
			results[3].Status = bootstrapPhaseDone
			results[3].Detail = "dry-run"
		} else {
			proceed := false
			switch {
			case yesFlag:
				if deps.opSignedIn(cmdContext(cmd)) {
					proceed = true
				} else {
					addWarn(3, "1Password not signed in (op whoami failed) — skipping secrets pull, identity zones, and restores")
					results[3].Status = bootstrapPhaseSkipped
					results[3].Reason = "1Password not signed in"
				}
			case deps.isTTY(cmd, ctx):
				if !ctx.JSON {
					log.Info("1Password: sign in and enable Settings -> Developer -> CLI integration")
				}
				ready, cerr := deps.confirmOnePassword(cmd, ctx)
				if cerr != nil {
					if IsCancelledError(cerr) {
						return cerr
					}
					addWarn(3, fmt.Sprintf("1Password prompt failed: %v", cerr))
					results[3].Status = bootstrapPhaseSkipped
					results[3].Reason = cerr.Error()
				} else if ready {
					proceed = true
				} else {
					addWarn(3, "1Password not ready — skipping secrets pull, identity zones, and restores")
					results[3].Status = bootstrapPhaseSkipped
					results[3].Reason = "1Password not ready"
				}
			default:
				addWarn(3, "non-interactive without --yes and WS_ZONES unset — skipping 1Password gate, identity zones, and restores")
				results[3].Status = bootstrapPhaseSkipped
				results[3].Reason = "non-interactive without --yes"
			}

			if proceed {
				miseLocal := filepath.Join(ctx.RepoRoot, "mise.local.toml")
				canPull := true
				if !ctx.Allow.Replace && localPathExists(miseLocal) {
					if !yesFlag && deps.isTTY(cmd, ctx) {
						ok, cerr := deps.confirmAllow(cmd, ctx, "replace", []string{ctx.CollapseHome(miseLocal)})
						if cerr != nil {
							if IsCancelledError(cerr) {
								return cerr
							}
							canPull = false
							addWarn(3, fmt.Sprintf("replace prompt failed: %v", cerr))
							results[3].Status = bootstrapPhaseSkipped
							results[3].Reason = cerr.Error()
						} else if ok {
							grantAllowScope(&ctx.Allow, "replace")
						} else {
							canPull = false
							addWarn(3, "skipped vault secrets pull: mise.local.toml exists (requires --allow replace)")
							results[3].Status = bootstrapPhaseSkipped
							results[3].Reason = "mise.local.toml exists (requires --allow replace)"
						}
					} else {
						canPull = false
						addWarn(3, "skipped vault secrets pull: mise.local.toml exists (requires --allow replace)")
						results[3].Status = bootstrapPhaseSkipped
						results[3].Reason = "mise.local.toml exists (requires --allow replace)"
					}
				}
				if canPull {
					if err := deps.vaultPull(ctx, "secrets", nil, shell.VaultOptions{WarnOut: errW}); err != nil {
						addWarn(3, fmt.Sprintf("vault secrets pull failed: %v", err))
						results[3].Status = bootstrapPhaseFailed
						results[3].Reason = err.Error()
					} else {
						trustPath := ""
						if localPathExists(miseLocal) {
							trustPath = miseLocal
						}
						if err := deps.trustMise(ctx, trustPath); err != nil {
							addWarn(3, fmt.Sprintf("mise trust after secrets pull failed: %v", err))
						}
						if err := ctx.ReloadLocalEnv(); err != nil {
							addWarn(3, fmt.Sprintf("reload mise.local.toml failed: %v", err))
						}
						if !ctx.JSON {
							log.Done("Pulled 1Password secrets and reloaded zone environment")
						}
						results[3].Status = bootstrapPhaseDone
					}
				}
			}
		}

		if !ctx.JSON {
			log.Section("5. Identity zones")
		}
		if ctx.DryRun {
			if hasBootstrapZones(ctx) {
				za, zerr := deps.applyZones(ctx)
				if zerr != nil {
					addWarn(4, fmt.Sprintf("zone dry-run incomplete: %v", zerr))
					results[4].Status = bootstrapPhaseFailed
					results[4].Reason = zerr.Error()
				} else {
					if !ctx.JSON {
						for _, a := range za {
							log.PlanItem("zone", a)
						}
					}
					results[4].Status = bootstrapPhaseDone
					results[4].Detail = fmt.Sprintf("dry-run: %d action(s)", len(za))
				}
			} else {
				if !ctx.JSON {
					log.PlanItem("zone", "apply identity zones once WS_ZONES is loaded from mise.local.toml")
				}
				results[4].Status = bootstrapPhaseDone
				results[4].Detail = "dry-run: pending WS_ZONES"
			}
		} else if !hasBootstrapZones(ctx) {
			results[4].Status = bootstrapPhaseSkipped
			results[4].Reason = "WS_ZONES not configured"
			if !ctx.JSON {
				log.Skip("Identity zones skipped", "reason", results[4].Reason)
			}
		} else {
			za, zerr := deps.applyZones(ctx)
			if zerr != nil {
				addWarn(4, fmt.Sprintf("identity incomplete — re-run 'ws zone apply': %v", zerr))
				results[4].Status = bootstrapPhaseFailed
				results[4].Reason = zerr.Error()
			} else {
				if !ctx.JSON {
					for _, a := range za {
						log.ChangeItem(a)
					}
					if len(za) == 0 {
						log.Done("Identity zones up to date")
					} else {
						log.Done("Identity zones applied", "changes", len(za))
					}
				}
				results[4].Status = bootstrapPhaseDone
				results[4].Detail = fmt.Sprintf("%d change(s)", len(za))
			}
		}

		if !ctx.JSON {
			log.Section("6. Vault restores")
		}
		if ctx.DryRun {
			if !ctx.JSON {
				log.PlanItem("vault", "vault ssh pull")
				fileTargets := configuredVaultFileTargets(ctx)
				if len(fileTargets) > 0 {
					log.PlanItem("vault", fmt.Sprintf("vault files pull --all (%d configured path(s))", len(fileTargets)))
				} else {
					log.PlanItem("vault", "vault files pull --all")
				}
			}
			results[5].Status = bootstrapPhaseDone
			results[5].Detail = "dry-run"
		} else if !hasBootstrapZones(ctx) {
			results[5].Status = bootstrapPhaseSkipped
			results[5].Reason = "WS_ZONES not configured"
			if !ctx.JSON {
				log.Skip("Vault restores skipped", "reason", results[5].Reason)
			}
		} else if !deps.opSignedIn(cmdContext(cmd)) {
			addWarn(5, "1Password not signed in — skipping ssh and files restores")
			results[5].Status = bootstrapPhaseSkipped
			results[5].Reason = "1Password not signed in"
		} else {
			fileTargets := configuredVaultFileTargets(ctx)
			sshDir := filepath.Join(ctx.HomeDir, ".ssh")
			if !ctx.Allow.Replace {
				var existing []string
				if localPathExists(sshDir) {
					existing = append(existing, "~/.ssh")
				}
				for _, rel := range fileTargets {
					if localPathExists(ctx.ExpandHome(rel)) {
						existing = append(existing, rel)
					}
				}
				if len(existing) > 0 && !yesFlag && deps.isTTY(cmd, ctx) {
					ok, cerr := deps.confirmAllow(cmd, ctx, "replace", existing)
					if cerr != nil {
						if IsCancelledError(cerr) {
							return cerr
						}
						addWarn(5, fmt.Sprintf("replace prompt failed: %v", cerr))
					} else if ok {
						grantAllowScope(&ctx.Allow, "replace")
					}
				}
			}

			var restoreErrs []string
			restoredAny := false
			skippedReplace := false

			if !ctx.Allow.Replace && localPathExists(sshDir) {
				addWarn(5, "skipped vault ssh pull: ~/.ssh exists (requires --allow replace)")
				skippedReplace = true
			} else if err := deps.vaultPull(ctx, "ssh", nil, shell.VaultOptions{WarnOut: errW, SkipMissing: true}); err != nil {
				addWarn(5, fmt.Sprintf("vault ssh pull skipped: %v", err))
				restoreErrs = append(restoreErrs, fmt.Sprintf("ssh: %v", err))
			} else {
				restoredAny = true
			}

			if len(fileTargets) == 0 {
				addWarn(5, "WS_OP_FILES is empty — skipping vault files pull")
			} else if ctx.Allow.Replace {
				if err := deps.vaultPull(ctx, "files", nil, shell.VaultOptions{All: true, WarnOut: errW, SkipMissing: true}); err != nil {
					addWarn(5, fmt.Sprintf("vault files pull skipped: %v", err))
					restoreErrs = append(restoreErrs, fmt.Sprintf("files: %v", err))
				} else {
					restoredAny = true
				}
			} else {
				var safeTargets []string
				for _, rel := range fileTargets {
					if localPathExists(ctx.ExpandHome(rel)) {
						addWarn(5, fmt.Sprintf("skipped %s: exists (requires --allow replace)", rel))
						skippedReplace = true
					} else {
						safeTargets = append(safeTargets, rel)
					}
				}
				if len(safeTargets) > 0 {
					if len(safeTargets) == len(fileTargets) {
						if err := deps.vaultPull(ctx, "files", nil, shell.VaultOptions{All: true, WarnOut: errW, SkipMissing: true}); err != nil {
							addWarn(5, fmt.Sprintf("vault files pull skipped: %v", err))
							restoreErrs = append(restoreErrs, fmt.Sprintf("files: %v", err))
						} else {
							restoredAny = true
						}
					} else {
						if err := deps.vaultPull(ctx, "files", safeTargets, shell.VaultOptions{WarnOut: errW, SkipMissing: true}); err != nil {
							addWarn(5, fmt.Sprintf("vault files pull skipped: %v", err))
							restoreErrs = append(restoreErrs, fmt.Sprintf("files: %v", err))
						} else {
							restoredAny = true
						}
					}
				}
			}

			switch {
			case len(restoreErrs) > 0:
				results[5].Status = bootstrapPhaseFailed
				results[5].Reason = strings.Join(restoreErrs, "; ")
			case !restoredAny && skippedReplace:
				results[5].Status = bootstrapPhaseSkipped
				results[5].Reason = "existing local files require --allow replace"
			default:
				results[5].Status = bootstrapPhaseDone
				if !ctx.JSON {
					log.Done("Vault restores complete")
				}
			}
		}
	}

	if !ctx.JSON {
		log.Section("7. Theme")
	}
	activeTheme = resolveActiveThemeID(ctx)
	if _, err := config.LoadPalette(ctx.RepoRoot, activeTheme); err != nil {
		results[6].Status = bootstrapPhaseFailed
		results[6].Reason = err.Error()
		return finish(6, err)
	}
	themePlan, err := deps.planApply(ctx, engine.PlanApplyOptions{ThemeOverride: activeTheme})
	if err != nil {
		results[6].Status = bootstrapPhaseFailed
		results[6].Reason = err.Error()
		return finish(6, err)
	}
	if err := promptBootstrapAllows(cmd, ctx, themePlan, yesFlag, deps); err != nil {
		if IsCancelledError(err) {
			return err
		}
		results[6].Status = bootstrapPhaseFailed
		results[6].Reason = err.Error()
		return finish(6, err)
	}
	themeRes, err := deps.execApply(ctx, themePlan)
	if err != nil {
		results[6].Status = bootstrapPhaseFailed
		results[6].Reason = err.Error()
		return finish(6, err)
	}
	if !ctx.JSON {
		if ctx.DryRun {
			log.PlanItem("theme", fmt.Sprintf("render theme %s (%d action(s))", activeTheme, len(themePlan.Actions)))
		} else {
			log.Done("Theme rendered", "theme", activeTheme, "applied", themeRes.Applied)
		}
	}
	results[6].Status = bootstrapPhaseDone
	if ctx.DryRun {
		results[6].Detail = "dry-run: " + activeTheme
	} else {
		results[6].Detail = activeTheme
		recordPhaseConsent(6, themeRes)
	}

	return finish(-1, nil)
}

func hasBootstrapZones(ctx *app.Context) bool {
	if ctx == nil {
		return false
	}
	return strings.TrimSpace(ctx.LookupEnv("WS_ZONES")) != ""
}

func configuredVaultFileTargets(ctx *app.Context) []string {
	if ctx == nil {
		return nil
	}
	var targets []string
	for _, row := range app.ParsePipeLines(ctx.LookupEnv("WS_OP_FILES")) {
		if len(row) > 0 && row[0] != "" {
			targets = append(targets, row[0])
		}
	}
	return targets
}

func localPathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

func printBootstrapSummary(w io.Writer, roles ui.Roles, summary bootstrapSummaryOutput) {
	log := ui.NewLogger(w, roles)
	log.Section("Bootstrap summary")
	for _, ph := range summary.Phases {
		switch ph.Status {
		case bootstrapPhaseDone:
			log.StepDone(ph.Name, 0, ph.Detail)
		case bootstrapPhaseSkipped:
			detail := ph.Reason
			if detail == "" {
				detail = "skipped"
			}
			log.StepSkip(ph.Name, detail)
		case bootstrapPhasePartial:
			log.StepWarn(ph.Name, 0, ph.Reason)
		case bootstrapPhaseFailed:
			detail := ph.Reason
			if detail == "" {
				detail = "failed"
			}
			if ph.Required {
				log.StepFail(ph.Name, 0, detail)
			} else {
				log.StepWarn(ph.Name, 0, detail)
			}
		}
	}
	log.BlankLine()
	if summary.Error != "" && summary.Skipped > 0 && !bootstrapPhasesFailed(summary.Phases) {
		log.Warn("Bootstrap finished with skipped steps", "profile", summary.Profile, "preset", summary.Preset, "skipped", summary.Skipped)
		log.Warn(summary.Error)
		return
	}
	if summary.Error != "" {
		log.Error("Bootstrap failed", "profile", summary.Profile, "preset", summary.Preset)
		return
	}
	if summary.DryRun {
		log.Done("Bootstrap dry-run complete", "profile", summary.Profile, "preset", summary.Preset)
		return
	}
	if summary.Profile != "server" {
		log.Info("Remaining manual steps: see the runbook note in 1Password")
	}
	log.Done("Bootstrap complete", "profile", summary.Profile, "preset", summary.Preset)
}

func bootstrapPhasesFailed(phases []bootstrapPhaseResult) bool {
	for _, ph := range phases {
		if ph.Status == bootstrapPhaseFailed && ph.Required {
			return true
		}
	}
	return false
}
