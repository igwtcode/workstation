package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/opcli"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
)

type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e == nil || e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapCmdExitErr(err error) error {
	if err == nil {
		return nil
	}
	var ec *ExitCodeError
	if errors.As(err, &ec) {
		return err
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 0 {
		return &ExitCodeError{Code: ee.ExitCode(), Err: err}
	}
	return err
}

type UsageError struct {
	Err error
}

func (e *UsageError) Error() string {
	if e == nil || e.Err == nil {
		return "usage error"
	}
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsCancelledError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, picker.ErrCancelled) ||
		errors.Is(err, huh.ErrUserAborted) ||
		errors.Is(err, context.Canceled)
}

func IsUsageError(err error) bool {
	if err == nil {
		return false
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		return true
	}
	s := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, pat := range []string{
		"accepts ",
		"requires ",
		"unknown flag:",
		"unknown shorthand flag:",
		"flag needs an argument:",
		"invalid argument ",
		"unknown command ",
		"required flag(s) ",
		"bad flag syntax:",
		"mutually exclusive",
	} {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

func unknownSubcommandError(cmd *cobra.Command, arg string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown command %q for %q", arg, cmd.CommandPath())
	if !cmd.DisableSuggestions {
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if suggestions := cmd.SuggestionsFor(arg); len(suggestions) > 0 {
			b.WriteString("\n\nDid you mean this?\n")
			for _, s := range suggestions {
				fmt.Fprintf(&b, "\t%s\n", s)
			}
		}
	}
	return &UsageError{Err: errors.New(strings.TrimRight(b.String(), "\n"))}
}

func rejectUnknownSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return unknownSubcommandError(cmd, args[0])
}

func enforceStrictGroupArgs(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	for _, child := range cmd.Commands() {
		if child.HasSubCommands() && child.Args == nil {
			child.Args = rejectUnknownSubcommand
			if !child.Runnable() {
				child.RunE = func(c *cobra.Command, _ []string) error {
					return c.Help()
				}
			}
		}
		enforceStrictGroupArgs(child)
	}
}

func addPlanFlags(cmd *cobra.Command, opts *GlobalOpts) {
	cmd.Flags().StringVar(&opts.PlanPath, "plan", opts.PlanPath, "Execute a previously saved JSON plan file")
	cmd.Flags().StringVar(&opts.SavePlanPath, "save-plan", opts.SavePlanPath, "Write computed plan to a JSON file without executing")
}

func hideCmd(cmd *cobra.Command) *cobra.Command {
	cmd.Hidden = true
	return cmd
}

func ResolveCommandForArgs(root *cobra.Command, rawArgs []string) *cobra.Command {
	if root == nil {
		return nil
	}
	cur := root
	for i := 0; i < len(rawArgs); i++ {
		arg := rawArgs[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name, _, hasEq := strings.Cut(arg[2:], "=")
			if !hasEq {
				if fl := cur.Flags().Lookup(name); fl != nil && fl.NoOptDefVal == "" {
					i++
				} else if fl := cur.InheritedFlags().Lookup(name); fl != nil && fl.NoOptDefVal == "" {
					i++
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			if !strings.Contains(arg, "=") && len(arg) == 2 {
				sh := arg[1:2]
				if fl := cur.Flags().ShorthandLookup(sh); fl != nil && fl.NoOptDefVal == "" {
					i++
				} else if fl := cur.InheritedFlags().ShorthandLookup(sh); fl != nil && fl.NoOptDefVal == "" {
					i++
				}
			}
			continue
		}
		var matched *cobra.Command
		for _, child := range cur.Commands() {
			if child.Name() == arg || child.HasAlias(arg) {
				matched = child
				break
			}
		}
		if matched == nil {
			break
		}
		cur = matched
	}
	return cur
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec *ExitCodeError
	if errors.As(err, &ec) && ec.Code != 0 {
		return ec.Code
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() != 0 {
		return ee.ExitCode()
	}
	if IsCancelledError(err) {
		return 130
	}
	if IsUsageError(err) {
		return 2
	}
	return 1
}

const (
	groupWorkstation   = "workstation"
	groupWorkflow      = "workflow"
	groupSecretsState  = "secrets-state"
	groupPackagesFleet = "packages-fleet"
)

func withGroup(cmd *cobra.Command, groupID string) *cobra.Command {
	cmd.GroupID = groupID
	return cmd
}

type GlobalOpts struct {
	Repo             string
	Home             string
	Preset           string
	DryRun           bool
	NonInteractive   bool
	ForceInteractive bool
	Allow            string
	JSON             bool
	TSV              bool
	PlanPath         string
	SavePlanPath     string
	Version          string
}

func NewRootCmd(opts *GlobalOpts) *cobra.Command {
	if opts == nil {
		opts = &GlobalOpts{}
	}

	root := &cobra.Command{
		Use:           "ws",
		Short:         "Manage workstation dotfiles, packages, themes, and zones",
		Long:          "Manage workstation dotfiles, packages, themes, and zones.\n\nPreview convention: destructive or remote commands preview by default and require --execute; other mutating commands run by default and honor -n/--dry-run (ws diff is the alias of ws apply -n).",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.JSON && opts.TSV {
				return &UsageError{Err: fmt.Errorf("--json and --tsv are mutually exclusive")}
			}
			if opts.PlanPath != "" && opts.SavePlanPath != "" {
				return &UsageError{Err: fmt.Errorf("--plan and --save-plan are mutually exclusive")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if len(args) > 0 {
				plugin, perr := shell.FindPlugin(ctx, args[0])
				if perr == nil && plugin != nil {
					return runPlugin(ctx, cmd, plugin, args[1:])
				}
				return unknownSubcommandError(cmd, args[0])
			}

			if isInteractiveTTY(cmd, ctx) {
				return runTUI(cmd, ctx, "", opts.Version)
			}

			return cmd.Help()
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&opts.Repo, "repo", opts.Repo, "Path to workstation repository")
	pf.StringVar(&opts.Preset, "preset", opts.Preset, "Override active machine preset")
	pf.BoolVarP(&opts.DryRun, "dry-run", "n", opts.DryRun, "Preview changes without mutating the machine")
	pf.BoolVar(&opts.NonInteractive, "non-interactive", opts.NonInteractive, "Disable prompts and fail/skip guarded actions")
	pf.StringVar(&opts.Allow, "allow", opts.Allow, "Comma-separated consent scopes: replace,sudo,remove,script,all")
	pf.BoolVar(&opts.JSON, "json", opts.JSON, "Emit machine-readable JSON output")
	pf.BoolVar(&opts.TSV, "tsv", opts.TSV, "Emit plain tab-separated values for tables")

	_ = root.RegisterFlagCompletionFunc("preset", completePresetNames(opts))
	_ = root.RegisterFlagCompletionFunc("allow", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"replace", "sudo", "remove", "script", "all"}, cobra.ShellCompDirectiveNoFileComp
	})

	root.AddGroup(
		&cobra.Group{ID: groupWorkstation, Title: "Workstation"},
		&cobra.Group{ID: groupWorkflow, Title: "Workflow"},
		&cobra.Group{ID: groupSecretsState, Title: "Secrets & State"},
		&cobra.Group{ID: groupPackagesFleet, Title: "Packages & Fleet"},
	)

	envCmd := withTUIFlag(newEnvCmd(opts), opts, "env")
	envCmd.Short = "Trace and rotate environment variables"

	root.AddCommand(
		withGroup(newTUICmd(opts), groupWorkstation),
		withGroup(withTUIFlag(newStatusCmd(opts), opts, "overview"), groupWorkstation),
		withGroup(withTUIFlag(newDoctorCmd(opts), opts, "doctor"), groupWorkstation),
		withGroup(newApplyCmd(opts), groupWorkstation),
		withGroup(newDiffCmd(opts), groupWorkstation),
		withGroup(newUndoCmd(opts), groupWorkstation),
		withGroup(withTUIFlag(newThemeCmd(opts), opts, "themes"), groupWorkstation),
		withGroup(withTUIFlag(newPresetCmd(opts), opts, "presets"), groupWorkstation),
		withGroup(withTUIFlag(newConfigCmd(opts), opts, "settings"), groupWorkstation),
		withGroup(withTUIFlag(newRepoCmd(opts), opts, "projects"), groupWorkflow),
		withGroup(newGHCmd(opts), groupWorkflow),
		withGroup(newZoneCmd(opts), groupWorkflow),
		withGroup(newWhoamiCmd(opts), groupWorkflow),
		withGroup(newAWSCmd(opts), groupWorkflow),
		withGroup(withTUIFlag(newMiseCmd(opts), opts, "mise"), groupWorkflow),
		withGroup(withTUIFlag(newHistCmd(opts), opts, "history"), groupWorkflow),
		withGroup(envCmd, groupSecretsState),
		withGroup(withTUIFlag(newVaultCmd(opts), opts, "vault"), groupSecretsState),
		withGroup(newDBCmd(opts), groupSecretsState),
		withGroup(withTUIFlag(newPkgCmd(opts), opts, "pkgs"), groupPackagesFleet),
		withGroup(newUpdateCmd(opts), groupPackagesFleet),
		withGroup(newBootstrapCmd(opts), groupPackagesFleet),
		withGroup(withTUIFlag(newCleanCmd(opts), opts, "cleanup"), groupPackagesFleet),
		withGroup(withTUIFlag(newFleetCmd(opts), opts, "fleet"), groupPackagesFleet),
		hideCmd(newEditCmd(opts)),
		hideCmd(newRunCmd(opts)),
		hideCmd(newScheduleCmd(opts)),
		hideCmd(newGenCmd(opts)),
		hideCmd(newOSEnvCmd(opts)),
		hideCmd(newAndroidCmd(opts)),
		hideCmd(newPluginCmd(opts)),
		hideCmd(newTestVMCmd(opts)),
	)

	enforceStrictGroupArgs(root)
	return root
}

func TryPreDispatchPlugin(ctx context.Context, root *cobra.Command, opts *GlobalOpts, rawArgs []string) (bool, error) {
	if root == nil || opts == nil {
		return false, nil
	}
	localOpts := *opts
	idx := 0
	for idx < len(rawArgs) {
		arg := rawArgs[idx]
		if arg == "--" {
			return false, nil
		}
		if arg == "-h" || arg == "--help" || arg == "-v" || arg == "--version" {
			return false, nil
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			break
		}
		flagName, val, hasEq := strings.Cut(arg, "=")
		consumeNext := func(dst *string) bool {
			if hasEq {
				*dst = val
				return true
			}
			if idx+1 < len(rawArgs) {
				*dst = rawArgs[idx+1]
				idx++
				return true
			}
			return false
		}
		switch flagName {
		case "--repo":
			if !consumeNext(&localOpts.Repo) {
				return false, nil
			}
		case "--preset":
			if !consumeNext(&localOpts.Preset) {
				return false, nil
			}
		case "--allow":
			if !consumeNext(&localOpts.Allow) {
				return false, nil
			}
		case "-n", "--dry-run":
			localOpts.DryRun = true
		case "--non-interactive":
			localOpts.NonInteractive = true
		case "--json":
			localOpts.JSON = true
		case "--tsv":
			localOpts.TSV = true
		default:
			return false, nil
		}
		idx++
	}
	if idx >= len(rawArgs) {
		return false, nil
	}
	tok := strings.TrimSpace(rawArgs[idx])
	if tok == "" || tok == "help" || tok == "completion" || strings.HasPrefix(tok, "__complete") {
		return false, nil
	}
	for _, c := range root.Commands() {
		if c.Name() == tok || c.HasAlias(tok) {
			return false, nil
		}
	}
	appCtx, err := localOpts.buildContext()
	if err != nil {
		return false, nil
	}
	plugin, perr := shell.FindPlugin(appCtx, tok)
	if perr != nil || plugin == nil {
		return false, nil
	}
	root.SetContext(ctx)
	return true, runPlugin(appCtx, root, plugin, rawArgs[idx+1:])
}

func (o *GlobalOpts) buildContext() (*app.Context, error) {
	if o.JSON && o.TSV {
		return nil, fmt.Errorf("--json and --tsv are mutually exclusive")
	}
	nonInteractive := o.NonInteractive
	if !nonInteractive && !o.ForceInteractive && !isCharDevice(os.Stdin) {
		nonInteractive = true
	}
	return app.NewContext(app.Options{
		RepoRoot:       o.Repo,
		HomeDir:        o.Home,
		DryRun:         o.DryRun,
		NonInteractive: nonInteractive,
		JSON:           o.JSON,
		AllowRaw:       o.Allow,
		PresetOverride: o.Preset,
	})
}

func isValidTUITab(tab string) bool {
	switch strings.ToLower(strings.TrimSpace(tab)) {
	case "",
		"overview", "status", "home", "1", "8",
		"projects", "project", "repos", "repo", "git", "gg", "2",
		"gh", "gh-prs", "gh-issues", "prs", "pr", "issues", "issue",
		"packages", "pkg", "pkgs", "installed", "system", "sys", "3",
		"pkg-search", "search", "browse", "pkg-browse",
		"mise", "mise-tools", "mise-configs", "mise-outdated",
		"cleanup", "clean",
		"env", "secrets", "tokens", "vars", "4",
		"envfiles", "files",
		"vault", "db", "1password", "op",
		"fleet", "nodes", "tailscale", "vm", "filesync", "sync-files", "diff-files", "fleet-diff", "5",
		"workstation", "ws", "doctor", "doc", "6",
		"themes", "theme",
		"presets", "preset",
		"settings", "prefs", "config", "7",
		"watched", "watched-files",
		"history", "hist",
		"output", "out", "jobs", "0":
		return true
	default:
		return false
	}
}

func runTUI(cmd *cobra.Command, ctx *app.Context, startTab, version string) error {
	return runTUIWithOptions(cmd, ctx, tui.Options{
		StartTab: startTab,
		Version:  version,
	})
}

func runTUIWithOptions(cmd *cobra.Command, ctx *app.Context, opts tui.Options) error {
	startTab := opts.StartTab
	if !isValidTUITab(startTab) {
		return fmt.Errorf("unknown TUI tab %q (expected overview, projects, prs, issues, gh, packages, pkg-search, mise-tools, mise, cleanup, env, envfiles, vault, fleet, filesync, workstation, doctor, themes, presets, settings, watched, history, or output)", startTab)
	}
	if !isInteractiveTTY(cmd, ctx) {
		return fmt.Errorf("interactive terminal required for TUI")
	}
	backend := NewTUIBackend(ctx)
	defer func() { _ = backend.Close() }()

	cfg := ctx.EffectiveConfig()
	if strings.TrimSpace(startTab) == "" {
		if ctx.Config != nil && strings.TrimSpace(ctx.Config.TUI.StartTab) != "" && isValidTUITab(ctx.Config.TUI.StartTab) {
			startTab = ctx.Config.TUI.StartTab
		} else {
			startTab = "overview"
		}
	}

	var zones []string
	if statuses, zerr := shell.ZoneStatuses(ctx); zerr == nil {
		for _, st := range statuses {
			if st.Spec.Name != "" {
				zones = append(zones, st.Spec.Name)
			}
		}
	}
	defaultZone := strings.TrimSpace(opts.DefaultZone)
	if defaultZone == "" {
		if rep, werr := shell.Whoami(ctx, ""); werr == nil {
			defaultZone = rep.ActiveZone
		}
		if strings.TrimSpace(cfg.TUI.DefaultZone) != "" {
			defaultZone = strings.TrimSpace(cfg.TUI.DefaultZone)
		}
	}

	opts.StartTab = startTab
	if len(opts.Zones) == 0 {
		opts.Zones = zones
	}
	opts.DefaultZone = defaultZone
	if !opts.HasCDFile {
		opts.HasCDFile = strings.TrimSpace(os.Getenv("WS_TUI_CD_FILE")) != ""
	}

	restoreOpTTY := opcli.DisableTTY()
	res, err := tui.RunWithResult(cmdContext(cmd), backend, opts)
	restoreOpTTY()
	if err != nil {
		return err
	}
	if res.SelectedRepoPath != "" {
		if cdFile := strings.TrimSpace(os.Getenv("WS_TUI_CD_FILE")); cdFile != "" {
			if werr := os.WriteFile(cdFile, []byte(res.SelectedRepoPath+"\n"), 0o600); werr != nil {
				return werr
			}
		}
	}
	return nil
}

func withTUIFlag(cmd *cobra.Command, opts *GlobalOpts, tab string) *cobra.Command {
	if cmd == nil {
		return nil
	}
	var openTUI bool
	cmd.Flags().BoolVar(&openTUI, "tui", false, "Open this view in the interactive TUI")
	prevArgs := cmd.Args
	if prevArgs != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if openTUI {
				return cobra.MaximumNArgs(1)(c, args)
			}
			return prevArgs(c, args)
		}
	}
	origRunE := cmd.RunE
	origRun := cmd.Run
	cmd.Run = nil
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if openTUI {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			return runTUI(c, ctx, tab, opts.Version)
		}
		if origRunE != nil {
			return origRunE(c, args)
		}
		if origRun != nil {
			origRun(c, args)
			return nil
		}
		return c.Help()
	}
	return cmd
}

func newTUICmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:       "tui [view]",
		Short:     "Launch the interactive terminal UI",
		Long:      "Launch the interactive terminal UI.\n\nViews: overview (status), projects (repos, gh, prs, issues), packages (pkgs, pkg-search, mise-tools, mise, cleanup), env (secrets, envfiles, vault), fleet (nodes, filesync), workstation (doctor, themes, presets, settings, watched, history), output.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"overview", "status", "projects", "repos", "prs", "issues", "gh", "packages", "pkgs", "pkg-search", "mise-tools", "mise", "cleanup", "env", "secrets", "envfiles", "vault", "fleet", "nodes", "filesync", "workstation", "doctor", "themes", "presets", "settings", "watched", "history", "output"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			tab := ""
			if len(args) == 1 {
				tab = args[0]
			}
			return runTUI(cmd, ctx, tab, opts.Version)
		},
	}
}

