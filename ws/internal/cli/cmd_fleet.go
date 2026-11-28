package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/fleet"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

func newFleetCmd(opts *GlobalOpts) *cobra.Command {
	fleetCmd := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect and sync Tailscale peers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFleetStatus(cmd, opts)
		},
	}

	fleetCmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "List Tailscale nodes",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runFleetStatus(cmd, opts)
			},
		},
		newFleetJoinCmd(opts),
		newFleetTagsCmd(opts),
		newFleetSyncCmd(opts),
		newFleetDiffCmd(opts),
		newFleetSyncFilesCmd(opts),
	)

	return fleetCmd
}

func runFleetStatus(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	nodes, err := fleet.ListTailscaleNodes(ctx, nil)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), nodes)
	}
	if len(nodes) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No Tailscale nodes discovered.")
		return nil
	}
	rows := make([][]string, 0, len(nodes))
	for _, n := range nodes {
		state := "offline"
		if n.Online {
			state = "online"
		}
		sshTag := "-"
		if n.SSHEnabled {
			sshTag = "ssh"
		}
		plat := strings.Trim(n.OS+"/"+n.Arch, "/")
		if plat == "" {
			plat = "-"
		}
		ip := n.TailscaleIP
		if ip == "" {
			ip = "-"
		}
		rows = append(rows, []string{n.Name, state, sshTag, plat, ip, strings.Join(n.Tags, ",")})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "NODE", MinWidth: 18},
		{Title: "STATE", MinWidth: 7},
		{Title: "SSH", MinWidth: 3},
		{Title: "PLATFORM", MinWidth: 12},
		{Title: "TAILSCALE IP", MinWidth: 15},
		{Title: "TAGS"},
	}, rows)
}

func newFleetJoinCmd(opts *GlobalOpts) *cobra.Command {
	var hostname string
	var tags []string
	var authKeyEnv string
	var sshFlag bool

	cmd := &cobra.Command{
		Use:   "join",
		Short: "Print a Tailscale join command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			joinCmd, err := fleet.RenderJoinCommand(fleet.JoinOptions{
				Hostname:   hostname,
				Tags:       flattenCSV(tags),
				AuthKeyEnv: authKeyEnv,
				SSH:        sshFlag,
			})
			if err != nil {
				return err
			}
			if opts.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"command": joinCmd,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), joinCmd)
			return nil
		},
	}

	cmd.Flags().StringVar(&hostname, "hostname", "", "Hostname to advertise on tailnet")
	cmd.Flags().StringSliceVar(&tags, "tags", nil, "Comma-separated Tailscale tags (e.g. tag:dev,tag:vm)")
	cmd.Flags().StringVar(&authKeyEnv, "authkey-env", "TAILSCALE_AUTHKEY", "Environment variable holding the Tailscale auth key")
	cmd.Flags().BoolVar(&sshFlag, "ssh", true, "Enable Tailscale SSH")
	return cmd
}

func newFleetTagsCmd(opts *GlobalOpts) *cobra.Command {
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "tags <node> <tag1,tag2...>",
		Short: "Preview or set remote Tailscale tags",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			node := strings.TrimSpace(args[0])
			if err := fleet.ValidateNodeTarget(node); err != nil {
				return err
			}
			tagList := flattenCSV([]string{args[1]})
			remoteCmd, err := fleet.RenderTagsCommand(tagList)
			if err != nil {
				return err
			}
			sshArgs := []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "--", node, remoteCmd}

			if !executeFlag || ctx.DryRun {
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"dryRun":  true,
						"node":    node,
						"tags":    tagList,
						"command": sshArgs,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] %s\n", strings.Join(sshArgs, " "))
				return nil
			}

			c := exec.CommandContext(cmdContext(cmd), sshArgs[0], sshArgs[1:]...)
			var outBuf bytes.Buffer
			if ctx.JSON {
				c.Stdout = &outBuf
				c.Stderr = &outBuf
			} else {
				c.Stdin = cmd.InOrStdin()
				c.Stdout = cmd.OutOrStdout()
				c.Stderr = cmd.ErrOrStderr()
			}
			if err := c.Run(); err != nil {
				if ctx.JSON && outBuf.Len() > 0 {
					return wrapCmdExitErr(fmt.Errorf("%w: %s", err, strings.TrimSpace(outBuf.String())))
				}
				return wrapCmdExitErr(err)
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun":  false,
					"node":    node,
					"tags":    tagList,
					"applied": true,
					"output":  strings.TrimRight(outBuf.String(), "\r\n"),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated tags on %s: %s\n", node, strings.Join(tagList, ","))
			return nil
		},
	}

	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Apply tags remotely over SSH (default is dry-run)")
	return cmd
}

