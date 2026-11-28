package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/pelletier/go-toml/v2"
)

type PlanApplyOptions struct {
	OnlyApp          string
	ThemeOverride    string
	ForceHooks       bool
	AllPostSyncHooks bool
}

func PlanApply(ctx *app.Context, onlyApp string, themeOverride string) (*Plan, error) {
	return PlanApplyWithConfig(ctx, PlanApplyOptions{
		OnlyApp:       onlyApp,
		ThemeOverride: themeOverride,
	})
}

func PlanApplyWithOptions(ctx *app.Context, onlyApp string, themeOverride string, forceHooks bool) (*Plan, error) {
	return PlanApplyWithConfig(ctx, PlanApplyOptions{
		OnlyApp:       onlyApp,
		ThemeOverride: themeOverride,
		ForceHooks:    forceHooks,
	})
}

func PlanApplyWithConfig(ctx *app.Context, opts PlanApplyOptions) (*Plan, error) {
	onlyApp := opts.OnlyApp
	themeOverride := opts.ThemeOverride
	forceHooks := opts.ForceHooks
	allPostSyncHooks := opts.AllPostSyncHooks
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(ctx.RepoRoot) == "" {
		return nil, fmt.Errorf("workstation repository root is required")
	}
	if ctx.MiseLocalErr != nil {
		return nil, fmt.Errorf("cannot read mise.local.toml (refusing to ignore WS_EXCLUDE_PATHS): %w", ctx.MiseLocalErr)
	}

	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		return nil, err
	}

	themeID, pal, err := loadActivePalette(ctx.RepoRoot, themeOverride, ledger)
	if err != nil {
		return nil, err
	}

	presetID := ctx.EffectivePreset()
	var resolvedPreset *config.ResolvedPreset
	presets, perr := config.LoadPresets(ctx.RepoRoot)
	if perr != nil {
		if !errors.Is(perr, os.ErrNotExist) || onlyApp == "" {
			return nil, perr
		}
	} else if presetID != "" {
		resolvedPreset, err = config.ResolvePreset(ctx.RepoRoot, presets, presetID)
		if err != nil {
			return nil, err
		}
	}

	regEntries, err := config.LoadThemeRegistry(ctx.RepoRoot)
	if err != nil {
		return nil, err
	}

	var linkEntries []config.LinkEntry
	if resolvedPreset != nil {
		linkEntries, err = config.LoadLinksForPreset(ctx.RepoRoot, resolvedPreset)
		if err != nil {
			return nil, err
		}
	}

	outputTargets := buildOutputTargetMap(regEntries, linkEntries)

	only := strings.ToLower(strings.TrimSpace(onlyApp))
	if only != "" {
		var filteredReg []config.RegistryEntry
		for _, e := range regEntries {
			if strings.EqualFold(e.App, only) {
				filteredReg = append(filteredReg, e)
			}
		}
		var filteredLinks []config.LinkEntry
		for _, l := range linkEntries {
			if linkMatchesApp(l, only) {
				filteredLinks = append(filteredLinks, l)
			}
		}
		if len(filteredReg) == 0 && len(filteredLinks) == 0 {
			return nil, fmt.Errorf("unknown app %q", onlyApp)
		}
		regEntries = filteredReg
		linkEntries = filteredLinks
	}

	var excludes []string
	var excludedSkipped []string
	if excludes = parseExcludeFilters(ctx); len(excludes) > 0 {
		regEntries, linkEntries, excludedSkipped = applyExcludeFilters(ctx, excludes, regEntries, linkEntries, outputTargets)
	}

	title := fmt.Sprintf("Apply theme %s", themeID)
	if presetID != "" {
		title = fmt.Sprintf("Apply preset %s + theme %s", presetID, themeID)
	}
	if only != "" {
		title = fmt.Sprintf("Apply %s (theme %s)", only, themeID)
	}

	plan := &Plan{
		Verb:      "apply",
		Host:      FormatPlanHost(ctx),
		Title:     title,
		Preset:    presetID,
		Theme:     themeID,
		CreatedAt: time.Now().UTC(),
		Actions:   []Action{},
		Skipped:   excludedSkipped,
	}
	if only != "" {
		plan.Metadata = map[string]string{
			"partial": "true",
			"only":    only,
		}
	}

	var hooksToRun []config.RegistryEntry
	plannedOutputs := map[string]bool{}
	for _, entry := range regEntries {
		act, changed, err := planRenderEntry(ctx.RepoRoot, entry, themeID, pal)
		if err != nil {
			return nil, err
		}
		if !changed {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("unchanged %s", entry.Output))
			if forceHooks && entry.PostHook != "" {
				hooksToRun = append(hooksToRun, entry)
			}
			continue
		}
		if ledgerHash := ledger.RenderedHashes[entry.Output]; ledgerHash != "" {
			if diskBytes, rerr := os.ReadFile(filepath.Join(ctx.RepoRoot, entry.Output)); rerr == nil && SHA256Hex(diskBytes) != ledgerHash {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("hand-edited generated output %s will be overwritten", entry.Output))
			}
		}
		plannedOutputs[filepath.ToSlash(filepath.Clean(entry.Output))] = true
		plan.Actions = append(plan.Actions, act)
		if entry.PostHook != "" {
			hooksToRun = append(hooksToRun, entry)
		}
	}

	var batLinked bool
	var tmuxPlanned bool
	if len(linkEntries) > 0 {
		linkPlan, _, err := planLinkEntriesWithPlanned(ctx, presetID, linkEntries, plannedOutputs)
		if err != nil {
			return nil, err
		}
		tmuxDir := filepath.Clean(filepath.Join(ctx.ConfigDir, "tmux"))
		for _, a := range linkPlan.Actions {
			appName := config.DeriveAppName(a.Metadata["sourceRel"])
			if strings.EqualFold(appName, "bat") {
				batLinked = true
			}
			if strings.EqualFold(appName, "tmux") || filepath.Clean(a.Target) == tmuxDir {
				tmuxPlanned = true
			}
		}
		plan.Actions = append(plan.Actions, linkPlan.Actions...)
		plan.Skipped = append(plan.Skipped, linkPlan.Skipped...)
		plan.Warnings = append(plan.Warnings, linkPlan.Warnings...)
	}

	seenHooks := map[string]bool{}
	for idx, entry := range hooksToRun {
		if seenHooks[entry.PostHook] {
			continue
		}
		seenHooks[entry.PostHook] = true
		plan.Actions = append(plan.Actions, Action{
			ID:      fmt.Sprintf("hook:%s:%d", entry.App, idx),
			Kind:    ActionHook,
			Target:  entry.App,
			Summary: fmt.Sprintf("post-hook %s: %s", entry.App, entry.PostHook),
			Detail:  entry.PostHook,
			Risk:    RiskSafe,
			Command: []string{"bash", "-c", entry.PostHook},
			Metadata: map[string]string{
				"app":  entry.App,
				"hook": entry.PostHook,
			},
		})
	}

	if only == "" {
		syncActs, syncWarns := planPostSyncActions(ctx, excludes, seenHooks, batLinked, tmuxPlanned, allPostSyncHooks)
		plan.Actions = append(plan.Actions, syncActs...)
		plan.Warnings = append(plan.Warnings, syncWarns...)
	} else if batLinked && !seenHooks["bat cache --build"] && commandOnPath("bat") {
		plan.Actions = append(plan.Actions, Action{
			ID:      "sync:bat-cache",
			Kind:    ActionHook,
			Target:  "bat",
			Summary: "post-hook bat: rebuild bat cache",
			Risk:    RiskSafe,
			Command: []string{"bat", "cache", "--build"},
			Metadata: map[string]string{
				"app":  "bat",
				"hook": "bat cache --build",
			},
		})
	}

	return plan, nil
}

