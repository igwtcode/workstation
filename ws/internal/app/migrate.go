package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	_ "modernc.org/sqlite"
)

func MigrateLegacyWSHome(ctx *Context) error {
	if ctx == nil || ctx.DryRun || strings.TrimSpace(ctx.WSHome) == "" || strings.TrimSpace(ctx.HomeDir) == "" {
		return nil
	}
	home := filepath.Clean(ctx.HomeDir)
	wsHome := filepath.Clean(ctx.WSHome)

	if wsHome != filepath.Join(home, ".ws") {
		return nil
	}

	var legacyStateCand string
	if strings.TrimSpace(ctx.StateDir) != "" {
		cand := filepath.Join(filepath.Clean(ctx.StateDir), "ws")
		if cand != wsHome && isStrictSubpath(cand, home) {
			legacyStateCand = cand
		}
	}
	var legacyKeyCand string
	if strings.TrimSpace(ctx.ConfigDir) != "" {
		cand := filepath.Join(filepath.Clean(ctx.ConfigDir), "ws", "db.key")
		if cand != ctx.WSKeyPath() && isStrictSubpath(cand, home) {
			legacyKeyCand = cand
		}
	}

	doneMarker := ctx.WSDBPath() + ".migrate.cleanup"
	if !pathExists(legacyStateCand) && !pathExists(legacyKeyCand) && !pathExists(doneMarker) {
		return nil
	}

	return withMigrateLock(wsHome, func() error {
		var firstErr error
		recordErr := func(err error) {
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}

		hasLegacyState := pathExists(legacyStateCand)
		if hasLegacyState || pathExists(doneMarker) {
			var srcDB string
			if legacyStateCand != "" {
				srcDB = filepath.Join(legacyStateCand, "ws.db")
			}
			recordErr(migrateSQLiteDB(srcDB, ctx.WSDBPath()))
		}

		if hasLegacyState {
			filePairs := [][2]string{
				{filepath.Join(legacyStateCand, "ledger.json"), ctx.WSLedgerPath()},
				{filepath.Join(legacyStateCand, "theme"), ctx.WSThemePath()},
				{filepath.Join(legacyStateCand, "digest.json"), ctx.WSDigestPath()},
				{filepath.Join(legacyStateCand, "digest.err.log"), filepath.Join(ctx.WSLogsDir(), "digest.err.log")},
				{filepath.Join(legacyStateCand, "db-backup.err.log"), filepath.Join(ctx.WSLogsDir(), "db-backup.err.log")},
			}
			for _, pair := range filePairs {
				recordErr(moveRegularFile(pair[0], pair[1], 0))
			}

			for _, sub := range [][2]string{
				{filepath.Join(legacyStateCand, "backups"), ctx.WSBackupsDir()},
				{filepath.Join(legacyStateCand, "exports"), ctx.WSExportsDir()},
			} {
				moveDirRegularFiles(sub[0], sub[1], recordErr)
				_ = os.Remove(sub[0])
			}

			if entries, rErr := os.ReadDir(legacyStateCand); rErr == nil {
				if len(entries) == 1 && entries[0].Name() == "ledger.lock" && !entries[0].IsDir() {
					_ = os.Remove(filepath.Join(legacyStateCand, "ledger.lock"))
				}
				_ = os.Remove(legacyStateCand)
			}
		}

		if pathExists(legacyKeyCand) {
			recordErr(moveRegularFile(legacyKeyCand, ctx.WSKeyPath(), 0o600))
		}

		return firstErr
	})
}

func pathExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Lstat(p)
	return err == nil
}

