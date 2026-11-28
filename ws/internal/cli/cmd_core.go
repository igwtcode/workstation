package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

type applyJSONOutput struct {
	*engine.Plan
	Result *engine.ApplyResult `json:"result,omitempty"`
}

func doctorErrorCount(rep *engine.DoctorReport) int {
	if rep == nil {
		return 0
	}
	n := 0
	for _, f := range rep.Findings {
		switch strings.ToLower(string(f.Severity)) {
		case "err", "error":
			n++
		}
	}
	return n
}

func doctorFindingsError(errCount int) error {
	if errCount <= 0 {
		return nil
	}
	word := "errors"
	if errCount == 1 {
		word = "error"
	}
	return &ExitCodeError{
		Code: 1,
		Err:  fmt.Errorf("doctor reported %d %s", errCount, word),
	}
}

func newDoctorCmd(opts *GlobalOpts) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run workstation health checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			report, err := engine.RunDoctor(ctx)
			if err != nil {
				return err
			}

			var fixRes *engine.ApplyResult
			var skippedBlocked []string
			if fix {
				combined := &engine.Plan{
					Verb:      "apply",
					Host:      engine.FormatPlanHost(ctx),
					Title:     "Doctor auto-fix",
					Preset:    report.Preset,
					Theme:     report.Theme,
					CreatedAt: time.Now().UTC(),
					Metadata:  map[string]string{"partial": "true"},
					Actions:   []engine.Action{},
				}
				for _, f := range report.Findings {
					if f.FixPlan == nil || len(f.FixPlan.Actions) == 0 {
						continue
					}
					allowed := true
					for _, act := range f.FixPlan.Actions {
						if act.RequiresAllow != "" && !ctx.Allow.Satisfies(act.RequiresAllow) {
							allowed = false
							skippedBlocked = append(skippedBlocked, fmt.Sprintf("%s (requires --allow %s)", f.ID, act.RequiresAllow))
							break
						}
					}
					if allowed {
						combined.Actions = append(combined.Actions, f.FixPlan.Actions...)
					}
				}
				if len(combined.Actions) > 0 {
					fixRes, err = engine.ExecApply(ctx, combined)
					if err != nil {
						return err
					}
				}
			}

			errCount := doctorErrorCount(report)
			if fix && !ctx.DryRun && fixRes != nil && fixRes.Applied > 0 {
				if refreshed, rerr := engine.RunDoctor(ctx); rerr == nil {
					errCount = doctorErrorCount(refreshed)
				}
			}

			if ctx.JSON {
				if fix {
					if err := writeJSON(cmd.OutOrStdout(), map[string]any{
						"report":  report,
						"fix":     fixRes,
						"blocked": skippedBlocked,
					}); err != nil {
						return err
					}
					return doctorFindingsError(errCount)
				}
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
				return doctorFindingsError(errCount)
			}

			outW := cmd.OutOrStdout()
			roles := loadWriterRoles(ctx, outW)
			printDoctorReport(outW, roles, report)
			if fix {
				log := ui.NewLogger(outW, roles)
				if fixRes != nil {
					printApplyResult(outW, roles, fixRes, ctx.DryRun)
				} else {
					log.Info("No eligible auto-fixes to apply")
				}
				for _, b := range skippedBlocked {
					log.Skip(b)
				}
			}
			return doctorFindingsError(errCount)
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply safe fixes (or gated fixes permitted by --allow)")
	return cmd
}

