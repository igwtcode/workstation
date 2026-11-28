package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type TUIConfig struct {
	StartTab       string `toml:"start_tab,omitempty" json:"startTab,omitempty"`
	DefaultZone    string `toml:"default_zone,omitempty" json:"defaultZone,omitempty"`
	DefaultSplit   string `toml:"default_split,omitempty" json:"defaultSplit,omitempty"`
	BorderedBar    string `toml:"bordered_bar,omitempty" json:"borderedBar,omitempty"`
	Mouse          *bool  `toml:"mouse,omitempty" json:"mouse,omitempty"`
	AutoReload     *bool  `toml:"auto_reload,omitempty" json:"autoReload,omitempty"`
	AutoReloadSecs int    `toml:"auto_reload_secs,omitempty" json:"autoReloadSecs,omitempty"`
	GHRefreshMins  int    `toml:"gh_refresh_mins,omitempty" json:"ghRefreshMins,omitempty"`
	Theme          string `toml:"theme,omitempty" json:"theme,omitempty"`
}

type WorkspaceConfig struct {
	Preset   string `toml:"preset,omitempty" json:"preset,omitempty"`
	RepoRoot string `toml:"repo_root,omitempty" json:"repoRoot,omitempty"`
}

type FleetConfig struct {
	IncludeEnv *bool `toml:"include_env,omitempty" json:"includeEnv,omitempty"`
	IncludeDB  *bool `toml:"include_db,omitempty" json:"includeDb,omitempty"`
}

type WSConfig struct {
	TUI       TUIConfig         `toml:"tui,omitempty" json:"tui"`
	Workspace WorkspaceConfig   `toml:"workspace,omitempty" json:"workspace"`
	Fleet     FleetConfig       `toml:"fleet,omitempty" json:"fleet"`
	Env       map[string]string `toml:"env,omitempty" json:"env,omitempty"`
}

type SettingEntry struct {
	Key          string   `json:"key"`
	Category     string   `json:"category"`
	Kind         string   `json:"kind"`
	Value        string   `json:"value"`
	DefaultValue string   `json:"defaultValue"`
	Source       string   `json:"source"`
	Options      []string `json:"options,omitempty"`
	Description  string   `json:"description"`
	Overridden   bool     `json:"overridden"`
	ReadOnly     bool     `json:"readOnly"`
}

type WatchedFile struct {
	Path        string `json:"path"`
	Label       string `json:"label"`
	Exists      bool   `json:"exists"`
	SizeBytes   int64  `json:"sizeBytes"`
	ModTime     int64  `json:"modTime"`
	Fingerprint string `json:"fingerprint"`
}

