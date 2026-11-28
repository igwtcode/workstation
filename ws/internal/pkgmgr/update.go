package pkgmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
)

type UpdateEvent struct {
	Phase    string
	ActionID string
	Summary  string
	Status   string
	NeedsTTY bool
	Duration time.Duration
	Detail   string
}

type ExecUpdateOptions struct {
	StreamOut io.Writer
	StreamErr io.Writer
	OnEvent   func(UpdateEvent)
}

type UpdateOptions struct {
	All     bool
	PkgOnly bool
}

type DigestItem struct {
	Name     string           `json:"name"`
	Provider string           `json:"provider"`
	Current  string           `json:"current,omitempty"`
	Latest   string           `json:"latest,omitempty"`
	Risk     engine.RiskLevel `json:"risk"`
	Note     string           `json:"note,omitempty"`
}

type UpdateDigest struct {
	Preset       string       `json:"preset"`
	RepoDirty    bool         `json:"repoDirty"`
	SnapshotHint string       `json:"snapshotHint,omitempty"`
	Items        []DigestItem `json:"items"`
	Warnings     []string     `json:"warnings,omitempty"`
	Plan         *engine.Plan `json:"plan,omitempty"`
}

func BuildUpdateDigest(ctx *app.Context, all bool) (*UpdateDigest, error) {
	return BuildUpdateDigestWithRunner(ctx, all, ExecRunner{Probe: true})
}

