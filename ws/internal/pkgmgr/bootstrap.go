package pkgmgr

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

const (
	paruAURURL     = "https://aur.archlinux.org/paru-bin.git"
	flathubRepoURL = "https://dl.flathub.org/repo/flathub.flatpakrepo"

	HookDeclinedExitCode     = 75
	HookUserDeclinedExitCode = 76
)

var (
	currentEUID      = os.Geteuid
	flatpakGTKThemes = []string{
		"org.gtk.Gtk3theme.adw-gtk3",
		"org.gtk.Gtk3theme.adw-gtk3-dark",
	}
)

func PlanBootstrap(ctx *app.Context, presetID string) (*engine.Plan, error) {
	return PlanBootstrapWithRunner(ctx, presetID, ExecRunner{Probe: true})
}

func PlanBootstrapWithRunner(ctx *app.Context, presetID string, runner CommandRunner) (*engine.Plan, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	if presetID == "" {
		presetID = ctx.EffectivePreset()
	}

	presets, err := config.LoadPresets(ctx.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("load presets: %w", err)
	}
	resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, presetID)
	if err != nil {
		return nil, err
	}
	catalog, err := config.LoadTools(ctx.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("load tools: %w", err)
	}

	tools := config.ResolveTools(resolved, catalog, ctx.Host)
	tools = RefineToolsForPreset(ctx, resolved, tools, runner)
	providers := AllProviders(ctx, runner, tools)

	plan := &engine.Plan{
		Verb:      "bootstrap",
		Host:      engine.FormatPlanHost(ctx),
		Title:     fmt.Sprintf("Bootstrap preset %s", resolved.ID),
		Preset:    resolved.ID,
		CreatedAt: time.Now().UTC(),
	}

	if resolved.Secrets == "none" {
		plan.Skipped = append(plan.Skipped, "1password: skipped (preset secrets = none)")
	}

	archHost := isArchHost(ctx)
	serverMode := isServerPreset(ctx, resolved)
	bootstrapParu := archHost && !serverMode && !hasBin(runner, "paru") && hasBin(runner, "pacman")

	if bootstrapParu {
		plan.Actions = append(plan.Actions, paruBootstrapActions()...)
	}

	if len(resolved.ConflictPackages) > 0 {
		var archProv Provider
		if p, ok := providers["pacman"]; ok && p.Available() {
			archProv = p
		} else if p, ok := providers["paru"]; ok && p.Available() {
			archProv = p
		}
		if archProv != nil {
			for _, pkgName := range resolved.ConflictPackages {
				probe := config.ResolvedTool{
					ID:   pkgName,
					Spec: config.InstallSpec{Via: archProv.Name(), Name: pkgName},
				}
				if archProv.IsInstalled(probe) {
					plan.Actions = append(plan.Actions, presetConflictRemoveAction(archProv.Name(), pkgName))
				}
			}
		}
	}

	postHooks := slices.Clone(resolved.OSHooks)

	aptNeedsUpdate := false
	brewNeedsUpdate := false
	pacmanNeedsUpgrade := false
	var installActions []engine.Action

	for _, tool := range tools {
		if resolved.Secrets == "none" && isSecretTool(tool.ID) {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: skipped (preset secrets = none)", tool.ID))
			continue
		}

		for _, conflict := range tool.Conflicts {
			via, pkgName, ok := strings.Cut(conflict, ":")
			if !ok {
				continue
			}
			via = strings.TrimSpace(via)
			pkgName = strings.TrimSpace(pkgName)
			cProv, exists := providers[via]
			if !exists || !cProv.Available() {
				continue
			}
			probe := config.ResolvedTool{
				ID:   pkgName,
				Bin:  tool.Bin,
				Spec: config.InstallSpec{Via: via, Name: pkgName},
			}
			if cProv.IsInstalled(probe) {
				installActions = append(installActions, conflictRemoveAction(via, pkgName, tool.ID))
			}
		}

		prov, ok := providers[tool.Spec.Via]
		if !ok {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("no provider %q for tool %s", tool.Spec.Via, tool.ID))
			continue
		}

		if tool.Spec.Via == "mise" {
			ref := tool.Spec.Ref
			if ref == "" {
				ref = pkgNameOf(tool)
			}
			if err := ValidateMiseRef(ref); err != nil {
				return nil, fmt.Errorf("tool %s: %w", tool.ID, err)
			}
		}

		alreadyInstalled := prov.Available() && prov.IsInstalled(tool)
		if alreadyInstalled {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("%s: already installed (%s)", tool.ID, prov.Name()))
		} else {
			installProv := prov
			if bootstrapParu && tool.Spec.Via == "pacman" {
				if p, hasParuProv := providers["paru"]; hasParuProv {
					installProv = p
				}
			}
			act := installProv.InstallAction(tool)
			if tool.Spec.Via == "script" && tool.Spec.TOFU && strings.TrimSpace(tool.Spec.SHA256) == "" && !ctx.Allow.Script {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("unpinned TOFU script for %s requires --allow script", tool.ID))
			}
			if tool.Spec.Via == "apt" {
				aptNeedsUpdate = true
			}
			if tool.Spec.Via == "brew" || tool.Spec.Via == "cask" {
				brewNeedsUpdate = true
			}
			if serverMode && (tool.Spec.Via == "apt" || tool.Spec.Via == "dnf" || tool.Spec.Via == "pacman") {
				if act.Metadata == nil {
					act.Metadata = map[string]string{}
				}
				act.Metadata["skip_if_missing"] = "true"
				if tool.Spec.Via == "pacman" {
					pacmanNeedsUpgrade = true
				}
			}
			installActions = append(installActions, act)
		}

		if tool.Spec.Via == "apt" && tool.Spec.Shim != "" {
			if shimAct, needed, serr := planShimActionIfNeeded(ctx, tool, runner); serr == nil {
				if needed {
					installActions = append(installActions, shimAct)
				}
			} else {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("tool %s: %v", tool.ID, serr))
			}
		}
	}

	switch {
	case archHost && !serverMode:
		archRemoves, archInstalls, otherInstalls := partitionArchActions(installActions)
		plan.Actions = append(plan.Actions, archRemoves...)
		plan.Actions = append(plan.Actions, batchArchInstallActions(archInstalls, plan.Actions, "-Syu")...)
		plan.Actions = append(plan.Actions, otherInstalls...)
		if slices.Contains(resolved.PackageNames, "flatpak") {
			plan.Actions = append(plan.Actions, planFlatpakThemeActions(runner, &plan.Skipped)...)
		}
	case archHost && serverMode:
		if pacmanNeedsUpgrade {
			plan.Actions = append(plan.Actions, pacmanServerUpgradeAction())
		}
		plan.Actions = append(plan.Actions, installActions...)
	case ctx.Host.OS == "darwin" || brewNeedsUpdate:
		brewRemoves, brewInstalls, caskInstalls, otherInstalls := partitionBrewActions(installActions)
		plan.Actions = append(plan.Actions, brewRemoves...)
		if brewNeedsUpdate {
			plan.Actions = append(plan.Actions, brewUpdateAction())
		}
		tapped := map[string]bool{}
		trusted := map[string]bool{}
		plan.Actions = append(plan.Actions, planBatchedBrewKind(brewInstalls, plan.Actions, "brew", tapped, trusted)...)
		plan.Actions = append(plan.Actions, planBatchedBrewKind(caskInstalls, plan.Actions, "cask", tapped, trusted)...)
		plan.Actions = append(plan.Actions, otherInstalls...)
	default:
		if aptNeedsUpdate {
			plan.Actions = append(plan.Actions, engine.Action{
				ID:            "apt:update",
				Kind:          engine.ActionOSExec,
				Target:        "apt-get",
				Summary:       "Refresh apt package index",
				Risk:          engine.RiskSafe,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"sudo", "-E", "apt-get", "update", "-qq"},
				Metadata: map[string]string{
					"provider":            "apt",
					"env.DEBIAN_FRONTEND": "noninteractive",
				},
			})
		}
		plan.Actions = append(plan.Actions, installActions...)
	}

	for _, hook := range postHooks {
		plan.Actions = append(plan.Actions, osHookAction(ctx, resolved.ID, hook))
	}

	return plan, nil
}

