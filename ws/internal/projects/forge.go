package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/shell"
)

type CloneRequest struct {
	Source   string `json:"source"`
	Zone     string `json:"zone"`
	DestName string `json:"destName,omitempty"`
	NoZones  bool   `json:"noZones,omitempty"`
}

type ClonePlan struct {
	Forge         string   `json:"forge"`
	RemoteURL     string   `json:"remoteUrl"`
	Zone          string   `json:"zone"`
	DestDir       string   `json:"destDir"`
	GitUserName   string   `json:"gitUserName,omitempty"`
	GitUserEmail  string   `json:"gitUserEmail,omitempty"`
	GitSigningKey string   `json:"gitSigningKey,omitempty"`
	AWSProfile    string   `json:"awsProfile,omitempty"`
	NoZones       bool     `json:"noZones,omitempty"`
	Command       []string `json:"command"`
	UseGH         bool     `json:"useGh"`
}

type BatchPullResult struct {
	Updated          []string         `json:"updated,omitempty"`
	UpToDate         []string         `json:"upToDate,omitempty"`
	Dirty            []string         `json:"dirty,omitempty"`
	NoUpstream       []string         `json:"noUpstream,omitempty"`
	Failed           []string         `json:"failed,omitempty"`
	NeedsInteractive []RepoRecord     `json:"needsInteractive,omitempty"`
	Items            []shell.PullItem `json:"items,omitempty"`
}

