package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/cleanup"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/pkgmgr"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

func newPkgCmd(opts *GlobalOpts) *cobra.Command {
	pkgCmd := &cobra.Command{
		Use:   "pkg",
		Short: "Manage preset and OS packages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPkgList(cmd, opts)
		},
	}

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Install missing preset packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			var plan *engine.Plan
			if opts.PlanPath != "" {
				plan, err = engine.LoadPlan(opts.PlanPath)
			} else {
				plan, err = pkgmgr.PlanBootstrap(ctx, ctx.EffectivePreset())
				if err == nil {
					var pkgOnly []engine.Action
					for _, act := range plan.Actions {
						if act.Kind == engine.ActionOSExec && act.ID != "apt:update" {
							continue
						}
						pkgOnly = append(pkgOnly, act)
					}
					plan.Title = fmt.Sprintf("Sync packages for %s", plan.Preset)
					plan.Actions = pkgOnly
				}
			}
			if err != nil {
				return err
			}
			if opts.SavePlanPath != "" {
				if err := plan.Save(opts.SavePlanPath); err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), plan)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Saved package plan (%d actions) to %s\n", len(plan.Actions), opts.SavePlanPath)
				return nil
			}
			if err := promptMissingAllows(cmd, ctx, plan); err != nil {
				return err
			}
			res, err := pkgmgr.ExecBootstrap(ctx, plan, nil)
			if err != nil {
				return err
			}
			skipErr := consentSkippedError(res)
			if ctx.JSON {
				if werr := writeJSON(cmd.OutOrStdout(), applyJSONOutput{Plan: plan, Result: res}); werr != nil {
					return werr
				}
				return skipErr
			}
			outW := cmd.OutOrStdout()
			roles := loadWriterRoles(ctx, outW)
			printPlanSummary(outW, roles, plan, ctx.DryRun)
			if !ctx.DryRun {
				printApplyResult(outW, roles, res, false)
				if skipErr != nil {
					ui.NewLogger(outW, roles).Warn(skipErr.Error())
				}
			}
			return skipErr
		},
	}
	addPlanFlags(syncCmd, opts)

	pkgCmd.AddCommand(
		newPkgListCmd(opts),
		&cobra.Command{
			Use:    "check",
			Short:  "Check installed and missing preset packages",
			Hidden: true,
			Args:   cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runPkgCheck(cmd, opts, false)
			},
		},
		syncCmd,
		&cobra.Command{
			Use:   "outdated",
			Short: "List outdated packages",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				outW := cmd.OutOrStdout()
				var digest *pkgmgr.UpdateDigest
				if !ctx.JSON && !wantsTSV(cmd) {
					err = ui.Spin(outW, loadWriterRoles(ctx, outW), "Checking outdated packages", func() error {
						var derr error
						digest, derr = pkgmgr.BuildUpdateDigest(ctx, false)
						return derr
					})
				} else {
					digest, err = pkgmgr.BuildUpdateDigest(ctx, false)
				}
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(outW, digest)
				}
				printUpdateDigestWith(cmd, ctx, outW, digest)
				return nil
			},
		},
		newPkgSearchCmd(opts),
		newPkgInfoCmd(opts),
		newPkgInstallCmd(opts),
		newPkgUpdateCmd(opts),
		newPkgRemoveCmd(opts),
		newPkgUntrackedCmd(opts),
		newPkgAddCmd(opts),
		newPkgUntrackCmd(opts),
	)

	return pkgCmd
}

func newPkgListCmd(opts *GlobalOpts) *cobra.Command {
	var missingOnly bool
	var untrackedOnly bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List preset, missing, or untracked packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if missingOnly && untrackedOnly {
				return &UsageError{Err: fmt.Errorf("--missing and --untracked are mutually exclusive")}
			}
			if missingOnly {
				return runPkgCheck(cmd, opts, true)
			}
			if untrackedOnly {
				return runPkgUntracked(cmd, opts, "")
			}
			return runPkgList(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&missingOnly, "missing", false, "Show missing packages for the active preset")
	cmd.Flags().BoolVar(&untrackedOnly, "untracked", false, "Show explicitly installed packages absent from pkg/*.txt")
	return cmd
}

func runPkgCheck(cmd *cobra.Command, opts *GlobalOpts, onlyMissing bool) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	presets, err := config.LoadPresets(ctx.RepoRoot)
	if err != nil {
		return err
	}
	resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, ctx.EffectivePreset())
	if err != nil {
		return err
	}
	catalog, err := config.LoadTools(ctx.RepoRoot)
	if err != nil {
		return err
	}
	tools := pkgmgr.RefineToolsForPreset(ctx, resolved, config.ResolveTools(resolved, catalog, ctx.Host), nil)
	det := newPkgInstallDetector(ctx, tools)

	type row struct {
		ID        string `json:"id"`
		Provider  string `json:"provider"`
		Installed bool   `json:"installed"`
		Status    string `json:"status"`
		Updates   string `json:"updates"`
	}
	rows := make([]row, 0, len(tools))
	installedCount := 0
	for _, t := range tools {
		inst, status := det.checkStatus(t)
		if inst {
			installedCount++
		}
		if onlyMissing && inst {
			continue
		}
		rows = append(rows, row{
			ID:        t.ID,
			Provider:  t.Spec.Via,
			Installed: inst,
			Status:    status,
			Updates:   t.Updates,
		})
	}

	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"preset":    resolved.ID,
			"installed": installedCount,
			"missing":   len(tools) - installedCount,
			"total":     len(tools),
			"tools":     rows,
		})
	}
	if onlyMissing && len(rows) == 0 && !wantsTSV(cmd) {
		fmt.Fprintf(cmd.OutOrStdout(), "Preset %s: all %d packages installed\n", resolved.ID, len(tools))
		return nil
	}
	if !wantsTSV(cmd) {
		if onlyMissing {
			fmt.Fprintf(cmd.OutOrStdout(), "Preset %s: %d missing (%d/%d installed)\n", resolved.ID, len(rows), installedCount, len(tools))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Preset %s: %d/%d installed\n", resolved.ID, installedCount, len(tools))
		}
	}
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{r.Status, r.ID, r.Provider, r.Updates})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "STATUS", MinWidth: 10},
		{Title: "ID", MinWidth: 20},
		{Title: "PROVIDER", MinWidth: 8},
		{Title: "UPDATES"},
	}, tableRows)
}