func ExecBootstrap(ctx *app.Context, plan *engine.Plan, runner CommandRunner) (*engine.ApplyResult, error) {
	return ExecBootstrapWithHTTP(ctx, plan, runner, nil)
}

func ExecBootstrapWithHTTP(ctx *app.Context, plan *engine.Plan, runner CommandRunner, client *http.Client) (*engine.ApplyResult, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if plan == nil {
		return nil, errors.New("nil plan")
	}
	if plan.Verb != "" && plan.Verb != "bootstrap" && plan.Verb != "update" && plan.Verb != "pkg" {
		return nil, fmt.Errorf("refusing plan with verb %q in bootstrap", plan.Verb)
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	res := &engine.ApplyResult{
		Warnings: slices.Clone(plan.Warnings),
	}

	for _, act := range plan.Actions {
		if err := validateBootstrapAction(ctx, act); err != nil {
			return res, err
		}
	}

	if ctx.DryRun {
		res.Skipped = len(plan.Actions)
		return res, nil
	}

	priorToolSHA := map[string]string{}
	managedTools := map[string]app.ManagedToolRecord{}
	if ctx.WSBaseDir() != "" {
		initial, lerr := app.LoadLedger(ctx.WSBaseDir())
		if lerr != nil {
			return res, fmt.Errorf("load ledger: %w", lerr)
		}
		for id, rec := range initial.ManagedTools {
			priorToolSHA[id] = strings.TrimSpace(rec.SHA256)
		}
	}

	saved := false
	saveLedger := func() error {
		if saved || ctx.WSBaseDir() == "" || (res.Applied == 0 && len(managedTools) == 0) {
			return nil
		}
		err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
			cur, err := app.LoadLedger(ctx.WSBaseDir())
			if err != nil {
				return fmt.Errorf("load ledger: %w", err)
			}
			for id, rec := range managedTools {
				cur.ManagedTools[id] = rec
			}
			if (plan.Verb == "" || plan.Verb == "bootstrap") && plan.Preset != "" {
				cur.ActivePreset = plan.Preset
			}
			journalCmd := plan.Verb
			if journalCmd == "" {
				journalCmd = "bootstrap"
			}
			cur.RunJournal = append(cur.RunJournal, app.JournalEntry{
				Timestamp:    time.Now().UTC(),
				Command:      journalCmd,
				Preset:       plan.Preset,
				Summary:      plan.Title,
				ActionsCount: res.Applied,
				Warnings:     res.Warnings,
			})
			if err := app.SaveLedger(ctx.WSBaseDir(), cur); err != nil {
				return fmt.Errorf("save ledger: %w", err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		saved = true
		return nil
	}
	defer func() {
		_ = saveLedger()
	}()

	skippedConflictTools := map[string]bool{}
	skippedMissingTools := map[string]bool{}
	skippedConsentTools := map[string][]string{}
	for _, act := range plan.Actions {
		if missing := missingBootstrapAllows(ctx.Allow, act); len(missing) > 0 {
			res.Skipped++
			recordConsentSkip(res, missing)
			if act.Kind == engine.ActionRemove {
				if forTool := act.Metadata["for_tool"]; forTool != "" {
					skippedConflictTools[forTool] = true
				}
			}
			if toolID := act.Metadata["tool"]; toolID != "" {
				if _, seen := skippedConsentTools[toolID]; !seen {
					skippedConsentTools[toolID] = missing
				}
			}
			if act.Metadata["provider"] == "script" && act.Metadata["tofu"] == "true" && act.Metadata["sha256"] == "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped unpinned TOFU script %s (requires --allow script)", act.Target))
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: requires --allow %s", act.ID, strings.Join(missing, ",")))
			}
			continue
		}

		if act.Kind == engine.ActionInstall {
			if toolID := act.Metadata["tool"]; toolID != "" && skippedConflictTools[toolID] {
				res.Skipped++
				recordConsentSkip(res, []string{"remove"})
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: conflicting package removal for %s was skipped", act.ID, toolID))
				if _, seen := skippedConsentTools[toolID]; !seen {
					skippedConsentTools[toolID] = []string{"remove"}
				}
				continue
			}
		}

		if toolID := act.Metadata["tool"]; toolID != "" {
			if scopes, ok := skippedConsentTools[toolID]; ok {
				res.Skipped++
				recordConsentSkip(res, scopes)
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: %s was not installed (requires --allow %s)", act.ID, toolID, strings.Join(scopes, ",")))
				continue
			}
		}

		if act.Metadata["provider"] == "paru-bootstrap" {
			if err := runParuBootstrap(ctx, act, runner); err != nil {
				return res, err
			}
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			continue
		}

		if act.Metadata["provider"] == "script" {
			tofu, _ := strconv.ParseBool(act.Metadata["tofu"])
			spec := config.InstallSpec{
				Via:    "script",
				URL:    act.Metadata["url"],
				SHA256: act.Metadata["sha256"],
				TOFU:   tofu,
			}
			toolID := act.Metadata["tool"]
			if toolID == "" {
				toolID = act.Target
			}
			priorSHA256 := strings.TrimSpace(managedTools[toolID].SHA256)
			if priorSHA256 == "" {
				priorSHA256 = priorToolSHA[toolID]
			}
			sum, skipped, warn, err := verifyAndRunScriptWithPrior(ctx, spec, toolID, priorSHA256, client, runner)
			if warn != "" {
				res.Warnings = append(res.Warnings, warn)
			}
			if err != nil {
				return res, err
			}
			if skipped {
				res.Skipped++
				recordConsentSkip(res, []string{"script"})
				continue
			}
			managedTools[toolID] = app.ManagedToolRecord{
				Provider:    "script",
				PackageName: spec.URL,
				SHA256:      sum,
				Updates:     act.Metadata["updates"],
				InstalledAt: time.Now().UTC(),
			}
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			continue
		}

		if act.Kind == engine.ActionSymlink && act.Metadata["shim"] != "" {
			if toolID := act.Metadata["tool"]; toolID != "" && skippedMissingTools[toolID] {
				res.Skipped++
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: package %s was not available", act.ID, toolID))
				continue
			}
			if _, _, err := EnsureShim(ctx, act.Metadata["shim"], runner); err != nil {
				return res, fmt.Errorf("%s: %w", act.ID, err)
			}
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			continue
		}

		if act.Metadata["provider"] == "mise" {
			if ref := act.Metadata["ref"]; ref != "" {
				if err := ValidateMiseRef(ref); err != nil {
					return res, fmt.Errorf("%s: %w", act.ID, err)
				}
			}
		}

		env := extractEnv(act.Metadata)
		if act.Kind == engine.ActionOSExec {
			env = osHookEnv(ctx, env)
		}
		workDir := ""
		if act.Kind == engine.ActionOSExec || act.Metadata["provider"] == "mise" || (len(act.Command) > 0 && act.Command[0] == "mise") {
			workDir = ctx.RepoRoot
		}
		if _, stderr, err := runner.Run(act.Command, env, workDir, act.NeedsTTY && ctx.ChildTTY()); err != nil {
			if hook := act.Metadata["hook"]; act.Kind == engine.ActionOSExec && hook != "" {
				switch hookExitCode(err) {
				case HookDeclinedExitCode:
					res.Skipped++
					recordHookDecline(res)
					res.Warnings = append(res.Warnings, fmt.Sprintf("declined %s (rerun with --yes to accept)", hook))
					continue
				case HookUserDeclinedExitCode:
					res.Skipped++
					res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s (declined at prompt)", hook))
					continue
				}
			}
			msg := strings.TrimSpace(stderr)
			if act.Metadata["skip_if_missing"] == "true" {
				res.Skipped++
				if toolID := act.Metadata["tool"]; toolID != "" {
					skippedMissingTools[toolID] = true
				}
				prov := act.Metadata["provider"]
				if msg != "" {
					res.Warnings = append(res.Warnings, fmt.Sprintf("not available via %s, skipping %s: %v: %s", prov, act.Target, err, msg))
				} else {
					res.Warnings = append(res.Warnings, fmt.Sprintf("not available via %s, skipping %s: %v", prov, act.Target, err))
				}
				continue
			}
			if act.Metadata["continue_on_error"] == "true" {
				res.Skipped++
				if msg != "" {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s warning: %v: %s", act.ID, err, msg))
				} else {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s warning: %v", act.ID, err))
				}
				continue
			}
			if msg != "" {
				return res, fmt.Errorf("%s failed: %w: %s", act.ID, err, msg)
			}
			return res, fmt.Errorf("%s failed: %w", act.ID, err)
		}

		res.Applied++
		res.Changed = append(res.Changed, act.ID)

		if act.Kind == engine.ActionInstall {
			if toolID := act.Metadata["tool"]; toolID != "" {
				managedTools[toolID] = app.ManagedToolRecord{
					Provider:    act.Metadata["provider"],
					PackageName: act.Metadata["pkg"],
					Updates:     act.Metadata["updates"],
					InstalledAt: time.Now().UTC(),
				}
			} else if rawPkgs := strings.TrimSpace(act.Metadata["pkgs"]); rawPkgs != "" {
				prov := act.Metadata["provider"]
				now := time.Now().UTC()
				for _, p := range strings.Split(rawPkgs, ",") {
					name := strings.TrimSpace(p)
					if name != "" {
						managedTools[name] = app.ManagedToolRecord{
							Provider:    prov,
							PackageName: name,
							Updates:     "manager",
							InstalledAt: now,
						}
					}
				}
			}
		}
	}

	if err := saveLedger(); err != nil {
		return res, err
	}

	return res, nil
}

