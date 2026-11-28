package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

type Severity string

const (
	SeverityOK    Severity = "ok"
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

type Finding struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	FixCLI   string   `json:"fixCli,omitempty"`
	FixPlan  *Plan    `json:"fixPlan,omitempty"`
}

type DoctorReport struct {
	Host           app.HostInfo `json:"host"`
	Preset         string       `json:"preset"`
	Theme          string       `json:"theme"`
	LinksOK        int          `json:"linksOk"`
	LinksTotal     int          `json:"linksTotal"`
	TemplatesOK    int          `json:"templatesOk"`
	TemplatesTotal int          `json:"templatesTotal"`
	Findings       []Finding    `json:"findings"`
}

func RunDoctor(ctx *app.Context) (*DoctorReport, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if strings.TrimSpace(ctx.RepoRoot) == "" {
		return nil, fmt.Errorf("workstation repository root is required")
	}
	ledger, err := app.LoadLedger(ctx.WSBaseDir())
	if err != nil {
		return nil, err
	}

	presetID := ctx.EffectivePreset()
	themeID := ledger.ActiveTheme
	report := &DoctorReport{
		Host:     ctx.Host,
		Preset:   presetID,
		Theme:    themeID,
		Findings: []Finding{},
	}

	if ctx.MiseLocalErr != nil {
		report.Findings = append(report.Findings, Finding{
			ID:       "config:mise-local-error",
			Severity: SeverityError,
			Category: "config",
			Title:    "Cannot parse mise.local.toml",
			Detail:   ctx.MiseLocalErr.Error(),
		})
	}

	resolvedPreset := checkPreset(ctx, presetID, report)
	effectiveTheme := checkTheme(ctx, themeID, report)
	var managedLinks []config.LinkEntry
	if resolvedPreset != nil {
		managedLinks = checkLinks(ctx, resolvedPreset, report)
	}
	if effectiveTheme != "" {
		checkDoctorTemplates(ctx, effectiveTheme, report)
	}
	checkPathAndTools(ctx, resolvedPreset, report)
	checkLeftoverBackups(ctx, managedLinks, report)
	checkZones(ctx, report)
	checkSyncHooks(ctx, report)
	checkRepoContracts(ctx, report)

	return report, nil
}

func checkRepoContracts(ctx *app.Context, report *DoctorReport) {
	seen := map[string]bool{}
	for _, f := range report.Findings {
		seen[f.ID] = true
	}
	for _, issue := range config.ValidateRepoContracts(ctx.RepoRoot) {
		if issue.ID == "contract:presets" && seen["preset:load-error"] {
			continue
		}
		if issue.ID == "contract:tools" && seen["tools:load-error"] {
			continue
		}
		report.Findings = append(report.Findings, Finding{
			ID:       issue.ID,
			Severity: SeverityError,
			Category: "contracts",
			Title:    issue.Title,
			Detail:   issue.Detail,
		})
	}
}

func checkPreset(ctx *app.Context, presetID string, report *DoctorReport) *config.ResolvedPreset {
	presets, err := config.LoadPresets(ctx.RepoRoot)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			ID:       "preset:load-error",
			Severity: SeverityError,
			Category: "preset",
			Title:    "Cannot load presets.toml",
			Detail:   err.Error(),
		})
		return nil
	}
	if presetID == "" {
		report.Findings = append(report.Findings, Finding{
			ID:       "preset:unset",
			Severity: SeverityWarn,
			Category: "preset",
			Title:    "No active preset configured",
			FixCLI:   "ws apply --preset base",
		})
		return nil
	}
	rawPreset, ok := presets[presetID]
	if !ok {
		report.Findings = append(report.Findings, Finding{
			ID:       "preset:invalid",
			Severity: SeverityError,
			Category: "preset",
			Title:    fmt.Sprintf("Active preset %q does not exist in presets.toml", presetID),
			Detail:   fmt.Sprintf("available: %s", strings.Join(config.ListPresetIDs(presets), ", ")),
			FixCLI:   fmt.Sprintf("ws apply --preset %s", ctx.Host.DefaultPreset),
		})
		return nil
	}
	if !rawPreset.MatchesHost(ctx.Host) {
		report.Findings = append(report.Findings, Finding{
			ID:       "preset:host-mismatch",
			Severity: SeverityInfo,
			Category: "preset",
			Title:    fmt.Sprintf("Active preset %q does not match detected host (%s/%s)", presetID, ctx.Host.OS, ctx.Host.Distro),
			FixCLI:   fmt.Sprintf("ws apply --preset %s", ctx.Host.DefaultPreset),
		})
	}
	resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, presetID)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			ID:       "preset:resolve-error",
			Severity: SeverityError,
			Category: "preset",
			Title:    fmt.Sprintf("Failed to resolve preset %q", presetID),
			Detail:   err.Error(),
		})
		return nil
	}
	return resolved
}