func runPkgUntracked(cmd *cobra.Command, opts *GlobalOpts, presetFlag string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	presetID := strings.TrimSpace(presetFlag)
	if presetID == "" {
		presetID = ctx.EffectivePreset()
	}
	outW := cmd.OutOrStdout()
	var items []pkgmgr.UntrackedPackage
	if !ctx.JSON && !wantsTSV(cmd) {
		err = ui.Spin(outW, loadWriterRoles(ctx, outW), fmt.Sprintf("Scanning untracked packages (%s)", presetID), func() error {
			var uerr error
			items, uerr = pkgmgr.DetectUntrackedPackages(ctx, presetID, nil)
			return uerr
		})
	} else {
		items, err = pkgmgr.DetectUntrackedPackages(ctx, presetID, nil)
	}
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), items)
	}
	if len(items) == 0 && !wantsTSV(cmd) {
		fmt.Fprintln(cmd.OutOrStdout(), "No untracked explicitly installed packages found.")
		return nil
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{it.Name, it.Provider, it.SuggestedFile})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "PACKAGE", MinWidth: 24},
		{Title: "PROVIDER", MinWidth: 10},
		{Title: "SUGGESTED FILE"},
	}, rows)
}

func newPkgUntrackedCmd(opts *GlobalOpts) *cobra.Command {
	var presetFlag string
	cmd := &cobra.Command{
		Use:    "untracked",
		Short:  "List explicitly installed untracked packages",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPkgUntracked(cmd, opts, presetFlag)
		},
	}
	cmd.Flags().StringVar(&presetFlag, "preset", "", "Preset ID to check against (defaults to active host preset)")
	return cmd
}

func newPkgAddCmd(opts *GlobalOpts) *cobra.Command {
	var (
		providerFlag string
		fileFlag     string
		noInstall    bool
	)
	cmd := &cobra.Command{
		Use:   "add <package>...",
		Short: "Install and track packages",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			tb := NewTUIBackend(ctx)
			var results []map[string]any
			for _, pkgName := range args {
				res, aerr := tb.AddAndTrackPackage(pkgName, providerFlag, fileFlag, !noInstall)
				if aerr != nil {
					return aerr
				}
				results = append(results, map[string]any{
					"package": pkgName,
					"command": res.Command,
					"output":  res.Output,
				})
				if !ctx.JSON && strings.TrimSpace(res.Output) != "" {
					fmt.Fprintln(cmd.OutOrStdout(), res.Output)
				}
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Package provider: brew, cask, pacman, paru, apt, dnf, mise, gh-ext")
	cmd.Flags().StringVar(&fileFlag, "file", "", "Target package list file (e.g. pkg/mac-brew.txt)")
	cmd.Flags().BoolVar(&noInstall, "no-install", false, "Only add to pkg/*.txt without installing now")
	return cmd
}

func runPkgUntrack(cmd *cobra.Command, opts *GlobalOpts, fileFlag string, args []string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	removed := make([]map[string]any, 0, len(args))
	for _, name := range args {
		targetFile := strings.TrimSpace(fileFlag)
		if targetFile == "" {
			if found, ok := pkgmgr.FindPackageListFile(ctx.RepoRoot, name, nil); ok {
				targetFile = found
			} else {
				targetFile = pkgmgr.DefaultPackageListFile(ctx, defaultProviderForHost(ctx))
			}
		}
		ok := true
		if !ctx.DryRun {
			var rerr error
			ok, rerr = pkgmgr.RemovePackageFromList(ctx.RepoRoot, targetFile, name)
			if rerr != nil {
				return rerr
			}
		}
		removed = append(removed, map[string]any{
			"package": name,
			"file":    targetFile,
			"removed": ok,
			"dryRun":  ctx.DryRun,
		})
		if !ctx.JSON {
			if ctx.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] untrack %s from %s\n", name, targetFile)
			} else if ok {
				fmt.Fprintf(cmd.OutOrStdout(), "Untracked %s from %s\n", name, targetFile)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s not found in %s\n", name, targetFile)
			}
		}
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), removed)
	}
	return nil
}

func newPkgUntrackCmd(opts *GlobalOpts) *cobra.Command {
	var fileFlag string
	cmd := &cobra.Command{
		Use:    "untrack <package>...",
		Short:  "Untrack packages without uninstalling",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPkgUntrack(cmd, opts, fileFlag, args)
		},
	}
	cmd.Flags().StringVar(&fileFlag, "file", "", "Package list file to remove from (defaults to auto-detect across pkg/*.txt)")
	return cmd
}

func newPkgSearchCmd(opts *GlobalOpts) *cobra.Command {
	var (
		providerFlag string
		limitFlag    int
		openTUI      bool
	)
	cmd := &cobra.Command{
		Use:     "search [query]",
		Aliases: []string{"find", "s"},
		Short:   "Search package repositories",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			query := strings.TrimSpace(strings.Join(args, " "))
			if openTUI || (query == "" && !ctx.JSON && !wantsTSV(cmd) && isInteractiveTTY(cmd, ctx)) {
				return runTUIWithOptions(cmd, ctx, tui.Options{
					StartTab:          "pkg-search",
					Version:           opts.Version,
					PkgSearchQuery:    query,
					PkgSearchProvider: providerFlag,
				})
			}
			if query == "" {
				return fmt.Errorf("requires at least 1 arg(s), received 0")
			}

			outW := cmd.OutOrStdout()
			var results []pkgmgr.SearchResult
			if !ctx.JSON && !wantsTSV(cmd) {
				err = ui.Spin(outW, loadWriterRoles(ctx, outW), fmt.Sprintf("Searching packages for %q", query), func() error {
					var serr error
					results, serr = pkgmgr.SearchPackages(ctx, query, pkgmgr.SearchOptions{
						Provider: providerFlag,
						Limit:    limitFlag,
					})
					return serr
				})
			} else {
				results, err = pkgmgr.SearchPackages(ctx, query, pkgmgr.SearchOptions{
					Provider: providerFlag,
					Limit:    limitFlag,
				})
			}
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), results)
			}
			if len(results) == 0 && !wantsTSV(cmd) {
				fmt.Fprintf(cmd.OutOrStdout(), "No packages matched %q.\n", query)
				return nil
			}
			rows := make([][]string, 0, len(results))
			for _, r := range results {
				stLabel := "available"
				switch {
				case r.Installed && r.Tracked:
					stLabel = "installed,tracked"
				case r.Installed:
					stLabel = "installed"
				case r.Tracked:
					stLabel = "tracked"
				}
				provCol := r.Provider
				if r.Repo != "" && r.Repo != r.Provider && !strings.HasPrefix(r.Repo, "homebrew/") {
					provCol = r.Provider + "/" + r.Repo
				}
				rows = append(rows, []string{provCol, r.Name, nonEmptyStr(r.Version, "-"), stLabel, r.Description})
			}
			return renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "PROVIDER", MinWidth: 10},
				{Title: "PACKAGE", MinWidth: 24},
				{Title: "VERSION", MinWidth: 12},
				{Title: "STATUS", MinWidth: 12},
				{Title: "DESCRIPTION"},
			}, rows)
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Filter provider: all, brew, cask, paru, pacman, mise, apt")
	cmd.Flags().IntVar(&limitFlag, "limit", 80, "Maximum search results to display")
	cmd.Flags().BoolVar(&openTUI, "tui", false, "Open interactive TUI package browser")
	return cmd
}

