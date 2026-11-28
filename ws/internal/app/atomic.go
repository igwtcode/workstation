package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func WriteAtomicFile(path string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("file path is required")
	}
	cleanPath := filepath.Clean(path)
	if fi, err := os.Lstat(cleanPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through symlink %s", cleanPath)
	}
	dir := filepath.Dir(cleanPath)
	dirPerm := os.FileMode(0o755)
	if perm&0o077 == 0 {
		dirPerm = 0o700
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".ws-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, cleanPath); err != nil {
		cleanup()
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) && !errors.Is(syncErr, syscall.EROFS) && !errors.Is(syncErr, syscall.EBADF) {
		return syncErr
	}
	return closeErr
}