func planRenderEntry(repoRoot string, entry config.RegistryEntry, themeID string, pal config.Palette) (Action, bool, error) {
	rendered, err := renderRegistryEntry(repoRoot, entry, pal)
	if err != nil {
		return Action{}, false, fmt.Errorf("render %s: %w", entry.Template, err)
	}

	outAbs := filepath.Join(repoRoot, entry.Output)
	diskBytes, err := os.ReadFile(outAbs)
	diskExists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Action{}, false, err
	}
	if diskExists && string(diskBytes) == rendered {
		return Action{}, false, nil
	}

	oldLabel := entry.Output
	oldText := string(diskBytes)
	if !diskExists {
		oldLabel = "/dev/null"
		oldText = ""
	}
	diff := UnifiedDiff(oldLabel, entry.Output, oldText, rendered)

	return Action{
		ID:      "render:" + entry.Output,
		Kind:    ActionRender,
		Target:  outAbs,
		Source:  filepath.Join(repoRoot, entry.Template),
		Summary: fmt.Sprintf("render %s (%s)", entry.Output, themeID),
		Detail:  fmt.Sprintf("app=%s theme=%s", entry.App, themeID),
		Diff:    diff,
		Risk:    RiskSafe,
		Metadata: map[string]string{
			"app":         entry.App,
			"outputRel":   entry.Output,
			"templateRel": entry.Template,
			"theme":       themeID,
			"sha256":      SHA256Hex([]byte(rendered)),
			"content":     rendered,
		},
	}, true, nil
}

func linkMatchesApp(l config.LinkEntry, appLower string) bool {
	return strings.EqualFold(config.DeriveAppName(l.SourceRel), appLower)
}

