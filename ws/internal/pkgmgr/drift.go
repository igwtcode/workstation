package pkgmgr

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/config"
)

type UntrackedPackage struct {
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	SuggestedFile string `json:"suggestedFile"`
}

func DefaultPackageListFile(ctx *app.Context, provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "cask":
		return "pkg/mac-cask.txt"
	}
	if ctx != nil {
		if ctx.Host.OS == "darwin" {
			return "pkg/mac-brew.txt"
		}
		if ctx.Host.Distro == "arch" || ctx.Host.Distro == "cachyos" || slices.Contains(ctx.Host.DistroLike, "arch") {
			return "pkg/arch.txt"
		}
	}
	return "pkg/server.txt"
}

func buildManagedPackageMatcher(ctx *app.Context, presetID string, runner CommandRunner) func(string) bool {
	managedExact := map[string]bool{}
	managedBase := map[string]bool{}
	addManaged := func(name string) {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" {
			return
		}
		managedExact[n] = true
		if slash := strings.LastIndexByte(n, '/'); slash >= 0 && slash+1 < len(n) {
			managedBase[n[slash+1:]] = true
		} else {
			managedBase[n] = true
		}
	}

	if ctx != nil && ctx.RepoRoot != "" {
		if presets, err := config.LoadPresets(ctx.RepoRoot); err == nil {
			if resolved, rerr := config.ResolvePreset(ctx.RepoRoot, presets, presetID); rerr == nil {
				for _, pkgName := range resolved.PackageNames {
					addManaged(pkgName)
				}
				if catalog, cerr := config.LoadTools(ctx.RepoRoot); cerr == nil {
					tools := RefineToolsForPreset(ctx, resolved, config.ResolveTools(resolved, catalog, ctx.Host), runner)
					for _, t := range tools {
						addManaged(t.ID)
						addManaged(t.Spec.Name)
					}

					for id, spec := range catalog {
						for _, inst := range spec.Install {
							if inst.OS == "" || inst.OS == ctx.Host.OS {
								addManaged(id)
								addManaged(inst.Name)
							}
						}
					}
				}
			}
		}
	}

	return func(name string) bool {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" || managedExact[n] || managedBase[n] {
			return true
		}
		if slash := strings.LastIndexByte(n, '/'); slash >= 0 && slash+1 < len(n) {
			if managedBase[n[slash+1:]] {
				return true
			}
		}
		return false
	}
}

func DetectUntrackedPackages(ctx *app.Context, presetID string, runner CommandRunner) ([]UntrackedPackage, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	if strings.TrimSpace(presetID) == "" {
		presetID = ctx.EffectivePreset()
	}

	isManaged := buildManagedPackageMatcher(ctx, presetID, runner)

	var mu sync.Mutex
	var out []UntrackedPackage
	var wg sync.WaitGroup

	if ctx.Host.OS == "darwin" && hasBin(runner, "brew") {
		wg.Add(2)
		go func() {
			defer wg.Done()
			stdout, _, err := runner.Run([]string{"brew", "list", "--formula", "--installed-on-request", "--full-name"}, nil, "", false)
			if err != nil {
				return
			}
			var found []UntrackedPackage
			for _, name := range splitNonEmptyLines(stdout) {
				if !isManaged(name) {
					found = append(found, UntrackedPackage{
						Name:          name,
						Provider:      "brew",
						SuggestedFile: "pkg/mac-brew.txt",
					})
				}
			}
			mu.Lock()
			out = append(out, found...)
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			stdout, _, err := runner.Run([]string{"brew", "list", "--cask", "--full-name"}, nil, "", false)
			if err != nil {
				return
			}
			var found []UntrackedPackage
			for _, name := range splitNonEmptyLines(stdout) {
				if !isManaged(name) {
					found = append(found, UntrackedPackage{
						Name:          name,
						Provider:      "cask",
						SuggestedFile: "pkg/mac-cask.txt",
					})
				}
			}
			mu.Lock()
			out = append(out, found...)
			mu.Unlock()
		}()
	} else if (ctx.Host.Distro == "arch" || ctx.Host.Distro == "cachyos" || slices.Contains(ctx.Host.DistroLike, "arch")) &&
		(hasBin(runner, "pacman") || hasBin(runner, "paru")) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			queryBin := "pacman"
			if !hasBin(runner, "pacman") {
				queryBin = "paru"
			}
			stdout, _, err := runner.Run([]string{queryBin, "-Qeq"}, nil, "", false)
			if err != nil {
				return
			}
			aurSet := readForeignPackages(runner)
			var found []UntrackedPackage
			for _, name := range splitNonEmptyLines(stdout) {
				if isManaged(name) {
					continue
				}
				prov := queryBin
				if aurSet[name] || hasBin(runner, "paru") {
					prov = "paru"
				}
				found = append(found, UntrackedPackage{
					Name:          name,
					Provider:      prov,
					SuggestedFile: "pkg/arch.txt",
				})
			}
			mu.Lock()
			out = append(out, found...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func resolveSafePkgListFile(repoRoot, relFile string) (string, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return "", fmt.Errorf("workstation repository root is required")
	}
	cleanRel := path.Clean(strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(relFile)), "/"))
	if !strings.HasPrefix(cleanRel, "pkg/") || !strings.HasSuffix(cleanRel, ".txt") || strings.Contains(cleanRel, "..") {
		return "", fmt.Errorf("invalid package list path %q (must be pkg/*.txt)", relFile)
	}
	abs := filepath.Join(filepath.Clean(repoRoot), filepath.FromSlash(cleanRel))
	return abs, nil
}

