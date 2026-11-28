package pkgmgr

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
)

type SearchResult struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Repo        string `json:"repo,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Installed   bool   `json:"installed"`
	Tracked     bool   `json:"tracked"`
}

type PackageInfo struct {
	Name          string   `json:"name"`
	Provider      string   `json:"provider"`
	Repo          string   `json:"repo,omitempty"`
	Version       string   `json:"version,omitempty"`
	LatestVersion string   `json:"latestVersion,omitempty"`
	Installed     bool     `json:"installed"`
	Description   string   `json:"description,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	License       string   `json:"license,omitempty"`
	InstalledSize string   `json:"installedSize,omitempty"`
	Dependencies  []string `json:"dependencies,omitempty"`
	OptionalDeps  []string `json:"optionalDeps,omitempty"`
	RequiredBy    []string `json:"requiredBy,omitempty"`
	Conflicts     []string `json:"conflicts,omitempty"`
	Artifacts     []string `json:"artifacts,omitempty"`
	Caveats       string   `json:"caveats,omitempty"`
	RawInfo       string   `json:"rawInfo,omitempty"`
}

type SearchOptions struct {
	Provider string
	Limit    int
}

func AvailableSearchProviders(ctx *app.Context, runner CommandRunner) []string {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	var out []string
	addIf := func(cond bool, p string) {
		if cond && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	hasBrew := hasBin(runner, "brew")
	hasParu := hasBin(runner, "paru")
	hasPacman := hasBin(runner, "pacman")
	hasApt := hasBin(runner, "apt-cache")
	hasMise := hasBin(runner, "mise")

	osName, distro := "", ""
	var distroLike []string
	if ctx != nil {
		osName = ctx.Host.OS
		distro = ctx.Host.Distro
		distroLike = ctx.Host.DistroLike
	}
	isArch := distro == "arch" || distro == "cachyos" || slices.Contains(distroLike, "arch")
	isDebian := distro == "debian" || distro == "ubuntu" || slices.Contains(distroLike, "debian") || slices.Contains(distroLike, "ubuntu")

	switch {
	case osName == "darwin":
		addIf(hasBrew, "brew")
		addIf(hasBrew, "cask")
		addIf(hasMise, "mise")
	case isArch:
		if hasParu {
			addIf(true, "paru")
		} else {
			addIf(hasPacman, "pacman")
		}
		addIf(hasMise, "mise")
	case isDebian:
		addIf(hasApt, "apt")
		addIf(hasMise, "mise")
	default:
		addIf(hasBrew, "brew")
		addIf(hasBrew, "cask")
		addIf(hasParu, "paru")
		addIf(!hasParu && hasPacman, "pacman")
		addIf(hasApt, "apt")
		addIf(hasMise, "mise")
	}

	return out
}

func validateSearchQuery(raw string) (string, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return "", fmt.Errorf("search query is required")
	}
	if strings.ContainsAny(q, "\x00\r\n") {
		return "", fmt.Errorf("invalid search query")
	}
	return q, nil
}

func validatePkgQueryName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, " \t\r\n\x00") {
		return "", fmt.Errorf("invalid package name %q", raw)
	}
	return name, nil
}

func normalizeSearchProvider(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "", "all":
		return "all", nil
	case "brew", "formula", "formulae":
		return "brew", nil
	case "cask", "casks":
		return "cask", nil
	case "paru", "aur":
		return "paru", nil
	case "pacman":
		return "pacman", nil
	case "mise":
		return "mise", nil
	case "apt", "apt-cache", "dpkg":
		return "apt", nil
	default:
		return "", fmt.Errorf("unsupported search provider %q (expected all, brew, cask, paru, pacman, mise, or apt)", raw)
	}
}

func brewCommandEnv() map[string]string {
	return map[string]string{
		"HOMEBREW_NO_AUTO_UPDATE": "1",
		"HOMEBREW_NO_ENV_HINTS":   "1",
		"HOMEBREW_NO_COLOR":       "1",
	}
}

func SearchPackages(ctx *app.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	return SearchPackagesWithRunner(ctx, query, opts, ExecRunner{Probe: true})
}