func checkTheme(ctx *app.Context, themeID string, report *DoctorReport) string {
	if themeID == "" {
		defaultID := "gruvbox-dark"
		if _, err := config.LoadPalette(ctx.RepoRoot, defaultID); err != nil {
			if themes, lerr := config.ListThemes(ctx.RepoRoot); lerr == nil && len(themes) > 0 {
				defaultID = themes[0]
			} else {
				report.Findings = append(report.Findings, Finding{
					ID:       "theme:none",
					Severity: SeverityError,
					Category: "theme",
					Title:    "No valid themes found under themes/",
					Detail:   err.Error(),
				})
				return ""
			}
		}
		report.Findings = append(report.Findings, Finding{
			ID:       "theme:unset",
			Severity: SeverityInfo,
			Category: "theme",
			Title:    "No active theme recorded in ledger",
			Detail:   fmt.Sprintf("checking templates against default %s", defaultID),
			FixCLI:   fmt.Sprintf("ws apply --theme %s", defaultID),
		})
		report.Theme = defaultID
		return defaultID
	}

	if _, err := config.LoadPalette(ctx.RepoRoot, themeID); err != nil {
		available, _ := config.ListThemes(ctx.RepoRoot)
		report.Findings = append(report.Findings, Finding{
			ID:       "theme:invalid",
			Severity: SeverityError,
			Category: "theme",
			Title:    fmt.Sprintf("Active theme %q is invalid or missing", themeID),
			Detail:   fmt.Sprintf("%v (available: %s)", err, strings.Join(available, ", ")),
			FixCLI:   "ws apply --theme gruvbox-dark",
		})
		return ""
	}
	return themeID
}

