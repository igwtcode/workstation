package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/atotto/clipboard"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/insights"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

var (
	writeClipboard  = clipboard.WriteAll
	histPick        = picker.Pick
	histIsPickerTTY = isPickerInteractiveTTY
)

func newOSEnvCmd(opts *GlobalOpts) *cobra.Command {
	osEnvCmd := &cobra.Command{
		Use:   "os-env",
		Short: "Render or apply OS GUI XDG env",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runOSEnvPrint(cmd, opts)
		},
	}

	osEnvCmd.AddCommand(
		&cobra.Command{
			Use:   "print",
			Short: "Print rendered XDG environment config",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runOSEnvPrint(cmd, opts)
			},
		},
		&cobra.Command{
			Use:   "apply",
			Short: "Write and activate XDG environment config",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				art, err := shell.RenderOSEnv(ctx)
				if err != nil {
					return err
				}
				if err := shell.ApplyOSEnv(ctx); err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"artifact": art,
						"dryRun":   ctx.DryRun,
					})
				}
				if ctx.DryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would write %s\n", art.Path)
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Applied %s (%s)\n", art.Path, art.Summary)
				return nil
			},
		},
	)

	return osEnvCmd
}

func runOSEnvPrint(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	art, err := shell.RenderOSEnv(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), art)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "# %s (%s)\n%s", art.Path, art.Summary, art.Content)
	return nil
}

func newZoneCmd(opts *GlobalOpts) *cobra.Command {
	zoneCmd := &cobra.Command{
		Use:     "zone",
		Aliases: []string{"identity"},
		Short:   "Manage directory-scoped identity zones",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runZoneStatus(cmd, opts)
		},
	}

	zoneCmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show configured identity zones",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runZoneStatus(cmd, opts)
			},
		},
		&cobra.Command{
			Use:               "apply [zone]",
			Short:             "Create and configure identity zones",
			Args:              cobra.MaximumNArgs(1),
			ValidArgsFunction: completeZoneNames(opts),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				only := ""
				if len(args) == 1 {
					only = args[0]
				}
				actions, err := shell.ApplyZones(ctx, only)
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"actions": actions,
						"dryRun":  ctx.DryRun,
					})
				}
				if len(actions) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Zones already up to date.")
					return nil
				}
				for _, a := range actions {
					fmt.Fprintln(cmd.OutOrStdout(), a)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:               "fix-symlinks [zone]",
			Short:             "Convert symlinked zone configs to real files",
			Args:              cobra.MaximumNArgs(1),
			ValidArgsFunction: completeZoneNames(opts),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				only := ""
				if len(args) == 1 {
					only = args[0]
				}
				converted, err := shell.FixZoneSymlinks(ctx, only)
				if err != nil {
					return err
				}
				applied, err := shell.ApplyZones(ctx, only)
				if err != nil {
					return err
				}
				all := append(converted, applied...)
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"actions": all,
						"dryRun":  ctx.DryRun,
					})
				}
				if len(all) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No zone symlinks needed conversion.")
					return nil
				}
				for _, a := range all {
					fmt.Fprintln(cmd.OutOrStdout(), a)
				}
				return nil
			},
		},
		newZoneMigrateCmd(opts),
		newRunCmd(opts),
	)

	return zoneCmd
}

func newZoneMigrateCmd(opts *GlobalOpts) *cobra.Command {
	var destSub string
	var allFlag bool
	var entries []string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:               "migrate <zone> [source]",
		Short:             "Move directories into a zone",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeZoneNames(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			zone := strings.TrimSpace(args[0])
			sourceDir := ""
			if len(args) == 2 {
				sourceDir = args[1]
			}
			destSet := cmd.Flags().Changed("dest")
			srcAbs, destAbs, available, err := shell.ListMigratableZoneDirs(ctx, zone, sourceDir, destSub, destSet)
			if err != nil {
				return err
			}
			if len(available) == 0 {
				return fmt.Errorf("no directories to move under %s", ctx.CollapseHome(srcAbs))
			}

			selected := append([]string(nil), entries...)
			if len(selected) == 0 {
				if allFlag {
					selected = append(selected, available...)
				} else if !isPickerInteractiveTTY(cmd, ctx) {
					return fmt.Errorf("non-interactive session: pass --all or --entry to migrate %d directory(s) from %s", len(available), ctx.CollapseHome(srcAbs))
				} else {
					optsList := make([]huh.Option[string], 0, len(available))
					for _, e := range available {
						optsList = append(optsList, huh.NewOption(e, e))
					}
					form := huh.NewForm(huh.NewGroup(
						huh.NewMultiSelect[string]().
							Title(fmt.Sprintf("Move directories from %s into %s", ctx.CollapseHome(srcAbs), ctx.CollapseHome(destAbs))).
							Options(optsList...).
							Value(&selected),
					)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
					if herr := form.Run(); herr != nil {
						return herr
					}
					if len(selected) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "Nothing selected.")
						return nil
					}
				}
			}

			if !ctx.DryRun && isPickerInteractiveTTY(cmd, ctx) {
				choice, perr := promptSelect(
					cmd,
					ctx,
					fmt.Sprintf("Move %d directory(s) from %s into %s?", len(selected), ctx.CollapseHome(srcAbs), ctx.CollapseHome(destAbs)),
					[]picker.Item{
						{Title: "Cancel", Subtitle: "leave directories untouched", Value: "cancel"},
						{Title: "Move directories", Subtitle: fmt.Sprintf("%d selected", len(selected)), Value: "move"},
					},
				)
				if perr != nil {
					return perr
				}
				if choice != "move" {
					fmt.Fprintln(cmd.OutOrStdout(), "Migration cancelled.")
					return nil
				}
			}

			rep, err := shell.MigrateZoneDirs(ctx, shell.MigrateZoneOptions{
				Zone:    zone,
				Source:  sourceDir,
				DestSub: destSub,
				DestSet: destSet,
				Entries: selected,
				All:     allFlag,
			})
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			for _, it := range rep.Items {
				switch it.Status {
				case "skipped":
					fmt.Fprintf(cmd.OutOrStdout(), "%sskipped   %-16s (target exists: %s)\n", prefix, it.Name, ctx.CollapseHome(it.To))
				default:
					fmt.Fprintf(cmd.OutOrStdout(), "%s%-9s %-16s -> %s\n", prefix, it.Status, it.Name, ctx.CollapseHome(it.To))
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%szone migrate: %d moved, %d skipped\n", prefix, rep.Moved, rep.Skipped)
			if ctx.DryRun && !executeFlag && len(rep.Items) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to move.")
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&destSub, "dest", "d", "code", "Subdirectory inside the zone (empty moves to zone root)")
	cmd.Flags().BoolVarP(&allFlag, "all", "a", false, "Move every directory under source without interactive picker")
	cmd.Flags().StringSliceVar(&entries, "entry", nil, "Specific directory name(s) to migrate")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Actually move directories (default is dry-run preview)")
	return cmd
}

func runZoneStatus(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	statuses, err := shell.ZoneStatuses(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), statuses)
	}
	if len(statuses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No identity zones configured (WS_ZONES unset).")
		return nil
	}
	for _, st := range statuses {
		cfgState := "missing"
		if st.ConfigIsSymlink {
			cfgState = "symlink!"
		} else if st.ConfigExists {
			cfgState = "ok"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-12s root=%-5v mise.toml=%-8s trusted=%-5v git=%-5v %s\n",
			st.Spec.Name, st.RootExists, cfgState, st.Trusted, st.GitConfigExists, ctx.CollapseHome(st.Root))
		for _, w := range st.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  ! %s\n", w)
		}
	}
	return nil
}

func newWhoamiCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show active zone and tool identities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			rep, err := shell.Whoami(ctx, "")
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			zone := rep.ActiveZone
			if zone == "" {
				zone = "(none)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Zone:        %s\n", zone)
			fmt.Fprintf(cmd.OutOrStdout(), "CWD:         %s\n", rep.CWD)
			if rep.ZoneMismatch {
				fmt.Fprintf(cmd.OutOrStdout(), "Mismatch:    %s\n", rep.MismatchDetail)
			}
			if rep.GitName != "" || rep.GitEmail != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Git:         %s <%s>\n", rep.GitName, rep.GitEmail)
			}
			if rep.AWSProfile != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "AWS Profile: %s\n", rep.AWSProfile)
			}
			if rep.GHConfigDir != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "GH Config:   %s\n", rep.GHConfigDir)
			}
			if rep.KubeConfig != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Kubeconfig:  %s\n", rep.KubeConfig)
			}
			if rep.ClaudeConfigDir != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Claude Dir:  %s\n", rep.ClaudeConfigDir)
			}
			if rep.CodexHome != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Codex Home:  %s\n", rep.CodexHome)
			} else if !rep.ZoneMismatch && ctx.HomeDir != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Codex Home:  %s (default)\n", filepath.Join(ctx.HomeDir, ".codex"))
			}
			return nil
		},
	}
}

func newRunCmd(opts *GlobalOpts) *cobra.Command {
	var zoneName string

	cmd := &cobra.Command{
		Use:   "run --zone <zone> -- <cmd...>",
		Short: "Run a command inside a zone environment",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(zoneName) == "" {
				return &UsageError{Err: fmt.Errorf("--zone is required")}
			}
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			zoneEnv, err := shell.ResolveZoneEnv(ctx, zoneName)
			if err != nil {
				return err
			}
			if ctx.DryRun {
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"zone":    zoneName,
						"env":     zoneEnv,
						"command": args,
						"dryRun":  true,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] zone=%s command=%s\n", zoneName, strings.Join(args, " "))
				return nil
			}
			c := exec.CommandContext(cmdContext(cmd), args[0], args[1:]...)
			c.Env = shell.ReplaceZoneEnvForContext(ctx, os.Environ(), zoneEnv)
			c.Stdin = cmd.InOrStdin()
			c.Stdout = cmd.OutOrStdout()
			c.Stderr = cmd.ErrOrStderr()
			return wrapCmdExitErr(c.Run())
		},
	}

	cmd.Flags().StringVarP(&zoneName, "zone", "z", "", "Target identity zone name")
	_ = cmd.RegisterFlagCompletionFunc("zone", completeZoneNames(opts))
	return cmd
}

func filterReposByZone(repos []projects.RepoRecord, zone string) []projects.RepoRecord {
	z := strings.TrimSpace(zone)
	if z == "" || strings.EqualFold(z, "all") {
		return repos
	}
	out := make([]projects.RepoRecord, 0, len(repos))
	for _, r := range repos {
		if strings.EqualFold(r.Zone, z) {
			out = append(out, r)
		}
	}
	return out
}