func RefineToolsForPreset(ctx *app.Context, preset *config.ResolvedPreset, tools []config.ResolvedTool, runner CommandRunner) []config.ResolvedTool {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	caskSet := map[string]bool{}
	if ctx.Host.OS == "darwin" && slices.Contains(preset.PackageFiles, "pkg/mac-cask.txt") {
		if items, err := config.ParsePackageList(filepath.Join(ctx.RepoRoot, "pkg/mac-cask.txt")); err == nil {
			for _, item := range items {
				caskSet[item] = true
			}
		}
	}

	archHost := isArchHost(ctx)
	serverMode := isServerPreset(ctx, preset)
	useParu := archHost && !serverMode && hasBin(runner, "paru")

	out := make([]config.ResolvedTool, len(tools))
	for i, t := range tools {
		if t.Source == "pkg-list" && caskSet[t.ID] {
			t.Spec.Via = "cask"
			t.Spec.Name = t.ID
		} else if archHost && serverMode && t.Spec.Via == "paru" {
			t.Spec.Via = "pacman"
		} else if useParu && t.Spec.Via == "pacman" {
			t.Spec.Via = "paru"
		}
		out[i] = t
	}
	return out
}

func isServerPreset(ctx *app.Context, resolved *config.ResolvedPreset) bool {
	if resolved == nil {
		return false
	}
	if slices.Contains(resolved.Links, "links/server.links") {
		return true
	}
	if resolved.ID == "server" || strings.HasSuffix(resolved.ID, "-server") || strings.HasSuffix(resolved.ID, "-runner") {
		return true
	}
	if ctx != nil && ctx.Host.OS == "linux" && ctx.Host.Headless && !slices.Contains(resolved.PackageFiles, "pkg/arch.txt") {
		return true
	}
	return false
}