func BuildUpdateDigestWithRunner(ctx *app.Context, all bool, runner CommandRunner) (*UpdateDigest, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}

	presetID := ctx.EffectivePreset()
	digest := &UpdateDigest{
		Preset: presetID,
		Items:  []DigestItem{},
	}

	resolvedTools, selfByProv, err := resolveUpdateSelfTools(ctx, presetID)
	if err != nil {
		return nil, err
	}
	providers := AllProviders(ctx, runner, resolvedTools)

	var (
		mu              sync.Mutex
		wg              sync.WaitGroup
		activeProviders = map[string]bool{}
		queriedOutdated = map[string][]string{}
	)

	if ctx.RepoRoot != "" && hasBin(runner, "git") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, _, err := runner.Run([]string{"git", "-C", ctx.RepoRoot, "status", "--porcelain"}, nil, "", false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				digest.Warnings = append(digest.Warnings, fmt.Sprintf("unable to inspect git status: %v", err))
			} else if strings.TrimSpace(stdout) != "" {
				digest.RepoDirty = true
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		hint := detectSnapshotHint(ctx, runner)
		mu.Lock()
		digest.SnapshotHint = hint
		mu.Unlock()
	}()

	if hasBin(runner, "brew") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			brewCmd := []string{"brew", "outdated", "--json=v2"}
			if all {
				brewCmd = []string{"brew", "outdated", "--json=v2", "--greedy"}
			}
			stdout, _, err := runner.Run(brewCmd, nil, "", false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				digest.Warnings = append(digest.Warnings, fmt.Sprintf("brew outdated check failed: %v", err))
				return
			}
			formulaItems, caskItems, ok := parseBrewOutdatedJSON(stdout)
			if !ok {
				return
			}
			keptFormulae, skippedFormulae := filterDigestSelf(formulaItems, selfByProv["brew"])
			digest.Items = append(digest.Items, keptFormulae...)
			if len(keptFormulae) > 0 {
				activeProviders["brew"] = true
				queriedOutdated["brew"] = digestItemNames(keptFormulae)
			}
			for _, s := range skippedFormulae {
				digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating formula %s", s))
			}

			keptCasks, skippedCasks := filterDigestSelf(caskItems, selfByProv["cask"])
			digest.Items = append(digest.Items, keptCasks...)
			if len(keptCasks) > 0 {
				activeProviders["cask"] = true
				queriedOutdated["cask"] = digestItemNames(keptCasks)
			}
			for _, s := range skippedCasks {
				digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating cask %s", s))
			}
		}()
	}

	if hasBin(runner, "paru") || hasBin(runner, "checkupdates") || hasBin(runner, "pacman") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			aurSet := readForeignPackages(runner)
			var archItems []DigestItem
			provName := "pacman"
			if hasBin(runner, "paru") {
				provName = "paru"
			}

			if hasBin(runner, "checkupdates") {
				if stdout, _, err := runner.Run([]string{"checkupdates"}, nil, "", false); (err == nil || commandExitCode(err) == 2 || strings.TrimSpace(stdout) != "") && strings.TrimSpace(stdout) != "" {
					archItems = append(archItems, parseArchOutdated(stdout, "pacman", aurSet)...)
				}
				if hasBin(runner, "paru") {
					if stdout, _, err := runner.Run([]string{"paru", "-Qua"}, nil, "", false); (err == nil || strings.TrimSpace(stdout) != "") && strings.TrimSpace(stdout) != "" {
						for _, item := range parseArchOutdated(stdout, "paru", map[string]bool{}) {
							item.Provider = "aur"
							item.Risk, item.Note = classifyUpdateRisk(item.Name, "aur", true)
							archItems = append(archItems, item)
						}
					}
				}
			} else if hasBin(runner, "paru") {
				if stdout, _, err := runner.Run([]string{"paru", "-Qu"}, nil, "", false); err == nil || strings.TrimSpace(stdout) != "" {
					archItems = append(archItems, parseArchOutdated(stdout, "paru", aurSet)...)
				}
			} else if hasBin(runner, "pacman") {
				if stdout, _, err := runner.Run([]string{"pacman", "-Qu"}, nil, "", false); err == nil || strings.TrimSpace(stdout) != "" {
					archItems = append(archItems, parseArchOutdated(stdout, "pacman", aurSet)...)
				}
			}

			if len(archItems) > 0 {
				archSkip := unionSorted(selfByProv["pacman"], selfByProv["paru"])
				kept, skipped := filterDigestSelf(archItems, archSkip)
				mu.Lock()
				digest.Items = append(digest.Items, kept...)
				if len(kept) > 0 {
					activeProviders[provName] = true
				}
				for _, s := range skipped {
					digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating package %s (%s)", s, provName))
				}
				mu.Unlock()
			}
		}()
	}

	if hasBin(runner, "apt") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if stdout, _, err := runner.Run([]string{"apt", "list", "--upgradable"}, nil, "", false); err == nil {
				items := parseAptUpgradable(stdout)
				kept, skipped := filterDigestSelf(items, selfByProv["apt"])
				mu.Lock()
				digest.Items = append(digest.Items, kept...)
				if len(kept) > 0 {
					activeProviders["apt"] = true
					queriedOutdated["apt"] = digestItemNames(kept)
				}
				for _, s := range skipped {
					digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating apt package %s", s))
				}
				mu.Unlock()
			}
		}()
	}

	if hasBin(runner, "dnf") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, _, err := runner.Run([]string{"dnf", "check-update", "-q"}, nil, "", false)
			code := commandExitCode(err)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && code != 100 && code != 0 && strings.TrimSpace(stdout) == "" {
				digest.Warnings = append(digest.Warnings, fmt.Sprintf("dnf check-update failed: %v", err))
			} else if strings.TrimSpace(stdout) != "" && (err == nil || code == 100) {
				items := parseDnfOutdated(stdout)
				kept, skipped := filterDigestSelf(items, selfByProv["dnf"])
				digest.Items = append(digest.Items, kept...)
				if len(kept) > 0 {
					activeProviders["dnf"] = true
				}
				for _, s := range skipped {
					digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating dnf package %s", s))
				}
			}
		}()
	}

	if hasBin(runner, "mise") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if items, ok := queryMiseOutdated(ctx, runner); ok {
				kept, skipped := filterDigestSelf(items, selfByProv["mise"])
				mu.Lock()
				digest.Items = append(digest.Items, kept...)
				if len(kept) > 0 {
					activeProviders["mise"] = true
					queriedOutdated["mise"] = digestItemNames(kept)
				}
				for _, s := range skipped {
					digest.Warnings = append(digest.Warnings, fmt.Sprintf("excluded self-updating mise tool %s", s))
				}
				mu.Unlock()
			}
		}()
	}

	if hasBin(runner, "gh") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if stdout, _, err := runner.Run([]string{"gh", "extension", "list"}, nil, "", false); err == nil {
				if len(splitNonEmptyLines(stdout)) > 0 {
					mu.Lock()
					activeProviders["gh-ext"] = true
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	sort.Slice(digest.Items, func(i, j int) bool {
		ri, rj := riskRank(digest.Items[i].Risk), riskRank(digest.Items[j].Risk)
		if ri != rj {
			return ri > rj
		}
		if digest.Items[i].Provider != digest.Items[j].Provider {
			return digest.Items[i].Provider < digest.Items[j].Provider
		}
		return digest.Items[i].Name < digest.Items[j].Name
	})

	plan := &engine.Plan{
		Verb:      "update",
		Host:      engine.FormatPlanHost(ctx),
		Title:     fmt.Sprintf("Update packages for %s", presetID),
		Preset:    presetID,
		CreatedAt: time.Now().UTC(),
		Warnings:  slices.Clone(digest.Warnings),
	}

	if activeProviders["brew"] || activeProviders["cask"] {
		plan.Actions = append(plan.Actions, engine.Action{
			ID:      "update:brew-index",
			Kind:    engine.ActionUpgrade,
			Target:  "brew",
			Summary: "Refresh Homebrew formulae and casks index",
			Risk:    engine.RiskSafe,
			Command: []string{"brew", "update"},
			Metadata: map[string]string{
				"provider":            "brew",
				"phase":               "prep",
				"env.HOMEBREW_NO_ASK": "1",
			},
		})
	}

	orderedProviders := []string{"brew", "cask", "paru", "pacman", "apt", "dnf", "mise", "gh-ext"}
	for _, pname := range orderedProviders {
		if !activeProviders[pname] {
			continue
		}
		var act *engine.Action
		switch pname {
		case "brew":
			if bp, ok := providers["brew"].(*brewProvider); ok {
				act = bp.upgradeActionFromNames(queriedOutdated["brew"], true)
			}
		case "cask":
			if cp, ok := providers["cask"].(*caskProvider); ok {
				act = cp.upgradeActionFromNames(all, queriedOutdated["cask"], true)
			}
		case "apt":
			if ap, ok := providers["apt"].(*aptProvider); ok {
				act = ap.upgradeActionFromNames(queriedOutdated["apt"], true)
			}
		case "mise":
			if mp, ok := providers["mise"].(*miseProvider); ok {
				act = mp.upgradeActionFromNames(queriedOutdated["mise"], true)
			}
		default:
			if prov, ok := providers[pname]; ok {
				act = prov.UpgradeAction(all)
			}
		}
		if act != nil {
			if act.Metadata == nil {
				act.Metadata = map[string]string{}
			}
			if pname == "mise" || pname == "gh-ext" {
				act.Metadata["phase"] = "post-parallel"
			} else {
				act.Metadata["phase"] = "os-pkg"
			}
			plan.Actions = append(plan.Actions, *act)
		}
	}
	digest.Plan = plan

	return digest, nil
}

func resolveUpdateSelfTools(ctx *app.Context, presetID string) ([]config.ResolvedTool, map[string][]string, error) {
	var resolvedTools []config.ResolvedTool
	if ctx != nil && ctx.RepoRoot != "" {
		catalog, err := config.LoadTools(ctx.RepoRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("load tools: %w", err)
		}
		if presets, perr := config.LoadPresets(ctx.RepoRoot); perr == nil {
			if resolved, rerr := config.ResolvePreset(ctx.RepoRoot, presets, presetID); rerr == nil {
				resolvedTools = config.ResolveTools(resolved, catalog, ctx.Host)
			}
		}

		for _, id := range config.ListToolIDs(catalog) {
			spec := catalog[id]
			if spec.Updates != "self" {
				continue
			}
			if len(spec.Install) == 0 {
				resolvedTools = append(resolvedTools, config.ResolvedTool{
					ID:        id,
					Bin:       spec.Bin,
					Updates:   spec.Updates,
					Conflicts: spec.Conflicts,
					Source:    "tools.toml",
				})
				continue
			}
			for _, inst := range spec.Install {
				resolvedTools = append(resolvedTools, config.ResolvedTool{
					ID:        id,
					Bin:       spec.Bin,
					Updates:   spec.Updates,
					Conflicts: spec.Conflicts,
					Spec:      inst,
					Source:    "tools.toml",
				})
			}
		}
	}
	return resolvedTools, SelfUpdatedByProvider(resolvedTools), nil
}

func PlanUpdate(ctx *app.Context, opts UpdateOptions) (*UpdateDigest, error) {
	return PlanUpdateWithRunner(ctx, opts, ExecRunner{Probe: true})
}

func PlanUpdateWithRunner(ctx *app.Context, opts UpdateOptions, runner CommandRunner) (*UpdateDigest, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	digest, err := BuildUpdateDigestWithRunner(ctx, opts.All, runner)
	if err != nil {
		return nil, err
	}
	if opts.PkgOnly || digest.Plan == nil {
		return digest, nil
	}

	resolvedTools, selfByProv, serr := resolveUpdateSelfTools(ctx, digest.Preset)
	if serr != nil {
		return nil, serr
	}
	providers := AllProviders(ctx, runner, resolvedTools)

	plan := digest.Plan
	hasID := map[string]bool{}
	for _, a := range plan.Actions {
		hasID[a.ID] = true
	}

	var prepActions []engine.Action
	if ctx.RepoRoot != "" && !digest.RepoDirty && hasBin(runner, "git") {
		prepActions = append(prepActions, engine.Action{
			ID:      "update:repo-pull",
			Kind:    engine.ActionOSExec,
			Target:  ctx.RepoRoot,
			Summary: "Fast-forward workstation repository",
			Risk:    engine.RiskSafe,
			Command: []string{"git", "-C", ctx.RepoRoot, "pull", "--ff-only"},
			Metadata: map[string]string{
				"phase":             "prep",
				"continue_on_error": "true",
			},
		})
	}
	if ctx.Host.OS == "darwin" && hasBin(runner, "brew") && !hasID["update:brew-index"] {
		prepActions = append(prepActions, engine.Action{
			ID:      "update:brew-index",
			Kind:    engine.ActionUpgrade,
			Target:  "brew",
			Summary: "Refresh Homebrew formulae and casks index",
			Risk:    engine.RiskSafe,
			Command: []string{"brew", "update"},
			Metadata: map[string]string{
				"provider":                  "brew",
				"phase":                     "prep",
				"env.HOMEBREW_NO_ASK":       "1",
				"env.HOMEBREW_NO_ENV_HINTS": "1",
			},
		})
		hasID["update:brew-index"] = true
	}

	var osUpgradeActions []engine.Action
	var postActions []engine.Action
	for _, a := range plan.Actions {
		switch a.Metadata["phase"] {
		case "prep":
			prepActions = append(prepActions, a)
		case "post-parallel":
			postActions = append(postActions, a)
		default:
			osUpgradeActions = append(osUpgradeActions, a)
		}
	}

	brewIndexWillRefresh := hasID["update:brew-index"]
	if brewIndexWillRefresh {
		ensureFreshBrewUpgrade := func(provName string, greedy bool) {
			actID := "upgrade:" + provName
			skipSelf := selfByProv[provName]
			found := false
			for i := range osUpgradeActions {
				if osUpgradeActions[i].ID == actID {
					found = true
					configureBrewUpgradeAfterIndex(&osUpgradeActions[i], provName, greedy, skipSelf)
					break
				}
			}
			if !found {
				act := newFreshBrewUpgradeAction(provName, greedy, skipSelf)
				osUpgradeActions = append(osUpgradeActions, act)
				hasID[actID] = true
			}
		}
		ensureFreshBrewUpgrade("brew", false)
		ensureFreshBrewUpgrade("cask", opts.All)
	}

	isArch := isArchHost(ctx)
	if isArch && !hasID["upgrade:paru"] && !hasID["upgrade:pacman"] {
		archProv := "pacman"
		if hasBin(runner, "paru") {
			archProv = "paru"
		}
		if prov, ok := providers[archProv]; ok && prov.Available() {
			if act := prov.UpgradeAction(opts.All); act != nil {
				if act.Metadata == nil {
					act.Metadata = map[string]string{}
				}
				act.Metadata["phase"] = "os-pkg"
				osUpgradeActions = append(osUpgradeActions, *act)
				hasID[act.ID] = true
			}
		}
	}

	var osActions []engine.Action
	var archBootActions []engine.Action
	if ctx.RepoRoot != "" && digest.Preset != "" {
		if bootPlan, berr := PlanBootstrapWithRunner(ctx, digest.Preset, runner); berr == nil && bootPlan != nil {
			for _, ba := range bootPlan.Actions {
				if hasID[ba.ID] {
					continue
				}
				if ba.Kind == engine.ActionOSExec {
					continue
				}
				if ba.ID == "brew:update" && hasID["update:brew-index"] {
					continue
				}
				if ba.ID == "pacman:upgrade" && (hasID["upgrade:pacman"] || hasID["upgrade:paru"]) {
					continue
				}
				if ba.Metadata == nil {
					ba.Metadata = map[string]string{}
				}
				ba.Metadata["phase"] = "os-pkg"
				hasID[ba.ID] = true
				if isArch {
					archBootActions = append(archBootActions, ba)
					continue
				}
				osActions = append(osActions, ba)
			}
		}
	}
	if isArch {
		var paruBoot []engine.Action
		var restBoot []engine.Action
		for _, ba := range archBootActions {
			if ba.Metadata["source"] == "paru-bootstrap" || ba.Metadata["provider"] == "paru-bootstrap" {
				paruBoot = append(paruBoot, ba)
				continue
			}
			restBoot = append(restBoot, ba)
		}
		archPreUpgrade, archPostInstalls, archOtherPost := partitionArchActions(restBoot)
		osActions = append(osActions, paruBoot...)
		osActions = append(osActions, archPreUpgrade...)
		osActions = append(osActions, osUpgradeActions...)
		osActions = append(osActions, batchArchInstallActions(archPostInstalls, archPreUpgrade, "-S")...)
		osActions = append(osActions, archOtherPost...)
	} else {
		osActions = append(osActions, osUpgradeActions...)
	}

	if ctx.Host.OS == "darwin" && hasBin(runner, "brew") {
		osActions = append(osActions,
			engine.Action{
				ID:      "update:brew-autoremove",
				Kind:    engine.ActionUpgrade,
				Target:  "brew",
				Summary: "Remove unused Homebrew dependencies",
				Risk:    engine.RiskSafe,
				Command: []string{"brew", "autoremove"},
				Metadata: map[string]string{
					"provider":                    "brew",
					"phase":                       "os-pkg",
					"continue_on_error":           "true",
					"env.HOMEBREW_NO_ASK":         "1",
					"env.HOMEBREW_NO_AUTO_UPDATE": "1",
					"env.HOMEBREW_NO_ENV_HINTS":   "1",
				},
			},
			engine.Action{
				ID:      "update:brew-cleanup",
				Kind:    engine.ActionUpgrade,
				Target:  "brew",
				Summary: "Prune stale Homebrew downloads",
				Risk:    engine.RiskSafe,
				Command: []string{"brew", "cleanup", "--prune=all"},
				Metadata: map[string]string{
					"provider":                    "brew",
					"phase":                       "os-pkg",
					"continue_on_error":           "true",
					"env.HOMEBREW_NO_ASK":         "1",
					"env.HOMEBREW_NO_AUTO_UPDATE": "1",
					"env.HOMEBREW_NO_ENV_HINTS":   "1",
				},
			},
		)
	}

	if isArch && hasBin(runner, "paccache") {
		osActions = append(osActions,
			engine.Action{
				ID:            "update:paccache-keep",
				Kind:          engine.ActionUpgrade,
				Target:        "paccache",
				Summary:       "Prune pacman package cache (keep 2 latest versions)",
				Risk:          engine.RiskSafe,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"sudo", "paccache", "-rk2"},
				Metadata: map[string]string{
					"provider":          "pacman",
					"phase":             "os-pkg",
					"continue_on_error": "true",
				},
			},
			engine.Action{
				ID:            "update:paccache-uninstalled",
				Kind:          engine.ActionUpgrade,
				Target:        "paccache",
				Summary:       "Purge uninstalled pacman cache entries",
				Risk:          engine.RiskSafe,
				RequiresAllow: "sudo",
				NeedsTTY:      true,
				Command:       []string{"sudo", "paccache", "-ruk0"},
				Metadata: map[string]string{
					"provider":          "pacman",
					"phase":             "os-pkg",
					"continue_on_error": "true",
				},
			},
		)
	}
	if isArch && (hasBin(runner, "pacman") || hasBin(runner, "paru")) {
		qBin := "pacman"
		if !hasBin(runner, "pacman") {
			qBin = "paru"
		}
		if out, _, oerr := runner.Run([]string{qBin, "-Qdtq"}, nil, "", false); oerr == nil {
			if orphans := splitNonEmptyLines(out); len(orphans) > 0 {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("orphan Arch packages detected (%d): %s", len(orphans), strings.Join(orphans, ", ")))
			}
		}
	}

	if act, warn := detectStaleTailscaled(ctx, runner); act != nil {
		if act.Metadata == nil {
			act.Metadata = map[string]string{}
		}
		act.Metadata["phase"] = "os-pkg"
		osActions = append(osActions, *act)
	} else if warn != "" {
		plan.Warnings = append(plan.Warnings, warn)
	}

	if hasBin(runner, "rustup") {
		postActions = append(postActions, engine.Action{
			ID:      "update:rustup",
			Kind:    engine.ActionUpgrade,
			Target:  "rustup",
			Summary: "Update Rust toolchains via rustup",
			Risk:    engine.RiskSafe,
			Command: []string{"rustup", "update"},
			Metadata: map[string]string{
				"phase":             "post-parallel",
				"continue_on_error": "true",
			},
		})
	}
	if hasBin(runner, "tldr") {
		postActions = append(postActions, engine.Action{
			ID:      "update:tldr",
			Kind:    engine.ActionUpgrade,
			Target:  "tldr",
			Summary: "Update tldr page cache",
			Risk:    engine.RiskSafe,
			Command: []string{"tldr", "--update"},
			Metadata: map[string]string{
				"phase":             "post-parallel",
				"continue_on_error": "true",
			},
		})
	}
	if hasBin(runner, "gh") && !hasID["upgrade:gh-ext"] {
		postActions = append(postActions, engine.Action{
			ID:      "update:gh-ext",
			Kind:    engine.ActionUpgrade,
			Target:  "gh-ext",
			Summary: "Upgrade GitHub CLI extensions",
			Risk:    engine.RiskSafe,
			Command: []string{"gh", "extension", "upgrade", "--all"},
			Metadata: map[string]string{
				"phase":             "post-parallel",
				"continue_on_error": "true",
			},
		})
	}
	if hasBin(runner, "mise") && !hasID["upgrade:mise"] {
		if len(selfByProv["mise"]) == 0 {
			postActions = append(postActions, engine.Action{
				ID:      "update:mise-up",
				Kind:    engine.ActionUpgrade,
				Target:  "mise",
				Summary: "Upgrade mise managed runtimes and tools",
				Risk:    engine.RiskSafe,
				Command: []string{"mise", "up"},
				Metadata: map[string]string{
					"provider":          "mise",
					"phase":             "post-parallel",
					"continue_on_error": "true",
				},
			})
		}
	}

	reordered := make([]engine.Action, 0, len(prepActions)+len(osActions)+len(postActions))
	reordered = append(reordered, prepActions...)
	reordered = append(reordered, osActions...)
	reordered = append(reordered, postActions...)
	plan.Actions = reordered

	if plan.Metadata == nil {
		plan.Metadata = map[string]string{}
	}
	if ctx.RepoRoot != "" {
		plan.Metadata["post_apply"] = "true"
	}

	return digest, nil
}