func newRepoCmd(opts *GlobalOpts) *cobra.Command {
	var zoneFilter string

	repoCmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage repositories across zones",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRepoList(cmd, opts, zoneFilter)
		},
	}
	repoCmd.PersistentFlags().StringVar(&zoneFilter, "zone", "", "Filter or target identity zone")
	_ = repoCmd.RegisterFlagCompletionFunc("zone", completeZoneNames(opts))

	var cloneName string
	var cloneNoZones bool
	var cloneAll bool
	cloneCmd := &cobra.Command{
		Use:   "clone [source|forge-selector]",
		Short: "Clone repositories into a zone",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			source := ""
			if len(args) == 1 {
				source = strings.TrimSpace(args[0])
			}

			useForgeMode := source == ""
			var matchedForges []projects.ForgeSpec
			if !useForgeMode && !cloneNoZones && cloneName == "" {
				if forges, ferr := projects.ParseForges(ctx); ferr == nil {
					if sel, serr := projects.ResolveForgeSelection(forges, source); serr == nil && len(sel) > 0 {
						useForgeMode = true
						matchedForges = sel
					}
				}
			} else if useForgeMode {
				forges, ferr := projects.ParseForges(ctx)
				if ferr != nil {
					return ferr
				}
				matchedForges = forges
			}

			if useForgeMode {
				if len(matchedForges) > 1 && source == "" && !cloneAll && isPickerInteractiveTTY(cmd, ctx) {
					forgeItems := make([]picker.Item, 0, len(matchedForges)+1)
					forgeItems = append(forgeItems, picker.Item{
						Title:    fmt.Sprintf("All forges (%d)", len(matchedForges)),
						Subtitle: "list repositories across all configured forges",
						Value:    "__all__",
					})
					for i, f := range matchedForges {
						forgeItems = append(forgeItems, picker.Item{
							Title:    fmt.Sprintf("%s:%s", f.Kind, f.Source),
							Subtitle: ctx.CollapseHome(f.Dest),
							Value:    strconv.Itoa(i),
						})
					}
					choice, perr := promptSelect(cmd, ctx, "Select forge", forgeItems)
					if perr != nil {
						return perr
					}
					if choice != "__all__" {
						idx, _ := strconv.Atoi(choice)
						if idx >= 0 && idx < len(matchedForges) {
							matchedForges = []projects.ForgeSpec{matchedForges[idx]}
						}
					}
				}

				results := make([]*projects.BulkCloneResult, 0, len(matchedForges))
				for _, f := range matchedForges {
					targetZone := strings.TrimSpace(zoneFilter)
					if targetZone == "" {
						targetZone = projects.DeriveZoneForPath(ctx, f.Dest)
					}
					listed, lerr := projects.ListForgeRepos(ctx, f, targetZone, nil)
					if lerr != nil {
						return lerr
					}
					selectedRepos := listed
					if !cloneAll {
						if !isPickerInteractiveTTY(cmd, ctx) {
							if len(listed) > 1 {
								return fmt.Errorf("non-interactive bulk clone matched %d repo(s) for %s:%s: pass --all or clone a single slug", len(listed), f.Kind, f.Source)
							}
						} else if len(listed) > 0 {
							var chosenRefs []string
							repoOpts := make([]huh.Option[string], 0, len(listed))
							for _, r := range listed {
								label := r.Display
								if _, stErr := os.Stat(filepath.Join(f.Dest, r.Name)); stErr == nil {
									label += " (exists)"
								}
								repoOpts = append(repoOpts, huh.NewOption(label, r.CloneRef))
							}
							form := huh.NewForm(huh.NewGroup(
								huh.NewMultiSelect[string]().
									Title(fmt.Sprintf("Clone from %s:%s -> %s", f.Kind, f.Source, ctx.CollapseHome(f.Dest))).
									Options(repoOpts...).
									Value(&chosenRefs),
							)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
							if herr := form.Run(); herr != nil {
								return herr
							}
							wantRef := make(map[string]bool, len(chosenRefs))
							for _, ref := range chosenRefs {
								wantRef[ref] = true
							}
							selectedRepos = nil
							for _, r := range listed {
								if wantRef[r.CloneRef] {
									selectedRepos = append(selectedRepos, r)
								}
							}
						}
					}
					res, cerr := projects.BulkCloneForgeRepos(ctx, f, targetZone, selectedRepos, nil)
					if res != nil {
						results = append(results, res)
					}
					if cerr != nil {
						return cerr
					}
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"dryRun":  ctx.DryRun,
						"results": results,
					})
				}
				prefix := ""
				if ctx.DryRun {
					prefix = "[dry-run] "
				}
				for _, r := range results {
					fmt.Fprintf(cmd.OutOrStdout(), "%sForge %s:%s -> %s (cloned=%d existing=%d)\n",
						prefix, r.Forge.Kind, r.Forge.Source, ctx.CollapseHome(r.Forge.Dest), len(r.Cloned), len(r.Existing))
					for _, c := range r.Cloned {
						fmt.Fprintf(cmd.OutOrStdout(), "  + %s\n", c)
					}
				}
				return nil
			}

			plan, err := projects.PlanClone(ctx, projects.CloneRequest{
				Source:   source,
				Zone:     zoneFilter,
				DestName: cloneName,
				NoZones:  cloneNoZones,
			})
			if err != nil {
				return err
			}
			if err := projects.ExecClone(ctx, plan, nil); err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun": ctx.DryRun,
					"plan":   plan,
				})
			}
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sClone (%s) -> %s\n  cmd: %s\n",
				prefix, plan.Forge, ctx.CollapseHome(plan.DestDir), strings.Join(plan.Command, " "))
			return nil
		},
	}
	cloneCmd.Flags().StringVar(&cloneName, "name", "", "Override destination directory name")
	cloneCmd.Flags().BoolVar(&cloneNoZones, "no-zones", false, "Clone into flat ~/code instead of a zone")
	cloneCmd.Flags().BoolVarP(&cloneAll, "all", "a", false, "Bulk clone all repos matched from WS_FORGES")

	prsRefresh := false
	prsCmd := &cobra.Command{
		Use:    "prs",
		Short:  "List open GitHub pull requests",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRepoGHItems(cmd, opts, "pr", prsRefresh, zoneFilter)
		},
	}
	prsCmd.Flags().BoolVar(&prsRefresh, "refresh", false, "Bypass cache and query GitHub CLI now")

	issuesRefresh := false
	issuesCmd := &cobra.Command{
		Use:    "issues",
		Short:  "List open GitHub issues",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRepoGHItems(cmd, opts, "issue", issuesRefresh, zoneFilter)
		},
	}
	issuesCmd.Flags().BoolVar(&issuesRefresh, "refresh", false, "Bypass cache and query GitHub CLI now")

	var listRefresh bool
	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"status"},
		Short:   "List discovered repositories",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = listRefresh
			return runRepoList(cmd, opts, zoneFilter)
		},
	}
	listCmd.Flags().BoolVar(&listRefresh, "refresh", false, "Refresh repository status and ws.db cache")

	var pickRefresh bool
	pickCmd := &cobra.Command{
		Use:   "pick [query]",
		Short: "Fuzzy-pick a repository path",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			var repos []projects.RepoRecord
			if pickRefresh {
				db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
				if err != nil {
					return err
				}
				defer func() { _ = db.Close() }()
				repos, err = projects.ScanRepos(ctx, db)
				if err != nil {
					return err
				}
			} else {
				var db *store.DB
				dbPath := store.DefaultPath(ctx.WSBaseDir())
				if _, statErr := os.Stat(dbPath); statErr == nil {
					if opened, derr := store.Open(dbPath); derr == nil {
						db = opened
						defer func() { _ = db.Close() }()
					}
				}
				repos, err = projects.DiscoverReposFast(ctx, db)
				if err != nil {
					return err
				}
			}
			repos = filterReposByZone(repos, zoneFilter)
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			matches := projects.FuzzyMatchRepos(repos, query)
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), matches)
			}
			if len(matches) == 0 {
				return fmt.Errorf("no matching repositories found")
			}
			if len(matches) == 1 {
				fmt.Fprintln(cmd.OutOrStdout(), matches[0].LocalPath)
				return nil
			}
			if isPickerInteractiveTTY(cmd, ctx) {
				zoneW := 10
				for _, r := range matches {
					zw := len(r.Zone)
					if zw > zoneW && zw <= 14 {
						zoneW = zw
					}
				}
				const branchW = 24

				items := make([]picker.Item, 0, len(matches))
				for _, r := range matches {
					z := r.Zone
					if z == "" {
						z = "flat"
					}
					branch := r.Branch
					if branch == "" {
						branch = "-"
					}
					if r.Dirty {
						branch += " *"
					}
					displayPath := ctx.CollapseHome(r.LocalPath)
					items = append(items, picker.Item{
						Title:    displayPath,
						Subtitle: z + " · " + branch,
						Primary:  r.Name,
						Columns:  []string{z, branch, displayPath},
						Value:    r.LocalPath,
						Filter:   strings.TrimSpace(z + " " + r.Branch + " " + displayPath + " " + r.Name),
					})
				}
				chosen, err := picker.Pick(cmdContext(cmd), picker.Options{
					Title: "Select repository",
					Query: query,
					Items: items,
					Columns: []picker.ColumnSpec{
						{Title: "ZONE", Width: zoneW, Accent: true},
						{Title: "BRANCH", Width: branchW, Muted: true},
						{Title: "REPOSITORY", Width: 0},
					},
					Palette: loadActivePalette(ctx),
				})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), chosen.Value)
				return nil
			}
			if query != "" {
				fmt.Fprintln(cmd.OutOrStdout(), matches[0].LocalPath)
				return nil
			}
			for _, r := range matches {
				fmt.Fprintln(cmd.OutOrStdout(), r.LocalPath)
			}
			return nil
		},
	}
	pickCmd.Flags().BoolVar(&pickRefresh, "refresh", false, "Run full git status inspection and refresh ws.db cache")

	var pullAll bool
	pullCmd := &cobra.Command{
		Use:   "pull [dir...]",
		Short: "Fast-forward pull clean repositories",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pullAll && len(args) > 0 {
				return &UsageError{Err: fmt.Errorf("--all cannot be combined with a directory argument")}
			}
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			var absDirs []string
			for _, raw := range args {
				dir := strings.TrimSpace(raw)
				if dir == "" {
					continue
				}
				absDir, aerr := filepath.Abs(filepath.Clean(ctx.ExpandHome(dir)))
				if aerr != nil {
					return aerr
				}
				absDirs = append(absDirs, absDir)
			}

			db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			repos, err := projects.ScanRepos(ctx, db)
			if err != nil {
				return err
			}
			if len(absDirs) > 0 {
				seenRepo := map[string]bool{}
				var scoped []projects.RepoRecord
				for _, r := range repos {
					cleanPath := filepath.Clean(r.LocalPath)
					for _, absDir := range absDirs {
						if cleanPath == absDir || strings.HasPrefix(cleanPath, absDir+string(filepath.Separator)) {
							if !seenRepo[cleanPath] {
								seenRepo[cleanPath] = true
								scoped = append(scoped, r)
							}
							break
						}
					}
				}
				if len(scoped) == 0 {
					var agg shell.PullSummary
					agg.ScanDir = strings.Join(absDirs, ", ")
					for _, absDir := range absDirs {
						sum, perr := shell.PullRepos(ctx, absDir)
						if perr != nil {
							return perr
						}
						if len(absDirs) == 1 {
							agg = *sum
							break
						}
						agg.Total += sum.Total
						agg.Updated += sum.Updated
						agg.UpToDate += sum.UpToDate
						agg.Dirty += sum.Dirty
						agg.NoUpstream += sum.NoUpstream
						agg.Failed += sum.Failed
						agg.Items = append(agg.Items, sum.Items...)
					}
					if ctx.JSON {
						return writeJSON(cmd.OutOrStdout(), &agg)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Scanned %s (%d repos): updated=%d up-to-date=%d dirty=%d no-upstream=%d failed=%d\n",
						agg.ScanDir, agg.Total, agg.Updated, agg.UpToDate, agg.Dirty, agg.NoUpstream, agg.Failed)
					return nil
				}
				repos = scoped
			} else if !pullAll && strings.TrimSpace(zoneFilter) != "" {
				repos = filterReposByZone(repos, zoneFilter)
			}

			res, err := projects.BatchPull(ctx, repos)
			if err != nil {
				return err
			}

			if len(res.NeedsInteractive) > 0 && !ctx.DryRun && isPickerInteractiveTTY(cmd, ctx) {
				for _, r := range res.NeedsInteractive {
					choice, perr := promptSelect(
						cmd,
						ctx,
						fmt.Sprintf("Run interactive CodeCommit pull for %s?", r.Name),
						[]picker.Item{
							{Title: "Skip", Value: "skip"},
							{Title: "Pull interactively", Subtitle: r.RemoteURL, Value: "pull"},
						},
					)
					if perr != nil {
						return perr
					}
					if choice != "pull" {
						continue
					}
					c := projects.BuildInteractivePullCmd(ctx, r)
					c.Stdin = cmd.InOrStdin()
					c.Stdout = cmd.OutOrStdout()
					c.Stderr = cmd.ErrOrStderr()
					if rerr := c.Run(); rerr != nil {
						res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", r.Name, rerr))
					} else {
						res.Updated = append(res.Updated, ctx.CollapseHome(r.LocalPath))
					}
				}
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Pulled %d repo(s): updated=%d up-to-date=%d dirty=%d no-upstream=%d failed=%d interactive=%d\n",
				len(repos), len(res.Updated), len(res.UpToDate), len(res.Dirty), len(res.NoUpstream), len(res.Failed), len(res.NeedsInteractive))
			for _, it := range res.Items {
				if it.Status != shell.PullUpToDate {
					line := fmt.Sprintf("  %-12s %s", it.Status, it.Rel)
					if it.Error != "" {
						line += " (" + it.Error + ")"
					}
					fmt.Fprintln(w, line)
				}
			}
			if len(res.NeedsInteractive) > 0 && (ctx.DryRun || !isPickerInteractiveTTY(cmd, ctx)) {
				fmt.Fprintln(w, "Needs interactive auth (CodeCommit):")
				for _, r := range res.NeedsInteractive {
					fmt.Fprintf(w, "  %-16s %s\n", r.Name, ctx.CollapseHome(r.LocalPath))
				}
			}
			return nil
		},
	}
	pullCmd.Flags().BoolVarP(&pullAll, "all", "a", false, "Pull all discovered repositories across configured zones")

	repoCmd.AddCommand(
		listCmd,
		pickCmd,
		cloneCmd,
		pullCmd,
		prsCmd,
		issuesCmd,
		&cobra.Command{
			Use:   "locals [repo]",
			Short: "Show untracked local files per repo",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
				if err != nil {
					return err
				}
				defer func() { _ = db.Close() }()

				repos, err := projects.ScanRepos(ctx, db)
				if err != nil {
					return err
				}
				if len(args) == 1 {
					repos = projects.FuzzyMatchRepos(repos, args[0])
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), repos)
				}
				w := cmd.OutOrStdout()
				printed := 0
				for _, r := range repos {
					if len(r.UntrackedLocals) == 0 {
						continue
					}
					printed++
					z := r.Zone
					if z == "" {
						z = "flat"
					}
					fmt.Fprintf(w, "[%s] %s (%s)\n", z, r.Name, ctx.CollapseHome(r.LocalPath))
					locRows := make([][]string, 0, len(r.UntrackedLocals))
					for _, loc := range r.UntrackedLocals {
						status := "not backed up"
						if loc.BackedUp {
							status = "✓ ws.db"
						}
						locRows = append(locRows, []string{loc.RelPath, formatHumanBytes(loc.SizeBytes), status})
					}
					if err := renderCLITable(cmd, ctx, []ui.TableColumn{
						{Title: "FILE", MinWidth: 24},
						{Title: "SIZE", MinWidth: 8, RightAlign: true},
						{Title: "BACKUP"},
					}, locRows); err != nil {
						return err
					}
				}
				if printed == 0 {
					fmt.Fprintln(w, "No untracked local env/config files detected across repositories.")
				}
				return nil
			},
		},
	)

	return repoCmd
}