func checkLinks(ctx *app.Context, preset *config.ResolvedPreset, report *DoctorReport) []config.LinkEntry {
	entries, err := config.LoadLinksForPreset(ctx.RepoRoot, preset)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			ID:       "links:load-error",
			Severity: SeverityError,
			Category: "links",
			Title:    "Failed to load link manifests",
			Detail:   err.Error(),
		})
		return nil
	}
	if excludes := parseExcludeFilters(ctx); len(excludes) > 0 {
		_, entries, _ = applyExcludeFilters(ctx, excludes, nil, entries, nil)
	}
	regOutputs := map[string]bool{}
	if reg, rerr := config.LoadThemeRegistry(ctx.RepoRoot); rerr == nil {
		for _, r := range reg {
			regOutputs[filepath.ToSlash(filepath.Clean(r.Output))] = true
		}
	}

	report.LinksTotal = len(entries)
	for _, entry := range entries {
		srcAbs := filepath.Join(ctx.RepoRoot, entry.SourceRel)
		targetAbs := ctx.ExpandHome(entry.TargetRaw)
		disp := ctx.CollapseHome(targetAbs)

		if _, err := os.Lstat(srcAbs); err != nil {
			if errors.Is(err, os.ErrNotExist) && isPlannedOutput(regOutputs, entry.SourceRel) {
				report.Findings = append(report.Findings, Finding{
					ID:       "link:unrendered-source:" + entry.SourceRel,
					Severity: SeverityWarn,
					Category: "links",
					Title:    fmt.Sprintf("Generated link source not rendered yet: %s", entry.SourceRel),
					Detail:   fmt.Sprintf("referenced by %s:%d; render templates to create it", entry.Manifest, entry.LineNumber),
					FixCLI:   "ws apply",
				})
				continue
			}
			report.Findings = append(report.Findings, Finding{
				ID:       "link:missing-source:" + entry.SourceRel,
				Severity: SeverityError,
				Category: "links",
				Title:    fmt.Sprintf("Link source missing in repo: %s", entry.SourceRel),
				Detail:   fmt.Sprintf("referenced by %s:%d", entry.Manifest, entry.LineNumber),
			})
			continue
		}

		fi, err := os.Lstat(targetAbs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fix := singleLinkPlan(ctx, preset.ID, entry, srcAbs, targetAbs, disp, "created", "")
				report.Findings = append(report.Findings, Finding{
					ID:       "link:missing:" + entry.TargetRaw,
					Severity: SeverityWarn,
					Category: "links",
					Title:    fmt.Sprintf("Missing symlink %s", disp),
					Detail:   fmt.Sprintf("expected symlink -> %s", entry.SourceRel),
					FixCLI:   "ws apply",
					FixPlan:  fix,
				})
				continue
			}
			report.Findings = append(report.Findings, Finding{
				ID:       "link:error:" + entry.TargetRaw,
				Severity: SeverityError,
				Category: "links",
				Title:    fmt.Sprintf("Cannot inspect %s", disp),
				Detail:   err.Error(),
			})
			continue
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			cur, rerr := os.Readlink(targetAbs)
			if rerr != nil {
				report.Findings = append(report.Findings, Finding{
					ID:       "link:error:" + entry.TargetRaw,
					Severity: SeverityError,
					Category: "links",
					Title:    fmt.Sprintf("Cannot readlink %s", disp),
					Detail:   rerr.Error(),
				})
				continue
			}
			if _, statErr := os.Stat(targetAbs); statErr != nil {
				if cur == srcAbs {
					report.Findings = append(report.Findings, Finding{
						ID:       "link:broken-source:" + entry.TargetRaw,
						Severity: SeverityError,
						Category: "links",
						Title:    fmt.Sprintf("Link %s points to broken repo source", disp),
						Detail:   fmt.Sprintf("%s -> %s (%v)", disp, srcAbs, statErr),
					})
					continue
				}
				fix := singleLinkPlan(ctx, preset.ID, entry, srcAbs, targetAbs, disp, "relinked", "")
				report.Findings = append(report.Findings, Finding{
					ID:       "link:broken:" + entry.TargetRaw,
					Severity: SeverityError,
					Category: "links",
					Title:    fmt.Sprintf("Broken symlink %s", disp),
					Detail:   fmt.Sprintf("dangling target %s (expected %s)", cur, srcAbs),
					FixCLI:   "ws apply",
					FixPlan:  fix,
				})
				continue
			}
			if cur != srcAbs {
				fix := singleLinkPlan(ctx, preset.ID, entry, srcAbs, targetAbs, disp, "relinked", "")
				report.Findings = append(report.Findings, Finding{
					ID:       "link:drifted:" + entry.TargetRaw,
					Severity: SeverityWarn,
					Category: "links",
					Title:    fmt.Sprintf("Drifted symlink %s", disp),
					Detail:   fmt.Sprintf("points to %s instead of %s", cur, srcAbs),
					FixCLI:   "ws apply",
					FixPlan:  fix,
				})
				continue
			}
			report.LinksOK++
			continue
		}

		fix := singleLinkPlan(ctx, preset.ID, entry, srcAbs, targetAbs, disp, "replaced", "replace")
		report.Findings = append(report.Findings, Finding{
			ID:       "link:conflict:" + entry.TargetRaw,
			Severity: SeverityWarn,
			Category: "links",
			Title:    fmt.Sprintf("Real file/directory blocks symlink %s", disp),
			Detail:   fmt.Sprintf("would back up to %s.pre-ws and link -> %s", disp, entry.SourceRel),
			FixCLI:   "ws apply --allow replace",
			FixPlan:  fix,
		})
	}
	return entries
}

func singleLinkPlan(ctx *app.Context, presetID string, entry config.LinkEntry, srcAbs, targetAbs, disp, state, reqAllow string) *Plan {
	risk := RiskSafe
	if reqAllow != "" {
		risk = RiskMedium
	}
	return &Plan{
		Verb:      "apply",
		Host:      FormatPlanHost(ctx),
		Title:     fmt.Sprintf("Fix symlink %s", disp),
		Preset:    presetID,
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]string{"partial": "true"},
		Actions: []Action{
			{
				ID:            "link:" + entry.TargetRaw,
				Kind:          ActionSymlink,
				Target:        targetAbs,
				Source:        srcAbs,
				Summary:       fmt.Sprintf("%s %s -> %s", state, disp, entry.SourceRel),
				Risk:          risk,
				RequiresAllow: reqAllow,
				Metadata: map[string]string{
					"state":     state,
					"sourceRel": entry.SourceRel,
					"targetRaw": entry.TargetRaw,
				},
			},
		},
	}
}