func SearchPackagesWithRunner(ctx *app.Context, query string, opts SearchOptions, runner CommandRunner) ([]SearchResult, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	q, err := validateSearchQuery(query)
	if err != nil {
		return nil, err
	}
	normProv, err := normalizeSearchProvider(opts.Provider)
	if err != nil {
		return nil, err
	}

	var targets []string
	if normProv == "all" {
		targets = AvailableSearchProviders(ctx, runner)
	} else {
		switch normProv {
		case "brew", "cask":
			if hasBin(runner, "brew") {
				targets = []string{normProv}
			}
		case "paru":
			if hasBin(runner, "paru") {
				targets = []string{"paru"}
			}
		case "pacman":
			if hasBin(runner, "pacman") {
				targets = []string{"pacman"}
			}
		case "mise":
			if hasBin(runner, "mise") {
				targets = []string{"mise"}
			}
		case "apt":
			if hasBin(runner, "apt-cache") {
				targets = []string{"apt"}
			}
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no search-capable package provider available for %q", nonEmptyPkgStr(opts.Provider, "this host"))
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		results []SearchResult
	)
	addResults := func(items []SearchResult) {
		if len(items) == 0 {
			return
		}
		mu.Lock()
		results = append(results, items...)
		mu.Unlock()
	}

	wantBrewFormula := slices.Contains(targets, "brew")
	wantBrewCask := slices.Contains(targets, "cask")
	if wantBrewFormula || wantBrewCask {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addResults(searchHomebrew(q, wantBrewFormula, wantBrewCask, runner))
		}()
	}

	for _, prov := range targets {
		switch prov {
		case "paru", "pacman":
			p := prov
			wg.Add(1)
			go func() {
				defer wg.Done()
				addResults(searchArch(q, p, runner))
			}()
		case "mise":
			wg.Add(1)
			go func() {
				defer wg.Done()
				addResults(searchMiseRegistry(ctx, q, runner))
			}()
		case "apt":
			wg.Add(1)
			go func() {
				defer wg.Done()
				addResults(searchApt(q, runner))
			}()
		}
	}

	installedByProv := map[string]map[string]bool{}
	var instMu sync.Mutex
	setInstalled := func(prov string, set map[string]bool) {
		instMu.Lock()
		installedByProv[prov] = set
		instMu.Unlock()
	}

	if wantBrewFormula {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if set, ok := LoadBrewInstalledSet(runner, false); ok {
				setInstalled("brew", set)
			}
		}()
	}
	if wantBrewCask {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if set, ok := LoadBrewInstalledSet(runner, true); ok {
				setInstalled("cask", set)
			}
		}()
	}
	if slices.Contains(targets, "paru") || slices.Contains(targets, "pacman") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if set, ok := LoadArchInstalledSet(runner, "paru"); ok {
				setInstalled("arch", set)
			}
		}()
	}
	if slices.Contains(targets, "apt") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if set, ok := LoadDpkgInstalledSet(runner); ok {
				setInstalled("apt", set)
			}
		}()
	}
	if slices.Contains(targets, "mise") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set := map[string]bool{}
			if stdout, _, rerr := runner.Run([]string{"mise", "ls", "--installed", "--json"}, nil, miseWorkDir(ctx), false); rerr == nil && strings.TrimSpace(stdout) != "" {
				var parsed map[string]json.RawMessage
				if json.Unmarshal([]byte(stdout), &parsed) == nil {
					for k := range parsed {
						set[strings.ToLower(strings.TrimSpace(k))] = true
					}
				}
			} else if stdout, _, rerr := runner.Run([]string{"mise", "ls", "--installed"}, nil, miseWorkDir(ctx), false); rerr == nil {
				for _, line := range splitNonEmptyLines(stdout) {
					fields := strings.Fields(line)
					if len(fields) > 0 {
						set[strings.ToLower(fields[0])] = true
					}
				}
			}
			setInstalled("mise", set)
		}()
	}

	wg.Wait()

	presetID := ""
	if ctx != nil {
		presetID = ctx.EffectivePreset()
	}
	isManaged := buildManagedPackageMatcher(ctx, presetID, runner)

	seen := map[string]bool{}
	deduped := make([]SearchResult, 0, len(results))
	for _, r := range results {
		nameLow := strings.ToLower(strings.TrimSpace(r.Name))
		provLow := strings.ToLower(strings.TrimSpace(r.Provider))
		if nameLow == "" || provLow == "" {
			continue
		}
		key := provLow + ":" + nameLow + ":" + strings.ToLower(r.Repo)
		if seen[key] {
			continue
		}
		seen[key] = true

		switch provLow {
		case "brew":
			if matchBrewInstalled(installedByProv["brew"], r.Name, "homebrew/core/") {
				r.Installed = true
			}
		case "cask":
			if matchBrewInstalled(installedByProv["cask"], r.Name, "homebrew/cask/") {
				r.Installed = true
			}
		case "paru", "pacman", "aur":
			if installedByProv["arch"][nameLow] {
				r.Installed = true
			}
		case "apt":
			if installedByProv["apt"][nameLow] {
				r.Installed = true
			}
		case "mise":
			if installedByProv["mise"][nameLow] {
				r.Installed = true
			}
		}
		if isManaged != nil && isManaged(r.Name) {
			r.Tracked = true
		}
		deduped = append(deduped, r)
	}

	rankSearchResults(deduped, q)
	if len(deduped) > limit {
		deduped = deduped[:limit]
	}
	return deduped, nil
}

