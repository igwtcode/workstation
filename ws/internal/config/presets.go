package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/pelletier/go-toml/v2"
)

var BinaryVersion = "dev"

type PresetCondition struct {
	OS       string   `toml:"os" json:"os,omitempty"`
	Distro   []string `toml:"distro" json:"distro,omitempty"`
	Headless *bool    `toml:"headless" json:"headless,omitempty"`
}

type Preset struct {
	ID              string          `toml:"-" json:"id"`
	Description     string          `toml:"description" json:"description"`
	Include         []string        `toml:"include" json:"include,omitempty"`
	Exclude         []string        `toml:"exclude" json:"exclude,omitempty"`
	When            PresetCondition `toml:"when" json:"when,omitempty"`
	Links           []string        `toml:"links" json:"links,omitempty"`
	Packages        []string        `toml:"packages" json:"packages,omitempty"`
	Conflicts       []string        `toml:"conflicts" json:"conflicts,omitempty"`
	ExcludePackages []string        `toml:"exclude_packages" json:"excludePackages,omitempty"`
	ExtraTools      []string        `toml:"extra_tools" json:"extraTools,omitempty"`
	OSHooks         []string        `toml:"os_hooks" json:"osHooks,omitempty"`
	Secrets         string          `toml:"secrets" json:"secrets"`
	Tags            []string        `toml:"tags" json:"tags,omitempty"`
}

type PackageOrigin struct {
	PresetID   string `json:"presetId"`
	SourceFile string `json:"sourceFile"`
}

type ResolvedPreset struct {
	ID               string                   `json:"id"`
	Description      string                   `json:"description"`
	Chain            []string                 `json:"chain"`
	Links            []string                 `json:"links"`
	PackageFiles     []string                 `json:"packageFiles"`
	PackageNames     []string                 `json:"packageNames"`
	PackageOrigins   map[string]PackageOrigin `json:"packageOrigins,omitempty"`
	ConflictFiles    []string                 `json:"conflictFiles,omitempty"`
	ConflictPackages []string                 `json:"conflictPackages,omitempty"`
	OSHooks          []string                 `json:"osHooks"`
	Secrets          string                   `json:"secrets"`
	ExcludedTags     []string                 `json:"excludedTags,omitempty"`
	ExcludedNames    []string                 `json:"excludedNames,omitempty"`
}

type presetsFile struct {
	WSMinVersion string            `toml:"ws_min_version"`
	Preset       map[string]Preset `toml:"preset"`
}

func LoadPresets(repoRoot string) (map[string]Preset, error) {
	return LoadPresetsWithVersion(repoRoot, BinaryVersion)
}

func LoadPresetsWithVersion(repoRoot, binaryVersion string) (map[string]Preset, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("workstation repo root not found (set WS_DIR, pass --repo <path>, or configure workspace.repo_root)")
	}
	path := filepath.Join(repoRoot, "presets.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed presetsFile
	dec := toml.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := CheckMinVersion(binaryVersion, parsed.WSMinVersion); err != nil {
		return nil, err
	}
	for id, p := range parsed.Preset {
		p.ID = id
		parsed.Preset[id] = p
	}
	return parsed.Preset, nil
}

func CheckMinVersion(current, minVersion string) error {
	req := strings.TrimSpace(minVersion)
	if req == "" {
		return nil
	}
	reqMaj, reqMin, reqPatch, reqSuffix, ok := parseSemver(req)
	if !ok || reqSuffix != "" {
		return fmt.Errorf("presets.toml has invalid ws_min_version %q (expected semver MAJOR.MINOR.PATCH)", req)
	}

	cur := strings.TrimSpace(current)
	if cur == "" || cur == "dev" {
		return nil
	}
	curMaj, curMin, curPatch, curSuffix, ok := parseSemver(cur)
	if !ok {
		return nil
	}

	if cmp := compareSemverTriple(curMaj, curMin, curPatch, reqMaj, reqMin, reqPatch); cmp < 0 {
		return fmt.Errorf("presets.toml requires ws >= %s (this binary is %s; rebuild with 'mise run ws:install')", req, cur)
	} else if cmp == 0 && curSuffix != "" && !isGitDescribeAheadOrDirty(curSuffix) {
		return fmt.Errorf("presets.toml requires ws >= %s (this binary is %s; rebuild with 'mise run ws:install')", req, cur)
	}
	return nil
}