func newPkgInfoCmd(opts *GlobalOpts) *cobra.Command {
	var providerFlag string
	cmd := &cobra.Command{
		Use:     "info [package]",
		Aliases: []string{"show"},
		Short:   "Show package metadata",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			prov := strings.TrimSpace(providerFlag)
			pkgName := ""
			if len(args) > 0 {
				pkgName = strings.TrimSpace(args[0])
			} else {
				if !isPickerInteractiveTTY(cmd, ctx) {
					return fmt.Errorf("requires at least 1 arg(s), received 0")
				}
				installed, lerr := pkgmgr.ListInstalledPackages(ctx, prov)
				if lerr != nil {
					return lerr
				}
				if len(installed) == 0 {
					return fmt.Errorf("no installed packages found")
				}
				items := make([]picker.Item, 0, len(installed))
				for _, it := range installed {
					items = append(items, picker.Item{
						Title:    it.Name,
						Subtitle: strings.TrimSpace(it.Provider + " " + it.Version),
						Primary:  it.Name,
						Columns:  []string{it.Provider, it.Name, nonEmptyStr(it.Version, "-")},
						Value:    it.Provider + "\t" + it.Name,
						Filter:   strings.TrimSpace(it.Provider + " " + it.Name + " " + it.Version),
					})
				}
				chosen, perr := picker.Pick(cmdContext(cmd), picker.Options{
					Title: "Select installed package",
					Items: items,
					Columns: []picker.ColumnSpec{
						{Title: "PROVIDER", Width: 10, Accent: true},
						{Title: "PACKAGE", Width: 0},
						{Title: "VERSION", Width: 18, Muted: true},
					},
					Palette: loadActivePalette(ctx),
				})
				if perr != nil {
					return perr
				}
				p, n, ok := strings.Cut(chosen.Value, "\t")
				if ok {
					if prov == "" {
						prov = p
					}
					pkgName = n
				} else {
					pkgName = chosen.Value
				}
			}

			info, err := pkgmgr.GetPackageInfo(ctx, pkgName, prov)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), info)
			}
			w := cmd.OutOrStdout()
			status := "not installed"
			if info.Installed {
				status = "installed"
			}
			provLabel := info.Provider
			if info.Repo != "" {
				provLabel += " (" + info.Repo + ")"
			}
			ver := nonEmptyStr(info.Version, info.LatestVersion)
			if info.LatestVersion != "" && info.Version != "" && info.LatestVersion != info.Version {
				ver = fmt.Sprintf("%s (latest: %s)", info.Version, info.LatestVersion)
			}
			fmt.Fprintf(w, "Name:         %s\n", info.Name)
			fmt.Fprintf(w, "Provider:     %s\n", provLabel)
			if ver != "" {
				fmt.Fprintf(w, "Version:      %s\n", ver)
			}
			fmt.Fprintf(w, "Status:       %s\n", status)
			if info.Description != "" {
				fmt.Fprintf(w, "Description:  %s\n", info.Description)
			}
			if info.Homepage != "" {
				fmt.Fprintf(w, "Homepage:     %s\n", info.Homepage)
			}
			if info.License != "" {
				fmt.Fprintf(w, "License:      %s\n", info.License)
			}
			if info.InstalledSize != "" {
				fmt.Fprintf(w, "Size:         %s\n", info.InstalledSize)
			}
			if len(info.Dependencies) > 0 {
				fmt.Fprintf(w, "Dependencies: %s\n", strings.Join(info.Dependencies, ", "))
			}
			if len(info.OptionalDeps) > 0 {
				fmt.Fprintf(w, "Optional:     %s\n", strings.Join(info.OptionalDeps, "; "))
			}
			if len(info.RequiredBy) > 0 {
				fmt.Fprintf(w, "Required By:  %s\n", strings.Join(info.RequiredBy, ", "))
			}
			if len(info.Conflicts) > 0 {
				fmt.Fprintf(w, "Conflicts:    %s\n", strings.Join(info.Conflicts, ", "))
			}
			if len(info.Artifacts) > 0 {
				fmt.Fprintf(w, "Artifacts:    %s\n", strings.Join(info.Artifacts, ", "))
			}
			if info.Caveats != "" {
				fmt.Fprintf(w, "\nCaveats:\n%s\n", info.Caveats)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Package provider: brew, cask, paru, pacman, mise, apt")
	return cmd
}