var (
	ghShorthandRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9._-]{1,100}$`)
	safeZoneRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	safeDestRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func DetectForge(remoteURL string) string {
	u := strings.ToLower(strings.TrimSpace(remoteURL))
	if u == "" {
		return "other"
	}
	if strings.HasPrefix(u, "codecommit://") || strings.HasPrefix(u, "codecommit::") {
		return "codecommit"
	}
	if strings.Contains(u, "git-codecommit.") || strings.Contains(u, "amazonaws.com/v1/repos/") {
		return "codecommit"
	}

	host := extractRemoteHost(u)
	if host != "" {
		if strings.Contains(host, "codecommit") {
			return "codecommit"
		}
		if host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasPrefix(host, "github.com-") || strings.HasPrefix(host, "github-") {
			return "github"
		}
		if host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com") || strings.HasPrefix(host, "gitlab.com-") || strings.Contains(host, "gitlab.") {
			return "gitlab"
		}
		return "other"
	}

	if isGitHubShorthand(strings.TrimSpace(remoteURL)) {
		return "github"
	}
	return "other"
}

func extractRemoteHost(lowerURL string) string {
	if idx := strings.Index(lowerURL, "://"); idx >= 0 {
		rest := lowerURL[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			rest = rest[:slash]
		}
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			rest = rest[at+1:]
		}
		if colon := strings.IndexByte(rest, ':'); colon >= 0 {
			rest = rest[:colon]
		}
		return rest
	}
	if !strings.HasPrefix(lowerURL, "/") && !strings.HasPrefix(lowerURL, ".") {
		if colon := strings.IndexByte(lowerURL, ':'); colon > 0 {
			hostPart := lowerURL[:colon]
			if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
				hostPart = hostPart[at+1:]
			}
			return hostPart
		}
	}
	return ""
}

func ExtractGitHubRepoSlug(remoteURL string) string {
	trimmed := strings.TrimSpace(remoteURL)
	if trimmed == "" {
		return ""
	}
	if isGitHubShorthand(trimmed) {
		slug := strings.TrimSuffix(trimmed, ".git")
		if isGitHubShorthand(slug) {
			return slug
		}
		return ""
	}
	if DetectForge(trimmed) != "github" {
		return ""
	}

	var pathPart string
	if idx := strings.Index(trimmed, "://"); idx >= 0 {
		rest := trimmed[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			pathPart = rest[slash+1:]
		}
	} else if colon := strings.IndexByte(trimmed, ':'); colon >= 0 {
		pathPart = trimmed[colon+1:]
	}
	pathPart = strings.Trim(pathPart, "/")
	pathPart = strings.TrimSuffix(pathPart, ".git")
	if isGitHubShorthand(pathPart) {
		return pathPart
	}
	return ""
}

func isGitHubShorthand(src string) bool {
	if strings.Contains(src, "://") || strings.Contains(src, ":") || strings.Contains(src, "..") {
		return false
	}
	if !ghShorthandRe.MatchString(src) {
		return false
	}
	owner, repo, ok := strings.Cut(src, "/")
	if !ok || owner == "." || owner == ".." || repo == "." || repo == ".." {
		return false
	}
	return true
}

func PlanClone(ctx *app.Context, req CloneRequest) (*ClonePlan, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}

	rawSrc := req.Source
	if strings.ContainsAny(rawSrc, "\n\r\x00") {
		return nil, fmt.Errorf("invalid clone source: contains control characters")
	}
	src := strings.TrimSpace(rawSrc)
	if src == "" {
		return nil, fmt.Errorf("clone source is required")
	}
	if strings.HasPrefix(src, "-") {
		return nil, fmt.Errorf("invalid clone source %q: cannot start with '-'", src)
	}
	if strings.ContainsAny(src, " \t") {
		return nil, fmt.Errorf("invalid clone source %q: whitespace is not allowed", src)
	}

	zone := strings.TrimSpace(req.Zone)
	if zone != "" {
		if !safeZoneRe.MatchString(zone) || strings.Contains(zone, "..") {
			return nil, fmt.Errorf("invalid zone name %q", zone)
		}
	}

	destName := strings.TrimSpace(req.DestName)
	if destName == "" {
		destName = deriveRepoName(src)
	}
	if err := validateDestName(destName); err != nil {
		return nil, err
	}

	var codeRoot string
	var zoneSpec shell.ZoneSpec
	if zone != "" {
		if specs, err := shell.ParseZones(ctx); err == nil {
			for _, s := range specs {
				if s.Name == zone {
					zoneSpec = s
					break
				}
			}
		}
	}

	if req.NoZones {
		codeRoot = filepath.Join(ctx.HomeDir, "code")
	} else if zone != "" {
		zroot := filepath.Join(ctx.HomeDir, zone)
		codeRoot = filepath.Join(zroot, "code")
		if envMap, err := shell.ReadZoneMiseEnv(zroot); err == nil && envMap["WS_CODE_ROOT"] != "" {
			codeRoot = ctx.ExpandHome(envMap["WS_CODE_ROOT"])
		}
	} else {
		codeRoot = filepath.Join(ctx.HomeDir, "code")
		if cr := strings.TrimSpace(ctx.LookupEnv("WS_CODE_ROOT")); cr != "" {
			cleanCR := filepath.Clean(ctx.ExpandHome(cr))
			fromLocalEnv := ctx.LocalEnv != nil && strings.TrimSpace(ctx.LocalEnv["WS_CODE_ROOT"]) != ""
			realHome, _ := os.UserHomeDir()
			cleanHome := filepath.Clean(ctx.HomeDir)
			sameAsRealHome := ctx.HomeDir != "" && realHome != "" && cleanHome == filepath.Clean(realHome)
			withinCtxHome := ctx.HomeDir != "" && (cleanCR == cleanHome || strings.HasPrefix(cleanCR, cleanHome+string(filepath.Separator)))
			if fromLocalEnv || sameAsRealHome || withinCtxHome {
				codeRoot = cleanCR
			}
		}
	}

	cleanRoot := filepath.Clean(codeRoot)
	destDir := filepath.Clean(filepath.Join(cleanRoot, destName))
	if !strings.HasPrefix(destDir, cleanRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("clone destination %q escapes code root %q", destName, cleanRoot)
	}

	forge := DetectForge(src)
	useGH := isGitHubShorthand(src)
	remoteURL := StripURLCredentials(src)
	var cmd []string
	if useGH {
		forge = "github"
		remoteURL = "https://github.com/" + src + ".git"
		cmd = []string{"gh", "repo", "clone", src, destDir}
	} else {
		cmd = []string{"git", "clone", "--", src, destDir}
	}

	return &ClonePlan{
		Forge:         forge,
		RemoteURL:     remoteURL,
		Zone:          zone,
		DestDir:       destDir,
		GitUserName:   zoneSpec.GitName,
		GitUserEmail:  zoneSpec.GitEmail,
		GitSigningKey: zoneSpec.SigningKey,
		AWSProfile:    zoneSpec.AWSProfile,
		NoZones:       req.NoZones,
		Command:       cmd,
		UseGH:         useGH,
	}, nil
}

func deriveRepoName(src string) string {
	trimmed := strings.TrimRight(src, "/")
	if strings.Contains(trimmed, "..") {
		return ".."
	}
	if idx := strings.LastIndexAny(trimmed, "/:"); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}

	if at := strings.LastIndexByte(trimmed, '@'); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	trimmed = strings.TrimSuffix(trimmed, ".git")
	return trimmed
}

func validateDestName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid clone directory name %q", name)
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`+"\n\r\x00") {
		return fmt.Errorf("unsafe clone directory name %q", name)
	}
	if !safeDestRe.MatchString(name) {
		return fmt.Errorf("invalid clone directory name %q", name)
	}
	return nil
}

