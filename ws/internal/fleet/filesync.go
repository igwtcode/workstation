package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/engine"
	"github.com/igwtcode/workstation/ws/internal/store"
)

const (
	maxTextDiffBytes = 512 * 1024
	preSyncSuffix    = ".pre-ws-sync."
)

type FileMeta struct {
	Exists    bool   `json:"exists"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	ModTime   int64  `json:"modTime"`
	Mode      string `json:"mode,omitempty"`
}

type FileDiffEntry struct {
	RelPath  string   `json:"relPath"`
	Root     string   `json:"root"`
	Category string   `json:"category"`
	Status   string   `json:"status"`
	IsSecret bool     `json:"isSecret"`
	Local    FileMeta `json:"local"`
	Remote   FileMeta `json:"remote"`
}

type FileDiffReport struct {
	Node            string          `json:"node"`
	TargetID        string          `json:"targetId"`
	IncludeEnv      bool            `json:"includeEnv"`
	IncludeDB       bool            `json:"includeDb"`
	Roots           []string        `json:"roots"`
	Entries         []FileDiffEntry `json:"entries"`
	SameCount       int             `json:"sameCount"`
	DiffCount       int             `json:"diffCount"`
	LocalOnlyCount  int             `json:"localOnlyCount"`
	RemoteOnlyCount int             `json:"remoteOnlyCount"`
	Warnings        []string        `json:"warnings,omitempty"`
}

type FileSyncDiffOptions struct {
	IncludeEnv bool
	IncludeDB  bool
	ExtraPaths []string
	Runner     ExecRunner
}

type FileSyncOptions struct {
	Direction  string
	RelPaths   []string
	IncludeEnv bool
	IncludeDB  bool
	DryRun     bool
	Runner     ExecRunner
	Now        time.Time
}

type FileSyncResult struct {
	Node        string   `json:"node"`
	Direction   string   `json:"direction"`
	Transferred int      `json:"transferred"`
	Skipped     int      `json:"skipped"`
	Failed      int      `json:"failed"`
	DryRun      bool     `json:"dryRun,omitempty"`
	BackupPaths []string `json:"backupPaths,omitempty"`
	Lines       []string `json:"lines,omitempty"`
}

func ResolveSyncPaths(ctx *app.Context, includeEnv, includeDB bool, extraPaths []string) ([]string, []string, error) {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" {
		return nil, nil, fmt.Errorf("home directory context is required")
	}
	homeClean := filepath.Clean(ctx.HomeDir)
	var warnings []string

	addNormalized := func(raw string, seen map[string]bool, out *[]string) error {
		norm, err := NormalizeSyncRelPath(homeClean, raw)
		if err != nil {
			return err
		}
		if !seen[norm] {
			seen[norm] = true
			*out = append(*out, norm)
		}
		return nil
	}

	seen := map[string]bool{}
	var roots []string

	if len(extraPaths) > 0 {
		for _, p := range extraPaths {
			if strings.TrimSpace(p) == "" {
				continue
			}
			if err := addNormalized(p, seen, &roots); err != nil {
				return nil, nil, err
			}
		}
		return roots, warnings, nil
	}

	overrideRaw := strings.TrimSpace(ctx.LookupEnv("WS_SYNC_PATHS"))
	replaceDefaults := false
	var envExtras []string
	if overrideRaw != "" {
		if strings.HasPrefix(overrideRaw, "=") {
			replaceDefaults = true
			overrideRaw = strings.TrimPrefix(overrideRaw, "=")
		}
		for _, part := range splitCommaOrNewline(overrideRaw) {
			envExtras = append(envExtras, part)
		}
	}

	if !replaceDefaults {
		defaults := []string{".ssh"}
		for _, opRel := range splitCommaOrNewline(ctx.LookupEnv("WS_OP_FILES")) {
			defaults = append(defaults, opRel)
		}
		if ctx.RepoRoot != "" {
			for _, sub := range []string{"mise.local.toml", filepath.Join(".mise", "tasks", "local")} {
				cand := filepath.Join(ctx.RepoRoot, sub)
				rel, err := filepath.Rel(homeClean, cand)
				if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
					warnings = append(warnings, fmt.Sprintf("skipping %s from home-relative fleet diff (outside HOME)", sub))
					continue
				}
				defaults = append(defaults, filepath.ToSlash(rel))
			}
		}
		for _, d := range defaults {
			if err := addNormalized(d, seen, &roots); err != nil {
				warnings = append(warnings, fmt.Sprintf("ignored default sync path %q: %v", d, err))
			}
		}
	}

	for _, e := range envExtras {
		if err := addNormalized(e, seen, &roots); err != nil {
			return nil, warnings, fmt.Errorf("WS_SYNC_PATHS %q: %w", e, err)
		}
	}

	if includeDB {
		if err := addNormalized(".ws/backups/ws-backup-latest.enc", seen, &roots); err != nil {
			warnings = append(warnings, fmt.Sprintf("ignored db snapshot path: %v", err))
		}
	}
	if includeEnv {
		roots = append(roots, "~envfiles")
	}

	return roots, warnings, nil
}

func NormalizeSyncRelPath(homeDir, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("sync path cannot be empty")
	}
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", fmt.Errorf("sync path contains control characters")
	}
	s = strings.ReplaceAll(s, "\\", "/")
	switch {
	case s == "~" || s == "~/":
		return "", fmt.Errorf("refusing to sync entire home directory")
	case strings.HasPrefix(s, "~/"):
		s = strings.TrimPrefix(s, "~/")
	case strings.HasPrefix(s, "$HOME/"):
		s = strings.TrimPrefix(s, "$HOME/")
	case path.IsAbs(s):
		homeSlash := filepath.ToSlash(filepath.Clean(homeDir))
		cleanAbs := path.Clean(s)
		if cleanAbs == homeSlash {
			return "", fmt.Errorf("refusing to sync entire home directory")
		}
		prefix := strings.TrimSuffix(homeSlash, "/") + "/"
		if !strings.HasPrefix(cleanAbs, prefix) {
			return "", fmt.Errorf("path %q must stay inside home directory", raw)
		}
		s = strings.TrimPrefix(cleanAbs, prefix)
	}
	clean := path.Clean(strings.TrimLeft(s, "/"))
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe relative path %q", raw)
	}
	if clean == ".ws/ws.db" || clean == ".ws/ws.db-wal" || clean == ".ws/ws.db-shm" || clean == ".ws/db.key" {
		return "", fmt.Errorf("refusing raw sqlite/key path %q (sync .ws/backups/ws-backup-latest.enc instead)", clean)
	}
	return clean, nil
}

func IsSensitiveSyncPath(relPath string) bool {
	clean := path.Clean(strings.TrimLeft(filepath.ToSlash(relPath), "/"))
	lower := strings.ToLower(clean)
	base := path.Base(lower)

	if strings.HasPrefix(lower, ".ssh/") || lower == ".ssh" {
		if base == "config" || base == "known_hosts" || strings.HasSuffix(base, ".pub") {
			return false
		}
		return true
	}
	if lower == ".aws/credentials" || lower == ".netrc" || lower == ".npmrc" || lower == ".pypirc" || lower == ".docker/config.json" {
		return true
	}
	if base == "mise.local.toml" || base == "db.key" || strings.HasPrefix(base, "ws.db") {
		return true
	}
	if strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") {
		if isEnvTemplateSyncName(base) {
			return false
		}
		return true
	}
	for _, ext := range []string{".key", ".pem", ".p12", ".pfx", ".enc", ".age"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	for _, kw := range []string{"credential", "secret", "token", "passwd", "password", "kubeconfig", ".kube/config"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func DiffNodeFiles(ctx *app.Context, node, targetID string, opts FileSyncDiffOptions) (FileDiffReport, error) {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" {
		return FileDiffReport{}, fmt.Errorf("home directory context is required")
	}
	target := strings.TrimSpace(targetID)
	if target == "" {
		target = strings.TrimSpace(node)
	}
	if err := ValidateNodeTarget(target); err != nil {
		return FileDiffReport{}, err
	}
	runner := opts.Runner
	if runner == nil {
		runner = osExecRunner{}
	}

	roots, warnings, err := ResolveSyncPaths(ctx, opts.IncludeEnv, opts.IncludeDB, opts.ExtraPaths)
	if err != nil {
		return FileDiffReport{}, err
	}

	report := FileDiffReport{
		Node:       nonEmptyStr(node, target),
		TargetID:   target,
		IncludeEnv: opts.IncludeEnv,
		IncludeDB:  opts.IncludeDB,
		Roots:      roots,
		Warnings:   warnings,
	}

	localFiles, localWarn := collectLocalSyncFiles(ctx, roots, opts.IncludeEnv)
	report.Warnings = append(report.Warnings, localWarn...)

	remoteFiles, remoteWarn, err := fetchRemoteFileManifest(target, roots, opts.IncludeEnv, runner)
	report.Warnings = append(report.Warnings, remoteWarn...)
	if err != nil {
		return report, err
	}

	allPaths := make([]string, 0, len(localFiles)+len(remoteFiles))
	seen := map[string]bool{}
	for p := range localFiles {
		seen[p] = true
		allPaths = append(allPaths, p)
	}
	for p := range remoteFiles {
		if !seen[p] {
			seen[p] = true
			allPaths = append(allPaths, p)
		}
	}
	sort.Strings(allPaths)

	for _, rel := range allPaths {
		loc, hasLoc := localFiles[rel]
		rem, hasRem := remoteFiles[rel]
		root := matchSyncRoot(rel, roots)
		cat := classifySyncCategory(ctx, rel, root)
		status := classifyDiffStatus(loc, hasLoc, rem, hasRem)

		switch status {
		case "same":
			report.SameCount++
		case "local-only":
			report.LocalOnlyCount++
		case "remote-only":
			report.RemoteOnlyCount++
		default:
			report.DiffCount++
		}

		report.Entries = append(report.Entries, FileDiffEntry{
			RelPath:  rel,
			Root:     root,
			Category: cat,
			Status:   status,
			IsSecret: IsSensitiveSyncPath(rel),
			Local:    loc.meta,
			Remote:   rem.meta,
		})
	}

	sort.SliceStable(report.Entries, func(i, j int) bool {
		pi := diffStatusRank(report.Entries[i].Status)
		pj := diffStatusRank(report.Entries[j].Status)
		if pi != pj {
			return pi < pj
		}
		return report.Entries[i].RelPath < report.Entries[j].RelPath
	})

	return report, nil
}

func PreviewFileTextDiff(ctx *app.Context, targetID, relPath string, runner ExecRunner) (string, error) {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" {
		return "", fmt.Errorf("home directory context is required")
	}
	norm, err := NormalizeSyncRelPath(ctx.HomeDir, relPath)
	if err != nil {
		return "", err
	}
	if IsSensitiveSyncPath(norm) {
		return fmt.Sprintf("Redacted: %s is sensitive (key, credential, .env, or encrypted backup).\nText diff preview is blocked to prevent secret exposure; compare SHA-256, size, mtime, and mode instead.", norm), nil
	}
	target := strings.TrimSpace(targetID)
	if err := ValidateNodeTarget(target); err != nil {
		return "", err
	}
	if runner == nil {
		runner = osExecRunner{}
	}

	locData, locErr := readSafeRegularFile(ctx.HomeDir, norm, maxTextDiffBytes+1)
	locExists := true
	if errors.Is(locErr, os.ErrNotExist) {
		locExists = false
	} else if locErr != nil {
		return "", locErr
	}
	if len(locData) > maxTextDiffBytes || bytes.IndexByte(locData, 0) >= 0 {
		return fmt.Sprintf("Binary or large (>512 KiB) file %s; compare SHA-256 checksums instead.", norm), nil
	}

	remScript := fmt.Sprintf(
		`%s && p="$HOME"/%s && { if [ ! -e "$p" ]; then exit 44; fi; if [ ! -f "$p" ]; then echo "__WS_NOTREG__" >&2; exit 43; fi; head -c %d -- "$p"; }`,
		remoteSafePathGuard(norm), shellSingleQuote(norm), maxTextDiffBytes+1,
	)
	remOut, remStderr, remErr := runner.Run(nil, "ssh", buildFleetSSHArgs(10, target, remScript)...)
	remExists := true
	if remErr != nil {
		var exitErr interface{ ExitCode() int }
		stderrStr := string(remStderr)
		if strings.Contains(stderrStr, "__WS_SYMLINK_PARENT__") || (errors.As(remErr, &exitErr) && exitErr.ExitCode() == 45) {
			return "", fmt.Errorf("remote path %s has a symlink parent directory; refused", norm)
		}
		if strings.Contains(stderrStr, "__WS_SYMLINK__") || (errors.As(remErr, &exitErr) && exitErr.ExitCode() == 42) {
			return "", fmt.Errorf("remote path %s is a symlink; refused", norm)
		}
		if strings.Contains(stderrStr, "__WS_NOTREG__") || (errors.As(remErr, &exitErr) && exitErr.ExitCode() == 43) {
			return "", fmt.Errorf("remote path %s is not a regular file; refused", norm)
		}
		if strings.Contains(remErr.Error(), "exit status 44") || (errors.As(remErr, &exitErr) && exitErr.ExitCode() == 44) {
			remExists = false
			remOut = nil
		} else {
			return "", formatFleetSSHError("read remote "+norm, target, remErr, remStderr)
		}
	}
	if len(remOut) > maxTextDiffBytes || bytes.IndexByte(remOut, 0) >= 0 {
		return fmt.Sprintf("Binary or large (>512 KiB) remote file %s; compare SHA-256 checksums instead.", norm), nil
	}

	if !locExists && !remExists {
		return fmt.Sprintf("%s does not exist on either host.", norm), nil
	}
	if locExists && remExists && bytes.Equal(locData, remOut) {
		return fmt.Sprintf("%s is byte-identical on local and %s.", norm, target), nil
	}
	return strings.TrimRight(engine.UnifiedDiff("local/"+norm, target+"/"+norm, string(locData), string(remOut)), "\n"), nil
}

func SyncNodeFiles(ctx *app.Context, node, targetID string, opts FileSyncOptions) (FileSyncResult, error) {
	if ctx == nil || strings.TrimSpace(ctx.HomeDir) == "" {
		return FileSyncResult{}, fmt.Errorf("home directory context is required")
	}
	dir := strings.ToLower(strings.TrimSpace(opts.Direction))
	if dir != "push" && dir != "pull" {
		return FileSyncResult{}, fmt.Errorf("invalid sync direction %q (expected push or pull)", opts.Direction)
	}
	target := strings.TrimSpace(targetID)
	if target == "" {
		target = strings.TrimSpace(node)
	}
	if err := ValidateNodeTarget(target); err != nil {
		return FileSyncResult{}, err
	}
	if len(opts.RelPaths) == 0 {
		return FileSyncResult{}, fmt.Errorf("no files selected to sync")
	}
	runner := opts.Runner
	if runner == nil {
		runner = osExecRunner{}
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stamp := now.Format("20060102-150405")
	dryRun := opts.DryRun || ctx.DryRun

	res := FileSyncResult{
		Node:      nonEmptyStr(node, target),
		Direction: dir,
		DryRun:    dryRun,
	}

	seen := map[string]bool{}
	for _, raw := range opts.RelPaths {
		norm, err := NormalizeSyncRelPath(ctx.HomeDir, raw)
		if err != nil {
			res.Failed++
			res.Lines = append(res.Lines, fmt.Sprintf("[skip] %s: %v", raw, err))
			continue
		}
		if seen[norm] {
			continue
		}
		seen[norm] = true

		if dir == "push" {
			if err := pushOneSyncFile(ctx.HomeDir, target, norm, stamp, dryRun, runner, &res); err != nil {
				res.Failed++
				res.Lines = append(res.Lines, fmt.Sprintf("[error] push %s: %v", norm, err))
			} else {
				res.Transferred++
			}
		} else {
			if err := pullOneSyncFile(ctx.HomeDir, target, norm, stamp, dryRun, runner, &res); err != nil {
				res.Failed++
				res.Lines = append(res.Lines, fmt.Sprintf("[error] pull %s: %v", norm, err))
			} else {
				res.Transferred++
			}
		}
	}

	if res.Failed > 0 {
		return res, fmt.Errorf("%s finished with %d error(s) (%d transferred)", dir, res.Failed, res.Transferred)
	}
	return res, nil
}

func pushOneSyncFile(homeDir, target, rel, stamp string, dryRun bool, runner ExecRunner, res *FileSyncResult) error {
	absSrc, err := resolveSafeLocalSyncPath(homeDir, rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(absSrc)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return fmt.Errorf("local source %s is not a regular file", rel)
	}
	data, err := os.ReadFile(absSrc)
	if err != nil {
		return err
	}
	mode := targetSyncFileMode(rel, fi.Mode())
	if dryRun {
		res.Lines = append(res.Lines, fmt.Sprintf("[dry-run] push %s -> %s:~/%s (mode %04o; existing backed up with %s%s)", rel, target, rel, mode, preSyncSuffix, stamp))
		return nil
	}

	destExpr := fmt.Sprintf(`"$HOME"/%s`, shellSingleQuote(rel))
	dirExpr := fmt.Sprintf(`"$HOME"/%s`, shellSingleQuote(path.Dir(rel)))
	dirUmask := "022"
	if strings.HasPrefix(rel, ".ssh/") || mode&0o077 == 0 {
		dirUmask = "077"
	}
	remoteScript := fmt.Sprintf(
		`%s && dir=%s && { [ "$dir" = "$HOME/." ] || [ -d "$dir" ] || (umask %s && mkdir -p "$dir"); } && `+
			`umask 077 && dest=%s && if [ -L "$dest" ]; then echo "__WS_SYMLINK__" >&2; exit 42; fi && `+
			`bk="" && if [ -e "$dest" ]; then bk="$dest%s%s" && cat -- "$dest" > "$bk" && chmod 0600 "$bk"; fi && `+
			`tmp="${dest}.ws-tmp.$$" && trap 'rm -f "$tmp"' EXIT && : > "$tmp" && chmod 0600 "$tmp" && cat > "$tmp" && chmod %04o "$tmp" && mv -f "$tmp" "$dest" && `+
			`printf 'OK\t%%s\n' "$bk"`,
		remoteSafePathGuard(rel), dirExpr, dirUmask, destExpr, preSyncSuffix, stamp, mode,
	)

	out, stderr, err := runner.Run(bytes.NewReader(data), "ssh", buildFleetSSHArgs(15, target, remoteScript)...)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "__WS_SYMLINK_PARENT__") || (errors.As(err, &exitErr) && exitErr.ExitCode() == 45) {
			return fmt.Errorf("remote path %s has a symlink parent directory; refused", rel)
		}
		if strings.Contains(stderrStr, "__WS_SYMLINK__") || (errors.As(err, &exitErr) && exitErr.ExitCode() == 42) {
			return fmt.Errorf("remote path %s is a symlink; refused", rel)
		}
		return formatFleetSSHError("ssh push "+rel, target, err, stderr)
	}
	bkNote := ""
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(ln, "OK\t") {
			bk := strings.TrimSpace(strings.TrimPrefix(ln, "OK\t"))
			if bk != "" {
				res.BackupPaths = append(res.BackupPaths, target+":"+bk)
				bkNote = " (remote backup created)"
			}
		}
	}
	res.Lines = append(res.Lines, fmt.Sprintf("pushed %s -> %s:~/%s [%04o]%s", rel, target, rel, mode, bkNote))
	return nil
}

func pullOneSyncFile(homeDir, target, rel, stamp string, dryRun bool, runner ExecRunner, res *FileSyncResult) error {
	absDest, err := resolveSafeLocalSyncPath(homeDir, rel)
	if err != nil {
		return err
	}
	var existingMode fs.FileMode
	if fi, statErr := os.Lstat(absDest); statErr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite local symlink %s", rel)
		}
		existingMode = fi.Mode().Perm()
	}
	if dryRun {
		res.Lines = append(res.Lines, fmt.Sprintf("[dry-run] pull %s:~/%s -> %s (existing backed up with %s%s)", target, rel, rel, preSyncSuffix, stamp))
		return nil
	}

	remoteScript := fmt.Sprintf(
		`%s && p="$HOME"/%s && { if [ ! -f "$p" ]; then echo "__WS_MISSING__" >&2; exit 44; fi; m=$(stat -c '%%a' "$p" 2>/dev/null || stat -f '%%Lp' "$p" 2>/dev/null || true); [ -n "$m" ] && printf '__WS_MODE__:%%s\n' "$m" >&2; cat -- "$p"; }`,
		remoteSafePathGuard(rel), shellSingleQuote(rel),
	)
	data, stderr, err := runner.Run(nil, "ssh", buildFleetSSHArgs(15, target, remoteScript)...)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		stderrStr := string(stderr)
		if strings.Contains(stderrStr, "__WS_SYMLINK_PARENT__") || (errors.As(err, &exitErr) && exitErr.ExitCode() == 45) {
			return fmt.Errorf("remote path %s has a symlink parent directory; refused", rel)
		}
		if strings.Contains(stderrStr, "__WS_SYMLINK__") || (errors.As(err, &exitErr) && exitErr.ExitCode() == 42) {
			return fmt.Errorf("remote path %s is a symlink; refused", rel)
		}
		return formatFleetSSHError("ssh pull "+rel, target, err, stderr)
	}

	bkNote := ""
	if existing, rerr := os.ReadFile(absDest); rerr == nil {
		bkPath := absDest + preSyncSuffix + stamp
		if werr := app.WriteAtomicFile(bkPath, existing, 0o600); werr != nil {
			return fmt.Errorf("backup existing local %s: %w", rel, werr)
		}
		res.BackupPaths = append(res.BackupPaths, bkPath)
		bkNote = fmt.Sprintf(" (backup: %s)", filepath.Base(bkPath))
	}

	srcMode := parseRemoteFileMode(stderr)
	if srcMode == 0 {
		srcMode = existingMode
	}
	mode := targetSyncFileMode(rel, srcMode)
	if strings.HasPrefix(rel, ".ssh/") || mode&0o077 == 0 {
		if merr := os.MkdirAll(filepath.Dir(absDest), 0o700); merr != nil {
			return merr
		}
	}
	if werr := app.WriteAtomicFile(absDest, data, mode); werr != nil {
		return werr
	}
	res.Lines = append(res.Lines, fmt.Sprintf("pulled %s:~/%s -> %s [%04o]%s", target, rel, rel, mode, bkNote))
	return nil
}

func isEnvTemplateSyncName(base string) bool {
	for _, suf := range []string{".example", ".template", ".sample", ".dist"} {
		if strings.HasSuffix(base, suf) || strings.HasSuffix(base, suf+".env") {
			return true
		}
	}
	switch base {
	case "example.env", "sample.env", "template.env", "dist.env":
		return true
	}
	return false
}

func parseRemoteFileMode(stderr []byte) fs.FileMode {
	for _, ln := range strings.Split(string(stderr), "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "__WS_MODE__:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(ln, "__WS_MODE__:"))
		if raw == "" || len(raw) > 4 {
			continue
		}
		if v, err := strconv.ParseUint(raw, 8, 32); err == nil {
			return fs.FileMode(v & 0o777)
		}
	}
	return 0
}

func targetSyncFileMode(rel string, srcMode fs.FileMode) os.FileMode {
	if IsSensitiveSyncPath(rel) {
		return 0o600
	}
	base := os.FileMode(0o644)
	if (srcMode != 0 && srcMode&0o111 != 0) || strings.Contains(rel, "/.mise/tasks/") || strings.HasPrefix(rel, ".local/bin/") {
		base = 0o755
	}
	if srcMode != 0 {
		mode := base & srcMode.Perm()
		if mode&0o400 == 0 {
			mode |= 0o600
		}
		return mode
	}
	return base
}

type localFileRecord struct {
	meta FileMeta
}

func collectLocalSyncFiles(ctx *app.Context, roots []string, includeEnv bool) (map[string]localFileRecord, []string) {
	out := map[string]localFileRecord{}
	var warnings []string
	homeClean := filepath.Clean(ctx.HomeDir)

	walkEntry := func(rootRel string, envOnly bool) {
		abs := filepath.Join(homeClean, filepath.FromSlash(rootRel))
		if err := checkNoSymlinkParent(homeClean, abs); err != nil {
			warnings = append(warnings, fmt.Sprintf("local symlink parent skipped: ~/%s", rootRel))
			return
		}
		fi, err := os.Lstat(abs)
		if err != nil {
			return
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			warnings = append(warnings, fmt.Sprintf("local symlink skipped: ~/%s", rootRel))
			return
		}
		if !fi.IsDir() {
			if rec, ok := inspectLocalRegularFile(abs, fi); ok {
				out[rootRel] = rec
			}
			return
		}
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				warnings = append(warnings, fmt.Sprintf("local symlink skipped: %s", p))
				return nil
			}
			if d.Type()&os.ModeSocket != 0 {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if name == ".git" || name == "node_modules" || name == "agent" && filepath.Base(filepath.Dir(p)) == ".ssh" {
					return filepath.SkipDir
				}
				if envOnly {
					relDepth, rerr := filepath.Rel(abs, p)
					if rerr == nil && relDepth != "." && strings.Count(relDepth, string(filepath.Separator))+1 > 5 {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !d.Type().IsRegular() || shouldSkipSyncFilename(name) {
				return nil
			}
			if envOnly && name != ".env" && !strings.HasPrefix(name, ".env.") {
				return nil
			}
			relFromHome, rerr := filepath.Rel(homeClean, p)
			if rerr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(relFromHome)
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			if rec, ok := inspectLocalRegularFile(p, info); ok {
				out[relSlash] = rec
			}
			return nil
		})
	}

	for _, r := range roots {
		if r == "~envfiles" {
			continue
		}
		walkEntry(r, false)
	}
	if includeEnv {
		for _, envRoot := range discoverLocalEnvRoots(ctx, homeClean) {
			walkEntry(envRoot, true)
		}
	}
	return out, warnings
}

func discoverLocalEnvRoots(ctx *app.Context, homeClean string) []string {
	var candidates []string
	seen := map[string]bool{}
	add := func(rel string) {
		rel = strings.Trim(filepath.ToSlash(rel), "/")
		if rel == "" || rel == "." || seen[rel] {
			return
		}
		if fi, err := os.Lstat(filepath.Join(homeClean, filepath.FromSlash(rel))); err == nil && fi.IsDir() {
			seen[rel] = true
			candidates = append(candidates, rel)
		}
	}
	if ctx != nil {
		if cr := strings.TrimSpace(ctx.LookupEnv("WS_CODE_ROOT")); cr != "" {
			if norm, err := NormalizeSyncRelPath(homeClean, cr); err == nil {
				add(norm)
			}
		}
	}
	add("code")
	entries, err := os.ReadDir(homeClean)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				add(path.Join(e.Name(), "code"))
			}
		}
	}
	return candidates
}

func inspectLocalRegularFile(abs string, info fs.FileInfo) (localFileRecord, bool) {
	if !info.Mode().IsRegular() || shouldSkipSyncFilename(filepath.Base(abs)) {
		return localFileRecord{}, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return localFileRecord{}, false
	}
	sum := sha256.Sum256(data)
	return localFileRecord{
		meta: FileMeta{
			Exists:    true,
			SHA256:    hex.EncodeToString(sum[:]),
			SizeBytes: info.Size(),
			ModTime:   info.ModTime().Unix(),
			Mode:      fmt.Sprintf("%04o", info.Mode().Perm()),
		},
	}, true
}

func shouldSkipSyncFilename(name string) bool {
	return name == ".DS_Store" || strings.Contains(name, preSyncSuffix) ||
		name == "ws.db" || name == "ws.db-wal" || name == "ws.db-shm" || name == "db.key"
}

func resolveSafeLocalSyncPath(homeDir, rel string) (string, error) {
	abs, err := store.ResolveSafeHomePath(homeDir, filepath.Join(homeDir, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if err := checkNoSymlinkParent(homeDir, abs); err != nil {
		return "", err
	}
	return abs, nil
}

func isWithinDir(target, root string) bool {
	cleanTarget := filepath.Clean(target)
	cleanRoot := filepath.Clean(root)
	if cleanTarget == cleanRoot {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(cleanRoot, sep) {
		cleanRoot += sep
	}
	return strings.HasPrefix(cleanTarget, cleanRoot)
}

func checkNoSymlinkParent(homeDir, targetPath string) error {
	cleanHome, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return err
	}
	evalHome := cleanHome
	if resolved, rerr := filepath.EvalSymlinks(cleanHome); rerr == nil {
		evalHome = resolved
	}
	cleanTarget, err := filepath.Abs(filepath.Clean(targetPath))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(cleanHome, filepath.Dir(cleanTarget))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s escapes home directory", targetPath)
	}
	cur := cleanHome
	if rel != "." {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "" || part == "." {
				continue
			}
			cur = filepath.Join(cur, part)
			fi, lerr := os.Lstat(cur)
			if lerr != nil {
				if errors.Is(lerr, os.ErrNotExist) {
					break
				}
				return lerr
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				resolved, rerr := filepath.EvalSymlinks(cur)
				if rerr != nil {
					return fmt.Errorf("refusing unresolved symlink parent directory %s: %w", cur, rerr)
				}
				if !isWithinDir(resolved, evalHome) {
					return fmt.Errorf("refusing symlink parent directory %s escaping home", cur)
				}
			}
		}
	}
	return nil
}

func remoteSafePathGuard(rel string) string {
	return fmt.Sprintf(
		`{ _home=$(cd -P -- "$HOME" 2>/dev/null && pwd -P) || { echo "__WS_SYMLINK_PARENT__" >&2; exit 45; }; _hp="${_home%%/}"; _rem=%s; _cur="$HOME"; while [ -n "$_rem" ]; do case "$_rem" in */*) _seg="${_rem%%%%/*}"; _rem="${_rem#*/}"; _cur="$_cur/$_seg"; if [ -L "$_cur" ]; then _res=$(cd -P -- "$_cur" 2>/dev/null && pwd -P) || { echo "__WS_SYMLINK_PARENT__" >&2; exit 45; }; case "$_res" in "$_home"|"$_hp"/*) ;; *) echo "__WS_SYMLINK_PARENT__" >&2; exit 45 ;; esac; fi ;; *) _cur="$_cur/$_rem"; _rem=""; if [ -L "$_cur" ]; then echo "__WS_SYMLINK__" >&2; exit 42; fi ;; esac; done; }`,
		shellSingleQuote(rel),
	)
}

