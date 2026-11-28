package cli

import (
	"fmt"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

func newConfigCmd(opts *GlobalOpts) *cobra.Command {
	cfgCmd := &cobra.Command{
		Use:     "config",
		Aliases: []string{"settings"},
		Short:   "Manage persistent ws settings",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigShow(cmd, opts)
		},
	}

	showCmd := &cobra.Command{
		Use:     "show",
		Aliases: []string{"list"},
		Short:   "Show effective settings and sources",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runConfigShow(cmd, opts)
		},
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config override",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			key := strings.TrimSpace(args[0])
			val := args[1]
			if err := ctx.SetConfigOverride(key, val); err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"key":        key,
					"value":      val,
					"overridden": true,
					"dryRun":     ctx.DryRun,
					"configPath": ctx.WSConfigPath(),
				})
			}
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sSet %s = %q (%s)\n", prefix, key, val, ctx.CollapseHome(ctx.WSConfigPath()))
			return nil
		},
	}

	unsetCmd := &cobra.Command{
		Use:     "unset <key>",
		Aliases: []string{"reset"},
		Short:   "Clear a config override",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			key := strings.TrimSpace(args[0])
			if err := ctx.ResetConfigOverride(key); err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"key":        key,
					"reset":      true,
					"dryRun":     ctx.DryRun,
					"configPath": ctx.WSConfigPath(),
				})
			}
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sReset override for %s (%s)\n", prefix, key, ctx.CollapseHome(ctx.WSConfigPath()))
			return nil
		},
	}

	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"configPath": ctx.WSConfigPath(),
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), ctx.WSConfigPath())
			return nil
		},
	}

	cfgCmd.AddCommand(showCmd, setCmd, unsetCmd, pathCmd)
	return cfgCmd
}

func gatherContextSettingOptions(ctx *app.Context) (themeIDs, presetIDs, zoneNames []string) {
	if ctx == nil {
		return nil, nil, nil
	}
	if ids, err := config.ListThemes(ctx.RepoRoot); err == nil {
		themeIDs = ids
	}
	if presets, err := config.LoadPresets(ctx.RepoRoot); err == nil {
		presetIDs = config.ListPresetIDs(presets)
	}
	seenZones := make(map[string]struct{})
	if zones, err := shell.ZoneStatuses(ctx); err == nil {
		for _, z := range zones {
			if z.Spec.Name != "" {
				zoneNames = append(zoneNames, z.Spec.Name)
				seenZones[z.Spec.Name] = struct{}{}
			}
		}
	}
	for _, zName := range shell.DiscoverZoneNames(ctx) {
		if _, ok := seenZones[zName]; !ok && zName != "" {
			zoneNames = append(zoneNames, zName)
			seenZones[zName] = struct{}{}
		}
	}
	return themeIDs, presetIDs, zoneNames
}

func runConfigShow(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	themeIDs, presetIDs, zoneNames := gatherContextSettingOptions(ctx)
	entries := ctx.ListSettings(themeIDs, presetIDs, zoneNames)
	fp, watched := ctx.ConfigFingerprint()

	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"configPath":   ctx.WSConfigPath(),
			"fingerprint":  fp,
			"settings":     entries,
			"watchedFiles": watched,
		})
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Config file: %s (fingerprint %s)\n", ctx.CollapseHome(ctx.WSConfigPath()), fp)
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		val := e.Value
		if val == "" {
			val = "—"
		}
		rows = append(rows, []string{e.Key, e.Category, val, e.Source, e.Description})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "KEY", MinWidth: 18},
		{Title: "CAT", MinWidth: 9},
		{Title: "VALUE", MinWidth: 14},
		{Title: "SOURCE", MinWidth: 10},
		{Title: "DESCRIPTION", MinWidth: 24},
	}, rows)
}
