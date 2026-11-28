package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

type Tier string

const (
	TierSafe      Tier = "safe"
	TierCostly    Tier = "costly"
	TierDangerous Tier = "dangerous"
)

type Candidate struct {
	ID                string   `json:"id"`
	Tier              Tier     `json:"tier"`
	Category          string   `json:"category"`
	Title             string   `json:"title"`
	Paths             []string `json:"paths,omitempty"`
	CleanerCmd        []string `json:"cleanerCmd,omitempty"`
	Bytes             int64    `json:"bytes"`
	Warning           string   `json:"warning,omitempty"`
	SelectedByDefault bool     `json:"selectedByDefault"`
}

const cargoStaleAge = 30 * 24 * time.Hour

func AllowlistedRoots(ctx *app.Context) []string {
	if ctx == nil {
		return nil
	}
	var roots []string
	add := func(p string) {
		c := filepath.Clean(p)
		if !slices.Contains(roots, c) {
			roots = append(roots, c)
		}
	}
	for _, r := range strictParentRoots(ctx) {
		add(r)
	}
	for _, r := range exactOrChildRoots(ctx) {
		add(r)
	}
	return roots
}

func strictParentRoots(ctx *app.Context) []string {
	if ctx == nil {
		return nil
	}
	var roots []string
	add := func(p string) {
		if strings.TrimSpace(p) == "" || !filepath.IsAbs(p) {
			return
		}
		c := filepath.Clean(p)
		if !slices.Contains(roots, c) {
			roots = append(roots, c)
		}
	}
	if ctx.CacheDir != "" {
		add(ctx.CacheDir)
	}
	if ctx.HomeDir != "" {
		add(filepath.Join(ctx.HomeDir, "Library", "Caches"))
	}
	return roots
}

func exactOrChildRoots(ctx *app.Context) []string {
	if ctx == nil || ctx.HomeDir == "" {
		return nil
	}
	var roots []string
	for _, p := range []string{
		filepath.Join(ctx.HomeDir, ".npm", "_cacache"),
		filepath.Join(ctx.HomeDir, "Library", "Developer", "Xcode", "DerivedData"),
		filepath.Join(ctx.HomeDir, ".gradle", "caches"),
		filepath.Join(ctx.HomeDir, ".android", "avd"),
	} {
		c := filepath.Clean(p)
		if !slices.Contains(roots, c) {
			roots = append(roots, c)
		}
	}
	return roots
}

