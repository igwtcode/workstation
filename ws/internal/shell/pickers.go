package shell

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/opcli"
)

type RepoEntry struct {
	Zone    string `json:"zone"`
	Path    string `json:"path"`
	Display string `json:"display"`
	Name    string `json:"name"`
}

type AWSProfile struct {
	Zone   string `json:"zone"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type RepoPullStatus string

const (
	PullUpdated    RepoPullStatus = "updated"
	PullUpToDate   RepoPullStatus = "up-to-date"
	PullDryRun     RepoPullStatus = "dry-run"
	PullDirty      RepoPullStatus = "dirty"
	PullNoUpstream RepoPullStatus = "no-upstream"
	PullFailed     RepoPullStatus = "failed"
)

type PullItem struct {
	Path   string         `json:"path"`
	Rel    string         `json:"rel"`
	Status RepoPullStatus `json:"status"`
	Error  string         `json:"error,omitempty"`
}

type PullSummary struct {
	ScanDir    string     `json:"scanDir"`
	Total      int        `json:"total"`
	Updated    int        `json:"updated"`
	UpToDate   int        `json:"upToDate"`
	DryRun     int        `json:"dryRun,omitempty"`
	Dirty      int        `json:"dirty"`
	NoUpstream int        `json:"noUpstream"`
	Failed     int        `json:"failed"`
	Items      []PullItem `json:"items"`
}

func DiscoverRepos(ctx *app.Context) ([]RepoEntry, error) {
	order, roots, _ := discoverZoneRoots(ctx)
	var out []RepoEntry
	seen := map[string]bool{}

	for _, zname := range order {
		zroot := roots[zname]
		codeRoot := filepath.Join(zroot, "code")
		if envMap, err := ReadZoneMiseEnv(zroot); err == nil && envMap["WS_CODE_ROOT"] != "" {
			codeRoot = envMap["WS_CODE_ROOT"]
		}
		repos, err := findGitRepos(codeRoot, 4)
		if err != nil {
			continue
		}
		for _, repoPath := range repos {
			if seen[repoPath] {
				continue
			}
			seen[repoPath] = true
			out = append(out, RepoEntry{
				Zone:    zname,
				Path:    repoPath,
				Display: ctx.CollapseHome(repoPath),
				Name:    filepath.Base(repoPath),
			})
		}
	}
	return out, nil
}

func DiscoverReposFast(ctx *app.Context) ([]RepoEntry, error) {
	if ctx == nil || ctx.HomeDir == "" {
		return nil, nil
	}
	order, roots, _ := discoverZoneRootsFast(ctx)
	zoneRepos := make([][]string, len(order))
	var wg sync.WaitGroup
	for i, zname := range order {
		zroot := roots[zname]
		codeRoot := filepath.Join(zroot, "code")
		if envMap, err := ReadZoneMiseEnvFast(zroot); err == nil && envMap["WS_CODE_ROOT"] != "" {
			codeRoot = envMap["WS_CODE_ROOT"]
		}
		wg.Add(1)
		go func(idx int, root string) {
			defer wg.Done()
			if repos, err := findGitReposFast(root, 4); err == nil {
				zoneRepos[idx] = repos
			}
		}(i, codeRoot)
	}
	wg.Wait()

	var out []RepoEntry
	seen := map[string]bool{}
	for i, zname := range order {
		for _, repoPath := range zoneRepos[i] {
			if seen[repoPath] {
				continue
			}
			seen[repoPath] = true
			out = append(out, RepoEntry{
				Zone:    zname,
				Path:    repoPath,
				Display: ctx.CollapseHome(repoPath),
				Name:    filepath.Base(repoPath),
			})
		}
	}
	return out, nil
}

func findGitRepos(root string, maxGitDepth int) ([]string, error) {
	return findGitReposWithOpts(root, maxGitDepth, false)
}

func findGitReposFast(root string, maxGitDepth int) ([]string, error) {
	return findGitReposWithOpts(root, maxGitDepth, true)
}

func shouldSkipRepoScanDir(name string, fast bool) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "target":
		return true
	case "vendor", ".venv", "venv", "dist", "build", ".next", ".cache", "Library", "Applications", "Movies", "Music", "Pictures":
		return fast
	}
	return false
}

func findGitReposWithOpts(root string, maxGitDepth int, fast bool) ([]string, error) {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, err
	}
	var repos []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth >= maxGitDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == ".git" {
				repos = append(repos, dir)
				return
			}
		}
		for _, e := range entries {
			if !e.IsDir() || (fast && e.Type()&os.ModeSymlink != 0) {
				continue
			}
			if shouldSkipRepoScanDir(e.Name(), fast) {
				continue
			}
			walk(filepath.Join(dir, e.Name()), depth+1)
		}
	}
	walk(filepath.Clean(root), 0)
	sort.Strings(repos)
	return repos, nil
}

func DiscoverAWSProfiles(ctx *app.Context) ([]AWSProfile, error) {
	order, roots, _ := discoverZoneRootsFast(ctx)
	var out []AWSProfile

	for _, zname := range order {
		zroot := roots[zname]
		envMap, err := ReadZoneMiseEnvFast(zroot)
		if err != nil {
			continue
		}
		seen := map[string]string{}
		if cfg := envMap["AWS_CONFIG_FILE"]; cfg != "" {
			for _, name := range parseAWSINI(cfg, true) {
				if _, ok := seen[name]; !ok {
					seen[name] = cfg
				}
			}
		}
		if cred := envMap["AWS_SHARED_CREDENTIALS_FILE"]; cred != "" {
			for _, name := range parseAWSINI(cred, false) {
				if _, ok := seen[name]; !ok {
					seen[name] = cred
				}
			}
		}
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, AWSProfile{
				Zone:   zname,
				Name:   n,
				Source: seen[n],
			})
		}
	}
	if len(order) == 0 && ctx != nil && ctx.HomeDir != "" {
		awsDir := filepath.Join(ctx.HomeDir, ".aws")
		seen := map[string]string{}
		for _, pair := range []struct {
			path     string
			isConfig bool
		}{
			{filepath.Join(awsDir, "config"), true},
			{filepath.Join(awsDir, "credentials"), false},
		} {
			for _, name := range parseAWSINI(pair.path, pair.isConfig) {
				if _, ok := seen[name]; !ok {
					seen[name] = pair.path
				}
			}
		}
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, AWSProfile{
				Zone:   "default",
				Name:   n,
				Source: seen[n],
			})
		}
	}
	return out, nil
}

func parseAWSINI(path string, isConfigFile bool) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var profiles []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		sec := strings.TrimSpace(line[1 : len(line)-1])
		if sec == "" {
			continue
		}
		if isConfigFile {
			if sec == "default" {
				profiles = append(profiles, "default")
			} else if strings.HasPrefix(sec, "profile ") {
				name := strings.TrimSpace(strings.TrimPrefix(sec, "profile "))
				if name != "" {
					profiles = append(profiles, name)
				}
			}
		} else {
			profiles = append(profiles, sec)
		}
	}
	return profiles
}

func PullRepos(ctx *app.Context, scanDir string) (*PullSummary, error) {
	if scanDir == "" && ctx != nil {
		scanDir = ctx.LookupEnv("WS_CODE_ROOT")
	}
	if scanDir == "" {
		return nil, fmt.Errorf("scan directory is required (or set WS_CODE_ROOT)")
	}
	if ctx != nil {
		scanDir = ctx.ExpandHome(scanDir)
	}
	scanDir = filepath.Clean(scanDir)
	fi, err := os.Stat(scanDir)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("scan directory %q does not exist", scanDir)
	}

	repos, err := findGitRepos(scanDir, 4)
	if err != nil {
		return nil, err
	}

	dryRun := ctx != nil && ctx.DryRun
	items := make([]PullItem, len(repos))

	workers := 8
	if len(repos) < workers {
		workers = len(repos)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int, len(repos))
	for i := range repos {
		jobs <- i
	}
	close(jobs)

	zoneEnvOverlay := []string{"GIT_TERMINAL_PROMPT=0"}
	if ctx != nil {
		if zname := zoneForPath(ctx, scanDir); zname != "" {
			if zenv, zerr := ResolveZoneEnv(ctx, zname); zerr == nil {
				zoneEnvOverlay = append(zenv, "GIT_TERMINAL_PROMPT=0")
			}
		}
	}
	cmdEnv := ReplaceZoneEnvForContext(ctx, os.Environ(), zoneEnvOverlay)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				repo := repos[idx]
				rel, err := filepath.Rel(scanDir, repo)
				if err != nil {
					rel = repo
				}
				items[idx] = pullOneRepo(repo, filepath.ToSlash(rel), dryRun, cmdEnv)
			}
		}()
	}
	wg.Wait()

	summary := &PullSummary{
		ScanDir: scanDir,
		Total:   len(items),
		Items:   items,
	}
	for _, it := range items {
		switch it.Status {
		case PullUpdated:
			summary.Updated++
		case PullUpToDate:
			summary.UpToDate++
		case PullDryRun:
			summary.DryRun++
		case PullDirty:
			summary.Dirty++
		case PullNoUpstream:
			summary.NoUpstream++
		case PullFailed:
			summary.Failed++
		}
	}
	return summary, nil
}

func gitCmdWithEnv(repo string, baseEnv []string, args ...string) *exec.Cmd {
	full := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = baseEnv
	return cmd
}

func pullOneRepo(repo, rel string, dryRun bool, cmdEnv []string) PullItem {
	item := PullItem{Path: repo, Rel: rel}

	statusOut, err := gitCmdWithEnv(repo, cmdEnv, "status", "--porcelain").Output()
	if err != nil {
		item.Status = PullFailed
		item.Error = err.Error()
		return item
	}
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		item.Status = PullDirty
		return item
	}

	if err := gitCmdWithEnv(repo, cmdEnv, "rev-parse", "--abbrev-ref", "@{upstream}").Run(); err != nil {
		item.Status = PullNoUpstream
		return item
	}

	if dryRun {
		item.Status = PullDryRun
		return item
	}

	beforeBytes, err := gitCmdWithEnv(repo, cmdEnv, "rev-parse", "HEAD").Output()
	if err != nil {
		item.Status = PullFailed
		item.Error = err.Error()
		return item
	}
	before := strings.TrimSpace(string(beforeBytes))

	if out, err := gitCmdWithEnv(repo, cmdEnv, "pull", "--ff-only", "--quiet").CombinedOutput(); err != nil {
		item.Status = PullFailed
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		item.Error = msg
		return item
	}

	afterBytes, err := gitCmdWithEnv(repo, cmdEnv, "rev-parse", "HEAD").Output()
	if err != nil {
		item.Status = PullFailed
		item.Error = err.Error()
		return item
	}
	if before == strings.TrimSpace(string(afterBytes)) {
		item.Status = PullUpToDate
	} else {
		item.Status = PullUpdated
	}
	return item
}

func GenSecret(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "hex":
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return hex.EncodeToString(buf), nil
	case "base64":
		buf := make([]byte, 60)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(buf), nil
	case "uuid":
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		buf[6] = (buf[6] & 0x0f) | 0x40
		buf[8] = (buf[8] & 0x3f) | 0x80
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
	default:
		return "", fmt.Errorf("unsupported secret kind %q (expected hex, base64, or uuid)", kind)
	}
}

type VaultOptions struct {
	DocTitle    string
	All         bool
	SkipMissing bool
	WarnOut     io.Writer
}

func VaultOpWithOptions(ctx *app.Context, mode, scope string, args []string, opts VaultOptions) error {
	warnOut := opts.WarnOut
	if warnOut == nil {
		warnOut = os.Stderr
	}
	if opts.All && strings.EqualFold(strings.TrimSpace(scope), "envfiles") && len(args) == 0 {
		if ctx == nil {
			return fmt.Errorf("context is required")
		}
		if ctx.DryRun {
			return fmt.Errorf("vault operation %s/%s refused in dry-run mode", scope, mode)
		}
		order, roots, _ := discoverZoneRoots(ctx)
		if len(order) == 0 {
			return fmt.Errorf("no zones configured for envfiles --all")
		}
		for _, zname := range order {
			zroot := roots[zname]
			codeRoot := filepath.Join(zroot, "code")
			if envMap, err := ReadZoneMiseEnv(zroot); err == nil && envMap["WS_CODE_ROOT"] != "" {
				codeRoot = envMap["WS_CODE_ROOT"]
			}
			if _, statErr := os.Stat(codeRoot); statErr != nil {
				continue
			}
			if opts.DocTitle != "" {
				subOpts := opts
				subOpts.All = false
				subOpts.DocTitle = ""
				if err := vaultOpExec(ctx, mode, scope, []string{codeRoot}, subOpts, warnOut); err != nil {
					return err
				}
			} else {
				if err := vaultOpExec(ctx, mode, scope, []string{codeRoot}, VaultOptions{WarnOut: warnOut}, warnOut); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return vaultOpExec(ctx, mode, scope, args, opts, warnOut)
}

func vaultOpExec(ctx *app.Context, mode, scope string, args []string, opts VaultOptions, warnOut io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if ctx.DryRun {
		return fmt.Errorf("vault operation %s/%s refused in dry-run mode", scope, mode)
	}

	normScope := strings.ToLower(strings.TrimSpace(scope))
	switch normScope {
	case "secrets", "ssh", "files", "envfiles":
	default:
		return fmt.Errorf("unsupported vault scope %q (expected secrets, ssh, files, or envfiles)", scope)
	}

	normMode := strings.ToLower(strings.TrimSpace(mode))
	switch normMode {
	case "load", "pull":
		normMode = "load"
	case "save", "push":
		normMode = "save"
	default:
		return fmt.Errorf("unsupported vault mode %q (expected load/pull or save/push)", mode)
	}

	if normScope == "files" {
		targets := resolveVaultFileTargets(ctx, args)
		homeClean := filepath.Clean(ctx.HomeDir)
		for _, rel := range targets {
			if !strings.HasPrefix(rel, "~/") {
				return fmt.Errorf("vault file path must start with ~/ (got %q)", rel)
			}
			abs := ctx.ExpandHome(rel)
			if !isPathWithin(abs, homeClean) || filepath.Clean(abs) == homeClean {
				return fmt.Errorf("vault file path %q escapes home directory", rel)
			}
			if normMode == "load" && !ctx.Allow.Replace && pathExists(abs) {
				return fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", rel)
			}
		}
	}
	if normMode == "load" && !ctx.Allow.Replace {
		switch normScope {
		case "secrets":
			if p := filepath.Join(ctx.RepoRoot, "mise.local.toml"); pathExists(p) {
				return fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", ctx.CollapseHome(p))
			}
		case "ssh":
			if sshDir := filepath.Join(ctx.HomeDir, ".ssh"); pathExists(sshDir) {
				return fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", ctx.CollapseHome(sshDir))
			}
		case "envfiles":
			tree := resolveVaultEnvTree(ctx, args)
			if tree != "" {
				if existing := findFirstEnvFile(tree); existing != "" {
					return fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", ctx.CollapseHome(existing))
				}
			}
		}
	}

	opBin, err := opcli.RequireSignedIn(context.Background())
	if err != nil {
		return err
	}

	vault := strings.TrimSpace(ctx.LookupEnv("WS_OP_VAULT"))
	if vault == "" && warnOut != nil {
		fmt.Fprintln(warnOut, "warning: WS_OP_VAULT is unset; searching across all 1Password vaults (set WS_OP_VAULT to pin a vault)")
	}
	tmpDir, err := os.MkdirTemp("", "ws-op-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	switch normScope {
	case "secrets":
		docTitle := strings.TrimSpace(opts.DocTitle)
		if docTitle == "" {
			docTitle = ctx.LookupEnv("WS_OP_SECRETS_DOC")
		}
		if docTitle == "" {
			docTitle = "workstation-secrets.zip"
		}
		zipPath := filepath.Join(tmpDir, "secrets.zip")
		if normMode == "save" {
			var relPaths []string
			if _, err := os.Stat(filepath.Join(ctx.RepoRoot, "mise.local.toml")); err == nil {
				relPaths = append(relPaths, "mise.local.toml")
			}
			if fi, err := os.Stat(filepath.Join(ctx.RepoRoot, ".mise", "tasks", "local")); err == nil && fi.IsDir() {
				relPaths = append(relPaths, filepath.Join(".mise", "tasks", "local"))
			}
			if len(relPaths) == 0 {
				return fmt.Errorf("nothing to back up in %s", ctx.RepoRoot)
			}
			if err := zipRelativePaths(ctx.RepoRoot, relPaths, zipPath); err != nil {
				return err
			}
			return opcli.PutDocument(context.Background(), opBin, vault, docTitle, zipPath)
		}
		if err := opcli.GetDocument(context.Background(), opBin, vault, docTitle, zipPath); err != nil {
			return err
		}
		if err := unzipArchiveWithPolicy(zipPath, ctx.RepoRoot, ctx.Allow.Replace); err != nil {
			return err
		}
		if p := filepath.Join(ctx.RepoRoot, "mise.local.toml"); fileExists(p) {
			_ = os.Chmod(p, 0o600)
		}
		return nil

	case "ssh":
		docTitle := strings.TrimSpace(opts.DocTitle)
		if docTitle == "" {
			docTitle = ctx.LookupEnv("WS_OP_SSH_DOC")
		}
		if docTitle == "" {
			docTitle = "~/.ssh.zip"
		}
		zipPath := filepath.Join(tmpDir, "ssh.zip")
		sshDir := filepath.Join(ctx.HomeDir, ".ssh")
		if normMode == "save" {
			if fi, err := os.Stat(sshDir); err != nil || !fi.IsDir() {
				return fmt.Errorf("no .ssh directory under %s", ctx.HomeDir)
			}
			if err := zipRelativePaths(ctx.HomeDir, []string{".ssh"}, zipPath); err != nil {
				return err
			}
			return opcli.PutDocument(context.Background(), opBin, vault, docTitle, zipPath)
		}
		if err := opcli.GetDocument(context.Background(), opBin, vault, docTitle, zipPath); err != nil {
			if opts.SkipMissing && errors.Is(err, opcli.ErrDocumentNotFound) {
				if warnOut != nil {
					fmt.Fprintf(warnOut, "warning: skip ~/.ssh — no 1Password document %q\n", docTitle)
				}
				return nil
			}
			return err
		}
		backups, err := unzipArchiveWithBackup(zipPath, ctx.HomeDir, ctx.Allow.Replace, true)
		if warnOut != nil {
			for _, b := range backups {
				fmt.Fprintf(warnOut, "backed up %s -> %s\n", ctx.CollapseHome(b.Original), ctx.CollapseHome(b.Backup))
			}
		}
		if err != nil {
			return err
		}
		_ = os.Chmod(sshDir, 0o700)
		return nil

	case "files":
		targets := resolveVaultFileTargets(ctx, args)
		if len(targets) == 0 {
			return fmt.Errorf("no paths provided and WS_OP_FILES is empty")
		}
		for _, rel := range targets {
			abs := ctx.ExpandHome(rel)
			if normMode == "save" {
				fi, err := os.Lstat(abs)
				if err != nil {
					if warnOut != nil {
						fmt.Fprintf(warnOut, "warning: skipping missing path %s\n", rel)
					}
					continue
				}
				if fi.Mode()&os.ModeSymlink != 0 {
					if warnOut != nil {
						fmt.Fprintf(warnOut, "warning: skipping symlink path %s\n", rel)
					}
					continue
				}
				if fi.IsDir() {
					zipPath := filepath.Join(tmpDir, "payload.zip")
					if err := zipRelativePaths(filepath.Dir(abs), []string{filepath.Base(abs)}, zipPath); err != nil {
						return err
					}
					if err := opcli.PutDocument(context.Background(), opBin, vault, rel+".zip", zipPath); err != nil {
						return err
					}
					_ = os.Remove(zipPath)
				} else {
					if err := opcli.PutDocument(context.Background(), opBin, vault, rel, abs); err != nil {
						return err
					}
				}
			} else {
				if !ctx.Allow.Replace && pathExists(abs) {
					return fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", rel)
				}
				title := rel
				zipID, idErr := opcli.FindDocument(context.Background(), opBin, vault, rel+".zip")
				if idErr != nil {
					return idErr
				}
				if zipID != "" {
					title = rel + ".zip"
				}
				payload := filepath.Join(tmpDir, "payload")
				if err := opcli.GetDocument(context.Background(), opBin, vault, title, payload); err != nil {
					if opts.SkipMissing && errors.Is(err, opcli.ErrDocumentNotFound) {
						if warnOut != nil {
							fmt.Fprintf(warnOut, "warning: skip %s — no 1Password document %q\n", rel, title)
						}
						_ = os.Remove(payload)
						continue
					}
					return err
				}
				if strings.HasSuffix(title, ".zip") {
					if err := unzipArchiveWithPolicy(payload, filepath.Dir(abs), ctx.Allow.Replace); err != nil {
						return err
					}
				} else {
					raw, err := os.ReadFile(payload)
					if err != nil {
						return err
					}
					if err := app.WriteAtomicFile(abs, raw, 0o600); err != nil {
						return err
					}
				}
				_ = os.Remove(payload)
			}
		}
		return nil

	case "envfiles":
		tree := resolveVaultEnvTree(ctx, args)
		if tree == "" {
			return fmt.Errorf("code root directory is required for envfiles")
		}
		docTitle := strings.TrimSpace(opts.DocTitle)
		if docTitle == "" {
			docTitle = ctx.LookupEnv("WS_OP_ENVFILES_DOC")
		}
		if docTitle == "" {
			zone := zoneForPath(ctx, tree)
			if zone == "" {
				zone = os.Getenv("WS_ZONE")
			}
			if zone != "" {
				docTitle = "workstation-envfiles-" + zone + ".zip"
			} else {
				docTitle = "workstation-envfiles.zip"
			}
		}
		zipPath := filepath.Join(tmpDir, "envfiles.zip")
		if normMode == "save" {
			var envFiles []string
			err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if d.IsDir() {
					if d.Name() == "node_modules" || d.Name() == ".git" {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Name() == ".env" || strings.HasPrefix(d.Name(), ".env.") {
					if rel, err := filepath.Rel(tree, p); err == nil {
						envFiles = append(envFiles, rel)
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			if len(envFiles) == 0 {
				return fmt.Errorf("no .env files found under %s", tree)
			}
			if err := zipRelativePaths(tree, envFiles, zipPath); err != nil {
				return err
			}
			return opcli.PutDocument(context.Background(), opBin, vault, docTitle, zipPath)
		}
		if err := opcli.GetDocument(context.Background(), opBin, vault, docTitle, zipPath); err != nil {
			return err
		}
		return unzipArchiveWithPolicy(zipPath, tree, ctx.Allow.Replace)
	}
	return nil
}

func resolveVaultFileTargets(ctx *app.Context, args []string) []string {
	if len(args) > 0 {
		return args
	}
	var targets []string
	for _, row := range app.ParsePipeLines(ctx.LookupEnv("WS_OP_FILES")) {
		if len(row) > 0 && row[0] != "" {
			targets = append(targets, row[0])
		}
	}
	return targets
}

func resolveVaultEnvTree(ctx *app.Context, args []string) string {
	tree := ""
	if len(args) > 0 {
		tree = args[0]
	} else {
		tree = ctx.LookupEnv("WS_CODE_ROOT")
	}
	if strings.TrimSpace(tree) == "" {
		return ""
	}
	return filepath.Clean(ctx.ExpandHome(tree))
}

func findFirstEnvFile(tree string) string {
	found := ""
	_ = filepath.WalkDir(tree, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".env" || strings.HasPrefix(d.Name(), ".env.") {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func zipRelativePaths(baseDir string, relPaths []string, destZip string) error {
	outFile, err := os.Create(destZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(outFile)

	for _, rel := range relPaths {
		full := filepath.Join(baseDir, rel)
		err := filepath.WalkDir(full, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "agent" && filepath.Base(filepath.Dir(p)) == ".ssh" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == ".DS_Store" || d.Type()&os.ModeSocket != 0 {
				return nil
			}
			arcRel, err := filepath.Rel(baseDir, p)
			if err != nil {
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			hdr, err := zip.FileInfoHeader(fi)
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(arcRel)
			hdr.Method = zip.Deflate
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(w, f)
			return err
		})
		if err != nil {
			zw.Close()
			outFile.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		outFile.Close()
		return err
	}
	return outFile.Close()
}

const (
	maxArchiveEntryBytes int64 = 64 << 20
	maxArchiveTotalBytes int64 = 256 << 20
)

func checkNoSymlinkParentsUnder(target, destRoot string) error {
	cleanDest := filepath.Clean(destRoot)
	cur := filepath.Dir(filepath.Clean(target))
	for isPathWithin(cur, cleanDest) && cur != cleanDest {
		fi, err := os.Lstat(cur)
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink parent directory %s inside archive destination", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return nil
}

type vaultBackupRecord struct {
	Original string
	Backup   string
}

func uniqueVaultBackupPath(target string, now time.Time) string {
	stamp := now.UTC().Format("20060102-150405")
	base := fmt.Sprintf("%s.%s.pre-ws-vault", target, stamp)
	if !pathExists(base) {
		return base
	}
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s.%s-%d.pre-ws-vault", target, stamp, i)
		if !pathExists(cand) {
			return cand
		}
	}
	return fmt.Sprintf("%s.%d.pre-ws-vault", target, now.UTC().UnixNano())
}

func unzipArchiveWithPolicy(zipPath, destDir string, allowReplace bool) error {
	_, err := unzipArchiveWithBackup(zipPath, destDir, allowReplace, false)
	return err
}

func unzipArchiveWithBackup(zipPath, destDir string, allowReplace, backupReplaced bool) ([]vaultBackupRecord, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	cleanDest := filepath.Clean(destDir)
	for _, f := range zr.File {
		target := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		if !isPathWithin(target, cleanDest) {
			return nil, fmt.Errorf("archive entry %q escapes destination", f.Name)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink archive entry %q", f.Name)
		}
		if err := checkNoSymlinkParentsUnder(target, cleanDest); err != nil {
			return nil, err
		}
		if !f.FileInfo().IsDir() {
			if fi, lerr := os.Lstat(target); lerr == nil {
				if fi.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("refusing to overwrite symlink %s", target)
				}
				if fi.IsDir() {
					return nil, fmt.Errorf("refusing to overwrite directory %s with file", target)
				}
				if !allowReplace {
					return nil, fmt.Errorf("refusing to overwrite existing %s: requires --allow replace", target)
				}
			}
		}
	}

	now := time.Now().UTC()
	var backups []vaultBackupRecord
	var totalBytes int64
	for _, f := range zr.File {
		target := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		inSSH := isArchivePathInSSH(f.Name)
		if f.FileInfo().IsDir() {
			dirPerm := os.FileMode(0o755)
			if inSSH || filepath.Base(target) == ".ssh" {
				dirPerm = 0o700
			}
			if err := os.MkdirAll(target, dirPerm); err != nil {
				return backups, err
			}
			if dirPerm == 0o700 {
				_ = os.Chmod(target, 0o700)
			}
			continue
		}
		parentPerm := os.FileMode(0o755)
		if inSSH {
			parentPerm = 0o700
		}
		if err := os.MkdirAll(filepath.Dir(target), parentPerm); err != nil {
			return backups, err
		}
		if inSSH {
			_ = os.Chmod(filepath.Dir(target), 0o700)
		}
		rc, err := f.Open()
		if err != nil {
			return backups, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxArchiveEntryBytes+1))
		rc.Close()
		if err != nil {
			return backups, err
		}
		if int64(len(data)) > maxArchiveEntryBytes {
			return backups, fmt.Errorf("archive entry %q exceeds %d MiB limit", f.Name, maxArchiveEntryBytes/(1<<20))
		}
		totalBytes += int64(len(data))
		if totalBytes > maxArchiveTotalBytes {
			return backups, fmt.Errorf("archive total uncompressed size exceeds %d MiB limit", maxArchiveTotalBytes/(1<<20))
		}
		if backupReplaced {
			if fi, lerr := os.Lstat(target); lerr == nil && fi.Mode().IsRegular() {
				origBytes, rerr := os.ReadFile(target)
				if rerr != nil {
					return backups, fmt.Errorf("read existing %s for backup: %w", target, rerr)
				}
				backupPath := uniqueVaultBackupPath(target, now)
				if berr := app.WriteAtomicFile(backupPath, origBytes, 0o600); berr != nil {
					return backups, fmt.Errorf("write backup %s: %w", backupPath, berr)
				}
				backups = append(backups, vaultBackupRecord{
					Original: target,
					Backup:   backupPath,
				})
			}
		}
		mode := resolveUnzipMode(f.Name, f.Mode())
		if err := app.WriteAtomicFile(target, data, mode); err != nil {
			return backups, err
		}
	}
	return backups, nil
}

func isArchivePathInSSH(arcName string) bool {
	norm := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(arcName)), "./")
	parts := strings.Split(norm, "/")
	if len(parts) < 2 {
		return false
	}
	for _, seg := range parts[:len(parts)-1] {
		if seg == ".ssh" {
			return true
		}
	}
	return false
}

func resolveUnzipMode(arcName string, rawMode fs.FileMode) os.FileMode {
	mode := rawMode.Perm()
	if mode == 0 || mode == 0o666 {
		mode = 0o600
	}
	mode &^= 0o022

	norm := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(arcName)), "./")
	base := filepath.Base(norm)
	if isArchivePathInSSH(norm) {
		if strings.HasSuffix(base, ".pub") {
			return 0o644
		}
		return 0o600
	}
	if strings.HasPrefix(norm, ".mise/tasks/local/") || strings.Contains(norm, "/.mise/tasks/local/") {
		return 0o755
	}
	return mode
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
