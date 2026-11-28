package misemgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/projects"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/pelletier/go-toml/v2"
)

const (
	miseSubprocessTimeout = 8 * time.Second
	maxOutdatedWorkers    = 4
)

type CommandRunner interface {
	Run(cmd []string, env map[string]string, workDir string) (stdout, stderr string, err error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(cmd []string, env map[string]string, workDir string) (string, string, error) {
	if len(cmd) == 0 {
		return "", "", fmt.Errorf("empty command")
	}
	cctx, cancel := context.WithTimeout(context.Background(), miseSubprocessTimeout)
	defer cancel()
	ec := exec.CommandContext(cctx, cmd[0], cmd[1:]...)
	if workDir != "" {
		ec.Dir = workDir
	}
	if len(env) > 0 {
		merged := append([]string(nil), os.Environ()...)
		for k, v := range env {
			merged = append(merged, k+"="+v)
		}
		ec.Env = merged
	}
	out, err := ec.CombinedOutput()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return string(out), "", fmt.Errorf("%s timed out after %s", cmd[0], miseSubprocessTimeout)
	}
	return string(out), "", err
}

type ToolDecl struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Backend   string `json:"backend,omitempty"`
	Line      int    `json:"line,omitempty"`
}

type ConfigFile struct {
	Path          string     `json:"path"`
	Kind          string     `json:"kind"`
	Scope         string     `json:"scope"`
	Zone          string     `json:"zone,omitempty"`
	ProjectName   string     `json:"projectName,omitempty"`
	ProjectPath   string     `json:"projectPath,omitempty"`
	Forge         string     `json:"forge,omitempty"`
	GitHubSlug    string     `json:"githubSlug,omitempty"`
	RemoteURL     string     `json:"remoteUrl,omitempty"`
	Branch        string     `json:"branch,omitempty"`
	Dirty         bool       `json:"dirty,omitempty"`
	LocalOverride bool       `json:"localOverride"`
	Exists        bool       `json:"exists"`
	Tools         []ToolDecl `json:"tools,omitempty"`
	EnvKeys       []string   `json:"envKeys,omitempty"`
	Tasks         []string   `json:"tasks,omitempty"`
	OutdatedCount int        `json:"outdatedCount"`
	MissingCount  int        `json:"missingCount"`
	SizeBytes     int64      `json:"sizeBytes,omitempty"`
	ModTime       int64      `json:"modTime,omitempty"`
}

type InstalledTool struct {
	Name        string `json:"name"`
	Requested   string `json:"requested"`
	Version     string `json:"version"`
	Latest      string `json:"latest,omitempty"`
	InstallPath string `json:"installPath,omitempty"`
	SourcePath  string `json:"sourcePath"`
	Scope       string `json:"scope"`
	Zone        string `json:"zone,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	ProjectPath string `json:"projectPath,omitempty"`
	Forge       string `json:"forge,omitempty"`
	GitHubSlug  string `json:"githubSlug,omitempty"`
	Installed   bool   `json:"installed"`
	Active      bool   `json:"active"`
	Outdated    bool   `json:"outdated"`
	Status      string `json:"status"`
}

type Inventory struct {
	ScannedAt     int64           `json:"scannedAt"`
	MiseVersion   string          `json:"miseVersion,omitempty"`
	Configs       []ConfigFile    `json:"configs"`
	Tools         []InstalledTool `json:"tools"`
	OutdatedCount int             `json:"outdatedCount"`
	MissingCount  int             `json:"missingCount"`
	Warnings      []string        `json:"warnings,omitempty"`
}

type miseLSItem struct {
	Version     string `json:"version"`
	Requested   string `json:"requested_version"`
	InstallPath string `json:"install_path"`
	Installed   bool   `json:"installed"`
	Active      bool   `json:"active"`
	Source      struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
}

type miseOutdatedEntry struct {
	Name      string `json:"name"`
	Requested string `json:"requested"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Source    struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
}

func Scan(ctx *app.Context, repos []projects.RepoRecord, zoneFilter string, checkOutdated bool) (*Inventory, error) {
	return ScanWithRunner(ctx, osCommandRunner{}, repos, zoneFilter, checkOutdated)
}