func isArchHost(ctx *app.Context) bool {
	if ctx == nil {
		return false
	}
	return ctx.Host.Distro == "arch" || ctx.Host.Distro == "cachyos" || slices.Contains(ctx.Host.DistroLike, "arch")
}

func partitionArchActions(actions []engine.Action) (removes, archInstalls, other []engine.Action) {
	for _, a := range actions {
		switch {
		case a.Kind == engine.ActionRemove:
			removes = append(removes, a)
		case a.Kind == engine.ActionInstall && (a.Metadata["provider"] == "paru" || a.Metadata["provider"] == "pacman") && a.Metadata["source"] != "paru-bootstrap":
			archInstalls = append(archInstalls, a)
		default:
			other = append(other, a)
		}
	}
	return removes, archInstalls, other
}

func withArchSyncFlag(act engine.Action, syncFlag string) engine.Action {
	if len(act.Command) == 0 || syncFlag == "" {
		return act
	}
	cmd := slices.Clone(act.Command)
	flagIdx := -1
	if cmd[0] == "paru" && len(cmd) >= 2 {
		flagIdx = 1
	} else if cmd[0] == "sudo" && len(cmd) >= 3 && cmd[1] == "pacman" {
		flagIdx = 2
	} else if cmd[0] == "pacman" && len(cmd) >= 2 {
		flagIdx = 1
	}
	if flagIdx >= 0 && (cmd[flagIdx] == "-S" || cmd[flagIdx] == "-Syu") {
		cmd[flagIdx] = syncFlag
		act.Command = cmd
	}
	return act
}