func withMigrateLock(wsHome string, fn func() error) error {
	if err := os.MkdirAll(wsHome, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(wsHome, "migrate.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func migrateSQLiteDB(srcDB, dstDB string) error {
	stageDB := dstDB + ".migrate.tmp"
	doneMarker := dstDB + ".migrate.cleanup"

	_, dstErr := os.Lstat(dstDB)
	if dstErr != nil && !errors.Is(dstErr, os.ErrNotExist) {
		return dstErr
	}
	if dstErr == nil {
		if _, merr := os.Lstat(doneMarker); errors.Is(merr, os.ErrNotExist) {
			return nil
		} else if merr != nil {
			return merr
		}

		removeSQLiteFiles(stageDB)
		if err := removeSQLiteFilesChecked(srcDB); err != nil {
			return err
		}
		if err := os.Remove(doneMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	removeSQLiteFiles(stageDB)
	_ = os.Remove(doneMarker)

	if srcDB == "" {
		return nil
	}
	fi, err := os.Lstat(srcDB)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(dstDB), 0o700); err != nil {
		return err
	}
	if err := copyAndVerifySQLiteDB(srcDB, stageDB); err != nil {
		removeSQLiteFiles(stageDB)
		return err
	}
	if err := os.WriteFile(doneMarker, []byte("1\n"), 0o600); err != nil {
		removeSQLiteFiles(stageDB)
		return err
	}
	if err := os.Rename(stageDB, dstDB); err != nil {
		_ = os.Remove(doneMarker)
		removeSQLiteFiles(stageDB)
		return err
	}
	if err := os.Chmod(dstDB, 0o600); err != nil {
		return err
	}
	if err := removeSQLiteFilesChecked(srcDB); err != nil {
		return err
	}
	if err := os.Remove(doneMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func sqliteFileDSN(path, query string) string {
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(filepath.Clean(path)),
		RawQuery: query,
	}).String()
}

func copyAndVerifySQLiteDB(srcDB, stageDB string) error {
	src, err := sql.Open("sqlite", sqliteFileDSN(srcDB, "_pragma=busy_timeout(5000)"))
	if err != nil {
		return fmt.Errorf("open legacy sqlite %s: %w", srcDB, err)
	}
	if _, err := src.Exec("VACUUM INTO ?", stageDB); err != nil {
		_ = src.Close()
		return fmt.Errorf("vacuum legacy sqlite %s: %w", srcDB, err)
	}
	if err := src.Close(); err != nil {
		return fmt.Errorf("close legacy sqlite %s: %w", srcDB, err)
	}
	if err := os.Chmod(stageDB, 0o600); err != nil {
		return err
	}

	dst, err := sql.Open("sqlite", sqliteFileDSN(stageDB, "_pragma=busy_timeout(5000)&mode=ro"))
	if err != nil {
		return fmt.Errorf("open staged sqlite %s: %w", stageDB, err)
	}
	rows, err := dst.Query("PRAGMA integrity_check")
	if err != nil {
		_ = dst.Close()
		return fmt.Errorf("verify staged sqlite %s: %w", stageDB, err)
	}
	var checks []string
	for rows.Next() {
		var msg string
		if serr := rows.Scan(&msg); serr != nil {
			_ = rows.Close()
			_ = dst.Close()
			return fmt.Errorf("scan integrity_check for %s: %w", stageDB, serr)
		}
		checks = append(checks, msg)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = dst.Close()
		return fmt.Errorf("integrity_check rows for %s: %w", stageDB, err)
	}
	if err := rows.Close(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("close integrity_check for %s: %w", stageDB, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close staged sqlite %s: %w", stageDB, err)
	}
	if len(checks) != 1 || checks[0] != "ok" {
		return fmt.Errorf("staged sqlite %s failed integrity_check: %s", stageDB, strings.Join(checks, "; "))
	}

	f, err := os.Open(stageDB)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func removeSQLiteFiles(base string) {
	if base == "" {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(base + suffix)
	}
}

func removeSQLiteFilesChecked(base string) error {
	if base == "" {
		return nil
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(base + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func moveDirRegularFiles(srcDir, dstDir string, recordErr func(error)) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		recordErr(moveRegularFile(filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), 0))
	}
}

func moveRegularFile(src, dst string, forcePerm os.FileMode) error {
	fi, err := os.Lstat(src)
	if err != nil || fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if _, dstErr := os.Lstat(dst); dstErr == nil {
		return nil
	}
	mode := fi.Mode().Perm()
	if forcePerm != 0 {
		mode = forcePerm
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		_ = os.Chmod(dst, mode)
		return nil
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := WriteAtomicFile(dst, data, mode); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

func isStrictSubpath(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