func paruBootstrapActions() []engine.Action {
	return []engine.Action{
		{
			ID:            "bootstrap:paru:deps",
			Kind:          engine.ActionInstall,
			Target:        "base-devel,git",
			Summary:       "Install paru build dependencies (base-devel, git)",
			Risk:          engine.RiskMedium,
			RequiresAllow: "sudo",
			NeedsTTY:      true,
			Command:       []string{"sudo", "pacman", "-S", "--needed", "--noconfirm", "base-devel", "git"},
			Metadata: map[string]string{
				"provider": "pacman",
				"source":   "paru-bootstrap",
			},
		},
		{
			ID:            "bootstrap:paru:build",
			Kind:          engine.ActionInstall,
			Target:        "paru-bin",
			Summary:       "Build and install paru-bin from AUR (makepkg -si --noconfirm)",
			Risk:          engine.RiskMedium,
			RequiresAllow: "sudo",
			NeedsTTY:      true,
			Metadata: map[string]string{
				"provider": "paru-bootstrap",
				"source":   "paru-bootstrap",
				"url":      paruAURURL,
			},
		},
	}
}

func runParuBootstrap(ctx *app.Context, act engine.Action, runner CommandRunner) error {
	if currentEUID() == 0 {
		return errors.New("paru bootstrap must run as the invoking user, never root")
	}
	repoURL := strings.TrimSpace(act.Metadata["url"])
	if repoURL != paruAURURL {
		return fmt.Errorf("%s: disallowed paru bootstrap URL %q", act.ID, repoURL)
	}
	tmpDir, err := os.MkdirTemp("", "ws-paru-*")
	if err != nil {
		return fmt.Errorf("%s: create temp dir: %w", act.ID, err)
	}
	defer os.RemoveAll(tmpDir)

	buildDir := filepath.Join(tmpDir, "paru-bin")
	if _, stderr, err := runner.Run([]string{"git", "clone", "--depth=1", repoURL, buildDir}, nil, "", false); err != nil {
		msg := strings.TrimSpace(stderr)
		if msg != "" {
			return fmt.Errorf("%s: git clone failed: %w: %s", act.ID, err, msg)
		}
		return fmt.Errorf("%s: git clone failed: %w", act.ID, err)
	}
	if _, stderr, err := runner.Run([]string{"makepkg", "-si", "--noconfirm"}, nil, buildDir, act.NeedsTTY && ctx.ChildTTY()); err != nil {
		msg := strings.TrimSpace(stderr)
		if msg != "" {
			return fmt.Errorf("%s: makepkg failed: %w: %s", act.ID, err, msg)
		}
		return fmt.Errorf("%s: makepkg failed: %w", act.ID, err)
	}
	return nil
}

func pacmanServerUpgradeAction() engine.Action {
	return engine.Action{
		ID:            "pacman:upgrade",
		Kind:          engine.ActionUpgrade,
		Target:        "pacman",
		Summary:       "Sync package databases and upgrade Arch server packages",
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"sudo", "pacman", "-Syu", "--noconfirm"},
		Metadata: map[string]string{
			"provider": "pacman",
		},
	}
}

func brewUpdateAction() engine.Action {
	return engine.Action{
		ID:      "brew:update",
		Kind:    engine.ActionUpgrade,
		Target:  "brew",
		Summary: "Refresh Homebrew package index",
		Risk:    engine.RiskSafe,
		Command: []string{"brew", "update"},
		Metadata: map[string]string{
			"provider":            "brew",
			"env.HOMEBREW_NO_ASK": "1",
		},
	}
}

func partitionBrewActions(actions []engine.Action) (removes, brewInstalls, caskInstalls, other []engine.Action) {
	for _, a := range actions {
		switch {
		case a.Kind == engine.ActionRemove:
			removes = append(removes, a)
		case a.Kind == engine.ActionInstall && a.Metadata["provider"] == "brew":
			brewInstalls = append(brewInstalls, a)
		case a.Kind == engine.ActionInstall && a.Metadata["provider"] == "cask":
			caskInstalls = append(caskInstalls, a)
		default:
			other = append(other, a)
		}
	}
	return removes, brewInstalls, caskInstalls, other
}