func checkDoctorTemplates(ctx *app.Context, themeID string, report *DoctorReport) {
	statuses, err := CheckTemplates(ctx, themeID)
	if err != nil {
		report.Findings = append(report.Findings, Finding{
			ID:       "templates:check-error",
			Severity: SeverityError,
			Category: "templates",
			Title:    "Failed to evaluate theme templates",
			Detail:   err.Error(),
		})
		return
	}

	pal, _ := config.LoadPalette(ctx.RepoRoot, themeID)
	report.TemplatesTotal = len(statuses)
	for _, st := range statuses {
		if st.State == "ok" {
			report.TemplatesOK++
			continue
		}
		entry := config.RegistryEntry{
			Template: st.Template,
			Output:   st.Output,
			PostHook: st.PostHook,
			App:      st.App,
		}
		var fixPlan *Plan
		if act, changed, perr := planRenderEntry(ctx.RepoRoot, entry, themeID, pal); perr == nil && changed {
			actions := []Action{act}
			if entry.PostHook != "" {
				actions = append(actions, Action{
					ID:      "hook:" + entry.App,
					Kind:    ActionHook,
					Target:  entry.App,
					Summary: fmt.Sprintf("post-hook %s: %s", entry.App, entry.PostHook),
					Risk:    RiskSafe,
					Command: []string{"bash", "-c", entry.PostHook},
					Metadata: map[string]string{
						"app":  entry.App,
						"hook": entry.PostHook,
					},
				})
			}
			fixPlan = &Plan{
				Verb:      "apply",
				Host:      FormatPlanHost(ctx),
				Title:     fmt.Sprintf("Render %s (%s)", entry.Output, themeID),
				Theme:     themeID,
				CreatedAt: time.Now().UTC(),
				Metadata:  map[string]string{"partial": "true", "only": entry.App},
				Actions:   actions,
			}
		}

		title := fmt.Sprintf("Template output %s is %s", st.Output, st.State)
		if st.State == "modified" {
			title = fmt.Sprintf("Hand-edited generated template output %s", st.Output)
		}
		report.Findings = append(report.Findings, Finding{
			ID:       fmt.Sprintf("template:%s:%s", st.State, st.App),
			Severity: SeverityWarn,
			Category: "templates",
			Title:    title,
			Detail:   st.Diff,
			FixCLI:   fmt.Sprintf("ws apply --only %s", st.App),
			FixPlan:  fixPlan,
		})
	}
}