func validatePkgListEntryName(pkgName string) (string, error) {
	p := strings.TrimSpace(pkgName)
	if p == "" || strings.ContainsAny(p, " \t\r\n#\x00") {
		return "", fmt.Errorf("invalid package name %q", pkgName)
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("unsafe package name %q", pkgName)
	}
	return p, nil
}

func AddPackageToList(repoRoot, relFile, pkgName string) (bool, error) {
	abs, err := resolveSafePkgListFile(repoRoot, relFile)
	if err != nil {
		return false, err
	}
	entry, err := validatePkgListEntryName(pkgName)
	if err != nil {
		return false, err
	}

	raw, rerr := os.ReadFile(abs)
	if rerr != nil && !os.IsNotExist(rerr) {
		return false, rerr
	}

	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.EqualFold(trimmed, entry) {
			return false, nil
		}
	}

	hasAddedHeader := false
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "# added via ws" {
			hasAddedHeader = true
			break
		}
	}
	if !hasAddedHeader {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "# added via ws")
	}
	lines = append(lines, entry)

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return false, err
	}
	out := strings.Join(lines, "\n") + "\n"
	if err := app.WriteAtomicFile(abs, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func RemovePackageFromList(repoRoot, relFile, pkgName string) (bool, error) {
	abs, err := resolveSafePkgListFile(repoRoot, relFile)
	if err != nil {
		return false, err
	}
	entry, err := validatePkgListEntryName(pkgName)
	if err != nil {
		return false, err
	}

	raw, rerr := os.ReadFile(abs)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, nil
		}
		return false, rerr
	}

	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	kept := make([]string, 0, len(lines))
	removed := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && strings.EqualFold(trimmed, entry) {
			removed = true
			continue
		}
		kept = append(kept, ln)
	}
	if !removed {
		return false, nil
	}
	out := ""
	if len(kept) > 0 {
		out = strings.Join(kept, "\n") + "\n"
	}
	if err := app.WriteAtomicFile(abs, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func FindPackageListFile(repoRoot, pkgName string, candidates []string) (string, bool) {
	entry, err := validatePkgListEntryName(pkgName)
	if err != nil || strings.TrimSpace(repoRoot) == "" {
		return "", false
	}
	if len(candidates) == 0 {
		candidates = []string{"pkg/mac-brew.txt", "pkg/mac-cask.txt", "pkg/arch.txt", "pkg/server.txt"}
	}
	for _, rel := range candidates {
		abs, err := resolveSafePkgListFile(repoRoot, rel)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		for _, ln := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			t := strings.TrimSpace(ln)
			if t != "" && !strings.HasPrefix(t, "#") && strings.EqualFold(t, entry) {
				return rel, true
			}
		}
	}
	return "", false
}
