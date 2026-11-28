package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igwtcode/workstation/ws/internal/app"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
	"github.com/igwtcode/workstation/ws/internal/shell"
	"github.com/igwtcode/workstation/ws/internal/store"
)

type LocalFileEntry struct {
	RelPath   string `json:"relPath"`
	SizeBytes int64  `json:"sizeBytes"`
	BackedUp  bool   `json:"backedUp"`
	IsSecret  bool   `json:"isSecret"`
}

type RepoRecord struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Zone            string           `json:"zone"`
	Forge           string           `json:"forge"`
	RemoteURL       string           `json:"remoteUrl"`
	LocalPath       string           `json:"localPath"`
	Branch          string           `json:"branch"`
	Dirty           bool             `json:"dirty"`
	Ahead           int              `json:"ahead"`
	Behind          int              `json:"behind"`
	UntrackedLocals []LocalFileEntry `json:"untrackedLocals,omitempty"`
	ScannedAt       int64            `json:"scannedAt"`
}

const (
	maxScanWorkers = 8
	maxRepoDepth   = 5
	maxLocalDepth  = 3
)

var httpUserinfoFallbackRe = regexp.MustCompile(`(?i)^(https?://)([^/@\s]+@)`)

func StripURLCredentials(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !strings.Contains(trimmed, "://") {
		return trimmed
	}
	u, err := url.Parse(trimmed)
	if err == nil {
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			return trimmed
		}
		if u.User == nil {
			return trimmed
		}
		u.User = nil
		return u.String()
	}
	if httpUserinfoFallbackRe.MatchString(trimmed) {
		return httpUserinfoFallbackRe.ReplaceAllString(trimmed, "$1")
	}
	return trimmed
}

func DiscoverReposFast(ctx *app.Context, db *store.DB) ([]RepoRecord, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}

	discovered, err := discoverAllReposWith(ctx, shell.DiscoverReposFast)
	if err != nil {
		return nil, err
	}

	cachedByPath := map[string]RepoRecord{}
	if db != nil {
		if cached, cerr := ListCachedRepos(db); cerr == nil {
			for _, r := range cached {
				cachedByPath[filepath.Clean(r.LocalPath)] = r
			}
		}
	}

	records := make([]RepoRecord, 0, len(discovered))
	for _, entry := range discovered {
		cleanPath := filepath.Clean(entry.Path)
		rec := RepoRecord{
			ID:        filepath.ToSlash(ctx.CollapseHome(cleanPath)),
			Name:      entry.Name,
			Zone:      entry.Zone,
			LocalPath: cleanPath,
		}
		if cached, ok := cachedByPath[cleanPath]; ok {
			rec.Forge = cached.Forge
			rec.RemoteURL = cached.RemoteURL
			rec.Branch = cached.Branch
			rec.Dirty = cached.Dirty
			rec.Ahead = cached.Ahead
			rec.Behind = cached.Behind
			rec.UntrackedLocals = cached.UntrackedLocals
			rec.ScannedAt = cached.ScannedAt
		}
		if liveBranch := readGitBranchFast(cleanPath); liveBranch != "" {
			rec.Branch = liveBranch
		}
		if rec.Forge == "" && rec.RemoteURL != "" {
			rec.Forge = DetectForge(rec.RemoteURL)
		}
		records = append(records, rec)
	}
	return records, nil
}

func readGitBranchFast(repoPath string) string {
	headPath := filepath.Join(repoPath, ".git", "HEAD")
	raw, err := os.ReadFile(headPath)
	if err != nil {

		dotGitRaw, derr := os.ReadFile(filepath.Join(repoPath, ".git"))
		if derr != nil {
			return ""
		}
		for _, ln := range strings.Split(string(dotGitRaw), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "gitdir:") {
				gitDir := strings.TrimSpace(strings.TrimPrefix(ln, "gitdir:"))
				if gitDir == "" {
					break
				}
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(repoPath, filepath.FromSlash(gitDir))
				}
				raw, err = os.ReadFile(filepath.Join(filepath.Clean(gitDir), "HEAD"))
				break
			}
		}
		if err != nil {
			return ""
		}
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
		if strings.HasPrefix(ref, "refs/heads/") {
			return strings.TrimPrefix(ref, "refs/heads/")
		}
		return ref
	}
	if isHexSHA(line) && len(line) >= 7 {
		return line[:7]
	}
	return line
}