func readSafeRegularFile(homeDir, rel string, maxBytes int64) ([]byte, error) {
	abs, err := resolveSafeLocalSyncPath(homeDir, rel)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if maxBytes > 0 && fi.Size() > maxBytes {
		return make([]byte, maxBytes), nil
	}
	return os.ReadFile(abs)
}

func fetchRemoteFileManifest(target string, roots []string, includeEnv bool, runner ExecRunner) (map[string]localFileRecord, []string, error) {
	var quotedRoots []string
	for _, r := range roots {
		if r == "~envfiles" {
			continue
		}
		quotedRoots = append(quotedRoots, shellSingleQuote(r))
	}
	envFlag := "0"
	if includeEnv {
		envFlag = "1"
	}

	probeScript := fmt.Sprintf(`set -u
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}' | sed 's/^\\//'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  fi
}
stat_meta() {
  if out=$(stat -c '%%s %%Y %%a' "$1" 2>/dev/null) && [ -n "$out" ]; then
    printf '%%s\n' "$out"
  elif out=$(stat -f '%%z %%m %%Lp' "$1" 2>/dev/null) && [ -n "$out" ]; then
    printf '%%s\n' "$out"
  else
    printf '0 0 644\n'
  fi
}
emit_file() {
  f="$1"
  rel="$2"
  b=$(basename "$f")
  case "$b" in
    .DS_Store|*.pre-ws-sync.*|ws.db|ws.db-*|db.key) return ;;
  esac
  if [ -L "$f" ]; then
    printf 'W\tremote symlink skipped: ~/%%s\n' "$rel"
    return
  fi
  [ -f "$f" ] || return
  h=$(hash_file "$f")
  m=$(stat_meta "$f")
  IFS=' 	' read -r sz mt md <<EOF
$m
EOF
  printf 'F\t%%s\t%%s\t%%s\t%%s\t%%s\n' "$rel" "$h" "${sz:-0}" "${mt:-0}" "${md:-644}"
}
check_safe_ancestors() {
  _home=$(cd -P -- "$HOME" 2>/dev/null && pwd -P) || return 1
  _hp="${_home%%/}"
  _rem="$1"
  _cur="$HOME"
  while [ -n "$_rem" ]; do
    case "$_rem" in
      */*)
        _seg="${_rem%%%%/*}"
        _rem="${_rem#*/}"
        [ -n "$_seg" ] || continue
        _cur="$_cur/$_seg"
        if [ -L "$_cur" ]; then
          _res=$(cd -P -- "$_cur" 2>/dev/null && pwd -P) || return 1
          case "$_res" in
            "$_home"|"$_hp"/*) ;;
            *) return 1 ;;
          esac
        fi
        ;;
      *)
        _seg="$_rem"
        _rem=""
        [ -n "$_seg" ] || continue
        _cur="$_cur/$_seg"
        if [ -L "$_cur" ]; then
          return 1
        fi
        ;;
    esac
  done
  return 0
}
scan_root() {
  r="$1"
  p="$HOME/$r"
  if ! check_safe_ancestors "$r"; then
    printf 'W\tremote symlink skipped: ~/%%s\n' "$r"
    return
  fi
  if [ -f "$p" ]; then
    emit_file "$p" "$r"
    return
  fi
  if [ -d "$p" ]; then
    find "$p" \( -name .git -o -name node_modules -o -path '*/.ssh/agent' \) -prune -o -type f -print 2>/dev/null | while IFS= read -r f; do
      rel="${f#"$HOME/"}"
      emit_file "$f" "$rel"
    done
  fi
}
for r in %s; do
  scan_root "$r"
done
if [ "%s" = "1" ]; then
  for d in "$HOME/code" "$HOME"/*/code; do
    [ -d "$d" ] || continue
    rel_d="${d#"$HOME/"}"
    check_safe_ancestors "$rel_d" || continue
    find "$d" -maxdepth 5 \( -name .git -o -name node_modules \) -prune -o -type f \( -name '.env' -o -name '.env.*' \) -print 2>/dev/null | while IFS= read -r f; do
      rel="${f#"$HOME/"}"
      emit_file "$f" "$rel"
    done
  done
fi
`, strings.Join(quotedRoots, " "), envFlag)

	out, stderr, err := runner.Run(nil, "ssh", buildFleetSSHArgs(15, target, probeScript)...)
	if err != nil {
		return nil, nil, formatFleetSSHError("probe remote files", target, err, stderr)
	}

	files := map[string]localFileRecord{}
	var warnings []string
	for _, rawLine := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "W\t") {
			warnings = append(warnings, strings.TrimPrefix(line, "W\t"))
			continue
		}
		if !strings.HasPrefix(line, "F\t") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 4 {
			tail := strings.ReplaceAll(strings.TrimSpace(parts[3]), `\t`, " ")
			if fields := strings.Fields(tail); len(fields) == 3 {
				parts = []string{parts[0], parts[1], parts[2], fields[0], fields[1], fields[2]}
			}
		}
		if len(parts) < 6 {
			continue
		}
		rel := path.Clean(strings.TrimLeft(parts[1], "/"))
		if rel == "." || strings.HasPrefix(rel, "..") || shouldSkipSyncFilename(path.Base(rel)) {
			continue
		}

		size, _ := strconv.ParseInt(parts[3], 10, 64)
		mtime, _ := strconv.ParseInt(parts[4], 10, 64)
		mode := fmt.Sprintf("%04s", parts[5])
		if v, perr := strconv.ParseUint(parts[5], 8, 32); perr == nil {
			mode = fmt.Sprintf("%04o", v&0o777)
		}
		files[rel] = localFileRecord{
			meta: FileMeta{
				Exists:    true,
				SHA256:    strings.ToLower(strings.TrimSpace(parts[2])),
				SizeBytes: size,
				ModTime:   mtime,
				Mode:      mode,
			},
		}
	}
	return files, warnings, nil
}