func ScanWithRunner(ctx *app.Context, runner CommandRunner, repos []projects.RepoRecord, zoneFilter string, checkOutdated bool) (*Inventory, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if runner == nil {
		runner = osCommandRunner{}
	}
	zf := normalizeZone(zoneFilter)

	inv := &Inventory{
		ScannedAt: time.Now().Unix(),
	}

	workDir := filepath.Clean(strings.TrimSpace(ctx.HomeDir))
	if workDir == "." {
		workDir = ""
	}
	if verOut, _, err := runner.Run([]string{"mise", "--version"}, nil, workDir); err == nil {
		for _, ln := range strings.Split(verOut, "\n") {
			t := strings.TrimSpace(ln)
			if t != "" && !strings.HasPrefix(t, "_") && !strings.HasPrefix(t, "/") {
				fields := strings.Fields(t)
				if len(fields) > 0 && (strings.Contains(fields[0], ".") || strings.HasPrefix(fields[0], "v")) {
					inv.MiseVersion = fields[0]
					break
				}
			}
		}
	}

	installedMap, lsWarn := queryMiseInstalled(runner, workDir)
	if lsWarn != "" {
		inv.Warnings = append(inv.Warnings, lsWarn)
	}

	candidates := discoverConfigCandidates(ctx, repos)
	configs := make([]ConfigFile, 0, len(candidates))
	for _, cand := range candidates {
		if zf != "" && cand.Zone != "" && !strings.EqualFold(cand.Zone, zf) {
			continue
		}
		if zf != "" && cand.Zone == "" && cand.Scope != "global" {
			continue
		}
		info, err := os.Stat(cand.Path)
		if err != nil || info.IsDir() {
			continue
		}
		cand.Exists = true
		cand.SizeBytes = info.Size()
		cand.ModTime = info.ModTime().Unix()

		if err := parseMiseConfigFile(&cand); err != nil {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf("parse %s: %v", cand.Path, err))
		}
		configs = append(configs, cand)
	}

	outdatedByDir := map[string]map[string]miseOutdatedEntry{}
	if checkOutdated {
		workDirs := collectOutdatedWorkDirs(ctx, configs)
		outdatedByDir = queryOutdatedByDir(runner, workDirs)
	}

	globalOutdatedKey := filepath.Clean(ctx.HomeDir)
	globalOutdated := outdatedByDir[globalOutdatedKey]

	var tools []InstalledTool
	for ci := range configs {
		cfg := &configs[ci]
		var dirOut map[string]miseOutdatedEntry
		if cfg.ProjectPath != "" {
			dirOut = outdatedByDir[filepath.Clean(cfg.ProjectPath)]
		} else {
			dirOut = globalOutdated
		}

		for _, td := range cfg.Tools {
			row := InstalledTool{
				Name:        td.Name,
				Requested:   td.Requested,
				SourcePath:  cfg.Path,
				Scope:       cfg.Scope,
				Zone:        cfg.Zone,
				ProjectName: cfg.ProjectName,
				ProjectPath: cfg.ProjectPath,
				Forge:       cfg.Forge,
				GitHubSlug:  cfg.GitHubSlug,
				Status:      "missing",
			}

			if matched, ok := matchInstalledMiseItem(installedMap, td.Name, td.Requested, cfg.Path); ok {
				row.Installed = true
				row.Active = matched.Active
				row.Version = matched.Version
				row.InstallPath = matched.InstallPath
				row.Status = "installed"
			}

			if outEntry, isOut := findOutdatedEntry(dirOut, td.Name, cfg.Path); isOut {
				row.Outdated = true
				row.Latest = outEntry.Latest
				if row.Version == "" && outEntry.Current != "" {
					row.Version = outEntry.Current
				}
				if row.Installed || outEntry.Current != "" {
					row.Installed = true
					row.Status = "outdated"
				}
			}

			switch row.Status {
			case "outdated":
				cfg.OutdatedCount++
				inv.OutdatedCount++
			case "missing":
				cfg.MissingCount++
				inv.MissingCount++
			}
			tools = append(tools, row)
		}
	}

	seenDeclared := make(map[string]bool, len(tools))
	for _, t := range tools {
		seenDeclared[strings.ToLower(t.Name)+"\x00"+filepath.Clean(t.SourcePath)] = true
	}
	if zf == "" {
		var extraNames []string
		for name := range installedMap {
			extraNames = append(extraNames, name)
		}
		sort.Strings(extraNames)
		for _, name := range extraNames {
			for _, item := range installedMap[name] {
				if !item.Active {
					continue
				}
				src := filepath.Clean(strings.TrimSpace(item.Source.Path))
				if src == "" || src == "." {
					continue
				}
				if seenDeclared[strings.ToLower(name)+"\x00"+src] {
					continue
				}
				row := InstalledTool{
					Name:        name,
					Requested:   nonEmpty(item.Requested, item.Version),
					Version:     item.Version,
					InstallPath: item.InstallPath,
					SourcePath:  src,
					Scope:       "global",
					Installed:   item.Installed,
					Active:      true,
					Status:      "installed",
				}
				if outEntry, isOut := findOutdatedEntry(globalOutdated, name, src); isOut {
					row.Outdated = true
					row.Latest = outEntry.Latest
					row.Status = "outdated"
					inv.OutdatedCount++
				}
				tools = append(tools, row)
			}
		}
	}

	sort.SliceStable(configs, func(i, j int) bool {
		if scopeRank(configs[i].Scope) != scopeRank(configs[j].Scope) {
			return scopeRank(configs[i].Scope) < scopeRank(configs[j].Scope)
		}
		if configs[i].Zone != configs[j].Zone {
			return configs[i].Zone < configs[j].Zone
		}
		return configs[i].Path < configs[j].Path
	})

	sort.SliceStable(tools, func(i, j int) bool {
		if statusRank(tools[i].Status) != statusRank(tools[j].Status) {
			return statusRank(tools[i].Status) < statusRank(tools[j].Status)
		}
		if tools[i].Name != tools[j].Name {
			return tools[i].Name < tools[j].Name
		}
		return tools[i].SourcePath < tools[j].SourcePath
	})

	inv.Configs = configs
	inv.Tools = tools
	return inv, nil
}