func newPluginCmd(opts *GlobalOpts) *cobra.Command {
	pluginCmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage ws-* and local task plugins",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginList(cmd, opts)
		},
	}
	pluginCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available plugins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginList(cmd, opts)
		},
	})
	return pluginCmd
}

func runPluginList(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	plugins, err := shell.DiscoverPlugins(ctx)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), plugins)
	}
	if len(plugins) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No plugins found.")
		return nil
	}
	rows := make([][]string, 0, len(plugins))
	for _, p := range plugins {
		rows = append(rows, []string{p.Name, p.Source, p.Path})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "PLUGIN", MinWidth: 16},
		{Title: "SOURCE", MinWidth: 10},
		{Title: "PATH"},
	}, rows)
}

func runPlugin(ctx *app.Context, cmd *cobra.Command, plugin *shell.Plugin, args []string) error {
	if ctx.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] plugin %s: %s %s\n", plugin.Name, plugin.Path, strings.Join(args, " "))
		return nil
	}

	overlay := []string{
		"WS_REPO=" + ctx.RepoRoot,
		"WS_DIR=" + ctx.RepoRoot,
	}

	themeID := resolveActiveThemeID(ctx)
	if themeID != "" {
		overlay = append(overlay, "WS_THEME="+themeID)
		if pal, err := config.LoadPalette(ctx.RepoRoot, themeID); err == nil {
			overlay = append(overlay,
				"WS_THEME_NAME="+pal.Name,
				"WS_THEME_MODE="+pal.Mode,
			)
			keys := make([]string, 0, len(pal.Tokens))
			for k := range pal.Tokens {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				envKey := "WS_THEME_" + strings.ToUpper(k)
				overlay = append(overlay, envKey+"="+pal.Tokens[k])
			}
		}
	}

	c := exec.CommandContext(cmdContext(cmd), plugin.Path, args...)
	c.Env = shell.MergeEnv(os.Environ(), overlay)
	c.Stdin = cmd.InOrStdin()
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return wrapCmdExitErr(c.Run())
}