func newPkgInstallCmd(opts *GlobalOpts) *cobra.Command {
	var (
		providerFlag string
		trackFlag    bool
		fileFlag     string
	)
	cmd := &cobra.Command{
		Use:     "install [package]...",
		Aliases: []string{"i"},
		Short:   "Install packages via host manager",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if isInteractiveTTY(cmd, ctx) {
					return runTUIWithOptions(cmd, ctx, tui.Options{
						StartTab:          "pkg-search",
						Version:           opts.Version,
						PkgSearchProvider: providerFlag,
					})
				}
				return fmt.Errorf("requires at least 1 arg(s), received 0")
			}

			tb := NewTUIBackend(ctx)
			defer func() { _ = tb.Close() }()

			var results []map[string]any
			for _, pkgName := range args {
				name := strings.TrimSpace(pkgName)
				if name == "" {
					continue
				}
				prov := strings.ToLower(strings.TrimSpace(providerFlag))
				if prov == "" {
					if info, ierr := pkgmgr.GetPackageInfo(ctx, name, ""); ierr == nil && info.Provider != "" {
						prov = info.Provider
					} else {
						prov = defaultProviderForHost(ctx)
					}
				}
				if trackFlag {
					res, aerr := tb.AddAndTrackPackage(name, prov, fileFlag, true)
					if aerr != nil {
						return aerr
					}
					results = append(results, map[string]any{
						"package":  name,
						"provider": prov,
						"tracked":  true,
						"command":  res.Command,
						"output":   res.Output,
					})
					if !ctx.JSON && strings.TrimSpace(res.Output) != "" {
						fmt.Fprintln(cmd.OutOrStdout(), res.Output)
					}
					continue
				}

				res, ierr := tb.ExecPackageAction("install", []tui.PresetToolRow{{
					ID:          name,
					PackageName: name,
					Provider:    prov,
					Updates:     "manager",
				}})
				if ierr != nil {
					return ierr
				}
				results = append(results, map[string]any{
					"package":  name,
					"provider": prov,
					"tracked":  false,
					"command":  res.Command,
					"output":   res.Output,
				})
				if !ctx.JSON && strings.TrimSpace(res.Output) != "" {
					fmt.Fprintln(cmd.OutOrStdout(), res.Output)
				}
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Package provider: brew, cask, paru, pacman, apt, dnf, mise")
	cmd.Flags().BoolVar(&trackFlag, "track", false, "Also append installed package(s) to pkg/*.txt")
	cmd.Flags().StringVar(&fileFlag, "file", "", "Target pkg/*.txt file when --track is set")
	return cmd
}

func newPkgUpdateCmd(opts *GlobalOpts) *cobra.Command {
	var providerFlag string
	cmd := &cobra.Command{
		Use:     "update <package>...",
		Aliases: []string{"upgrade"},
		Short:   "Upgrade specific packages",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			tb := NewTUIBackend(ctx)
			defer func() { _ = tb.Close() }()

			var results []map[string]any
			for _, pkgName := range args {
				name := strings.TrimSpace(pkgName)
				if name == "" {
					continue
				}
				prov := strings.ToLower(strings.TrimSpace(providerFlag))
				if prov == "" {
					if info, ierr := pkgmgr.GetPackageInfo(ctx, name, ""); ierr == nil && info.Provider != "" {
						prov = info.Provider
					} else {
						prov = defaultProviderForHost(ctx)
					}
				}
				res, uerr := tb.ExecPackageAction("update", []tui.PresetToolRow{{
					ID:          name,
					PackageName: name,
					Provider:    prov,
					Updates:     "manager",
				}})
				if uerr != nil {
					return uerr
				}
				results = append(results, map[string]any{
					"package":  name,
					"provider": prov,
					"command":  res.Command,
					"output":   res.Output,
				})
				if !ctx.JSON && strings.TrimSpace(res.Output) != "" {
					fmt.Fprintln(cmd.OutOrStdout(), res.Output)
				}
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), results)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Package provider: brew, cask, paru, pacman, apt, dnf, mise, gh-ext")
	return cmd
}

func newPkgRemoveCmd(opts *GlobalOpts) *cobra.Command {
	var (
		providerFlag  string
		fileFlag      string
		keepInstalled bool
		untrackFlag   bool
	)
	cmd := &cobra.Command{
		Use:     "remove [package]...",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Uninstall and untrack packages",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keepInstalled {
				if len(args) == 0 {
					return &UsageError{Err: fmt.Errorf("requires at least 1 arg(s), received 0")}
				}
				return runPkgUntrack(cmd, opts, fileFlag, args)
			}
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			type rmTarget struct {
				name string
				prov string
			}
			var targets []rmTarget
			if len(args) == 0 {
				if !isPickerInteractiveTTY(cmd, ctx) {
					return fmt.Errorf("requires at least 1 arg(s), received 0")
				}
				installed, lerr := pkgmgr.ListInstalledPackages(ctx, providerFlag)
				if lerr != nil {
					return lerr
				}
				if len(installed) == 0 {
					return fmt.Errorf("no installed packages found")
				}
				items := make([]picker.Item, 0, len(installed))
				for _, it := range installed {
					items = append(items, picker.Item{
						Title:    it.Name,
						Subtitle: strings.TrimSpace(it.Provider + " " + it.Version),
						Primary:  it.Name,
						Columns:  []string{it.Provider, it.Name, nonEmptyStr(it.Version, "-")},
						Value:    it.Provider + "\t" + it.Name,
						Filter:   strings.TrimSpace(it.Provider + " " + it.Name + " " + it.Version),
					})
				}
				chosen, perr := picker.Pick(cmdContext(cmd), picker.Options{
					Title: "Select package to remove",
					Items: items,
					Columns: []picker.ColumnSpec{
						{Title: "PROVIDER", Width: 10, Accent: true},
						{Title: "PACKAGE", Width: 0},
						{Title: "VERSION", Width: 18, Muted: true},
					},
					Palette: loadActivePalette(ctx),
				})
				if perr != nil {
					return perr
				}
				p, n, ok := strings.Cut(chosen.Value, "\t")
				if ok {
					prov := strings.TrimSpace(providerFlag)
					if prov == "" {
						prov = p
					}
					targets = append(targets, rmTarget{name: n, prov: prov})
				} else {
					targets = append(targets, rmTarget{name: chosen.Value, prov: nonEmptyStr(providerFlag, defaultProviderForHost(ctx))})
				}
			} else {
				for _, a := range args {
					name := strings.TrimSpace(a)
					if name == "" {
						continue
					}
					prov := strings.ToLower(strings.TrimSpace(providerFlag))
					if prov == "" {
						if info, ierr := pkgmgr.GetPackageInfo(ctx, name, ""); ierr == nil && info.Provider != "" {
							prov = info.Provider
						} else {
							prov = defaultProviderForHost(ctx)
						}
					}
					targets = append(targets, rmTarget{name: name, prov: prov})
				}
			}
			if len(targets) == 0 {
				return fmt.Errorf("no package specified")
			}

			names := make([]string, 0, len(targets))
			rows := make([]tui.PresetToolRow, 0, len(targets))
			for _, t := range targets {
				names = append(names, fmt.Sprintf("%s (%s)", t.name, t.prov))
				rows = append(rows, tui.PresetToolRow{
					ID:          t.name,
					PackageName: t.name,
					Provider:    t.prov,
					Updates:     "manager",
				})
			}

			if !ctx.DryRun && !allowGranted(ctx.Allow, "remove") {
				if ctx.NonInteractive || ctx.JSON {
					return fmt.Errorf("removing packages requires --allow remove in non-interactive or --json mode")
				}
				var confirmed bool
				form := huh.NewForm(huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Uninstall %d package(s): %s?", len(targets), strings.Join(names, ", "))).
						Affirmative("Uninstall").
						Negative("Cancel").
						Value(&confirmed),
				))
				if ferr := form.Run(); ferr != nil {
					return ferr
				}
				if !confirmed {
					return picker.ErrCancelled
				}
			}

			tb := NewTUIBackend(ctx)
			defer func() { _ = tb.Close() }()
			res, rerr := tb.ExecPackageAction("remove", rows)
			if rerr != nil {
				return rerr
			}

			var untrackedFiles []string
			if untrackFlag && !ctx.DryRun && ctx.RepoRoot != "" {
				for _, t := range targets {
					targetFile := strings.TrimSpace(fileFlag)
					if targetFile == "" {
						if relFile, ok := pkgmgr.FindPackageListFile(ctx.RepoRoot, t.name, nil); ok {
							targetFile = relFile
						}
					}
					if targetFile != "" {
						if removed, uerr := pkgmgr.RemovePackageFromList(ctx.RepoRoot, targetFile, t.name); uerr == nil && removed {
							untrackedFiles = append(untrackedFiles, t.name+":"+targetFile)
						}
					}
				}
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"targets":   names,
					"command":   res.Command,
					"output":    res.Output,
					"dryRun":    ctx.DryRun,
					"untracked": untrackedFiles,
				})
			}
			if strings.TrimSpace(res.Output) != "" {
				fmt.Fprintln(cmd.OutOrStdout(), res.Output)
			}
			for _, u := range untrackedFiles {
				fmt.Fprintf(cmd.OutOrStdout(), "Untracked %s\n", u)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerFlag, "provider", "", "Package provider: brew, cask, paru, pacman, apt, dnf, mise")
	cmd.Flags().BoolVar(&keepInstalled, "keep-installed", false, "Remove package(s) from pkg/*.txt without uninstalling")
	cmd.Flags().StringVar(&fileFlag, "file", "", "Target pkg/*.txt file when untracking")
	cmd.Flags().BoolVar(&untrackFlag, "untrack", true, "Also remove package name(s) from pkg/*.txt")
	_ = cmd.Flags().MarkHidden("untrack")
	return cmd
}

