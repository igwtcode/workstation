package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/pelletier/go-toml/v2"
)

type InstallSpec struct {
	Via    string `toml:"via" json:"via"`
	Name   string `toml:"name" json:"name,omitempty"`
	Ref    string `toml:"ref" json:"ref,omitempty"`
	URL    string `toml:"url" json:"url,omitempty"`
	SHA256 string `toml:"sha256" json:"sha256,omitempty"`
	TOFU   bool   `toml:"tofu" json:"tofu,omitempty"`
	OS     string `toml:"os" json:"os,omitempty"`
	Shim   string `toml:"shim" json:"shim,omitempty"`
}

type ToolSpec struct {
	ID          string        `toml:"-" json:"id"`
	Bin         string        `toml:"bin" json:"bin,omitempty"`
	Description string        `toml:"description" json:"description,omitempty"`
	Updates     string        `toml:"updates" json:"updates,omitempty"`
	Conflicts   []string      `toml:"conflicts" json:"conflicts,omitempty"`
	Install     []InstallSpec `toml:"install" json:"install,omitempty"`
}

type ResolvedTool struct {
	ID           string        `json:"id"`
	Bin          string        `json:"bin"`
	Updates      string        `json:"updates"`
	Conflicts    []string      `json:"conflicts,omitempty"`
	Spec         InstallSpec   `json:"spec"`
	Source       string        `json:"source"`
	OriginPreset string        `json:"originPreset,omitempty"`
	OriginFile   string        `json:"originFile,omitempty"`
	AllSpecs     []InstallSpec `json:"allSpecs,omitempty"`
}

type toolsFile struct {
	Tool map[string]ToolSpec `toml:"tool"`
}

func LoadTools(repoRoot string) (map[string]ToolSpec, error) {
	path := filepath.Join(repoRoot, "tools.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed toolsFile
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for id, t := range parsed.Tool {
		t.ID = id
		if t.Updates == "" {
			t.Updates = "manager"
		}
		if t.Bin == "" {
			t.Bin = id
		}
		parsed.Tool[id] = t
	}
	return parsed.Tool, nil
}

func ParsePackageList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var items []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		items = append(items, line)
	}
	return items, sc.Err()
}

func ResolveTools(preset *ResolvedPreset, catalog map[string]ToolSpec, host app.HostInfo) []ResolvedTool {
	if preset == nil {
		return nil
	}
	var out []ResolvedTool
	byTarget := map[string]int{}
	addOrMerge := func(rt ResolvedTool) {
		key := rt.Spec.Via + ":" + targetPkgKey(rt)
		if idx, exists := byTarget[key]; exists {
			if out[idx].Source != "tools.toml" && rt.Source == "tools.toml" {
				out[idx] = rt
			}
			return
		}
		byTarget[key] = len(out)
		out = append(out, rt)
	}

	for _, pkgName := range preset.PackageNames {
		var orig PackageOrigin
		if preset.PackageOrigins != nil {
			orig = preset.PackageOrigins[pkgName]
		}
		if spec, ok := catalog[pkgName]; ok {
			chosen := pickInstallSpec(spec, host)
			addOrMerge(ResolvedTool{
				ID:           pkgName,
				Bin:          spec.Bin,
				Updates:      spec.Updates,
				Conflicts:    spec.Conflicts,
				Spec:         chosen,
				Source:       "tools.toml",
				OriginPreset: orig.PresetID,
				OriginFile:   orig.SourceFile,
				AllSpecs:     slices.Clone(spec.Install),
			})
			continue
		}
		defSpec := defaultInstallSpec(pkgName, orig.SourceFile, preset, host)
		addOrMerge(ResolvedTool{
			ID:           pkgName,
			Bin:          defaultBinForPkg(pkgName),
			Updates:      "manager",
			Spec:         defSpec,
			Source:       "pkg-list",
			OriginPreset: orig.PresetID,
			OriginFile:   orig.SourceFile,
			AllSpecs:     []InstallSpec{defSpec},
		})
	}
	return out
}