func runRepoList(cmd *cobra.Command, opts *GlobalOpts, zone string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	repos, err := projects.ScanRepos(ctx, db)
	if err != nil {
		return err
	}
	repos = filterReposByZone(repos, zone)
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), repos)
	}
	if len(repos) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No repositories discovered across configured zones.")
		return nil
	}
	rows := make([][]string, 0, len(repos))
	for _, r := range repos {
		z := r.Zone
		if z == "" {
			z = "flat"
		}
		state := "clean"
		if r.Dirty {
			state = "dirty"
		}
		branch := r.Branch
		if branch == "" {
			branch = "-"
		}
		rows = append(rows, []string{
			z,
			r.Name,
			branch,
			state,
			r.Forge,
			strconv.Itoa(len(r.UntrackedLocals)),
			ctx.CollapseHome(r.LocalPath),
		})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "ZONE", MinWidth: 8},
		{Title: "REPO", MinWidth: 20},
		{Title: "BRANCH", MinWidth: 12},
		{Title: "STATE", MinWidth: 5},
		{Title: "FORGE", MinWidth: 8},
		{Title: "LOCALS", MinWidth: 6, RightAlign: true},
		{Title: "PATH"},
	}, rows)
}

func runRepoGHItems(cmd *cobra.Command, opts *GlobalOpts, kind string, refresh bool, zone string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	all, notes, err := projects.FetchGitHubItems(ctx, db, refresh, nil)
	if err != nil {
		return err
	}
	zFilter := strings.TrimSpace(zone)
	filtered := make([]projects.GHItem, 0, len(all))
	for _, it := range all {
		if !strings.EqualFold(it.Kind, kind) {
			continue
		}
		if zFilter != "" && !strings.EqualFold(zFilter, "all") && !strings.EqualFold(it.Zone, zFilter) {
			continue
		}
		filtered = append(filtered, it)
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"kind":  kind,
			"items": filtered,
			"notes": notes,
		})
	}
	w := cmd.OutOrStdout()
	if len(filtered) == 0 {
		fmt.Fprintf(w, "No open GitHub %ss found.\n", kind)
	} else {
		rows := make([][]string, 0, len(filtered))
		for _, it := range filtered {
			z := it.Zone
			if z == "" {
				z = "default"
			}
			rows = append(rows, []string{
				z,
				it.Repo,
				strconv.Itoa(it.Number),
				it.Title,
				it.URL,
			})
		}
		if err := renderCLITable(cmd, ctx, []ui.TableColumn{
			{Title: "ZONE", MinWidth: 8},
			{Title: "REPO", MinWidth: 24},
			{Title: "#", MinWidth: 4, RightAlign: true},
			{Title: "TITLE", MaxWidth: 64},
			{Title: "URL"},
		}, rows); err != nil {
			return err
		}
	}
	for _, n := range notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
	return nil
}