func parseExcludeFilters(ctx *app.Context) []string {
	if ctx == nil {
		return nil
	}
	rawExact, _ := ctx.LookupEnvExact("WS_EXCLUDE_PATHS")
	raw := strings.TrimSpace(rawExact)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func buildOutputTargetMap(regs []config.RegistryEntry, links []config.LinkEntry) map[string]string {
	out := make(map[string]string, len(regs))
	for _, r := range regs {
		outNorm := filepath.ToSlash(filepath.Clean(r.Output))
		for _, l := range links {
			srcNorm := filepath.ToSlash(filepath.Clean(l.SourceRel))
			if outNorm == srcNorm {
				out[outNorm] = l.TargetRaw
				break
			}
			if strings.HasPrefix(outNorm, srcNorm+"/") {
				rel := strings.TrimPrefix(outNorm, srcNorm+"/")
				out[outNorm] = strings.TrimRight(l.TargetRaw, "/") + "/" + rel
				break
			}
		}
	}
	return out
}

func applyExcludeFilters(ctx *app.Context, excludes []string, regs []config.RegistryEntry, links []config.LinkEntry, outputTargets map[string]string) ([]config.RegistryEntry, []config.LinkEntry, []string) {
	var skipped []string
	keptRegs := make([]config.RegistryEntry, 0, len(regs))
	for _, r := range regs {
		outNorm := filepath.ToSlash(filepath.Clean(r.Output))
		mappedTarget := ""
		if outputTargets != nil {
			mappedTarget = outputTargets[outNorm]
		}
		if matchExcludeFilter(ctx, excludes, r.App, r.Output, mappedTarget) {
			skipped = append(skipped, fmt.Sprintf("excluded %s (WS_EXCLUDE_PATHS)", r.Output))
			continue
		}
		keptRegs = append(keptRegs, r)
	}
	keptLinks := make([]config.LinkEntry, 0, len(links))
	for _, l := range links {
		if matchExcludeFilter(ctx, excludes, config.DeriveAppName(l.SourceRel), l.SourceRel, l.TargetRaw) {
			skipped = append(skipped, fmt.Sprintf("excluded %s (WS_EXCLUDE_PATHS)", l.TargetRaw))
			continue
		}
		keptLinks = append(keptLinks, l)
	}
	return keptRegs, keptLinks, skipped
}

func matchExcludeFilter(ctx *app.Context, excludes []string, appName, repoRelPath, targetPath string) bool {
	normRepo := strings.Trim(filepath.ToSlash(filepath.Clean(repoRelPath)), "/")
	repoSegs := strings.Split(normRepo, "/")

	var tgtCol, tgtExp string
	var tgtSegs []string
	if targetPath != "" && ctx != nil {
		tgtCol = strings.TrimRight(filepath.ToSlash(ctx.CollapseHome(targetPath)), "/")
		tgtExp = strings.TrimRight(filepath.ToSlash(filepath.Clean(ctx.ExpandHome(targetPath))), "/")
		tgtSegs = strings.Split(strings.Trim(tgtCol, "/"), "/")
	}

	for _, ex := range excludes {
		isToken := !strings.Contains(ex, "/") && !strings.HasPrefix(ex, "~")
		if isToken {
			if appName != "" && strings.EqualFold(appName, ex) {
				return true
			}
			for _, seg := range repoSegs {
				if strings.EqualFold(seg, ex) {
					return true
				}
			}
			for _, seg := range tgtSegs {
				if strings.EqualFold(seg, ex) {
					return true
				}
			}
			continue
		}

		exRepo := strings.Trim(filepath.ToSlash(filepath.Clean(ex)), "/")
		if normRepo != "" && exRepo != "" && !strings.HasPrefix(exRepo, "~") && !strings.HasPrefix(exRepo, "/") {
			if normRepo == exRepo || strings.HasPrefix(normRepo, exRepo+"/") {
				return true
			}
		}

		exCol := ex
		exExp := ex
		if ctx != nil {
			exCol = strings.TrimRight(filepath.ToSlash(ctx.CollapseHome(ex)), "/")
			exExp = strings.TrimRight(filepath.ToSlash(filepath.Clean(ctx.ExpandHome(ex))), "/")
		}
		if targetPath != "" {
			if tgtCol == exCol || strings.HasPrefix(tgtCol, exCol+"/") || tgtExp == exExp || strings.HasPrefix(tgtExp, exExp+"/") {
				return true
			}
		}
	}
	return false
}

func allowGranted(allow app.AllowSet, scope string) bool {
	return allow.Satisfies(scope)
}

func validateApplyAction(ctx *app.Context, act Action, regByOutput map[string]config.RegistryEntry, allowedHooks map[string]bool) error {
	switch act.Kind {
	case ActionRender:
		if err := validateRenderTarget(ctx, act.Target, regByOutput); err != nil {
			return fmt.Errorf("%s: %w", act.ID, err)
		}
		if act.Source != "" && !isSourceStrictlyInside(act.Source, ctx.RepoRoot) {
			return fmt.Errorf("%s: render source %q must stay inside repo root", act.ID, act.Source)
		}
		return nil
	case ActionSymlink:
		if err := validateSymlinkTarget(ctx, act.Target); err != nil {
			return fmt.Errorf("%s: %w", act.ID, err)
		}
		if act.ID == "sync:aws-default-creds" && act.Metadata["relative"] == "true" {
			if filepath.Base(act.Target) != "credentials" || filepath.Base(filepath.Dir(act.Target)) != ".aws" {
				return fmt.Errorf("%s: invalid relative symlink target %q", act.ID, act.Target)
			}
			if act.Source == "" || filepath.IsAbs(act.Source) || strings.ContainsAny(act.Source, `/\`) || strings.HasPrefix(act.Source, ".") || !strings.HasSuffix(act.Source, "-credentials") {
				return fmt.Errorf("%s: invalid relative aws credentials target %q", act.ID, act.Source)
			}
			return nil
		}
		if act.Source == "" || !isSourceStrictlyInside(act.Source, ctx.RepoRoot) {
			return fmt.Errorf("%s: symlink source %q must stay inside repo root", act.ID, act.Source)
		}
		return nil
	case ActionEnv:
		if !strings.HasPrefix(act.ID, "sync:codex-analytics:") {
			return fmt.Errorf("%s: unsupported env_write action in apply", act.ID)
		}
		if ctx == nil || ctx.HomeDir == "" || !isStrictlyInside(act.Target, ctx.HomeDir) || filepath.Base(act.Target) != "config.toml" {
			return fmt.Errorf("%s: invalid codex analytics target %q", act.ID, act.Target)
		}
		return nil
	case ActionHook:
		return validateHookAction(ctx, act, allowedHooks)
	case ActionRemove, ActionOSExec:
		return fmt.Errorf("action %s: kind %q is not permitted in apply", act.ID, act.Kind)
	default:
		return fmt.Errorf("unsupported action kind %q", act.Kind)
	}
}

func validateHookAction(ctx *app.Context, act Action, allowedHooks map[string]bool) error {
	if isBuiltinSyncHook(ctx, act) {
		return nil
	}
	if isAllowedRegistryHook(act, allowedHooks) {
		return nil
	}
	if ctx != nil && ctx.Allow.Script {
		return nil
	}
	return fmt.Errorf("%s: hook command is not in themes/registry and requires --allow script", act.ID)
}

func isAllowedRegistryHook(act Action, allowedHooks map[string]bool) bool {
	hookStr := strings.TrimSpace(act.Metadata["hook"])
	if hookStr == "" || !allowedHooks[hookStr] {
		return false
	}
	return len(act.Command) == 0 || (len(act.Command) == 3 && act.Command[0] == "bash" && act.Command[1] == "-c" && strings.TrimSpace(act.Command[2]) == hookStr)
}

func tmuxTPMInstallCommand(tpmDir string) []string {
	q := shellSingleQuote(tpmDir)
	return []string{
		"bash", "-c",
		fmt.Sprintf("git clone --depth=1 https://github.com/tmux-plugins/tpm %s && %s/bin/install_plugins", q, q),
	}
}

func builtinSyncHookSpec(ctx *app.Context, id string) (cmd []string, requiresAllow string, ok bool) {
	switch id {
	case "sync:go-telemetry":
		return []string{"go", "telemetry", "off"}, "", true
	case "sync:bat-cache":
		return []string{"bat", "cache", "--build"}, "", true
	case "sync:gh-dash":
		return []string{"gh", "extension", "install", "dlvhdr/gh-dash"}, "script", true
	case "sync:tmux-tpm":
		if ctx == nil || strings.TrimSpace(ctx.ConfigDir) == "" {
			return nil, "", false
		}
		tpmDir := filepath.Join(ctx.ConfigDir, "tmux", "plugins", "tpm")
		return tmuxTPMInstallCommand(tpmDir), "script", true
	}
	return nil, "", false
}

func isBuiltinSyncHook(ctx *app.Context, act Action) bool {
	expectedCmd, _, ok := builtinSyncHookSpec(ctx, act.ID)
	if !ok || !slices.Equal(act.Command, expectedCmd) {
		return false
	}
	if act.ID == "sync:tmux-tpm" && act.Target != "" {
		expectedDir := filepath.Join(ctx.ConfigDir, "tmux", "plugins", "tpm")
		if filepath.Clean(act.Target) != filepath.Clean(expectedDir) {
			return false
		}
	}
	return true
}

func requiredApplyAllow(ctx *app.Context, act Action, allowedHooks map[string]bool) string {
	var scopes []string
	addScope := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || slices.Contains(scopes, s) {
			return
		}
		scopes = append(scopes, s)
	}
	switch act.Kind {
	case ActionSymlink:
		if fi, err := os.Lstat(act.Target); err == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				addScope("replace")
			} else if act.ID == "sync:aws-default-creds" && act.Metadata["relative"] == "true" {
				if cur, rerr := os.Readlink(act.Target); rerr != nil || cur != act.Source {
					addScope("replace")
				}
			}
		}
	case ActionHook:
		if _, reqAllow, ok := builtinSyncHookSpec(ctx, act.ID); ok && isBuiltinSyncHook(ctx, act) {
			addScope(reqAllow)
		} else if !isAllowedRegistryHook(act, allowedHooks) {
			addScope("script")
		}
	}
	return strings.Join(scopes, ",")
}

func validateRenderTarget(ctx *app.Context, target string, regByOutput map[string]config.RegistryEntry) error {
	if ctx == nil || strings.TrimSpace(ctx.RepoRoot) == "" {
		return fmt.Errorf("repository root is required")
	}
	if !isStrictlyInside(target, ctx.RepoRoot) {
		return fmt.Errorf("render target %q must stay strictly inside repo root", target)
	}
	rel, ok := repoRelPath(ctx.RepoRoot, target)
	if !ok {
		return fmt.Errorf("render target %q is outside repo root", target)
	}
	if _, exists := regByOutput[filepath.ToSlash(filepath.Clean(rel))]; !exists {
		return fmt.Errorf("render target %q is not a declared themes/registry output", rel)
	}
	return nil
}

func validateSymlinkTarget(ctx *app.Context, target string) error {
	if ctx == nil || ctx.HomeDir == "" {
		return fmt.Errorf("home directory is required for symlink target %q", target)
	}
	if isStrictlyInside(target, ctx.HomeDir) {
		return nil
	}
	return fmt.Errorf("symlink target %q must stay inside home directory", target)
}

func isSourceStrictlyInside(source, root string) bool {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	if !filepath.IsAbs(source) || !filepath.IsAbs(root) {
		return false
	}
	realRoot, err := evalExistingPath(filepath.Clean(root))
	if err != nil {
		return false
	}
	realSource, err := evalExistingPath(filepath.Clean(source))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realRoot, realSource)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func isStrictlyInside(target, root string) bool {
	if strings.TrimSpace(target) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	if !filepath.IsAbs(target) || !filepath.IsAbs(root) {
		return false
	}
	cleanTarget := filepath.Clean(target)
	cleanRoot := filepath.Clean(root)
	realRoot, err := evalExistingPath(cleanRoot)
	if err != nil {
		return false
	}
	realParent, err := evalExistingPath(filepath.Dir(cleanTarget))
	if err != nil {
		return false
	}
	realTarget := filepath.Clean(filepath.Join(realParent, filepath.Base(cleanTarget)))
	rel, err := filepath.Rel(realRoot, realTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func evalExistingPath(path string) (string, error) {
	clean := filepath.Clean(path)
	var suffix []string
	cur := clean
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if _, lerr := os.Lstat(cur); lerr == nil || !errors.Is(lerr, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean, nil
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}

func ExecApply(ctx *app.Context, plan *Plan) (*ApplyResult, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(ctx.RepoRoot) == "" {
		return nil, fmt.Errorf("workstation repository root is required")
	}
	if ctx.MiseLocalErr != nil {
		return nil, fmt.Errorf("cannot read mise.local.toml (refusing to ignore WS_EXCLUDE_PATHS): %w", ctx.MiseLocalErr)
	}
	if plan == nil {
		return nil, fmt.Errorf("plan is nil")
	}
	if plan.Verb != "apply" {
		return nil, fmt.Errorf("refusing plan with verb %q in apply (expected \"apply\")", plan.Verb)
	}
	expectedHost := FormatPlanHost(ctx)
	if plan.Host == "" || plan.Host != expectedHost {
		return nil, fmt.Errorf("refusing plan for host %q on current host %q", plan.Host, expectedHost)
	}

	regEntries, err := config.LoadThemeRegistry(ctx.RepoRoot)
	if err != nil {
		return nil, err
	}
	regByOutput := make(map[string]config.RegistryEntry, len(regEntries))
	allowedHooks := make(map[string]bool, len(regEntries))
	for _, entry := range regEntries {
		regByOutput[filepath.ToSlash(filepath.Clean(entry.Output))] = entry
		if entry.PostHook != "" {
			allowedHooks[entry.PostHook] = true
		}
	}

	res := &ApplyResult{
		Warnings: append([]string{}, plan.Warnings...),
	}

	for _, act := range plan.Actions {
		if err := validateApplyAction(ctx, act, regByOutput, allowedHooks); err != nil {
			return res, err
		}
	}

	if ctx.DryRun {
		res.Skipped = len(plan.Actions) + len(plan.Skipped)
		for _, act := range plan.Actions {
			if req := requiredApplyAllow(ctx, act, allowedHooks); req != "" && !allowGranted(ctx.Allow, req) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("[dry-run] %s requires --allow %s", act.ID, req))
			}
		}
		return res, nil
	}

	consentMissing := make(map[int]string, len(plan.Actions))
	for i, act := range plan.Actions {
		if req := requiredApplyAllow(ctx, act, allowedHooks); req != "" && !allowGranted(ctx.Allow, req) {
			consentMissing[i] = req
		}
	}

	var resultErr error
	lockErr := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
		ledger, err := app.LoadLedger(ctx.WSBaseDir())
		if err != nil {
			return err
		}

		tx := app.UndoTx{
			ID:         fmt.Sprintf("tx-%d", time.Now().UTC().UnixNano()),
			Timestamp:  time.Now().UTC(),
			Summary:    plan.Title,
			PrevTheme:  ledger.ActiveTheme,
			PrevPreset: ledger.ActivePreset,
		}

		savedLedger := false
		defer func() {
			if savedLedger || len(tx.Ops) == 0 {
				return
			}
			ledger.UndoLog = append(ledger.UndoLog, tx)
			res.UndoID = tx.ID
			ledger.RunJournal = append(ledger.RunJournal, app.JournalEntry{
				Timestamp:    time.Now().UTC(),
				Command:      "apply",
				Preset:       plan.Preset,
				Summary:      plan.Title + " (partial)",
				ActionsCount: res.Applied,
				Warnings:     res.Warnings,
			})
			if err := app.SaveLedger(ctx.WSBaseDir(), ledger); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("save partial undo ledger failed: %v", err))
			}
		}()

		palCache := map[string]config.Palette{}
		getPalette := func(themeID string) (config.Palette, error) {
			if themeID == "" {
				themeID = plan.Theme
			}
			if pal, ok := palCache[themeID]; ok {
				return pal, nil
			}
			resolvedID, pal, perr := loadActivePalette(ctx.RepoRoot, themeID, ledger)
			if perr != nil {
				return config.Palette{}, perr
			}
			palCache[themeID] = pal
			if resolvedID != "" {
				palCache[resolvedID] = pal
			}
			return pal, nil
		}

		res.Skipped = len(plan.Skipped)

		for i, act := range plan.Actions {
			if req, blocked := consentMissing[i]; blocked {
				recordApplyConsentSkip(ctx, res, act, req)
				continue
			}
			switch act.Kind {
			case ActionRender:
				outRel, ok := repoRelPath(ctx.RepoRoot, act.Target)
				if !ok {
					return fmt.Errorf("render target %q outside repo", act.Target)
				}
				outRel = filepath.ToSlash(filepath.Clean(outRel))
				regEntry, ok := regByOutput[outRel]
				if !ok {
					return fmt.Errorf("render target %q is not in themes/registry", outRel)
				}
				themeID := act.Metadata["theme"]
				pal, perr := getPalette(themeID)
				if perr != nil {
					return perr
				}
				content, rerr := renderRegistryEntry(ctx.RepoRoot, regEntry, pal)
				if rerr != nil {
					return rerr
				}
				if metaContent, hasMeta := act.Metadata["content"]; hasMeta && metaContent != content {
					return fmt.Errorf("%s: plan render content does not match template %s", act.ID, regEntry.Template)
				}

				if err := writeFileWithUndo(act.Target, []byte(content), 0o644, &tx); err != nil {
					return err
				}
				ledger.RenderedHashes[outRel] = SHA256Hex([]byte(content))
				res.Applied++
				res.Changed = append(res.Changed, act.Summary)

			case ActionSymlink:
				_, prevManaged := ledger.ManagedLinks[act.Target]
				var state string
				var serr error
				if act.ID == "sync:aws-default-creds" && act.Metadata["relative"] == "true" {
					state, serr = applyRelativeSymlink(act.Source, act.Target, ctx.Allow.Replace, &tx)
				} else {
					state, serr = ApplySymlink(act.Source, act.Target, ctx.Allow.Replace, &tx)
				}
				if serr != nil {
					return serr
				}
				if state == "ok" {
					res.Skipped++
					continue
				}
				if len(tx.Ops) > 0 && tx.Ops[len(tx.Ops)-1].Target == filepath.Clean(act.Target) {
					tx.Ops[len(tx.Ops)-1].PrevWasManaged = prevManaged
					if tx.Ops[len(tx.Ops)-1].PostLinkTarget == "" {
						tx.Ops[len(tx.Ops)-1].PostLinkTarget = act.Source
					}
				}
				ledger.ManagedLinks[act.Target] = act.Source
				res.Applied++
				res.Changed = append(res.Changed, fmt.Sprintf("%s (%s)", ctx.CollapseHome(act.Target), state))

			case ActionEnv:
				changed, cerr := applyCodexAnalyticsOff(act.Target, &tx)
				if cerr != nil {
					return cerr
				}
				if !changed {
					res.Skipped++
					continue
				}
				res.Applied++
				res.Changed = append(res.Changed, act.Summary)

			case ActionHook:
				execAct := act
				if cmd, _, ok := builtinSyncHookSpec(ctx, act.ID); ok && isBuiltinSyncHook(ctx, act) {
					execAct.Command = cmd
				} else if isAllowedRegistryHook(act, allowedHooks) {
					execAct.Command = []string{"bash", "-c", strings.TrimSpace(act.Metadata["hook"])}
				}
				if err := runActionCommand(ctx, execAct); err != nil {

					res.Warnings = append(res.Warnings, fmt.Sprintf("post-hook failed (%s): %v", act.Target, err))
					res.Skipped++
					continue
				}
				res.Applied++
				res.Changed = append(res.Changed, act.Summary)

			default:
				return fmt.Errorf("unsupported action kind %q", act.Kind)
			}
		}

		isPartial := plan.Metadata["partial"] == "true"
		stateChanged := false
		if !isPartial {
			if plan.Theme != "" && ledger.ActiveTheme != plan.Theme {
				ledger.ActiveTheme = plan.Theme
				stateChanged = true
			}
			if plan.Preset != "" && ledger.ActivePreset != plan.Preset {
				ledger.ActivePreset = plan.Preset
				stateChanged = true
			}
		}
		if len(tx.Ops) > 0 {
			ledger.UndoLog = append(ledger.UndoLog, tx)
			res.UndoID = tx.ID
		}
		if res.Applied > 0 || stateChanged || len(tx.Ops) > 0 {
			ledger.RunJournal = append(ledger.RunJournal, app.JournalEntry{
				Timestamp:    time.Now().UTC(),
				Command:      "apply",
				Preset:       plan.Preset,
				Summary:      plan.Title,
				ActionsCount: res.Applied,
				Warnings:     res.Warnings,
			})
			savedLedger = true
			if err := app.SaveLedger(ctx.WSBaseDir(), ledger); err != nil {
				return err
			}
		}
		return nil
	})
	if lockErr != nil {
		return res, lockErr
	}
	return res, resultErr
}

func recordApplyConsentSkip(ctx *app.Context, res *ApplyResult, act Action, req string) {
	res.Skipped++
	res.ConsentSkipped++
	for _, sc := range strings.Split(req, ",") {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if sc != "" && !slices.Contains(res.MissingAllows, sc) {
			res.MissingAllows = append(res.MissingAllows, sc)
		}
	}
	if act.Kind == ActionSymlink && req == "replace" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("kept existing %s (requires --allow replace)", ctx.CollapseHome(act.Target)))
		return
	}
	res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %s: requires --allow %s", act.ID, req))
}

func runActionCommand(ctx *app.Context, act Action) error {
	var cmd *exec.Cmd
	if len(act.Command) > 0 {
		cmd = exec.Command(act.Command[0], act.Command[1:]...)
	} else if hook := act.Metadata["hook"]; hook != "" {
		cmd = exec.Command("bash", "-c", hook)
	} else {
		return fmt.Errorf("action %s has no command", act.ID)
	}
	cmd.Env = applyActionEnv(ctx)
	if ctx.RepoRoot != "" {
		cmd.Dir = ctx.RepoRoot
	}
	if act.NeedsTTY && ctx.ChildTTY() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func applyActionEnv(ctx *app.Context) []string {
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

func writeFileWithUndo(target string, data []byte, defaultMode os.FileMode, tx *app.UndoTx) error {
	op := app.UndoOp{
		Kind:              "write_file",
		Target:            target,
		PostContentSHA256: SHA256Hex(data),
	}
	mode := defaultMode
	if fi, err := os.Lstat(target); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symlink %s during file write", target)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite non-regular file %s", target)
		}
		op.PrevExists = true
		op.PrevMode = uint32(fi.Mode().Perm())
		if fi.Mode().Perm() != 0 {
			mode = fi.Mode().Perm()
		}
		prevBytes, rerr := os.ReadFile(target)
		if rerr != nil {
			return rerr
		}
		op.PrevContentB64 = base64.StdEncoding.EncodeToString(prevBytes)
		op.PrevContentSHA256 = SHA256Hex(prevBytes)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := app.WriteAtomicFile(target, data, mode); err != nil {
		return err
	}
	if tx != nil {
		tx.Ops = append(tx.Ops, op)
	}
	return nil
}

func UndoLast(ctx *app.Context) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is nil")
	}
	var outMsg string
	err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
		ledger, err := app.LoadLedger(ctx.WSBaseDir())
		if err != nil {
			return err
		}
		if len(ledger.UndoLog) == 0 {
			return fmt.Errorf("undo log is empty")
		}

		tx := ledger.UndoLog[len(ledger.UndoLog)-1]
		if ctx.DryRun {
			outMsg = fmt.Sprintf("[dry-run] would undo %s (%s, %d ops)", tx.ID, tx.Summary, len(tx.Ops))
			return nil
		}

		completed := false
		origOpsCount := len(tx.Ops)
		remainingEnd := origOpsCount
		var skippedOps []app.UndoOp
		var skippedDrifted []string
		defer func() {
			if completed || (remainingEnd == origOpsCount && len(skippedOps) == 0) {
				return
			}
			kept := make([]app.UndoOp, 0, remainingEnd+len(skippedOps))
			kept = append(kept, tx.Ops[:remainingEnd]...)
			for j := len(skippedOps) - 1; j >= 0; j-- {
				kept = append(kept, skippedOps[j])
			}
			tx.Ops = kept
			if len(tx.Ops) == 0 {
				ledger.UndoLog = ledger.UndoLog[:len(ledger.UndoLog)-1]
			} else {
				ledger.UndoLog[len(ledger.UndoLog)-1] = tx
			}
			_ = app.SaveLedger(ctx.WSBaseDir(), ledger)
		}()

		var undoneOutputs []string
		undoneCount := 0
		for i := origOpsCount - 1; i >= 0; i-- {
			op := tx.Ops[i]
			switch op.Kind {
			case "write_file":
				if op.PostContentSHA256 != "" && !ctx.Allow.Replace {
					if curBytes, rerr := os.ReadFile(op.Target); rerr == nil {
						if SHA256Hex(curBytes) != op.PostContentSHA256 {
							return fmt.Errorf("refusing to overwrite post-apply edits at %s without --allow replace", op.Target)
						}
					}
				}
				if !op.PrevExists {
					if err := os.Remove(op.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
					if rel, ok := repoRelPath(ctx.RepoRoot, op.Target); ok {
						rel = filepath.ToSlash(filepath.Clean(rel))
						delete(ledger.RenderedHashes, rel)
						undoneOutputs = append(undoneOutputs, rel)
					}
				} else {
					prev, err := base64.StdEncoding.DecodeString(op.PrevContentB64)
					if err != nil {
						return fmt.Errorf("decode undo content for %s: %w", op.Target, err)
					}
					mode := os.FileMode(op.PrevMode)
					if mode == 0 {
						mode = 0o644
					}
					if err := app.WriteAtomicFile(op.Target, prev, mode); err != nil {
						return err
					}
					if rel, ok := repoRelPath(ctx.RepoRoot, op.Target); ok {
						rel = filepath.ToSlash(filepath.Clean(rel))
						ledger.RenderedHashes[rel] = SHA256Hex(prev)
						undoneOutputs = append(undoneOutputs, rel)
					}
				}

			case "create_symlink":
				if fi, err := os.Lstat(op.Target); err == nil {
					if fi.Mode()&os.ModeSymlink == 0 {
						return fmt.Errorf("cannot undo symlink %s: target is no longer a symlink", op.Target)
					}
					driftMsg, derr := checkSymlinkUndoDrift(ctx, op)
					if derr != nil {
						return derr
					}
					if driftMsg != "" {
						skippedDrifted = append(skippedDrifted, driftMsg)
						skippedOps = append(skippedOps, op)
						remainingEnd = i
						continue
					}
					if err := os.Remove(op.Target); err != nil {
						return err
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				delete(ledger.ManagedLinks, op.Target)

			case "replace_symlink":
				if fi, err := os.Lstat(op.Target); err == nil {
					if fi.Mode()&os.ModeSymlink == 0 {
						return fmt.Errorf("cannot restore symlink %s: target is no longer a symlink", op.Target)
					}
					driftMsg, derr := checkSymlinkUndoDrift(ctx, op)
					if derr != nil {
						return derr
					}
					if driftMsg != "" {
						skippedDrifted = append(skippedDrifted, driftMsg)
						skippedOps = append(skippedOps, op)
						remainingEnd = i
						continue
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if err := atomicSymlink(op.PrevLinkTarget, op.Target); err != nil {
					return err
				}
				if op.PrevWasManaged {
					ledger.ManagedLinks[op.Target] = op.PrevLinkTarget
				} else {
					delete(ledger.ManagedLinks, op.Target)
				}

			case "restore_backup":
				if op.BackupPath == "" {
					return fmt.Errorf("missing backup path for %s", op.Target)
				}
				if _, berr := os.Lstat(op.BackupPath); berr != nil {
					return fmt.Errorf("backup missing for %s (%s): %w", op.Target, op.BackupPath, berr)
				}
				if fi, err := os.Lstat(op.Target); err == nil {
					if fi.Mode()&os.ModeSymlink != 0 {
						driftMsg, derr := checkSymlinkUndoDrift(ctx, op)
						if derr != nil {
							return derr
						}
						if driftMsg != "" {
							skippedDrifted = append(skippedDrifted, driftMsg)
							skippedOps = append(skippedOps, op)
							remainingEnd = i
							continue
						}
						if err := os.Remove(op.Target); err != nil {
							return err
						}
					} else if op.PostContentSHA256 == "" || fi.Mode().IsDir() {
						return fmt.Errorf("cannot restore backup to %s: target is no longer a symlink", op.Target)
					} else {
						if err := os.Remove(op.Target); err != nil {
							return err
						}
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if err := os.Rename(op.BackupPath, op.Target); err != nil {
					return fmt.Errorf("restore backup %s -> %s: %w", op.BackupPath, op.Target, err)
				}
				delete(ledger.ManagedLinks, op.Target)

			default:
				return fmt.Errorf("unknown undo op kind %q", op.Kind)
			}
			undoneCount++
			remainingEnd = i
		}

		if undoneCount > 0 || len(skippedOps) == 0 {
			ledger.ActiveTheme = tx.PrevTheme
			if tx.PrevPreset != "" {
				ledger.ActivePreset = tx.PrevPreset
			}
		}
		if len(skippedOps) == 0 {
			ledger.UndoLog = ledger.UndoLog[:len(ledger.UndoLog)-1]
		} else {
			kept := make([]app.UndoOp, 0, len(skippedOps))
			for j := len(skippedOps) - 1; j >= 0; j-- {
				kept = append(kept, skippedOps[j])
			}
			tx.Ops = kept
			tx.PrevTheme = ledger.ActiveTheme
			tx.PrevPreset = ledger.ActivePreset
			ledger.UndoLog[len(ledger.UndoLog)-1] = tx
		}
		ledger.RunJournal = append(ledger.RunJournal, app.JournalEntry{
			Timestamp:    time.Now().UTC(),
			Command:      "undo",
			Summary:      fmt.Sprintf("undid %s: %s", tx.ID, tx.Summary),
			ActionsCount: undoneCount,
			Warnings:     skippedDrifted,
		})
		completed = true
		if err := app.SaveLedger(ctx.WSBaseDir(), ledger); err != nil {
			return err
		}

		if len(undoneOutputs) > 0 && ctx.RepoRoot != "" {
			if entries, rerr := config.LoadThemeRegistry(ctx.RepoRoot); rerr == nil {
				byOut := map[string]config.RegistryEntry{}
				for _, e := range entries {
					byOut[filepath.ToSlash(filepath.Clean(e.Output))] = e
				}
				seenHook := map[string]bool{}
				for _, outRel := range undoneOutputs {
					e, ok := byOut[outRel]
					if !ok || e.PostHook == "" || seenHook[e.PostHook] {
						continue
					}
					seenHook[e.PostHook] = true
					_ = runActionCommand(ctx, Action{
						ID:       "hook:undo:" + e.App,
						Kind:     ActionHook,
						Target:   e.App,
						Command:  []string{"bash", "-c", e.PostHook},
						Metadata: map[string]string{"app": e.App, "hook": e.PostHook},
					})
				}
			}
		}

		if len(skippedDrifted) > 0 {
			outMsg = fmt.Sprintf("undid %s (%s, %d/%d ops; skipped %d drifted link(s): %s)", tx.ID, tx.Summary, undoneCount, origOpsCount, len(skippedDrifted), strings.Join(skippedDrifted, "; "))
		} else {
			outMsg = fmt.Sprintf("undid %s (%s, %d ops)", tx.ID, tx.Summary, undoneCount)
		}
		return nil
	})
	return outMsg, err
}

func checkSymlinkUndoDrift(ctx *app.Context, op app.UndoOp) (string, error) {
	if op.PostLinkTarget == "" || ctx.Allow.Replace {
		return "", nil
	}
	cur, err := os.Readlink(op.Target)
	if err != nil {
		return "", err
	}
	if cur == op.PostLinkTarget {
		return "", nil
	}
	return fmt.Sprintf("%s (points to %s, expected %s; rerun with --allow replace)", ctx.CollapseHome(op.Target), cur, op.PostLinkTarget), nil
}

func commandOnPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func runSyncProbe(ctx *app.Context, timeout time.Duration, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, args[0], args[1:]...)
	cmd.Env = app.ProbeEnv(applyActionEnv(ctx))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func planPostSyncActions(ctx *app.Context, excludes []string, seenHooks map[string]bool, batLinked, tmuxPlanned, allPostSyncHooks bool) ([]Action, []string) {
	var actions []Action
	var warnings []string

	isExcl := func(token, target string) bool {
		return len(excludes) > 0 && matchExcludeFilter(ctx, excludes, token, "", target)
	}

	if (batLinked || allPostSyncHooks) && !seenHooks["bat cache --build"] && !isExcl("bat", "") && commandOnPath("bat") {
		actions = append(actions, Action{
			ID:      "sync:bat-cache",
			Kind:    ActionHook,
			Target:  "bat",
			Summary: "post-hook bat: rebuild bat cache",
			Risk:    RiskSafe,
			Command: []string{"bat", "cache", "--build"},
			Metadata: map[string]string{
				"app":  "bat",
				"hook": "bat cache --build",
			},
		})
	}

	if !isExcl("go", "") && commandOnPath("go") {
		if out, err := runSyncProbe(ctx, 2*time.Second, "go", "telemetry"); err == nil && out != "off" {
			actions = append(actions, Action{
				ID:      "sync:go-telemetry",
				Kind:    ActionHook,
				Target:  "go",
				Summary: "turn go telemetry off",
				Risk:    RiskSafe,
				Command: []string{"go", "telemetry", "off"},
			})
		}
	}

	for idx, cfgPath := range discoverCodexConfigPaths(ctx) {
		if isExcl("codex", cfgPath) {
			continue
		}
		pending, skipReason, err := checkCodexAnalyticsPending(cfgPath)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("codex %s: %v", ctx.CollapseHome(cfgPath), err))
			continue
		}
		if skipReason != "" {
			warnings = append(warnings, skipReason)
			continue
		}
		if pending {
			actions = append(actions, Action{
				ID:      fmt.Sprintf("sync:codex-analytics:%d", idx),
				Kind:    ActionEnv,
				Target:  cfgPath,
				Summary: fmt.Sprintf("disable codex analytics in %s", ctx.CollapseHome(cfgPath)),
				Risk:    RiskSafe,
				Metadata: map[string]string{
					"backup": cfgPath + ".pre-ws",
				},
			})
		}
	}

	if awsAct, ok := planDefaultAWSCredentialsAction(ctx); ok && !isExcl("aws", awsAct.Target) {
		actions = append(actions, awsAct)
	}

	if !isExcl("tmux", "") {
		tmuxDir := filepath.Join(ctx.ConfigDir, "tmux")
		tpmDir := filepath.Join(tmuxDir, "plugins", "tpm")
		_, statErr := os.Stat(tmuxDir)
		if statErr == nil || (allPostSyncHooks && tmuxPlanned) {
			if _, terr := os.Stat(tpmDir); errors.Is(terr, os.ErrNotExist) && commandOnPath("git") {
				actions = append(actions, Action{
					ID:            "sync:tmux-tpm",
					Kind:          ActionHook,
					Target:        tpmDir,
					Summary:       "install tmux plugin manager (tpm)",
					Risk:          RiskMedium,
					RequiresAllow: "script",
					Command:       tmuxTPMInstallCommand(tpmDir),
				})
			}
		}
	}

	if !isExcl("gh-dash", "") && commandOnPath("gh") && !ghExtensionInstalled(ctx, "dlvhdr/gh-dash") {
		actions = append(actions, Action{
			ID:            "sync:gh-dash",
			Kind:          ActionHook,
			Target:        "gh-dash",
			Summary:       "install gh-dash extension",
			Risk:          RiskSafe,
			RequiresAllow: "script",
			Command:       []string{"gh", "extension", "install", "dlvhdr/gh-dash"},
		})
	}

	return actions, warnings
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

var (
	codexHomeTemplateRe       = regexp.MustCompile(`\{\{\s*env\.HOME\s*\}\}`)
	codexConfigRootTemplateRe = regexp.MustCompile(`\{\{\s*config_root\s*\}\}`)
)

func discoverCodexConfigPaths(ctx *app.Context) []string {
	if ctx == nil || ctx.HomeDir == "" {
		return nil
	}
	var dirs []string
	if ch := strings.TrimSpace(ctx.LookupEnv("CODEX_HOME")); ch != "" {
		dirs = append(dirs, ctx.ExpandHome(ch))
	}
	dirs = append(dirs, filepath.Join(ctx.HomeDir, ".codex"))
	if matches, err := filepath.Glob(filepath.Join(ctx.HomeDir, ".codex-*")); err == nil {
		dirs = append(dirs, matches...)
	}
	dirs = append(dirs, discoverZoneCodexHomes(ctx)...)
	seen := map[string]bool{}
	var paths []string
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		cfg := filepath.Clean(filepath.Join(d, "config.toml"))
		if seen[cfg] {
			continue
		}
		seen[cfg] = true
		fi, err := os.Lstat(cfg)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			continue
		}
		paths = append(paths, cfg)
	}
	sort.Strings(paths)
	return paths
}

func discoverZoneCodexHomes(ctx *app.Context) []string {
	if ctx == nil || ctx.HomeDir == "" {
		return nil
	}
	seenZone := map[string]bool{}
	var zoneNames []string
	addZone := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || seenZone[name] {
			return
		}
		seenZone[name] = true
		zoneNames = append(zoneNames, name)
	}

	if raw := ctx.LookupEnv("WS_ZONES"); strings.TrimSpace(raw) != "" {
		for _, parts := range app.ParsePipeLines(raw) {
			if len(parts) > 0 {
				addZone(parts[0])
			}
		}
	}
	if ctx.RepoRoot != "" {
		if seeds, err := filepath.Glob(filepath.Join(ctx.RepoRoot, "config", "identity", "*.mise.toml")); err == nil {
			for _, seed := range seeds {
				base := filepath.Base(seed)
				addZone(strings.TrimSuffix(base, ".mise.toml"))
			}
		}
	}

	var dirs []string
	for _, name := range zoneNames {
		zroot := filepath.Join(ctx.HomeDir, name)
		ch := readZoneCodexHome(ctx, zroot)
		if ch != "" {
			dirs = append(dirs, ch)
		}
	}
	return dirs
}

func readZoneCodexHome(ctx *app.Context, zoneRoot string) string {
	val := ""
	for _, fname := range []string{"mise.toml", "mise.local.toml"} {
		p := filepath.Join(zoneRoot, fname)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			Env map[string]any `toml:"env"`
		}
		if err := toml.Unmarshal(raw, &doc); err != nil {
			continue
		}
		v, ok := doc.Env["CODEX_HOME"]
		if !ok {
			continue
		}
		var s string
		switch tv := v.(type) {
		case string:
			s = tv
		case map[string]any:
			if mv, ok := tv["value"].(string); ok {
				s = mv
			}
		}
		s = strings.TrimSpace(s)
		if s != "" {
			val = s
		}
	}
	if val == "" {
		return ""
	}
	expanded := codexHomeTemplateRe.ReplaceAllLiteralString(val, ctx.HomeDir)
	expanded = codexConfigRootTemplateRe.ReplaceAllLiteralString(expanded, zoneRoot)
	expanded = ctx.ExpandHome(expanded)
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(zoneRoot, expanded)
	}
	return filepath.Clean(expanded)
}

func checkCodexAnalyticsPending(cfgPath string) (pending bool, skipReason string, err error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, "", err
	}
	text := string(raw)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "analytics") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "analytics"))
			if strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "=") {
				return false, fmt.Sprintf("codex %s uses inline/dotted analytics key; left unchanged", cfgPath), nil
			}
		}
	}
	rewritten := rewriteCodexAnalyticsTOML(text)
	if rewritten == text {
		return false, "", nil
	}
	var doc map[string]any
	if err := toml.Unmarshal([]byte(rewritten), &doc); err != nil {
		return false, fmt.Sprintf("codex %s did not validate after analytics edit; left unchanged", cfgPath), nil
	}
	analyticsSec, ok := doc["analytics"].(map[string]any)
	if !ok {
		return false, fmt.Sprintf("codex %s missing [analytics] table after rewrite", cfgPath), nil
	}
	enabled, ok := analyticsSec["enabled"].(bool)
	if !ok || enabled {
		return false, fmt.Sprintf("codex %s analytics.enabled did not resolve to false", cfgPath), nil
	}
	return true, "", nil
}

func rewriteCodexAnalyticsTOML(src string) string {
	norm := strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(strings.TrimSuffix(norm, "\n"), "\n")
	if norm == "" {
		lines = nil
	}
	var out []string
	inAnalytics := false
	seenAnalytics := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			sec := trimmed
			if hash := strings.IndexByte(sec, '#'); hash >= 0 {
				sec = strings.TrimSpace(sec[:hash])
			}
			inAnalytics = sec == "[analytics]"
			out = append(out, line)
			if inAnalytics {
				out = append(out, "enabled = false")
				seenAnalytics = true
			}
			continue
		}
		if inAnalytics {
			if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == "enabled" {
				continue
			}
		}
		out = append(out, line)
	}
	if !seenAnalytics {
		if len(out) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, "[analytics]", "enabled = false")
	}
	return strings.Join(out, "\n") + "\n"
}

func applyCodexAnalyticsOff(cfgPath string, tx *app.UndoTx) (bool, error) {
	fi, err := os.Lstat(cfgPath)
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return false, fmt.Errorf("codex config %s must be a regular file", cfgPath)
	}
	pending, _, err := checkCodexAnalyticsPending(cfgPath)
	if err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	origBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		return false, err
	}
	rewritten := rewriteCodexAnalyticsTOML(string(origBytes))
	if bytes.Equal(origBytes, []byte(rewritten)) {
		return false, nil
	}
	backupPath := cfgPath + ".pre-ws"
	if _, berr := os.Lstat(backupPath); errors.Is(berr, os.ErrNotExist) {
		if err := app.WriteAtomicFile(backupPath, origBytes, fi.Mode().Perm()); err != nil {
			return false, err
		}
	}
	if err := writeFileWithUndo(cfgPath, []byte(rewritten), fi.Mode().Perm(), tx); err != nil {
		return false, err
	}
	if tx != nil && len(tx.Ops) > 0 {
		tx.Ops[len(tx.Ops)-1].BackupPath = backupPath
	}
	return true, nil
}

func planDefaultAWSCredentialsAction(ctx *app.Context) (Action, bool) {
	if ctx == nil || ctx.HomeDir == "" {
		return Action{}, false
	}
	awsZone := strings.TrimSpace(ctx.LookupEnv("WS_AWS_DEFAULT_ZONE"))
	if awsZone == "" || strings.ContainsAny(awsZone, `/\`) || strings.HasPrefix(awsZone, ".") {
		return Action{}, false
	}
	awsDefault := filepath.Join(ctx.HomeDir, ".aws", "credentials")
	if _, err := os.Lstat(awsDefault); !errors.Is(err, os.ErrNotExist) {
		return Action{}, false
	}
	zoneCreds := filepath.Join(ctx.HomeDir, ".aws", awsZone+"-credentials")
	fi, err := os.Stat(zoneCreds)
	if err != nil || fi.IsDir() {
		return Action{}, false
	}
	relTarget := awsZone + "-credentials"
	return Action{
		ID:      "sync:aws-default-creds",
		Kind:    ActionSymlink,
		Target:  awsDefault,
		Source:  relTarget,
		Summary: fmt.Sprintf("link %s -> %s", ctx.CollapseHome(awsDefault), relTarget),
		Risk:    RiskSafe,
		Metadata: map[string]string{
			"relative": "true",
		},
	}, true
}

func applyRelativeSymlink(relTarget, targetAbs string, allowReplace bool, tx *app.UndoTx) (string, error) {
	fi, err := os.Lstat(targetAbs)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if cur, rerr := os.Readlink(targetAbs); rerr == nil && cur == relTarget {
				return "ok", nil
			}
			if !allowReplace {
				return "", fmt.Errorf("refusing to overwrite symlink %s without --allow replace", targetAbs)
			}
			prev, _ := os.Readlink(targetAbs)
			if err := atomicSymlink(relTarget, targetAbs); err != nil {
				return "", err
			}
			if tx != nil {
				tx.Ops = append(tx.Ops, app.UndoOp{
					Kind:           "replace_symlink",
					Target:         targetAbs,
					PrevExists:     true,
					PrevIsSymlink:  true,
					PrevLinkTarget: prev,
					PostLinkTarget: relTarget,
				})
			}
			return "relinked", nil
		}
		if !allowReplace {
			return "", fmt.Errorf("refusing to overwrite existing %s without --allow replace", targetAbs)
		}
		return "", fmt.Errorf("refusing to replace existing regular file %s", targetAbs)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(targetAbs), 0o700); err != nil {
		return "", err
	}
	if err := os.Symlink(relTarget, targetAbs); err != nil {
		return "", err
	}
	if tx != nil {
		tx.Ops = append(tx.Ops, app.UndoOp{
			Kind:           "create_symlink",
			Target:         targetAbs,
			PrevExists:     false,
			PostLinkTarget: relTarget,
		})
	}
	return "created", nil
}

func ghExtensionInstalled(ctx *app.Context, slug string) bool {
	parts := strings.Split(strings.TrimSpace(slug), "/")
	if len(parts) == 2 && ctx != nil && ctx.DataDir != "" {
		extDir := filepath.Join(ctx.DataDir, "gh", "extensions", parts[1])
		if _, err := os.Stat(extDir); err == nil {
			return true
		}
	}
	if commandOnPath("gh") {
		if out, err := runSyncProbe(ctx, 2*time.Second, "gh", "extension", "list"); err == nil {
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, slug) {
					return true
				}
			}
		}
	}
	return false
}

func repoRelPath(repoRoot, absPath string) (string, bool) {
	if repoRoot == "" {
		return "", false
	}
	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}
