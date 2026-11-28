package cli

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/envmgr"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/tui"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/igwtcode/workstation/ws/internal/ui/picker"
	"github.com/spf13/cobra"
	_ "modernc.org/sqlite"
)

const (
	envLastScanMetaKey = "env_last_scan_at"
	envScanTTL         = 10 * time.Minute
)

func newEnvCmd(opts *GlobalOpts) *cobra.Command {
	envCmd := &cobra.Command{
		Use:   "env",
		Short: "Index, trace provenance, audit blast radius, and rotate environment variables",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEnvList(cmd, opts, "", "", false, false)
		},
	}

	envCmd.AddCommand(
		newEnvScanCmd(opts),
		newEnvListCmd(opts),
		newEnvFilesCmd(opts),
		newEnvFindCmd(opts),
		newEnvProvenanceCmd(opts),
		newEnvImpactCmd(opts),
		newEnvRotateCmd(opts),
		newEnvSetCmd(opts),
		newEnvUndoCmd(opts),
		newEnvRestoreCmd(opts),
	)

	return envCmd
}

func ensureEnvIndexed(ctx *app.Context, db *store.DB, kr *store.Keyring, forceScan bool) error {
	return ensureEnvIndexedAt(ctx, db, kr, forceScan, time.Now())
}

func scanAndRecordEnv(ctx *app.Context, db *store.DB, kr *store.Keyring, now time.Time) (*envmgr.ScanResult, error) {
	res, err := envmgr.Scan(ctx, db, kr)
	if err != nil {
		return nil, err
	}
	if err := db.SetMeta(envLastScanMetaKey, strconv.FormatInt(now.Unix(), 10)); err != nil {
		return nil, err
	}
	return res, nil
}