type awsProfileMeta struct {
	Region    string
	AccountID string
}

func parseAWSINIProfileMeta(path string) map[string]awsProfileMeta {
	out := map[string]awsProfileMeta{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	var current string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sec := strings.TrimSpace(line[1 : len(line)-1])
			if sec == "default" {
				current = "default"
			} else if strings.HasPrefix(sec, "profile ") {
				current = strings.TrimSpace(strings.TrimPrefix(sec, "profile "))
			} else {
				current = sec
			}
			continue
		}
		if current == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if val == "" {
			continue
		}
		meta := out[current]
		switch key {
		case "region":
			if meta.Region == "" {
				meta.Region = val
			}
		case "sso_account_id", "aws_account_id":
			if meta.AccountID == "" {
				meta.AccountID = val
			}
		case "role_arn":
			if meta.AccountID == "" {
				if acct := extractAWSAccountFromARN(val); acct != "" {
					meta.AccountID = acct
				}
			}
		}
		out[current] = meta
	}
	return out
}

func extractAWSAccountFromARN(arn string) string {
	parts := strings.Split(strings.TrimSpace(arn), ":")
	if len(parts) < 5 {
		return ""
	}
	acct := strings.TrimSpace(parts[4])
	if len(acct) != 12 {
		return ""
	}
	for _, r := range acct {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return acct
}

func formatAWSProfileSubtitle(zone string, meta awsProfileMeta) string {
	parts := make([]string, 0, 3)
	if zone != "" {
		parts = append(parts, zone)
	}
	if meta.Region != "" {
		parts = append(parts, meta.Region)
	}
	if meta.AccountID != "" {
		parts = append(parts, "acct:"+meta.AccountID)
	}
	return strings.Join(parts, " · ")
}

func runAWSMode(cmd *cobra.Command, opts *GlobalOpts, zone, mode, query string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	profs, err := shell.DiscoverAWSProfiles(ctx)
	if err != nil {
		return err
	}
	if z := strings.TrimSpace(zone); z != "" && !strings.EqualFold(z, "all") {
		filtered := make([]shell.AWSProfile, 0, len(profs))
		for _, p := range profs {
			if strings.EqualFold(p.Zone, z) {
				filtered = append(filtered, p)
			}
		}
		profs = filtered
	}

	metaCache := map[string]map[string]awsProfileMeta{}
	metaFor := func(p shell.AWSProfile) awsProfileMeta {
		if p.Source == "" {
			return awsProfileMeta{}
		}
		byProf, ok := metaCache[p.Source]
		if !ok {
			byProf = parseAWSINIProfileMeta(p.Source)
			metaCache[p.Source] = byProf
		}
		return byProf[p.Name]
	}

	if mode == "list" || (mode == "" && !isPickerInteractiveTTY(cmd, ctx)) {
		if ctx.JSON {
			return writeJSON(cmd.OutOrStdout(), profs)
		}
		if len(profs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No AWS profiles discovered.")
			return nil
		}
		rows := make([][]string, 0, len(profs))
		for _, p := range profs {
			m := metaFor(p)
			region := m.Region
			if region == "" {
				region = "-"
			}
			acct := m.AccountID
			if acct == "" {
				acct = "-"
			}
			rows = append(rows, []string{p.Zone, p.Name, region, acct, ctx.CollapseHome(p.Source)})
		}
		return renderCLITable(cmd, ctx, []ui.TableColumn{
			{Title: "ZONE", MinWidth: 10},
			{Title: "PROFILE", MinWidth: 20},
			{Title: "REGION", MinWidth: 12},
			{Title: "ACCOUNT", MinWidth: 12},
			{Title: "SOURCE"},
		}, rows)
	}

	if mode == "" || mode == "pick" {
		if ctx.JSON {
			return writeJSON(cmd.OutOrStdout(), profs)
		}
		if len(profs) == 0 {
			return fmt.Errorf("no AWS profiles discovered")
		}
		zoneW := 10
		hasAccount := false
		metas := make([]awsProfileMeta, len(profs))
		for i, p := range profs {
			if zw := len(p.Zone); zw > zoneW && zw <= 14 {
				zoneW = zw
			}
			m := metaFor(p)
			metas[i] = m
			if m.AccountID != "" {
				hasAccount = true
			}
		}

		items := make([]picker.Item, 0, len(profs))
		for i, p := range profs {
			m := metas[i]
			region := m.Region
			if region == "" {
				region = "-"
			}
			cols := []string{p.Zone, region}
			if hasAccount {
				acct := m.AccountID
				if acct == "" {
					acct = "-"
				}
				cols = append(cols, acct)
			}
			cols = append(cols, p.Name)

			items = append(items, picker.Item{
				Title:    p.Name,
				Subtitle: formatAWSProfileSubtitle(p.Zone, m),
				Primary:  p.Name,
				Columns:  cols,
				Value:    p.Name,
				Filter:   strings.TrimSpace(p.Zone + " " + m.Region + " " + m.AccountID + " " + p.Name),
			})
		}
		if isPickerInteractiveTTY(cmd, ctx) {
			colSpecs := []picker.ColumnSpec{
				{Title: "ZONE", Width: zoneW, Accent: true},
				{Title: "REGION", Width: 14, Muted: true},
			}
			if hasAccount {
				colSpecs = append(colSpecs, picker.ColumnSpec{Title: "ACCOUNT", Width: 12, Muted: true})
			}
			colSpecs = append(colSpecs, picker.ColumnSpec{Title: "PROFILE", Width: 0})

			chosen, perr := picker.Pick(cmdContext(cmd), picker.Options{
				Title:   "Select AWS profile",
				Query:   query,
				Items:   items,
				Columns: colSpecs,
				Palette: loadActivePalette(ctx),
			})
			if perr != nil {
				return perr
			}
			fmt.Fprintln(cmd.OutOrStdout(), chosen.Value)
			return nil
		}
		if query != "" {
			matched := picker.FilterItems(items, query)
			if len(matched) == 0 {
				return fmt.Errorf("no AWS profile matched query %q", query)
			}
			fmt.Fprintln(cmd.OutOrStdout(), matched[0].Value)
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), items[0].Value)
		return nil
	}

	for _, p := range profs {
		if p.Name == mode {
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), p)
			}
			fmt.Fprintln(cmd.OutOrStdout(), p.Name)
			return nil
		}
	}
	return fmt.Errorf("AWS profile %q not found", mode)
}