func ValidateEditableMisePath(ctx *app.Context, rawPath string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("context is required")
	}
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("config path is required")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}

	base := filepath.Base(abs)
	if !isAllowedMiseFilename(abs, base) {
		return "", fmt.Errorf("refusing non-mise file %s", base)
	}

	roots := []string{ctx.HomeDir, ctx.ConfigDir, ctx.RepoRoot}
	inRoot := false
	for _, r := range roots {
		if r == "" {
			continue
		}
		rResolved, rErr := filepath.EvalSymlinks(r)
		if rErr != nil {
			rResolved = filepath.Clean(r)
		}
		if isWithinRoot(resolved, rResolved) || isWithinRoot(abs, filepath.Clean(r)) {
			inRoot = true
			break
		}
	}
	if !inRoot {
		return "", fmt.Errorf("path %s is outside allowed workstation directories", abs)
	}
	return abs, nil
}

func isAllowedMiseFilename(fullPath, base string) bool {
	switch base {
	case "mise.toml", "mise.local.toml", ".mise.toml", ".mise.local.toml", ".tool-versions", "config.toml", "config.local.toml":
		return true
	}
	slash := filepath.ToSlash(fullPath)
	return strings.HasSuffix(base, ".toml") && (strings.Contains(slash, "/mise/") || strings.Contains(slash, "/.mise/"))
}

func isWithinRoot(target, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type discoveredZone struct {
	name     string
	root     string
	codeRoot string
}

func discoverZones(ctx *app.Context) []discoveredZone {
	zoneIdx := map[string]int{}
	var zones []discoveredZone
	registerZone := func(name, root, codeRoot string) {
		name = strings.TrimSpace(name)
		root = strings.TrimSpace(root)
		codeRoot = strings.TrimSpace(codeRoot)
		if name == "" || root == "" {
			return
		}
		cleanRoot := filepath.Clean(root)
		cleanCode := ""
		if codeRoot != "" {
			cleanCode = filepath.Clean(ctx.ExpandHome(codeRoot))
		}
		if idx, ok := zoneIdx[name]; ok {
			if zones[idx].codeRoot == "" && cleanCode != "" {
				zones[idx].codeRoot = cleanCode
			}
			return
		}
		if cleanCode == "" {
			cleanCode = filepath.Join(cleanRoot, "code")
		}
		zoneIdx[name] = len(zones)
		zones = append(zones, discoveredZone{
			name:     name,
			root:     cleanRoot,
			codeRoot: cleanCode,
		})
	}

	if statuses, err := shell.ZoneStatuses(ctx); err == nil {
		for _, st := range statuses {
			registerZone(st.Spec.Name, st.Root, st.CodeRoot)
		}
	}
	if ctx.HomeDir != "" {
		if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
					continue
				}
				zroot := filepath.Join(ctx.HomeDir, e.Name())
				envMap, err := shell.ReadZoneMiseEnv(zroot)
				if err != nil {
					continue
				}
				if zname := envMap["WS_ZONE"]; zname != "" {
					codeRoot := filepath.Join(zroot, "code")
					if cr := envMap["WS_CODE_ROOT"]; cr != "" {
						codeRoot = cr
					}
					registerZone(zname, zroot, codeRoot)
				}
			}
		}
	}

	sort.Slice(zones, func(i, j int) bool {
		return zones[i].name < zones[j].name
	})
	return zones
}