func batchArchInstallActions(installs, preUpgrade []engine.Action, syncFlag string) []engine.Action {
	if len(installs) == 0 {
		return nil
	}
	if syncFlag == "" {
		syncFlag = "-S"
	}
	conflictTool := map[string]bool{}
	for _, pre := range preUpgrade {
		if pre.Kind == engine.ActionRemove && pre.Metadata["for_tool"] != "" {
			conflictTool[pre.Metadata["for_tool"]] = true
		}
	}
	var out []engine.Action
	var conflictInstalls []engine.Action
	var provOrder []string
	grouped := map[string][]engine.Action{}
	for _, act := range installs {
		if toolID := act.Metadata["tool"]; toolID != "" && conflictTool[toolID] {
			conflictInstalls = append(conflictInstalls, withArchSyncFlag(act, syncFlag))
			continue
		}
		prov := act.Metadata["provider"]
		if _, ok := grouped[prov]; !ok {
			provOrder = append(provOrder, prov)
		}
		grouped[prov] = append(grouped[prov], act)
	}
	for _, prov := range provOrder {
		acts := grouped[prov]
		if len(acts) == 1 && strings.TrimSpace(acts[0].Metadata["pkgs"]) == "" {
			out = append(out, withArchSyncFlag(acts[0], syncFlag))
			continue
		}
		seen := map[string]bool{}
		var pkgs []string
		phase := ""
		for _, a := range acts {
			if phase == "" && a.Metadata["phase"] != "" {
				phase = a.Metadata["phase"]
			}
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
		if len(pkgs) == 0 {
			continue
		}
		var cmd []string
		if prov == "paru" {
			cmd = append([]string{"paru", syncFlag, "--needed", "--noconfirm"}, pkgs...)
		} else {
			cmd = append([]string{"sudo", "pacman", syncFlag, "--needed", "--noconfirm"}, pkgs...)
		}
		summaryList := strings.Join(pkgs, ", ")
		if len(pkgs) > 6 {
			summaryList = fmt.Sprintf("%s, +%d more", strings.Join(pkgs[:6], ", "), len(pkgs)-6)
		}
		meta := map[string]string{
			"provider": prov,
			"pkgs":     strings.Join(pkgs, ","),
		}
		if phase != "" {
			meta["phase"] = phase
		}
		out = append(out, engine.Action{
			ID:            fmt.Sprintf("install:%s:batch", prov),
			Kind:          engine.ActionInstall,
			Target:        strings.Join(pkgs, ","),
			Summary:       fmt.Sprintf("Install %d missing %s packages (%s)", len(pkgs), prov, summaryList),
			Risk:          engine.RiskMedium,
			RequiresAllow: "sudo",
			NeedsTTY:      true,
			Command:       cmd,
			Metadata:      meta,
		})
	}
	out = append(out, conflictInstalls...)
	return out
}

func configureBrewUpgradeAfterIndex(act *engine.Action, provName string, greedy bool, skipSelf []string) {
	if act.Metadata == nil {
		act.Metadata = map[string]string{}
	}
	act.Metadata["phase"] = "os-pkg"
	act.Metadata["env.HOMEBREW_NO_ASK"] = "1"
	act.Metadata["env.HOMEBREW_NO_AUTO_UPDATE"] = "1"
	act.Metadata["env.HOMEBREW_NO_ENV_HINTS"] = "1"
	if provName == "cask" && greedy {
		act.Metadata["greedy"] = "true"
	}
	if len(skipSelf) > 0 {
		act.Metadata["refresh_before_run"] = "true"
		act.Metadata["skip_self_updated"] = strings.Join(skipSelf, ",")
		if provName == "cask" {
			act.Summary = "Upgrade outdated Homebrew casks (refresh after index, self-updaters excluded)"
		} else {
			act.Summary = "Upgrade outdated Homebrew formulae (refresh after index, self-updaters excluded)"
		}
		return
	}
	delete(act.Metadata, "refresh_before_run")
	if provName == "cask" {
		cmd := []string{"brew", "upgrade", "--cask"}
		if greedy {
			cmd = append(cmd, "--greedy")
		}
		act.Command = cmd
		act.Summary = "Upgrade Homebrew casks"
		return
	}
	act.Command = []string{"brew", "upgrade", "--formula"}
	act.Summary = "Upgrade Homebrew formulae"
}

func newFreshBrewUpgradeAction(provName string, greedy bool, skipSelf []string) engine.Action {
	risk := engine.RiskSafe
	needsTTY := false
	cmd := []string{"brew", "upgrade", "--formula"}
	if provName == "cask" {
		risk = engine.RiskMedium
		needsTTY = true
		cmd = []string{"brew", "upgrade", "--cask"}
		if greedy {
			cmd = append(cmd, "--greedy")
		}
	}
	act := engine.Action{
		ID:       "upgrade:" + provName,
		Kind:     engine.ActionUpgrade,
		Target:   provName,
		Risk:     risk,
		NeedsTTY: needsTTY,
		Command:  cmd,
		Metadata: map[string]string{
			"provider": provName,
			"phase":    "os-pkg",
		},
	}
	configureBrewUpgradeAfterIndex(&act, provName, greedy, skipSelf)
	return act
}

func ExecUpdate(ctx *app.Context, digest *UpdateDigest, runner CommandRunner) (*engine.ApplyResult, error) {
	return ExecUpdateWithOptions(ctx, digest, runner, ExecUpdateOptions{})
}

func ExecUpdateWithOptions(ctx *app.Context, digest *UpdateDigest, runner CommandRunner, opts ExecUpdateOptions) (*engine.ApplyResult, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if digest == nil || digest.Plan == nil {
		return nil, errors.New("nil update plan")
	}
	if digest.Plan.Verb != "" && digest.Plan.Verb != "update" && digest.Plan.Verb != "bootstrap" {
		return nil, fmt.Errorf("refusing plan with verb %q in update", digest.Plan.Verb)
	}
	if runner == nil {
		runner = ExecRunner{}
	}

	plan := digest.Plan
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

	var (
		mu                   sync.Mutex
		emitMu               sync.Mutex
		skippedConflictTools = map[string]bool{}
		skippedConsentTools  = map[string][]string{}
		priorToolSHA         = map[string]string{}
		managedTools         = map[string]app.ManagedToolRecord{}
	)
	emit := func(ev UpdateEvent) {
		if opts.OnEvent == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		opts.OnEvent(ev)
	}
	if ctx.WSBaseDir() != "" {
		initial, lerr := app.LoadLedger(ctx.WSBaseDir())
		if lerr != nil {
			return res, fmt.Errorf("load ledger: %w", lerr)
		}
		for id, rec := range initial.ManagedTools {
			priorToolSHA[id] = strings.TrimSpace(rec.SHA256)
		}
	}

	runAction := func(act engine.Action, phase string, allowStream bool) error {
		stepStart := time.Now()

		mu.Lock()
		missing := missingBootstrapAllows(ctx.Allow, act)
		if len(missing) > 0 {
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
			warnMsg := fmt.Sprintf("skipped %s: requires --allow %s", act.ID, strings.Join(missing, ","))
			res.Warnings = append(res.Warnings, warnMsg)
			mu.Unlock()
			emit(UpdateEvent{
				Phase:    phase,
				ActionID: act.ID,
				Summary:  act.Summary,
				Status:   "skip",
				Detail:   "requires --allow " + strings.Join(missing, ","),
			})
			return nil
		}
		if act.Kind == engine.ActionInstall {
			if toolID := act.Metadata["tool"]; toolID != "" && skippedConflictTools[toolID] {
				res.Skipped++
				recordConsentSkip(res, []string{"remove"})
				warnMsg := fmt.Sprintf("skipped %s: conflicting package removal for %s was skipped", act.ID, toolID)
				res.Warnings = append(res.Warnings, warnMsg)
				if _, seen := skippedConsentTools[toolID]; !seen {
					skippedConsentTools[toolID] = []string{"remove"}
				}
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "skip",
					Detail:   fmt.Sprintf("conflicting package removal for %s was skipped", toolID),
				})
				return nil
			}
		}
		if toolID := act.Metadata["tool"]; toolID != "" {
			if scopes, ok := skippedConsentTools[toolID]; ok {
				res.Skipped++
				recordConsentSkip(res, scopes)
				detail := fmt.Sprintf("%s was not installed (requires --allow %s)", toolID, strings.Join(scopes, ","))
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: %s", act.ID, detail))
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "skip",
					Detail:   detail,
				})
				return nil
			}
		}
		mu.Unlock()

		if act.Metadata["provider"] == "paru-bootstrap" {
			emit(UpdateEvent{Phase: phase, ActionID: act.ID, Summary: act.Summary, Status: "start", NeedsTTY: act.NeedsTTY && ctx.ChildTTY()})
			if err := runParuBootstrap(ctx, act, runner); err != nil {
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "fail",
					Duration: time.Since(stepStart),
					Detail:   compactEventDetail(err.Error()),
				})
				return err
			}
			mu.Lock()
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			mu.Unlock()
			emit(UpdateEvent{
				Phase:    phase,
				ActionID: act.ID,
				Summary:  act.Summary,
				Status:   "done",
				Duration: time.Since(stepStart),
			})
			return nil
		}

		if act.Metadata["provider"] == "script" {
			emit(UpdateEvent{Phase: phase, ActionID: act.ID, Summary: act.Summary, Status: "start"})
			mu.Lock()
			toolID := nonEmptyToolID(act)
			priorSHA256 := strings.TrimSpace(managedTools[toolID].SHA256)
			if priorSHA256 == "" {
				priorSHA256 = priorToolSHA[toolID]
			}
			mu.Unlock()
			tofu := strings.EqualFold(act.Metadata["tofu"], "true")
			spec := config.InstallSpec{
				Via:    "script",
				URL:    act.Metadata["url"],
				SHA256: act.Metadata["sha256"],
				TOFU:   tofu,
			}
			sum, skipped, warn, err := verifyAndRunScriptWithPrior(ctx, spec, toolID, priorSHA256, nil, runner)
			mu.Lock()
			if warn != "" {
				res.Warnings = append(res.Warnings, warn)
			}
			if err != nil {
				if act.Metadata["continue_on_error"] == "true" {
					res.Warnings = append(res.Warnings, fmt.Sprintf("%s warning: %v", act.ID, err))
					mu.Unlock()
					emit(UpdateEvent{
						Phase:    phase,
						ActionID: act.ID,
						Summary:  act.Summary,
						Status:   "warn",
						Duration: time.Since(stepStart),
						Detail:   compactEventDetail(err.Error()),
					})
					return nil
				}
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "fail",
					Duration: time.Since(stepStart),
					Detail:   compactEventDetail(err.Error()),
				})
				return err
			}
			if skipped {
				res.Skipped++
				recordConsentSkip(res, []string{"script"})
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "skip",
					Duration: time.Since(stepStart),
					Detail:   warn,
				})
				return nil
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
			mu.Unlock()
			emit(UpdateEvent{
				Phase:    phase,
				ActionID: act.ID,
				Summary:  act.Summary,
				Status:   "done",
				Duration: time.Since(stepStart),
			})
			return nil
		}

		if act.Kind == engine.ActionSymlink && act.Metadata["shim"] != "" {
			emit(UpdateEvent{Phase: phase, ActionID: act.ID, Summary: act.Summary, Status: "start"})
			if _, _, err := EnsureShim(ctx, act.Metadata["shim"], runner); err != nil {
				fullErr := fmt.Errorf("%s: %w", act.ID, err)
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "fail",
					Duration: time.Since(stepStart),
					Detail:   compactEventDetail(fullErr.Error()),
				})
				return fullErr
			}
			mu.Lock()
			res.Applied++
			res.Changed = append(res.Changed, act.ID)
			mu.Unlock()
			emit(UpdateEvent{
				Phase:    phase,
				ActionID: act.ID,
				Summary:  act.Summary,
				Status:   "done",
				Duration: time.Since(stepStart),
			})
			return nil
		}

		if act.Metadata["provider"] == "mise" {
			if ref := act.Metadata["ref"]; ref != "" {
				if err := ValidateMiseRef(ref); err != nil {
					fullErr := fmt.Errorf("%s: %w", act.ID, err)
					emit(UpdateEvent{
						Phase:    phase,
						ActionID: act.ID,
						Summary:  act.Summary,
						Status:   "fail",
						Duration: time.Since(stepStart),
						Detail:   compactEventDetail(fullErr.Error()),
					})
					return fullErr
				}
			}
		}

		env := extractEnv(act.Metadata)
		cmd := slices.Clone(act.Command)
		if act.Metadata["refresh_before_run"] == "true" && (act.Metadata["provider"] == "brew" || act.Metadata["provider"] == "cask") {
			refreshedCmd, skipRefresh, warnMsg := resolveFreshBrewUpgradeCmd(act, env, runner)
			if warnMsg != "" {
				mu.Lock()
				res.Warnings = append(res.Warnings, warnMsg)
				mu.Unlock()
			}
			if skipRefresh {
				mu.Lock()
				res.Skipped++
				mu.Unlock()
				if warnMsg != "" {
					emit(UpdateEvent{
						Phase:    phase,
						ActionID: act.ID,
						Summary:  act.Summary,
						Status:   "warn",
						Duration: time.Since(stepStart),
						Detail:   compactEventDetail(warnMsg),
					})
				} else {
					emit(UpdateEvent{
						Phase:    phase,
						ActionID: act.ID,
						Summary:  act.Summary,
						Status:   "skip",
						Duration: time.Since(stepStart),
						Detail:   "up to date",
					})
				}
				return nil
			}
			cmd = refreshedCmd
		}
		workDir := ""
		if act.Kind == engine.ActionOSExec || act.Metadata["provider"] == "mise" || (len(cmd) > 0 && cmd[0] == "mise") {
			workDir = ctx.RepoRoot
		}

		needsTTY := act.NeedsTTY && ctx.ChildTTY()
		emit(UpdateEvent{Phase: phase, ActionID: act.ID, Summary: act.Summary, Status: "start", NeedsTTY: needsTTY})

		var stderr string
		var err error
		if allowStream && opts.StreamOut != nil {
			if sr, ok := runner.(StreamingCommandRunner); ok {
				stderr, err = sr.RunStreaming(cmd, env, workDir, needsTTY, opts.StreamOut, opts.StreamErr)
			} else {
				_, stderr, err = runner.Run(cmd, env, workDir, needsTTY)
			}
		} else {
			_, stderr, err = runner.Run(cmd, env, workDir, needsTTY)
		}
		if err != nil {
			msg := strings.TrimSpace(stderr)
			var fullErr error
			if msg != "" {
				fullErr = fmt.Errorf("%s failed: %w: %s", act.ID, err, msg)
			} else {
				fullErr = fmt.Errorf("%s failed: %w", act.ID, err)
			}
			if act.Metadata["skip_if_missing"] == "true" {
				mu.Lock()
				res.Skipped++
				res.Warnings = append(res.Warnings, fmt.Sprintf("not available via %s, skipping %s: %v", act.Metadata["provider"], act.Target, err))
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "warn",
					Duration: time.Since(stepStart),
					Detail:   compactEventDetail(fullErr.Error()),
				})
				return nil
			}
			if act.Metadata["continue_on_error"] == "true" {
				mu.Lock()
				res.Warnings = append(res.Warnings, fullErr.Error())
				mu.Unlock()
				emit(UpdateEvent{
					Phase:    phase,
					ActionID: act.ID,
					Summary:  act.Summary,
					Status:   "warn",
					Duration: time.Since(stepStart),
					Detail:   compactEventDetail(fullErr.Error()),
				})
				return nil
			}
			emit(UpdateEvent{
				Phase:    phase,
				ActionID: act.ID,
				Summary:  act.Summary,
				Status:   "fail",
				Duration: time.Since(stepStart),
				Detail:   compactEventDetail(fullErr.Error()),
			})
			return fullErr
		}

		mu.Lock()
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
			}
		}
		mu.Unlock()
		emit(UpdateEvent{
			Phase:    phase,
			ActionID: act.ID,
			Summary:  act.Summary,
			Status:   "done",
			Duration: time.Since(stepStart),
		})
		return nil
	}

	var prepActions, osActions, postActions []engine.Action
	for _, act := range plan.Actions {
		switch act.Metadata["phase"] {
		case "prep":
			prepActions = append(prepActions, act)
		case "post-parallel":
			postActions = append(postActions, act)
		default:
			osActions = append(osActions, act)
		}
	}

	runParallel := func(acts []engine.Action, phase string) error {
		chains := map[string][]engine.Action{}
		var chainOrder []string
		var solo []engine.Action
		for _, a := range acts {
			if c := a.Metadata["chain"]; c != "" {
				if len(chains[c]) == 0 {
					chainOrder = append(chainOrder, c)
				}
				chains[c] = append(chains[c], a)
			} else {
				solo = append(solo, a)
			}
		}
		var (
			wg       sync.WaitGroup
			errMu    sync.Mutex
			firstErr error
		)
		setErr := func(err error) {
			if err == nil {
				return
			}
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
		}
		for _, a := range solo {
			act := a
			wg.Add(1)
			go func() {
				defer wg.Done()
				setErr(runAction(act, phase, false))
			}()
		}
		for _, cname := range chainOrder {
			seq := chains[cname]
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, act := range seq {
					if err := runAction(act, phase, false); err != nil {
						setErr(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		return firstErr
	}

	var (
		beforeHead      string
		hasRepoPull     bool
		tailRestartDone bool
	)
	for _, a := range prepActions {
		if a.ID == "update:repo-pull" {
			hasRepoPull = true
			break
		}
	}
	for _, a := range osActions {
		if a.ID == "update:tailscaled-restart" {
			tailRestartDone = true
			break
		}
	}
	if hasRepoPull && ctx.RepoRoot != "" && fileExistsNonDir(filepath.Join(ctx.RepoRoot, "ws", "go.mod")) && hasBin(runner, "git") {
		if out, _, gerr := runner.Run([]string{"git", "-C", ctx.RepoRoot, "rev-parse", "HEAD"}, nil, "", false); gerr == nil {
			beforeHead = strings.TrimSpace(out)
		}
	}

	var execErr error
	if len(prepActions) > 0 {
		if len(prepActions) == 1 {
			emit(UpdateEvent{Phase: "prep", Status: "phase", Summary: "Prep"})
			execErr = runAction(prepActions[0], "prep", true)
		} else {
			emit(UpdateEvent{Phase: "prep", Status: "phase", Summary: "Prep (parallel)"})
			execErr = runParallel(prepActions, "prep")
		}
	}

	if execErr == nil {
		if len(osActions) > 0 {
			emit(UpdateEvent{Phase: "os-pkg", Status: "phase", Summary: "OS packages & cleanup"})
		}
		for _, act := range osActions {
			if err := runAction(act, "os-pkg", true); err != nil {
				execErr = err
				break
			}
		}
		if execErr == nil && !tailRestartDone {
			if act, warn := detectStaleTailscaled(ctx, runner); act != nil {
				if len(osActions) == 0 {
					emit(UpdateEvent{Phase: "os-pkg", Status: "phase", Summary: "OS packages & cleanup"})
				}
				if err := runAction(*act, "os-pkg", true); err != nil {
					warnMsg := fmt.Sprintf("tailscaled restart warning: %v", err)
					res.Warnings = append(res.Warnings, warnMsg)
				}
			} else if warn != "" && !slices.Contains(res.Warnings, warn) {
				res.Warnings = append(res.Warnings, warn)
			}
		}
	}

	if execErr == nil && len(postActions) > 0 {
		if len(postActions) == 1 {
			emit(UpdateEvent{Phase: "post-parallel", Status: "phase", Summary: "User tools"})
			execErr = runAction(postActions[0], "post-parallel", true)
		} else {
			emit(UpdateEvent{Phase: "post-parallel", Status: "phase", Summary: "User tools (parallel)"})
			execErr = runParallel(postActions, "post-parallel")
		}
	}

	doPostApply := execErr == nil && plan.Metadata["post_apply"] == "true" && ctx.RepoRoot != ""
	if doPostApply {
		emit(UpdateEvent{Phase: "finalize", Status: "phase", Summary: "Finalize"})
		const applySummary = "Re-sync dotfile symlinks and rendered theme templates (ws apply)"
		applyStart := time.Now()
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:post-apply", Summary: applySummary, Status: "start"})
		applyPlan, perr := engine.PlanApply(ctx, "", "")
		if perr != nil {
			warnMsg := fmt.Sprintf("post-update apply plan failed: %v", perr)
			res.Warnings = append(res.Warnings, warnMsg)
			emit(UpdateEvent{
				Phase:    "finalize",
				ActionID: "update:post-apply",
				Summary:  applySummary,
				Status:   "warn",
				Duration: time.Since(applyStart),
				Detail:   compactEventDetail(perr.Error()),
			})
		} else if len(applyPlan.Actions) == 0 {
			emit(UpdateEvent{
				Phase:    "finalize",
				ActionID: "update:post-apply",
				Summary:  applySummary,
				Status:   "done",
				Duration: time.Since(applyStart),
				Detail:   "up to date",
			})
		} else {
			ares, aerr := engine.ExecApply(ctx, applyPlan)
			if aerr != nil {
				warnMsg := fmt.Sprintf("post-update apply failed: %v", aerr)
				res.Warnings = append(res.Warnings, warnMsg)
				emit(UpdateEvent{
					Phase:    "finalize",
					ActionID: "update:post-apply",
					Summary:  applySummary,
					Status:   "warn",
					Duration: time.Since(applyStart),
					Detail:   compactEventDetail(aerr.Error()),
				})
			} else {
				res.Applied += ares.Applied
				res.Changed = append(res.Changed, ares.Changed...)
				res.Warnings = append(res.Warnings, ares.Warnings...)
				emit(UpdateEvent{
					Phase:    "finalize",
					ActionID: "update:post-apply",
					Summary:  applySummary,
					Status:   "done",
					Duration: time.Since(applyStart),
					Detail:   fmt.Sprintf("%d applied", ares.Applied),
				})
				for _, w := range ares.Warnings {
					emit(UpdateEvent{
						Phase:    "finalize",
						ActionID: "update:post-apply",
						Summary:  "ws apply",
						Status:   "warn",
						Detail:   w,
					})
				}
			}
		}
		rebuildWSBinaryIfNeeded(ctx, hasRepoPull, beforeHead, runner, res, emit)
	}

	if ctx.WSBaseDir() != "" && (res.Applied > 0 || len(managedTools) > 0) {
		if lerr := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
			cur, err := app.LoadLedger(ctx.WSBaseDir())
			if err != nil {
				return fmt.Errorf("load ledger: %w", err)
			}
			for id, rec := range managedTools {
				cur.ManagedTools[id] = rec
			}
			if res.Applied > 0 {
				cur.RunJournal = append(cur.RunJournal, app.JournalEntry{
					Timestamp:    time.Now().UTC(),
					Command:      "update",
					Preset:       plan.Preset,
					Summary:      plan.Title,
					ActionsCount: res.Applied,
					Warnings:     slices.Clone(res.Warnings),
				})
			}
			if err := app.SaveLedger(ctx.WSBaseDir(), cur); err != nil {
				return fmt.Errorf("save ledger: %w", err)
			}
			return nil
		}); lerr != nil && execErr == nil {
			execErr = lerr
		}
	}

	return res, execErr
}

func resolveFreshBrewUpgradeCmd(act engine.Action, env map[string]string, runner CommandRunner) (cmd []string, skip bool, warning string) {
	prov := act.Metadata["provider"]
	greedy := act.Metadata["greedy"] == "true"
	var skipSelf []string
	for _, s := range strings.Split(act.Metadata["skip_self_updated"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			skipSelf = append(skipSelf, s)
		}
	}
	queryCmd := []string{"brew", "outdated", "--formula", "--quiet"}
	baseUpgrade := []string{"brew", "upgrade", "--formula"}
	if prov == "cask" {
		queryCmd = []string{"brew", "outdated", "--cask"}
		baseUpgrade = []string{"brew", "upgrade", "--cask"}
		if greedy {
			queryCmd = append(queryCmd, "--greedy")
			baseUpgrade = append(baseUpgrade, "--greedy")
		}
		queryCmd = append(queryCmd, "--quiet")
	}
	stdout, stderr, err := runner.Run(queryCmd, env, "", false)
	if err != nil {

		msg := strings.TrimSpace(stderr)
		if msg != "" {
			return nil, true, fmt.Sprintf("skipped %s: failed to re-query outdated packages after brew update: %v: %s", act.ID, err, msg)
		}
		return nil, true, fmt.Sprintf("skipped %s: failed to re-query outdated packages after brew update: %v", act.ID, err)
	}
	outdated := splitNonEmptyLines(stdout)
	kept, _ := filterOutSelf(outdated, skipSelf)
	if len(kept) == 0 {
		return nil, true, ""
	}
	return append(baseUpgrade, kept...), false, ""
}

func rebuildWSBinaryIfNeeded(ctx *app.Context, hasRepoPull bool, beforeHead string, runner CommandRunner, res *engine.ApplyResult, emit func(UpdateEvent)) {
	if ctx == nil || ctx.RepoRoot == "" || ctx.HomeDir == "" || runner == nil {
		return
	}
	wsDir := filepath.Join(ctx.RepoRoot, "ws")
	if !fileExistsNonDir(filepath.Join(wsDir, "go.mod")) || !hasBin(runner, "go") {
		return
	}
	target := filepath.Join(ctx.HomeDir, ".local", "bin", "ws")
	needsRebuild := !fileExistsNonDir(target)
	if !needsRebuild && hasRepoPull && beforeHead != "" && hasBin(runner, "git") {
		if afterOut, _, gerr := runner.Run([]string{"git", "-C", ctx.RepoRoot, "rev-parse", "HEAD"}, nil, "", false); gerr == nil {
			afterHead := strings.TrimSpace(afterOut)
			if afterHead != "" && afterHead != beforeHead {
				if diffOut, _, derr := runner.Run([]string{"git", "-C", ctx.RepoRoot, "diff", "--name-only", beforeHead + ".." + afterHead, "--", "ws/"}, nil, "", false); derr == nil && strings.TrimSpace(diffOut) != "" {
					needsRebuild = true
				}
			}
		}
	}
	if !needsRebuild {
		return
	}

	const summary = "Rebuild and install ~/.local/bin/ws"
	start := time.Now()
	emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "start"})

	ver := "dev"
	if hasBin(runner, "git") {
		if vOut, _, verr := runner.Run([]string{"git", "-C", ctx.RepoRoot, "describe", "--tags", "--always", "--dirty"}, nil, "", false); verr == nil && strings.TrimSpace(vOut) != "" {
			ver = strings.TrimSpace(vOut)
		}
	}
	destDir := filepath.Dir(target)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		msg := fmt.Sprintf("ws binary rebuild skipped: %v", err)
		res.Warnings = append(res.Warnings, msg)
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "warn", Duration: time.Since(start), Detail: compactEventDetail(err.Error())})
		return
	}
	tmp, err := os.CreateTemp(destDir, ".ws.tmp.*")
	if err != nil {
		msg := fmt.Sprintf("ws binary rebuild skipped: %v", err)
		res.Warnings = append(res.Warnings, msg)
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "warn", Duration: time.Since(start), Detail: compactEventDetail(err.Error())})
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if _, stderr, berr := runner.Run([]string{"go", "build", "-ldflags", "-s -w -X main.Version=" + ver, "-o", tmpPath, "./cmd/ws"}, nil, wsDir, false); berr != nil {
		stderrMsg := strings.TrimSpace(stderr)
		var warnMsg string
		if stderrMsg != "" {
			warnMsg = fmt.Sprintf("ws binary rebuild warning: %v: %s", berr, stderrMsg)
		} else {
			warnMsg = fmt.Sprintf("ws binary rebuild warning: %v", berr)
		}
		res.Warnings = append(res.Warnings, warnMsg)
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "warn", Duration: time.Since(start), Detail: compactEventDetail(warnMsg)})
		return
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("ws binary chmod warning: %v", err))
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "warn", Duration: time.Since(start), Detail: compactEventDetail(err.Error())})
		return
	}
	if err := os.Rename(tmpPath, target); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("ws binary install warning: %v", err))
		emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "warn", Duration: time.Since(start), Detail: compactEventDetail(err.Error())})
		return
	}
	res.Applied++
	res.Changed = append(res.Changed, "update:ws-install")
	emit(UpdateEvent{Phase: "finalize", ActionID: "update:ws-install", Summary: summary, Status: "done", Duration: time.Since(start), Detail: "version " + ver})
}