func newFleetSyncCmd(opts *GlobalOpts) *cobra.Command {
	var transport string
	var targetID string
	var remotePreset string
	var arch string
	var binaryPath string
	var noZones bool
	var repos []string
	var pushEnv bool
	var excludePaths []string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "sync <node>",
		Short: "Plan or run headless peer sync",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			targetPreset := strings.TrimSpace(remotePreset)
			if !cmd.Flags().Changed("remote-preset") && strings.TrimSpace(opts.Preset) != "" {
				targetPreset = strings.TrimSpace(opts.Preset)
			}
			if targetPreset == "" {
				targetPreset = "ubuntu-runner"
			}

			var db *store.DB
			var kr *store.Keyring
			dbExists := false
			if _, statErr := os.Stat(store.DefaultPath(ctx.WSBaseDir())); statErr == nil {
				dbExists = true
			}
			if pushEnv || (!ctx.DryRun && dbExists) {
				openedDB, openedKR, derr := openStoreAndKey(ctx)
				if derr != nil {
					return fmt.Errorf("open ws.db and keyring: %w", derr)
				}
				db = openedDB
				kr = openedKR
				defer func() { _ = db.Close() }()
			} else if dbExists {
				if openedDB, openedKR, derr := openStoreAndKey(ctx); derr == nil {
					db = openedDB
					kr = openedKR
					defer func() { _ = db.Close() }()
				}
			}

			spec := fleet.SyncSpec{
				Node:          args[0],
				Transport:     transport,
				TargetID:      targetID,
				Preset:        targetPreset,
				Arch:          arch,
				BinaryPath:    binaryPath,
				NoZones:       noZones,
				PushEnvFiles:  pushEnv,
				SelectedRepos: flattenCSV(repos),
				ExcludePaths:  flattenCSV(excludePaths),
			}

			plan, err := fleet.PlanFleetSync(ctx, db, kr, spec)
			if err != nil {
				return err
			}

			executed := false
			if executeFlag && !ctx.DryRun {
				if err := fleet.ExecFleetSync(ctx, db, kr, plan, nil); err != nil {
					return err
				}
				executed = true
			}

			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun":   ctx.DryRun,
					"executed": executed,
					"plan":     plan,
				})
			}

			w := cmd.OutOrStdout()
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(w, "%sFleet Sync Plan: %s (%s -> %s, preset=%s, arch=%s, no-zones=%v, push-env=%v)\n",
				prefix, plan.Spec.Node, plan.Spec.Transport, plan.Spec.TargetID, plan.Spec.Preset, plan.Spec.Arch, plan.Spec.NoZones, plan.Spec.PushEnvFiles)
			fmt.Fprintf(w, "\nSteps (%d):\n", len(plan.Steps))
			for i, st := range plan.Steps {
				fmt.Fprintf(w, "  %d. [%s] %s — %s\n", i+1, st.ID, st.Title, st.Detail)
			}
			if len(plan.SkippedConfigs) > 0 {
				fmt.Fprintf(w, "\nSkipped GUI/macOS configs (%d): %s\n", len(plan.SkippedConfigs), strings.Join(plan.SkippedConfigs, ", "))
			}
			fmt.Fprintf(w, "Encrypted .env files matched: %d\n", plan.EnvFilesCount)
			for _, warn := range plan.Warnings {
				fmt.Fprintf(w, "  ! %s\n", warn)
			}
			fmt.Fprintf(w, "\nRemaining Manual Steps Checklist (%d):\n", len(plan.ManualChecklist))
			for _, item := range plan.ManualChecklist {
				mark := "[ ]"
				if item.Done {
					mark = "[x]"
				}
				fmt.Fprintf(w, "  %s %s — $ %s\n", mark, item.Title, item.Command)
			}
			if ctx.DryRun && !executeFlag {
				fmt.Fprintln(w, "\nPreview only (dry-run by default). Re-run with --execute to sync.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&transport, "transport", "tailscale-ssh", "Transport: tailscale-ssh")
	cmd.Flags().StringVar(&targetID, "target", "", "SSH target (defaults to <node>)")
	cmd.Flags().StringVar(&remotePreset, "remote-preset", "ubuntu-runner", "Remote workstation preset")
	cmd.Flags().StringVar(&arch, "arch", "amd64", "Remote Linux architecture: amd64 or arm64")
	cmd.Flags().StringVar(&binaryPath, "binary", "", "Local path to ws-linux-<arch> static binary")
	cmd.Flags().BoolVar(&noZones, "no-zones", false, "Use flat ~/code layout without identity zones")
	cmd.Flags().StringSliceVar(&repos, "repos", nil, "Comma-separated repository names/IDs/URLs to clone")
	cmd.Flags().BoolVar(&pushEnv, "push-env", false, "Stream matching decrypted .env files from ws.db over stdin (0600)")
	cmd.Flags().StringSliceVar(&excludePaths, "exclude", nil, "Comma-separated GUI/macOS config groups to exclude")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Execute remote sync steps (default is dry-run preview)")
	return cmd
}

func flattenCSV(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func resolveFleetFileSyncTarget(ctx *app.Context, node, explicitTarget string) (string, error) {
	if t := strings.TrimSpace(explicitTarget); t != "" {
		if err := fleet.ValidateNodeTarget(t); err != nil {
			return "", err
		}
		return t, nil
	}
	want := strings.TrimSpace(node)
	if nodes, err := fleet.ListTailscaleNodes(ctx, nil); err == nil {
		for _, n := range nodes {
			if strings.EqualFold(n.Name, want) || strings.EqualFold(n.DNSName, want) || n.TailscaleIP == want {
				if n.IsSelf {
					return "", fmt.Errorf("cannot diff or sync files against self node %q", n.Name)
				}
				if n.TargetID != "" {
					return n.TargetID, nil
				}
			}
		}
	}
	if err := fleet.ValidateNodeTarget(want); err != nil {
		return "", err
	}
	return want, nil
}

func newFleetDiffCmd(opts *GlobalOpts) *cobra.Command {
	var targetID string
	var includeEnv bool
	var includeDB bool
	var showContent bool

	cmd := &cobra.Command{
		Use:   "diff <node> [paths...]",
		Short: "Compare local and remote peer files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			node := strings.TrimSpace(args[0])
			target, err := resolveFleetFileSyncTarget(ctx, node, targetID)
			if err != nil {
				return err
			}
			rep, err := fleet.DiffNodeFiles(ctx, node, target, fleet.FileSyncDiffOptions{
				IncludeEnv: includeEnv,
				IncludeDB:  includeDB,
				ExtraPaths: args[1:],
			})
			if err != nil {
				return err
			}

			var previews map[string]string
			if showContent {
				previews = make(map[string]string)
				for _, e := range rep.Entries {
					if e.Status == "same" {
						continue
					}
					if fleet.IsSensitiveSyncPath(e.RelPath) {
						previews[e.RelPath] = "Redacted: sensitive file content diff disabled."
						continue
					}
					diffText, derr := fleet.PreviewFileTextDiff(ctx, target, e.RelPath, nil)
					if derr != nil {
						previews[e.RelPath] = fmt.Sprintf("error: %v", derr)
					} else {
						previews[e.RelPath] = diffText
					}
				}
			}

			if ctx.JSON {
				type diffJSONOut struct {
					fleet.FileDiffReport
					TextDiffs map[string]string `json:"textDiffs,omitempty"`
				}
				return writeJSON(cmd.OutOrStdout(), diffJSONOut{
					FileDiffReport: rep,
					TextDiffs:      previews,
				})
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Fleet File Diff: %s (%s) — diff:%d local-only:%d remote-only:%d same:%d\n",
				rep.Node, rep.TargetID, rep.DiffCount, rep.LocalOnlyCount, rep.RemoteOnlyCount, rep.SameCount)
			rows := make([][]string, 0, len(rep.Entries))
			for _, e := range rep.Entries {
				loc := "—"
				if e.Local.Exists {
					loc = fmt.Sprintf("%dB %s", e.Local.SizeBytes, e.Local.Mode)
				}
				rem := "—"
				if e.Remote.Exists {
					rem = fmt.Sprintf("%dB %s", e.Remote.SizeBytes, e.Remote.Mode)
				}
				sec := ""
				if e.IsSecret {
					sec = "secret"
				}
				rows = append(rows, []string{e.Status, e.Category, "~/" + e.RelPath, loc, rem, sec})
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "No matching files found under selected sync paths.")
			} else if err := renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "STATUS", MinWidth: 12},
				{Title: "CAT", MinWidth: 8},
				{Title: "PATH", MinWidth: 22},
				{Title: "LOCAL", MinWidth: 10},
				{Title: "REMOTE", MinWidth: 10},
				{Title: "SEC", MinWidth: 6},
			}, rows); err != nil {
				return err
			}
			for _, warn := range rep.Warnings {
				fmt.Fprintf(w, "  ! %s\n", warn)
			}
			if showContent && len(previews) > 0 {
				fmt.Fprintln(w, "\nNon-Secret Diff Previews:")
				for _, e := range rep.Entries {
					if txt, ok := previews[e.RelPath]; ok {
						fmt.Fprintf(w, "\n--- %s ---\n%s\n", e.RelPath, txt)
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&targetID, "target", "", "Override SSH target (defaults to Tailscale peer DNSName/IP)")
	cmd.Flags().BoolVar(&includeEnv, "include-env", false, "Include zone/code .env and .env.* files")
	cmd.Flags().BoolVar(&includeDB, "include-db", false, "Include encrypted ws.db snapshot (~/.ws/backups/ws-backup-latest.enc)")
	cmd.Flags().BoolVar(&showContent, "content", false, "Show unified text diffs for non-secret configs (secrets redacted)")
	return cmd
}

func newFleetSyncFilesCmd(opts *GlobalOpts) *cobra.Command {
	var targetID string
	var push bool
	var pull bool
	var allDiff bool
	var includeEnv bool
	var includeDB bool
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "sync-files <node> [paths...]",
		Short: "Push or pull files with a peer",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if push == pull {
				return &UsageError{Err: fmt.Errorf("specify exactly one of --push or --pull")}
			}
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			node := strings.TrimSpace(args[0])
			target, err := resolveFleetFileSyncTarget(ctx, node, targetID)
			if err != nil {
				return err
			}

			direction := "push"
			if pull {
				direction = "pull"
			}

			relPaths := append([]string(nil), args[1:]...)
			if len(relPaths) == 0 {
				if !allDiff {
					return &UsageError{Err: fmt.Errorf("specify one or more relative paths or pass --all-diff")}
				}
				rep, err := fleet.DiffNodeFiles(ctx, node, target, fleet.FileSyncDiffOptions{
					IncludeEnv: includeEnv,
					IncludeDB:  includeDB,
				})
				if err != nil {
					return err
				}
				for _, e := range rep.Entries {
					switch direction {
					case "push":
						if e.Status == "local-only" || e.Status == "local-newer" || e.Status == "modified" {
							relPaths = append(relPaths, e.RelPath)
						}
					case "pull":
						if e.Status == "remote-only" || e.Status == "remote-newer" || e.Status == "modified" {
							relPaths = append(relPaths, e.RelPath)
						}
					}
				}
			}

			if len(relPaths) == 0 {
				empty := fleet.FileSyncResult{
					Node:      node,
					Direction: direction,
					DryRun:    ctx.DryRun,
					Lines:     []string{"All diffed files already match in this direction."},
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), empty)
				}
				fmt.Fprintln(cmd.OutOrStdout(), empty.Lines[0])
				return nil
			}

			res, err := fleet.SyncNodeFiles(ctx, node, target, fleet.FileSyncOptions{
				Direction:  direction,
				RelPaths:   relPaths,
				IncludeEnv: includeEnv,
				IncludeDB:  includeDB,
				DryRun:     ctx.DryRun,
			})
			if ctx.JSON {
				if err != nil {
					return err
				}
				return writeJSON(cmd.OutOrStdout(), res)
			}

			w := cmd.OutOrStdout()
			prefix := ""
			if res.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(w, "%sFleet %s (%s): transferred=%d failed=%d\n",
				prefix, strings.ToUpper(res.Direction), res.Node, res.Transferred, res.Failed)
			for _, ln := range res.Lines {
				fmt.Fprintf(w, "  %s\n", ln)
			}
			if res.DryRun && !executeFlag {
				fmt.Fprintln(w, "Preview only (dry-run by default). Re-run with --execute to apply changes.")
			}
			return err
		},
	}

	cmd.Flags().StringVar(&targetID, "target", "", "Override SSH target (defaults to Tailscale peer DNSName/IP)")
	cmd.Flags().BoolVar(&push, "push", false, "Push local files -> remote node")
	cmd.Flags().BoolVar(&pull, "pull", false, "Pull remote files -> local machine")
	cmd.Flags().BoolVar(&allDiff, "all-diff", false, "Select all differing/unique files in the chosen direction")
	cmd.Flags().BoolVar(&includeEnv, "include-env", false, "Include zone/code .env* files when using --all-diff")
	cmd.Flags().BoolVar(&includeDB, "include-db", false, "Include encrypted ws.db snapshot when using --all-diff")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Execute file transfers (default is dry-run preview)")
	return cmd
}