func discoverConfigCandidates(ctx *app.Context, repos []projects.RepoRecord) []ConfigFile {
	seen := map[string]bool{}
	var out []ConfigFile

	addCandidate := func(cf ConfigFile) {
		clean := filepath.Clean(cf.Path)
		if clean == "" || seen[clean] {
			return
		}
		seen[clean] = true
		cf.Path = clean
		base := filepath.Base(clean)
		cf.Kind = base
		cf.LocalOverride = strings.Contains(base, ".local.")
		out = append(out, cf)
	}

	if ctx.ConfigDir != "" {
		miseDir := filepath.Join(ctx.ConfigDir, "mise")
		addCandidate(ConfigFile{Path: filepath.Join(miseDir, "config.toml"), Scope: "global"})
		addCandidate(ConfigFile{Path: filepath.Join(miseDir, "config.local.toml"), Scope: "global"})
		confD := filepath.Join(miseDir, "conf.d")
		if entries, err := os.ReadDir(confD); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".toml") {
					addCandidate(ConfigFile{Path: filepath.Join(confD, e.Name()), Scope: "global"})
				}
			}
		}
	}
	if ctx.HomeDir != "" {
		for _, name := range []string{".mise.toml", ".mise.local.toml", ".tool-versions"} {
			addCandidate(ConfigFile{Path: filepath.Join(ctx.HomeDir, name), Scope: "global"})
		}
	}

	zones := discoverZones(ctx)
	for _, z := range zones {
		if z.root == "" {
			continue
		}
		for _, name := range []string{"mise.toml", "mise.local.toml", ".mise.toml", ".mise.local.toml"} {
			addCandidate(ConfigFile{
				Path:        filepath.Join(z.root, name),
				Scope:       "zone",
				Zone:        z.name,
				ProjectName: z.name + " (zone)",
				ProjectPath: z.root,
				Forge:       "local",
			})
		}
	}

	projectRelFiles := []string{
		"mise.toml",
		"mise.local.toml",
		".mise.toml",
		".mise.local.toml",
		filepath.Join(".mise", "config.toml"),
		filepath.Join(".mise", "config.local.toml"),
		".tool-versions",
	}
	for _, r := range repos {
		if strings.TrimSpace(r.LocalPath) == "" {
			continue
		}
		forge := strings.ToLower(strings.TrimSpace(r.Forge))
		if forge == "" {
			forge = projects.DetectForge(r.RemoteURL)
		}
		if forge == "other" && strings.TrimSpace(r.RemoteURL) == "" {
			forge = "local"
		}
		ghSlug := projects.ExtractGitHubRepoSlug(r.RemoteURL)
		safeRemote := projects.StripURLCredentials(r.RemoteURL)
		projName := nonEmpty(r.Name, filepath.Base(r.LocalPath))

		for _, rel := range projectRelFiles {
			addCandidate(ConfigFile{
				Path:        filepath.Join(r.LocalPath, rel),
				Scope:       "project",
				Zone:        r.Zone,
				ProjectName: projName,
				ProjectPath: filepath.Clean(r.LocalPath),
				Forge:       forge,
				GitHubSlug:  ghSlug,
				RemoteURL:   safeRemote,
				Branch:      r.Branch,
				Dirty:       r.Dirty,
			})
		}
	}

	for _, z := range zones {
		codeRoot := filepath.Clean(strings.TrimSpace(z.codeRoot))
		if codeRoot == "" {
			continue
		}
		entries, err := os.ReadDir(codeRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			projDir := filepath.Join(codeRoot, e.Name())
			for _, rel := range projectRelFiles {
				addCandidate(ConfigFile{
					Path:        filepath.Join(projDir, rel),
					Scope:       "project",
					Zone:        z.name,
					ProjectName: e.Name(),
					ProjectPath: projDir,
					Forge:       "local",
				})
			}
		}
	}

	if ctx.RepoRoot != "" {
		forge := "local"
		slug := ""
		remote := ""
		if rOut, err := exec.Command("git", "-C", ctx.RepoRoot, "remote", "get-url", "origin").Output(); err == nil {
			rawRemote := strings.TrimSpace(string(rOut))
			if rawRemote != "" {
				forge = projects.DetectForge(rawRemote)
				slug = projects.ExtractGitHubRepoSlug(rawRemote)
				remote = projects.StripURLCredentials(rawRemote)
			}
		}
		repoZone := ""
		for _, z := range zones {
			if z.root != "" && isWithinRoot(filepath.Clean(ctx.RepoRoot), filepath.Clean(z.root)) {
				repoZone = z.name
				break
			}
		}
		for _, rel := range projectRelFiles {
			addCandidate(ConfigFile{
				Path:        filepath.Join(ctx.RepoRoot, rel),
				Scope:       "project",
				Zone:        repoZone,
				ProjectName: filepath.Base(ctx.RepoRoot),
				ProjectPath: filepath.Clean(ctx.RepoRoot),
				Forge:       forge,
				GitHubSlug:  slug,
				RemoteURL:   remote,
			})
		}
	}

	return out
}