func compactEventDetail(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "; "), "\n", "; "))
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

func fileExistsNonDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func nonEmptyToolID(act engine.Action) string {
	if t := act.Metadata["tool"]; t != "" {
		return t
	}
	return act.Target
}

func digestItemNames(items []DigestItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

func detectStaleTailscaled(ctx *app.Context, runner CommandRunner) (*engine.Action, string) {
	if !hasBin(runner, "tailscale") || !hasBin(runner, "tailscaled") {
		return nil, ""
	}
	stdout, _, err := runner.Run([]string{"tailscale", "version"}, nil, "", false)
	if err != nil || strings.TrimSpace(stdout) == "" {
		return nil, ""
	}
	var cliVer, daemonVer string
	for _, line := range splitNonEmptyLines(stdout) {
		if cliVer == "" {
			cliVer = strings.Fields(line)[0]
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "daemon:") {
			fields := strings.Fields(strings.TrimPrefix(line, "daemon:"))
			if len(fields) > 0 {
				daemonVer = fields[0]
			}
		}
	}
	if cliVer == "" || daemonVer == "" || cliVer == daemonVer {
		return nil, ""
	}
	if ctx != nil && ctx.Host.OS == "linux" && hasBin(runner, "systemctl") {
		return &engine.Action{
			ID:            "update:tailscaled-restart",
			Kind:          engine.ActionOSExec,
			Target:        "tailscaled",
			Summary:       fmt.Sprintf("Restart stale tailscaled daemon (%s -> %s)", daemonVer, cliVer),
			Risk:          engine.RiskMedium,
			RequiresAllow: "sudo",
			NeedsTTY:      true,
			Command:       []string{"sudo", "systemctl", "restart", "tailscaled"},
		}, ""
	}
	return nil, fmt.Sprintf("tailscaled daemon (%s) differs from CLI (%s); restart the Tailscale daemon", daemonVer, cliVer)
}

func detectSnapshotHint(ctx *app.Context, runner CommandRunner) string {
	isArch := ctx.Host.Distro == "arch" || ctx.Host.Distro == "cachyos" || slices.Contains(ctx.Host.DistroLike, "arch")
	if isArch || hasBin(runner, "snapper") {
		if hasBin(runner, "snapper") {
			return "sudo snapper create --description 'pre-ws-update'"
		}
		if isArch {
			return "Create a btrfs snapshot before upgrading desktop/kernel packages"
		}
	}
	if ctx.Host.OS == "darwin" && hasBin(runner, "tmutil") {
		return "tmutil localsnapshot"
	}
	return ""
}

func readForeignPackages(runner CommandRunner) map[string]bool {
	out := map[string]bool{}
	stdout, _, err := runner.Run([]string{"pacman", "-Qm"}, nil, "", false)
	if err != nil {
		return out
	}
	for _, line := range splitNonEmptyLines(stdout) {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			out[fields[0]] = true
		}
	}
	return out
}