func newAWSCmd(opts *GlobalOpts) *cobra.Command {
	var awsZone string

	cmd := &cobra.Command{
		Use:   "aws [profile|list|pick [query]]",
		Short: "List or pick AWS profiles",
		Args:  cobra.MaximumNArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			ctx, err := opts.buildContext()
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			profs, err := shell.DiscoverAWSProfiles(ctx)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			out := make([]string, 0, len(profs)+2)
			out = append(out, "list", "pick")
			for _, p := range profs {
				out = append(out, p.Name)
			}
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := ""
			query := ""
			if len(args) >= 1 {
				mode = args[0]
			}
			if len(args) == 2 {
				if mode != "pick" {
					return &UsageError{Err: fmt.Errorf("unexpected argument %q for mode %q (use: ws aws [profile|list|pick [query]])", args[1], mode)}
				}
				query = args[1]
			}
			return runAWSMode(cmd, opts, awsZone, mode, query)
		},
	}
	cmd.PersistentFlags().StringVar(&awsZone, "zone", "", "Filter AWS profiles by identity zone")
	_ = cmd.RegisterFlagCompletionFunc("zone", completeZoneNames(opts))

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List discovered AWS profiles",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runAWSMode(cmd, opts, awsZone, "list", "")
			},
		},
		&cobra.Command{
			Use:   "pick [query]",
			Short: "Fuzzy-pick an AWS profile",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				q := ""
				if len(args) == 1 {
					q = args[0]
				}
				return runAWSMode(cmd, opts, awsZone, "pick", q)
			},
		},
	)
	return cmd
}