func ExecClone(ctx *app.Context, plan *ClonePlan, runner CommandRunner) error {
	if plan == nil || len(plan.Command) == 0 {
		return fmt.Errorf("clone plan is required")
	}
	if ctx != nil && ctx.DryRun {
		return nil
	}
	if runner == nil {
		runner = execCommandRunner{}
	}

	env := os.Environ()
	if plan.Zone != "" && !plan.NoZones && ctx != nil {
		zoneEnv, err := shell.ResolveZoneEnv(ctx, plan.Zone)
		if err != nil {
			return fmt.Errorf("resolve zone %q env: %w", plan.Zone, err)
		}
		env = shell.ReplaceZoneEnv(env, zoneEnv)
		env = stripEnvKey(stripEnvKey(env, "GH_TOKEN"), "GITHUB_TOKEN")
	}
	if plan.AWSProfile != "" {
		env = shell.MergeEnv(env, []string{"AWS_PROFILE=" + plan.AWSProfile})
	}

	if err := os.MkdirAll(filepath.Dir(plan.DestDir), 0o755); err != nil {
		return fmt.Errorf("create clone parent dir: %w", err)
	}

	if out, err := runner.Output(env, "", plan.Command[0], plan.Command[1:]...); err != nil {
		return formatCmdErr(plan.Command[0], out, err)
	}

	needLocalGitIdentity := plan.NoZones || plan.Zone == ""
	if !needLocalGitIdentity && ctx != nil && plan.Zone != "" {
		if _, err := os.Stat(filepath.Join(ctx.HomeDir, plan.Zone, ".gitconfig")); errors.Is(err, os.ErrNotExist) {
			needLocalGitIdentity = true
		}
	}

	if needLocalGitIdentity {
		configs := [][2]string{
			{"user.name", plan.GitUserName},
			{"user.email", plan.GitUserEmail},
			{"user.signingkey", plan.GitSigningKey},
		}
		if plan.GitSigningKey != "" {
			configs = append(configs,
				[2]string{"gpg.format", "ssh"},
				[2]string{"commit.gpgsign", "true"},
				[2]string{"tag.gpgsign", "true"},
			)
		}
		for _, kv := range configs {
			if kv[1] == "" {
				continue
			}
			if out, err := runner.Output(env, plan.DestDir, "git", "config", "--local", kv[0], kv[1]); err != nil {
				return formatCmdErr("git config", out, err)
			}
		}
	}
	return nil
}

func BatchPull(ctx *app.Context, repos []RepoRecord) (*BatchPullResult, error) {
	res := &BatchPullResult{}
	var autoRepos []RepoRecord

	for _, r := range repos {
		forge := r.Forge
		if forge == "" {
			forge = DetectForge(r.RemoteURL)
		}
		if forge == "codecommit" {
			res.NeedsInteractive = append(res.NeedsInteractive, r)
			continue
		}
		autoRepos = append(autoRepos, r)
	}

	if len(autoRepos) == 0 {
		return res, nil
	}

	items := make([]shell.PullItem, len(autoRepos))
	workers := maxScanWorkers
	if len(autoRepos) < workers {
		workers = len(autoRepos)
	}

	jobs := make(chan int, len(autoRepos))
	for i := range autoRepos {
		jobs <- i
	}
	close(jobs)

	dryRun := ctx != nil && ctx.DryRun

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				items[idx] = pullRepoNonInteractive(ctx, autoRepos[idx], dryRun)
			}
		}()
	}
	wg.Wait()

	res.Items = items
	for _, it := range items {
		label := it.Rel
		if label == "" {
			label = it.Path
		}
		switch it.Status {
		case shell.PullUpdated:
			res.Updated = append(res.Updated, label)
		case shell.PullUpToDate:
			res.UpToDate = append(res.UpToDate, label)
		case shell.PullDryRun:

		case shell.PullDirty:
			res.Dirty = append(res.Dirty, label)
		case shell.PullNoUpstream:
			res.NoUpstream = append(res.NoUpstream, label)
		case shell.PullFailed:
			if it.Error != "" {
				res.Failed = append(res.Failed, label+": "+it.Error)
			} else {
				res.Failed = append(res.Failed, label)
			}
		}
	}

	return res, nil
}

