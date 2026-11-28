package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/store"
	"github.com/igwtcode/workstation/ws/internal/ui"
	"github.com/spf13/cobra"
)

type dbStatusOutput struct {
	store.DBStats
	KeySource   store.KeySource `json:"keySource"`
	HasKey      bool            `json:"hasKey"`
	KeyFilePath string          `json:"keyFilePath,omitempty"`
}

func openStoreAndKey(ctx *app.Context) (*store.DB, *store.Keyring, error) {
	db, err := store.Open(store.DefaultPath(ctx.WSBaseDir()))
	if err != nil {
		return nil, nil, err
	}
	kr, err := store.LoadKey(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, kr, nil
}

func newDBCmd(opts *GlobalOpts) *cobra.Command {
	dbCmd := &cobra.Command{
		Use:   "db",
		Short: "Manage encrypted local ws.db state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDBStatus(cmd, opts)
		},
	}

	dbCmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show database status and table counts",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runDBStatus(cmd, opts)
			},
		},
		newDBKeyCmd(opts),
		newDBBackupCmd(opts),
		newDBRestoreCmd(opts),
	)

	return dbCmd
}

func runDBStatus(cmd *cobra.Command, opts *GlobalOpts) error {
	ctx, err := opts.buildContext()
	if err != nil {
		return err
	}
	db, kr, err := openStoreAndKey(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	stats, err := db.Stats()
	if err != nil {
		return err
	}
	out := dbStatusOutput{
		DBStats:     stats,
		KeySource:   kr.Source,
		HasKey:      kr.HasKey(),
		KeyFilePath: kr.KeyFilePath,
	}
	if ctx.JSON {
		return writeJSON(cmd.OutOrStdout(), out)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Database:     %s (schema v%d, %s)\n", ctx.CollapseHome(stats.Path), stats.SchemaVersion, formatHumanBytes(stats.SizeBytes))
	keyState := "missing"
	if out.HasKey {
		keyState = string(out.KeySource)
	}
	fmt.Fprintf(w, "Key:          %s\n", keyState)
	if stats.LastBackupAt > 0 {
		fmt.Fprintf(w, "Last Backup:  %s\n", time.Unix(stats.LastBackupAt, 0).Local().Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "Last Backup:  never")
	}
	fmt.Fprintf(w, "Dirty:        %v\n", stats.DirtySinceBackup)
	fmt.Fprintf(w, "Content Hash: %s\n", stats.CurrentHash)

	tables := make([]string, 0, len(stats.TableCounts))
	for k := range stats.TableCounts {
		tables = append(tables, k)
	}
	sort.Strings(tables)
	rows := make([][]string, 0, len(tables))
	for _, tbl := range tables {
		rows = append(rows, []string{tbl, strconv.Itoa(stats.TableCounts[tbl])})
	}
	return renderCLITable(cmd, ctx, []ui.TableColumn{
		{Title: "TABLE", MinWidth: 16},
		{Title: "ROWS", MinWidth: 6, RightAlign: true},
	}, rows)
}

func newDBKeyCmd(opts *GlobalOpts) *cobra.Command {
	keyCmd := &cobra.Command{
		Use:   "key",
		Short: "Manage the ws.db encryption key",
	}

	var initKeychain bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a new database keyfile",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			kr, err := store.InitKey(ctx, initKeychain)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"source":      kr.Source,
					"hasKey":      kr.HasKey(),
					"keyFilePath": kr.KeyFilePath,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized ws.db key (%s): %s\n", kr.Source, ctx.CollapseHome(kr.KeyFilePath))
			return nil
		},
	}
	initCmd.Flags().BoolVar(&initKeychain, "keychain", false, "Also store key in OS keychain / secret-tool")

	var backupExecute bool
	backupKeyCmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the master key to 1Password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !backupExecute {
				ctx.DryRun = true
			}
			kr, err := store.LoadKey(ctx)
			if err != nil {
				return err
			}
			if ctx.DryRun {
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"backedUp": false,
						"dryRun":   true,
						"source":   kr.Source,
						"hasKey":   kr.HasKey(),
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would back up ws.db master key (%s) to 1Password\n", kr.Source)
				if !backupExecute {
					fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to upload.")
				}
				return nil
			}
			if err := store.BackupKeyTo1Password(ctx, kr); err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"backedUp": true,
					"dryRun":   false,
					"source":   kr.Source,
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Backed up ws.db master key to 1Password.")
			return nil
		},
	}
	backupKeyCmd.Flags().BoolVar(&backupExecute, "execute", false, "Upload master key to 1Password (default is dry-run preview)")

	var restoreKeychain bool
	var restoreExecute bool
	restoreKeyCmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the master key from 1Password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := opts.buildContext()
			if err != nil {
				return err
			}
			if !restoreExecute {
				ctx.DryRun = true
			}
			if ctx.DryRun {
				keyPath := store.DefaultKeyPath(ctx.WSBaseDir())
				if ctx.JSON {
					return writeJSON(cmd.OutOrStdout(), map[string]any{
						"restored":    false,
						"dryRun":      true,
						"keyFilePath": keyPath,
						"keychain":    restoreKeychain,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would restore ws.db master key from 1Password to %s\n", ctx.CollapseHome(keyPath))
				if !restoreExecute {
					fmt.Fprintln(cmd.OutOrStdout(), "Preview only (dry-run by default). Re-run with --execute to restore.")
				}
				return nil
			}
			kr, err := store.RestoreKeyFrom1Password(ctx, restoreKeychain)
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"restored":    true,
					"dryRun":      false,
					"source":      kr.Source,
					"keyFilePath": kr.KeyFilePath,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Restored ws.db master key (%s): %s\n", kr.Source, ctx.CollapseHome(kr.KeyFilePath))
			return nil
		},
	}
	restoreKeyCmd.Flags().BoolVar(&restoreKeychain, "keychain", false, "Also store restored key in OS keychain / secret-tool")
	restoreKeyCmd.Flags().BoolVar(&restoreExecute, "execute", false, "Restore master key from 1Password (default is dry-run preview)")

	keyCmd.AddCommand(initCmd, backupKeyCmd, restoreKeyCmd)
	return keyCmd
}

