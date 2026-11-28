package shell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/pelletier/go-toml/v2"
)

type ZoneSpec struct {
	Name       string `json:"name"`
	GitName    string `json:"gitName"`
	GitEmail   string `json:"gitEmail"`
	SigningKey string `json:"signingKey,omitempty"`
	AWSProfile string `json:"awsProfile,omitempty"`
}

type ZoneStatus struct {
	Spec            ZoneSpec          `json:"spec"`
	Root            string            `json:"root"`
	CodeRoot        string            `json:"codeRoot"`
	RootExists      bool              `json:"rootExists"`
	ConfigPath      string            `json:"configPath"`
	ConfigExists    bool              `json:"configExists"`
	ConfigIsSymlink bool              `json:"configIsSymlink,omitempty"`
	Trusted         bool              `json:"trusted"`
	GitConfigPath   string            `json:"gitConfigPath"`
	GitConfigExists bool              `json:"gitConfigExists"`
	Env             map[string]string `json:"env,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
}

type WhoamiReport struct {
	CWD             string            `json:"cwd"`
	ActiveZone      string            `json:"activeZone"`
	EnvZone         string            `json:"envZone,omitempty"`
	PathZone        string            `json:"pathZone,omitempty"`
	ZoneMismatch    bool              `json:"zoneMismatch,omitempty"`
	MismatchDetail  string            `json:"mismatchDetail,omitempty"`
	GitName         string            `json:"gitName,omitempty"`
	GitEmail        string            `json:"gitEmail,omitempty"`
	GitSigningKey   string            `json:"gitSigningKey,omitempty"`
	GHConfigDir     string            `json:"ghConfigDir,omitempty"`
	AWSConfigFile   string            `json:"awsConfigFile,omitempty"`
	AWSCredsFile    string            `json:"awsSharedCredentialsFile,omitempty"`
	AWSProfile      string            `json:"awsProfile,omitempty"`
	KubeConfig      string            `json:"kubeconfig,omitempty"`
	ClaudeConfigDir string            `json:"claudeConfigDir,omitempty"`
	CodexHome       string            `json:"codexHome,omitempty"`
	CodeRoot        string            `json:"codeRoot,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
}

var (
	miseHomeTemplateRe       = regexp.MustCompile(`\{\{\s*env\.HOME\s*\}\}`)
	miseConfigRootTemplateRe = regexp.MustCompile(`\{\{\s*config_root\s*\}\}`)
)