func parseMiseConfigFile(cfg *ConfigFile) error {
	raw, err := os.ReadFile(cfg.Path)
	if err != nil {
		return err
	}
	text := string(raw)
	if filepath.Base(cfg.Path) == ".tool-versions" {
		cfg.Tools = parseToolVersions(text)
		return nil
	}

	var doc struct {
		Tools map[string]any `toml:"tools"`
		Env   map[string]any `toml:"env"`
		Tasks map[string]any `toml:"tasks"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return err
	}

	lineByTool := indexToolLines(text)
	var toolNames []string
	for k := range doc.Tools {
		toolNames = append(toolNames, k)
	}
	sort.Strings(toolNames)
	for _, name := range toolNames {
		req := extractToolRequestedVersion(doc.Tools[name])
		backend := ""
		if colon := strings.IndexByte(name, ':'); colon > 0 {
			backend = name[:colon]
		}
		cfg.Tools = append(cfg.Tools, ToolDecl{
			Name:      name,
			Requested: req,
			Backend:   backend,
			Line:      lineByTool[name],
		})
	}

	for k := range doc.Env {
		if k == "_" {
			continue
		}
		cfg.EnvKeys = append(cfg.EnvKeys, k)
	}
	sort.Strings(cfg.EnvKeys)

	for k := range doc.Tasks {
		cfg.Tasks = append(cfg.Tasks, k)
	}
	sort.Strings(cfg.Tasks)
	return nil
}

func parseToolVersions(text string) []ToolDecl {
	var out []ToolDecl
	for idx, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if hash := strings.IndexByte(trimmed, '#'); hash >= 0 {
			trimmed = strings.TrimSpace(trimmed[:hash])
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 1 {
			continue
		}
		req := "latest"
		if len(fields) >= 2 {
			req = strings.Join(fields[1:], ",")
		}
		out = append(out, ToolDecl{
			Name:      fields[0],
			Requested: req,
			Line:      idx + 1,
		})
	}
	return out
}

func extractToolRequestedVersion(v any) string {
	switch tv := v.(type) {
	case string:
		return strings.TrimSpace(tv)
	case []any:
		var parts []string
		for _, elem := range tv {
			if s := extractToolRequestedVersion(elem); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		if ver, ok := tv["version"]; ok {
			return fmt.Sprint(ver)
		}
		return "custom"
	default:
		if tv == nil {
			return ""
		}
		return fmt.Sprint(tv)
	}
}

func indexToolLines(text string) map[string]int {
	lines := strings.Split(text, "\n")
	out := map[string]int{}
	inTools := false
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inTools = trimmed == "[tools]" || strings.HasPrefix(trimmed, "[tools.")
			continue
		}
		if !inTools {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		key = strings.Trim(key, `"'`)
		if key != "" && out[key] == 0 {
			out[key] = i + 1
		}
	}
	return out
}

func queryMiseInstalled(runner CommandRunner, workDir string) (map[string][]miseLSItem, string) {
	out := map[string][]miseLSItem{}
	stdout, _, err := runner.Run([]string{"mise", "ls", "--json"}, nil, workDir)
	if err != nil && strings.TrimSpace(stdout) == "" {
		return out, fmt.Sprintf("mise ls --json unavailable: %v", err)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return out, ""
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, ""
	}
	return out, "unable to parse mise ls --json output"
}