func parseSemver(v string) (major, minor, patch int, suffix string, ok bool) {
	s := strings.TrimPrefix(strings.TrimSpace(v), "v")
	core := s
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		core = s[:dash]
		suffix = s[dash+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return 0, 0, 0, "", false
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	pat, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || maj < 0 || min < 0 || pat < 0 {
		return 0, 0, 0, "", false
	}
	return maj, min, pat, suffix, true
}

func compareSemverTriple(a1, a2, a3, b1, b2, b3 int) int {
	if a1 != b1 {
		if a1 < b1 {
			return -1
		}
		return 1
	}
	if a2 != b2 {
		if a2 < b2 {
			return -1
		}
		return 1
	}
	if a3 != b3 {
		if a3 < b3 {
			return -1
		}
		return 1
	}
	return 0
}

func isGitDescribeAheadOrDirty(suffix string) bool {
	if suffix == "dirty" {
		return true
	}
	s := strings.TrimSuffix(suffix, "-dirty")
	nStr, hash, hasDash := strings.Cut(s, "-")
	if !hasDash || !strings.HasPrefix(hash, "g") || len(hash) < 2 {
		return false
	}
	n, err := strconv.Atoi(nStr)
	return err == nil && n > 0
}

func ListPresetIDs(presets map[string]Preset) []string {
	ids := make([]string, 0, len(presets))
	for id := range presets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (p Preset) MatchesHost(h app.HostInfo) bool {
	if p.When.OS != "" && p.When.OS != h.OS {
		return false
	}
	if len(p.When.Distro) > 0 {
		matched := slices.Contains(p.When.Distro, h.Distro)
		for _, like := range h.DistroLike {
			if slices.Contains(p.When.Distro, like) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if p.When.Headless != nil && *p.When.Headless != h.Headless {
		return false
	}
	return true
}

func ResolvePreset(repoRoot string, presets map[string]Preset, id string) (*ResolvedPreset, error) {
	root, ok := presets[id]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", id)
	}

	var chain []string
	seen := map[string]bool{}
	visiting := map[string]bool{}

	var dfs func(parentID, curID string) error
	dfs = func(parentID, curID string) error {
		if visiting[curID] {
			return fmt.Errorf("preset cycle detected at %q", curID)
		}
		if seen[curID] {
			return nil
		}
		p, exists := presets[curID]
		if !exists {
			if parentID == "" {
				return fmt.Errorf("unknown preset %q", curID)
			}
			return fmt.Errorf("preset %q includes unknown preset %q", parentID, curID)
		}
		visiting[curID] = true
		for _, parent := range p.Include {
			if err := dfs(curID, parent); err != nil {
				return err
			}
		}
		delete(visiting, curID)
		seen[curID] = true
		chain = append(chain, curID)
		return nil
	}

	if err := dfs("", id); err != nil {
		return nil, err
	}

	out := &ResolvedPreset{
		ID:          id,
		Description: root.Description,
		Chain:       chain,
		Secrets:     "none",
	}

	linkSet := map[string]bool{}
	pkgFileSet := map[string]bool{}
	conflictFileSet := map[string]bool{}
	conflictPkgSet := map[string]bool{}
	hookSet := map[string]bool{}
	excludePkgSet := map[string]bool{}
	excludeTagSet := map[string]bool{}
	var orderedPkgNames []string
	pkgNameSet := map[string]bool{}
	origins := map[string]PackageOrigin{}

	for _, cid := range chain {
		p := presets[cid]
		for _, t := range p.Exclude {
			excludeTagSet[t] = true
		}
		for _, ex := range p.ExcludePackages {
			excludePkgSet[ex] = true
		}
	}

	isExcludedByTag := func(cid string, tags []string) bool {
		if cid == id || len(excludeTagSet) == 0 {
			return false
		}
		for _, t := range tags {
			if excludeTagSet[t] {
				return true
			}
		}
		return false
	}

	for _, cid := range chain {
		p := presets[cid]
		if isExcludedByTag(cid, p.Tags) {
			continue
		}
		if p.Secrets != "" {
			out.Secrets = p.Secrets
		}
		for _, l := range p.Links {
			if !linkSet[l] {
				linkSet[l] = true
				out.Links = append(out.Links, l)
			}
		}
		for _, pf := range p.Packages {
			if !pkgFileSet[pf] {
				pkgFileSet[pf] = true
				out.PackageFiles = append(out.PackageFiles, pf)
			}
			items, err := ParsePackageList(filepath.Join(repoRoot, pf))
			if err != nil {
				return nil, err
			}
			for _, pkg := range items {
				if !pkgNameSet[pkg] {
					pkgNameSet[pkg] = true
					orderedPkgNames = append(orderedPkgNames, pkg)
					origins[pkg] = PackageOrigin{PresetID: cid, SourceFile: pf}
				}
			}
		}
		for _, cf := range p.Conflicts {
			if !conflictFileSet[cf] {
				conflictFileSet[cf] = true
				out.ConflictFiles = append(out.ConflictFiles, cf)
			}
			items, err := ParsePackageList(filepath.Join(repoRoot, cf))
			if err != nil {
				return nil, err
			}
			for _, pkg := range items {
				if !conflictPkgSet[pkg] {
					conflictPkgSet[pkg] = true
					out.ConflictPackages = append(out.ConflictPackages, pkg)
				}
			}
		}
		for _, extra := range p.ExtraTools {
			if !pkgNameSet[extra] {
				pkgNameSet[extra] = true
				orderedPkgNames = append(orderedPkgNames, extra)
				origins[extra] = PackageOrigin{PresetID: cid, SourceFile: "extra_tools"}
			}
		}
		for _, hook := range p.OSHooks {
			if !hookSet[hook] {
				hookSet[hook] = true
				out.OSHooks = append(out.OSHooks, hook)
			}
		}
	}

	out.PackageOrigins = make(map[string]PackageOrigin, len(orderedPkgNames))
	for _, name := range orderedPkgNames {
		if excludePkgSet[name] {
			continue
		}
		out.PackageNames = append(out.PackageNames, name)
		if orig, ok := origins[name]; ok {
			out.PackageOrigins[name] = orig
		}
	}
	for tag := range excludeTagSet {
		out.ExcludedTags = append(out.ExcludedTags, tag)
	}
	sort.Strings(out.ExcludedTags)
	for ex := range excludePkgSet {
		out.ExcludedNames = append(out.ExcludedNames, ex)
	}
	sort.Strings(out.ExcludedNames)

	return out, nil
}