func BoolVal(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

func cloneWSConfig(src *WSConfig) *WSConfig {
	if src == nil {
		return &WSConfig{Env: map[string]string{}}
	}
	cp := *src
	if src.TUI.Mouse != nil {
		v := *src.TUI.Mouse
		cp.TUI.Mouse = &v
	}
	if src.TUI.AutoReload != nil {
		v := *src.TUI.AutoReload
		cp.TUI.AutoReload = &v
	}
	if src.Fleet.IncludeEnv != nil {
		v := *src.Fleet.IncludeEnv
		cp.Fleet.IncludeEnv = &v
	}
	if src.Fleet.IncludeDB != nil {
		v := *src.Fleet.IncludeDB
		cp.Fleet.IncludeDB = &v
	}
	cp.Env = make(map[string]string, len(src.Env))
	maps.Copy(cp.Env, src.Env)
	return &cp
}

func wsConfigEqual(a, b *WSConfig) bool {
	ca := cloneWSConfig(a)
	cb := cloneWSConfig(b)
	return ca.TUI.StartTab == cb.TUI.StartTab &&
		ca.TUI.DefaultZone == cb.TUI.DefaultZone &&
		ca.TUI.DefaultSplit == cb.TUI.DefaultSplit &&
		ca.TUI.BorderedBar == cb.TUI.BorderedBar &&
		BoolVal(ca.TUI.Mouse, true) == BoolVal(cb.TUI.Mouse, true) &&
		BoolVal(ca.TUI.AutoReload, true) == BoolVal(cb.TUI.AutoReload, true) &&
		ca.TUI.AutoReloadSecs == cb.TUI.AutoReloadSecs &&
		ca.TUI.GHRefreshMins == cb.TUI.GHRefreshMins &&
		ca.TUI.Theme == cb.TUI.Theme &&
		ca.Workspace.Preset == cb.Workspace.Preset &&
		ca.Workspace.RepoRoot == cb.Workspace.RepoRoot &&
		BoolVal(ca.Fleet.IncludeEnv, false) == BoolVal(cb.Fleet.IncludeEnv, false) &&
		BoolVal(ca.Fleet.IncludeDB, false) == BoolVal(cb.Fleet.IncludeDB, false) &&
		maps.Equal(ca.Env, cb.Env)
}

func LoadWSConfig(path string) (*WSConfig, error) {
	if strings.TrimSpace(path) == "" {
		return &WSConfig{Env: map[string]string{}}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &WSConfig{Env: map[string]string{}}, nil
		}
		return &WSConfig{Env: map[string]string{}}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg WSConfig
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := toml.Unmarshal(raw, &cfg); err != nil {
			return &WSConfig{Env: map[string]string{}}, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	return &cfg, nil
}

func SaveWSConfig(path string, cfg *WSConfig) error {
	cleanPath := filepath.Clean(path)
	if strings.TrimSpace(cleanPath) == "" || cleanPath == "." {
		return fmt.Errorf("config path is required")
	}
	payload := cloneWSConfig(cfg)
	if len(payload.Env) == 0 {
		payload.Env = nil
	}
	raw, err := toml.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return WriteAtomicFile(cleanPath, raw, 0o600)
}

func (c *Context) WSConfigPath() string {
	if c == nil || c.WSBaseDir() == "" {
		return ""
	}
	return filepath.Join(c.WSBaseDir(), "config.toml")
}

func (c *Context) EffectiveConfig() WSConfig {
	var base *WSConfig
	if c != nil {
		c.cfgRLock()
		base = cloneWSConfig(c.Config)
		c.cfgRUnlock()
	} else {
		base = cloneWSConfig(nil)
	}

	if strings.TrimSpace(base.TUI.StartTab) == "" {
		base.TUI.StartTab = "overview"
	}
	if strings.TrimSpace(base.TUI.DefaultSplit) == "" {
		base.TUI.DefaultSplit = "auto"
	}
	if strings.TrimSpace(base.TUI.BorderedBar) == "" {
		base.TUI.BorderedBar = "auto"
	}
	if base.TUI.Mouse == nil {
		v := true
		base.TUI.Mouse = &v
	}
	if base.TUI.AutoReload == nil {
		v := true
		base.TUI.AutoReload = &v
	}
	if base.TUI.AutoReloadSecs <= 0 {
		base.TUI.AutoReloadSecs = 2
	}
	if base.TUI.GHRefreshMins <= 0 {
		base.TUI.GHRefreshMins = 60
	}
	if base.Fleet.IncludeEnv == nil {
		v := false
		base.Fleet.IncludeEnv = &v
	}
	if base.Fleet.IncludeDB == nil {
		v := false
		base.Fleet.IncludeDB = &v
	}
	return *base
}

var (
	validStartTabs     = []string{"overview", "projects", "packages", "env", "fleet", "workstation", "output"}
	legacyStartTabs    = []string{"mise", "system", "doctor", "settings", "secrets", "pkgs", "presets", "themes", "cleanup", "history", "vault", "filesync", "repos"}
	validDefaultSplits = []string{"auto", "off", "vertical", "horizontal"}
	validBorderedBars  = []string{"auto", "always", "compact"}
)

var allowedEnvOverrideKeys = map[string]struct{}{
	"WS_DIR":              {},
	"WS_CODE_ROOT":        {},
	"WS_ZONES":            {},
	"WS_FORGES":           {},
	"WS_AWS_DEFAULT_ZONE": {},
	"WS_OP_VAULT":         {},
	"WS_OP_FILES":         {},
	"WS_OP_SSH_DOC":       {},
	"WS_OP_SECRETS_DOC":   {},
	"WS_OP_ENVFILES_DOC":  {},
	"WS_SYNC_PATHS":       {},
}

func validateEnumValue(key, raw string, opts []string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	for _, opt := range opts {
		if v == opt {
			return opt, nil
		}
	}
	return "", fmt.Errorf("invalid value %q for %s (expected %s)", raw, key, strings.Join(opts, "|"))
}

func parseStrictBool(key, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q for %s (expected true or false)", raw, key)
	}
}

func validateSafeSlugOrEmpty(key, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if strings.ContainsAny(v, "/\\\r\n\x00") || strings.Contains(v, "..") {
		return "", fmt.Errorf("unsafe value %q for %s", raw, key)
	}
	return v, nil
}

func (c *Context) SetConfigOverride(key, value string) error {
	if c == nil {
		return fmt.Errorf("context is required")
	}
	k := strings.TrimSpace(key)
	if strings.HasPrefix(k, "path.") {
		return fmt.Errorf("setting %q is read-only", k)
	}

	c.cfgLock()
	defer c.cfgUnlock()

	next := cloneWSConfig(c.Config)
	switch k {
	case "tui.start_tab":
		v, err := validateEnumValue(k, value, validStartTabs)
		if err != nil {
			if lv, lerr := validateEnumValue(k, value, legacyStartTabs); lerr == nil {
				v = lv
			} else {
				return err
			}
		}
		next.TUI.StartTab = v
	case "tui.default_zone":
		v, err := validateSafeSlugOrEmpty(k, value)
		if err != nil {
			return err
		}
		next.TUI.DefaultZone = v
	case "tui.default_split":
		v, err := validateEnumValue(k, value, validDefaultSplits)
		if err != nil {
			return err
		}
		next.TUI.DefaultSplit = v
	case "tui.bordered_bar":
		v, err := validateEnumValue(k, value, validBorderedBars)
		if err != nil {
			return err
		}
		next.TUI.BorderedBar = v
	case "tui.mouse":
		b, err := parseStrictBool(k, value)
		if err != nil {
			return err
		}
		next.TUI.Mouse = &b
	case "tui.auto_reload":
		b, err := parseStrictBool(k, value)
		if err != nil {
			return err
		}
		next.TUI.AutoReload = &b
	case "tui.auto_reload_secs":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 || n > 60 {
			return fmt.Errorf("invalid value %q for %s (expected integer 1..60)", value, k)
		}
		next.TUI.AutoReloadSecs = n
	case "tui.gh_refresh_mins":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 5 || n > 720 {
			return fmt.Errorf("invalid value %q for %s (expected integer 5..720)", value, k)
		}
		next.TUI.GHRefreshMins = n
	case "tui.theme":
		v, err := validateSafeSlugOrEmpty(k, value)
		if err != nil {
			return err
		}
		if v == "" {
			return fmt.Errorf("tui.theme cannot be empty (use unset/reset to clear)")
		}
		next.TUI.Theme = v
	case "workspace.preset":
		v, err := validateSafeSlugOrEmpty(k, value)
		if err != nil {
			return err
		}
		if v == "" {
			return fmt.Errorf("workspace.preset cannot be empty (use unset/reset to clear)")
		}
		next.Workspace.Preset = v
	case "workspace.repo_root":
		v := strings.TrimSpace(value)
		if strings.ContainsRune(v, '\x00') {
			return fmt.Errorf("setting %q contains invalid NUL byte", k)
		}
		if v != "" {
			expanded := c.ExpandHome(v)
			if !isWorkstationRoot(expanded) {
				return fmt.Errorf("invalid workstation repository %q: missing themes/registry or links/common.links", expanded)
			}
			c.RepoRoot = absOrClean(expanded)
		}
		next.Workspace.RepoRoot = v
	case "fleet.include_env":
		b, err := parseStrictBool(k, value)
		if err != nil {
			return err
		}
		next.Fleet.IncludeEnv = &b
	case "fleet.include_db":
		b, err := parseStrictBool(k, value)
		if err != nil {
			return err
		}
		next.Fleet.IncludeDB = &b
	default:
		if _, ok := allowedEnvOverrideKeys[k]; !ok {
			return fmt.Errorf("unknown setting key %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("setting %q contains invalid NUL byte", k)
		}
		next.Env[k] = strings.TrimSpace(value)
	}

	if !c.DryRun && c.WSConfigPath() != "" {
		if err := SaveWSConfig(c.WSConfigPath(), next); err != nil {
			return err
		}
	}
	c.Config = next
	c.ConfigErr = nil
	return nil
}

func (c *Context) ResetConfigOverride(key string) error {
	if c == nil {
		return fmt.Errorf("context is required")
	}
	k := strings.TrimSpace(key)
	if strings.HasPrefix(k, "path.") {
		return fmt.Errorf("setting %q is read-only", k)
	}

	c.cfgLock()
	defer c.cfgUnlock()

	next := cloneWSConfig(c.Config)
	switch k {
	case "tui.start_tab":
		next.TUI.StartTab = ""
	case "tui.default_zone":
		next.TUI.DefaultZone = ""
	case "tui.default_split":
		next.TUI.DefaultSplit = ""
	case "tui.bordered_bar":
		next.TUI.BorderedBar = ""
	case "tui.mouse":
		next.TUI.Mouse = nil
	case "tui.auto_reload":
		next.TUI.AutoReload = nil
	case "tui.auto_reload_secs":
		next.TUI.AutoReloadSecs = 0
	case "tui.gh_refresh_mins":
		next.TUI.GHRefreshMins = 0
	case "tui.theme":
		next.TUI.Theme = ""
	case "workspace.preset":
		next.Workspace.Preset = ""
	case "workspace.repo_root":
		next.Workspace.RepoRoot = ""
	case "fleet.include_env":
		next.Fleet.IncludeEnv = nil
	case "fleet.include_db":
		next.Fleet.IncludeDB = nil
	default:
		if _, ok := allowedEnvOverrideKeys[k]; !ok {
			return fmt.Errorf("unknown setting key %q", key)
		}
		delete(next.Env, k)
	}

	if !c.DryRun && c.WSConfigPath() != "" {
		if err := SaveWSConfig(c.WSConfigPath(), next); err != nil {
			return err
		}
	}
	c.Config = next
	c.ConfigErr = nil
	return nil
}

func (c *Context) ReloadConfig() ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("context is required")
	}
	c.cfgLock()
	defer c.cfgUnlock()

	var changed []string
	var firstErr error

	if cfgPath := c.WSConfigPath(); cfgPath != "" {
		newCfg, err := LoadWSConfig(cfgPath)
		c.ConfigErr = err
		if err != nil {
			firstErr = err
		} else {
			if !wsConfigEqual(c.Config, newCfg) {
				changed = append(changed, "config.toml")
			}
			c.Config = newCfg
		}
	}

	if c.RepoRoot != "" {
		envMap, lerr := LoadMiseLocalEnv(filepath.Join(c.RepoRoot, "mise.local.toml"))
		switch {
		case lerr == nil:
			if !maps.Equal(c.LocalEnv, envMap) {
				changed = append(changed, "mise.local.toml")
			}
			c.LocalEnv = envMap
			c.MiseLocalErr = nil
		case errors.Is(lerr, os.ErrNotExist):
			if len(c.LocalEnv) > 0 {
				changed = append(changed, "mise.local.toml")
			}
			c.LocalEnv = map[string]string{}
			c.MiseLocalErr = nil
		default:
			c.MiseLocalErr = lerr
			if firstErr == nil {
				firstErr = lerr
			}
		}
	}

	return changed, firstErr
}