func checkPathAndTools(ctx *app.Context, preset *config.ResolvedPreset, report *DoctorReport) {
	catalog, err := config.LoadTools(ctx.RepoRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			report.Findings = append(report.Findings, Finding{
				ID:       "tools:load-error",
				Severity: SeverityWarn,
				Category: "path",
				Title:    "Cannot parse tools.toml",
				Detail:   err.Error(),
			})
		}
		return
	}

	pathDirs := filepath.SplitList(ctx.LookupEnv("PATH"))

	expected := map[string]config.ToolSpec{}
	if preset != nil {
		for _, pkgName := range preset.PackageNames {
			if spec, ok := catalog[pkgName]; ok {
				expected[pkgName] = spec
			}
		}
	}

	for id, spec := range catalog {
		if spec.Updates == "self" && len(spec.Conflicts) > 0 {
			expected[id] = spec
		}
	}

	ids := make([]string, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		spec := expected[id]
		if len(spec.Install) == 1 && spec.Install[0].Via == "gh-ext" {
			extSlug := spec.Install[0].Name
			if preset != nil && containsString(preset.PackageNames, id) && commandOnPath("gh") && !ghExtensionInstalled(ctx, extSlug) {
				report.Findings = append(report.Findings, Finding{
					ID:       "path:missing:" + id,
					Severity: SeverityInfo,
					Category: "path",
					Title:    fmt.Sprintf("Expected gh extension %q not installed", extSlug),
					Detail:   fmt.Sprintf("required by preset %s (%s)", preset.ID, id),
					FixCLI:   "ws pkg sync",
				})
			}
			continue
		}
		if spec.Bin == "" {
			continue
		}
		hits := findAllInPath(spec.Bin, pathDirs)
		if len(hits) == 0 {

			if preset != nil && containsString(preset.PackageNames, id) {
				report.Findings = append(report.Findings, Finding{
					ID:       "path:missing:" + spec.Bin,
					Severity: SeverityInfo,
					Category: "path",
					Title:    fmt.Sprintf("Expected binary %q not found on PATH", spec.Bin),
					Detail:   fmt.Sprintf("required by preset %s (%s)", preset.ID, id),
					FixCLI:   "ws bootstrap",
				})
			}
			continue
		}

		if spec.Updates == "self" && hasBrewConflict(spec.Conflicts) {
			var brewHits []string
			for _, h := range hits {
				if isBrewManagedPath(h) {
					brewHits = append(brewHits, h)
				}
			}
			if len(brewHits) > 0 {
				report.Findings = append(report.Findings, Finding{
					ID:       "path:self-updater-conflict:" + spec.Bin,
					Severity: SeverityWarn,
					Category: "path",
					Title:    fmt.Sprintf("Self-updater conflict: %q found in Homebrew prefix", spec.Bin),
					Detail:   fmt.Sprintf("native installer owns %s updates, but found %s (conflicts: %s)", id, strings.Join(brewHits, ", "), strings.Join(spec.Conflicts, ", ")),
					FixCLI:   brewConflictFixCLI(brewHits, spec.Conflicts, id),
				})
			}
		}

		nonShimHits := filterOutMiseShims(hits)
		if len(nonShimHits) > 1 {
			report.Findings = append(report.Findings, Finding{
				ID:       "path:shadow:" + spec.Bin,
				Severity: SeverityWarn,
				Category: "path",
				Title:    fmt.Sprintf("PATH shadowing for %q (%d distinct binaries)", spec.Bin, len(nonShimHits)),
				Detail:   fmt.Sprintf("active: %s; shadowed: %s", nonShimHits[0], strings.Join(nonShimHits[1:], ", ")),
			})
		}
	}
}

func filterOutMiseShims(hits []string) []string {
	var out []string
	for _, h := range hits {
		sl := filepath.ToSlash(h)
		if strings.Contains(sl, "/mise/shims/") {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(h); err == nil && filepath.Base(resolved) == "mise" {
			continue
		}
		out = append(out, h)
	}
	return out
}

func brewConflictFixCLI(brewHits, conflicts []string, fallbackID string) string {
	for _, h := range brewHits {
		check := h
		if resolved, err := filepath.EvalSymlinks(h); err == nil {
			check = resolved
		}
		sl := filepath.ToSlash(check)
		if _, rest, ok := strings.Cut(sl, "/Caskroom/"); ok {
			if name, _, _ := strings.Cut(rest, "/"); name != "" {
				return "brew uninstall --cask " + name
			}
		}
		if _, rest, ok := strings.Cut(sl, "/Cellar/"); ok {
			if name, _, _ := strings.Cut(rest, "/"); name != "" {
				return "brew uninstall " + name
			}
		}
	}
	for _, c := range conflicts {
		if name, ok := strings.CutPrefix(c, "cask:"); ok {
			return "brew uninstall --cask " + name
		}
		if name, ok := strings.CutPrefix(c, "brew:"); ok {
			return "brew uninstall " + name
		}
	}
	return "brew uninstall " + fallbackID
}

func findAllInPath(bin string, dirs []string) []string {
	var hits []string
	seenReal := map[string]bool{}
	seenDirs := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		cleanDir := filepath.Clean(dir)
		if seenDirs[cleanDir] {
			continue
		}
		seenDirs[cleanDir] = true

		cand := filepath.Join(cleanDir, bin)
		fi, err := os.Stat(cand)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		realPath := cand
		if resolved, rerr := filepath.EvalSymlinks(cand); rerr == nil {
			realPath = resolved
		}
		if seenReal[realPath] {
			continue
		}
		seenReal[realPath] = true
		hits = append(hits, cand)
	}
	return hits
}

func hasBrewConflict(conflicts []string) bool {
	for _, c := range conflicts {
		if strings.HasPrefix(c, "cask:") || strings.HasPrefix(c, "brew:") {
			return true
		}
	}
	return false
}