func filterDigestSelf(items []DigestItem, skip []string) (kept []DigestItem, excluded []string) {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[strings.ToLower(s)] = true
	}
	for _, item := range items {
		if skipSet[strings.ToLower(item.Name)] {
			excluded = append(excluded, item.Name)
			continue
		}
		kept = append(kept, item)
	}
	return kept, excluded
}

type brewOutdatedFormulaJSON struct {
	Name              string   `json:"name"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
	Pinned            bool     `json:"pinned"`
}

type brewOutdatedCaskJSON struct {
	Name              string   `json:"name"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
}

type brewOutdatedV2 struct {
	Formulae []brewOutdatedFormulaJSON `json:"formulae"`
	Casks    []brewOutdatedCaskJSON    `json:"casks"`
}

func parseBrewOutdatedJSON(stdout string) (formulae []DigestItem, casks []DigestItem, ok bool) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || trimmed[0] != '{' {
		return nil, nil, false
	}
	var doc brewOutdatedV2
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, nil, false
	}
	for _, f := range doc.Formulae {
		if f.Pinned || strings.TrimSpace(f.Name) == "" {
			continue
		}
		risk, note := classifyUpdateRisk(f.Name, "brew", false)
		formulae = append(formulae, DigestItem{
			Name:     f.Name,
			Provider: "brew",
			Current:  strings.Join(f.InstalledVersions, ", "),
			Latest:   f.CurrentVersion,
			Risk:     risk,
			Note:     note,
		})
	}
	for _, c := range doc.Casks {
		if strings.TrimSpace(c.Name) == "" {
			continue
		}
		risk, note := classifyUpdateRisk(c.Name, "cask", false)
		casks = append(casks, DigestItem{
			Name:     c.Name,
			Provider: "cask",
			Current:  strings.Join(c.InstalledVersions, ", "),
			Latest:   c.CurrentVersion,
			Risk:     risk,
			Note:     note,
		})
	}
	return formulae, casks, true
}