func printDoctorReport(w io.Writer, roles ui.Roles, rep *engine.DoctorReport) {
	log := ui.NewLogger(w, roles)
	log.KV("Host", 11, fmt.Sprintf("%s/%s (%s)", rep.Host.OS, rep.Host.Arch, rep.Host.Distro))
	log.KV("Preset", 11, rep.Preset)
	log.KV("Theme", 11, rep.Theme)
	log.KV("Links", 11, fmt.Sprintf("%d/%d ok", rep.LinksOK, rep.LinksTotal))
	log.KV("Templates", 11, fmt.Sprintf("%d/%d ok", rep.TemplatesOK, rep.TemplatesTotal))
	if len(rep.Findings) == 0 {
		log.KV("Status", 11, roles.OK.Render("all checks healthy"))
		return
	}
	log.Section(fmt.Sprintf("Findings (%d)", len(rep.Findings)))
	for _, f := range rep.Findings {
		sev := strings.ToUpper(string(f.Severity))
		sevCell := fmt.Sprintf("%-5s", sev)
		switch strings.ToLower(string(f.Severity)) {
		case "err", "error":
			sevCell = roles.Err.Render(sevCell)
		case "warn", "warning":
			sevCell = roles.Warn.Render(sevCell)
		case "ok":
			sevCell = roles.OK.Render(sevCell)
		default:
			sevCell = roles.Info.Render(sevCell)
		}
		catCell := roles.Muted.Render(fmt.Sprintf("%-10s", f.Category))
		fmt.Fprintf(w, "  %s %s %s\n", sevCell, catCell, f.Title)
		if f.FixCLI != "" {
			fmt.Fprintf(w, "        %s\n", roles.Muted.Render("fix: "+f.FixCLI))
		}
	}
}

func newApplyCmd(opts *GlobalOpts) *cobra.Command {
	var onlyApp string
	var themeFlag string

	cmd := &cobra.Command{
		Use:     "apply",
		Aliases: []string{"sync"},
		Short:   "Render templates and sync symlinks",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}

			var plan *engine.Plan
			if opts.PlanPath != "" {
				plan, err = engine.LoadPlan(opts.PlanPath)
			} else {
				plan, err = engine.PlanApply(ctx, onlyApp, themeFlag)
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
				fmt.Fprintf(cmd.OutOrStdout(), "Saved plan (%d actions) to %s\n", len(plan.Actions), opts.SavePlanPath)
				return nil
			}

			if err := promptMissingAllows(cmd, ctx, plan); err != nil {
				return err
			}

			res, err := engine.ExecApply(ctx, plan)
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

	cmd.Flags().StringVar(&onlyApp, "only", "", "Limit apply to a single app template/link group")
	cmd.Flags().StringVar(&themeFlag, "theme", "", "Override theme palette for this apply")
	addPlanFlags(cmd, opts)
	_ = cmd.RegisterFlagCompletionFunc("only", completeAppNames(opts))
	_ = cmd.RegisterFlagCompletionFunc("theme", completeThemeIDs(opts))
	return cmd
}

func newDiffCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Preview pending apply changes (alias of apply -n)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			plan, err := engine.PlanApply(ctx, "", "")
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), plan)
			}
			if len(plan.Actions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			}
			for _, act := range plan.Actions {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", act.Kind, act.Summary)
				if act.Diff != "" {
					fmt.Fprint(cmd.OutOrStdout(), act.Diff)
				}
			}
			return nil
		},
	}
}

func newUndoCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "undo",
		Short: "Revert the last apply transaction",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			summary, err := engine.UndoLast(ctx)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"summary": summary,
					"dryRun":  ctx.DryRun,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), summary)
			return nil
		},
	}
}

func newEditCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:               "edit [app]",
		Short:             "Edit and re-render an app theme template",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeAppNames(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}

			appName := ""
			if len(args) == 1 {
				appName = args[0]
			} else {
				entries, err := config.LoadThemeRegistry(ctx.RepoRoot)
				if err != nil {
					return err
				}
				if !isPickerInteractiveTTY(cmd, ctx) {
					names := make([]string, 0, len(entries))
					for _, e := range entries {
						names = append(names, e.App)
					}
					return fmt.Errorf("app argument required in non-interactive mode (available: %s)", strings.Join(names, ", "))
				}
				items := make([]picker.Item, 0, len(entries))
				for _, e := range entries {
					items = append(items, picker.Item{
						Title:    e.App,
						Subtitle: e.Template,
						Value:    e.App,
					})
				}
				appName, err = promptSelect(cmd, ctx, "Select app template to edit", items)
				if err != nil {
					return err
				}
			}

			entry, err := engine.ResolveAppTemplate(ctx.RepoRoot, appName)
			if err != nil {
				return err
			}

			outAbs := filepath.Join(ctx.RepoRoot, entry.Output)
			if ctx.DryRun {
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"dryRun":   true,
						"app":      entry.App,
						"template": entry.Template,
						"output":   entry.Output,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would edit %s and re-render %s\n", entry.Template, entry.Output)
				return nil
			}
			var preHash string
			if existing, rerr := os.ReadFile(outAbs); rerr == nil {
				preHash = engine.SHA256Hex(existing)
			} else if !errors.Is(rerr, os.ErrNotExist) {
				return rerr
			}

			tmplAbs := filepath.Join(ctx.RepoRoot, entry.Template)
			edCmd := resolveEditorCmd(tmplAbs)
			edCmd.Stdin = cmd.InOrStdin()
			edCmd.Stdout = cmd.OutOrStdout()
			edCmd.Stderr = cmd.ErrOrStderr()
			if err := edCmd.Run(); err != nil {
				return fmt.Errorf("editor failed: %w", err)
			}

			plan, err := engine.EditAppPlan(ctx, entry, preHash)
			if err != nil {
				return err
			}
			var diffText string
			for _, act := range plan.Actions {
				if act.Diff != "" {
					diffText += act.Diff
				}
			}

			res, err := engine.ExecEditAfterSave(ctx, entry.App)
			if err != nil {
				return err
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"app":      entry.App,
					"template": entry.Template,
					"output":   entry.Output,
					"diff":     diffText,
					"result":   res,
				})
			}
			if diffText != "" {
				fmt.Fprint(cmd.OutOrStdout(), diffText)
			}
			if res.Applied == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s unchanged.\n", entry.App)
				return nil
			}
			printApplyResult(cmd.OutOrStdout(), loadWriterRoles(ctx, cmd.OutOrStdout()), res, ctx.DryRun)
			return nil
		},
	}
}

func applyThemeRun(cmd *cobra.Command, opts *GlobalOpts, themeID, onlyApp string) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	var plan *engine.Plan
	if opts.PlanPath != "" {
		plan, err = engine.LoadPlan(opts.PlanPath)
		if err != nil {
			return err
		}
		if themeID == "" {
			themeID = plan.Theme
		}
	} else {
		if _, err := config.LoadPalette(ctx.RepoRoot, themeID); err != nil {
			return err
		}
		plan, err = engine.PlanApply(ctx, strings.TrimSpace(onlyApp), themeID)
		if err != nil {
			return err
		}
	}
	if opts.SavePlanPath != "" {
		if err := plan.Save(opts.SavePlanPath); err != nil {
			return err
		}
		if ctx.JSON {
			return writeJSON(cmd.OutOrStdout(), plan)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved theme plan (%d actions) to %s\n", len(plan.Actions), opts.SavePlanPath)
		return nil
	}

	if err := promptMissingAllows(cmd, ctx, plan); err != nil {
		return err
	}

	res, err := engine.ExecApply(ctx, plan)
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
	if themeID != "" {
		ui.NewLogger(outW, roles).Info("Theme selected", "theme", themeID)
	}
	printApplyResult(outW, roles, res, ctx.DryRun)
	if skipErr != nil {
		ui.NewLogger(outW, roles).Warn(skipErr.Error())
	}
	return skipErr
}

