package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/misemgr"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

func newMiseCmd(opts *GlobalOpts) *cobra.Command {
	var zoneFlag string
	var scopeFlag string
	var outdatedOnly bool
	var noOutdated bool

	cmd := &cobra.Command{
		Use:   "mise",
		Short: "Inspect and edit mise configs and tools",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			return runMiseList(cmd, ctx, zoneFlag, scopeFlag, outdatedOnly, !noOutdated)
		},
	}
	cmd.Flags().StringVar(&zoneFlag, "zone", "", "Filter by zone name (or 'all')")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "Filter by scope: global, zone, project")
	cmd.Flags().BoolVar(&outdatedOnly, "outdated", false, "Show only outdated tools")
	cmd.Flags().BoolVar(&noOutdated, "no-outdated", false, "Skip running mise outdated checks")

	cmd.AddCommand(
		newMiseListCmd(opts),
		newMiseConfigsCmd(opts),
		newMiseOutdatedCmd(opts),
		newMiseEditCmd(opts),
	)
	return cmd
}

func newMiseListCmd(opts *GlobalOpts) *cobra.Command {
	var zoneFlag, scopeFlag string
	var outdatedOnly, noOutdated bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List declared and active mise tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			return runMiseList(cmd, ctx, zoneFlag, scopeFlag, outdatedOnly, !noOutdated)
		},
	}
	cmd.Flags().StringVar(&zoneFlag, "zone", "", "Filter by zone name")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "Filter by scope: global, zone, project")
	cmd.Flags().BoolVar(&outdatedOnly, "outdated", false, "Show only outdated tools")
	cmd.Flags().BoolVar(&noOutdated, "no-outdated", false, "Skip running mise outdated checks")
	return cmd
}

func newMiseConfigsCmd(opts *GlobalOpts) *cobra.Command {
	var zoneFlag, scopeFlag string
	var noOutdated bool
	cmd := &cobra.Command{
		Use:   "configs",
		Short: "List discovered mise config files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			inv, err := scanMiseForCLI(ctx, zoneFlag, !noOutdated)
			if err != nil {
				return err
			}
			configs := filterMiseConfigsScope(inv.Configs, scopeFlag)
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), configs)
			}
			rows := make([][]string, 0, len(configs))
			for _, c := range configs {
				rows = append(rows, []string{
					c.Scope,
					nonEmptyCLI(c.Zone, "global"),
					nonEmptyCLI(c.ProjectName, c.Kind),
					nonEmptyCLI(c.Forge, "local"),
					fmt.Sprintf("%d", len(c.Tools)),
					fmt.Sprintf("%d", c.OutdatedCount),
					c.Path,
				})
			}
			return renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "SCOPE", MinWidth: 8},
				{Title: "ZONE", MinWidth: 8},
				{Title: "PROJECT", MinWidth: 16},
				{Title: "FORGE", MinWidth: 10},
				{Title: "TOOLS", MinWidth: 5, RightAlign: true},
				{Title: "OUTDATED", MinWidth: 8, RightAlign: true},
				{Title: "PATH", MinWidth: 28},
			}, rows)
		},
	}
	cmd.Flags().StringVar(&zoneFlag, "zone", "", "Filter by zone name")
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "Filter by scope: global, zone, project")
	cmd.Flags().BoolVar(&noOutdated, "no-outdated", false, "Skip running mise outdated checks")
	return cmd
}

func newMiseOutdatedCmd(opts *GlobalOpts) *cobra.Command {
	var zoneFlag string
	cmd := &cobra.Command{
		Use:   "outdated",
		Short: "Check outdated mise tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			return runMiseList(cmd, ctx, zoneFlag, "", true, true)
		},
	}
	cmd.Flags().StringVar(&zoneFlag, "zone", "", "Filter by zone name")
	return cmd
}

func newMiseEditCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "edit <path-or-project>",
		Short: "Open a mise config in your editor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if ctx.NonInteractive {
				return fmt.Errorf("mise edit requires an interactive terminal")
			}
			query := strings.TrimSpace(args[0])
			target, err := misemgr.ValidateEditableMisePath(ctx, query)
			if err != nil {
				inv, sErr := scanMiseForCLI(ctx, "", false)
				if sErr != nil {
					return err
				}
				var matched string
				qLower := strings.ToLower(query)
				for _, c := range inv.Configs {
					if strings.EqualFold(c.ProjectName, query) || strings.Contains(strings.ToLower(c.Path), qLower) || strings.Contains(strings.ToLower(c.ProjectName), qLower) {
						matched = c.Path
						break
					}
				}
				if matched == "" {
					return fmt.Errorf("no mise config matched %q: %w", query, err)
				}
				target, err = misemgr.ValidateEditableMisePath(ctx, matched)
				if err != nil {
					return err
				}
			}
			edCmd := resolveEditorCmd(target)
			edCmd.Stdin = cmd.InOrStdin()
			edCmd.Stdout = cmd.OutOrStdout()
			edCmd.Stderr = cmd.ErrOrStderr()
			return edCmd.Run()
		},
	}
}

func runMiseList(cmd *cobra.Command, ctx *app.Context, zoneFlag, scopeFlag string, outdatedOnly, checkOutdated bool) error {
	inv, err := scanMiseForCLI(ctx, zoneFlag, checkOutdated)
	if err != nil {
		return err
	}
	var tools []misemgr.InstalledTool
	sf := strings.ToLower(strings.TrimSpace(scopeFlag))
	for _, t := range inv.Tools {
		if sf != "" && !strings.EqualFold(t.Scope, sf) {
			continue
		}
		if outdatedOnly && !t.Outdated {
			continue
		}
		tools = append(tools, t)
	}
	if ctx.JSON {
		outInv := *inv
		outInv.Tools = tools
		outInv.Configs = filterMiseConfigsScope(inv.Configs, scopeFlag)
		return writeJSON(cmd.OutOrStdout(), outInv)
	}
	rows := make([][]string, 0, len(tools))
	for _, t := range tools {
		rows = append(rows, []string{
			t.Name,
			nonEmptyCLI(t.Requested, "—"),
			nonEmptyCLI(t.Version, "—"),
			nonEmptyCLI(t.Latest, "—"),
			t.Status,
			t.Scope,
			nonEmptyCLI(t.ProjectName, "global"),
			t.SourcePath,
		})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "TOOL", MinWidth: 16},
		{Title: "REQ", MinWidth: 8},
		{Title: "INSTALLED", MinWidth: 10},
		{Title: "LATEST", MinWidth: 10},
		{Title: "STATUS", MinWidth: 9},
		{Title: "SCOPE", MinWidth: 8},
		{Title: "PROJECT", MinWidth: 14},
		{Title: "SOURCE", MinWidth: 26},
	}, rows)
}

func filterMiseConfigsScope(configs []misemgr.ConfigFile, scopeFlag string) []misemgr.ConfigFile {
	sf := strings.ToLower(strings.TrimSpace(scopeFlag))
	if sf == "" {
		return configs
	}
	var out []misemgr.ConfigFile
	for _, c := range configs {
		if strings.EqualFold(c.Scope, sf) {
			out = append(out, c)
		}
	}
	return out
}

func scanMiseForCLI(ctx *app.Context, zoneFilter string, checkOutdated bool) (*misemgr.Inventory, error) {
	repos := loadReposBestEffort(ctx)
	return misemgr.Scan(ctx, repos, zoneFilter, checkOutdated)
}

func loadReposBestEffort(ctx *app.Context) []projects.RepoRecord {
	if ctx == nil {
		return nil
	}
	if ctx.WSBaseDir() != "" {
		if db, err := store.Open(store.DefaultPath(ctx.WSBaseDir())); err == nil {
			defer db.Close()
			if rows, rErr := projects.ListCachedRepos(db); rErr == nil && len(rows) > 0 {
				return rows
			}
		}
	}
	repos, err := projects.ScanRepos(ctx, nil)
	if err != nil {
		return nil
	}

	seen := map[string]bool{}
	var out []projects.RepoRecord
	for _, r := range repos {
		p := filepath.Clean(r.LocalPath)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, r)
	}
	return out
}

func nonEmptyCLI(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
