package app

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

var zoneScopedEnvKeys = map[string]bool{
	"WS_ZONE":                     true,
	"WS_CODE_ROOT":                true,
	"GH_CONFIG_DIR":               true,
	"CLAUDE_CONFIG_DIR":           true,
	"CODEX_HOME":                  true,
	"AWS_CONFIG_FILE":             true,
	"AWS_SHARED_CREDENTIALS_FILE": true,
	"AWS_PROFILE":                 true,
	"KUBECONFIG":                  true,
}

var probeEnvOverrides = []string{
	"MISE_AUTO_INSTALL=0",
	"MISE_NOT_FOUND_AUTO_INSTALL=0",
	"MISE_EXEC_AUTO_INSTALL=0",
}

func ProbeEnv(base []string) []string {
	out := make([]string, 0, len(base)+len(probeEnvOverrides))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "MISE_AUTO_INSTALL" || k == "MISE_NOT_FOUND_AUTO_INSTALL" || k == "MISE_EXEC_AUTO_INSTALL" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, probeEnvOverrides...)
}

func PinnedMachineEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if ok && zoneScopedEnvKeys[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

type AllowSet struct {
	Replace bool
	Sudo    bool
	Remove  bool
	Script  bool
	Yes     bool
}

func ParseAllowStrict(raw string) (AllowSet, error) {
	return parseAllow(raw, true)
}

func parseAllow(raw string, strict bool) (AllowSet, error) {
	var a AllowSet
	for _, part := range strings.Split(raw, ",") {
		tok := strings.TrimSpace(strings.ToLower(part))
		if tok == "" {
			continue
		}
		switch tok {
		case "replace":
			a.Replace = true
		case "sudo":
			a.Sudo = true
		case "remove":
			a.Remove = true
		case "script":
			a.Script = true
		case "all":
			a.Replace = true
			a.Sudo = true
			a.Remove = true
			a.Script = true
			a.Yes = true
		default:
			if strict {
				return AllowSet{}, fmt.Errorf("unknown --allow scope %q (expected replace, sudo, remove, script, or all)", tok)
			}
		}
	}
	return a, nil
}

func (a AllowSet) Satisfies(scope string) bool {
	trimmed := strings.TrimSpace(scope)
	if trimmed == "" {
		return true
	}
	for _, part := range strings.Split(trimmed, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "":
			continue
		case "replace":
			if !a.Replace {
				return false
			}
		case "sudo":
			if !a.Sudo {
				return false
			}
		case "remove":
			if !a.Remove {
				return false
			}
		case "script":
			if !a.Script {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (a AllowSet) String() string {
	var parts []string
	if a.Replace {
		parts = append(parts, "replace")
	}
	if a.Sudo {
		parts = append(parts, "sudo")
	}
	if a.Remove {
		parts = append(parts, "remove")
	}
	if a.Script {
		parts = append(parts, "script")
	}
	return strings.Join(parts, ",")
}

type HostInfo struct {
	OS            string   `json:"os"`
	Distro        string   `json:"distro"`
	DistroLike    []string `json:"distroLike,omitempty"`
	Arch          string   `json:"arch"`
	Headless      bool     `json:"headless"`
	PkgManagers   []string `json:"pkgManagers"`
	DefaultPreset string   `json:"defaultPreset"`
}

type Context struct {
	RepoRoot       string
	HomeDir        string
	WSHome         string
	ConfigDir      string
	DataDir        string
	StateDir       string
	CacheDir       string
	Host           HostInfo
	DryRun         bool
	NonInteractive bool
	JSON           bool
	Allow          AllowSet
	PresetOverride string
	cfgMu          *sync.RWMutex
	Config         *WSConfig
	ConfigErr      error
	LocalEnv       map[string]string
	MiseLocalErr   error
}

type Options struct {
	RepoRoot       string
	HomeDir        string
	WSHome         string
	DryRun         bool
	NonInteractive bool
	JSON           bool
	AllowRaw       string
	PresetOverride string
}

func NewContext(opts Options) (*Context, error) {
	allowSet, err := ParseAllowStrict(opts.AllowRaw)
	if err != nil {
		return nil, err
	}

	home := opts.HomeDir
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
	}
	if home != "" {
		if absHome, aerr := filepath.Abs(home); aerr == nil {
			home = absHome
		}
		home = filepath.Clean(home)
	}

	xdgOr := func(envKey string, fallbackParts ...string) string {
		if opts.HomeDir == "" {
			if v := strings.TrimSpace(os.Getenv(envKey)); v != "" && filepath.IsAbs(v) {
				return filepath.Clean(v)
			}
		}
		return filepath.Join(append([]string{home}, fallbackParts...)...)
	}

	var repoRoot string
	if opts.RepoRoot != "" {
		if !isWorkstationRoot(opts.RepoRoot) {
			return nil, fmt.Errorf("invalid workstation repository %q: missing themes/registry or links/common.links", opts.RepoRoot)
		}
		if abs, err := filepath.Abs(opts.RepoRoot); err == nil {
			repoRoot = abs
		} else {
			repoRoot = filepath.Clean(opts.RepoRoot)
		}
	} else {
		repoRoot = FindRepoRoot("", home)
	}
	host := DetectHost()

	wsHome := ""
	if strings.TrimSpace(opts.WSHome) != "" {
		if filepath.IsAbs(opts.WSHome) {
			if absWS, aerr := filepath.Abs(opts.WSHome); aerr == nil {
				wsHome = absWS
			} else {
				wsHome = filepath.Clean(opts.WSHome)
			}
		}
	}
	if wsHome == "" && opts.HomeDir == "" {
		if v := strings.TrimSpace(os.Getenv("WS_HOME")); v != "" && filepath.IsAbs(v) {
			wsHome = filepath.Clean(v)
		}
	}
	if wsHome == "" && home != "" {
		wsHome = filepath.Join(home, ".ws")
	}

	ctx := &Context{
		RepoRoot:       repoRoot,
		HomeDir:        home,
		WSHome:         wsHome,
		ConfigDir:      xdgOr("XDG_CONFIG_HOME", ".config"),
		DataDir:        xdgOr("XDG_DATA_HOME", ".local", "share"),
		StateDir:       xdgOr("XDG_STATE_HOME", ".local", "state"),
		CacheDir:       xdgOr("XDG_CACHE_HOME", ".cache"),
		Host:           host,
		DryRun:         opts.DryRun,
		NonInteractive: opts.NonInteractive,
		JSON:           opts.JSON,
		Allow:          allowSet,
		PresetOverride: opts.PresetOverride,
		cfgMu:          &sync.RWMutex{},
		Config:         &WSConfig{Env: map[string]string{}},
		LocalEnv:       map[string]string{},
	}

	if repoRoot != "" {
		envMap, lerr := LoadMiseLocalEnv(filepath.Join(repoRoot, "mise.local.toml"))
		if lerr == nil {
			ctx.LocalEnv = envMap
		} else if !os.IsNotExist(lerr) {
			ctx.MiseLocalErr = lerr
		}
	}
	if !ctx.DryRun && ctx.WSHome != "" {
		if err := MigrateLegacyWSHome(ctx); err != nil {
			return nil, fmt.Errorf("migrate legacy ws home: %w", err)
		}
	}
	if cfgPath := ctx.WSConfigPath(); cfgPath != "" {
		cfg, cerr := LoadWSConfig(cfgPath)
		if cerr != nil {
			ctx.ConfigErr = cerr
		}
		if cfg != nil {
			ctx.Config = cfg
		}
	}

	if opts.RepoRoot == "" && ctx.Config != nil {
		var cfgCand string
		if rawRoot := strings.TrimSpace(ctx.Config.Workspace.RepoRoot); rawRoot != "" {
			if exp := ctx.ExpandHome(rawRoot); isWorkstationRoot(exp) {
				cfgCand = exp
			}
		} else if ctx.RepoRoot == "" {
			for _, k := range []string{"WS_DIR", "WS_REPO"} {
				if rawEnv := strings.TrimSpace(ctx.Config.Env[k]); rawEnv != "" {
					if exp := ctx.ExpandHome(rawEnv); isWorkstationRoot(exp) {
						cfgCand = exp
						break
					}
				}
			}
		}
		if cfgCand != "" {
			cfgCand = absOrClean(cfgCand)
			if cfgCand != ctx.RepoRoot {
				ctx.RepoRoot = cfgCand
				if envMap, lerr := LoadMiseLocalEnv(filepath.Join(ctx.RepoRoot, "mise.local.toml")); lerr == nil {
					ctx.LocalEnv = envMap
					ctx.MiseLocalErr = nil
				} else if !os.IsNotExist(lerr) {
					ctx.MiseLocalErr = lerr
				}
			}
		}
	}
	return ctx, nil
}

func (c *Context) ReloadLocalEnv() error {
	if c == nil || c.RepoRoot == "" {
		return nil
	}
	envMap, err := LoadMiseLocalEnv(filepath.Join(c.RepoRoot, "mise.local.toml"))
	c.cfgLock()
	defer c.cfgUnlock()
	if err != nil {
		if os.IsNotExist(err) {
			c.LocalEnv = map[string]string{}
			c.MiseLocalErr = nil
			return nil
		}
		c.MiseLocalErr = err
		return err
	}
	c.LocalEnv = envMap
	c.MiseLocalErr = nil
	return nil
}

func (c *Context) ChildTTY() bool {
	return c != nil && !c.NonInteractive && !c.JSON
}

func (c *Context) WSBaseDir() string {
	if c == nil {
		return ""
	}
	if strings.TrimSpace(c.WSHome) != "" {
		return filepath.Clean(c.WSHome)
	}
	if strings.TrimSpace(c.HomeDir) != "" {
		return filepath.Join(filepath.Clean(c.HomeDir), ".ws")
	}
	if strings.TrimSpace(c.StateDir) != "" {
		return filepath.Join(filepath.Clean(c.StateDir), "ws")
	}
	return ""
}

func (c *Context) WSDBPath() string { return filepath.Join(c.WSBaseDir(), "ws.db") }

func (c *Context) WSKeyPath() string { return filepath.Join(c.WSBaseDir(), "db.key") }

func (c *Context) WSLedgerPath() string { return filepath.Join(c.WSBaseDir(), "ledger.json") }

func (c *Context) WSThemePath() string { return filepath.Join(c.WSBaseDir(), "theme") }

func (c *Context) WSBackupsDir() string { return filepath.Join(c.WSBaseDir(), "backups") }

func (c *Context) WSExportsDir() string { return filepath.Join(c.WSBaseDir(), "exports") }

func (c *Context) WSCacheDir() string { return filepath.Join(c.WSBaseDir(), "cache") }

func (c *Context) WSLogsDir() string { return filepath.Join(c.WSBaseDir(), "logs") }

func (c *Context) WSDigestPath() string { return filepath.Join(c.WSBaseDir(), "cache", "digest.json") }

func (c *Context) cfgRLock() {
	if c != nil && c.cfgMu != nil {
		c.cfgMu.RLock()
	}
}

func (c *Context) cfgRUnlock() {
	if c != nil && c.cfgMu != nil {
		c.cfgMu.RUnlock()
	}
}

func (c *Context) cfgLock() {
	if c != nil && c.cfgMu != nil {
		c.cfgMu.Lock()
	}
}

func (c *Context) cfgUnlock() {
	if c != nil && c.cfgMu != nil {
		c.cfgMu.Unlock()
	}
}

func (c *Context) EffectivePreset() string {
	if c.PresetOverride != "" {
		return c.PresetOverride
	}
	c.cfgRLock()
	if c.Config != nil && strings.TrimSpace(c.Config.Workspace.Preset) != "" {
		p := strings.TrimSpace(c.Config.Workspace.Preset)
		c.cfgRUnlock()
		return p
	}
	c.cfgRUnlock()
	if ledger, err := LoadLedger(c.WSBaseDir()); err == nil && ledger.ActivePreset != "" {
		return ledger.ActivePreset
	}
	return c.Host.DefaultPreset
}

func (c *Context) LookupEnv(key string) string {
	if c != nil {
		c.cfgRLock()
		var ov string
		var hasOv bool
		if c.Config != nil && c.Config.Env != nil {
			ov, hasOv = c.Config.Env[key]
		}
		c.cfgRUnlock()
		if hasOv && ov != "" {
			return ov
		}
	}
	if v := os.Getenv(key); v != "" {
		return v
	}
	if c != nil {
		c.cfgRLock()
		v := c.LocalEnv[key]
		c.cfgRUnlock()
		return v
	}
	return ""
}

func (c *Context) LookupEnvExact(key string) (string, bool) {
	if c != nil {
		c.cfgRLock()
		var ov string
		var hasOv bool
		if c.Config != nil && c.Config.Env != nil {
			ov, hasOv = c.Config.Env[key]
		}
		c.cfgRUnlock()
		if hasOv {
			return ov, true
		}
	}
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	if c != nil {
		c.cfgRLock()
		v, ok := c.LocalEnv[key]
		c.cfgRUnlock()
		return v, ok
	}
	return "", false
}

func (c *Context) ExpandHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		return filepath.Clean(c.HomeDir)
	}
	if strings.HasPrefix(p, "~/") {
		if c.HomeDir == "" {
			return p
		}
		return filepath.Clean(filepath.Join(c.HomeDir, p[2:]))
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return p
}

func (c *Context) CollapseHome(p string) string {
	if c.HomeDir == "" {
		return p
	}
	if p == c.HomeDir {
		return "~"
	}
	prefix := c.HomeDir + string(filepath.Separator)
	if strings.HasPrefix(p, prefix) {
		return "~/" + filepath.ToSlash(p[len(prefix):])
	}
	return p
}

func absOrClean(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

func FindRepoRoot(explicit, home string) string {
	if explicit != "" {
		if isWorkstationRoot(explicit) {
			return absOrClean(explicit)
		}
		return ""
	}

	candidates := []string{}
	for _, envKey := range []string{"WS_DIR", "WS_REPO", "MISE_PROJECT_ROOT"} {
		if env := strings.TrimSpace(os.Getenv(envKey)); env != "" {
			candidates = append(candidates, env)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, walkUpCandidates(cwd)...)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidates = append(candidates, walkUpCandidates(filepath.Dir(exe))...)
	}
	if home != "" {
		candidates = append(candidates, homeRepoCandidates(home)...)
	}

	seen := make(map[string]bool, len(candidates))
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		cleaned := filepath.Clean(dir)
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		if isWorkstationRoot(cleaned) {
			return absOrClean(cleaned)
		}
	}
	return ""
}

var repoRootLinkProbes = []string{
	".zshenv",
	".zshrc",
	".bashrc",
	".vimrc",
	filepath.Join(".config", "shell"),
	filepath.Join(".config", "tmux"),
	filepath.Join(".config", "nvim"),
	filepath.Join(".config", "git"),
	filepath.Join(".config", "bat"),
	filepath.Join(".config", "starship"),
	filepath.Join(".config", "fzf"),
	filepath.Join(".config", "gum"),
}

func homeRepoCandidates(home string) []string {
	cleanHome := absOrClean(home)
	if cleanHome == "" {
		return nil
	}
	var out []string

	for _, rel := range repoRootLinkProbes {
		linkPath := filepath.Join(cleanHome, rel)
		if resolved, err := filepath.EvalSymlinks(linkPath); err == nil {
			resolvedClean := filepath.Clean(resolved)
			if resolvedClean != filepath.Clean(linkPath) {
				out = append(out, walkUpCandidates(filepath.Dir(resolvedClean))...)
			}
		}
	}

	out = append(out,
		filepath.Join(cleanHome, "workstation"),
		filepath.Join(cleanHome, "dotfiles"),
		filepath.Join(cleanHome, "code", "workstation"),
		filepath.Join(cleanHome, "src", "workstation"),
	)

	if rawRoot := strings.TrimSpace(os.Getenv("WS_CODE_ROOT")); rawRoot != "" {
		exp := rawRoot
		if rawRoot == "~" {
			exp = cleanHome
		} else if strings.HasPrefix(rawRoot, "~/") {
			exp = filepath.Join(cleanHome, rawRoot[2:])
		}
		expClean := filepath.Clean(exp)
		if isPathWithinHome(expClean, cleanHome) {
			out = append(out, filepath.Join(expClean, "workstation"))
			out = append(out, depthOneWorkstationCandidates(expClean)...)
		}
	}

	zones := []string{"personal"}
	seenZones := map[string]bool{"personal": true}
	for _, line := range strings.FieldsFunc(os.Getenv("WS_ZONES"), func(r rune) bool {
		return r == ',' || r == '\n'
	}) {
		spec := strings.TrimSpace(line)
		if spec == "" {
			continue
		}
		name, _, _ := strings.Cut(spec, "|")
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." || seenZones[name] {
			continue
		}
		seenZones[name] = true
		zones = append(zones, name)
	}
	for _, z := range zones {
		zoneCode := filepath.Join(cleanHome, z, "code")
		out = append(out, filepath.Join(zoneCode, "workstation"))
		out = append(out, depthOneWorkstationCandidates(zoneCode)...)
	}

	return out
}

func depthOneWorkstationCandidates(codeRoot string) []string {
	entries, err := os.ReadDir(codeRoot)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		out = append(out, filepath.Join(codeRoot, entry.Name(), "workstation"))
	}
	return out
}