func runPkgList(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	presets, err := config.LoadPresets(ctx.RepoRoot)
	if err != nil {
		return err
	}
	resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, ctx.EffectivePreset())
	if err != nil {
		return err
	}
	catalog, err := config.LoadTools(ctx.RepoRoot)
	if err != nil {
		return err
	}
	tools := pkgmgr.RefineToolsForPreset(ctx, resolved, config.ResolveTools(resolved, catalog, ctx.Host), nil)
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"preset": resolved.ID,
			"tools":  tools,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Preset %s (%d tools):\n", resolved.ID, len(tools))
	rows := make([][]string, 0, len(tools))
	for _, t := range tools {
		pkgName := t.Spec.Name
		if pkgName == "" {
			pkgName = t.Spec.Ref
		}
		if pkgName == "" {
			pkgName = t.ID
		}
		rows = append(rows, []string{t.ID, t.Spec.Via, pkgName, t.Updates, t.Source})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "ID", MinWidth: 20},
		{Title: "PROVIDER", MinWidth: 8},
		{Title: "PACKAGE", MinWidth: 20},
		{Title: "UPDATES", MinWidth: 8},
		{Title: "SOURCE"},
	}, rows)
}

func newUpdateCmd(opts *GlobalOpts) *cobra.Command {
	var allFlag bool
	var digestFlag bool
	var pkgOnlyFlag bool
	var outputPath string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update packages, tools, and dotfiles",
		Args:  rejectUnknownSubcommand,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}

			liveOutput := !ctx.JSON && strings.TrimSpace(outputPath) == "" && !digestFlag && !ctx.DryRun && opts.SavePlanPath == ""
			runStart := time.Now()
			outW := cmd.OutOrStdout()
			roles := loadWriterRoles(ctx, outW)
			log := ui.NewLogger(outW, roles)
			isInteractiveOut := !ctx.JSON && strings.TrimSpace(outputPath) == "" && ui.IsTTY(outW)

			var digest *pkgmgr.UpdateDigest
			if opts.PlanPath != "" {
				plan, lerr := engine.LoadPlan(opts.PlanPath)
				if lerr != nil {
					return lerr
				}
				digest = &pkgmgr.UpdateDigest{
					Preset: plan.Preset,
					Items:  []pkgmgr.DigestItem{},
					Plan:   plan,
				}
			} else {
				if isInteractiveOut {
					err = ui.Spin(outW, roles, fmt.Sprintf("Checking workstation updates (%s)", ctx.EffectivePreset()), func() error {
						var perr error
						digest, perr = pkgmgr.PlanUpdate(ctx, pkgmgr.UpdateOptions{
							All:     allFlag,
							PkgOnly: pkgOnlyFlag || digestFlag,
						})
						return perr
					})
				} else {
					if liveOutput {
						log.Info("Checking workstation updates", "preset", ctx.EffectivePreset())
					}
					digest, err = pkgmgr.PlanUpdate(ctx, pkgmgr.UpdateOptions{
						All:     allFlag,
						PkgOnly: pkgOnlyFlag || digestFlag,
					})
				}
				if err != nil {
					return err
				}
			}

			emitOutput := func(writeFn func(w io.Writer) error) error {
				trimmed := strings.TrimSpace(outputPath)
				if trimmed == "" {
					return writeFn(cmd.OutOrStdout())
				}
				var buf bytes.Buffer
				if err := writeFn(&buf); err != nil {
					return err
				}
				dest := ctx.ExpandHome(trimmed)
				return app.WriteAtomicFile(dest, buf.Bytes(), 0o600)
			}

			if digestFlag || ctx.DryRun {
				if opts.SavePlanPath != "" && digest.Plan != nil {
					if err := digest.Plan.Save(opts.SavePlanPath); err != nil {
						return err
					}
				}
				if ctx.JSON {
					return emitOutput(func(w io.Writer) error { return writeJSON(w, digest) })
				}
				return emitOutput(func(w io.Writer) error {
					printUpdateDigestWith(cmd, ctx, w, digest)
					if ctx.DryRun && digest.Plan != nil && (len(digest.Plan.Actions) > 0 || digest.Plan.Metadata["post_apply"] == "true") {
						printUpdatePlanSummary(w, ctx, digest.Plan, true, digest.Warnings)
					}
					return nil
				})
			}

			if opts.SavePlanPath != "" && digest.Plan != nil {
				if err := digest.Plan.Save(opts.SavePlanPath); err != nil {
					return err
				}
				if ctx.JSON {
					return emitOutput(func(w io.Writer) error { return writeJSON(w, digest.Plan) })
				}
				return emitOutput(func(w io.Writer) error {
					fmt.Fprintf(w, "Saved update plan (%d actions) to %s\n", len(digest.Plan.Actions), opts.SavePlanPath)
					return nil
				})
			}

			if digest.Plan == nil || (len(digest.Plan.Actions) == 0 && digest.Plan.Metadata["post_apply"] != "true") {
				if ctx.JSON {
					return emitOutput(func(w io.Writer) error { return writeJSON(w, digest) })
				}
				return emitOutput(func(w io.Writer) error {
					ui.NewLogger(w, loadWriterRoles(ctx, w)).Done("All managed packages are up to date")
					return nil
				})
			}

			if err := promptMissingAllows(cmd, ctx, digest.Plan); err != nil {
				return err
			}

			if liveOutput {
				printUpdateDigestWith(cmd, ctx, outW, digest)
				emittedWarnings := slices.Clone(digest.Warnings)
				prog := ui.NewProgress(outW, roles)
				res, err := pkgmgr.ExecUpdateWithOptions(ctx, digest, nil, pkgmgr.ExecUpdateOptions{
					StreamOut: prog.ChildWriter(),
					StreamErr: prog.ChildWriter(),
					OnEvent: func(ev pkgmgr.UpdateEvent) {
						switch ev.Status {
						case "phase":
							prog.Section(ev.Summary)
						case "start":
							prog.StartWithTTY(ev.ActionID, ev.Summary, ev.NeedsTTY)
						case "done":
							prog.Done(ev.ActionID, ev.Summary, ev.Duration, ev.Detail)
						case "skip":
							prog.Skip(ev.ActionID, ev.Summary, ev.Detail)
						case "warn":
							msg := ev.Detail
							if msg != "" {
								emittedWarnings = append(emittedWarnings, msg)
							}
							if ev.ActionID == "update:post-apply" && ev.Summary == "ws apply" {
								prog.WarnLog(msg)
							} else {
								prog.Warn(ev.ActionID, ev.Summary, ev.Duration, msg)
							}
						case "fail":
							prog.Fail(ev.ActionID, ev.Summary, ev.Duration, ev.Detail)
						}
					},
				})
				prog.Close()
				if res != nil {
					for _, w := range res.Warnings {
						if !slices.Contains(emittedWarnings, w) && !warningDetailEmitted(emittedWarnings, w) {
							log.Warn(w)
						}
					}
					log.BlankLine()
					elapsed := ui.FormatDuration(time.Since(runStart))
					if err != nil {
						log.Error("Update failed", "applied", res.Applied, "skipped", res.Skipped, "elapsed", elapsed)
					} else if skipErr := consentSkippedError(res); skipErr != nil {
						log.Warn(skipErr.Error())
						err = skipErr
					} else {
						log.Done("Update complete", "applied", res.Applied, "skipped", res.Skipped, "elapsed", elapsed)
					}
				}
				return err
			}

			res, err := pkgmgr.ExecUpdate(ctx, digest, nil)
			if err != nil {
				return err
			}
			skipErr := consentSkippedError(res)
			if ctx.JSON {
				if werr := emitOutput(func(w io.Writer) error {
					return writeJSON(w, map[string]any{
						"digest": digest,
						"result": res,
					})
				}); werr != nil {
					return werr
				}
				return skipErr
			}
			if werr := emitOutput(func(w io.Writer) error {
				printUpdateDigestWith(cmd, ctx, w, digest)
				printApplyResult(w, loadWriterRoles(ctx, w), res, false)
				if skipErr != nil {
					ui.NewLogger(w, loadWriterRoles(ctx, w)).Warn(skipErr.Error())
				}
				return nil
			}); werr != nil {
				return werr
			}
			return skipErr
		},
	}

	cmd.Flags().BoolVar(&allFlag, "all", false, "Include greedy cask checks (still excludes updates=self tools)")
	cmd.Flags().BoolVar(&digestFlag, "digest", false, "Print categorized update digest without applying")
	cmd.Flags().BoolVar(&pkgOnlyFlag, "pkg-only", false, "Upgrade managed packages only without repo pull, shared tools, or post-update apply")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write output atomically to a file instead of stdout")
	addPlanFlags(cmd, opts)
	cmd.AddCommand(newScheduleCmd(opts))
	return cmd
}