func isHexSHA(s string) bool {
	if len(s) < 7 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func ScanRepos(ctx *app.Context, db *store.DB) ([]RepoRecord, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is required")
	}

	discovered, err := discoverAllRepos(ctx)
	if err != nil {
		return nil, err
	}

	backedUp := loadBackedUpEnvPaths(ctx, db)
	now := time.Now().Unix()

	records := make([]RepoRecord, len(discovered))
	if len(discovered) > 0 {
		workers := maxScanWorkers
		if len(discovered) < workers {
			workers = len(discovered)
		}

		jobs := make(chan int, len(discovered))
		for i := range discovered {
			jobs <- i
		}
		close(jobs)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					records[idx] = inspectRepo(ctx, discovered[idx], backedUp, now)
				}
			}()
		}
		wg.Wait()
	}

	if db != nil && !ctx.DryRun {
		if err := replaceCachedRepos(db, records); err != nil {
			return records, err
		}
	}

	return records, nil
}

func discoverAllRepos(ctx *app.Context) ([]shell.RepoEntry, error) {
	return discoverAllReposWith(ctx, shell.DiscoverRepos)
}

func discoverAllReposWith(ctx *app.Context, discoverFn func(*app.Context) ([]shell.RepoEntry, error)) ([]shell.RepoEntry, error) {
	entries, err := discoverFn(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(entries))
	out := make([]shell.RepoEntry, 0, len(entries))
	for _, e := range entries {
		clean := filepath.Clean(e.Path)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		e.Path = clean
		out = append(out, e)
	}

	flatCodeRoot := filepath.Join(ctx.HomeDir, "code")
	flatPaths := findGitReposShallow(flatCodeRoot, maxRepoDepth)
	for _, p := range flatPaths {
		clean := filepath.Clean(p)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, shell.RepoEntry{
			Zone:    "",
			Path:    clean,
			Display: ctx.CollapseHome(clean),
			Name:    filepath.Base(clean),
		})
	}
	return out, nil
}

func findGitReposShallow(root string, maxDepth int) []string {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil
	}
	var repos []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == ".git" {
				repos = append(repos, dir)
				return
			}
		}
		for _, e := range entries {
			if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "target" ||
				name == "vendor" || name == ".venv" || name == "venv" || name == "dist" ||
				name == "build" || name == ".next" || name == ".cache" {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}
	walk(filepath.Clean(root), 0)
	sort.Strings(repos)
	return repos
}

func loadBackedUpEnvPaths(ctx *app.Context, db *store.DB) map[string]bool {
	out := map[string]bool{}
	if db == nil {
		return out
	}
	blobs, err := db.ListSecretBlobs("envfile")
	if err != nil {
		return out
	}
	for _, b := range blobs {
		if b.SourcePath == "" {
			continue
		}
		out[filepath.Clean(b.SourcePath)] = true
		expanded := filepath.Clean(ctx.ExpandHome(b.SourcePath))
		out[expanded] = true
		out[filepath.ToSlash(ctx.CollapseHome(expanded))] = true
	}
	return out
}