func searchHomebrew(query string, wantFormula, wantCask bool, runner CommandRunner) []SearchResult {
	env := brewCommandEnv()
	var cmd []string
	defaultProv := ""
	switch {
	case wantFormula && wantCask:
		cmd = []string{"brew", "desc", "--search", "--", query}
	case wantFormula:
		cmd = []string{"brew", "desc", "--search", "--formula", "--", query}
		defaultProv = "brew"
	case wantCask:
		cmd = []string{"brew", "desc", "--search", "--cask", "--", query}
		defaultProv = "cask"
	default:
		return nil
	}

	if stdout, _, err := runner.Run(cmd, env, "", false); err == nil {
		if items := parseBrewDescSearch(stdout, defaultProv); len(items) > 0 {
			return items
		}
	}

	var fallback []SearchResult
	if wantFormula {
		if stdout, _, err := runner.Run([]string{"brew", "search", "--formula", "--", query}, env, "", false); err == nil {
			fallback = append(fallback, parseBrewNameSearch(stdout, "brew")...)
		}
	}
	if wantCask {
		if stdout, _, err := runner.Run([]string{"brew", "search", "--cask", "--", query}, env, "", false); err == nil {
			fallback = append(fallback, parseBrewNameSearch(stdout, "cask")...)
		}
	}
	return fallback
}

func parseBrewDescSearch(stdout, defaultProv string) []SearchResult {
	var out []SearchResult
	curProv := defaultProv
	for _, rawLine := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "==>") {
			hdr := strings.ToLower(line)
			switch {
			case strings.Contains(hdr, "formula"):
				curProv = "brew"
			case strings.Contains(hdr, "cask"):
				curProv = "cask"
			}
			continue
		}
		namePart, descPart, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		installed := strings.Contains(namePart, "✔")
		name := strings.TrimSpace(strings.ReplaceAll(namePart, "✔", ""))
		if name == "" {
			continue
		}
		prov := curProv
		if prov == "" {
			prov = "brew"
		}
		repo := "homebrew/core"
		if prov == "cask" {
			repo = "homebrew/cask"
		}
		out = append(out, SearchResult{
			Name:        name,
			Provider:    prov,
			Repo:        repo,
			Description: strings.TrimSpace(descPart),
			Installed:   installed,
		})
	}
	return out
}

func parseBrewNameSearch(stdout, prov string) []SearchResult {
	var out []SearchResult
	repo := "homebrew/core"
	if prov == "cask" {
		repo = "homebrew/cask"
	}
	for _, rawLine := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "==>") {
			continue
		}
		for _, tok := range strings.Fields(line) {
			if tok == "✔" {
				continue
			}
			installed := strings.HasSuffix(tok, "✔")
			name := strings.TrimSpace(strings.TrimSuffix(tok, "✔"))
			if name == "" {
				continue
			}
			out = append(out, SearchResult{
				Name:      name,
				Provider:  prov,
				Repo:      repo,
				Installed: installed,
			})
		}
	}
	return out
}