func warningDetailEmitted(emitted []string, full string) bool {
	if full == "" {
		return true
	}
	norm := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(full, "\r\n", "; "), "\n", "; "))
	for _, e := range emitted {
		if e == "" {
			continue
		}
		if e == full || e == norm || strings.Contains(norm, e) {
			return true
		}
		if strings.HasSuffix(e, "...") {
			if prefix := strings.TrimSuffix(e, "..."); prefix != "" && strings.HasPrefix(norm, prefix) {
				return true
			}
		}
	}
	return false
}

func printUpdatePlanSummary(w io.Writer, ctx *app.Context, plan *engine.Plan, dryRun bool, printedWarnings []string) {
	log := ui.NewLogger(w, loadWriterRoles(ctx, w))
	var finalizeSteps []string
	if plan.Metadata["post_apply"] == "true" {
		finalizeSteps = append(finalizeSteps, "Re-sync dotfile symlinks and rendered theme templates (ws apply)")
		if ctx != nil && ctx.RepoRoot != "" {
			if fi, err := os.Stat(filepath.Join(ctx.RepoRoot, "ws", "go.mod")); err == nil && !fi.IsDir() {
				if _, lerr := exec.LookPath("go"); lerr == nil {
					finalizeSteps = append(finalizeSteps, "Rebuild ~/.local/bin/ws if ws/ changed or binary is missing")
				}
			}
		}
	}
	log.BlankLine()
	var kv []any
	if dryRun {
		kv = append(kv, "mode", "dry-run")
	}
	kv = append(kv, "actions", len(plan.Actions))
	if len(finalizeSteps) > 0 {
		kv = append(kv, "finalize", len(finalizeSteps))
	}
	kv = append(kv, "skipped", len(plan.Skipped))
	log.Plan(plan.Title, kv...)
	for _, act := range plan.Actions {
		log.PlanItem(string(act.Kind), act.Summary)
	}
	for _, step := range finalizeSteps {
		log.PlanItem("finalize", step)
	}
	for _, warn := range plan.Warnings {
		if !slices.Contains(printedWarnings, warn) {
			log.Warn(warn)
		}
	}
}

func printUpdateDigestWith(cmd *cobra.Command, ctx *app.Context, w io.Writer, d *pkgmgr.UpdateDigest) {
	roles := loadWriterRoles(ctx, w)
	log := ui.NewLogger(w, roles)
	log.Info("Update digest", "preset", d.Preset, "pending", len(d.Items))
	if d.RepoDirty {
		log.Warn("Repository has uncommitted changes (automatic pull skipped)")
	}
	if d.SnapshotHint != "" {
		log.Info("Snapshot hint", "cmd", d.SnapshotHint)
	}
	for _, warn := range d.Warnings {
		log.Warn(warn)
	}
	if len(d.Items) == 0 {
		log.Info("No outdated packages detected")
		return
	}
	rows := make([][]string, 0, len(d.Items))
	for _, it := range d.Items {
		ver := it.Latest
		if it.Current != "" {
			ver = fmt.Sprintf("%s -> %s", it.Current, it.Latest)
		}
		riskLabel := strings.ToUpper(string(it.Risk))
		switch strings.ToLower(string(it.Risk)) {
		case "high", "dangerous":
			riskLabel = roles.Err.Render(riskLabel)
		case "medium", "costly", "warn":
			riskLabel = roles.Warn.Render(riskLabel)
		case "safe", "low":
			riskLabel = roles.OK.Render(riskLabel)
		}
		rows = append(rows, []string{
			riskLabel,
			it.Provider,
			it.Name,
			ver,
			it.Note,
		})
	}
	log.BlankLine()
	_ = renderCLITableTo(cmd, ctx, w, []ui.TableColumn{
		{Title: "RISK", MinWidth: 9},
		{Title: "PROVIDER", MinWidth: 8},
		{Title: "PACKAGE", MinWidth: 20},
		{Title: "VERSION", MinWidth: 14},
		{Title: "NOTE"},
	}, rows)
}