func matchInstalledMiseItem(installedMap map[string][]miseLSItem, name, requested, sourcePath string) (miseLSItem, bool) {
	items := installedMap[name]
	if len(items) == 0 {
		for k, list := range installedMap {
			if strings.EqualFold(k, name) {
				items = list
				break
			}
		}
	}
	if len(items) == 0 {
		return miseLSItem{}, false
	}
	cleanSrc := filepath.Clean(sourcePath)
	for _, it := range items {
		if it.Installed && it.Source.Path != "" && filepath.Clean(it.Source.Path) == cleanSrc {
			return it, true
		}
	}
	for _, it := range items {
		if it.Installed && requested != "" && requested != "latest" && (it.Requested == requested || it.Version == requested || strings.HasPrefix(it.Version, requested+".")) {
			return it, true
		}
	}
	for _, it := range items {
		if it.Installed && it.Active {
			return it, true
		}
	}
	for _, it := range items {
		if it.Installed {
			return it, true
		}
	}
	return miseLSItem{}, false
}

func collectOutdatedWorkDirs(ctx *app.Context, configs []ConfigFile) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		clean := filepath.Clean(dir)
		if clean == "" || clean == "." || seen[clean] {
			return
		}
		seen[clean] = true
		dirs = append(dirs, clean)
	}
	if ctx.HomeDir != "" {
		add(ctx.HomeDir)
	}
	for _, c := range configs {
		if len(c.Tools) == 0 {
			continue
		}
		if c.ProjectPath != "" {
			add(c.ProjectPath)
		} else {
			add(filepath.Dir(c.Path))
		}
	}
	return dirs
}

func queryOutdatedByDir(runner CommandRunner, dirs []string) map[string]map[string]miseOutdatedEntry {
	result := make(map[string]map[string]miseOutdatedEntry, len(dirs))
	if len(dirs) == 0 {
		return result
	}
	var mu sync.Mutex
	sem := make(chan struct{}, maxOutdatedWorkers)
	var wg sync.WaitGroup
	for _, d := range dirs {
		dir := d
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			entries := map[string]miseOutdatedEntry{}
			if stdout, _, err := runner.Run([]string{"mise", "outdated", "--json"}, nil, dir); err == nil {
				for _, e := range parseOutdatedJSON(stdout) {
					entries[strings.ToLower(e.Name)] = e
				}
			} else if stdout != "" {
				for _, e := range parseOutdatedJSON(stdout) {
					entries[strings.ToLower(e.Name)] = e
				}
			}
			mu.Lock()
			result[dir] = entries
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}

func parseOutdatedJSON(stdout string) []miseOutdatedEntry {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil
	}
	var out []miseOutdatedEntry
	switch trimmed[0] {
	case '{':
		var byTool map[string]miseOutdatedEntry
		if err := json.Unmarshal([]byte(trimmed), &byTool); err != nil {
			return nil
		}
		for k, entry := range byTool {
			if strings.TrimSpace(entry.Name) == "" {
				entry.Name = k
			}
			out = append(out, entry)
		}
	case '[':
		var list []miseOutdatedEntry
		if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
			return nil
		}
		out = append(out, list...)
	}
	return out
}

func findOutdatedEntry(entries map[string]miseOutdatedEntry, toolName, sourcePath string) (miseOutdatedEntry, bool) {
	if len(entries) == 0 {
		return miseOutdatedEntry{}, false
	}
	e, ok := entries[strings.ToLower(toolName)]
	if !ok {
		return miseOutdatedEntry{}, false
	}
	if e.Source.Path != "" && sourcePath != "" && filepath.Clean(e.Source.Path) != filepath.Clean(sourcePath) {
		return miseOutdatedEntry{}, false
	}
	return e, true
}

func scopeRank(scope string) int {
	switch scope {
	case "global":
		return 0
	case "zone":
		return 1
	default:
		return 2
	}
}

func statusRank(status string) int {
	switch status {
	case "outdated":
		return 0
	case "missing":
		return 1
	default:
		return 2
	}
}

func normalizeZone(z string) string {
	trimmed := strings.TrimSpace(z)
	if strings.EqualFold(trimmed, "all") {
		return ""
	}
	return trimmed
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