func ensureEnvIndexedAt(ctx *app.Context, db *store.DB, kr *store.Keyring, forceScan bool, now time.Time) error {
	if !forceScan {
		var cnt int
		if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM env_entries`).Scan(&cnt); err == nil && cnt > 0 {
			if rawTS, ok, merr := db.GetMeta(envLastScanMetaKey); merr == nil && ok {
				if ts, perr := strconv.ParseInt(strings.TrimSpace(rawTS), 10, 64); perr == nil && ts > 0 {
					age := now.Sub(time.Unix(ts, 0))
					if age >= 0 && age <= envScanTTL {
						return nil
					}
				}
			}
		}
	}
	_, err := scanAndRecordEnv(ctx, db, kr, now)
	return err
}

func newEnvScanCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Index shell RC, mise, and project .env files into ws.db",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			res, err := scanAndRecordEnv(ctx, db, kr, time.Now())
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scanned %d file(s): %d entries (%d secrets), %d encrypted blob(s) stored.\n",
				res.FilesScanned, res.EntriesCount, res.SecretsCount, res.BlobsStored)
			return nil
		},
	}
}

func newEnvFilesCmd(opts *GlobalOpts) *cobra.Command {
	var zone string
	var rescan bool

	cmd := &cobra.Command{
		Use:   "files",
		Short: "List indexed .env, secret, shell RC, and mise config files with counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			if err := ensureEnvIndexed(ctx, db, kr, rescan); err != nil {
				return err
			}
			files, err := queryEnvFiles(ctx, db, zone)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), files)
			}
			if len(files) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No indexed environment files matched.")
				return nil
			}
			rows := make([][]string, 0, len(files))
			for _, f := range files {
				zLabel := f.Zone
				if zLabel == "" {
					zLabel = "-"
				}
				state := "present"
				if f.Missing {
					state = "missing"
				}
				rows = append(rows, []string{
					f.Path,
					f.SourceKind,
					zLabel,
					strconv.Itoa(f.VarCount),
					strconv.Itoa(f.SecretCount),
					state,
				})
			}
			return renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "PATH"},
				{Title: "KIND", MinWidth: 14},
				{Title: "ZONE", MinWidth: 8},
				{Title: "VARS", MinWidth: 4, RightAlign: true},
				{Title: "SECRETS", MinWidth: 7, RightAlign: true},
				{Title: "STATE", MinWidth: 7},
			}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "Filter files by identity zone")
	cmd.Flags().BoolVar(&rescan, "scan", false, "Re-scan filesystem before listing")
	return cmd
}

func queryEnvFiles(ctx *app.Context, db *store.DB, zone string) ([]tui.EnvFileRow, error) {
	list, err := envmgr.ListFiles(db, zone)
	if err != nil {
		return nil, err
	}
	files := make([]tui.EnvFileRow, 0, len(list))
	for _, f := range list {
		missing := f.Missing
		if !missing && ctx != nil && f.Path != "" {
			if _, statErr := os.Stat(ctx.ExpandHome(f.Path)); errors.Is(statErr, os.ErrNotExist) {
				missing = true
			}
		}
		files = append(files, tui.EnvFileRow{
			Path:        f.Path,
			SourceKind:  f.SourceKind,
			Zone:        f.Zone,
			VarCount:    f.VarCount,
			SecretCount: f.SecretCount,
			Missing:     missing,
			Ignored:     f.Ignored,
		})
	}
	return files, nil
}

func newEnvListCmd(opts *GlobalOpts) *cobra.Command {
	var zone string
	var secretsOnly bool
	var filter string
	var rescan bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List indexed environment variables with active sources and masked values",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runEnvList(cmd, opts, zone, filter, secretsOnly, rescan)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "Filter variables by identity zone")
	cmd.Flags().BoolVar(&secretsOnly, "secrets-only", false, "Show only variables classified as secrets")
	cmd.Flags().StringVar(&filter, "filter", "", "Substring filter across key, zone, source, or fingerprint")
	cmd.Flags().BoolVar(&rescan, "scan", false, "Re-scan filesystem before listing")
	return cmd
}

func runEnvList(cmd *cobra.Command, opts *GlobalOpts, zone, filter string, secretsOnly, rescan bool) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	db, kr, err := openStoreAndKey(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := ensureEnvIndexed(ctx, db, kr, rescan); err != nil {
		return err
	}

	summaries, err := envmgr.ListSummariesForZone(db, filter, zone, secretsOnly)
	if err != nil {
		return err
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), summaries)
	}
	if len(summaries) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No indexed environment variables matched.")
		return nil
	}
	rows := make([][]string, 0, len(summaries))
	for _, s := range summaries {
		zLabel := s.Zone
		if zLabel == "" {
			zLabel = "-"
		}
		secTag := ""
		if s.IsSecret {
			secTag = "sec"
		}
		rows = append(rows, []string{
			s.VarKey,
			zLabel,
			secTag,
			s.ActiveSourceKind,
			s.ActiveMaskedValue,
			s.ActiveSourcePath,
		})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "KEY", MinWidth: 22},
		{Title: "ZONE", MinWidth: 8},
		{Title: "SEC", MinWidth: 3},
		{Title: "SOURCE", MinWidth: 14},
		{Title: "VALUE", MinWidth: 16},
		{Title: "PATH"},
	}, rows)
}

func newEnvFindCmd(opts *GlobalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "find <query>",
		Short: "Search indexed environment entries by key, path, zone, or fingerprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			if err := ensureEnvIndexed(ctx, db, kr, false); err != nil {
				return err
			}
			entries, err := envmgr.FindEntriesForZone(db, args[0], zone)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No matching environment entries found.")
				return nil
			}
			rows := make([][]string, 0, len(entries))
			for _, e := range entries {
				zLabel := e.Zone
				if zLabel == "" {
					zLabel = "-"
				}
				rows = append(rows, []string{
					e.VarKey,
					zLabel,
					e.SourceKind,
					fmt.Sprintf("%s:%d", e.SourcePath, e.LineNum),
					e.MaskedValue,
					e.Fingerprint,
				})
			}
			return renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "KEY", MinWidth: 20},
				{Title: "ZONE", MinWidth: 8},
				{Title: "SOURCE", MinWidth: 14},
				{Title: "LOCATION"},
				{Title: "VALUE", MinWidth: 14},
				{Title: "FINGERPRINT"},
			}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "Filter entries by identity zone")
	return cmd
}

func newEnvProvenanceCmd(opts *GlobalOpts) *cobra.Command {
	var targetDir string

	cmd := &cobra.Command{
		Use:     "provenance <KEY>",
		Aliases: []string{"show"},
		Short:   "Trace override precedence chain and cross-project impact for a variable",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			if err := ensureEnvIndexed(ctx, db, kr, false); err != nil {
				return err
			}
			rep, err := envmgr.GetProvenance(ctx, db, args[0], targetDir)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), rep)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Variable:    %s\n", rep.VarKey)
			if rep.TargetDir != "" {
				fmt.Fprintf(w, "Directory:   %s\n", rep.TargetDir)
			}
			fmt.Fprintf(w, "Active:      %s (%s)\n", rep.ActiveValueMasked, rep.Fingerprint)
			fmt.Fprintf(w, "\nPrecedence Layers (%d):\n", len(rep.Layers))
			if len(rep.Layers) > 0 {
				layerRows := make([][]string, 0, len(rep.Layers))
				for _, l := range rep.Layers {
					layerRows = append(layerRows, []string{
						l.Status,
						l.SourceKind,
						fmt.Sprintf("%s:%d", l.SourcePath, l.LineNum),
						l.MaskedValue,
					})
				}
				if err := renderCLITable(cmd, ctx, []ui.TableColumn{
					{Title: "STATUS", MinWidth: 8},
					{Title: "SOURCE", MinWidth: 14},
					{Title: "LOCATION"},
					{Title: "VALUE"},
				}, layerRows); err != nil {
					return err
				}
			}
			fmt.Fprintf(w, "\nBlast Radius (%d):\n", len(rep.ImpactMatches))
			if len(rep.ImpactMatches) > 0 {
				impactRows := make([][]string, 0, len(rep.ImpactMatches))
				for _, m := range rep.ImpactMatches {
					zLabel := m.Zone
					if zLabel == "" {
						zLabel = "-"
					}
					impactRows = append(impactRows, []string{
						zLabel,
						m.SourceKind,
						fmt.Sprintf("%s:%d", m.SourcePath, m.LineNum),
						m.MaskedValue,
						m.Fingerprint,
					})
				}
				if err := renderCLITable(cmd, ctx, []ui.TableColumn{
					{Title: "ZONE", MinWidth: 8},
					{Title: "SOURCE", MinWidth: 14},
					{Title: "LOCATION"},
					{Title: "VALUE"},
					{Title: "FINGERPRINT"},
				}, impactRows); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&targetDir, "dir", "", "Scope provenance precedence chain to a directory")
	return cmd
}

func newEnvImpactCmd(opts *GlobalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "impact <KEY|fp:...>",
		Short: "Find every indexed file sharing a variable key or salted value fingerprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			if err := ensureEnvIndexed(ctx, db, kr, false); err != nil {
				return err
			}
			matches, err := envmgr.GetBlastRadius(db, args[0])
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), matches)
			}
			if len(matches) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No files matched %q.\n", args[0])
				return nil
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Impact for %s (%d occurrence(s)):\n", args[0], len(matches))
			rows := make([][]string, 0, len(matches))
			for _, m := range matches {
				zLabel := m.Zone
				if zLabel == "" {
					zLabel = "-"
				}
				rows = append(rows, []string{
					m.VarKey,
					zLabel,
					m.SourceKind,
					fmt.Sprintf("%s:%d", m.SourcePath, m.LineNum),
					m.MaskedValue,
					m.Fingerprint,
				})
			}
			return renderCLITable(cmd, ctx, []ui.TableColumn{
				{Title: "KEY", MinWidth: 20},
				{Title: "ZONE", MinWidth: 8},
				{Title: "SOURCE", MinWidth: 14},
				{Title: "LOCATION"},
				{Title: "VALUE"},
				{Title: "FINGERPRINT"},
			}, rows)
		},
	}
}

func openExistingDBReadOnly(ctx *app.Context) (*sql.DB, error) {
	if ctx == nil || strings.TrimSpace(ctx.WSBaseDir()) == "" {
		return nil, nil
	}
	dbPath := filepath.Clean(store.DefaultPath(ctx.WSBaseDir()))
	fi, err := os.Lstat(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink database path %s", dbPath)
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(dbPath),
		RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)",
	}).String()
	rawDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	if err := rawDB.Ping(); err != nil {
		_ = rawDB.Close()
		return nil, fmt.Errorf("open sqlite read-only %s: %w", dbPath, err)
	}
	return rawDB, nil
}

func newEnvRotateCmd(opts *GlobalOpts) *cobra.Command {
	var fingerprint string
	var zone string
	var paths []string
	var valueStdin bool
	var valueFlag string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "rotate [KEY]",
		Short: "Preview or atomically rotate a secret across matching .env* and mise.local.toml files",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buildOpts := *opts
			if !executeFlag {
				buildOpts.DryRun = true
			}
			ctx, err := buildOpts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}

			varKey := ""
			if len(args) == 1 {
				varKey = strings.TrimSpace(args[0])
			}
			if varKey == "" && strings.TrimSpace(fingerprint) == "" {
				return fmt.Errorf("variable KEY or --fingerprint is required")
			}

			newVal, err := readSecretValue(cmd, ctx, valueFlag, valueStdin, "Enter replacement value: ", ctx.DryRun)
			if err != nil {
				return err
			}

			req := envmgr.RotateRequest{
				VarKey:           varKey,
				MatchFingerprint: fingerprint,
				Zone:             zone,
				Paths:            paths,
				OnlyExisting:     len(paths) == 0,
			}

			if ctx.DryRun {
				roDB, err := openExistingDBReadOnly(ctx)
				if err != nil {
					return err
				}
				if roDB != nil {
					defer func() { _ = roDB.Close() }()
				}
				kr, err := store.LoadKey(ctx)
				if err != nil {
					return err
				}
				entries, _, err := envmgr.ScanInMemory(ctx, kr)
				if err != nil {
					return err
				}
				plan, err := envmgr.PlanRotateFromEntries(ctx, entries, req, newVal)
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"dryRun": true,
						"plan":   plan,
					})
				}
				printRotatePlan(cmd.OutOrStdout(), plan, true)
				if !executeFlag && len(plan.Targets) > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to rotate.")
				}
				return nil
			}

			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			if err := ensureEnvIndexed(ctx, db, kr, true); err != nil {
				return err
			}

			plan, err := envmgr.PlanRotate(ctx, db, kr, req, newVal)
			if err != nil {
				return err
			}

			if len(plan.Targets) > 0 && !ctx.Allow.Replace {
				if !isPickerInteractiveTTY(cmd, ctx) {
					return fmt.Errorf("rotating existing secrets across %d file(s) requires --allow replace in non-interactive mode", len(plan.Targets))
				}
				choice, perr := promptSelect(
					cmd,
					ctx,
					fmt.Sprintf("Rotate %s in %d file(s)?", plan.VarKey, len(plan.Targets)),
					[]picker.Item{
						{Title: "Cancel", Subtitle: "abort without writing", Value: "cancel"},
						{Title: "Rotate now", Subtitle: fmt.Sprintf("update %d file(s)", len(plan.Targets)), Value: "rotate"},
					},
				)
				if perr != nil {
					return perr
				}
				if choice != "rotate" {
					fmt.Fprintln(cmd.OutOrStdout(), "Rotation cancelled.")
					return nil
				}
			}

			res, err := envmgr.ExecRotate(ctx, db, kr, plan, newVal)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun": false,
					"plan":   plan,
					"result": res,
				})
			}
			printRotatePlan(cmd.OutOrStdout(), plan, false)
			fmt.Fprintf(cmd.OutOrStdout(), "Rotated %d file(s) (tx: %s). Roll back with: ws env undo %s\n",
				len(res.UpdatedFiles), res.TxID, res.TxID)
			return nil
		},
	}

	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "Match entries by salted fingerprint (fp:...)")
	cmd.Flags().StringVar(&zone, "zone", "", "Restrict rotation to one identity zone")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "Restrict or explicitly target specific writable env files")
	cmd.Flags().BoolVar(&valueStdin, "value-stdin", false, "Read replacement secret from stdin")
	cmd.Flags().StringVar(&valueFlag, "value", "", "Replacement value (discouraged: prefer --value-stdin or interactive prompt)")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Write rotated values atomically (default is dry-run preview)")
	return cmd
}

func newEnvSetCmd(opts *GlobalOpts) *cobra.Command {
	var filePath string
	var valueStdin bool
	var valueFlag string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "set <KEY>",
		Short: "Preview or atomically set a variable in a specific .env* or mise.local.toml file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buildOpts := *opts
			if !executeFlag {
				buildOpts.DryRun = true
			}
			ctx, err := buildOpts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			if strings.TrimSpace(filePath) == "" {
				return fmt.Errorf("--file is required")
			}

			newVal, err := readSecretValue(cmd, ctx, valueFlag, valueStdin, "Enter value for "+args[0]+": ", ctx.DryRun)
			if err != nil {
				return err
			}

			if ctx.DryRun {
				kr, err := store.LoadKey(ctx)
				if err != nil {
					return err
				}
				plan, err := envmgr.PlanSetVar(ctx, nil, kr, filePath, args[0], newVal)
				if err != nil {
					return err
				}
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"dryRun": true,
						"plan":   plan,
					})
				}
				printRotatePlan(cmd.OutOrStdout(), plan, true)
				if !executeFlag {
					fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to write.")
				}
				return nil
			}

			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			plan, err := envmgr.PlanSetVar(ctx, db, kr, filePath, args[0], newVal)
			if err != nil {
				return err
			}

			if len(plan.Targets) > 0 && !ctx.Allow.Replace {
				replacingExisting := false
				for _, t := range plan.Targets {
					if t.LineNum > 0 || t.OldMasked != "" {
						replacingExisting = true
						break
					}
				}
				if replacingExisting {
					if !isPickerInteractiveTTY(cmd, ctx) {
						return fmt.Errorf("overwriting existing %s in %s requires --allow replace in non-interactive mode", plan.VarKey, filePath)
					}
					choice, perr := promptSelect(
						cmd,
						ctx,
						fmt.Sprintf("Set %s in %s?", plan.VarKey, filePath),
						[]picker.Item{
							{Title: "Cancel", Subtitle: "abort without writing", Value: "cancel"},
							{Title: "Set value now", Subtitle: ctx.CollapseHome(filePath), Value: "set"},
						},
					)
					if perr != nil {
						return perr
					}
					if choice != "set" {
						fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
						return nil
					}
				}
			}

			res, err := envmgr.ExecRotate(ctx, db, kr, plan, newVal)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun": false,
					"plan":   plan,
					"result": res,
				})
			}
			printRotatePlan(cmd.OutOrStdout(), plan, false)
			fmt.Fprintf(cmd.OutOrStdout(), "Updated %d file(s) (tx: %s). Roll back with: ws env undo %s\n",
				len(res.UpdatedFiles), res.TxID, res.TxID)
			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Target writable .env* or mise.local.toml file")
	cmd.Flags().BoolVar(&valueStdin, "value-stdin", false, "Read value from stdin")
	cmd.Flags().StringVar(&valueFlag, "value", "", "Value to set (discouraged: prefer --value-stdin or interactive prompt)")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Apply write atomically (default is dry-run preview)")
	return cmd
}

func newEnvUndoCmd(opts *GlobalOpts) *cobra.Command {
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "undo [tx-id]",
		Short: "Roll back a secret rotation transaction from encrypted ws.db snapshots",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			txID := ""
			if len(args) == 1 {
				txID = args[0]
			}

			if !ctx.DryRun && !ctx.Allow.Replace {
				if !isPickerInteractiveTTY(cmd, ctx) {
					return fmt.Errorf("rolling back env rotation files requires --allow replace in non-interactive mode")
				}
				choice, perr := promptSelect(
					cmd,
					ctx,
					"Roll back rotation transaction from encrypted snapshots?",
					[]picker.Item{
						{Title: "Cancel", Subtitle: "keep current files", Value: "cancel"},
						{Title: "Roll back now", Subtitle: "restore snapshot copies", Value: "rollback"},
					},
				)
				if perr != nil {
					return perr
				}
				if choice != "rollback" {
					fmt.Fprintln(cmd.OutOrStdout(), "Rollback cancelled.")
					return nil
				}
			}

			res, err := envmgr.UndoRotate(ctx, db, kr, txID)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			prefix := ""
			if res.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sRolled back tx %s (%d file(s)):\n", prefix, res.TxID, len(res.UpdatedFiles))
			for _, f := range res.UpdatedFiles {
				fmt.Fprintf(cmd.OutOrStdout(), "  <- %s\n", f)
			}
			if !executeFlag && len(res.UpdatedFiles) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to roll back.")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Restore files on disk (default is dry-run preview)")
	return cmd
}

func newEnvRestoreCmd(opts *GlobalOpts) *cobra.Command {
	var paths []string
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Decrypt and restore backed-up .env* files from ws.db (0600)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !executeFlag {
				ctx.DryRun = true
			}
			if ctx.DryRun {
				if _, serr := os.Lstat(store.DefaultPath(ctx.WSBaseDir())); errors.Is(serr, os.ErrNotExist) {
					if ctx.JSON {
						return writeJSON(cmd.OutOrStdout(), map[string]any{"dryRun": true, "files": []string{}})
					}
					fmt.Fprintln(cmd.OutOrStdout(), "No stored env files matched.")
					return nil
				}
			}
			db, kr, err := openStoreAndKey(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			written, err := envmgr.RestoreEnvFiles(ctx, db, kr, paths, ctx.Allow.Replace)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"dryRun": ctx.DryRun,
					"files":  written,
				})
			}
			if len(written) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No stored env files matched.")
				return nil
			}
			prefix := ""
			if ctx.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%sRestored %d env file(s):\n", prefix, len(written))
			for _, p := range written {
				fmt.Fprintf(cmd.OutOrStdout(), "  + %s\n", ctx.CollapseHome(p))
			}
			if ctx.DryRun && !executeFlag {
				fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to restore.")
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&paths, "path", nil, "Filter stored env files by path or label")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Restore files on disk (default is dry-run preview)")
	return cmd
}

func readSecretValue(cmd *cobra.Command, ctx *app.Context, valueFlag string, valueStdin bool, prompt string, allowEmptyForDryRun bool) (string, error) {
	if valueStdin {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read secret from stdin: %w", err)
		}
		val := strings.TrimRight(string(raw), "\r\n")
		if val == "" && !allowEmptyForDryRun {
			return "", fmt.Errorf("empty value received on stdin")
		}
		return val, nil
	}
	if cmd.Flags().Changed("value") || valueFlag != "" {
		if !ctx.JSON {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: passing secrets via --value can expose them in process listings; prefer --value-stdin or interactive prompt")
		}
		return valueFlag, nil
	}
	if isInteractiveTTY(cmd, ctx) {
		val, err := readPasswordTTY(cmd.InOrStdin(), cmd.ErrOrStderr(), prompt)
		if err != nil {
			return "", err
		}
		if val == "" && !allowEmptyForDryRun {
			return "", fmt.Errorf("replacement value cannot be empty")
		}
		return val, nil
	}
	if allowEmptyForDryRun {
		return "", nil
	}
	return "", fmt.Errorf("new value required: pass --value-stdin (recommended) or run in an interactive TTY")
}

func readPasswordTTY(r io.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	var ttyFD uintptr
	hasTTYFD := false
	if f, ok := r.(*os.File); ok && term.IsTerminal(f.Fd()) {
		ttyFD = f.Fd()
		hasTTYFD = true
	} else if term.IsTerminal(os.Stdin.Fd()) {
		ttyFD = os.Stdin.Fd()
		hasTTYFD = true
	}
	if hasTTYFD {
		raw, err := term.ReadPassword(ttyFD)
		fmt.Fprintln(w)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func printRotatePlan(w io.Writer, plan *envmgr.RotatePlan, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "[dry-run] "
	}
	fmt.Fprintf(w, "%sPlan %s for %s (%d target(s)):\n", prefix, plan.TxID, plan.VarKey, len(plan.Targets))
	if len(plan.Targets) == 0 {
		fmt.Fprintln(w, "  No writable .env* or mise.local.toml targets matched.")
		return
	}
	for _, t := range plan.Targets {
		loc := t.SourcePath
		if t.LineNum > 0 {
			loc = fmt.Sprintf("%s:%d", t.SourcePath, t.LineNum)
		}
		oldVal := t.OldMasked
		if oldVal == "" {
			oldVal = "(unset)"
		}
		newVal := t.NewMasked
		if newVal == "" {
			newVal = "(pending)"
		}
		fmt.Fprintf(w, "  %-16s %s  %s -> %s\n", t.SourceKind, loc, oldVal, newVal)
	}
}
