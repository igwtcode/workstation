package cli

import (
	"fmt"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/opcli"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

func newGHCmd(opts *GlobalOpts) *cobra.Command {
	var (
		repoFlag  string
		kindFlag  string
		stateFlag string
		zoneFlag  string
		allFlag   bool
		refresh   bool
		listFlag  bool
		openTUI   bool
	)

	cmd := &cobra.Command{
		Use:     "gh [owner/repo]",
		Aliases: []string{"dash"},
		Short:   "Interactive GitHub PR and issue dashboard",
		Long: "Launch an interactive terminal dashboard for GitHub Pull Requests and Issues in the current " +
			"repository (or across all your configured zones with --all), featuring fuzzy filtering, markdown & " +
			"comment preview, field copy (#/title/URL/milestone), and approve/merge/close/reopen/comment/checkout actions.",
		Example: "  ws gh\n  ws gh owner/repo\n  ws gh --all --zone personal\n  ws gh --json --state open",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}

			repo := strings.TrimSpace(repoFlag)
			if repo == "" && len(args) > 0 {
				repo = strings.TrimSpace(args[0])
			}

			cwdCtx := projects.DetectCWDContext("")
			if repo == "" && !allFlag {
				repo = cwdCtx.RepoSlug
			}

			zone := strings.TrimSpace(zoneFlag)
			if zone == "" {
				if rep, wErr := shell.Whoami(ctx, ""); wErr == nil && rep != nil {
					if pz := strings.TrimSpace(rep.PathZone); pz != "" {
						zone = pz
					} else if !rep.ZoneMismatch {
						zone = strings.TrimSpace(rep.ActiveZone)
					}
				}
			}

			startTab := "gh-prs"
			if strings.EqualFold(strings.TrimSpace(kindFlag), "issue") {
				startTab = "gh-issues"
			}

			tb := NewTUIBackend(ctx)
			if openTUI || (!ctx.JSON && !wantsTSV(cmd) && !listFlag && isInteractiveTTY(cmd, ctx)) {
				defer opcli.DisableTTY()()
				return tui.Run(cmdContext(cmd), tb, tui.Options{
					StartTab:    startTab,
					DefaultZone: zone,
					Zones:       shell.DiscoverZoneNames(ctx),
					GHRepo:      repo,
					GHAllMine:   allFlag || repo == "",
					GHState:     stateFlag,
					GHRefresh:   refresh,
				})
			}

			var items []tui.GHItemRow
			if repo != "" && !allFlag {
				items, err = tb.LoadRepoGHItems(repo, zone, stateFlag, refresh)
			} else {
				items, err = tb.LoadGHItems(refresh, zone)
			}
			if err != nil {
				return err
			}

			filtered := filterGHItemsCLI(items, kindFlag, stateFlag)
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), filtered)
			}
			if len(filtered) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching GitHub PRs or issues.")
				return nil
			}
			rows := make([][]string, 0, len(filtered))
			for _, it := range filtered {
				rows = append(rows, []string{
					strings.ToUpper(it.Kind),
					it.Repo,
					fmt.Sprintf("#%d", it.Number),
					strings.ToUpper(it.State),
					it.Title,
					it.Milestone,
					it.Author,
					it.Zone,
					it.URL,
				})
			}
			cols := []ui.TableColumn{
				{Title: "Kind"},
				{Title: "Repo"},
				{Title: "Item"},
				{Title: "State"},
				{Title: "Title"},
				{Title: "Milestone"},
				{Title: "Author"},
				{Title: "Zone"},
				{Title: "URL"},
			}
			return renderCLITable(cmd, ctx, cols, rows)
		},
	}

	cmd.Flags().StringVarP(&repoFlag, "repo", "R", "", "GitHub owner/repo slug (defaults to CWD repo when present)")
	cmd.Flags().StringVarP(&kindFlag, "kind", "k", "pr", "Initial tab or filter kind: pr, issue, all")
	cmd.Flags().StringVarP(&stateFlag, "state", "s", "open", "Item state filter: open, closed, merged, all")
	cmd.Flags().StringVarP(&zoneFlag, "zone", "z", "", "Workstation zone to use for gh authentication")
	cmd.Flags().BoolVarP(&allFlag, "all", "a", false, "Show PRs and issues across all repositories")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Bypass SQLite cache and fetch fresh GitHub data")
	cmd.Flags().BoolVar(&listFlag, "list", false, "Print non-interactive table output")
	cmd.Flags().BoolVar(&openTUI, "tui", false, "Open the GitHub subview inside the full TUI")
	_ = cmd.RegisterFlagCompletionFunc("zone", completeZoneNames(opts))

	return cmd
}

func filterGHItemsCLI(items []tui.GHItemRow, kindFlag, stateFlag string) []tui.GHItemRow {
	wantKind := strings.ToLower(strings.TrimSpace(kindFlag))
	if wantKind == "all" {
		wantKind = ""
	}
	wantState := strings.ToLower(strings.TrimSpace(stateFlag))
	if wantState == "all" {
		wantState = ""
	}
	out := make([]tui.GHItemRow, 0, len(items))
	for _, it := range items {
		k := strings.ToLower(strings.TrimSpace(it.Kind))
		if wantKind != "" && k != wantKind {
			continue
		}
		st := strings.ToLower(strings.TrimSpace(it.State))
		if wantState != "" && st != wantState {
			continue
		}
		out = append(out, it)
	}
	return out
}