func planBatchedBrewKind(installs, existing []engine.Action, kind string, tapped, trusted map[string]bool) []engine.Action {
	if len(installs) == 0 {
		return nil
	}
	conflictTool := map[string]bool{}
	for _, pre := range existing {
		if pre.Kind == engine.ActionRemove && pre.Metadata["for_tool"] != "" {
			conflictTool[pre.Metadata["for_tool"]] = true
		}
	}

	var out []engine.Action
	trustFlag := "--formula"
	if kind == "cask" {
		trustFlag = "--cask"
	}

	var batchable []engine.Action
	var conflictInstalls []engine.Action
	for _, act := range installs {
		pkgName := strings.TrimSpace(act.Metadata["pkg"])
		if pkgName == "" {
			pkgName = strings.TrimSpace(act.Target)
		}
		if strings.Count(pkgName, "/") == 2 {
			parts := strings.Split(pkgName, "/")
			tap := parts[0] + "/" + parts[1]
			if !tapped[tap] {
				tapped[tap] = true
				out = append(out, engine.Action{
					ID:      fmt.Sprintf("tap:brew:%s", tap),
					Kind:    engine.ActionInstall,
					Target:  tap,
					Summary: fmt.Sprintf("Add Homebrew tap %s", tap),
					Risk:    engine.RiskSafe,
					Command: []string{"brew", "tap", tap},
					Metadata: map[string]string{
						"provider":                    "brew",
						"env.HOMEBREW_NO_ASK":         "1",
						"env.HOMEBREW_NO_AUTO_UPDATE": "1",
					},
				})
			}
			trustKey := kind + ":" + pkgName
			if !trusted[trustKey] {
				trusted[trustKey] = true
				out = append(out, engine.Action{
					ID:      fmt.Sprintf("trust:%s:%s", kind, pkgName),
					Kind:    engine.ActionInstall,
					Target:  pkgName,
					Summary: fmt.Sprintf("Trust %s from third-party tap %s", pkgName, tap),
					Risk:    engine.RiskSafe,
					Command: []string{"brew", "trust", trustFlag, pkgName},
					Metadata: map[string]string{
						"provider":                    "brew",
						"continue_on_error":           "true",
						"env.HOMEBREW_NO_ASK":         "1",
						"env.HOMEBREW_NO_AUTO_UPDATE": "1",
					},
				})
			}
		}
		if toolID := act.Metadata["tool"]; toolID != "" && conflictTool[toolID] {
			cloned := act
			meta := map[string]string{}
			for k, v := range act.Metadata {
				meta[k] = v
			}
			meta["env.HOMEBREW_NO_ASK"] = "1"
			meta["env.HOMEBREW_NO_AUTO_UPDATE"] = "1"
			cloned.Metadata = meta
			conflictInstalls = append(conflictInstalls, cloned)
			continue
		}
		batchable = append(batchable, act)
	}

	if len(batchable) == 1 && strings.TrimSpace(batchable[0].Metadata["pkgs"]) == "" {
		single := batchable[0]
		meta := map[string]string{}
		for k, v := range single.Metadata {
			meta[k] = v
		}
		meta["env.HOMEBREW_NO_ASK"] = "1"
		meta["env.HOMEBREW_NO_AUTO_UPDATE"] = "1"
		single.Metadata = meta
		out = append(out, single)
	} else if len(batchable) > 0 {
		seen := map[string]bool{}
		var pkgs []string
		for _, a := range batchable {
			if rawPkgs := strings.TrimSpace(a.Metadata["pkgs"]); rawPkgs != "" {
				for _, p := range strings.Split(rawPkgs, ",") {
					name := strings.TrimSpace(p)
					if name == "" || seen[name] {
						continue
					}
					seen[name] = true
					pkgs = append(pkgs, name)
				}
				continue
			}
			name := strings.TrimSpace(a.Metadata["pkg"])
			if name == "" {
				name = strings.TrimSpace(a.Target)
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			pkgs = append(pkgs, name)
		}
		if len(pkgs) > 0 {
			summaryList := strings.Join(pkgs, ", ")
			if len(pkgs) > 6 {
				summaryList = fmt.Sprintf("%s, +%d more", strings.Join(pkgs[:6], ", "), len(pkgs)-6)
			}
			cmd := append([]string{"brew", "install", trustFlag}, pkgs...)
			risk := engine.RiskSafe
			needsTTY := false
			summary := fmt.Sprintf("Install %d missing Homebrew formulae (%s)", len(pkgs), summaryList)
			if kind == "cask" {
				risk = engine.RiskMedium
				needsTTY = true
				summary = fmt.Sprintf("Install %d missing Homebrew casks (%s)", len(pkgs), summaryList)
			}
			out = append(out, engine.Action{
				ID:       fmt.Sprintf("install:%s:batch", kind),
				Kind:     engine.ActionInstall,
				Target:   strings.Join(pkgs, ","),
				Summary:  summary,
				Risk:     risk,
				NeedsTTY: needsTTY,
				Command:  cmd,
				Metadata: map[string]string{
					"provider":                    kind,
					"pkgs":                        strings.Join(pkgs, ","),
					"env.HOMEBREW_NO_ASK":         "1",
					"env.HOMEBREW_NO_AUTO_UPDATE": "1",
				},
			})
		}
	}

	out = append(out, conflictInstalls...)
	return out
}

func planFlatpakThemeActions(runner CommandRunner, skipped *[]string) []engine.Action {
	var actions []engine.Action
	hasFlatpak := hasBin(runner, "flatpak")

	hasFlathub := false
	if hasFlatpak {
		if out, _, err := runner.Run([]string{"flatpak", "remotes", "--columns=name"}, nil, "", false); err == nil {
			for _, line := range splitNonEmptyLines(out) {
				if strings.EqualFold(line, "flathub") {
					hasFlathub = true
					break
				}
			}
		}
	}
	if hasFlathub {
		*skipped = append(*skipped, "flathub: already configured (flatpak)")
	} else {
		actions = append(actions, engine.Action{
			ID:       "flatpak:remote:flathub",
			Kind:     engine.ActionInstall,
			Target:   "flathub",
			Summary:  "Add Flathub Flatpak remote",
			Risk:     engine.RiskSafe,
			NeedsTTY: true,
			Command:  []string{"flatpak", "remote-add", "--if-not-exists", "flathub", flathubRepoURL},
			Metadata: map[string]string{
				"provider": "flatpak",
			},
		})
	}

	installedThemes := map[string]bool{}
	if hasFlatpak {
		if out, _, err := runner.Run([]string{"flatpak", "list", "--runtime", "--columns=application"}, nil, "", false); err == nil {
			for _, line := range splitNonEmptyLines(out) {
				installedThemes[line] = true
			}
		}
	}
	var missingThemes []string
	for _, theme := range flatpakGTKThemes {
		if installedThemes[theme] {
			*skipped = append(*skipped, fmt.Sprintf("%s: already installed (flatpak)", theme))
			continue
		}
		missingThemes = append(missingThemes, theme)
	}
	if len(missingThemes) > 0 {
		actions = append(actions, engine.Action{
			ID:       "install:flatpak:adw-gtk3",
			Kind:     engine.ActionInstall,
			Target:   strings.Join(missingThemes, ","),
			Summary:  fmt.Sprintf("Install Flatpak GTK3 themes (%s)", strings.Join(missingThemes, ", ")),
			Risk:     engine.RiskSafe,
			NeedsTTY: true,
			Command:  append([]string{"flatpak", "install", "-y", "--noninteractive", "flathub"}, missingThemes...),
			Metadata: map[string]string{
				"provider": "flatpak",
				"pkgs":     strings.Join(missingThemes, ","),
			},
		})
	}
	return actions
}

func isSecretTool(id string) bool {
	switch strings.ToLower(id) {
	case "1password", "1password-cli":
		return true
	default:
		return false
	}
}

func osHookAction(ctx *app.Context, presetID, hookRel string) engine.Action {
	hookAbs := filepath.Join(ctx.RepoRoot, hookRel)
	meta := map[string]string{
		"hook":   hookRel,
		"preset": presetID,
	}
	if ctx != nil && (ctx.NonInteractive || ctx.JSON) {
		meta["env.WS_NON_INTERACTIVE"] = "1"
	}
	if ctx != nil && ctx.Allow.Yes {
		meta["env.WS_YES"] = "1"
	}
	return engine.Action{
		ID:            fmt.Sprintf("os_exec:%s", hookRel),
		Kind:          engine.ActionOSExec,
		Target:        hookAbs,
		Source:        hookRel,
		Summary:       fmt.Sprintf("Run OS hook %s", hookRel),
		Risk:          engine.RiskMedium,
		RequiresAllow: "sudo",
		NeedsTTY:      true,
		Command:       []string{"bash", hookAbs},
		Metadata:      meta,
	}
}

func osHookEnv(ctx *app.Context, env map[string]string) map[string]string {
	if env == nil {
		env = map[string]string{}
	}
	env["WS_NON_INTERACTIVE"] = "0"
	if ctx.NonInteractive || ctx.JSON {
		env["WS_NON_INTERACTIVE"] = "1"
	}
	env["WS_YES"] = "0"
	if ctx.Allow.Yes {
		env["WS_YES"] = "1"
	}
	return env
}

func presetConflictRemoveAction(via, pkgName string) engine.Action {
	cmd := []string{"sudo", "pacman", "-Rdd", "--noconfirm", pkgName}
	if via == "paru" {
		cmd = []string{"paru", "-Rdd", "--noconfirm", pkgName}
	}
	return engine.Action{
		ID:            fmt.Sprintf("conflict:remove:%s:%s", via, pkgName),
		Kind:          engine.ActionRemove,
		Target:        via + ":" + pkgName,
		Summary:       fmt.Sprintf("Remove conflicting %s package %s", via, pkgName),
		Detail:        "Replaced by preset package list; --noconfirm cannot resolve conflicts",
		Risk:          engine.RiskMedium,
		RequiresAllow: "remove,sudo",
		NeedsTTY:      true,
		Command:       cmd,
		Metadata: map[string]string{
			"provider": via,
			"pkg":      pkgName,
			"source":   "conflicts",
		},
	}
}

func conflictRemoveAction(via, pkgName, forTool string) engine.Action {
	var cmd []string
	meta := map[string]string{
		"provider": via,
		"pkg":      pkgName,
		"for_tool": forTool,
	}
	switch via {
	case "cask":
		cmd = []string{"brew", "uninstall", "--cask", pkgName}
		meta["env.HOMEBREW_NO_ASK"] = "1"
	case "brew":
		cmd = []string{"brew", "uninstall", pkgName}
		meta["env.HOMEBREW_NO_ASK"] = "1"
	case "pacman":
		cmd = []string{"sudo", "pacman", "-Rns", "--noconfirm", pkgName}
	case "paru":
		cmd = []string{"paru", "-Rns", "--noconfirm", pkgName}
	case "apt":
		cmd = []string{"sudo", "-E", "apt-get", "remove", "-y", pkgName}
		meta["env.DEBIAN_FRONTEND"] = "noninteractive"
	case "dnf":
		cmd = []string{"sudo", "dnf", "remove", "-y", pkgName}
	default:
		cmd = []string{via, "remove", pkgName}
	}

	reqAllow := "remove"
	if len(cmd) > 0 && (cmd[0] == "sudo" || cmd[0] == "paru") {
		reqAllow = "remove,sudo"
	}

	return engine.Action{
		ID:            fmt.Sprintf("conflict:remove:%s:%s", via, pkgName),
		Kind:          engine.ActionRemove,
		Target:        via + ":" + pkgName,
		Summary:       fmt.Sprintf("Remove conflicting %s package %s before installing %s", via, pkgName, forTool),
		Detail:        "Prevents PATH shadowing between conflicting installers",
		Risk:          engine.RiskMedium,
		RequiresAllow: reqAllow,
		NeedsTTY:      via == "cask" || (len(cmd) > 0 && (cmd[0] == "sudo" || cmd[0] == "paru")),
		Command:       cmd,
		Metadata:      meta,
	}
}

func planShimActionIfNeeded(ctx *app.Context, tool config.ResolvedTool, runner CommandRunner) (engine.Action, bool, error) {
	act, err := planShimAction(ctx, tool)
	if err != nil {
		return engine.Action{}, false, err
	}
	if ctx != nil && runner != nil {
		srcBin, _, _ := ParseShim(tool.Spec.Shim)
		if srcPath, lerr := runner.LookPath(srcBin); lerr == nil && srcPath != "" {
			if curDest, rerr := os.Readlink(act.Target); rerr == nil && filepath.Clean(curDest) == filepath.Clean(srcPath) {
				return act, false, nil
			}
		}
	}
	return act, true, nil
}

func planShimAction(ctx *app.Context, tool config.ResolvedTool) (engine.Action, error) {
	srcBin, dstBin, err := ParseShim(tool.Spec.Shim)
	if err != nil {
		return engine.Action{}, err
	}
	linkPath := filepath.Join(ctx.HomeDir, ".local", "bin", dstBin)
	return engine.Action{
		ID:      fmt.Sprintf("shim:%s:%s", tool.ID, dstBin),
		Kind:    engine.ActionSymlink,
		Target:  linkPath,
		Source:  srcBin,
		Summary: fmt.Sprintf("Create shim %s -> %s", ctx.CollapseHome(linkPath), srcBin),
		Risk:    engine.RiskSafe,
		Metadata: map[string]string{
			"shim": tool.Spec.Shim,
			"tool": tool.ID,
		},
	}, nil
}

var allowedBootstrapBins = map[string]bool{
	"brew":     true,
	"pacman":   true,
	"paru":     true,
	"paccache": true,
	"sudo":     true,
	"apt-get":  true,
	"apt":      true,
	"dnf":      true,
	"mise":     true,
	"gh":       true,
	"bash":     true,
	"git":      true,
	"rustup":   true,
	"tldr":     true,
	"flatpak":  true,
}

var allowedSudoProviderBins = map[string]bool{
	"pacman":    true,
	"paccache":  true,
	"apt-get":   true,
	"apt":       true,
	"dnf":       true,
	"systemctl": true,
}

func validateBootstrapAction(ctx *app.Context, act engine.Action) error {
	if act.Metadata["provider"] == "paru-bootstrap" {
		if len(act.Command) > 0 {
			return fmt.Errorf("%s: paru-bootstrap action must not specify raw command", act.ID)
		}
		if strings.TrimSpace(act.Metadata["url"]) != paruAURURL {
			return fmt.Errorf("%s: disallowed paru bootstrap URL %q", act.ID, act.Metadata["url"])
		}
		return nil
	}
	if act.Metadata["provider"] == "script" {
		if len(act.Command) > 0 {
			return fmt.Errorf("%s: script action must not specify raw command", act.ID)
		}
		return nil
	}
	if act.Kind == engine.ActionSymlink {
		if act.Metadata["shim"] == "" {
			return fmt.Errorf("%s: symlink action requires shim metadata", act.ID)
		}
		if _, _, err := ParseShim(act.Metadata["shim"]); err != nil {
			return fmt.Errorf("%s: %w", act.ID, err)
		}
		return nil
	}
	if len(act.Command) == 0 {
		return fmt.Errorf("%s: missing command", act.ID)
	}
	bin := act.Command[0]
	if !allowedBootstrapBins[bin] {
		return fmt.Errorf("%s: command %q is not an allowlisted package provider binary", act.ID, bin)
	}

	effCmd := act.Command
	usedSudo := false
	if bin == "sudo" {
		usedSudo = true
		idx := 1
		for idx < len(act.Command) && strings.HasPrefix(act.Command[idx], "-") {
			if act.Command[idx] != "-E" {
				return fmt.Errorf("%s: disallowed sudo flag %q", act.ID, act.Command[idx])
			}
			idx++
		}
		if idx >= len(act.Command) || !allowedSudoProviderBins[act.Command[idx]] {
			return fmt.Errorf("%s: sudo target is not an allowlisted package provider binary", act.ID)
		}
		effCmd = act.Command[idx:]
	}

	if act.Kind == engine.ActionOSExec {
		if isAptUpdateCommand(act) {
			return nil
		}
		if effCmd[0] == "git" && len(effCmd) == 5 && effCmd[1] == "-C" && effCmd[2] == ctx.RepoRoot && effCmd[3] == "pull" && effCmd[4] == "--ff-only" {
			return nil
		}
		if usedSudo && slices.Equal(effCmd, []string{"systemctl", "restart", "tailscaled"}) {
			return nil
		}
		if len(act.Command) != 2 || bin != "bash" {
			return fmt.Errorf("%s: os_exec must invoke bash with a single script path under os/", act.ID)
		}
		if !isScriptInsideOSDir(ctx, act.Command[1]) {
			return fmt.Errorf("%s: os_exec script %q must stay strictly inside %s", act.ID, act.Command[1], filepath.Join(ctx.RepoRoot, "os"))
		}
		if act.Target != "" && !isScriptInsideOSDir(ctx, act.Target) {
			return fmt.Errorf("%s: os_exec target %q must stay strictly inside %s", act.ID, act.Target, filepath.Join(ctx.RepoRoot, "os"))
		}
		return nil
	}
	if bin == "bash" {
		return fmt.Errorf("%s: bash is only permitted for os_exec hooks under os/", act.ID)
	}
	return validateProviderSubcommand(act.ID, effCmd, usedSudo)
}

func validateProviderSubcommand(actID string, cmd []string, usedSudo bool) error {
	if len(cmd) == 0 {
		return fmt.Errorf("%s: empty command", actID)
	}
	switch cmd[0] {
	case "brew":
		if len(cmd) < 2 {
			return fmt.Errorf("%s: missing brew subcommand", actID)
		}
		switch cmd[1] {
		case "install", "upgrade", "uninstall", "tap", "trust", "update", "autoremove", "cleanup":
			return nil
		default:
			return fmt.Errorf("%s: disallowed brew subcommand %q", actID, cmd[1])
		}
	case "mise":
		if len(cmd) < 2 {
			return fmt.Errorf("%s: missing mise subcommand", actID)
		}
		switch cmd[1] {
		case "use", "upgrade", "up":
			return nil
		default:
			return fmt.Errorf("%s: disallowed mise subcommand %q", actID, cmd[1])
		}
	case "gh":
		if len(cmd) >= 3 && cmd[1] == "extension" && (cmd[2] == "install" || cmd[2] == "upgrade") {
			return nil
		}
		return fmt.Errorf("%s: disallowed gh subcommand %v", actID, cmd[1:])
	case "pacman", "paru":
		if len(cmd) >= 2 && (cmd[1] == "-S" || cmd[1] == "-Syu" || cmd[1] == "-Rns" || cmd[1] == "-Rdd") {
			return nil
		}
		return fmt.Errorf("%s: disallowed %s subcommand %v", actID, cmd[0], cmd[1:])
	case "paccache":
		for _, arg := range cmd[1:] {
			switch arg {
			case "-rk2", "-ruk0", "-r", "-u", "-k", "-d":
			default:
				return fmt.Errorf("%s: disallowed paccache flag %q", actID, arg)
			}
		}
		return nil
	case "apt-get", "apt":
		if len(cmd) < 2 {
			return fmt.Errorf("%s: missing %s subcommand", actID, cmd[0])
		}
		switch cmd[1] {
		case "update", "install", "upgrade", "remove":
		default:
			return fmt.Errorf("%s: disallowed %s subcommand %q", actID, cmd[0], cmd[1])
		}
		for _, arg := range cmd[2:] {
			if arg == "-o" || strings.HasPrefix(arg, "-o=") || arg == "--option" || strings.HasPrefix(arg, "--option=") {
				return fmt.Errorf("%s: disallowed apt-get option flag %q", actID, arg)
			}
		}
		return nil
	case "dnf":
		if len(cmd) >= 2 && (cmd[1] == "install" || cmd[1] == "upgrade" || cmd[1] == "remove") {
			return nil
		}
		return fmt.Errorf("%s: disallowed dnf subcommand %v", actID, cmd[1:])
	case "rustup":
		if slices.Equal(cmd, []string{"rustup", "update"}) {
			return nil
		}
		return fmt.Errorf("%s: disallowed rustup command %v", actID, cmd)
	case "tldr":
		if slices.Equal(cmd, []string{"tldr", "--update"}) {
			return nil
		}
		return fmt.Errorf("%s: disallowed tldr command %v", actID, cmd)
	case "flatpak":
		if len(cmd) >= 2 && (cmd[1] == "remote-add" || cmd[1] == "install") {
			return nil
		}
		return fmt.Errorf("%s: disallowed flatpak subcommand %v", actID, cmd[1:])
	case "systemctl":
		if usedSudo && slices.Equal(cmd, []string{"systemctl", "restart", "tailscaled"}) {
			return nil
		}
		return fmt.Errorf("%s: disallowed systemctl command %v", actID, cmd)
	default:
		return fmt.Errorf("%s: unsupported command %q", actID, cmd[0])
	}
}

func isAptUpdateCommand(act engine.Action) bool {
	if act.ID != "apt:update" {
		return false
	}
	cmd := act.Command
	idx := 0
	if len(cmd) > 0 && cmd[0] == "sudo" {
		idx = 1
		for idx < len(cmd) && cmd[idx] == "-E" {
			idx++
		}
	}
	return idx+1 < len(cmd) && cmd[idx] == "apt-get" && cmd[idx+1] == "update"
}

func isScriptInsideOSDir(ctx *app.Context, scriptPath string) bool {
	if ctx == nil || strings.TrimSpace(ctx.RepoRoot) == "" || strings.TrimSpace(scriptPath) == "" {
		return false
	}
	if !filepath.IsAbs(ctx.RepoRoot) || !filepath.IsAbs(scriptPath) {
		return false
	}
	osDir := filepath.Join(filepath.Clean(ctx.RepoRoot), "os")
	cleanScript := filepath.Clean(scriptPath)
	rel, err := filepath.Rel(osDir, cleanScript)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	realOSDir := evalExistingPkgPath(osDir)
	realScript := evalExistingPkgPath(cleanScript)
	realRel, err := filepath.Rel(realOSDir, realScript)
	if err != nil || realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func evalExistingPkgPath(path string) string {
	clean := filepath.Clean(path)
	var suffix []string
	cur := clean
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}

func missingBootstrapAllows(a app.AllowSet, act engine.Action) []string {
	var scopes []string
	addScope := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || slices.Contains(scopes, s) {
			return
		}
		scopes = append(scopes, s)
	}
	for _, part := range strings.Split(act.RequiresAllow, ",") {
		addScope(part)
	}
	if act.Metadata["provider"] == "script" {
		addScope("script")
	}
	if act.Metadata["provider"] == "paru-bootstrap" {
		addScope("sudo")
	}
	if act.Kind == engine.ActionOSExec && len(act.Command) > 0 && act.Command[0] == "bash" {
		addScope("sudo")
	}
	if act.Kind == engine.ActionRemove {
		addScope("remove")
	}
	if len(act.Command) > 0 && (act.Command[0] == "sudo" || act.Command[0] == "paru") {
		addScope("sudo")
	}
	var missing []string
	for _, scope := range scopes {
		if !allowSatisfied(a, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

func MissingPlanScopes(a app.AllowSet, plan *engine.Plan) ([]string, map[string][]string) {
	if plan == nil {
		return nil, nil
	}
	var order []string
	byScope := map[string][]string{}
	for _, act := range plan.Actions {
		for _, sc := range missingBootstrapAllows(a, act) {
			if _, ok := byScope[sc]; !ok {
				order = append(order, sc)
			}
			label := strings.TrimSpace(act.Summary)
			if label == "" {
				label = act.ID
			}
			byScope[sc] = append(byScope[sc], label)
		}
	}
	return order, byScope
}

func recordConsentSkip(res *engine.ApplyResult, scopes []string) {
	if res == nil {
		return
	}
	res.ConsentSkipped++
	for _, sc := range scopes {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if sc != "" && !slices.Contains(res.MissingAllows, sc) {
			res.MissingAllows = append(res.MissingAllows, sc)
		}
	}
}

func hookExitCode(err error) int {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func recordHookDecline(res *engine.ApplyResult) {
	if res == nil {
		return
	}
	res.ConsentSkipped++
	res.Declined++
}

func allowSatisfied(a app.AllowSet, scope string) bool {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return true
	}
	for _, part := range strings.Split(trimmed, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "":
			continue
		case "replace":
			if !a.Replace {
				return false
			}
		case "sudo":
			if !a.Sudo {
				return false
			}
		case "remove":
			if !a.Remove {
				return false
			}
		case "script":
			if !a.Script {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func extractEnv(meta map[string]string) map[string]string {
	var env map[string]string
	for k, v := range meta {
		if strings.HasPrefix(k, "env.") {
			if env == nil {
				env = map[string]string{}
			}
			env[strings.TrimPrefix(k, "env.")] = v
		}
	}
	return env
}