func codeRoots(ctx *app.Context) []string {
	if ctx == nil {
		return nil
	}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = ctx.ExpandHome(p)
		if !filepath.IsAbs(p) {
			return
		}
		c := filepath.Clean(p)
		if isFilesystemRoot(c) {
			return
		}
		if ctx.HomeDir != "" && isWithinOrEqual(filepath.Clean(ctx.HomeDir), c) {
			return
		}
		if ctx.RepoRoot != "" && isWithinOrEqual(c, filepath.Clean(ctx.RepoRoot)) {
			return
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if envRoot := ctx.LookupEnv("WS_CODE_ROOT"); envRoot != "" {
		add(envRoot)
	}
	if ctx.HomeDir != "" {
		add(filepath.Join(ctx.HomeDir, "personal", "code"))
		add(filepath.Join(ctx.HomeDir, "work", "code"))
	}
	return out
}

func scanAllCandidates(ctx *app.Context, runner CommandRunner, now time.Time) []Candidate {
	var out []Candidate

	out = append(out, scanPackageCaches(ctx, runner)...)
	out = append(out, scanCLICaches(ctx, runner)...)
	if c := scanPreWSBackups(ctx); c != nil {
		out = append(out, *c)
	}

	if c := scanStaleCargoTargets(ctx, now); c != nil {
		out = append(out, *c)
	}
	if c := scanDockerBuildx(runner); c != nil {
		out = append(out, *c)
	}
	if c := scanSingleDirCandidate(
		ctx,
		"costly:xcode-derived-data",
		TierCostly,
		"ide",
		"Xcode DerivedData",
		filepath.Join(ctx.HomeDir, "Library", "Developer", "Xcode", "DerivedData"),
		"Rebuilding Xcode projects will take longer until DerivedData is regenerated.",
		false,
	); c != nil {
		out = append(out, *c)
	}
	if c := scanSingleDirCandidate(
		ctx,
		"costly:gradle-caches",
		TierCostly,
		"build",
		"Gradle build caches",
		filepath.Join(ctx.HomeDir, ".gradle", "caches"),
		"Gradle dependencies and build artifacts will be re-downloaded on next build.",
		false,
	); c != nil {
		out = append(out, *c)
	}

	if c := scanSingleDirCandidate(
		ctx,
		"dangerous:android-avd",
		TierDangerous,
		"vm",
		"Android Emulator AVDs",
		filepath.Join(ctx.HomeDir, ".android", "avd"),
		"Permanently destroys Android emulator disk images and local app state.",
		false,
	); c != nil {
		out = append(out, *c)
	}
	if c := scanDockerVolumes(runner); c != nil {
		out = append(out, *c)
	}

	sort.Slice(out, func(i, j int) bool {
		ti, tj := tierOrder(out[i].Tier), tierOrder(out[j].Tier)
		if ti != tj {
			return ti < tj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func scanPackageCaches(ctx *app.Context, runner CommandRunner) []Candidate {
	var out []Candidate
	roots := AllowlistedRoots(ctx)

	var brewPaths []string
	seenBrew := map[string]bool{}
	var brewBytes int64
	brewCandidates := []string{
		filepath.Join(ctx.HomeDir, "Library", "Caches", "Homebrew"),
		filepath.Join(ctx.CacheDir, "Homebrew"),
	}
	for _, p := range brewCandidates {
		valid, err := ValidateTarget(ctx, p, roots)
		if err != nil {
			continue
		}
		canon := evalExisting(valid)
		if seenBrew[canon] {
			continue
		}
		seenBrew[canon] = true
		sz, err := SafeDirSize(valid, 8)
		if err == nil && sz > 0 {
			brewPaths = append(brewPaths, valid)
			brewBytes += sz
		}
	}
	if brewBytes > 0 {
		c := Candidate{
			ID:                "safe:homebrew-cache",
			Tier:              TierSafe,
			Category:          "pkg-cache",
			Title:             "Homebrew download cache",
			Paths:             brewPaths,
			Bytes:             brewBytes,
			SelectedByDefault: true,
		}
		if hasTool(runner, "brew") {
			c.CleanerCmd = []string{"brew", "cleanup", "--prune=all", "-s"}
		}
		out = append(out, c)
	}

	pacmanCacheDir := "/var/cache/pacman/pkg"
	if ctx.Host.OS == "linux" {
		if sz, err := SafeDirSize(pacmanCacheDir, 3); err == nil && sz > 0 {
			var cmd []string
			switch {
			case hasTool(runner, "paccache"):
				cmd = []string{"sudo", "paccache", "-rk2"}
			case hasTool(runner, "pacman"):
				cmd = []string{"sudo", "pacman", "-Sc", "--noconfirm"}
			}
			if len(cmd) > 0 {
				out = append(out, Candidate{
					ID:                "safe:pacman-cache",
					Tier:              TierSafe,
					Category:          "pkg-cache",
					Title:             "Pacman package cache",
					CleanerCmd:        cmd,
					Bytes:             sz,
					SelectedByDefault: true,
				})
			}
		}
	}

	if paruDir, err := ValidateTarget(ctx, filepath.Join(ctx.CacheDir, "paru", "clone"), roots); err == nil {
		if sz, serr := SafeDirSize(paruDir, 6); serr == nil && sz > 0 {
			out = append(out, Candidate{
				ID:                "safe:paru-cache",
				Tier:              TierSafe,
				Category:          "pkg-cache",
				Title:             "Paru AUR build cache",
				Paths:             []string{paruDir},
				Bytes:             sz,
				SelectedByDefault: true,
			})
		}
	}

	return out
}

func scanCLICaches(ctx *app.Context, runner CommandRunner) []Candidate {
	roots := AllowlistedRoots(ctx)

	type cacheSpec struct {
		id         string
		title      string
		paths      []string
		cleanerBin string
		cleanerCmd []string
	}

	specs := []cacheSpec{
		{
			id:    "safe:cli-bat",
			title: "bat syntax cache",
			paths: []string{filepath.Join(ctx.CacheDir, "bat")},
		},
		{
			id:    "safe:cli-tldr",
			title: "tldr / tealdeer page cache",
			paths: []string{
				filepath.Join(ctx.CacheDir, "tealdeer"),
				filepath.Join(ctx.CacheDir, "tldr"),
			},
		},
		{
			id:         "safe:cli-uv",
			title:      "uv Python package cache",
			paths:      []string{filepath.Join(ctx.CacheDir, "uv")},
			cleanerBin: "uv",
			cleanerCmd: []string{"uv", "cache", "clean"},
		},
		{
			id:    "safe:cli-npm",
			title: "npm cache",
			paths: []string{
				filepath.Join(ctx.HomeDir, ".npm", "_cacache"),
				filepath.Join(ctx.CacheDir, "npm"),
			},
		},
		{
			id:    "safe:cli-go-build",
			title: "Go build cache",
			paths: []string{
				filepath.Join(ctx.CacheDir, "go-build"),
				filepath.Join(ctx.HomeDir, "Library", "Caches", "go-build"),
			},
			cleanerBin: "go",
			cleanerCmd: []string{"go", "clean", "-cache"},
		},
	}

	var out []Candidate
	for _, sp := range specs {
		var validPaths []string
		seenPaths := map[string]bool{}
		var total int64
		for _, p := range sp.paths {
			valid, err := ValidateTarget(ctx, p, roots)
			if err != nil {
				continue
			}
			canon := evalExisting(valid)
			if seenPaths[canon] {
				continue
			}
			seenPaths[canon] = true
			sz, err := SafeDirSize(valid, 8)
			if err == nil && sz > 0 {
				validPaths = append(validPaths, valid)
				total += sz
			}
		}
		if total == 0 {
			continue
		}
		c := Candidate{
			ID:                sp.id,
			Tier:              TierSafe,
			Category:          "cli-cache",
			Title:             sp.title,
			Paths:             validPaths,
			Bytes:             total,
			SelectedByDefault: true,
		}
		if sp.cleanerBin != "" && hasTool(runner, sp.cleanerBin) {
			c.CleanerCmd = sp.cleanerCmd
		}
		out = append(out, c)
	}
	return out
}

func referencedUndoBackups(ledger *app.Ledger) map[string]bool {
	refs := map[string]bool{}
	if ledger == nil {
		return refs
	}
	for _, tx := range ledger.UndoLog {
		for _, op := range tx.Ops {
			bp := strings.TrimSpace(op.BackupPath)
			if bp == "" {
				continue
			}
			clean := filepath.Clean(bp)
			refs[clean] = true
			if resolved := evalExisting(clean); resolved != "" {
				refs[resolved] = true
			}
		}
	}
	return refs
}

func isBackupReferenced(path string, refs map[string]bool) bool {
	if len(refs) == 0 {
		return false
	}
	clean := filepath.Clean(path)
	if refs[clean] {
		return true
	}
	if resolved := evalExisting(clean); resolved != "" && refs[resolved] {
		return true
	}
	return false
}

func VerifyPreWSBackup(ctx *app.Context, backupPath string) (string, error) {
	var refs map[string]bool
	if ctx != nil && ctx.WSBaseDir() != "" {
		ledger, err := app.LoadLedger(ctx.WSBaseDir())
		if err != nil {
			return "", fmt.Errorf("%w: load ledger to verify .pre-ws backup: %v", ErrUnsafeTarget, err)
		}
		refs = referencedUndoBackups(ledger)
	}
	return verifyPreWSBackupWithRefs(ctx, backupPath, refs)
}

func verifyPreWSBackupWithRefs(ctx *app.Context, backupPath string, refs map[string]bool) (string, error) {
	if ctx == nil || ctx.RepoRoot == "" {
		return "", fmt.Errorf("%w: repo root unavailable to verify .pre-ws backup", ErrUnsafeTarget)
	}
	clean := filepath.Clean(backupPath)
	if !strings.HasSuffix(clean, ".pre-ws") {
		return "", fmt.Errorf("%w: not a .pre-ws backup: %s", ErrUnsafeTarget, clean)
	}

	if _, err := ValidateTarget(ctx, clean, []string{clean}); err != nil {
		return "", err
	}
	if isBackupReferenced(clean, refs) {
		return "", fmt.Errorf("%w: backup %s is still referenced by a retained undo transaction", ErrUnsafeTarget, clean)
	}

	origTarget := strings.TrimSuffix(clean, ".pre-ws")
	fi, err := os.Lstat(origTarget)
	if err != nil {
		return "", fmt.Errorf("%w: paired target missing for %s", ErrUnsafeTarget, clean)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%w: paired target %s is not a symlink", ErrUnsafeTarget, origTarget)
	}
	if _, serr := os.Stat(origTarget); serr != nil {
		return "", fmt.Errorf("%w: paired target symlink %s is dangling or unreadable: %v", ErrUnsafeTarget, origTarget, serr)
	}
	realDest, err := filepath.EvalSymlinks(origTarget)
	if err != nil {
		return "", fmt.Errorf("%w: resolve symlink %s: %v", ErrUnsafeTarget, origTarget, err)
	}
	realRepo, err := filepath.EvalSymlinks(filepath.Clean(ctx.RepoRoot))
	if err != nil {
		return "", fmt.Errorf("%w: resolve repo root: %v", ErrUnsafeTarget, err)
	}
	if !isWithinOrEqual(realDest, realRepo) {
		return "", fmt.Errorf("%w: symlink %s points outside repo (%s)", ErrUnsafeTarget, origTarget, realDest)
	}
	return clean, nil
}

func scanPreWSBackups(ctx *app.Context) *Candidate {
	if ctx == nil || ctx.RepoRoot == "" || ctx.HomeDir == "" {
		return nil
	}

	var refs map[string]bool
	if ctx.WSBaseDir() != "" {
		ledger, err := app.LoadLedger(ctx.WSBaseDir())
		if err != nil {
			return nil
		}
		refs = referencedUndoBackups(ledger)
	}

	seen := map[string]bool{}
	var verified []string
	var totalBytes int64

	consider := func(backupPath string) {
		c := filepath.Clean(backupPath)
		if seen[c] {
			return
		}
		seen[c] = true
		valid, err := verifyPreWSBackupWithRefs(ctx, c, refs)
		if err != nil {
			return
		}
		sz, err := SafeDirSize(valid, 6)
		if err != nil {
			return
		}
		verified = append(verified, valid)
		totalBytes += sz
	}

	if presets, err := config.LoadPresets(ctx.RepoRoot); err == nil {
		if resolved, rerr := config.ResolvePreset(ctx.RepoRoot, presets, ctx.EffectivePreset()); rerr == nil {
			if links, lerr := config.LoadLinksForPreset(ctx.RepoRoot, resolved); lerr == nil {
				for _, l := range links {
					targetAbs := ctx.ExpandHome(l.TargetRaw)
					consider(targetAbs + ".pre-ws")
				}
			}
		}
	}

	scanDirForPreWS(ctx.HomeDir, 0, 0, consider)
	if ctx.ConfigDir != "" {
		scanDirForPreWS(ctx.ConfigDir, 0, 3, consider)
	}

	if len(verified) == 0 {
		return nil
	}
	sort.Strings(verified)
	return &Candidate{
		ID:                "safe:pre-ws-backups",
		Tier:              TierSafe,
		Category:          "backups",
		Title:             fmt.Sprintf("Verified leftover *.pre-ws backups (%d)", len(verified)),
		Paths:             verified,
		Bytes:             totalBytes,
		SelectedByDefault: false,
	}
}

func scanDirForPreWS(dir string, depth, maxDepth int, visit func(string)) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if strings.HasSuffix(entry.Name(), ".pre-ws") {
			visit(full)
			continue
		}
		if entry.IsDir() && depth < maxDepth {
			scanDirForPreWS(full, depth+1, maxDepth, visit)
		}
	}
}

func scanStaleCargoTargets(ctx *app.Context, now time.Time) *Candidate {
	roots := codeRoots(ctx)
	if len(roots) == 0 {
		return nil
	}

	var stalePaths []string
	var totalBytes int64
	seen := map[string]bool{}

	for _, root := range roots {
		fi, err := os.Lstat(root)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			continue
		}
		walkForCargoTargets(ctx, root, roots, 0, 5, now, func(targetPath string, sz int64) {
			if !seen[targetPath] {
				seen[targetPath] = true
				stalePaths = append(stalePaths, targetPath)
				totalBytes += sz
			}
		})
	}

	if len(stalePaths) == 0 {
		return nil
	}
	sort.Strings(stalePaths)
	return &Candidate{
		ID:                "costly:cargo-targets",
		Tier:              TierCostly,
		Category:          "cargo",
		Title:             fmt.Sprintf("Stale Rust Cargo target/ directories (>30d, %d projects)", len(stalePaths)),
		Paths:             stalePaths,
		Bytes:             totalBytes,
		Warning:           "Rust projects will perform a full recompile on next cargo build.",
		SelectedByDefault: false,
	}
}

func walkForCargoTargets(ctx *app.Context, dir string, roots []string, depth, maxDepth int, now time.Time, onFound func(string, int64)) {
	if depth > maxDepth {
		return
	}
	if ctx.RepoRoot != "" && isWithinOrEqual(filepath.Clean(dir), filepath.Clean(ctx.RepoRoot)) {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	hasCargoToml := false
	hasTargetDir := false
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if entry.Name() == "Cargo.toml" && !entry.IsDir() {
			hasCargoToml = true
		}
		if entry.Name() == "target" && entry.IsDir() {
			hasTargetDir = true
		}
	}

	if hasCargoToml && hasTargetDir {
		targetPath := filepath.Join(dir, "target")
		if valid, err := ValidateTarget(ctx, targetPath, roots); err == nil {
			if hasCargoCachedirTag(valid) && isDirStale(valid, now, cargoStaleAge) {
				if sz, serr := SafeDirSize(valid, 10); serr == nil && sz > 0 {
					onFound(valid, sz)
				}
			}
		}
	}

	if depth == maxDepth {
		return
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "target" || name == ".git" || name == "node_modules" || strings.HasPrefix(name, ".") {
			continue
		}
		walkForCargoTargets(ctx, filepath.Join(dir, name), roots, depth+1, maxDepth, now, onFound)
	}
}

func hasCargoCachedirTag(targetDir string) bool {
	tag := filepath.Join(targetDir, "CACHEDIR.TAG")
	fi, err := os.Lstat(tag)
	return err == nil && fi.Mode()&os.ModeSymlink == 0 && fi.Mode().IsRegular()
}

func isDirStale(dir string, now time.Time, maxAge time.Duration) bool {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	newest := info.ModTime()
	if cargoLock := filepath.Join(filepath.Dir(dir), "Cargo.lock"); true {
		if fi, lerr := os.Lstat(cargoLock); lerr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	if dotLock := filepath.Join(dir, ".cargo-lock"); true {
		if fi, lerr := os.Lstat(dotLock); lerr == nil && fi.Mode()&os.ModeSymlink == 0 && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	walkNewestModTime(dir, 0, 6, &newest)
	return now.Sub(newest) > maxAge
}

func walkNewestModTime(dir string, depth, maxDepth int, newest *time.Time) {
	if depth > maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if fi, err := entry.Info(); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if fi.ModTime().After(*newest) {
				*newest = fi.ModTime()
			}
		}
		if entry.IsDir() && depth < maxDepth {
			walkNewestModTime(filepath.Join(dir, entry.Name()), depth+1, maxDepth, newest)
		}
	}
}

func scanSingleDirCandidate(ctx *app.Context, id string, tier Tier, category, title, rawPath, warning string, selectedByDefault bool) *Candidate {
	if ctx == nil || ctx.HomeDir == "" {
		return nil
	}
	valid, err := ValidateTarget(ctx, rawPath, AllowlistedRoots(ctx))
	if err != nil {
		return nil
	}
	sz, err := SafeDirSize(valid, 8)
	if err != nil || sz <= 0 {
		return nil
	}
	return &Candidate{
		ID:                id,
		Tier:              tier,
		Category:          category,
		Title:             title,
		Paths:             []string{valid},
		Bytes:             sz,
		Warning:           warning,
		SelectedByDefault: selectedByDefault,
	}
}

func scanDockerBuildx(runner CommandRunner) *Candidate {
	if !hasTool(runner, "docker") {
		return nil
	}
	stdout, _, err := runWithTimeout(runner, 8*time.Second, []string{"docker", "builder", "du"}, nil, "")
	if err != nil {
		return nil
	}
	sz := parseDockerBuilderDU(stdout)
	if sz <= 0 {
		return nil
	}
	return &Candidate{
		ID:                "costly:docker-buildx",
		Tier:              TierCostly,
		Category:          "containers",
		Title:             "Docker / OrbStack buildx cache",
		CleanerCmd:        []string{"docker", "builder", "prune", "-f"},
		Bytes:             sz,
		Warning:           "Container image builds will lose cached layers.",
		SelectedByDefault: false,
	}
}

func scanDockerVolumes(runner CommandRunner) *Candidate {
	if !hasTool(runner, "docker") {
		return nil
	}
	stdout, _, err := runWithTimeout(runner, 8*time.Second, []string{"docker", "system", "df", "--format", "{{.Type}}\t{{.Reclaimable}}"}, nil, "")
	if err != nil {
		return nil
	}
	var sz int64
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) < 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[0]), "Local Volumes") {
			sz = parseHumanBytes(parts[1])
			break
		}
	}
	if sz <= 0 {
		return nil
	}
	return &Candidate{
		ID:                "dangerous:docker-volumes",
		Tier:              TierDangerous,
		Category:          "containers",
		Title:             "Unused Docker / container volumes",
		CleanerCmd:        []string{"docker", "volume", "prune", "-f"},
		Bytes:             sz,
		Warning:           "Permanently destroys unused container volumes and database state.",
		SelectedByDefault: false,
	}
}