func isBrewManagedPath(p string) bool {
	sl := filepath.ToSlash(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		rs := filepath.ToSlash(resolved)
		if strings.Contains(rs, "/homebrew/") || strings.Contains(rs, "/Homebrew/") || strings.Contains(rs, "/Caskroom/") || strings.Contains(rs, "/Cellar/") || strings.Contains(rs, "/linuxbrew/") {
			return true
		}
	}
	return strings.Contains(sl, "/homebrew/") || strings.Contains(sl, "/Homebrew/") || strings.Contains(sl, "/Caskroom/") || strings.Contains(sl, "/Cellar/") || strings.Contains(sl, "/linuxbrew/")
}

func containsString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func checkLeftoverBackups(ctx *app.Context, managedLinks []config.LinkEntry, report *DoctorReport) {
	found := map[string]bool{}

	for _, entry := range managedLinks {
		backup := ctx.ExpandHome(entry.TargetRaw) + ".pre-ws"
		if _, err := os.Lstat(backup); err == nil {
			found[filepath.Clean(backup)] = true
		}
	}

	scanDirs := []string{ctx.HomeDir, ctx.ConfigDir}
	if ctx.HomeDir != "" {
		if codexDirs, gerr := filepath.Glob(filepath.Join(ctx.HomeDir, ".codex*")); gerr == nil {
			scanDirs = append(scanDirs, codexDirs...)
		}
	}
	for _, dir := range scanDirs {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".pre-ws") {
				found[filepath.Clean(filepath.Join(dir, e.Name()))] = true
			}
		}
	}

	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		disp := ctx.CollapseHome(p)
		report.Findings = append(report.Findings, Finding{
			ID:       "backup:" + disp,
			Severity: SeverityInfo,
			Category: "backups",
			Title:    fmt.Sprintf("Leftover backup %s", disp),
			Detail:   "created during a previous file replacement; safe to clean once verified",
			FixCLI:   "ws clean",
		})
	}
}

func checkZones(ctx *app.Context, report *DoctorReport) {
	raw := ctx.LookupEnv("WS_ZONES")
	if strings.TrimSpace(raw) == "" {
		return
	}
	rows := app.ParsePipeLines(raw)
	for _, row := range rows {
		if len(row) < 3 || row[0] == "" || row[1] == "" || row[2] == "" {
			report.Findings = append(report.Findings, Finding{
				ID:       "zone:malformed:" + strings.Join(row, "|"),
				Severity: SeverityError,
				Category: "zones",
				Title:    "Malformed WS_ZONES entry",
				Detail:   "expected name|git_name|git_email[|signingkey[|aws_profile]]",
			})
			continue
		}
		name := row[0]
		if strings.Contains(name, "/") || strings.HasPrefix(name, ".") {
			report.Findings = append(report.Findings, Finding{
				ID:       "zone:invalid-name:" + name,
				Severity: SeverityError,
				Category: "zones",
				Title:    fmt.Sprintf("Invalid identity zone name %q", name),
				Detail:   "zone name must be a single directory name under $HOME",
			})
			continue
		}

		miseToml := filepath.Join(ctx.HomeDir, name, "mise.toml")
		fi, err := os.Lstat(miseToml)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				report.Findings = append(report.Findings, Finding{
					ID:       "zone:missing:" + name,
					Severity: SeverityWarn,
					Category: "zones",
					Title:    fmt.Sprintf("Identity zone %q missing mise.toml", name),
					Detail:   fmt.Sprintf("expected real file at %s", ctx.CollapseHome(miseToml)),
					FixCLI:   "ws zone apply",
				})
				continue
			}
			report.Findings = append(report.Findings, Finding{
				ID:       "zone:error:" + name,
				Severity: SeverityError,
				Category: "zones",
				Title:    fmt.Sprintf("Cannot inspect zone %q mise.toml", name),
				Detail:   err.Error(),
			})
			continue
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			report.Findings = append(report.Findings, Finding{
				ID:       "zone:symlink:" + name,
				Severity: SeverityWarn,
				Category: "zones",
				Title:    fmt.Sprintf("Identity zone %q mise.toml is a symlink", name),
				Detail:   "zone mise.toml must be a real file, never a symlink into the repo",
				FixCLI:   "ws zone fix-symlinks --allow replace",
			})
		}
	}
}