func ParseZones(ctx *app.Context) ([]ZoneSpec, error) {
	if ctx == nil {
		return nil, nil
	}
	raw := ctx.LookupEnv("WS_ZONES")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	rows := app.ParsePipeLines(raw)
	specs := make([]ZoneSpec, 0, len(rows))
	seen := map[string]bool{}
	for _, parts := range rows {
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("malformed WS_ZONES entry %q (expected name|git_name|git_email[|signingkey[|aws_profile]])", strings.Join(parts, "|"))
		}
		name := parts[0]
		if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("invalid zone name %q: must be a single non-hidden directory under $HOME", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate zone %q in WS_ZONES", name)
		}
		seen[name] = true
		spec := ZoneSpec{
			Name:     name,
			GitName:  parts[1],
			GitEmail: parts[2],
		}
		if len(parts) >= 4 {
			spec.SigningKey = parts[3]
		}
		if len(parts) >= 5 {
			spec.AWSProfile = parts[4]
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

type zoneMiseDoc struct {
	Env map[string]any `toml:"env"`
}

func ReadZoneMiseEnv(zoneRoot string) (map[string]string, error) {
	cleanRoot := filepath.Clean(zoneRoot)
	homeDir := filepath.Dir(cleanRoot)

	if fi, err := os.Lstat(filepath.Join(cleanRoot, "mise.toml")); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if trustedEnv, ok := readZoneMiseEnvViaCLI(cleanRoot, homeDir); ok {
			return trustedEnv, nil
		}
	}

	return ReadZoneMiseEnvFast(cleanRoot)
}

func ReadZoneMiseEnvFast(zoneRoot string) (map[string]string, error) {
	cleanRoot := filepath.Clean(zoneRoot)
	homeDir := filepath.Dir(cleanRoot)

	out := map[string]string{}
	primary := filepath.Join(cleanRoot, "mise.toml")
	if err := readMiseEnvInto(primary, homeDir, cleanRoot, out); err != nil {
		return nil, err
	}

	localPath := filepath.Join(cleanRoot, "mise.local.toml")
	if _, err := os.Stat(localPath); err == nil {
		if err := readMiseEnvInto(localPath, homeDir, cleanRoot, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readZoneMiseEnvViaCLI(cleanRoot, homeDir string) (map[string]string, bool) {
	if !isZoneTrusted(&app.Context{HomeDir: homeDir}, cleanRoot) {
		return nil, false
	}
	miseBin, err := exec.LookPath("mise")
	if err != nil {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, miseBin, "env", "--json", "-C", cleanRoot)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var parsed map[string]string
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed) == 0 {
		return nil, false
	}
	return parsed, true
}

func readMiseEnvInto(path, homeDir, configRoot string, dst map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc zoneMiseDoc
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	for k, v := range doc.Env {
		var str string
		switch tv := v.(type) {
		case string:
			str = tv
		case int64, float64, bool:
			str = fmt.Sprintf("%v", tv)
		case map[string]any:
			if val, ok := tv["value"].(string); ok {
				str = val
			} else {
				continue
			}
		default:
			continue
		}
		expanded := miseHomeTemplateRe.ReplaceAllLiteralString(str, homeDir)
		expanded = miseConfigRootTemplateRe.ReplaceAllLiteralString(expanded, configRoot)
		dst[k] = expanded
	}
	return nil
}

func ZoneStatuses(ctx *app.Context) ([]ZoneStatus, error) {
	specs, err := ParseZones(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]ZoneStatus, 0, len(specs))
	for _, spec := range specs {
		root := filepath.Join(ctx.HomeDir, spec.Name)
		cfg := filepath.Join(root, "mise.toml")
		gitcfg := filepath.Join(root, ".gitconfig")

		st := ZoneStatus{
			Spec:          spec,
			Root:          root,
			CodeRoot:      filepath.Join(root, "code"),
			ConfigPath:    cfg,
			GitConfigPath: gitcfg,
		}
		if fi, err := os.Stat(root); err == nil && fi.IsDir() {
			st.RootExists = true
		}
		if fi, err := os.Lstat(cfg); err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				st.ConfigIsSymlink = true
				st.Warnings = append(st.Warnings, "mise.toml is a symlink; zone configs must be real files")
			} else {
				st.ConfigExists = true
			}
		}
		if fi, err := os.Stat(gitcfg); err == nil && !fi.IsDir() {
			st.GitConfigExists = true
		}
		if st.ConfigExists {
			if envMap, err := ReadZoneMiseEnv(root); err == nil {
				st.Env = envMap
				if cr := envMap["WS_CODE_ROOT"]; cr != "" {
					st.CodeRoot = cr
				}
			} else {
				st.Warnings = append(st.Warnings, err.Error())
			}
			st.Trusted = isZoneTrusted(ctx, root)
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

func isZoneTrusted(ctx *app.Context, zoneRoot string) bool {
	miseBin, err := exec.LookPath("mise")
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, miseBin, "trust", "--show", "-C", zoneRoot)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	collapsed := zoneRoot
	if ctx != nil {
		collapsed = ctx.CollapseHome(zoneRoot)
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == zoneRoot+": trusted" || line == collapsed+": trusted" {
			return true
		}
	}
	return false
}

func ApplyZones(ctx *app.Context, onlyZone string) ([]string, error) {
	specs, err := ParseZones(ctx)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("WS_ZONES is not set; configure zones in mise.local.toml [env]")
	}

	var targets []ZoneSpec
	if onlyZone != "" {
		for _, s := range specs {
			if s.Name == onlyZone {
				targets = append(targets, s)
				break
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("zone %q not found in WS_ZONES", onlyZone)
		}
	} else {
		targets = specs
	}

	var actions []string
	prefix := ""
	if ctx.DryRun {
		prefix = "[dry-run] "
	}

	for _, spec := range targets {
		zroot := filepath.Join(ctx.HomeDir, spec.Name)
		codeDir := filepath.Join(zroot, "code")
		cfgPath := filepath.Join(zroot, "mise.toml")
		gitCfgPath := filepath.Join(zroot, ".gitconfig")

		for _, dir := range []string{zroot, codeDir} {
			if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
				continue
			}
			if !ctx.DryRun {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return actions, err
				}
			}
			actions = append(actions, fmt.Sprintf("%screated %s/", prefix, ctx.CollapseHome(dir)))
		}

		seedPath := filepath.Join(ctx.RepoRoot, "config", "identity", spec.Name+".mise.toml")
		fi, lstatErr := os.Lstat(cfgPath)
		switch {
		case lstatErr == nil && fi.Mode()&os.ModeSymlink != 0:
			return actions, fmt.Errorf("%s is a symlink; zone mise.toml must be a real file", ctx.CollapseHome(cfgPath))
		case errors.Is(lstatErr, os.ErrNotExist):
			seedBytes, err := os.ReadFile(seedPath)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					actions = append(actions, fmt.Sprintf("%smissing seed %s", prefix, seedPath))
				} else {
					return actions, err
				}
			} else {
				if !ctx.DryRun {
					if err := app.WriteAtomicFile(cfgPath, seedBytes, 0o600); err != nil {
						return actions, err
					}
				}
				actions = append(actions, fmt.Sprintf("%sseeded %s", prefix, ctx.CollapseHome(cfgPath)))
			}
		case lstatErr != nil:
			return actions, lstatErr
		}

		if spec.AWSProfile != "" {
			localCfg := filepath.Join(zroot, "mise.local.toml")
			canWrite := true
			if existing, err := os.ReadFile(localCfg); err == nil {
				firstLine, _, _ := strings.Cut(string(existing), "\n")
				if !strings.Contains(firstLine, "generated by") {
					canWrite = false
					actions = append(actions, fmt.Sprintf("skipped hand-written %s", ctx.CollapseHome(localCfg)))
				}
			}
			if canWrite {
				localContent := fmt.Sprintf("# generated by ws identity from WS_ZONES\n\n[env]\nAWS_PROFILE = %q\n", spec.AWSProfile)
				if changed, err := writeIfChanged(localCfg, []byte(localContent), 0o600, ctx.DryRun); err != nil {
					return actions, err
				} else if changed {
					actions = append(actions, fmt.Sprintf("%swrote %s", prefix, ctx.CollapseHome(localCfg)))
				}
			}
		}

		canWriteGit := true
		if existing, err := os.ReadFile(gitCfgPath); err == nil {
			firstLine, _, _ := strings.Cut(string(existing), "\n")
			if !strings.Contains(firstLine, "generated by") {
				canWriteGit = false
				actions = append(actions, fmt.Sprintf("skipped hand-written %s", ctx.CollapseHome(gitCfgPath)))
			}
		}
		if canWriteGit {
			gitContent := renderZoneGitConfig(spec)
			if changed, err := writeIfChanged(gitCfgPath, []byte(gitContent), 0o644, ctx.DryRun); err != nil {
				return actions, err
			} else if changed {
				actions = append(actions, fmt.Sprintf("%swrote %s", prefix, ctx.CollapseHome(gitCfgPath)))
			}
		}

		if !ctx.DryRun {
			if _, err := os.Stat(cfgPath); err == nil && !isZoneTrusted(ctx, zroot) {
				if miseBin, err := exec.LookPath("mise"); err == nil {
					if out, err := exec.Command(miseBin, "trust", cfgPath).CombinedOutput(); err != nil {
						return actions, fmt.Errorf("mise trust %s: %w (%s)", cfgPath, err, strings.TrimSpace(string(out)))
					}
					actions = append(actions, fmt.Sprintf("trusted %s", ctx.CollapseHome(zroot)))
				}
			}
		}

		envMap, envErr := ReadZoneMiseEnv(zroot)
		if envErr != nil && ctx.DryRun {
			envMap = map[string]string{}
			_ = readMiseEnvInto(seedPath, ctx.HomeDir, zroot, envMap)
		}
		if len(envMap) > 0 {
			for _, k := range []string{"GH_CONFIG_DIR", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
				if dir := envMap[k]; dir != "" {
					if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
						if !ctx.DryRun {
							if err := os.MkdirAll(dir, 0o755); err != nil {
								return actions, err
							}
						}
						actions = append(actions, fmt.Sprintf("%screated %s/", prefix, ctx.CollapseHome(dir)))
					}
				}
			}
			for _, k := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE", "KUBECONFIG"} {
				if p := envMap[k]; p != "" {
					if _, lerr := os.Lstat(p); errors.Is(lerr, os.ErrNotExist) {
						if !ctx.DryRun {
							if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
								return actions, err
							}
							if err := os.WriteFile(p, nil, 0o600); err != nil {
								return actions, err
							}
						}
						actions = append(actions, fmt.Sprintf("%screated %s", prefix, ctx.CollapseHome(p)))
					}
				}
			}
		}
	}

	return actions, nil
}