func cmdContext(cmd *cobra.Command) context.Context {
	if cmd != nil && cmd.Context() != nil {
		return cmd.Context()
	}
	return context.Background()
}

func resolveActiveThemeID(ctx *app.Context) string {
	if ctx == nil {
		return "gruvbox-dark"
	}
	if t := strings.TrimSpace(ctx.EffectiveConfig().TUI.Theme); t != "" {
		if _, err := config.LoadPalette(ctx.RepoRoot, t); err == nil {
			return t
		}
	}
	if ledger, err := app.LoadLedger(ctx.WSBaseDir()); err == nil && ledger.ActiveTheme != "" {
		return ledger.ActiveTheme
	}
	if _, err := config.LoadPalette(ctx.RepoRoot, "gruvbox-dark"); err == nil {
		return "gruvbox-dark"
	}
	if ids, err := config.ListThemes(ctx.RepoRoot); err == nil && len(ids) > 0 {
		return ids[0]
	}
	return "gruvbox-dark"
}

func isInteractiveTTY(cmd *cobra.Command, ctx *app.Context) bool {
	if ctx == nil || ctx.NonInteractive || ctx.JSON {
		return false
	}
	if cmd != nil && cmd.OutOrStdout() != os.Stdout {
		return false
	}
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isPickerInteractiveTTY(cmd *cobra.Command, ctx *app.Context) bool {
	return isPickerInteractiveTTYWith(cmd, ctx, picker.HasInteractiveTTY)
}

func isPickerInteractiveTTYWith(cmd *cobra.Command, ctx *app.Context, hasTTY func() bool) bool {
	if ctx == nil || ctx.NonInteractive || ctx.JSON {
		return false
	}
	if cmd != nil && cmd.OutOrStdout() != os.Stdout {
		return false
	}
	if hasTTY != nil {
		return hasTTY()
	}
	return picker.HasInteractiveTTY()
}

func isCharDevice(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func loadActivePalette(ctx *app.Context) config.Palette {
	if ctx == nil || ctx.RepoRoot == "" {
		return config.Palette{}
	}
	themeID := resolveActiveThemeID(ctx)
	if pal, err := config.LoadPalette(ctx.RepoRoot, themeID); err == nil {
		return pal
	}
	if pal, err := config.LoadPalette(ctx.RepoRoot, "gruvbox-dark"); err == nil {
		return pal
	}
	return config.Palette{}
}

func loadWriterRoles(ctx *app.Context, w io.Writer) ui.Roles {
	return ui.NewRolesForWriter(loadActivePalette(ctx), w)
}

func wantsTSV(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	tsv, err := cmd.Flags().GetBool("tsv")
	return err == nil && tsv
}

func renderCLITable(cmd *cobra.Command, ctx *app.Context, cols []ui.TableColumn, rows [][]string) error {
	var w io.Writer = os.Stdout
	if cmd != nil {
		w = cmd.OutOrStdout()
	}
	return renderCLITableTo(cmd, ctx, w, cols, rows)
}

func renderCLITableTo(cmd *cobra.Command, ctx *app.Context, w io.Writer, cols []ui.TableColumn, rows [][]string) error {
	if w == nil {
		w = os.Stdout
	}
	if wantsTSV(cmd) {
		return ui.RenderTSV(w, rows)
	}
	return ui.RenderTable(w, cols, rows, loadWriterRoles(ctx, w))
}

func promptSelect(cmd *cobra.Command, ctx *app.Context, title string, items []picker.Item) (string, error) {
	chosen, err := picker.Pick(cmdContext(cmd), picker.Options{
		Title:   title,
		Items:   items,
		Palette: loadActivePalette(ctx),
	})
	if err != nil {
		return "", err
	}
	return chosen.Value, nil
}

func resolveEditorCmd(filePath string) *exec.Cmd {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	var parts []string
	if editor != "" {
		parts = strings.Fields(editor)
	} else if _, err := exec.LookPath("nvim"); err == nil {
		parts = []string{"nvim"}
	} else {
		parts = []string{"vi"}
	}
	cmdArgs := append(parts[1:], filePath)
	c := exec.Command(parts[0], cmdArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

func allowGranted(a app.AllowSet, scope string) bool {
	return a.Satisfies(scope)
}

func completePresetNames(opts *GlobalOpts) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, err := opts.buildContext()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		presets, err := config.LoadPresets(ctx.RepoRoot)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return config.ListPresetIDs(presets), cobra.ShellCompDirectiveNoFileComp
	}
}

func completeThemeIDs(opts *GlobalOpts) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, err := opts.buildContext()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ids, err := config.ListThemes(ctx.RepoRoot)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeAppNames(opts *GlobalOpts) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, err := opts.buildContext()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		entries, err := config.LoadThemeRegistry(ctx.RepoRoot)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		seen := map[string]bool{}
		var apps []string
		for _, e := range entries {
			if !seen[e.App] {
				seen[e.App] = true
				apps = append(apps, e.App)
			}
		}
		sort.Strings(apps)
		return apps, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeZoneNames(opts *GlobalOpts) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		ctx, err := opts.buildContext()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		statuses, err := shell.ZoneStatuses(ctx)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names := make([]string, 0, len(statuses))
		for _, st := range statuses {
			names = append(names, st.Spec.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