func targetPkgKey(rt ResolvedTool) string {
	if rt.Spec.Name != "" {
		return rt.Spec.Name
	}
	if rt.Spec.Ref != "" {
		return rt.Spec.Ref
	}
	return rt.ID
}

var pkgBinaryAliases = map[string]string{
	"ripgrep":            "rg",
	"git-delta":          "delta",
	"kubernetes-cli":     "kubectl",
	"go-yq":              "yq",
	"github-cli":         "gh",
	"fd-find":            "fd",
	"openssh":            "ssh",
	"wireguard-tools":    "wg",
	"bash-completion@2":  "bash",
	"visual-studio-code": "code",
	"1password-cli":      "op",
}

func defaultBinForPkg(pkgName string) string {
	if alias, ok := pkgBinaryAliases[pkgName]; ok {
		return alias
	}
	if slash := strings.LastIndexByte(pkgName, '/'); slash >= 0 && slash+1 < len(pkgName) {
		base := pkgName[slash+1:]
		if alias, ok := pkgBinaryAliases[base]; ok {
			return alias
		}
		return base
	}
	return pkgName
}

func pickInstallSpec(tool ToolSpec, host app.HostInfo) InstallSpec {
	useParu := slices.Contains(host.PkgManagers, "paru")
	for _, cand := range tool.Install {
		if cand.OS != "" && cand.OS != host.OS {
			continue
		}
		if providerMatchesHost(cand.Via, host) {
			if cand.Name == "" && cand.Via != "script" && cand.Via != "mise" {
				cand.Name = tool.ID
			}
			if cand.Via == "pacman" && useParu {
				cand.Via = "paru"
			}
			return cand
		}
	}
	return InstallSpec{Via: nativeProvider(host), Name: tool.ID}
}

func defaultInstallSpec(pkgName, sourceFile string, preset *ResolvedPreset, host app.HostInfo) InstallSpec {
	if host.OS == "darwin" {
		if filepath.Base(sourceFile) == "mac-cask.txt" {
			return InstallSpec{Via: "cask", Name: pkgName}
		}
		if sourceFile == "" && preset != nil && slices.Contains(preset.PackageFiles, "pkg/mac-cask.txt") && strings.HasPrefix(pkgName, "font-") {
			return InstallSpec{Via: "cask", Name: pkgName}
		}
	}
	return InstallSpec{Via: nativeProvider(host), Name: pkgName}
}

func nativeProvider(host app.HostInfo) string {
	switch {
	case host.OS == "darwin":
		return "brew"
	case host.Distro == "arch" || host.Distro == "cachyos" || slices.Contains(host.DistroLike, "arch"):
		if slices.Contains(host.PkgManagers, "paru") {
			return "paru"
		}
		return "pacman"
	case host.Distro == "ubuntu" || host.Distro == "debian" || slices.Contains(host.DistroLike, "debian"):
		return "apt"
	case host.Distro == "fedora" || host.Distro == "rhel" || slices.Contains(host.DistroLike, "fedora"):
		return "dnf"
	default:
		return "mise"
	}
}

func providerMatchesHost(via string, host app.HostInfo) bool {
	switch via {
	case "brew", "cask":
		return host.OS == "darwin"
	case "pacman", "paru":
		return host.OS == "linux" && (host.Distro == "arch" || host.Distro == "cachyos" || slices.Contains(host.DistroLike, "arch"))
	case "apt":
		return host.OS == "linux" && (host.Distro == "ubuntu" || host.Distro == "debian" || slices.Contains(host.DistroLike, "debian"))
	case "dnf":
		return host.OS == "linux" && (host.Distro == "fedora" || host.Distro == "rhel" || slices.Contains(host.DistroLike, "fedora"))
	case "mise", "script", "gh-ext":
		return true
	default:
		return false
	}
}

func ListToolIDs(catalog map[string]ToolSpec) []string {
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