func writeIfChanged(path string, content []byte, perm os.FileMode, dryRun bool) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	return true, app.WriteAtomicFile(path, content, perm)
}

func quoteGitConfigValue(v string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + r.Replace(v) + `"`
}

func unquoteGitConfigValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
		return v
	}
	inner := v[1 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
			switch inner[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteByte(inner[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(inner[i])
			}
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}

func renderZoneGitConfig(spec ZoneSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# git identity for the %s zone — generated by ws identity\n\n", spec.Name)
	b.WriteString("[user]\n")
	fmt.Fprintf(&b, "\tname = %s\n", quoteGitConfigValue(spec.GitName))
	fmt.Fprintf(&b, "\temail = %s\n", quoteGitConfigValue(spec.GitEmail))
	if spec.SigningKey != "" {
		fmt.Fprintf(&b, "\tsigningkey = %s\n", quoteGitConfigValue(spec.SigningKey))
		if strings.HasPrefix(spec.SigningKey, "ssh-") || strings.Contains(spec.SigningKey, "/") {
			b.WriteString("\n[gpg]\n\tformat = ssh\n")
		}
		b.WriteString("\n[commit]\n\tgpgsign = true\n")
		b.WriteString("\n[tag]\n\tgpgsign = true\n")
	}
	return b.String()
}

func discoverZoneRoots(ctx *app.Context) ([]string, map[string]string, map[string]ZoneSpec) {
	return discoverZoneRootsWithEnv(ctx, ReadZoneMiseEnv)
}

func DiscoverZoneNames(ctx *app.Context) []string {
	order, _, _ := discoverZoneRootsFast(ctx)
	return order
}

func discoverZoneRootsFast(ctx *app.Context) ([]string, map[string]string, map[string]ZoneSpec) {
	return discoverZoneRootsWithEnv(ctx, ReadZoneMiseEnvFast)
}

func discoverZoneRootsWithEnv(ctx *app.Context, readEnv func(string) (map[string]string, error)) ([]string, map[string]string, map[string]ZoneSpec) {
	roots := map[string]string{}
	specsMap := map[string]ZoneSpec{}
	var order []string
	add := func(name, root string) {
		if name == "" {
			return
		}
		if _, ok := roots[name]; !ok {
			roots[name] = root
			order = append(order, name)
		}
	}

	if specs, err := ParseZones(ctx); err == nil {
		for _, s := range specs {
			specsMap[s.Name] = s
			add(s.Name, filepath.Join(ctx.HomeDir, s.Name))
		}
	}

	if entries, err := os.ReadDir(ctx.HomeDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			zroot := filepath.Join(ctx.HomeDir, e.Name())
			envMap, err := readEnv(zroot)
			if err != nil {
				continue
			}
			if zname := envMap["WS_ZONE"]; zname != "" {
				add(zname, zroot)
			}
		}
	}

	active := os.Getenv("WS_ZONE")
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == active && order[j] != active {
			return true
		}
		if order[j] == active && order[i] != active {
			return false
		}
		return order[i] < order[j]
	})

	return order, roots, specsMap
}