func newThemeCmd(opts *GlobalOpts) *cobra.Command {
	var listFlag bool
	var onlyApp string

	cmd := &cobra.Command{
		Use:               "theme [id]",
		Short:             "List or apply a color theme",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeThemeIDs(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}

			if listFlag {
				return runThemeList(cmd, ctx)
			}
			if opts.PlanPath != "" && len(args) == 0 {
				return applyThemeRun(cmd, opts, "", onlyApp)
			}

			themeID := ""
			if len(args) == 1 {
				themeID = args[0]
			} else {
				themes, err := config.ListThemes(ctx.RepoRoot)
				if err != nil {
					return err
				}
				if !isPickerInteractiveTTY(cmd, ctx) {
					return runThemeList(cmd, ctx)
				}
				active := resolveActiveThemeID(ctx)
				items := make([]picker.Item, 0, len(themes))
				for _, id := range themes {
					sub := ""
					if id == active {
						sub = "active"
					}
					items = append(items, picker.Item{
						Title:    id,
						Subtitle: sub,
						Value:    id,
					})
				}
				themeID, err = promptSelect(cmd, ctx, "Select theme palette", items)
				if err != nil {
					return err
				}
			}

			return applyThemeRun(cmd, opts, themeID, onlyApp)
		},
	}

	setCmd := &cobra.Command{
		Use:               "set [id]",
		Short:             "Apply a theme palette",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeThemeIDs(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			if id == "" && opts.PlanPath == "" {
				return &UsageError{Err: fmt.Errorf("theme ID or --plan is required")}
			}
			return applyThemeRun(cmd, opts, id, onlyApp)
		},
	}
	addPlanFlags(setCmd, opts)

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List available themes",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				return runThemeList(cmd, ctx)
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show template render status",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				return runThemeStatus(cmd, ctx)
			},
		},
		setCmd,
		newEditCmd(opts),
	)

	cmd.Flags().BoolVar(&listFlag, "list", false, "List available themes")
	_ = cmd.Flags().MarkHidden("list")
	addPlanFlags(cmd, opts)
	cmd.PersistentFlags().StringVar(&onlyApp, "app", "", "Apply theme to a single registered app template")
	return cmd
}

func runThemeList(cmd *cobra.Command, ctx *app.Context) error {
	themes, err := config.ListThemes(ctx.RepoRoot)
	if err != nil {
		return err
	}
	active := resolveActiveThemeID(ctx)
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"active": active,
			"themes": themes,
		})
	}
	rows := make([][]string, 0, len(themes))
	for _, id := range themes {
		mark := ""
		if id == active {
			mark = "*"
		}
		rows = append(rows, []string{mark, id})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "ACT", MinWidth: 3},
		{Title: "THEME", MinWidth: 16},
	}, rows)
}

func runThemeStatus(cmd *cobra.Command, ctx *app.Context) error {
	statuses, err := engine.CheckTemplates(ctx, "")
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), statuses)
	}
	if len(statuses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No theme templates configured.")
		return nil
	}
	rows := make([][]string, 0, len(statuses))
	for _, st := range statuses {
		rows = append(rows, []string{st.App, st.State, st.Output})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "APP", MinWidth: 14},
		{Title: "STATE", MinWidth: 9},
		{Title: "OUTPUT"},
	}, rows)
}