func checkSyncHooks(ctx *app.Context, report *DoctorReport) {
	excludes := parseExcludeFilters(ctx)
	isExcl := func(token, target string) bool {
		return len(excludes) > 0 && matchExcludeFilter(ctx, excludes, token, "", target)
	}

	if !isExcl("go", "") && commandOnPath("go") {
		if out, err := runSyncProbe(ctx, 2*time.Second, "go", "telemetry"); err == nil && out != "off" {
			report.Findings = append(report.Findings, Finding{
				ID:       "sync:go-telemetry",
				Severity: SeverityWarn,
				Category: "sync",
				Title:    fmt.Sprintf("Go telemetry is %q (expected off)", out),
				FixCLI:   "ws apply",
				FixPlan: &Plan{
					Verb:      "apply",
					Host:      FormatPlanHost(ctx),
					Title:     "Disable Go telemetry",
					CreatedAt: time.Now().UTC(),
					Metadata:  map[string]string{"partial": "true"},
					Actions: []Action{{
						ID:      "sync:go-telemetry",
						Kind:    ActionHook,
						Target:  "go",
						Summary: "turn go telemetry off",
						Risk:    RiskSafe,
						Command: []string{"go", "telemetry", "off"},
					}},
				},
			})
		}
	}

	for idx, cfgPath := range discoverCodexConfigPaths(ctx) {
		if isExcl("codex", cfgPath) {
			continue
		}
		pending, _, err := checkCodexAnalyticsPending(cfgPath)
		if err != nil || !pending {
			continue
		}
		disp := ctx.CollapseHome(cfgPath)
		report.Findings = append(report.Findings, Finding{
			ID:       fmt.Sprintf("sync:codex-analytics:%d", idx),
			Severity: SeverityWarn,
			Category: "sync",
			Title:    fmt.Sprintf("Codex analytics enabled in %s", disp),
			FixCLI:   "ws apply",
			FixPlan: &Plan{
				Verb:      "apply",
				Host:      FormatPlanHost(ctx),
				Title:     "Disable Codex analytics",
				CreatedAt: time.Now().UTC(),
				Metadata:  map[string]string{"partial": "true"},
				Actions: []Action{{
					ID:      fmt.Sprintf("sync:codex-analytics:%d", idx),
					Kind:    ActionEnv,
					Target:  cfgPath,
					Summary: fmt.Sprintf("disable codex analytics in %s", disp),
					Risk:    RiskSafe,
				}},
			},
		})
	}

	if awsAct, ok := planDefaultAWSCredentialsAction(ctx); ok && !isExcl("aws", awsAct.Target) {
		report.Findings = append(report.Findings, Finding{
			ID:       "sync:aws-default-creds",
			Severity: SeverityInfo,
			Category: "sync",
			Title:    fmt.Sprintf("Missing default AWS credentials link (%s)", ctx.CollapseHome(awsAct.Target)),
			Detail:   awsAct.Summary,
			FixCLI:   "ws apply",
			FixPlan: &Plan{
				Verb:      "apply",
				Host:      FormatPlanHost(ctx),
				Title:     "Link default AWS credentials",
				CreatedAt: time.Now().UTC(),
				Metadata:  map[string]string{"partial": "true"},
				Actions:   []Action{awsAct},
			},
		})
	}

	if !isExcl("tmux", "") {
		tmuxDir := filepath.Join(ctx.ConfigDir, "tmux")
		tpmDir := filepath.Join(tmuxDir, "plugins", "tpm")
		if _, err := os.Stat(tmuxDir); err == nil {
			if _, terr := os.Stat(tpmDir); errors.Is(terr, os.ErrNotExist) && commandOnPath("git") {
				report.Findings = append(report.Findings, Finding{
					ID:       "sync:tmux-tpm",
					Severity: SeverityInfo,
					Category: "sync",
					Title:    "Tmux plugin manager (tpm) is not installed",
					Detail:   fmt.Sprintf("expected at %s", ctx.CollapseHome(tpmDir)),
					FixCLI:   "ws apply --allow script",
					FixPlan: &Plan{
						Verb:      "apply",
						Host:      FormatPlanHost(ctx),
						Title:     "Install tmux plugin manager (tpm)",
						CreatedAt: time.Now().UTC(),
						Metadata:  map[string]string{"partial": "true"},
						Actions: []Action{{
							ID:            "sync:tmux-tpm",
							Kind:          ActionHook,
							Target:        tpmDir,
							Summary:       "install tmux plugin manager (tpm)",
							Risk:          RiskMedium,
							RequiresAllow: "script",
							Command:       tmuxTPMInstallCommand(tpmDir),
						}},
					},
				})
			}
		}
	}
}