func runWithTimeout(runner CommandRunner, timeout time.Duration, cmd []string, env map[string]string, dir string) (string, string, error) {
	if tr, ok := runner.(TimeoutCommandRunner); ok {
		return tr.RunWithTimeout(timeout, cmd, env, dir)
	}
	return runner.Run(cmd, env, dir)
}

var reSizeToken = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(kib|mib|gib|tib|kb|mb|gb|tb|b)\b`)

func parseDockerBuilderDU(stdout string) int64 {
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "reclaimable:") {
			return parseHumanBytes(trimmed)
		}
	}
	return 0
}

func parseHumanBytes(s string) int64 {
	m := reSizeToken.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil || val <= 0 {
		return 0
	}
	switch strings.ToLower(m[2]) {
	case "b":
		return int64(val)
	case "kb":
		return int64(val * 1_000)
	case "kib":
		return int64(val * 1024)
	case "mb":
		return int64(val * 1_000_000)
	case "mib":
		return int64(val * 1024 * 1024)
	case "gb":
		return int64(val * 1_000_000_000)
	case "gib":
		return int64(val * 1024 * 1024 * 1024)
	case "tb":
		return int64(val * 1_000_000_000_000)
	case "tib":
		return int64(val * 1024 * 1024 * 1024 * 1024)
	default:
		return 0
	}
}

func tierOrder(t Tier) int {
	switch t {
	case TierSafe:
		return 1
	case TierCostly:
		return 2
	case TierDangerous:
		return 3
	default:
		return 4
	}
}

func hasTool(runner CommandRunner, bin string) bool {
	if runner == nil || bin == "" {
		return false
	}
	_, err := runner.LookPath(bin)
	return err == nil
}
