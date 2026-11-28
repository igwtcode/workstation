package cleanup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
)

var (
	ErrUnsafeTarget = errors.New("unsafe cleanup target")

	ErrSymlinkTarget = errors.New("symlink component refused in cleanup target")

	ErrOutsideAllowlist = errors.New("cleanup target outside allowlisted roots")

	ErrDangerousBlocked = errors.New("dangerous cleanup tier blocked")
)

func ValidateTarget(ctx *app.Context, candidate string, allowlistedRoots []string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafeTarget)
	}
	if !filepath.IsAbs(candidate) {
		return "", fmt.Errorf("%w: relative path refused: %q", ErrUnsafeTarget, candidate)
	}

	clean := filepath.Clean(candidate)
	if isFilesystemRoot(clean) {
		return "", fmt.Errorf("%w: filesystem root refused", ErrUnsafeTarget)
	}

	var cleanHome, cleanRepo string
	var realHome, realRepo string
	if ctx != nil {
		if ctx.HomeDir != "" {
			cleanHome = filepath.Clean(ctx.HomeDir)
			realHome = evalExisting(cleanHome)
		}
		if ctx.RepoRoot != "" {
			cleanRepo = filepath.Clean(ctx.RepoRoot)
			realRepo = evalExisting(cleanRepo)
		}
	}

	if cleanHome != "" && isWithinOrEqual(cleanHome, clean) {
		return "", fmt.Errorf("%w: home directory or ancestor refused: %s", ErrUnsafeTarget, clean)
	}
	if cleanRepo != "" && (isWithinOrEqual(clean, cleanRepo) || isWithinOrEqual(cleanRepo, clean)) {
		return "", fmt.Errorf("%w: repository path or ancestor refused: %s", ErrUnsafeTarget, clean)
	}

	matchedRoot := ""
	matchedStrictParent := false
	for _, rawRoot := range allowlistedRoots {
		if strings.TrimSpace(rawRoot) == "" || !filepath.IsAbs(rawRoot) {
			continue
		}
		r := filepath.Clean(rawRoot)
		if isFilesystemRoot(r) {
			continue
		}
		if cleanHome != "" && isWithinOrEqual(cleanHome, r) {
			continue
		}
		if realHome != "" && isWithinOrEqual(realHome, evalExisting(r)) {
			continue
		}
		if cleanRepo != "" && isWithinOrEqual(r, cleanRepo) {
			continue
		}
		if isStrictParentRoot(ctx, r) {
			if isStrictAncestor(r, clean) {
				matchedRoot = r
				matchedStrictParent = true
				break
			}
			continue
		}
		if isWithinOrEqual(clean, r) {
			matchedRoot = r
			break
		}
	}
	if matchedRoot == "" {
		return "", fmt.Errorf("%w: %s", ErrOutsideAllowlist, clean)
	}

	if err := checkNoSymlinkAncestors(clean, matchedRoot, cleanHome); err != nil {
		return "", err
	}

	realCandidate := evalExisting(clean)
	if isFilesystemRoot(realCandidate) {
		return "", fmt.Errorf("%w: resolved path is filesystem root", ErrUnsafeTarget)
	}
	if realHome != "" && isWithinOrEqual(realHome, realCandidate) {
		return "", fmt.Errorf("%w: resolved path is home directory or ancestor: %s", ErrUnsafeTarget, realCandidate)
	}
	if realRepo != "" && (isWithinOrEqual(realCandidate, realRepo) || isWithinOrEqual(realRepo, realCandidate)) {
		return "", fmt.Errorf("%w: resolved path overlaps repository: %s", ErrUnsafeTarget, realCandidate)
	}
	realRoot := evalExisting(matchedRoot)
	if realHome != "" && isWithinOrEqual(realHome, realRoot) {
		return "", fmt.Errorf("%w: allowlisted root contains home directory: %s", ErrOutsideAllowlist, realRoot)
	}
	if matchedStrictParent {
		if !isStrictAncestor(realRoot, realCandidate) {
			return "", fmt.Errorf("%w: resolved path %s must be a child of %s", ErrOutsideAllowlist, realCandidate, realRoot)
		}
	} else if !isWithinOrEqual(realCandidate, realRoot) {
		return "", fmt.Errorf("%w: resolved path %s escapes allowlisted root %s", ErrOutsideAllowlist, realCandidate, realRoot)
	}

	return clean, nil
}

func isStrictParentRoot(ctx *app.Context, cleanRoot string) bool {
	if ctx == nil {
		return false
	}
	for _, r := range strictParentRoots(ctx) {
		if cleanRoot == r || evalExisting(cleanRoot) == evalExisting(r) {
			return true
		}
	}
	return false
}

func SafeDirSize(path string, maxDepth int) (int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, nil
	}
	if !info.IsDir() {
		return info.Size(), nil
	}
	return safeDirSizeWalk(path, 0, maxDepth), nil
}

func safeDirSizeWalk(dir string, depth, maxDepth int) int64 {
	if maxDepth >= 0 && depth > maxDepth {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if maxDepth < 0 || depth < maxDepth {
				total += safeDirSizeWalk(filepath.Join(dir, entry.Name()), depth+1, maxDepth)
			}
			continue
		}
		fi, err := entry.Info()
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		total += fi.Size()
	}
	return total
}

func checkNoSymlinkAncestors(clean, matchedRoot, cleanHome string) error {
	cur := clean
	for {
		fi, err := os.Lstat(cur)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				if !isDarwinSystemAncestor(cur, clean, matchedRoot, cleanHome) &&
					!isOSHomeRootAncestor(cur, clean, matchedRoot, cleanHome) {
					return fmt.Errorf("%w: %s", ErrSymlinkTarget, cur)
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: lstat %s: %v", ErrUnsafeTarget, cur, err)
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}

func isOSHomeRootAncestor(cur, candidate, matchedRoot, cleanHome string) bool {
	if cleanHome == "" || filepath.Dir(cur) != string(filepath.Separator) {
		return false
	}
	if cur == candidate || cur == matchedRoot || cur == cleanHome {
		return false
	}
	return isStrictAncestor(cur, cleanHome) && isStrictAncestor(cur, matchedRoot) && isStrictAncestor(cur, candidate)
}

func isDarwinSystemAncestor(cur, candidate, matchedRoot, cleanHome string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if cur != "/var" && cur != "/tmp" {
		return false
	}
	if cur == candidate || cur == matchedRoot || cur == cleanHome {
		return false
	}
	linkTarget, err := os.Readlink(cur)
	if err != nil {
		return false
	}
	expected := "private" + cur
	if linkTarget != expected && linkTarget != "/"+expected {
		return false
	}
	return isStrictAncestor(cur, candidate) && isStrictAncestor(cur, matchedRoot)
}

func isFilesystemRoot(p string) bool {
	return p == "" || p == string(filepath.Separator) || filepath.Dir(p) == p
}

func isWithinOrEqual(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	if strings.HasSuffix(root, sep) {
		return strings.HasPrefix(path, root)
	}
	return strings.HasPrefix(path, root+sep)
}

func isStrictAncestor(ancestor, descendant string) bool {
	return ancestor != descendant && isWithinOrEqual(descendant, ancestor)
}

func evalExisting(path string) string {
	clean := filepath.Clean(path)
	var suffix []string
	cur := clean
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Clean(filepath.Join(parts...))
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}