func isPathWithinHome(path, home string) bool {
	if path == home {
		return true
	}
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func walkUpCandidates(start string) []string {
	var out []string
	cur := filepath.Clean(start)
	for cur != "" && cur != string(filepath.Separator) {
		out = append(out, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return out
}

func isWorkstationRoot(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	for _, rel := range []string{
		filepath.Join("themes", "registry"),
		filepath.Join("links", "common.links"),
	} {
		fi, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || fi.IsDir() {
			return false
		}
	}
	return true
}

func DetectHost() HostInfo {
	headless := os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == ""
	return DetectHostFromEnv("/etc/os-release", runtime.GOOS, runtime.GOARCH, headless, lookPathExists)
}

func lookPathExists(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func DetectHostFromEnv(osReleasePath, goos, goarch string, headless bool, hasCmd func(string) bool) HostInfo {
	info := HostInfo{
		OS:   goos,
		Arch: goarch,
	}

	if goos == "darwin" {
		info.Distro = "macos"
		info.Headless = false
		for _, pm := range []string{"brew", "mise", "gh"} {
			if hasCmd(pm) {
				info.PkgManagers = append(info.PkgManagers, pm)
			}
		}
		info.DefaultPreset = "mac-workstation"
		return info
	}

	id, like := parseOSRelease(osReleasePath)
	info.Distro = id
	info.DistroLike = like
	info.Headless = headless

	for _, pm := range []string{"paru", "pacman", "apt-get", "apt", "dnf", "mise", "gh"} {
		if hasCmd(pm) {
			info.PkgManagers = append(info.PkgManagers, pm)
		}
	}

	switch {
	case id == "cachyos":
		info.DefaultPreset = "cachyos-desktop"
	case id == "arch" || slices.Contains(like, "arch"):
		info.DefaultPreset = "arch-desktop"
	case id == "ubuntu" && headless:
		info.DefaultPreset = "ubuntu-runner"
	case (id == "debian" || slices.Contains(like, "debian")) && headless:
		info.DefaultPreset = "debian-server"
	case id == "ubuntu" || id == "debian" || slices.Contains(like, "debian"):
		info.DefaultPreset = "dev-cli"
	default:
		info.DefaultPreset = "base"
	}
	return info
}

func parseOSRelease(path string) (string, []string) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	var id string
	var like []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			id = strings.ToLower(v)
		case "ID_LIKE":
			for _, part := range strings.Fields(strings.ToLower(v)) {
				like = append(like, part)
			}
		}
	}
	return id, like
}