func isPathWithin(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}

func Whoami(ctx *app.Context, cwd string) (*WhoamiReport, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cleanCWD := filepath.Clean(cwd)
	order, roots, specsMap := discoverZoneRoots(ctx)

	var pathZone string
	for _, zname := range order {
		if isPathWithin(cleanCWD, roots[zname]) {
			pathZone = zname
			break
		}
	}

	envZone := os.Getenv("WS_ZONE")
	activeZone := pathZone
	if activeZone == "" {
		activeZone = envZone
	}

	report := &WhoamiReport{
		CWD:        cleanCWD,
		ActiveZone: activeZone,
		EnvZone:    envZone,
		PathZone:   pathZone,
		Env:        map[string]string{},
	}

	if envZone != "" && pathZone != "" && envZone != pathZone {
		report.ZoneMismatch = true
		report.MismatchDetail = fmt.Sprintf("WS_ZONE=%q disagrees with directory zone %q", envZone, pathZone)
	} else if envZone != "" && pathZone == "" {
		report.ZoneMismatch = true
		report.MismatchDetail = fmt.Sprintf("WS_ZONE=%q is set while cwd is outside every zone", envZone)
	}

	zoneEnv := map[string]string{}
	if activeZone != "" {
		zroot := roots[activeZone]
		if zroot == "" {
			zroot = filepath.Join(ctx.HomeDir, activeZone)
		}
		if parsed, err := ReadZoneMiseEnv(zroot); err == nil {
			zoneEnv = parsed
		}
		gitName, gitEmail, gitKey := parseGitUserConfig(filepath.Join(zroot, ".gitconfig"))
		if spec, ok := specsMap[activeZone]; ok {
			if gitName == "" {
				gitName = spec.GitName
			}
			if gitEmail == "" {
				gitEmail = spec.GitEmail
			}
			if gitKey == "" {
				gitKey = spec.SigningKey
			}
			if zoneEnv["AWS_PROFILE"] == "" && spec.AWSProfile != "" {
				zoneEnv["AWS_PROFILE"] = spec.AWSProfile
			}
		}
		report.GitName = gitName
		report.GitEmail = gitEmail
		report.GitSigningKey = gitKey
	}

	pick := func(key string) string {
		if v := zoneEnv[key]; v != "" {
			return v
		}
		if report.ZoneMismatch {
			return ""
		}
		return os.Getenv(key)
	}

	report.CodeRoot = pick("WS_CODE_ROOT")
	report.GHConfigDir = pick("GH_CONFIG_DIR")
	report.AWSConfigFile = pick("AWS_CONFIG_FILE")
	report.AWSCredsFile = pick("AWS_SHARED_CREDENTIALS_FILE")
	report.AWSProfile = pick("AWS_PROFILE")
	report.KubeConfig = pick("KUBECONFIG")
	report.ClaudeConfigDir = pick("CLAUDE_CONFIG_DIR")
	report.CodexHome = pick("CODEX_HOME")
	if report.CodexHome == "" && !report.ZoneMismatch && ctx != nil && ctx.HomeDir != "" {
		defaultCodex := filepath.Join(ctx.HomeDir, ".codex")
		if _, err := os.Stat(defaultCodex); err == nil {
			report.CodexHome = defaultCodex
		}
	}
	report.Env = zoneEnv

	return report, nil
}