func (c *Context) WatchedConfigFiles() []WatchedFile {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	var targets [][2]string
	addTarget := func(label, p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			targets = append(targets, [2]string{label, clean})
		}
	}

	if wsBase := c.WSBaseDir(); wsBase != "" {
		addTarget("config.toml", filepath.Join(wsBase, "config.toml"))
		addTarget("theme", filepath.Join(wsBase, "theme"))
		addTarget("ledger.json", filepath.Join(wsBase, "ledger.json"))
	}
	if c.RepoRoot != "" {
		addTarget("mise.local.toml", filepath.Join(c.RepoRoot, "mise.local.toml"))
		addTarget("presets.toml", filepath.Join(c.RepoRoot, "presets.toml"))
		addTarget("tools.toml", filepath.Join(c.RepoRoot, "tools.toml"))
	}
	if c.HomeDir != "" {
		zoneSeen := map[string]bool{}
		for _, row := range ParsePipeLines(c.LookupEnv("WS_ZONES")) {
			if len(row) > 0 && row[0] != "" {
				zoneSeen[row[0]] = true
			}
		}
		for _, defZone := range []string{"personal", "work"} {
			if _, stErr := os.Stat(filepath.Join(c.HomeDir, defZone, "mise.toml")); stErr == nil {
				zoneSeen[defZone] = true
			}
		}
		var zNames []string
		for z := range zoneSeen {
			zNames = append(zNames, z)
		}
		sort.Strings(zNames)
		for _, z := range zNames {
			addTarget("zone:"+z+"/mise.toml", filepath.Join(c.HomeDir, z, "mise.toml"))
		}
	}

	out := make([]WatchedFile, 0, len(targets))
	for _, t := range targets {
		wf := WatchedFile{
			Label:       t[0],
			Path:        t[1],
			Fingerprint: "missing",
		}
		if fi, err := os.Stat(t[1]); err == nil && !fi.IsDir() {
			wf.Exists = true
			wf.SizeBytes = fi.Size()
			wf.ModTime = fi.ModTime().Unix()
			wf.Fingerprint = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
		}
		out = append(out, wf)
	}
	return out
}