func searchArch(query, bin string, runner CommandRunner) []SearchResult {
	stdout, _, err := runner.Run([]string{bin, "--color=never", "-Ss", "--", query}, nil, "", false)
	if err != nil && strings.TrimSpace(stdout) == "" {
		return nil
	}
	return parseArchSearchOutput(stdout, bin)
}

func parseArchSearchOutput(stdout, bin string) []SearchResult {
	var out []SearchResult
	lines := strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n")
	var current *SearchResult
	flush := func() {
		if current != nil && current.Name != "" {
			out = append(out, *current)
		}
		current = nil
	}
	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if raw[0] == ' ' || raw[0] == '\t' {
			if current != nil {
				desc := strings.TrimSpace(raw)
				if desc != "" {
					if current.Description != "" {
						current.Description += " "
					}
					current.Description += desc
				}
			}
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		repo, name, ok := strings.Cut(fields[0], "/")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		flush()
		version := ""
		if len(fields) >= 2 && !strings.HasPrefix(fields[1], "[") && !strings.HasPrefix(fields[1], "(") {
			version = fields[1]
		}
		prov := bin
		if strings.EqualFold(repo, "aur") {
			prov = "paru"
		}
		rest := strings.ToLower(strings.Join(fields[1:], " "))
		current = &SearchResult{
			Name:      name,
			Provider:  prov,
			Repo:      repo,
			Version:   version,
			Installed: strings.Contains(rest, "[installed"),
		}
	}
	flush()
	return out
}

func searchMiseRegistry(ctx *app.Context, query string, runner CommandRunner) []SearchResult {
	stdout, _, err := runner.Run([]string{"mise", "registry"}, nil, miseWorkDir(ctx), false)
	if err != nil && strings.TrimSpace(stdout) == "" {
		return nil
	}
	qLow := strings.ToLower(query)
	type scored struct {
		item  SearchResult
		score int
	}
	var matched []scored
	for _, raw := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		backends := strings.Join(fields[1:], " ")
		res := fuzzy.Match(name, query, fuzzy.SchemeDefault, false)
		score := -1
		if res.Matched {
			score = 1000 + res.Score
			if strings.EqualFold(name, query) {
				score += 2000
			} else if strings.HasPrefix(strings.ToLower(name), qLow) {
				score += 1000
			}
		} else if strings.Contains(strings.ToLower(backends), qLow) {
			score = 100
		}
		if score < 0 {
			continue
		}
		repo := "mise"
		if len(fields) > 1 {
			if backend, _, hasColon := strings.Cut(fields[1], ":"); hasColon && backend != "" {
				repo = backend
			}
		}
		matched = append(matched, scored{
			item: SearchResult{
				Name:        name,
				Provider:    "mise",
				Repo:        repo,
				Description: backends,
			},
			score: score,
		})
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].score != matched[j].score {
			return matched[i].score > matched[j].score
		}
		return matched[i].item.Name < matched[j].item.Name
	})
	limit := min(40, len(matched))
	out := make([]SearchResult, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, matched[i].item)
	}
	return out
}

func searchApt(query string, runner CommandRunner) []SearchResult {
	stdout, _, err := runner.Run([]string{"apt-cache", "search", "--", query}, nil, "", false)
	if err != nil && strings.TrimSpace(stdout) == "" {
		return nil
	}
	var out []SearchResult
	for _, line := range splitNonEmptyLines(stdout) {
		name, desc, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, SearchResult{
			Name:        name,
			Provider:    "apt",
			Repo:        "apt",
			Description: strings.TrimSpace(desc),
		})
	}
	return out
}

func rankSearchResults(results []SearchResult, query string) {
	qLow := strings.ToLower(strings.TrimSpace(query))
	scoreOf := func(r SearchResult) int {
		nameLow := strings.ToLower(r.Name)
		base := 0
		if nameLow == qLow {
			base = 50000
		} else if strings.HasPrefix(nameLow, qLow) {
			base = 30000 - len(nameLow)
		} else if res := fuzzy.Match(r.Name, query, fuzzy.SchemeDefault, false); res.Matched {
			base = 10000 + res.Score
		} else if strings.Contains(strings.ToLower(r.Description), qLow) {
			base = 1000
		}
		if r.Installed {
			base += 200
		}
		if !strings.EqualFold(r.Repo, "aur") {
			base += 50
		}
		return base
	}
	sort.SliceStable(results, func(i, j int) bool {
		si, sj := scoreOf(results[i]), scoreOf(results[j])
		if si != sj {
			return si > sj
		}
		if results[i].Name != results[j].Name {
			return results[i].Name < results[j].Name
		}
		return results[i].Provider < results[j].Provider
	})
}