func newScheduleCmd(opts *GlobalOpts) *cobra.Command {
	var intervalHours int
	var withDBBackup bool

	schedCmd := &cobra.Command{
		Use:   "schedule",
		Short: "Manage periodic digest and backup schedules",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScheduleStatus(cmd, opts)
		},
	}
	schedCmd.PersistentFlags().IntVar(&intervalHours, "interval-hours", 12, "Hours between read-only digest checks")
	schedCmd.PersistentFlags().BoolVar(&withDBBackup, "with-db-backup", false, "Include auxiliary diff-gated ws db backup --if-changed schedule")

	schedCmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show digest schedule status",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runScheduleStatus(cmd, opts)
			},
		},
		&cobra.Command{
			Use:   "print",
			Short: "Print schedule unit files",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				spec, err := pkgmgr.RenderScheduleFilesWithOptions(ctx, pkgmgr.ScheduleOptions{
					IntervalHours: intervalHours,
					WithDBBackup:  withDBBackup,
				})
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), spec)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Backend: %s (every %dh)\n", spec.Backend, spec.IntervalHours)
				for _, f := range spec.Files {
					fmt.Fprintf(cmd.OutOrStdout(), "\n--- %s ---\n%s", f.Path, f.Content)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "install",
			Short: "Install and enable the digest schedule",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				st, err := pkgmgr.InstallScheduleWithOptions(ctx, pkgmgr.ScheduleOptions{
					IntervalHours: intervalHours,
					WithDBBackup:  withDBBackup,
				}, nil)
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), st)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Schedule (%s): installed=%v active=%v (%s)\n", st.Backend, st.Installed, st.Active, st.Detail)
				return nil
			},
		},
		&cobra.Command{
			Use:   "remove",
			Short: "Disable and remove the digest schedule",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				if err := pkgmgr.RemoveSchedule(ctx, nil); err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"removed": !ctx.DryRun,
						"dryRun":  ctx.DryRun,
					})
				}
				if ctx.DryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run] would remove digest schedule")
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Removed digest schedule.")
				return nil
			},
		},
	)

	return schedCmd
}

func runScheduleStatus(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	st, err := pkgmgr.GetScheduleStatus(ctx, nil)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), st)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Backend:   %s\n", st.Backend)
	fmt.Fprintf(cmd.OutOrStdout(), "Installed: %v\n", st.Installed)
	fmt.Fprintf(cmd.OutOrStdout(), "Active:    %v\n", st.Active)
	if st.IntervalHours > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Interval:  %dh\n", st.IntervalHours)
	}
	if st.Detail != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Detail:    %s\n", st.Detail)
	}
	return nil
}