func parseArchOutdated(stdout, provider string, aurSet map[string]bool) []DigestItem {
	var items []DigestItem
	for _, line := range splitNonEmptyLines(stdout) {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[2] != "->" {
			continue
		}
		name := fields[0]
		current := fields[1]
		latest := fields[3]
		isAUR := aurSet[name]
		itemProv := provider
		if isAUR {
			itemProv = "aur"
		}
		risk, note := classifyUpdateRisk(name, itemProv, isAUR)
		items = append(items, DigestItem{
			Name:     name,
			Provider: itemProv,
			Current:  current,
			Latest:   latest,
			Risk:     risk,
			Note:     note,
		})
	}
	return items
}

func parseAptUpgradable(stdout string) []DigestItem {
	var items []DigestItem
	for _, line := range splitNonEmptyLines(stdout) {
		if strings.HasPrefix(strings.ToLower(line), "listing") {
			continue
		}
		slash := strings.IndexByte(line, '/')
		if slash <= 0 {
			continue
		}
		name := line[:slash]
		fields := strings.Fields(line)
		latest := ""
		if len(fields) >= 2 {
			latest = fields[1]
		}
		current := ""
		if idx := strings.Index(line, "upgradable from:"); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("upgradable from:"):])
			current = strings.TrimRight(rest, "]")
		}
		risk, note := classifyUpdateRisk(name, "apt", false)
		items = append(items, DigestItem{
			Name:     name,
			Provider: "apt",
			Current:  current,
			Latest:   latest,
			Risk:     risk,
			Note:     note,
		})
	}
	return items
}