func pullRepoNonInteractive(ctx *app.Context, repo RepoRecord, dryRun bool) shell.PullItem {
	rel := repo.LocalPath
	if ctx != nil {
		rel = ctx.CollapseHome(repo.LocalPath)
	}
	item := shell.PullItem{
		Path: repo.LocalPath,
		Rel:  rel,
	}

	env := os.Environ()
	if ctx != nil && repo.Zone != "" {
		if zoneEnv, err := shell.ResolveZoneEnv(ctx, repo.Zone); err == nil {
			env = shell.ReplaceZoneEnv(env, zoneEnv)
		}
	}
	extraEnv := []string{"GIT_TERMINAL_PROMPT=0"}
	if !hasEnvKey(env, "GIT_SSH_COMMAND") {
		extraEnv = append(extraEnv, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	env = shell.MergeEnv(env, extraEnv)

	runGit := func(combined bool, args ...string) ([]byte, error) {
		cmdCtx, cancel := context.WithTimeout(context.Background(), cloneCommandTimeout)
		defer cancel()
		full := append([]string{"-C", repo.LocalPath}, args...)
		c := exec.CommandContext(cmdCtx, "git", full...)
		c.WaitDelay = 2 * time.Second
		c.Env = env
		var out []byte
		var err error
		if combined {
			out, err = c.CombinedOutput()
		} else {
			out, err = c.Output()
		}
		if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("git %s timed out after %s", strings.Join(args, " "), cloneCommandTimeout)
		}
		return out, err
	}

	statusOut, err := runGit(false, "status", "--porcelain")
	if err != nil {
		item.Status = shell.PullFailed
		item.Error = err.Error()
		return item
	}
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		item.Status = shell.PullDirty
		return item
	}

	if _, err := runGit(false, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil {
		item.Status = shell.PullNoUpstream
		return item
	}

	if dryRun {
		item.Status = shell.PullDryRun
		return item
	}

	beforeOut, err := runGit(false, "rev-parse", "HEAD")
	if err != nil {
		item.Status = shell.PullFailed
		item.Error = err.Error()
		return item
	}
	before := strings.TrimSpace(string(beforeOut))

	if out, err := runGit(true, "pull", "--ff-only", "--quiet"); err != nil {
		item.Status = shell.PullFailed
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		item.Error = msg
		return item
	}

	afterOut, err := runGit(false, "rev-parse", "HEAD")
	if err != nil {
		item.Status = shell.PullFailed
		item.Error = err.Error()
		return item
	}
	if before == strings.TrimSpace(string(afterOut)) {
		item.Status = shell.PullUpToDate
	} else {
		item.Status = shell.PullUpdated
	}
	return item
}

func BuildInteractivePullCmd(ctx *app.Context, repo RepoRecord) *exec.Cmd {
	cmd := exec.Command("git", "-C", repo.LocalPath, "pull", "--ff-only")
	env := stripEnvKey(os.Environ(), "GIT_TERMINAL_PROMPT")
	if ctx != nil && repo.Zone != "" {
		if zoneEnv, err := shell.ResolveZoneEnv(ctx, repo.Zone); err == nil {
			env = shell.ReplaceZoneEnv(env, zoneEnv)
		}
	}
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func stripEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func formatCmdErr(name string, out []byte, err error) error {
	var exitErr *exec.ExitError
	msg := strings.TrimSpace(string(out))
	if msg == "" && errors.As(err, &exitErr) {
		msg = strings.TrimSpace(string(exitErr.Stderr))
	}
	if msg != "" {
		return fmt.Errorf("%s failed: %w (%s)", name, err, msg)
	}
	return fmt.Errorf("%s failed: %w", name, err)
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func CheckRepoPullReady(repo RepoRecord) (dirty bool, noUpstream bool, err error) {
	statusOut, serr := exec.Command("git", "-C", repo.LocalPath, "status", "--porcelain").Output()
	if serr != nil {
		return false, false, serr
	}
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		return true, false, nil
	}
	if err := exec.Command("git", "-C", repo.LocalPath, "rev-parse", "--abbrev-ref", "@{upstream}").Run(); err != nil {
		return false, true, nil
	}
	return false, false, nil
}

type ForgeSpec struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

type ForgeRepo struct {
	Display  string `json:"display"`
	CloneRef string `json:"cloneRef"`
	Name     string `json:"name"`
}

type BulkCloneResult struct {
	Forge    ForgeSpec `json:"forge"`
	Zone     string    `json:"zone,omitempty"`
	Cloned   []string  `json:"cloned,omitempty"`
	Existing []string  `json:"existing,omitempty"`
	Failed   []string  `json:"failed,omitempty"`
	DryRun   bool      `json:"dryRun,omitempty"`
}

func ParseForges(ctx *app.Context) ([]ForgeSpec, error) {
	raw := strings.TrimSpace(os.Getenv("WS_FORGES"))
	if ctx != nil {
		raw = strings.TrimSpace(ctx.LookupEnv("WS_FORGES"))
	}
	if raw == "" {
		return nil, fmt.Errorf("WS_FORGES is not set — define forge lines in mise.local.toml [env] (kind|source|dest)")
	}
	var specs []ForgeSpec
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			return nil, fmt.Errorf("malformed forge line %q (expected kind|source|dest)", line)
		}
		if len(parts) > 3 {
			return nil, fmt.Errorf("forge line %q has leftover identity fields — commit identity now comes from the zone gitconfig; trim to kind|source|dest", line)
		}
		kind := strings.ToLower(strings.TrimSpace(parts[0]))
		source := strings.TrimSpace(parts[1])
		dest := strings.TrimSpace(parts[2])
		if kind != "github" && kind != "codecommit" {
			return nil, fmt.Errorf("unknown forge kind %q (expected github or codecommit)", kind)
		}
		if source == "" || dest == "" || strings.ContainsAny(source+dest, "\n\r\x00") || strings.HasPrefix(source, "-") {
			return nil, fmt.Errorf("malformed forge line %q (expected kind|source|dest)", line)
		}
		if ctx != nil {
			dest = filepath.Clean(ctx.ExpandHome(dest))
		}
		specs = append(specs, ForgeSpec{
			Kind:   kind,
			Source: source,
			Dest:   dest,
		})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("WS_FORGES contains no forge lines")
	}
	return specs, nil
}