func GetPackageInfo(ctx *app.Context, pkgName, provider string) (PackageInfo, error) {
	return GetPackageInfoWithRunner(ctx, pkgName, provider, ExecRunner{Probe: true})
}

func GetPackageInfoWithRunner(ctx *app.Context, pkgName, provider string, runner CommandRunner) (PackageInfo, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	name, err := validatePkgQueryName(pkgName)
	if err != nil {
		return PackageInfo{}, err
	}

	var candidates []string
	trimmedProv := strings.TrimSpace(provider)
	if trimmedProv != "" && !strings.EqualFold(trimmedProv, "all") {
		norm, perr := normalizeSearchProvider(trimmedProv)
		if perr != nil {
			return PackageInfo{}, perr
		}
		candidates = []string{norm}
	} else {
		candidates = AvailableSearchProviders(ctx, runner)
	}
	if len(candidates) == 0 {
		return PackageInfo{}, fmt.Errorf("no supported package manager found to inspect %q", name)
	}

	var lastErr error
	for _, prov := range candidates {
		info, ok, ierr := inspectPackageByProvider(ctx, name, prov, runner)
		if ierr != nil {
			lastErr = ierr
		}
		if ok {
			return info, nil
		}
	}
	if lastErr != nil {
		return PackageInfo{}, lastErr
	}
	return PackageInfo{}, fmt.Errorf("package %q not found in %s", name, strings.Join(candidates, ", "))
}

func inspectPackageByProvider(ctx *app.Context, name, provider string, runner CommandRunner) (PackageInfo, bool, error) {
	switch provider {
	case "brew", "cask":
		return inspectBrewPackage(name, provider == "cask", runner)
	case "paru", "pacman", "aur":
		return inspectArchPackage(name, provider, runner)
	case "mise":
		return inspectMisePackage(ctx, name, runner)
	case "apt":
		return inspectAptPackage(name, runner)
	default:
		return PackageInfo{}, false, nil
	}
}