func newPresetCmd(opts *GlobalOpts) *cobra.Command {
	presetCmd := &cobra.Command{
		Use:   "preset",
		Short: "Inspect and select machine presets",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPresetList(cmd, opts)
		},
	}

	presetCmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List available presets",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runPresetList(cmd, opts)
			},
		},
		&cobra.Command{
			Use:               "show <name>",
			Short:             "Show resolved preset details",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completePresetNames(opts),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				presets, err := config.LoadPresets(ctx.RepoRoot)
				if err != nil {
					return err
				}
				resolved, err := config.ResolvePreset(ctx.RepoRoot, presets, args[0])
				if err != nil {
					return err
				}
				var tools []config.ResolvedTool
				if catalog, cerr := config.LoadTools(ctx.RepoRoot); cerr == nil {
					tools = config.ResolveTools(resolved, catalog, ctx.Host)
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"preset": resolved,
						"tools":  tools,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Preset:      %s\n", resolved.ID)
				fmt.Fprintf(cmd.OutOrStdout(), "Description: %s\n", resolved.Description)
				fmt.Fprintf(cmd.OutOrStdout(), "Chain:       %s\n", strings.Join(resolved.Chain, " -> "))
				fmt.Fprintf(cmd.OutOrStdout(), "Secrets:     %s\n", resolved.Secrets)
				fmt.Fprintf(cmd.OutOrStdout(), "Links:       %s\n", strings.Join(resolved.Links, ", "))
				fmt.Fprintf(cmd.OutOrStdout(), "Packages:    %d\n", len(resolved.PackageNames))
				if len(resolved.OSHooks) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "OS Hooks:    %s\n", strings.Join(resolved.OSHooks, ", "))
				}
				return nil
			},
		},
		&cobra.Command{
			Use:               "use <name>",
			Short:             "Set the active preset",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completePresetNames(opts),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, err := opts.buildContext()
				if err != nil {
					return err
				}
				presets, err := config.LoadPresets(ctx.RepoRoot)
				if err != nil {
					return err
				}
				name := args[0]
				if _, ok := presets[name]; !ok {
					return fmt.Errorf("unknown preset %q (available: %s)", name, strings.Join(config.ListPresetIDs(presets), ", "))
				}
				if !ctx.DryRun {
					if err := app.WithLedgerLock(ctx.WSBaseDir(), func() error {
						ledger, err := app.LoadLedger(ctx.WSBaseDir())
						if err != nil {
							return err
						}
						ledger.ActivePreset = name
						return app.SaveLedger(ctx.WSBaseDir(), ledger)
					}); err != nil {
						return err
					}
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"activePreset": name,
						"dryRun":       ctx.DryRun,
					})
				}
				if ctx.DryRun {
					fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would set active preset to %s\n", name)
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Active preset set to %s\n", name)
				return nil
			},
		},
	)

	return presetCmd
}

func runPresetList(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	presets, err := config.LoadPresets(ctx.RepoRoot)
	if err != nil {
		return err
	}
	active := ctx.EffectivePreset()
	ids := config.ListPresetIDs(presets)
	if ctx.JSON {
		type item struct {
			config.Preset
			MatchesHost bool `json:"matchesHost"`
			Active      bool `json:"active"`
		}
		out := make([]item, 0, len(ids))
		for _, id := range ids {
			p := presets[id]
			out = append(out, item{
				Preset:      p,
				MatchesHost: p.MatchesHost(ctx.Host),
				Active:      id == active,
			})
		}
		return writeJSON(cmd.OutOrStdout(), out)
	}
	rows := make([][]string, 0, len(ids))
	for _, id := range ids {
		p := presets[id]
		mark := ""
		if id == active {
			mark = "*"
		} else if p.MatchesHost(ctx.Host) {
			mark = "~"
		}
		rows = append(rows, []string{mark, id, p.Secrets, p.Description})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "ACT", MinWidth: 3},
		{Title: "PRESET", MinWidth: 18},
		{Title: "SECRETS", MinWidth: 10},
		{Title: "DESCRIPTION"},
	}, rows)
}

func printPlanSummary(w io.Writer, roles ui.Roles, plan *engine.Plan, dryRun bool) {
	log := ui.NewLogger(w, roles)
	if dryRun {
		log.Plan(plan.Title, "mode", "dry-run", "actions", len(plan.Actions), "skipped", len(plan.Skipped))
	} else {
		log.Plan(plan.Title, "actions", len(plan.Actions), "skipped", len(plan.Skipped))
	}
	for _, act := range plan.Actions {
		log.PlanItem(string(act.Kind), act.Summary)
	}
	for _, warn := range plan.Warnings {
		log.Warn(warn)
	}
}

func printApplyResult(w io.Writer, roles ui.Roles, res *engine.ApplyResult, dryRun bool) {
	if res == nil {
		return
	}
	log := ui.NewLogger(w, roles)
	if dryRun {
		log.Info("Dry-run summary", "applied", res.Applied, "skipped", res.Skipped)
	} else {
		log.Done("Apply complete", "applied", res.Applied, "skipped", res.Skipped)
	}
	for _, c := range res.Changed {
		log.ChangeItem(c)
	}
	for _, warn := range res.Warnings {
		log.Warn(warn)
	}
	if res.UndoID != "" {
		log.Info("Undo available", "id", res.UndoID)
	}
}