func runVaultOp(cmd *cobra.Command, opts *GlobalOpts, scope, mode string, extra []string, allFlag bool, docTitle string, executeFlag bool) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	if !executeFlag {
		ctx.DryRun = true
	}

	if scope == "" {
		if !isPickerInteractiveTTY(cmd, ctx) {
			return &UsageError{Err: fmt.Errorf("vault <secrets|ssh|files|envfiles> <pull|push> required in non-interactive mode")}
		}
		chosenScope, perr := promptSelect(cmd, ctx, "Select vault backup scope", []picker.Item{
			{Title: "secrets", Subtitle: "mise.local.toml + .mise/tasks/local", Value: "secrets"},
			{Title: "ssh", Subtitle: "~/.ssh bundle", Value: "ssh"},
			{Title: "files", Subtitle: "WS_OP_FILES paths", Value: "files"},
			{Title: "envfiles", Subtitle: "code tree .env* archive", Value: "envfiles"},
		})
		if perr != nil {
			return perr
		}
		scope = chosenScope
	}
	if mode == "" {
		if !isPickerInteractiveTTY(cmd, ctx) {
			return &UsageError{Err: fmt.Errorf("vault %s <pull|push> required in non-interactive mode", scope)}
		}
		chosenMode, perr := promptSelect(cmd, ctx, fmt.Sprintf("Select operation for vault %s", scope), []picker.Item{
			{Title: "pull", Subtitle: "restore from 1Password", Value: "pull"},
			{Title: "push", Subtitle: "back up to 1Password", Value: "push"},
		})
		if perr != nil {
			return perr
		}
		mode = chosenMode
	}

	if strings.EqualFold(scope, "files") && len(extra) == 0 && !allFlag && isPickerInteractiveTTY(cmd, ctx) {
		var configured []string
		for _, row := range app.ParsePipeLines(ctx.LookupEnv("WS_OP_FILES")) {
			if len(row) > 0 && row[0] != "" {
				configured = append(configured, row[0])
			}
		}
		if len(configured) > 0 {
			optsList := make([]huh.Option[string], 0, len(configured))
			for _, p := range configured {
				optsList = append(optsList, huh.NewOption(p, p))
			}
			form := huh.NewForm(huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title(fmt.Sprintf("Select WS_OP_FILES entries to %s", mode)).
					Options(optsList...).
					Value(&extra),
			)).WithTheme(ui.HuhTheme(loadWriterRoles(ctx, cmd.ErrOrStderr()))).WithOutput(cmd.ErrOrStderr())
			if herr := form.Run(); herr != nil {
				return herr
			}
			if len(extra) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing selected.")
				return nil
			}
		}
	}

	if ctx.DryRun {
		if ctx.JSON {
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"scope":   scope,
				"mode":    mode,
				"all":     allFlag,
				"doc":     docTitle,
				"targets": extra,
				"dryRun":  true,
			})
		}
		targetDesc := ""
		if len(extra) > 0 {
			targetDesc = " " + strings.Join(extra, " ")
		} else if allFlag {
			targetDesc = " --all"
		}
		docDesc := ""
		if strings.TrimSpace(docTitle) != "" {
			docDesc = fmt.Sprintf(" (doc=%s)", docTitle)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] vault %s %s%s%s\n", scope, mode, targetDesc, docDesc)
		if !executeFlag {
			fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to sync with 1Password.")
		}
		return nil
	}

	if err := shell.VaultOpWithOptions(ctx, mode, scope, extra, shell.VaultOptions{
		DocTitle: docTitle,
		All:      allFlag,
		WarnOut:  cmd.ErrOrStderr(),
	}); err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"scope":  scope,
			"mode":   mode,
			"all":    allFlag,
			"doc":    docTitle,
			"dryRun": false,
			"ok":     true,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Vault %s %s complete.\n", scope, mode)
	return nil
}