func inspectBrewPackage(name string, isCask bool, runner CommandRunner) (PackageInfo, bool, error) {
	if !hasBin(runner, "brew") {
		return PackageInfo{}, false, nil
	}
	flag := "--formula"
	prov := "brew"
	repo := "homebrew/core"
	if isCask {
		flag = "--cask"
		prov = "cask"
		repo = "homebrew/cask"
	}
	stdout, _, err := runner.Run([]string{"brew", "info", "--json=v2", flag, "--", name}, brewCommandEnv(), "", false)
	if err != nil || strings.TrimSpace(stdout) == "" {
		return PackageInfo{}, false, err
	}
	info := PackageInfo{Name: name, Provider: prov, Repo: repo}
	if !isCask {
		var payload struct {
			Formulae []struct {
				Name      string   `json:"name"`
				FullName  string   `json:"full_name"`
				Tap       string   `json:"tap"`
				Desc      string   `json:"desc"`
				Homepage  string   `json:"homepage"`
				License   string   `json:"license"`
				Caveats   string   `json:"caveats"`
				Deps      []string `json:"dependencies"`
				Conflicts []struct {
					Name string `json:"name"`
				} `json:"conflicts_with"`
				Versions struct {
					Stable string `json:"stable"`
				} `json:"versions"`
				Installed []struct {
					Version string `json:"version"`
				} `json:"installed"`
			} `json:"formulae"`
		}
		if json.Unmarshal([]byte(stdout), &payload) != nil || len(payload.Formulae) == 0 {
			return PackageInfo{}, false, nil
		}
		f := payload.Formulae[0]
		if f.FullName != "" {
			info.Name = f.FullName
		} else if f.Name != "" {
			info.Name = f.Name
		}
		if f.Tap != "" {
			info.Repo = f.Tap
		}
		info.Description = f.Desc
		info.Homepage = f.Homepage
		info.License = f.License
		info.LatestVersion = f.Versions.Stable
		if len(f.Installed) > 0 {
			info.Installed = true
			info.Version = f.Installed[0].Version
		} else {
			info.Version = f.Versions.Stable
		}
		info.Dependencies = append([]string(nil), f.Deps...)
		for _, c := range f.Conflicts {
			if c.Name != "" {
				info.Conflicts = append(info.Conflicts, c.Name)
			}
		}
		info.Caveats = strings.TrimSpace(f.Caveats)
		return info, true, nil
	}

	var payload struct {
		Casks []struct {
			Token     string          `json:"token"`
			Tap       string          `json:"tap"`
			Desc      string          `json:"desc"`
			Homepage  string          `json:"homepage"`
			Version   string          `json:"version"`
			Installed string          `json:"installed"`
			Caveats   string          `json:"caveats"`
			Artifacts json.RawMessage `json:"artifacts"`
			DependsOn struct {
				Formula []string `json:"formula"`
				Cask    []string `json:"cask"`
			} `json:"depends_on"`
		} `json:"casks"`
	}
	if json.Unmarshal([]byte(stdout), &payload) != nil || len(payload.Casks) == 0 {
		return PackageInfo{}, false, nil
	}
	c := payload.Casks[0]
	if c.Token != "" {
		info.Name = c.Token
	}
	if c.Tap != "" {
		info.Repo = c.Tap
	}
	info.Description = c.Desc
	info.Homepage = c.Homepage
	info.LatestVersion = c.Version
	if strings.TrimSpace(c.Installed) != "" {
		info.Installed = true
		info.Version = c.Installed
	} else {
		info.Version = c.Version
	}
	info.Caveats = strings.TrimSpace(c.Caveats)
	info.Dependencies = append(info.Dependencies, c.DependsOn.Formula...)
	for _, dc := range c.DependsOn.Cask {
		info.Dependencies = append(info.Dependencies, "cask:"+dc)
	}
	if len(c.Artifacts) > 0 {
		var arts []map[string]json.RawMessage
		if json.Unmarshal(c.Artifacts, &arts) == nil {
			for _, m := range arts {
				for kind, val := range m {
					var items []string
					if json.Unmarshal(val, &items) == nil && len(items) > 0 {
						info.Artifacts = append(info.Artifacts, kind+": "+strings.Join(items, ", "))
					}
				}
			}
		}
	}
	return info, true, nil
}

func inspectArchPackage(name, provider string, runner CommandRunner) (PackageInfo, bool, error) {
	bin := "pacman"
	if provider != "pacman" && hasBin(runner, "paru") {
		bin = "paru"
	} else if !hasBin(runner, "pacman") {
		if hasBin(runner, "paru") {
			bin = "paru"
		} else {
			return PackageInfo{}, false, nil
		}
	}

	installed := false
	rawOut, _, err := runner.Run([]string{bin, "--color=never", "-Qi", "--", name}, nil, "", false)
	if err == nil && strings.TrimSpace(rawOut) != "" {
		installed = true
	} else {
		rawOut, _, err = runner.Run([]string{bin, "--color=never", "-Si", "--", name}, nil, "", false)
		if err != nil || strings.TrimSpace(rawOut) == "" {
			return PackageInfo{}, false, err
		}
	}
	info := parsePacmanKeyValues(rawOut, bin, installed)
	if info.Name == "" {
		info.Name = name
	}
	return info, true, nil
}