func inspectRepo(ctx *app.Context, entry shell.RepoEntry, backedUp map[string]bool, scannedAt int64) RepoRecord {
	cleanPath := filepath.Clean(entry.Path)
	id := filepath.ToSlash(ctx.CollapseHome(cleanPath))

	branch := strings.TrimSpace(runGitQuiet(cleanPath, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch == "" {
		branch = strings.TrimSpace(runGitQuiet(cleanPath, "symbolic-ref", "--short", "HEAD"))
	}

	statusOut := runGitQuiet(cleanPath, "status", "--porcelain")
	dirty := len(strings.TrimSpace(statusOut)) > 0

	var ahead, behind int
	if revOut := strings.TrimSpace(runGitQuiet(cleanPath, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")); revOut != "" {
		fields := strings.Fields(revOut)
		if len(fields) == 2 {
			if a, err := strconv.Atoi(fields[0]); err == nil {
				ahead = a
			}
			if b, err := strconv.Atoi(fields[1]); err == nil {
				behind = b
			}
		}
	}

	remoteURL := StripURLCredentials(runGitQuiet(cleanPath, "remote", "get-url", "origin"))
	forge := DetectForge(remoteURL)
	locals := scanLocalFiles(ctx, cleanPath, backedUp)

	return RepoRecord{
		ID:              id,
		Name:            entry.Name,
		Zone:            entry.Zone,
		Forge:           forge,
		RemoteURL:       remoteURL,
		LocalPath:       cleanPath,
		Branch:          branch,
		Dirty:           dirty,
		Ahead:           ahead,
		Behind:          behind,
		UntrackedLocals: locals,
		ScannedAt:       scannedAt,
	}
}

func runGitQuiet(repoPath string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full := append([]string{"-C", repoPath}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = shell.MergeEnv(os.Environ(), []string{"GIT_TERMINAL_PROMPT=0"})
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func scanLocalFiles(ctx *app.Context, repoRoot string, backedUp map[string]bool) []LocalFileEntry {
	var found []LocalFileEntry
	seen := map[string]bool{}

	addEntry := func(relSlash, full string, info os.FileInfo) {
		if seen[relSlash] || hasHeavyOrVCSDirSegment(relSlash) {
			return
		}
		name := filepath.Base(relSlash)
		if name == ".DS_Store" {
			return
		}
		matched, isSecret := classifyLocalFile(name, relSlash)
		if !matched {
			return
		}
		if info == nil {
			fi, err := os.Lstat(full)
			if err != nil || fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return
			}
			info = fi
		}
		seen[relSlash] = true
		cleanFull := filepath.Clean(full)
		isBackedUp := backedUp[cleanFull] || backedUp[filepath.ToSlash(ctx.CollapseHome(cleanFull))]
		found = append(found, LocalFileEntry{
			RelPath:   relSlash,
			SizeBytes: info.Size(),
			BackedUp:  isBackedUp,
			IsSecret:  isSecret,
		})
	}

	tracked := map[string]bool{}
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err == nil {
		if tOut := runGitQuiet(repoRoot, "ls-files", "-z"); tOut != "" {
			for _, raw := range strings.Split(tOut, "\x00") {
				if raw == "" {
					continue
				}
				tracked[filepath.ToSlash(filepath.Clean(raw))] = true
			}
		}
		out := runGitQuiet(
			repoRoot,
			"ls-files", "--others", "--ignored", "--exclude-standard", "-z",
			"--", ".",
			":(exclude)node_modules", ":(exclude)target", ":(exclude).venv", ":(exclude)venv",
			":(exclude)dist", ":(exclude)build", ":(exclude).next", ":(exclude).cache",
			":(exclude)vendor", ":(exclude).terraform", ":(exclude).idea", ":(exclude).vscode",
		)
		if out != "" {
			for _, raw := range strings.Split(out, "\x00") {
				if raw == "" {
					continue
				}
				relSlash := filepath.ToSlash(filepath.Clean(raw))
				if tracked[relSlash] || strings.HasPrefix(relSlash, "/") || relSlash == ".." || strings.HasPrefix(relSlash, "../") {
					continue
				}
				full := filepath.Join(repoRoot, filepath.FromSlash(relSlash))
				addEntry(relSlash, full, nil)
			}
		}
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(dir, name)
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				if isSkippedLocalDir(name) || depth >= maxLocalDepth {
					continue
				}
				walk(full, depth+1)
				continue
			}

			rel, err := filepath.Rel(repoRoot, full)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(filepath.Clean(rel))
			if tracked[relSlash] {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			addEntry(relSlash, full, info)
		}
	}

	walk(repoRoot, 0)
	sort.Slice(found, func(i, j int) bool {
		return found[i].RelPath < found[j].RelPath
	})
	return found
}

var heavyOrVCSSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
	".cache":       true,
	"vendor":       true,
	".terraform":   true,
	".idea":        true,
	".vscode":      true,
}

var curatedSecretDotDirs = map[string]bool{
	".secrets": true,
	".env":     true,
	".config":  true,
	".local":   true,
	".keys":    true,
}

func isSkippedLocalDir(name string) bool {
	if heavyOrVCSSkipDirs[name] {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return !curatedSecretDotDirs[strings.ToLower(name)]
	}
	return false
}

func hasHeavyOrVCSDirSegment(relSlash string) bool {
	dir := filepath.ToSlash(filepath.Dir(relSlash))
	if dir == "." || dir == "" {
		return false
	}
	for _, seg := range strings.Split(dir, "/") {
		if heavyOrVCSSkipDirs[seg] {
			return true
		}
	}
	return false
}

func isEnvTemplateFilename(lower string) bool {
	for _, suf := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(lower, suf) || strings.HasSuffix(lower, suf+".env") || strings.Contains(lower, suf+".") {
			return true
		}
	}
	switch lower {
	case "example.env", "sample.env", "template.env", "dist.env":
		return true
	}
	return false
}

func classifyLocalFile(name, relSlash string) (matched bool, isSecret bool) {
	lower := strings.ToLower(name)
	switch {
	case lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".env"):
		if isEnvTemplateFilename(lower) {
			return false, false
		}
		return true, true
	case lower == "mise.local.toml":
		return true, true
	case lower == "terraform.tfvars":
		return true, true
	case strings.HasPrefix(relSlash, ".local/") || strings.Contains(relSlash, "/.local/"):
		return true, hasSecretFilenameHint(lower)
	case strings.HasPrefix(relSlash, ".secrets/") || strings.Contains(relSlash, "/.secrets/"):
		return !isEnvTemplateFilename(lower), true
	case strings.HasPrefix(relSlash, ".keys/") || strings.Contains(relSlash, "/.keys/"):
		return !isEnvTemplateFilename(lower), true
	case strings.HasSuffix(lower, ".local.json") || strings.HasSuffix(lower, ".local.yaml") || strings.HasSuffix(lower, ".local.yml"):
		return true, hasSecretFilenameHint(lower)
	default:
		return false, false
	}
}