func ResolveForgeSelection(forges []ForgeSpec, selector string) ([]ForgeSpec, error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return append([]ForgeSpec(nil), forges...), nil
	}
	var out []ForgeSpec
	for _, f := range forges {
		if strings.EqualFold(sel, f.Kind) || strings.EqualFold(sel, f.Kind+":"+f.Source) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no forge in WS_FORGES matches %q", selector)
	}
	return out, nil
}

func DeriveZoneForPath(ctx *app.Context, destPath string) string {
	if ctx == nil || ctx.HomeDir == "" || strings.TrimSpace(destPath) == "" {
		return ""
	}
	cleanDest := filepath.Clean(ctx.ExpandHome(destPath))
	candidates := map[string]string{}
	if specs, err := shell.ParseZones(ctx); err == nil {
		for _, s := range specs {
			candidates[s.Name] = filepath.Join(ctx.HomeDir, s.Name)
		}
	}
	if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			zroot := filepath.Join(ctx.HomeDir, e.Name())
			if envMap, err := shell.ReadZoneMiseEnv(zroot); err == nil && envMap["WS_ZONE"] != "" {
				candidates[envMap["WS_ZONE"]] = zroot
			}
		}
	}
	names := make([]string, 0, len(candidates))
	for z := range candidates {
		names = append(names, z)
	}
	sort.Strings(names)
	for _, z := range names {
		zroot := filepath.Clean(candidates[z])
		if cleanDest == zroot || strings.HasPrefix(cleanDest, zroot+string(filepath.Separator)) {
			return z
		}
	}
	return ""
}

