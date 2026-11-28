package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type ContractIssue struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

func ValidateRepoContracts(repoRoot string) []ContractIssue {
	var issues []ContractIssue
	add := func(id, title, detail string) {
		issues = append(issues, ContractIssue{
			ID:     id,
			Title:  title,
			Detail: detail,
		})
	}

	if strings.TrimSpace(repoRoot) == "" {
		add("contract:repo-root", "Workstation repo root is empty", "")
		return issues
	}

	regOutputs := map[string]bool{}
	regEntries, regErr := LoadThemeRegistry(repoRoot)
	if regErr != nil {
		add("contract:themes-registry", "Cannot load themes/registry", regErr.Error())
	} else {
		for _, entry := range regEntries {
			outNorm := filepath.ToSlash(filepath.Clean(entry.Output))
			if regOutputs[outNorm] {
				add("contract:theme-duplicate-output:"+outNorm, fmt.Sprintf("Duplicate theme registry output %s", outNorm), entry.Template)
			}
			regOutputs[outNorm] = true

			tplAbs := filepath.Join(repoRoot, filepath.FromSlash(entry.Template))
			fi, err := os.Stat(tplAbs)
			if err != nil || fi.IsDir() {
				add("contract:theme-missing-template:"+entry.Template, fmt.Sprintf("Theme template missing: %s", entry.Template), fmt.Sprintf("output %s", entry.Output))
			}
		}
	}

	var palettes []Palette
	themeIDs, themeErr := ListThemes(repoRoot)
	if themeErr != nil {
		add("contract:themes-list", "Cannot list themes/", themeErr.Error())
	} else if len(themeIDs) == 0 {
		add("contract:themes-empty", "No palettes found under themes/", "")
	} else {
		for _, tid := range themeIDs {
			pal, err := LoadPalette(repoRoot, tid)
			if err != nil {
				add("contract:palette:"+tid, fmt.Sprintf("Invalid palette %s", tid), err.Error())
				continue
			}
			palettes = append(palettes, pal)
		}
	}

	if regErr == nil && len(palettes) > 0 {
		for _, entry := range regEntries {
			tplAbs := filepath.Join(repoRoot, filepath.FromSlash(entry.Template))
			raw, err := os.ReadFile(tplAbs)
			if err != nil {
				continue
			}
			content := string(raw)
			for _, pal := range palettes {
				if verr := ValidateTemplateTokens(content, pal); verr != nil {
					add(
						fmt.Sprintf("contract:template-render:%s:%s", entry.Template, pal.ID),
						fmt.Sprintf("Template %s fails against palette %s", entry.Template, pal.ID),
						verr.Error(),
					)
					break
				}
			}
		}
	}

	presets, presetErr := LoadPresets(repoRoot)
	if presetErr != nil {
		add("contract:presets", "Cannot load presets.toml", presetErr.Error())
	} else {
		checkedHooks := map[string]bool{}
		for _, pid := range ListPresetIDs(presets) {
			if _, rerr := ResolvePreset(repoRoot, presets, pid); rerr != nil {
				add("contract:preset-resolve:"+pid, fmt.Sprintf("Cannot resolve preset %q", pid), rerr.Error())
			}
			p := presets[pid]
			for _, hook := range p.OSHooks {
				if checkedHooks[hook] {
					continue
				}
				checkedHooks[hook] = true
				checkOSHook(repoRoot, hook, add)
			}
			for _, rel := range p.Links {
				linkAbs := filepath.Join(repoRoot, filepath.FromSlash(rel))
				if fi, err := os.Stat(linkAbs); err != nil || fi.IsDir() {
					add("contract:preset-link:"+pid+":"+rel, fmt.Sprintf("Preset %q references missing link manifest %s", pid, rel), "")
				}
			}
		}
	}

	if _, terr := LoadTools(repoRoot); terr != nil {
		add("contract:tools", "Cannot load tools.toml", terr.Error())
	}

	linksDir := filepath.Join(repoRoot, "links")
	linkDirEntries, lerr := os.ReadDir(linksDir)
	if lerr != nil {
		add("contract:links-dir", "Cannot read links/", lerr.Error())
	} else {
		var linkFiles []string
		for _, de := range linkDirEntries {
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".links") {
				linkFiles = append(linkFiles, filepath.ToSlash(filepath.Join("links", de.Name())))
			}
		}
		sort.Strings(linkFiles)
		if len(linkFiles) == 0 {
			add("contract:links-empty", "No *.links manifests found under links/", "")
		}
		for _, rel := range linkFiles {
			entries, perr := ParseLinkManifest(repoRoot, rel)
			if perr != nil {
				add("contract:links-parse:"+rel, fmt.Sprintf("Invalid link manifest %s", rel), perr.Error())
				continue
			}
			for _, e := range entries {
				srcAbs := filepath.Join(repoRoot, filepath.FromSlash(e.SourceRel))
				if _, serr := os.Lstat(srcAbs); serr != nil {
					if errors.Is(serr, os.ErrNotExist) && regOutputs[e.SourceRel] {
						continue
					}
					add(
						"contract:link-source:"+e.Manifest+":"+e.SourceRel,
						fmt.Sprintf("Link source missing in repo: %s", e.SourceRel),
						fmt.Sprintf("referenced by %s:%d", e.Manifest, e.LineNumber),
					)
				}
			}
		}
	}

	pkgDir := filepath.Join(repoRoot, "pkg")
	pkgDirEntries, perr := os.ReadDir(pkgDir)
	if perr != nil {
		add("contract:pkg-dir", "Cannot read pkg/", perr.Error())
	} else {
		var pkgFiles []string
		for _, de := range pkgDirEntries {
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".txt") {
				pkgFiles = append(pkgFiles, filepath.ToSlash(filepath.Join("pkg", de.Name())))
			}
		}
		sort.Strings(pkgFiles)
		if len(pkgFiles) == 0 {
			add("contract:pkg-empty", "No *.txt package lists found under pkg/", "")
		}
		for _, rel := range pkgFiles {
			items, lerr := ParsePackageList(filepath.Join(repoRoot, filepath.FromSlash(rel)))
			if lerr != nil {
				add("contract:pkg-parse:"+rel, fmt.Sprintf("Cannot parse package list %s", rel), lerr.Error())
				continue
			}
			if len(items) == 0 {
				add("contract:pkg-empty-file:"+rel, fmt.Sprintf("Package list %s has no entries", rel), "")
			}
		}
	}

	return issues
}

func checkOSHook(repoRoot, hookRel string, add func(id, title, detail string)) {
	cleanRel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(hookRel)))
	if strings.HasPrefix(hookRel, "/") || filepath.IsAbs(hookRel) || cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
		add("contract:os-hook-path:"+hookRel, fmt.Sprintf("OS hook path must stay inside repo: %s", hookRel), "")
		return
	}
	hookAbs := filepath.Join(repoRoot, filepath.FromSlash(cleanRel))
	fi, err := os.Stat(hookAbs)
	if err != nil || fi.IsDir() {
		add("contract:os-hook-missing:"+cleanRel, fmt.Sprintf("OS hook missing: %s", cleanRel), "")
		return
	}
	bashBin, err := exec.LookPath("bash")
	if err != nil {
		bashBin = "/bin/bash"
	}
	cmd := exec.Command(bashBin, "-n", hookAbs)
	if out, err := cmd.CombinedOutput(); err != nil {
		add("contract:os-hook-syntax:"+cleanRel, fmt.Sprintf("OS hook fails bash -n: %s", cleanRel), strings.TrimSpace(string(out)))
	}
}