func hasSecretFilenameHint(lowerName string) bool {
	for _, kw := range []string{"secret", "token", "key", "cred", "auth", "env", "password"} {
		if strings.Contains(lowerName, kw) {
			return true
		}
	}
	return false
}

func replaceCachedRepos(db *store.DB, records []RepoRecord) error {
	if db == nil || db.SQL() == nil {
		return fmt.Errorf("database is not open")
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM repos`); err != nil {
		return err
	}

	stmt, err := tx.Prepare(
		`INSERT INTO repos(id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		locals := r.UntrackedLocals
		if locals == nil {
			locals = []LocalFileEntry{}
		}
		localsJSON, err := json.Marshal(locals)
		if err != nil {
			return err
		}
		dirtyInt := 0
		if r.Dirty {
			dirtyInt = 1
		}
		if _, err := stmt.Exec(
			r.ID, r.Name, r.Zone, r.Forge, r.RemoteURL, r.LocalPath,
			r.Branch, dirtyInt, r.Ahead, r.Behind, string(localsJSON), r.ScannedAt,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func ListCachedRepos(db *store.DB) ([]RepoRecord, error) {
	if db == nil || db.SQL() == nil {
		return nil, fmt.Errorf("database is not open")
	}
	rows, err := db.SQL().Query(
		`SELECT id, name, zone, forge, remote_url, local_path, branch, dirty, ahead, behind, untracked_locals_json, scanned_at
		 FROM repos
		 ORDER BY zone ASC, name ASC, local_path ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RepoRecord
	for rows.Next() {
		var r RepoRecord
		var dirtyInt int
		var localsRaw string
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Zone, &r.Forge, &r.RemoteURL, &r.LocalPath,
			&r.Branch, &dirtyInt, &r.Ahead, &r.Behind, &localsRaw, &r.ScannedAt,
		); err != nil {
			return nil, err
		}
		r.Dirty = dirtyInt != 0
		if strings.TrimSpace(localsRaw) != "" {
			var locals []LocalFileEntry
			if err := json.Unmarshal([]byte(localsRaw), &locals); err == nil && len(locals) > 0 {
				r.UntrackedLocals = locals
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func FuzzyMatchRepos(repos []RepoRecord, query string) []RepoRecord {
	tokens := fuzzy.ParseQuery(query)
	if len(tokens) == 0 {
		out := make([]RepoRecord, len(repos))
		copy(out, repos)
		return out
	}

	type scoredRepo struct {
		repo         RepoRecord
		score        int
		primaryScore int
		span         int
	}

	matches := make([]scoredRepo, 0, len(repos))
	for _, r := range repos {
		fields := repoSearchFields(r)
		totalScore := 0
		totalPrimary := 0
		totalSpan := 0
		ok := true

		for _, tok := range tokens {
			if tok.Negate {
				matchedNeg := false
				for _, f := range fields {
					if fuzzy.MatchToken(f.text, tok, f.scheme, false).Matched {
						matchedNeg = true
						break
					}
				}
				if matchedNeg {
					ok = false
					break
				}
				continue
			}

			best := 0
			pScore := 0
			bestSpan := 0
			matched := false
			for _, f := range fields {
				res := fuzzy.MatchToken(f.text, tok, f.scheme, false)
				if !res.Matched {
					continue
				}
				adj := res.Score + f.boost
				span := res.End - res.Start + 1
				if f.isPrimary && adj > pScore {
					pScore = adj
				}
				if !matched || adj > best || (adj == best && (f.isPrimary || span < bestSpan)) {
					best = adj
					bestSpan = span
					matched = true
				}
			}
			if !matched {
				ok = false
				break
			}
			totalScore += best
			totalPrimary += pScore
			totalSpan += bestSpan
		}
		if ok {
			matches = append(matches, scoredRepo{
				repo:         r,
				score:        totalScore,
				primaryScore: totalPrimary,
				span:         totalSpan,
			})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.primaryScore != b.primaryScore {
			return a.primaryScore > b.primaryScore
		}
		if a.span != b.span {
			return a.span < b.span
		}
		if len(a.repo.Name) != len(b.repo.Name) {
			return len(a.repo.Name) < len(b.repo.Name)
		}
		if a.repo.Name != b.repo.Name {
			return a.repo.Name < b.repo.Name
		}
		if a.repo.Zone != b.repo.Zone {
			return a.repo.Zone < b.repo.Zone
		}
		return a.repo.LocalPath < b.repo.LocalPath
	})

	out := make([]RepoRecord, len(matches))
	for i, m := range matches {
		out[i] = m.repo
	}
	return out
}

type repoSearchField struct {
	text      string
	scheme    fuzzy.Scheme
	boost     int
	isPrimary bool
}

func repoSearchFields(r RepoRecord) []repoSearchField {
	fields := make([]repoSearchField, 0, 6)
	if r.Name != "" {
		fields = append(fields, repoSearchField{
			text:      r.Name,
			scheme:    fuzzy.SchemeDefault,
			boost:     32,
			isPrimary: true,
		})
	}
	if r.Zone != "" && r.Name != "" {
		fields = append(fields, repoSearchField{
			text:   r.Zone + "/" + r.Name,
			scheme: fuzzy.SchemePath,
			boost:  0,
		})
	}
	if rel := repoDisplayRelPath(r); rel != "" && rel != r.Name {
		fields = append(fields, repoSearchField{
			text:   rel,
			scheme: fuzzy.SchemePath,
			boost:  -4,
		})
	}
	if r.Zone != "" {
		fields = append(fields, repoSearchField{
			text:   r.Zone,
			scheme: fuzzy.SchemeDefault,
			boost:  -20,
		})
	}
	if r.Branch != "" {
		fields = append(fields, repoSearchField{
			text:   r.Branch,
			scheme: fuzzy.SchemePath,
			boost:  -24,
		})
	}
	if r.Forge != "" {
		fields = append(fields, repoSearchField{
			text:   r.Forge,
			scheme: fuzzy.SchemeDefault,
			boost:  -28,
		})
	}
	return fields
}

func repoDisplayRelPath(r RepoRecord) string {
	p := filepath.ToSlash(filepath.Clean(r.LocalPath))
	if r.Zone != "" {
		marker := "/" + r.Zone + "/"
		if idx := strings.LastIndex(p, marker); idx >= 0 {
			return p[idx+1:]
		}
	}
	if idx := strings.LastIndex(p, "/code/"); idx >= 0 {
		prefix := p[:idx]
		if prevSlash := strings.LastIndex(prefix, "/"); prevSlash >= 0 {
			return p[prevSlash+1:]
		}
		return p[idx+1:]
	}
	if r.Zone != "" && r.Name != "" {
		return r.Zone + "/" + r.Name
	}
	return r.Name
}