func parsePacmanKeyValues(stdout, bin string, installed bool) PackageInfo {
	fields := map[string][]string{}
	var order []string
	var lastKey string
	for _, rawLine := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(rawLine) == "" {
			continue
		}
		if rawLine[0] != ' ' && rawLine[0] != '\t' {
			k, v, ok := strings.Cut(rawLine, ":")
			if !ok {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(k))
			val := strings.TrimSpace(v)
			if _, exists := fields[key]; !exists {
				order = append(order, key)
			}
			if val != "" {
				fields[key] = append(fields[key], val)
			}
			lastKey = key
			continue
		}
		if lastKey != "" {
			if cont := strings.TrimSpace(rawLine); cont != "" {
				fields[lastKey] = append(fields[lastKey], cont)
			}
		}
	}
	_ = order

	firstVal := func(k string) string {
		if len(fields[k]) == 0 {
			return ""
		}
		return strings.Join(fields[k], " ")
	}
	splitPkgList := func(k string) []string {
		joined := strings.TrimSpace(firstVal(k))
		if joined == "" || strings.EqualFold(joined, "None") {
			return nil
		}
		var out []string
		for _, tok := range strings.Fields(joined) {
			if tok != "" && !strings.EqualFold(tok, "None") {
				out = append(out, tok)
			}
		}
		return out
	}

	repo := firstVal("repository")
	prov := bin
	if strings.EqualFold(repo, "aur") {
		prov = "paru"
	}
	var optDeps []string
	for _, line := range fields["optional deps"] {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.EqualFold(trimmed, "None") {
			optDeps = append(optDeps, trimmed)
		}
	}

	ver := firstVal("version")
	return PackageInfo{
		Name:          firstVal("name"),
		Provider:      prov,
		Repo:          repo,
		Version:       ver,
		LatestVersion: ver,
		Installed:     installed,
		Description:   firstVal("description"),
		Homepage:      firstVal("url"),
		License:       firstVal("licenses"),
		InstalledSize: firstVal("installed size"),
		Dependencies:  splitPkgList("depends on"),
		OptionalDeps:  optDeps,
		RequiredBy:    splitPkgList("required by"),
		Conflicts:     splitPkgList("conflicts with"),
		RawInfo:       strings.TrimSpace(stdout),
	}
}

func inspectMisePackage(ctx *app.Context, name string, runner CommandRunner) (PackageInfo, bool, error) {
	if !hasBin(runner, "mise") {
		return PackageInfo{}, false, nil
	}
	wd := miseWorkDir(ctx)
	regOut, _, regErr := runner.Run([]string{"mise", "registry", name}, nil, wd, false)
	lsOut, _, lsErr := runner.Run([]string{"mise", "ls", "--current", name}, nil, wd, false)
	if regErr != nil && lsErr != nil {
		return PackageInfo{}, false, regErr
	}
	if strings.TrimSpace(regOut) == "" && strings.TrimSpace(lsOut) == "" {
		return PackageInfo{}, false, nil
	}
	info := PackageInfo{
		Name:        name,
		Provider:    "mise",
		Repo:        "mise-registry",
		Description: strings.TrimSpace(regOut),
		RawInfo:     strings.TrimSpace(lsOut),
	}
	for _, line := range splitNonEmptyLines(lsOut) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], name) {
			info.Installed = true
			info.Version = fields[1]
			break
		}
	}
	return info, true, nil
}

func inspectAptPackage(name string, runner CommandRunner) (PackageInfo, bool, error) {
	if !hasBin(runner, "apt-cache") {
		return PackageInfo{}, false, nil
	}
	stdout, _, err := runner.Run([]string{"apt-cache", "show", "--", name}, nil, "", false)
	if err != nil || strings.TrimSpace(stdout) == "" {
		return PackageInfo{}, false, err
	}
	info := PackageInfo{Name: name, Provider: "apt", Repo: "apt", RawInfo: strings.TrimSpace(stdout)}
	for _, line := range strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			break
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		switch key {
		case "package":
			info.Name = val
		case "version":
			info.Version = val
			info.LatestVersion = val
		case "homepage":
			info.Homepage = val
		case "description":
			info.Description = val
		case "installed-size":
			info.InstalledSize = val + " KiB"
		case "depends":
			for _, dep := range strings.Split(val, ",") {
				if d := strings.TrimSpace(dep); d != "" {
					info.Dependencies = append(info.Dependencies, d)
				}
			}
		case "conflicts":
			for _, c := range strings.Split(val, ",") {
				if d := strings.TrimSpace(c); d != "" {
					info.Conflicts = append(info.Conflicts, d)
				}
			}
		}
	}
	if hasBin(runner, "dpkg-query") {
		if out, _, derr := runner.Run([]string{"dpkg-query", "-W", "-f=${Status}\t${Version}", name}, nil, "", false); derr == nil {
			parts := strings.SplitN(strings.TrimSpace(out), "\t", 2)
			if len(parts) >= 1 && strings.TrimSpace(parts[0]) == "install ok installed" {
				info.Installed = true
				if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
					info.Version = strings.TrimSpace(parts[1])
				}
			}
		}
	}
	return info, true, nil
}