func newDBBackupCmd(opts *GlobalOpts) *cobra.Command {
	var ifChanged bool
	var forceFlag bool
	var localOnly bool
	var onePassword bool
	var autoOnePassword bool
	var outputPath string

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write an encrypted ws.db snapshot",
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

			outPath := strings.TrimSpace(outputPath)
			if outPath != "" {
				outPath = ctx.ExpandHome(outPath)
			}

			res, err := store.BackupIfChanged(ctx, db, kr, store.BackupOptions{
				Force:           forceFlag || !ifChanged,
				OnePassword:     onePassword && !localOnly,
				AutoOnePassword: autoOnePassword && !localOnly && !onePassword,
				OutputPath:      outPath,
			})
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			w := cmd.OutOrStdout()
			if res.Skipped {
				fmt.Fprintf(w, "Skipped backup (ws.db unchanged, hash %s).\n", res.Hash)
				return nil
			}
			prefix := ""
			if res.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(w, "%sBackup written: %s (1password=%v, hash=%s)\n", prefix, ctx.CollapseHome(res.LocalPath), res.OnePassword, res.Hash)
			return nil
		},
	}

	cmd.Flags().BoolVar(&ifChanged, "if-changed", true, "Skip backup when canonical ws.db hash is unchanged")
	cmd.Flags().BoolVar(&forceFlag, "force", false, "Write backup even when canonical hash is unchanged")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "Only write local encrypted archive without 1Password upload")
	cmd.Flags().BoolVar(&onePassword, "1password", false, "Upload encrypted backup archive to 1Password via op CLI")
	cmd.Flags().BoolVar(&autoOnePassword, "auto-1password", false, "Upload to 1Password if op CLI is installed and signed in")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Custom output path for encrypted backup archive")
	return cmd
}

func newDBRestoreCmd(opts *GlobalOpts) *cobra.Command {
	var filePath string
	var fromOnePassword bool
	var scopes []string
	var vars []string
	var paths []string
	var writeFiles bool
	var executeFlag bool

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore state from an encrypted backup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
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

			inPath := strings.TrimSpace(filePath)
			if inPath != "" {
				inPath = ctx.ExpandHome(inPath)
			}

			res, err := store.RestoreBackup(ctx, db, kr, store.RestoreOptions{
				InputPath:       inPath,
				FromOnePassword: fromOnePassword,
				Scopes:          scopes,
				Vars:            vars,
				Paths:           paths,
				WriteFiles:      writeFiles,
				AllowReplace:    ctx.Allow.Replace,
			})
			if err != nil {
				return err
			}
			if ctx.JSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			w := cmd.OutOrStdout()
			prefix := ""
			if res.DryRun {
				prefix = "[dry-run] "
			}
			fmt.Fprintf(w, "%sRestored from %s (hash %s)\n", prefix, ctx.CollapseHome(res.Source), res.Hash)
			tables := make([]string, 0, len(res.TablesRestored))
			for k := range res.TablesRestored {
				tables = append(tables, k)
			}
			sort.Strings(tables)
			if len(tables) > 0 {
				rows := make([][]string, 0, len(tables))
				for _, tbl := range tables {
					rows = append(rows, []string{tbl, strconv.Itoa(res.TablesRestored[tbl])})
				}
				if err := renderCLITable(cmd, ctx, []ui.TableColumn{
					{Title: "TABLE", MinWidth: 16},
					{Title: "RESTORED", MinWidth: 8, RightAlign: true},
				}, rows); err != nil {
					return err
				}
			}
			for _, f := range res.FilesWritten {
				fmt.Fprintf(w, "  + %s\n", ctx.CollapseHome(f))
			}
			for _, s := range res.SkippedFiles {
				fmt.Fprintf(w, "  ! skipped %s\n", s)
			}
			if res.DryRun && !executeFlag {
				fmt.Fprintln(w, "Preview only (dry-run by default). Re-run with --execute to restore.")
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Local encrypted backup archive path (default: latest)")
	cmd.Flags().BoolVar(&fromOnePassword, "from-1password", false, "Fetch encrypted backup archive from 1Password")
	cmd.Flags().StringSliceVar(&scopes, "scope", nil, "Comma-separated scopes: env,secrets,repos,fleet")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Filter restored env entries or secret blobs by variable/label")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "Filter restored records by source path")
	cmd.Flags().BoolVar(&writeFiles, "write-files", false, "Write decrypted envfile blobs back to disk (0600)")
	cmd.Flags().BoolVar(&executeFlag, "execute", false, "Apply restore changes (default is dry-run preview)")
	return cmd
}