func newVaultCmd(opts *GlobalOpts) *cobra.Command {
	var allFlag bool
	var docTitle string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Sync encrypted backups with 1Password",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVaultOp(cmd, opts, "", "", nil, allFlag, docTitle, executeFlag)
		},
	}

	cmd.PersistentFlags().BoolVarP(&allFlag, "all", "a", false, "Apply to all configured WS_OP_FILES or all zone trees (envfiles)")
	cmd.PersistentFlags().StringVarP(&docTitle, "doc", "d", "", "Override 1Password document title")
	cmd.PersistentFlags().BoolVar(&executeFlag, "execute", false, "Execute remote 1Password sync (default is dry-run preview)")

	type scopeSpec struct {
		name     string
		short    string
		argUse   string
		argsFunc cobra.PositionalArgs
	}
	scopes := []scopeSpec{
		{name: "secrets", short: "Sync private mise secrets", argUse: "", argsFunc: cobra.NoArgs},
		{name: "ssh", short: "Sync ~/.ssh bundle", argUse: "", argsFunc: cobra.NoArgs},
		{name: "files", short: "Sync WS_OP_FILES entries", argUse: " [paths...]", argsFunc: cobra.ArbitraryArgs},
		{name: "envfiles", short: "Sync code tree .env archives", argUse: " [dir]", argsFunc: cobra.MaximumNArgs(1)},
	}
	for _, sc := range scopes {
		scopeName := sc.name
		scopeCmd := &cobra.Command{
			Use:   scopeName,
			Short: sc.short,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runVaultOp(cmd, opts, scopeName, "", nil, allFlag, docTitle, executeFlag)
			},
		}
		scopeCmd.AddCommand(
			&cobra.Command{
				Use:     "pull" + sc.argUse,
				Aliases: []string{"load"},
				Short:   "Restore " + scopeName + " from 1Password",
				Args:    sc.argsFunc,
				RunE: func(cmd *cobra.Command, args []string) error {
					return runVaultOp(cmd, opts, scopeName, "pull", args, allFlag, docTitle, executeFlag)
				},
			},
			&cobra.Command{
				Use:     "push" + sc.argUse,
				Aliases: []string{"save"},
				Short:   "Back up " + scopeName + " to 1Password",
				Args:    sc.argsFunc,
				RunE: func(cmd *cobra.Command, args []string) error {
					return runVaultOp(cmd, opts, scopeName, "push", args, allFlag, docTitle, executeFlag)
				},
			},
		)
		cmd.AddCommand(scopeCmd)
	}

	return cmd
}

func newGenCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:       "gen <hex|base64|uuid>",
		Short:     "Generate random hex, base64, or UUID values",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"hex", "base64", "uuid"},
		RunE: func(cmd *cobra.Command, args []string) error {
			val, err := shell.GenSecret(args[0])
			if err != nil {
				return err
			}
			if opts.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]string{
					"kind":  args[0],
					"value": val,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), val)
			return nil
		},
	}
}

func newHistCmd(opts *GlobalOpts) *cobra.Command {
	var limit int
	var listOnly bool
	var copySel bool

	histCmd := &cobra.Command{
		Use:   "hist [query]",
		Short: "Search unified shell history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := ""
			if len(args) == 1 {
				q = args[0]
			}
			return runHistSearch(cmd, opts, q, limit, listOnly, copySel)
		},
	}
	histCmd.PersistentFlags().IntVar(&limit, "limit", 0, "Maximum history entries (default 500 in picker, 50 in table)")
	histCmd.Flags().BoolVarP(&listOnly, "list", "l", false, "Print history table instead of opening the interactive picker")
	histCmd.Flags().BoolVarP(&copySel, "copy", "c", false, "Copy the selected command to the clipboard")

	histCmd.AddCommand(&cobra.Command{
		Use:    "search [query]",
		Short:  "Filter shell history by substring",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := ""
			if len(args) == 1 {
				q = args[0]
			}
			return runHistSearch(cmd, opts, q, limit, true, false)
		},
	})

	return histCmd
}

func runHistSearch(cmd *cobra.Command, opts *GlobalOpts, query string, limit int, listOnly, copySel bool) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	interactive := !listOnly && !ctx.JSON && !wantsTSV(cmd) && histIsPickerTTY(cmd, ctx)
	effLimit := limit
	if effLimit <= 0 {
		if interactive {
			effLimit = 500
		} else {
			effLimit = 50
		}
	}
	entries, source, err := insights.SearchHistory(ctx, query, effLimit)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"source":  source,
			"query":   query,
			"entries": entries,
		})
	}
	if len(entries) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "No matching history entries (source: %s).\n", source)
		return nil
	}

	if interactive {
		seenCmd := make(map[string]bool, len(entries))
		items := make([]picker.Item, 0, len(entries))
		shellW := 5
		zoneW := 8
		for _, e := range entries {
			cmdStr := strings.TrimSpace(e.Command)
			if cmdStr == "" || seenCmd[cmdStr] {
				continue
			}
			seenCmd[cmdStr] = true
			zone := e.Zone
			if zone == "" {
				zone = "-"
			}
			sh := e.Shell
			if sh == "" {
				sh = "-"
			}
			if len(sh) > shellW && len(sh) <= 8 {
				shellW = len(sh)
			}
			if len(zone) > zoneW && len(zone) <= 12 {
				zoneW = len(zone)
			}
			exitStr := strconv.Itoa(e.ExitCode)
			items = append(items, picker.Item{
				Title:    cmdStr,
				Subtitle: sh + " · " + zone + " · exit " + exitStr,
				Primary:  cmdStr,
				Columns:  []string{sh, zone, exitStr, cmdStr},
				Value:    cmdStr,
				Filter:   strings.TrimSpace(sh + " " + zone + " " + cmdStr),
			})
		}
		if len(items) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No matching history entries (source: %s).\n", source)
			return nil
		}
		chosen, perr := histPick(cmdContext(cmd), picker.Options{
			Title: "History (" + source + ")",
			Query: query,
			Items: items,
			Columns: []picker.ColumnSpec{
				{Title: "SHELL", Width: shellW, Muted: true},
				{Title: "ZONE", Width: zoneW, Accent: true},
				{Title: "EXIT", Width: 4, Muted: true},
				{Title: "COMMAND", Width: 0},
			},
			Palette: loadActivePalette(ctx),
		})
		if perr != nil {
			return perr
		}
		if copySel {
			if cerr := writeClipboard(chosen.Value); cerr != nil {
				return fmt.Errorf("copy to clipboard: %w", cerr)
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), chosen.Value)
		return nil
	}

	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		zone := e.Zone
		if zone == "" {
			zone = "-"
		}
		rows = append(rows, []string{e.Shell, zone, strconv.Itoa(e.ExitCode), e.Command})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "SHELL", MinWidth: 6},
		{Title: "ZONE", MinWidth: 8},
		{Title: "EXIT", MinWidth: 4, RightAlign: true},
		{Title: "COMMAND"},
	}, rows)
}