func parseGitUserConfig(path string) (name, email, signingKey string) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	inUser := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sec := strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			inUser = sec == "user"
			continue
		}
		if !inUser {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = unquoteGitConfigValue(v)
		switch k {
		case "name":
			name = v
		case "email":
			email = v
		case "signingkey":
			signingKey = v
		}
	}
	return name, email, signingKey
}

func ResolveZoneEnv(ctx *app.Context, zone string) ([]string, error) {
	zone = strings.TrimSpace(zone)
	if zone == "" || strings.ContainsAny(zone, `/\`) || strings.HasPrefix(zone, ".") {
		return nil, fmt.Errorf("invalid zone name %q", zone)
	}
	zroot := filepath.Join(ctx.HomeDir, zone)
	envMap, err := ReadZoneMiseEnv(zroot)
	if err != nil {
		return nil, fmt.Errorf("load zone %q env: %w", zone, err)
	}
	if envMap["WS_ZONE"] == "" {
		envMap["WS_ZONE"] = zone
	}
	if specs, err := ParseZones(ctx); err == nil {
		for _, s := range specs {
			if s.Name == zone && s.AWSProfile != "" && envMap["AWS_PROFILE"] == "" {
				envMap["AWS_PROFILE"] = s.AWSProfile
			}
		}
	}
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+envMap[k])
	}
	return out, nil
}

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

func isZoneScopedPrefix(key string) bool {
	return strings.HasPrefix(key, "AWS_") ||
		strings.HasPrefix(key, "GH_") ||
		strings.HasPrefix(key, "CLAUDE_") ||
		strings.HasPrefix(key, "CODEX_")
}

func zoneForPath(ctx *app.Context, p string) string {
	if ctx == nil || strings.TrimSpace(p) == "" {
		return ""
	}
	cleanP := filepath.Clean(ctx.ExpandHome(p))
	order, roots, _ := discoverZoneRoots(ctx)
	for _, zname := range order {
		if isPathWithin(cleanP, roots[zname]) {
			return zname
		}
		if envMap, err := ReadZoneMiseEnv(roots[zname]); err == nil {
			if cr := envMap["WS_CODE_ROOT"]; cr != "" && isPathWithin(cleanP, filepath.Clean(cr)) {
				return zname
			}
		}
	}
	return ""
}

func ReplaceZoneEnv(base, overlay []string) []string {
	return replaceZoneEnvWithExtra(base, overlay, nil)
}

func ReplaceZoneEnvForContext(ctx *app.Context, base, overlay []string) []string {
	var extra map[string]bool
	if ctx != nil {
		extra = map[string]bool{}
		order, roots, _ := discoverZoneRoots(ctx)
		for _, zname := range order {
			if envMap, err := ReadZoneMiseEnv(roots[zname]); err == nil {
				for k := range envMap {
					switch k {
					case "PATH", "HOME", "USER", "SHELL", "TERM", "TMPDIR":
						continue
					}
					extra[k] = true
				}
			}
		}
	}
	return replaceZoneEnvWithExtra(base, overlay, extra)
}

func replaceZoneEnvWithExtra(base, overlay []string, extra map[string]bool) []string {
	cleaned := make([]string, 0, len(base))
	for _, entry := range base {
		k, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if zoneScopedEnvKeys[k] || isZoneScopedPrefix(k) || extra[k] {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return MergeEnv(cleaned, overlay)
}

func FixZoneSymlinks(ctx *app.Context, onlyZone string) ([]string, error) {
	if ctx == nil || ctx.HomeDir == "" {
		return nil, fmt.Errorf("context with home directory is required")
	}
	specs, err := ParseZones(ctx)
	if err != nil {
		return nil, err
	}
	var actions []string
	prefix := ""
	if ctx.DryRun {
		prefix = "[dry-run] "
	}
	for _, spec := range specs {
		if onlyZone != "" && spec.Name != onlyZone {
			continue
		}
		zroot := filepath.Join(ctx.HomeDir, spec.Name)
		candidates := []struct {
			path string
			perm os.FileMode
		}{
			{filepath.Join(zroot, "mise.toml"), 0o600},
			{filepath.Join(zroot, "mise.local.toml"), 0o600},
			{filepath.Join(zroot, ".gitconfig"), 0o644},
		}
		for _, cand := range candidates {
			fi, lerr := os.Lstat(cand.path)
			if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if !ctx.DryRun && !ctx.Allow.Replace {
				return actions, fmt.Errorf("refusing to replace symlink %s without --allow replace", ctx.CollapseHome(cand.path))
			}
			content, rerr := os.ReadFile(cand.path)
			if rerr != nil {
				if filepath.Base(cand.path) == "mise.toml" && ctx.RepoRoot != "" {
					seedPath := filepath.Join(ctx.RepoRoot, "config", "identity", spec.Name+".mise.toml")
					content, rerr = os.ReadFile(seedPath)
				}
				if rerr != nil {
					return actions, fmt.Errorf("read symlink target %s: %w", ctx.CollapseHome(cand.path), rerr)
				}
			}
			if !ctx.DryRun {
				if err := os.Remove(cand.path); err != nil {
					return actions, err
				}
				if err := app.WriteAtomicFile(cand.path, content, cand.perm); err != nil {
					return actions, err
				}
			}
			actions = append(actions, fmt.Sprintf("%sconverted symlink %s to regular file", prefix, ctx.CollapseHome(cand.path)))
		}
	}
	return actions, nil
}

type MigrateZoneOptions struct {
	Zone    string
	Source  string
	DestSub string
	DestSet bool
	Entries []string
	All     bool
}

type MigrateZoneItem struct {
	Name   string `json:"name"`
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"`
}

type MigrateZoneReport struct {
	Zone    string            `json:"zone"`
	Source  string            `json:"source"`
	Dest    string            `json:"dest"`
	Moved   int               `json:"moved"`
	Skipped int               `json:"skipped"`
	Items   []MigrateZoneItem `json:"items"`
}

func ListMigratableZoneDirs(ctx *app.Context, zone, sourceDir, destSub string, destSet bool) (srcAbs, destAbs string, entries []string, err error) {
	if ctx == nil || ctx.HomeDir == "" {
		return "", "", nil, fmt.Errorf("home directory is required")
	}
	zone = strings.TrimSpace(zone)
	specs, err := ParseZones(ctx)
	if err != nil {
		return "", "", nil, err
	}
	found := false
	for _, s := range specs {
		if s.Name == zone {
			found = true
			break
		}
	}
	if !found {
		return "", "", nil, fmt.Errorf("zone %q not found in WS_ZONES", zone)
	}

	src := strings.TrimSpace(sourceDir)
	if src == "" {
		src = filepath.Join(ctx.HomeDir, "code")
	} else {
		src = ctx.ExpandHome(src)
	}
	srcAbs = filepath.Clean(src)
	fi, serr := os.Stat(srcAbs)
	if serr != nil || !fi.IsDir() {
		return "", "", nil, fmt.Errorf("source directory does not exist: %s", ctx.CollapseHome(srcAbs))
	}

	sub := "code"
	if destSet {
		sub = destSub
	}
	sub = strings.Trim(strings.TrimSpace(sub), `/\`)
	zroot := filepath.Join(ctx.HomeDir, zone)
	destAbs = zroot
	if sub != "" {
		destAbs = filepath.Clean(filepath.Join(zroot, sub))
		if !isPathWithin(destAbs, zroot) {
			return "", "", nil, fmt.Errorf("destination %q escapes zone root", sub)
		}
	}
	if srcAbs == destAbs {
		return "", "", nil, fmt.Errorf("source and destination are the same directory: %s", ctx.CollapseHome(srcAbs))
	}

	dirEntries, rerr := os.ReadDir(srcAbs)
	if rerr != nil {
		return "", "", nil, rerr
	}
	for _, e := range dirEntries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			entries = append(entries, e.Name())
		}
	}
	sort.Strings(entries)
	return srcAbs, destAbs, entries, nil
}

func MigrateZoneDirs(ctx *app.Context, opts MigrateZoneOptions) (*MigrateZoneReport, error) {
	srcAbs, destAbs, available, err := ListMigratableZoneDirs(ctx, opts.Zone, opts.Source, opts.DestSub, opts.DestSet)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("no directories to move under %s", ctx.CollapseHome(srcAbs))
	}

	selected := opts.Entries
	if len(selected) == 0 {
		if !opts.All {
			return nil, fmt.Errorf("no entries selected (pass directory names or --all)")
		}
		selected = available
	} else {
		for _, name := range selected {
			if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
				return nil, fmt.Errorf("invalid directory name %q", name)
			}
			if !slices.Contains(available, name) {
				return nil, fmt.Errorf("directory %q not found under %s", name, ctx.CollapseHome(srcAbs))
			}
		}
	}

	if ctx.RepoRoot != "" {
		cleanRepo := filepath.Clean(ctx.RepoRoot)
		for _, name := range selected {
			from := filepath.Join(srcAbs, name)
			if cleanRepo == from || isPathWithin(cleanRepo, from) {
				return nil, fmt.Errorf("%q holds the workstation repository — move it from a shell outside %s: mv %q %q", name, ctx.CollapseHome(from), from, destAbs)
			}
		}
	}

	report := &MigrateZoneReport{
		Zone:   opts.Zone,
		Source: srcAbs,
		Dest:   destAbs,
	}
	if !ctx.DryRun {
		if err := os.MkdirAll(destAbs, 0o755); err != nil {
			return nil, err
		}
	}

	for _, name := range selected {
		from := filepath.Join(srcAbs, name)
		to := filepath.Join(destAbs, name)
		if _, lerr := os.Lstat(to); lerr == nil {
			report.Skipped++
			report.Items = append(report.Items, MigrateZoneItem{
				Name:   name,
				From:   from,
				To:     to,
				Status: "skipped",
			})
			continue
		}
		if ctx.DryRun {
			report.Moved++
			report.Items = append(report.Items, MigrateZoneItem{
				Name:   name,
				From:   from,
				To:     to,
				Status: "dry-run",
			})
			continue
		}
		if err := moveDir(from, to); err != nil {
			return report, fmt.Errorf("move %s -> %s: %w", ctx.CollapseHome(from), ctx.CollapseHome(to), err)
		}
		report.Moved++
		report.Items = append(report.Items, MigrateZoneItem{
			Name:   name,
			From:   from,
			To:     to,
			Status: "moved",
		})
	}
	return report, nil
}

var renameDirFn = os.Rename

func moveDir(from, to string) error {
	err := renameDirFn(from, to)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyVerifyAndRemoveDir(from, to)
}

func copyVerifyAndRemoveDir(src, dst string) error {
	staging, err := os.MkdirTemp(filepath.Dir(dst), ".ws-migrate-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = makeTreeWritable(staging)
			_ = os.RemoveAll(staging)
		}
	}()

	if err := copyTreePreserving(src, staging); err != nil {
		return fmt.Errorf("copy across filesystems: %w", err)
	}
	if err := verifyCopiedTree(src, staging); err != nil {
		return fmt.Errorf("verify cross-filesystem copy: %w", err)
	}
	if err := os.Rename(staging, dst); err != nil {
		return fmt.Errorf("commit copied directory: %w", err)
	}
	committed = true

	_ = makeTreeWritable(src)
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove source after verified copy to %s: %w", dst, err)
	}
	return nil
}

type savedDirMode struct {
	path string
	perm os.FileMode
}

func copyTreePreserving(src, dst string) error {
	var dirModes []savedDirMode
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		mode := fi.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		case mode.IsDir():
			if rel != "." {
				if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
					return err
				}
			}
			dirModes = append(dirModes, savedDirMode{path: target, perm: mode.Perm()})
			return nil
		case mode.IsRegular():
			return copyRegularFile(p, target, mode.Perm())
		default:
			return fmt.Errorf("unsupported file type %s at %s", mode.Type(), p)
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirModes) - 1; i >= 0; i-- {
		if err := os.Chmod(dirModes[i].path, dirModes[i].perm); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(perm); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func verifyCopiedTree(src, dst string) error {
	srcCount := 0
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		srcCount++
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		sfi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		dfi, err := os.Lstat(target)
		if err != nil {
			return fmt.Errorf("missing copied entry %s: %w", rel, err)
		}
		if sfi.Mode().Type() != dfi.Mode().Type() {
			return fmt.Errorf("file type mismatch for %s", rel)
		}
		if sfi.Mode()&os.ModeSymlink != 0 {
			st, err := os.Readlink(p)
			if err != nil {
				return err
			}
			dt, err := os.Readlink(target)
			if err != nil {
				return err
			}
			if st != dt {
				return fmt.Errorf("symlink target mismatch for %s: got %q want %q", rel, dt, st)
			}
			return nil
		}
		if sfi.Mode().Perm() != dfi.Mode().Perm() {
			return fmt.Errorf("mode mismatch for %s: got %04o want %04o", rel, dfi.Mode().Perm(), sfi.Mode().Perm())
		}
		if sfi.Mode().IsRegular() {
			if sfi.Size() != dfi.Size() {
				return fmt.Errorf("size mismatch for %s: got %d want %d", rel, dfi.Size(), sfi.Size())
			}
			sh, err := fileSHA256(p)
			if err != nil {
				return err
			}
			dh, err := fileSHA256(target)
			if err != nil {
				return err
			}
			if sh != dh {
				return fmt.Errorf("checksum mismatch for %s", rel)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	dstCount := 0
	if err := filepath.WalkDir(dst, func(_ string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		dstCount++
		return nil
	}); err != nil {
		return err
	}
	if srcCount != dstCount {
		return fmt.Errorf("entry count mismatch: source has %d, destination has %d", srcCount, dstCount)
	}
	return nil
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return zero, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func makeTreeWritable(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if fi, err := os.Lstat(p); err == nil && fi.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(p, fi.Mode().Perm()|0o700)
			}
		}
		return nil
	})
}

func MergeEnv(base, overlay []string) []string {
	merged := make([]string, 0, len(base)+len(overlay))
	idx := make(map[string]int, len(base))
	for _, entry := range base {
		k, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		idx[k] = len(merged)
		merged = append(merged, entry)
	}
	for _, entry := range overlay {
		k, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if pos, exists := idx[k]; exists {
			merged[pos] = entry
		} else {
			idx[k] = len(merged)
			merged = append(merged, entry)
		}
	}
	return merged
}