func classifyDiffStatus(loc localFileRecord, hasLoc bool, rem localFileRecord, hasRem bool) string {
	switch {
	case hasLoc && !hasRem:
		return "local-only"
	case !hasLoc && hasRem:
		return "remote-only"
	case loc.meta.SHA256 == rem.meta.SHA256:
		return "same"
	case loc.meta.ModTime > rem.meta.ModTime+1:
		return "local-newer"
	case rem.meta.ModTime > loc.meta.ModTime+1:
		return "remote-newer"
	default:
		return "modified"
	}
}

func diffStatusRank(status string) int {
	switch status {
	case "local-newer", "remote-newer", "modified":
		return 0
	case "local-only", "remote-only":
		return 1
	default:
		return 2
	}
}

func matchSyncRoot(rel string, roots []string) string {
	for _, r := range roots {
		if r == "~envfiles" {
			continue
		}
		if rel == r || strings.HasPrefix(rel, r+"/") {
			return r
		}
	}
	base := path.Base(rel)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return "~envfiles"
	}
	return path.Dir(rel)
}

func classifySyncCategory(ctx *app.Context, rel, root string) string {
	switch {
	case rel == ".ssh" || strings.HasPrefix(rel, ".ssh/"):
		return "ssh"
	case rel == ".ws/backups/ws-backup-latest.enc":
		return "db"
	case path.Base(rel) == ".env" || strings.HasPrefix(path.Base(rel), ".env."):
		return "envfiles"
	case strings.HasSuffix(rel, "mise.local.toml") || strings.Contains(rel, ".mise/tasks/local"):
		return "secrets"
	case ctx != nil && root != "":
		for _, opRel := range splitCommaOrNewline(ctx.LookupEnv("WS_OP_FILES")) {
			if norm, err := NormalizeSyncRelPath(ctx.HomeDir, opRel); err == nil && (rel == norm || strings.HasPrefix(rel, norm+"/")) {
				return "files"
			}
		}
	}
	return "files"
}

func splitCommaOrNewline(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	for _, ln := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(ln)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func nonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func formatFleetSSHError(op, target string, err error, stderr []byte) error {
	raw := strings.TrimSpace(string(stderr))
	trimmed := trimBoundedStderr(stderr)
	base := fmt.Sprintf("%s on %s: %v", op, target, err)
	if trimmed != "" {
		base += fmt.Sprintf(" (%s)", trimmed)
	}
	if strings.Contains(raw, "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(raw, "Host key verification failed") {
		base += fmt.Sprintf(" — if host was rebuilt or switched to Tailscale SSH, run: ssh-keygen -R %s", target)
	}
	return errors.New(base)
}