func ListForgeRepos(ctx *app.Context, forge ForgeSpec, zone string, runner CommandRunner) ([]ForgeRepo, error) {
	if runner == nil {
		runner = execCommandRunner{}
	}
	env, err := buildForgeExecEnv(ctx, zone)
	if err != nil {
		return nil, err
	}

	var repos []ForgeRepo
	switch forge.Kind {
	case "github":
		if strings.Contains(forge.Source, "/") {
			if !isGitHubShorthand(forge.Source) {
				return nil, fmt.Errorf("invalid GitHub source %q", forge.Source)
			}
			repos = append(repos, ForgeRepo{
				Display:  forge.Source,
				CloneRef: forge.Source,
				Name:     filepath.Base(forge.Source),
			})
			return repos, nil
		}
		out, err := runner.Output(env, "", "gh", "repo", "list", forge.Source, "--limit", "1000", "--json", "nameWithOwner")
		if err != nil {
			return nil, formatCmdErr("gh repo list", out, err)
		}
		var parsed []struct {
			NameWithOwner string `json:"nameWithOwner"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return nil, fmt.Errorf("parse gh repo list output: %w", err)
		}
		for _, p := range parsed {
			slug := strings.TrimSpace(p.NameWithOwner)
			if !isGitHubShorthand(slug) {
				continue
			}
			repos = append(repos, ForgeRepo{
				Display:  slug,
				CloneRef: slug,
				Name:     filepath.Base(slug),
			})
		}

	case "codecommit":
		out, err := runner.Output(env, "", "aws", "codecommit", "list-repositories", "--profile", forge.Source, "--output", "json")
		if err != nil {
			return nil, formatCmdErr("aws codecommit list-repositories", out, err)
		}
		var parsed struct {
			Repositories []struct {
				RepositoryName string `json:"repositoryName"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return nil, fmt.Errorf("parse codecommit repository list: %w", err)
		}
		for _, r := range parsed.Repositories {
			name := strings.TrimSpace(r.RepositoryName)
			if err := validateDestName(name); err != nil {
				continue
			}
			repos = append(repos, ForgeRepo{
				Display:  name,
				CloneRef: "codecommit://" + forge.Source + "@" + name,
				Name:     name,
			})
		}
	default:
		return nil, fmt.Errorf("unknown forge kind %q", forge.Kind)
	}

	sort.Slice(repos, func(i, j int) bool { return repos[i].Display < repos[j].Display })
	return repos, nil
}

func BulkCloneForgeRepos(ctx *app.Context, forge ForgeSpec, zone string, selected []ForgeRepo, runner CommandRunner) (*BulkCloneResult, error) {
	if runner == nil {
		runner = execCommandRunner{}
	}
	cleanDest := filepath.Clean(forge.Dest)
	if ctx != nil {
		cleanDest = filepath.Clean(ctx.ExpandHome(forge.Dest))
	}
	res := &BulkCloneResult{
		Forge:  forge,
		Zone:   zone,
		DryRun: ctx != nil && ctx.DryRun,
	}
	if len(selected) == 0 {
		return res, nil
	}
	env, err := buildForgeExecEnv(ctx, zone)
	if err != nil {
		return nil, err
	}

	for _, r := range selected {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = deriveRepoName(r.Display)
		}
		if err := validateDestName(name); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", r.Display, err))
			continue
		}
		destDir := filepath.Clean(filepath.Join(cleanDest, name))
		if !strings.HasPrefix(destDir, cleanDest+string(filepath.Separator)) {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: escapes destination root", r.Display))
			continue
		}
		if fi, statErr := os.Stat(destDir); statErr == nil && fi.IsDir() {
			res.Existing = append(res.Existing, ctx.CollapseHome(destDir))
			continue
		}
		if ctx != nil && ctx.DryRun {
			res.Cloned = append(res.Cloned, ctx.CollapseHome(destDir))
			continue
		}
		if err := os.MkdirAll(cleanDest, 0o755); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", r.Display, err))
			continue
		}
		var cmd []string
		if forge.Kind == "github" {
			cmd = []string{"gh", "repo", "clone", r.CloneRef, destDir}
		} else {
			cmd = []string{"git", "clone", "--", r.CloneRef, destDir}
		}
		if out, err := runner.Output(env, "", cmd[0], cmd[1:]...); err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", r.Display, formatCmdErr(cmd[0], out, err)))
			continue
		}
		res.Cloned = append(res.Cloned, ctx.CollapseHome(destDir))
	}
	if len(res.Failed) > 0 {
		return res, fmt.Errorf("bulk clone failed for %d repo(s)", len(res.Failed))
	}
	return res, nil
}

func buildForgeExecEnv(ctx *app.Context, zone string) ([]string, error) {
	env := os.Environ()
	if zone != "" && ctx != nil {
		zoneEnv, err := shell.ResolveZoneEnv(ctx, zone)
		if err != nil {
			return nil, fmt.Errorf("resolve zone %q env: %w", zone, err)
		}
		env = shell.ReplaceZoneEnv(env, zoneEnv)
		env = stripEnvKey(stripEnvKey(env, "GH_TOKEN"), "GITHUB_TOKEN")
	}
	return env, nil
}