func newCleanCmd(opts *GlobalOpts) *cobra.Command {
	var tierRaw string
	var categoryRaw string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Preview or reclaim disk caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}

			tiers, err := parseCleanupTiers(tierRaw)
			if err != nil {
				return err
			}

			outW := cmd.OutOrStdout()
			roles := loadWriterRoles(ctx, outW)
			var candidates []cleanup.Candidate
			if !ctx.JSON && !wantsTSV(cmd) {
				err = ui.Spin(outW, roles, "Scanning reclaimable caches", func() error {
					var serr error
					candidates, serr = cleanup.Scan(ctx)
					return serr
				})
			} else {
				candidates, err = cleanup.Scan(ctx)
			}
			if err != nil {
				return err
			}

			if strings.TrimSpace(categoryRaw) != "" {
				var cats []string
				for _, part := range strings.Split(categoryRaw, ",") {
					if c := strings.ToLower(strings.TrimSpace(part)); c != "" {
						cats = append(cats, c)
					}
				}
				var filtered []cleanup.Candidate
				for _, cand := range candidates {
					if slices.Contains(cats, strings.ToLower(cand.Category)) {
						filtered = append(filtered, cand)
					}
				}
				candidates = filtered
			}

			var plan *engine.Plan
			if opts.PlanPath != "" {
				plan, err = engine.LoadPlan(opts.PlanPath)
			} else {
				plan, err = cleanup.PlanClean(ctx, candidates, nil, tiers)
			}
			if err != nil {
				return err
			}

			if opts.SavePlanPath != "" {
				if err := plan.Save(opts.SavePlanPath); err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), plan)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Saved cleanup plan (%d actions) to %s\n", len(plan.Actions), opts.SavePlanPath)
				return nil
			}

			if err := promptMissingAllows(cmd, ctx, plan); err != nil {
				return err
			}

			res, err := cleanup.ExecClean(ctx, plan, nil)
			if err != nil {
				return err
			}

			var totalBytes int64
			for _, c := range candidates {
				totalBytes += c.Bytes
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun":     ctx.DryRun,
					"candidates": candidates,
					"totalBytes": totalBytes,
					"plan":       plan,
					"result":     res,
				})
			}

			log := ui.NewLogger(outW, roles)
			if len(candidates) == 0 {
				log.Info("No reclaimable caches found")
				return nil
			}
			log.Info("Reclaimable candidates", "count", len(candidates), "total", formatHumanBytes(totalBytes))
			cleanRows := make([][]string, 0, len(candidates))
			for _, c := range candidates {
				cleanRows = append(cleanRows, []string{
					strings.ToUpper(string(c.Tier)),
					c.Category,
					c.Title,
					formatHumanBytes(c.Bytes),
				})
			}
			if err := renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "TIER", MinWidth: 9},
				{Title: "CATEGORY", MinWidth: 10},
				{Title: "TARGET", MinWidth: 28},
				{Title: "SIZE", MinWidth: 8, RightAlign: true},
			}, cleanRows); err != nil {
				return err
			}
			printPlanSummary(outW, roles, plan, ctx.DryRun)
			if ctx.DryRun {
				if !executeFlag && len(plan.Actions) > 0 {
					log.Info("Preview only (dry-run by default). Re-run with --execute to reclaim")
				}
			} else {
				printApplyResult(outW, roles, res, false)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&tierRaw, "tier", "", "Comma-separated tiers: safe,costly,dangerous")
	cmd.Flags().StringVar(&categoryRaw, "category", "", "Comma-separated cleanup categories (e.g. pkg-cache,cli-cache,backups,cargo)")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Actually delete/reclaim selected targets (default is dry-run preview)")
	addPlanFlags(cmd, opts)
	_ = cmd.RegisterFlagCompletionFunc("tier", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"safe", "costly", "dangerous"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

type pkgInstallDetector struct {
	ctx             *app.Context
	providers       map[string]pkgmgr.Provider
	brewFormulae    map[string]bool
	brewCasks       map[string]bool
	hasBrewFormulae bool
	hasBrewCasks    bool
	macApps         map[string]bool
	pacmanPkgs      map[string]bool
	hasPacman       bool
	dpkgPkgs        map[string]bool
	hasDpkg         bool
}

func newPkgInstallDetector(ctx *app.Context, tools []config.ResolvedTool) *pkgInstallDetector {
	r := pkgmgr.TimedExecRunner{Timeout: 10 * time.Second}
	d := &pkgInstallDetector{
		ctx:       ctx,
		providers: pkgmgr.AllProviders(ctx, r, tools),
	}
	switch ctx.Host.OS {
	case "darwin":
		d.brewFormulae, d.hasBrewFormulae = pkgmgr.LoadBrewInstalledSet(r, false)
		d.brewCasks, d.hasBrewCasks = pkgmgr.LoadBrewInstalledSet(r, true)
		d.macApps = loadMacAppBundles(ctx)
	case "linux":
		d.pacmanPkgs, d.hasPacman = pkgmgr.LoadArchInstalledSet(r, "")
		d.dpkgPkgs, d.hasDpkg = pkgmgr.LoadDpkgInstalledSet(r)
	}
	return d
}

func (d *pkgInstallDetector) checkStatus(t config.ResolvedTool) (bool, string) {
	pkgName := t.Spec.Name
	if pkgName == "" {
		pkgName = t.ID
	}

	installed := false
	switch t.Spec.Via {
	case "cask":
		if d.hasBrewCasks {
			installed = pkgmgr.MatchBrewInstalled(d.brewCasks, pkgName, true)
		} else if prov, ok := d.providers["cask"]; ok && prov.Available() {
			installed = prov.IsInstalled(t)
		} else if t.Bin != "" {
			if _, err := exec.LookPath(t.Bin); err == nil {
				installed = true
			}
		}
		if !installed && len(d.macApps) > 0 {
			installed = d.macApps[normalizeAppKey(pkgName)] || d.macApps[normalizeAppKey(t.ID)]
		}

	case "brew":
		if d.hasBrewFormulae {
			installed = pkgmgr.MatchBrewInstalled(d.brewFormulae, pkgName, false)
		} else if prov, ok := d.providers["brew"]; ok && prov.Available() {
			installed = prov.IsInstalled(t)
		} else if t.Bin != "" {
			if _, err := exec.LookPath(t.Bin); err == nil {
				installed = true
			}
		}
		if !installed && d.ctx != nil && d.ctx.Host.OS == "darwin" && strings.EqualFold(pkgName, "zsh") {
			bin := t.Bin
			if bin == "" {
				bin = "zsh"
			}
			if _, err := exec.LookPath(bin); err == nil {
				installed = true
			}
		}

	case "pacman", "paru":
		if d.hasPacman {
			installed = d.pacmanPkgs[pkgName]
		} else if prov, ok := d.providers[t.Spec.Via]; ok && prov.Available() {
			installed = prov.IsInstalled(t)
		} else if t.Bin != "" {
			if _, err := exec.LookPath(t.Bin); err == nil {
				installed = true
			}
		}

	case "apt":
		if d.hasDpkg {
			installed = d.dpkgPkgs[pkgName]
		} else if prov, ok := d.providers["apt"]; ok && prov.Available() {
			installed = prov.IsInstalled(t)
		} else if t.Bin != "" {
			if _, err := exec.LookPath(t.Bin); err == nil {
				installed = true
			}
		}

	default:
		if prov, ok := d.providers[t.Spec.Via]; ok && prov.Available() {
			installed = prov.IsInstalled(t)
		} else if t.Bin != "" {
			if _, err := exec.LookPath(t.Bin); err == nil {
				installed = true
			}
		}
	}

	if !installed {
		return false, "missing"
	}

	if t.Updates == "self" {
		if t.Bin != "" {
			if p, err := exec.LookPath(t.Bin); err == nil && hasBrewConflictPath(p, t.Conflicts) {
				return true, "conflict"
			}
		}
		for _, c := range t.Conflicts {
			if name, ok := strings.CutPrefix(c, "cask:"); ok && d.hasBrewCasks && pkgmgr.MatchBrewInstalled(d.brewCasks, name, true) {
				return true, "conflict"
			}
			if name, ok := strings.CutPrefix(c, "brew:"); ok && d.hasBrewFormulae && pkgmgr.MatchBrewInstalled(d.brewFormulae, name, false) {
				return true, "conflict"
			}
		}
	}

	return true, "installed"
}

func loadMacAppBundles(ctx *app.Context) map[string]bool {
	apps := map[string]bool{}
	var dirs []string
	if ctx != nil && ctx.HomeDir != "" {
		dirs = append(dirs, filepath.Join(ctx.HomeDir, "Applications"))
		if realHome, err := os.UserHomeDir(); err == nil && realHome != "" {
			a, err1 := filepath.Abs(filepath.Clean(ctx.HomeDir))
			b, err2 := filepath.Abs(filepath.Clean(realHome))
			if err1 == nil && err2 == nil && a == b {
				dirs = append(dirs, "/Applications")
			}
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(strings.ToLower(name), ".app") {
				if key := normalizeAppKey(name); key != "" {
					apps[key] = true
				}
			}
		}
	}
	return apps
}

func normalizeAppKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if slash := strings.LastIndexByte(s, '/'); slash >= 0 {
		s = s[slash+1:]
	}
	if at := strings.IndexByte(s, '@'); at >= 0 {
		s = s[:at]
	}
	s = strings.TrimSuffix(s, ".app")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseCleanupTiers(raw string) ([]cleanup.Tier, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []cleanup.Tier
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "":
			continue
		case "safe":
			out = append(out, cleanup.TierSafe)
		case "costly":
			out = append(out, cleanup.TierCostly)
		case "dangerous":
			out = append(out, cleanup.TierDangerous)
		default:
			return nil, fmt.Errorf("unknown cleanup tier %q (expected safe, costly, or dangerous)", strings.TrimSpace(part))
		}
	}
	return out, nil
}

func formatHumanBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