func parseDnfOutdated(stdout string) []DigestItem {
	var items []DigestItem
	for _, rawLine := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "obsoleting packages") || strings.HasPrefix(lower, "security:") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[0]
		if dot := strings.LastIndexByte(name, '.'); dot > 0 {
			name = name[:dot]
		}
		latest := fields[1]
		risk, note := classifyUpdateRisk(name, "dnf", false)
		items = append(items, DigestItem{
			Name:     name,
			Provider: "dnf",
			Latest:   latest,
			Risk:     risk,
			Note:     note,
		})
	}
	return items
}

type miseOutdatedEntry struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
}

func miseWorkDir(ctx *app.Context) string {
	if ctx != nil {
		return ctx.RepoRoot
	}
	return ""
}

func queryMiseOutdated(ctx *app.Context, runner CommandRunner) ([]DigestItem, bool) {
	workDir := miseWorkDir(ctx)
	if stdout, _, err := runner.Run([]string{"mise", "outdated", "--json"}, nil, workDir, false); err == nil {
		if items, ok := parseMiseOutdatedJSON(stdout); ok {
			return items, true
		}
	}
	if stdout, _, err := runner.Run([]string{"mise", "outdated"}, nil, workDir, false); err == nil {
		return parseMiseOutdated(stdout), true
	}
	return nil, false
}

func parseMiseOutdatedJSON(stdout string) ([]DigestItem, bool) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil, false
	}
	var items []DigestItem
	switch trimmed[0] {
	case '{':
		var byTool map[string]miseOutdatedEntry
		if err := json.Unmarshal([]byte(trimmed), &byTool); err != nil {
			return nil, false
		}
		keys := make([]string, 0, len(byTool))
		for k := range byTool {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entry := byTool[k]
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				name = k
			}
			risk, note := classifyUpdateRisk(name, "mise", false)
			items = append(items, DigestItem{
				Name:     name,
				Provider: "mise",
				Current:  entry.Current,
				Latest:   entry.Latest,
				Risk:     risk,
				Note:     note,
			})
		}
		return items, true
	case '[':
		var list []miseOutdatedEntry
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil, false
		}
		for _, entry := range list {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			risk, note := classifyUpdateRisk(name, "mise", false)
			items = append(items, DigestItem{
				Name:     name,
				Provider: "mise",
				Current:  entry.Current,
				Latest:   entry.Latest,
				Risk:     risk,
				Note:     note,
			})
		}
		return items, true
	default:
		return nil, false
	}
}

func parseMiseOutdated(stdout string) []DigestItem {
	var items []DigestItem
	for _, line := range splitNonEmptyLines(stdout) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if strings.EqualFold(fields[0], "tool") {
			continue
		}
		name := fields[0]
		current := fields[len(fields)-2]
		latest := fields[len(fields)-1]
		risk, note := classifyUpdateRisk(name, "mise", false)
		items = append(items, DigestItem{
			Name:     name,
			Provider: "mise",
			Current:  current,
			Latest:   latest,
			Risk:     risk,
			Note:     note,
		})
	}
	return items
}

func classifyUpdateRisk(name, provider string, isAUR bool) (engine.RiskLevel, string) {
	lower := strings.ToLower(name)

	if isAUR || provider == "aur" {
		return engine.RiskDangerous, "high risk: AUR package"
	}

	compositorsAndGreeters := []string{
		"hyprland", "niri", "noctalia", "dux-shell", "dux-greeter-bin",
		"greetd", "sway", "xwayland", "wayland",
	}
	for _, k := range compositorsAndGreeters {
		if lower == k || strings.HasPrefix(lower, k+"-") {
			return engine.RiskDangerous, "high risk: compositor/greeter"
		}
	}

	toolchainsAndCore := []string{
		"linux", "systemd", "glibc", "mesa", "vulkan", "nvidia",
		"go", "rust", "rustup", "gcc", "clang", "llvm", "python",
		"node", "nodejs", "bun", "jdk", "openjdk", "zulu",
	}
	for _, k := range toolchainsAndCore {
		if lower == k || strings.HasPrefix(lower, k+"-") || strings.HasPrefix(lower, k+"@") {
			return engine.RiskDangerous, "high risk: toolchain or core system component"
		}
	}

	if provider == "cask" || provider == "pacman" || provider == "paru" || provider == "apt" || provider == "dnf" {
		return engine.RiskMedium, ""
	}
	return engine.RiskSafe, ""
}

func riskRank(r engine.RiskLevel) int {
	switch r {
	case engine.RiskDangerous:
		return 4
	case engine.RiskCostly:
		return 3
	case engine.RiskMedium:
		return 2
	case engine.RiskSafe:
		return 1
	default:
		return 0
	}
}