func ListInstalledPackages(ctx *app.Context, provider string) ([]SearchResult, error) {
	return ListInstalledPackagesWithRunner(ctx, provider, ExecRunner{Probe: true})
}

func ListInstalledPackagesWithRunner(ctx *app.Context, provider string, runner CommandRunner) ([]SearchResult, error) {
	if runner == nil {
		runner = ExecRunner{Probe: true}
	}
	normProv, err := normalizeSearchProvider(provider)
	if err != nil {
		return nil, err
	}
	var targets []string
	if normProv == "all" {
		targets = AvailableSearchProviders(ctx, runner)
	} else {
		targets = []string{normProv}
	}

	var out []SearchResult
	if slices.Contains(targets, "brew") && hasBin(runner, "brew") {
		if stdout, _, rerr := runner.Run([]string{"brew", "list", "--formula", "--versions"}, brewCommandEnv(), "", false); rerr == nil {
			for _, line := range splitNonEmptyLines(stdout) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					ver := ""
					if len(fields) > 1 {
						ver = fields[len(fields)-1]
					}
					out = append(out, SearchResult{Name: fields[0], Provider: "brew", Repo: "homebrew/core", Version: ver, Installed: true})
				}
			}
		}
	}
	if slices.Contains(targets, "cask") && hasBin(runner, "brew") {
		if stdout, _, rerr := runner.Run([]string{"brew", "list", "--cask", "--versions"}, brewCommandEnv(), "", false); rerr == nil {
			for _, line := range splitNonEmptyLines(stdout) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					ver := ""
					if len(fields) > 1 {
						ver = fields[len(fields)-1]
					}
					out = append(out, SearchResult{Name: fields[0], Provider: "cask", Repo: "homebrew/cask", Version: ver, Installed: true})
				}
			}
		}
	}
	if (slices.Contains(targets, "paru") || slices.Contains(targets, "pacman")) && (hasBin(runner, "paru") || hasBin(runner, "pacman")) {
		qBin := "pacman"
		if !hasBin(runner, "pacman") || (slices.Contains(targets, "paru") && hasBin(runner, "paru")) {
			qBin = "paru"
		}
		if stdout, _, rerr := runner.Run([]string{qBin, "-Q"}, nil, "", false); rerr == nil {
			for _, line := range splitNonEmptyLines(stdout) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					ver := ""
					if len(fields) > 1 {
						ver = fields[1]
					}
					out = append(out, SearchResult{Name: fields[0], Provider: qBin, Version: ver, Installed: true})
				}
			}
		}
	}
	if slices.Contains(targets, "apt") && hasBin(runner, "dpkg-query") {
		if stdout, _, rerr := runner.Run([]string{"dpkg-query", "-W", "-f=${Status}\t${Package}\t${Version}\n"}, nil, "", false); rerr == nil {
			for _, line := range splitNonEmptyLines(stdout) {
				parts := strings.SplitN(line, "\t", 3)
				if len(parts) >= 2 && strings.TrimSpace(parts[0]) == "install ok installed" {
					ver := ""
					if len(parts) == 3 {
						ver = strings.TrimSpace(parts[2])
					}
					out = append(out, SearchResult{Name: strings.TrimSpace(parts[1]), Provider: "apt", Repo: "apt", Version: ver, Installed: true})
				}
			}
		}
	}
	if slices.Contains(targets, "mise") && hasBin(runner, "mise") {
		if stdout, _, rerr := runner.Run([]string{"mise", "ls", "--installed"}, nil, miseWorkDir(ctx), false); rerr == nil {
			for _, line := range splitNonEmptyLines(stdout) {
				fields := strings.Fields(line)
				if len(fields) > 0 {
					ver := ""
					if len(fields) > 1 {
						ver = fields[1]
					}
					out = append(out, SearchResult{Name: fields[0], Provider: "mise", Repo: "mise", Version: ver, Installed: true})
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

func nonEmptyPkgStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