func (c *Context) ConfigFingerprint() (string, []WatchedFile) {
	files := c.WatchedConfigFiles()
	h := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", f.Path, f.Fingerprint)
	}
	return hex.EncodeToString(h.Sum(nil))[:24], files
}

func (c *Context) ListSettings(themeIDs, presetIDs, zoneNames []string) []SettingEntry {
	if c == nil {
		return nil
	}
	c.cfgRLock()
	cfg := cloneWSConfig(c.Config)
	localCopy := maps.Clone(c.LocalEnv)
	c.cfgRUnlock()

	eff := c.EffectiveConfig()
	ledger, _ := LoadLedger(c.WSBaseDir())
	legacyTheme := ReadLegacyTheme(c.WSBaseDir())

	envLookupWithSource := func(key, defVal string) (val, source string, overridden bool) {
		if v, ok := cfg.Env[key]; ok {
			return v, "override", true
		}
		if v, ok := os.LookupEnv(key); ok && v != "" {
			return v, "env", false
		}
		if v := localCopy[key]; v != "" {
			return v, "mise.local", false
		}
		if defVal != "" {
			return defVal, "default", false
		}
		return "", "unset", false
	}

	presetVal := c.Host.DefaultPreset
	presetSrc := "default"
	presetOver := false
	if ledger != nil && ledger.ActivePreset != "" {
		presetVal = ledger.ActivePreset
		presetSrc = "ledger"
	}
	if strings.TrimSpace(cfg.Workspace.Preset) != "" {
		presetVal = strings.TrimSpace(cfg.Workspace.Preset)
		presetSrc = "override"
		presetOver = true
	}
	if strings.TrimSpace(c.PresetOverride) != "" {
		presetVal = strings.TrimSpace(c.PresetOverride)
		presetSrc = "flag"
	}

	themeVal := "gruvbox-dark"
	themeSrc := "default"
	themeOver := false
	if isValidLegacyThemeID(legacyTheme) {
		themeVal = legacyTheme
		themeSrc = "theme-file"
	}
	if ledger != nil && ledger.ActiveTheme != "" {
		themeVal = ledger.ActiveTheme
		themeSrc = "ledger"
	}
	if strings.TrimSpace(cfg.TUI.Theme) != "" {
		themeVal = strings.TrimSpace(cfg.TUI.Theme)
		themeSrc = "override"
		themeOver = true
	}

	zoneOpts := []string{"all"}
	seenZone := map[string]bool{"all": true}
	for _, z := range zoneNames {
		zt := strings.TrimSpace(z)
		if zt != "" && !seenZone[zt] {
			seenZone[zt] = true
			zoneOpts = append(zoneOpts, zt)
		}
	}

	startTabSrc := "default"
	if cfg.TUI.StartTab != "" {
		startTabSrc = "override"
	}
	defaultZoneVal := "all"
	defaultZoneSrc := "default"
	if strings.TrimSpace(cfg.TUI.DefaultZone) != "" {
		defaultZoneVal = strings.TrimSpace(cfg.TUI.DefaultZone)
		defaultZoneSrc = "override"
	}
	splitSrc := "default"
	if cfg.TUI.DefaultSplit != "" {
		splitSrc = "override"
	}
	barSrc := "default"
	if cfg.TUI.BorderedBar != "" {
		barSrc = "override"
	}
	mouseSrc := "default"
	if cfg.TUI.Mouse != nil {
		mouseSrc = "override"
	}
	reloadSrc := "default"
	if cfg.TUI.AutoReload != nil {
		reloadSrc = "override"
	}
	reloadSecsSrc := "default"
	if cfg.TUI.AutoReloadSecs > 0 {
		reloadSecsSrc = "override"
	}
	ghMinsSrc := "default"
	if cfg.TUI.GHRefreshMins > 0 {
		ghMinsSrc = "override"
	}
	fleetEnvSrc := "default"
	if cfg.Fleet.IncludeEnv != nil {
		fleetEnvSrc = "override"
	}
	fleetDBSrc := "default"
	if cfg.Fleet.IncludeDB != nil {
		fleetDBSrc = "override"
	}

	codeRootVal, codeRootSrc, codeRootOver := envLookupWithSource("WS_CODE_ROOT", "")
	zonesVal, zonesSrc, zonesOver := envLookupWithSource("WS_ZONES", "")
	forgesVal, forgesSrc, forgesOver := envLookupWithSource("WS_FORGES", "")
	awsZoneVal, awsZoneSrc, awsZoneOver := envLookupWithSource("WS_AWS_DEFAULT_ZONE", "")

	var awsZoneOpts []string
	for _, z := range zoneOpts {
		if z != "" && z != "all" {
			awsZoneOpts = append(awsZoneOpts, z)
		}
	}
	awsZoneKind := "string"
	if len(awsZoneOpts) > 0 {
		awsZoneKind = "enum"
	}

	opVaultVal, opVaultSrc, opVaultOver := envLookupWithSource("WS_OP_VAULT", "")
	opFilesVal, opFilesSrc, opFilesOver := envLookupWithSource("WS_OP_FILES", "")
	opSSHVal, opSSHSrc, opSSHOver := envLookupWithSource("WS_OP_SSH_DOC", "~/.ssh.zip")
	opSecVal, opSecSrc, opSecOver := envLookupWithSource("WS_OP_SECRETS_DOC", "workstation-secrets.zip")
	opEnvVal, opEnvSrc, opEnvOver := envLookupWithSource("WS_OP_ENVFILES_DOC", "workstation-envfiles-<zone>.zip")
	syncPathsVal, syncPathsSrc, syncPathsOver := envLookupWithSource("WS_SYNC_PATHS", "~/.ssh + WS_OP_FILES + private layer")

	zoneKind := "string"
	if len(zoneOpts) > 1 {
		zoneKind = "enum"
	}
	presetKind := "string"
	if len(presetIDs) > 0 {
		presetKind = "enum"
	}
	themeKind := "string"
	if len(themeIDs) > 0 {
		themeKind = "enum"
	}

	repoSrc := "unset"
	repoOver := false
	if strings.TrimSpace(cfg.Workspace.RepoRoot) != "" {
		repoSrc = "override"
		repoOver = true
	} else if c.RepoRoot != "" {
		repoSrc = "auto"
	}
	miseLocalPath := ""
	if c.RepoRoot != "" {
		miseLocalPath = filepath.Join(c.RepoRoot, "mise.local.toml")
	}

	return []SettingEntry{

		{Key: "tui.start_tab", Category: "tui", Kind: "enum", Value: eff.TUI.StartTab, DefaultValue: "overview", Source: startTabSrc, Options: validStartTabs, Overridden: cfg.TUI.StartTab != "", Description: "Initial TUI screen when launching ws tui without a tab argument"},
		{Key: "tui.default_zone", Category: "tui", Kind: zoneKind, Value: defaultZoneVal, DefaultValue: "all", Source: defaultZoneSrc, Options: zoneOpts, Overridden: cfg.TUI.DefaultZone != "", Description: "Default identity zone scope filter at TUI startup"},
		{Key: "tui.default_split", Category: "tui", Kind: "enum", Value: eff.TUI.DefaultSplit, DefaultValue: "auto", Source: splitSrc, Options: validDefaultSplits, Overridden: cfg.TUI.DefaultSplit != "", Description: "Preview split pane behavior (auto lets each panel decide)"},
		{Key: "tui.bordered_bar", Category: "tui", Kind: "enum", Value: eff.TUI.BorderedBar, DefaultValue: "auto", Source: barSrc, Options: validBorderedBars, Overridden: cfg.TUI.BorderedBar != "", Description: "Breadcrumb & Filter bar chrome height (auto=3 lines for h>=26)"},
		{Key: "tui.mouse", Category: "tui", Kind: "bool", Value: strconv.FormatBool(BoolVal(eff.TUI.Mouse, true)), DefaultValue: "true", Source: mouseSrc, Options: []string{"true", "false"}, Overridden: cfg.TUI.Mouse != nil, Description: "Capture mouse clicks and wheel scroll in TUI (set false for native terminal drag-select)"},
		{Key: "tui.auto_reload", Category: "tui", Kind: "bool", Value: strconv.FormatBool(BoolVal(eff.TUI.AutoReload, true)), DefaultValue: "true", Source: reloadSrc, Options: []string{"true", "false"}, Overridden: cfg.TUI.AutoReload != nil, Description: "Automatically watch and hot-reload config.toml, mise.local.toml, and theme files"},
		{Key: "tui.auto_reload_secs", Category: "tui", Kind: "int", Value: strconv.Itoa(eff.TUI.AutoReloadSecs), DefaultValue: "2", Source: reloadSecsSrc, Overridden: cfg.TUI.AutoReloadSecs > 0, Description: "Config watcher poll interval in seconds (1..60)"},
		{Key: "tui.gh_refresh_mins", Category: "tui", Kind: "int", Value: strconv.Itoa(eff.TUI.GHRefreshMins), DefaultValue: "60", Source: ghMinsSrc, Overridden: cfg.TUI.GHRefreshMins > 0, Description: "Background GitHub PRs/Issues refresh cadence in minutes (5..720)"},
		{Key: "tui.theme", Category: "tui", Kind: themeKind, Value: themeVal, DefaultValue: "gruvbox-dark", Source: themeSrc, Options: themeIDs, Overridden: themeOver, Description: "Active TUI color palette ID (live hot-swapped on save or auto-reload)"},

		{Key: "workspace.preset", Category: "workspace", Kind: presetKind, Value: presetVal, DefaultValue: c.Host.DefaultPreset, Source: presetSrc, Options: presetIDs, Overridden: presetOver, Description: "Active workstation package/link profile preset"},
		{Key: "workspace.repo_root", Category: "workspace", Kind: "path", Value: c.RepoRoot, DefaultValue: "", Source: repoSrc, Overridden: repoOver, Description: "Workstation repository checkout path (presets, themes, templates, links)"},
		{Key: "WS_CODE_ROOT", Category: "workspace", Kind: "path", Value: codeRootVal, DefaultValue: "", Source: codeRootSrc, Overridden: codeRootOver, Description: "Override root directory scanned for projects and zone repos"},
		{Key: "WS_ZONES", Category: "workspace", Kind: "string", Value: zonesVal, DefaultValue: "", Source: zonesSrc, Overridden: zonesOver, Description: "Identity zones, one per line (name|git_name|git_email[|signingkey[|aws_profile]])"},
		{Key: "WS_FORGES", Category: "workspace", Kind: "string", Value: forgesVal, DefaultValue: "", Source: forgesSrc, Overridden: forgesOver, Description: "Bulk-clone forges, one per line (kind|source|dest)"},
		{Key: "WS_AWS_DEFAULT_ZONE", Category: "workspace", Kind: awsZoneKind, Value: awsZoneVal, DefaultValue: "", Options: awsZoneOpts, Source: awsZoneSrc, Overridden: awsZoneOver, Description: "Default AWS identity zone when multiple profiles exist"},

		{Key: "WS_OP_VAULT", Category: "vault", Kind: "string", Value: opVaultVal, DefaultValue: "", Source: opVaultSrc, Overridden: opVaultOver, Description: "Target 1Password vault name (empty = account default)"},
		{Key: "WS_OP_FILES", Category: "vault", Kind: "string", Value: opFilesVal, DefaultValue: "", Source: opFilesSrc, Overridden: opFilesOver, Description: "One ~/-relative file or dir per line, backed up via 1Password & File Sync"},
		{Key: "WS_OP_SSH_DOC", Category: "vault", Kind: "string", Value: opSSHVal, DefaultValue: "~/.ssh.zip", Source: opSSHSrc, Overridden: opSSHOver, Description: "1Password document title used for ~/.ssh backup bundles"},
		{Key: "WS_OP_SECRETS_DOC", Category: "vault", Kind: "string", Value: opSecVal, DefaultValue: "workstation-secrets.zip", Source: opSecSrc, Overridden: opSecOver, Description: "1Password document title used for private workstation layer"},
		{Key: "WS_OP_ENVFILES_DOC", Category: "vault", Kind: "string", Value: opEnvVal, DefaultValue: "workstation-envfiles-<zone>.zip", Source: opEnvSrc, Overridden: opEnvOver, Description: "1Password document title template used for .env* code tree backups"},

		{Key: "WS_SYNC_PATHS", Category: "fleet", Kind: "string", Value: syncPathsVal, DefaultValue: "~/.ssh + WS_OP_FILES + private layer", Source: syncPathsSrc, Overridden: syncPathsOver, Description: "Extra paths for Tailscale file diff/sync (prefix with '=' to replace defaults)"},
		{Key: "fleet.include_env", Category: "fleet", Kind: "bool", Value: strconv.FormatBool(BoolVal(eff.Fleet.IncludeEnv, false)), DefaultValue: "false", Source: fleetEnvSrc, Options: []string{"true", "false"}, Overridden: cfg.Fleet.IncludeEnv != nil, Description: "Include zone/code .env* files by default in Fleet File Sync diffs"},
		{Key: "fleet.include_db", Category: "fleet", Kind: "bool", Value: strconv.FormatBool(BoolVal(eff.Fleet.IncludeDB, false)), DefaultValue: "false", Source: fleetDBSrc, Options: []string{"true", "false"}, Overridden: cfg.Fleet.IncludeDB != nil, Description: "Include encrypted ws.db snapshot by default in Fleet File Sync diffs"},

		{Key: "path.ws_home", Category: "paths", Kind: "readonly", Value: c.WSBaseDir(), DefaultValue: "~/.ws", Source: "readonly", ReadOnly: true, Description: "Unified workstation storage root (~/.ws or $WS_HOME)"},
		{Key: "path.config_file", Category: "paths", Kind: "readonly", Value: c.WSConfigPath(), DefaultValue: "~/.ws/config.toml", Source: "readonly", ReadOnly: true, Description: "Override configuration file managed by 6 Workstation → Settings and ws config"},
		{Key: "path.db_file", Category: "paths", Kind: "readonly", Value: c.WSDBPath(), DefaultValue: "~/.ws/ws.db", Source: "readonly", ReadOnly: true, Description: "SQLite database storing encrypted env snapshots, repos, and fleet state"},
		{Key: "path.repo_root", Category: "paths", Kind: "readonly", Value: c.RepoRoot, DefaultValue: "", Source: "readonly", ReadOnly: true, Description: "Detected workstation repository checkout root"},
		{Key: "path.mise_local", Category: "paths", Kind: "readonly", Value: miseLocalPath, DefaultValue: "", Source: "readonly", ReadOnly: true, Description: "Gitignored machine-local mise [env] configuration file"},
		{Key: "path.ledger", Category: "paths", Kind: "readonly", Value: c.WSLedgerPath(), DefaultValue: "~/.ws/ledger.json", Source: "readonly", ReadOnly: true, Description: "Managed templates/links/tools ledger with undo history"},
	}
}
